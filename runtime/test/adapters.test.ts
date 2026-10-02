import test from "node:test";
import assert from "node:assert/strict";
import {
  codexOptions,
  commonOptions,
  isolatedEnvironment,
  promptFor,
  instructions,
  sdkText,
  type RunRequest,
} from "../src/adapters.js";

test("malformed SDK text fails before it reaches PostgreSQL or the event stream", () => {
  assert.throws(() => sdkText('echo({"text":"marker"})\0\0'), /malformed text/);
  assert.equal(sdkText("中文 😀\nordinary text"), "中文 😀\nordinary text");
  assert.equal(sdkText(String.raw`\u0000`), String.raw`\u0000`);
});
const input: RunRequest = {
  runId: "test",
  sessionId: "session",
  config: { provider: "codex", model: "fixture", maxSteps: 4 },
  persona: {
    systemPrompt: "SECRETARY",
    examples: [{ role: "user", content: "EXAMPLE" }],
  },
  history: [{ role: "user", content: "HISTORY" }],
  prompt: "CURRENT",
  apiKey: "KEY",
  gatewayUrl: "http://core/internal/mcp",
  gatewayToken: "RUN_TOKEN",
};
test("fresh SDK sessions receive common history; native resume only receives the new input", () => {
  assert.match(promptFor(input), /EXAMPLE/);
  assert.match(promptFor(input), /HISTORY/);
  assert.doesNotMatch(
    promptFor({ ...input, nativeId: "native" }),
    /HISTORY|EXAMPLE/,
  );
  assert.match(promptFor({ ...input, nativeId: "native" }), /CURRENT/);
});
test("SDK options isolate credentials and route tools through the shared MCP endpoint", () => {
  process.env.SECRETARY_TEST_PRIVATE_VALUE = "do-not-inherit";
  const env = isolatedEnvironment("/isolated");
  assert.equal(env.HOME, "/isolated");
  assert.equal(env.SECRETARY_TEST_PRIVATE_VALUE, undefined);
  delete process.env.SECRETARY_TEST_PRIVATE_VALUE;
  const codex = codexOptions(input, "/isolated");
  assert.equal(codex.apiKey, "KEY");
  assert.equal(codex.env?.SECRETARY_MCP_TOKEN, "RUN_TOKEN");
  assert.equal((codex.config?.features as any).shell_tool, false);
  assert.equal(
    (codex.config?.mcp_servers as any).secretary.url,
    input.gatewayUrl,
  );
  assert.doesNotMatch(JSON.stringify(codex.config), /RUN_TOKEN|\"KEY\"/);
  for (const provider of ["claude", "codebuddy"] as const) {
    const options = commonOptions(
      { ...input, config: { ...input.config, provider } },
      "/work",
      "/isolated",
      new AbortController(),
    );
    assert.equal(
      options.mcpServers.secretary.headers.Authorization,
      "Bearer RUN_TOKEN",
    );
    assert.equal(options.maxTurns, 4);
    assert.deepEqual(options.settingSources, []);
    assert.equal(
      options.mcpServers.secretary.alwaysLoad,
      provider === "codebuddy" ? true : undefined,
    );
  }
  assert.match(instructions(input), /SECRETARY/);
});
