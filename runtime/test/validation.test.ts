import test from "node:test";
import assert from "node:assert/strict";
import { validRun, safeEvent } from "../src/validation.js";
const input = {
  runId: "one-run",
  sessionId: "one-session",
  config: { provider: "codex", model: "example", maxSteps: 12 },
  persona: { systemPrompt: "秘书" },
  history: [],
  prompt: "你好",
  gatewayToken: "run-token",
  gatewayUrl: "http://backend:8080/internal/mcp",
};

test("runtime rejects invalid input before reserving a run or starting an SDK", () => {
  assert.equal(validRun(input), true);
  assert.equal(
    validRun({
      ...input,
      history: null,
      persona: { systemPrompt: "秘书", examples: null },
    }),
    true,
  );
  for (const bad of [
    null,
    [],
    {},
    { ...input, runId: undefined },
    { ...input, sessionId: undefined },
    { ...input, history: "invalid" },
    { ...input, history: [null] },
    { ...input, config: { ...input.config, timeoutSec: -1 } },
    { ...input, config: { ...input.config, timeoutSec: Infinity } },
    { ...input, config: { ...input.config, maxSteps: 0 } },
    { ...input, prompt: "bad\0text" },
    { ...input, gatewayUrl: "file:///tmp" },
  ])
    assert.equal(validRun(bad), false);
});

test("SDK event boundaries reject actual NUL and oversized output and redact encoded keys", () => {
  const event = {
    type: "text.delta",
    data: { text: 'key with "quote" and\\slash' },
  };
  assert.doesNotMatch(
    safeEvent(event, ['key with "quote" and\\slash']).json,
    /quote|slash/,
  );
  assert.throws(
    () => safeEvent({ type: "error", data: { text: "bad\0" } }, []),
    /NUL/,
  );
  assert.doesNotThrow(() =>
    safeEvent({ type: "text.delta", data: { text: String.raw`\u0000` } }, []),
  );
  assert.throws(
    () =>
      safeEvent({ type: "completed", data: { text: "文".repeat(400000) } }, []),
    /output limit/,
  );
});
