import test from "node:test";
import assert from "node:assert/strict";
import {
  codexOptions,
  commonOptions,
  isolatedEnvironment,
  promptFor,
  instructions,
  sdkText,
  secretaryToolPermission,
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

test("SDK permission callback only permits valid secretary MCP tool names", async () => {
  const args = { text: "echo fixture" };
  assert.deepEqual(
    await secretaryToolPermission("mcp__secretary__example__echo", args),
    {
      behavior: "allow",
      updatedInput: args,
    },
  );
  for (const name of [
    "Bash",
    "PowerShell",
    "REPL",
    "SendMessage",
    "WebFetch",
    "mcp__other__echo",
    "mcp__secretary__",
    "mcp__secretary__echo\n",
    "mcp__secretary_fake__echo",
  ]) {
    assert.equal(
      (await secretaryToolPermission(name, args)).behavior,
      "deny",
      name,
    );
  }
});

test("observed CodeBuddy native tools are explicitly denied without auto-allow rules", async () => {
  const options = commonOptions(
    { ...input, config: { ...input.config, provider: "codebuddy" } },
    "/work",
    "/isolated",
    new AbortController(),
  );
  assert.equal("allowedTools" in options, false);
  for (const name of [
    "PowerShell",
    "REPL",
    "SendMessage",
    "SendUserMessage",
    "ImageGen",
    "VideoGen",
    "AudioTranscribe",
    "CronCreate",
    "WeChatReply",
    "WeComReply",
    "PushNotification",
    "ComputerUse",
    "A2ASendMessage",
    "MessageColleague",
    "SpeakInChannel",
    "Workflow",
    "ToolSearch",
  ]) {
    assert.ok(options.disallowedTools.includes(name), name);
    assert.equal((await secretaryToolPermission(name, {})).behavior, "deny");
  }
  assert.ok(
    options.disallowedTools.every(
      (name) => !name.startsWith("mcp__secretary__"),
    ),
  );
});
