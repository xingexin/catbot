import test from "node:test";
import assert from "node:assert/strict";
import {
  APIProviderPreset,
  apiEndpointKey,
  applyAPIProvider,
  applyModelEndpoint,
  modelOriginChanged,
  inferAPIProvider,
} from "./model-api-presets";
import {
  ModelLookup,
  ModelListSource,
  modelConnectionKey,
} from "./model-config";

const draft = {
  kind: "api",
  protocol: "openai-chat",
  baseUrl: "https://custom.example/v1",
  credentialId: "keep-key",
  model: "my-model",
  name: "my-config",
  capabilities: { tools: false, images: true },
  maxTokens: 901,
};
const expected = [
  {
    preset: APIProviderPreset.OpenAI,
    protocol: "openai-responses",
    baseUrl: "https://api.openai.com/v1",
  },
  {
    preset: APIProviderPreset.Anthropic,
    protocol: "anthropic",
    baseUrl: "https://api.anthropic.com",
  },
  {
    preset: APIProviderPreset.DeepSeek,
    protocol: "openai-chat",
    baseUrl: "https://api.deepseek.com",
  },
  {
    preset: APIProviderPreset.Kimi,
    protocol: "openai-chat",
    baseUrl: "https://api.moonshot.cn/v1",
  },
  {
    preset: APIProviderPreset.GLM,
    protocol: "openai-chat",
    baseUrl: "https://open.bigmodel.cn/api/paas/v4",
  },
];

test("API presets fill confirmed endpoints and clear cross-origin keys while preserving model and capabilities", () => {
  for (const row of expected) {
    const result = applyAPIProvider(draft, row.preset);
    assert.deepEqual(result, {
      ...draft,
      protocol: row.protocol,
      baseUrl: row.baseUrl,
      credentialId: undefined,
    });
    assert.equal(inferAPIProvider(result), row.preset);
    assert.equal(Object.hasOwn(result, "preset"), false);
  }
  assert.equal(draft.baseUrl, "https://custom.example/v1");
  assert.equal(draft.model, "my-model");
});

test("custom selection preserves the entire draft and unknown selections never silently pick a provider", () => {
  assert.deepEqual(applyAPIProvider(draft, APIProviderPreset.Custom), draft);
  assert.equal(inferAPIProvider(draft), APIProviderPreset.Custom);
  assert.throws(
    () => applyAPIProvider(draft, APIProviderPreset.Unknown),
    /有效/,
  );
  assert.throws(() => applyAPIProvider(draft, 99 as APIProviderPreset), /有效/);
  assert.throws(
    () => applyAPIProvider({ ...draft, kind: "sdk" }, APIProviderPreset.OpenAI),
    /直连/,
  );
  assert.equal(
    inferAPIProvider({ ...draft, kind: "sdk" }),
    APIProviderPreset.Unknown,
  );
});

test("existing official configurations match normalized endpoint and compatible protocol without rewriting", () => {
  const existing = {
    ...draft,
    protocol: "openai-chat",
    baseUrl: "https://API.OPENAI.COM:443/v1/",
  };
  assert.equal(inferAPIProvider(existing), APIProviderPreset.OpenAI);
  assert.equal(existing.protocol, "openai-chat");
  assert.equal(existing.baseUrl, "https://API.OPENAI.COM:443/v1/");
  assert.equal(
    inferAPIProvider({
      ...draft,
      protocol: "anthropic",
      baseUrl: "https://api.anthropic.com/v1/",
    }),
    APIProviderPreset.Anthropic,
  );
  for (const baseUrl of [
    "https://api.openai.com.example/v1",
    "https://custom.example/api.openai.com/v1",
    "http://api.openai.com/v1",
    "https://api.openai.com/v1?key=x",
    "https://user@api.openai.com/v1",
    "https://api.openai.com/other",
    "not-a-url",
  ])
    assert.equal(
      inferAPIProvider({ ...draft, baseUrl }),
      APIProviderPreset.Custom,
    );
  assert.equal(
    inferAPIProvider({
      ...draft,
      protocol: "anthropic",
      baseUrl: "https://api.openai.com/v1",
    }),
    APIProviderPreset.Custom,
  );
});

test("API preset changes invalidate pending model catalogs but leave hand-written model intact", async () => {
  const lookup = new ModelLookup();
  let current = { ...draft };
  let resolve!: (value: unknown) => void;
  const wait = new Promise<unknown>((yes) => {
    resolve = yes;
  });
  const pending = lookup.load(
    current,
    () => modelConnectionKey(current),
    () => wait,
  );
  const oldKey = apiEndpointKey(current);
  current = applyAPIProvider(current, APIProviderPreset.Kimi) as typeof draft;
  assert.notEqual(apiEndpointKey(current), oldKey);
  resolve({
    models: [{ id: "old-model", name: "Old" }],
    source: ModelListSource.Provider,
  });
  assert.equal(await pending, undefined);
  assert.equal(current.model, draft.model);
  assert.equal(current.credentialId, undefined);
});

test("same-origin protocol and path changes retain the selected credential", () => {
  const existing = {
    ...draft,
    baseUrl: "https://api.openai.com:443/v1/",
    protocol: "openai-chat",
  };
  const selected = applyAPIProvider(existing, APIProviderPreset.OpenAI);
  assert.equal(selected.credentialId, draft.credentialId);
  assert.equal(selected.protocol, "openai-responses");
  assert.equal(
    applyModelEndpoint(existing, "https://API.OPENAI.COM/other").credentialId,
    draft.credentialId,
  );
  assert.equal(
    modelOriginChanged(existing.baseUrl, "https://api.openai.com/v2"),
    false,
  );
});

test("manual host, scheme, port or invalid origin changes clear credentials once without clearing other fields", () => {
  for (const baseUrl of [
    "https://other.example/v1",
    "http://custom.example/v1",
    "https://custom.example:8443/v1",
    "",
    "not-a-url",
  ]) {
    const next = applyModelEndpoint(draft, baseUrl);
    assert.equal(next.credentialId, undefined);
    assert.equal(next.model, draft.model);
    assert.deepEqual(next.capabilities, draft.capabilities);
  }
  for (const baseUrl of [undefined, "", "invalid", "file:///tmp/model"]) {
    assert.equal(
      applyAPIProvider({ ...draft, baseUrl }, APIProviderPreset.Kimi)
        .credentialId,
      undefined,
    );
  }
  const switched = applyModelEndpoint(draft, "https://other.example/v1");
  const newKeySelected = { ...switched, credentialId: "new-host-key" };
  assert.equal(
    applyModelEndpoint(newKeySelected, "https://other.example/v2").credentialId,
    "new-host-key",
  );
  assert.deepEqual(applyAPIProvider(draft, APIProviderPreset.Custom), draft);
});
