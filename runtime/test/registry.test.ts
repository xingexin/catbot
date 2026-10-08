import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm, stat, readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { RunRequest, RunEvent, SDKProvider } from "../src/contracts.js";
import { ProviderRegistry, providerRegistry } from "../src/registry.js";
import { execute } from "../src/execution.js";

const input: RunRequest = {
  runId: "run-1",
  sessionId: "session-1",
  config: { provider: "codebuddy", model: "fixture", maxSteps: 3 },
  persona: { systemPrompt: "fixture persona" },
  history: [],
  prompt: "hello",
  gatewayUrl: "http://core/internal/mcp",
  gatewayToken: "fixture-token",
};

test("registry keeps the public provider order, rejects duplicate names, and fails explicitly for unknown SDKs", () => {
  assert.deepEqual(providerRegistry.names(), ["codebuddy", "claude", "codex"]);
  const names = providerRegistry.names();
  names.reverse();
  assert.deepEqual(providerRegistry.names(), ["codebuddy", "claude", "codex"]);
  assert.throws(() => providerRegistry.get("missing"), /Unsupported Agent SDK/);
  const fixture = { name: "codebuddy" as const, execute: async () => {} };
  assert.throws(
    () => new ProviderRegistry([fixture, fixture]),
    /Duplicate Agent SDK provider/,
  );
});

test("execution selects registered behavior with isolated persistent paths and forwards cancellation and events unchanged", async () => {
  const root = await mkdtemp(join(tmpdir(), "catbot-registry-"));
  const controller = new AbortController();
  const events: RunEvent[] = [];
  const seen: { home: string; cwd: string }[] = [];
  const replacement: SDKProvider = {
    name: "codebuddy",
    async execute(request, context) {
      assert.equal(request, input);
      assert.equal(context.controller, controller);
      assert.equal((await stat(context.home)).mode & 0o777, 0o700);
      assert.equal((await stat(context.cwd)).mode & 0o777, 0o700);
      seen.push(context);
      await context.emit({
        type: "completed",
        data: { text: "from replacement" },
      });
    },
  };
  try {
    const registry = new ProviderRegistry([replacement]);
    await execute(
      input,
      root,
      controller,
      async (event) => {
        events.push(event);
      },
      registry,
    );
    assert.equal(seen[0].home, join(root, "homes", "codebuddy"));
    assert.equal(
      seen[0].cwd,
      join(root, "workspaces", "codebuddy", "session-1"),
    );
    assert.deepEqual(events, [
      { type: "completed", data: { text: "from replacement" } },
    ]);
    controller.abort(new Error("fixture cancellation"));
    const cancelled = new ProviderRegistry([
      {
        name: "codebuddy",
        async execute(_, context) {
          context.controller.signal.throwIfAborted();
        },
      },
    ]);
    await assert.rejects(
      execute(input, root, controller, async () => {}, cancelled),
      /fixture cancellation/,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("invalid identifiers and unknown providers fail before creating SDK directories or invoking a provider", async () => {
  const root = await mkdtemp(join(tmpdir(), "catbot-invalid-"));
  let calls = 0;
  const registry = new ProviderRegistry([
    {
      name: "codebuddy",
      async execute() {
        calls++;
      },
    },
  ]);
  try {
    for (const invalid of [
      { ...input, runId: "../escape" },
      { ...input, sessionId: "../escape" },
    ]) {
      await assert.rejects(
        execute(invalid, root, new AbortController(), async () => {}, registry),
        /Invalid execution identifier/,
      );
    }
    await assert.rejects(
      execute(
        { ...input, config: { ...input.config, provider: "codex" } },
        root,
        new AbortController(),
        async () => {},
        registry,
      ),
      /Unsupported Agent SDK/,
    );
    assert.equal(calls, 0);
    assert.deepEqual(await readdir(root), []);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
