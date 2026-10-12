import test from "node:test";
import assert from "node:assert/strict";
import {
  CodeBuddyEnvironment,
  ExecutionKind,
  ModelListSource,
  ModelLookup,
  canListModels,
  executionKind,
  initialModelConfig,
  modelConnectionKey,
  modelAuthContextChanged,
  normalizeModelConfig,
  readAvailableModels,
} from "./model-config";

const codeBuddy = {
  kind: "sdk",
  provider: "codebuddy",
  credentialId: "ioa-key",
  codebuddyEnvironment: CodeBuddyEnvironment.IOA,
};
const answer = (id: string) => ({
  models: [{ id, name: id }],
  source: ModelListSource.Provider,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

test("legacy CodeBuddy editing displays inherited environment and preserves every existing draft field", () => {
  const saved = {
    ...codeBuddy,
    codebuddyEnvironment: CodeBuddyEnvironment.Unknown,
    name: "saved",
    model: "custom",
    capabilities: { tools: false, stream: false },
    maxSteps: 30,
    baseUrl: "https://endpoint",
    extraSetting: 7,
  };
  const draft = initialModelConfig(saved);
  assert.equal(draft.codebuddyEnvironment, CodeBuddyEnvironment.Inherit);
  assert.equal(draft.model, saved.model);
  assert.equal(draft.maxSteps, saved.maxSteps);
  assert.equal(draft.extraSetting, saved.extraSetting);
  assert.deepEqual(draft.capabilities, {
    tools: false,
    stream: false,
    images: false,
    resume: true,
  });
  assert.equal(saved.codebuddyEnvironment, CodeBuddyEnvironment.Unknown);
  assert.equal(
    initialModelConfig({ ...codeBuddy }).codebuddyEnvironment,
    CodeBuddyEnvironment.IOA,
  );
});

test("save boundary removes CodeBuddy-only configuration after switching strategy or provider", () => {
  for (const config of [
    { ...codeBuddy, provider: "claude" },
    { ...codeBuddy, provider: "codex" },
    { ...codeBuddy, kind: "api", protocol: "openai-chat" },
  ])
    assert.equal(
      Object.hasOwn(normalizeModelConfig(config), "codebuddyEnvironment"),
      false,
    );
  assert.equal(
    normalizeModelConfig(codeBuddy).codebuddyEnvironment,
    CodeBuddyEnvironment.IOA,
  );
  assert.throws(
    () => normalizeModelConfig({ ...codeBuddy, codebuddyEnvironment: 55 }),
    /未知/,
  );
  assert.throws(() => normalizeModelConfig({ kind: "unknown" }), /接入方式/);
  assert.throws(
    () => normalizeModelConfig({ kind: "sdk", provider: "unknown" }),
    /SDK/,
  );
  assert.equal(executionKind("unknown"), ExecutionKind.Unknown);
});

test("discovery supports API protocols, CodeBuddy and Claude while Codex stays manual", () => {
  for (const protocol of ["openai-chat", "openai-responses", "anthropic"])
    assert.equal(canListModels({ kind: "api", protocol }), true);
  assert.equal(canListModels(codeBuddy), true);
  assert.equal(canListModels({ ...codeBuddy, provider: "claude" }), true);
  assert.equal(canListModels({ ...codeBuddy, provider: "codex" }), false);
  assert.equal(canListModels({ kind: "api", protocol: "unknown" }), false);
});

test("model discovery sends credential reference and explicit environment without requiring name or model", async () => {
  const lookup = new ModelLookup();
  let received: unknown;
  const original = { ...codeBuddy };
  const result = await lookup.load(
    original,
    () => modelConnectionKey(original),
    async (config) => {
      received = config;
      return answer("server-model");
    },
  );
  assert.deepEqual(received, codeBuddy);
  assert.deepEqual(result, answer("server-model"));
  assert.equal(Object.hasOwn(original, "model"), false);
});

test("connection changes reject stale model lists before the invalidation effect runs", async () => {
  for (const change of [
    { codebuddyEnvironment: CodeBuddyEnvironment.CN },
    { baseUrl: "https://new" },
    { credentialId: "another-key" },
    { kind: "api", protocol: "openai-chat" },
    { provider: "claude" },
  ]) {
    const lookup = new ModelLookup();
    let current = { ...codeBuddy };
    const pending = deferred<unknown>();
    const request = lookup.load(
      current,
      () => modelConnectionKey(current),
      () => pending.promise,
    );
    current = { ...current, ...change };
    pending.resolve(answer("stale"));
    assert.equal(await request, undefined);
  }
});

test("closed editor and newer requests invalidate both late successes and late errors", async () => {
  const lookup = new ModelLookup();
  const old = deferred<unknown>();
  const first = lookup.load(
    codeBuddy,
    () => modelConnectionKey(codeBuddy),
    () => old.promise,
  );
  const second = await lookup.load(
    codeBuddy,
    () => modelConnectionKey(codeBuddy),
    async () => answer("current"),
  );
  old.resolve(answer("stale"));
  assert.deepEqual(second, answer("current"));
  assert.equal(await first, undefined);
  const closed = deferred<unknown>();
  const closing = lookup.load(
    codeBuddy,
    () => modelConnectionKey(codeBuddy),
    () => closed.promise,
  );
  lookup.invalidate();
  closed.reject(new Error("old account unauthorized"));
  assert.equal(await closing, undefined);
});

test("failed or invalid discovery preserves manual model input and never returns a fabricated list", async () => {
  const config = { ...codeBuddy, model: "hand-written" };
  const lookup = new ModelLookup();
  await assert.rejects(
    lookup.load(
      config,
      () => modelConnectionKey(config),
      async () => {
        throw new Error("unauthorized");
      },
    ),
    /unauthorized/,
  );
  assert.equal(config.model, "hand-written");
  assert.throws(
    () => readAvailableModels({ source: ModelListSource.Unknown, models: [] }),
    /格式/,
  );
  assert.throws(
    () =>
      readAvailableModels({
        source: ModelListSource.Provider,
        models: [{ id: "" }],
      }),
    /格式/,
  );
  assert.deepEqual(
    readAvailableModels({ source: ModelListSource.Provider, models: [] })
      .models,
    [],
  );
  assert.deepEqual(
    readAvailableModels({
      source: ModelListSource.Provider,
      models: [
        { id: "one", name: "One" },
        { id: "one", name: "Duplicate" },
      ],
    }).models.map((model) => model.id),
    ["one"],
  );
});

test("switching SDK providers or execution strategies changes credential context even when Base URL is empty", () => {
  const previous = { ...codeBuddy, baseUrl: "", model: "keep-model" };
  for (const next of [
    { ...previous, provider: "claude" },
    { ...previous, provider: "codex" },
    { ...previous, kind: "api", protocol: "openai-chat" },
  ])
    assert.equal(modelAuthContextChanged(previous, next), true);
  assert.equal(
    modelAuthContextChanged({ kind: "api", protocol: "openai-chat" }, previous),
    true,
  );
  assert.equal(
    modelAuthContextChanged(
      { kind: "sdk", provider: "claude" },
      { kind: "sdk", provider: "codex" },
    ),
    true,
  );
  assert.equal(previous.model, "keep-model");
  assert.equal(previous.baseUrl, "");
});

test("unchanged SDK or API strategy preserves credential context without treating API protocol as a provider", () => {
  assert.equal(modelAuthContextChanged(codeBuddy, { ...codeBuddy }), false);
  assert.equal(
    modelAuthContextChanged(
      { kind: "sdk", provider: "claude" },
      { kind: "sdk", provider: "claude" },
    ),
    false,
  );
  assert.equal(
    modelAuthContextChanged(
      { kind: "api", protocol: "openai-chat" },
      { kind: "api", protocol: "openai-responses" },
    ),
    false,
  );
  assert.equal(
    modelAuthContextChanged({ kind: "sdk", provider: "unknown" }, codeBuddy),
    true,
  );
});
