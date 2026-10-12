import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { setImmediate as nextTick } from "node:timers/promises";
import { query, type Options, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import { discoverClaudeModels } from "../src/providers/claude-models.js";
import { createClaudeProvider } from "../src/providers/claude.js";
import { discoverModels, modelDiscoveryError } from "../src/models.js";
import { ProviderRegistry } from "../src/registry.js";

const request = (key = "fixture-claude-key") => ({
  config: { provider: "claude" as const, baseUrl: "https://claude.invalid" },
  apiKey: key,
});
const context = () => ({
  home: "/fixture/home",
  cwd: "/fixture/work",
  signal: new AbortController().signal,
});
const model = { value: "fixture-claude", displayName: "Fixture Claude", description: "metadata" };

test("Claude concurrent catalogs consume no user input and isolate credentials and options", async () => {
  const ambient = process.env.ANTHROPIC_API_KEY;
  const captured: Options[] = [];
  const readers: Promise<IteratorResult<SDKUserMessage>>[] = [];
  let closed = 0;
  const results = await Promise.all(["first-key", "second-key"].map((key) =>
    discoverClaudeModels(request(key), context(), ({ prompt, options }) => {
      captured.push(options);
      let inputResolved = false;
      readers.push(prompt[Symbol.asyncIterator]().next().then((result) => {
        inputResolved = true;
        return result;
      }));
      return {
        async supportedModels() {
          await nextTick();
          assert.equal(inputResolved, false, "stdin must remain open without a user message");
          return [model];
        },
        close() { closed++; },
      };
    }),
  ));
  assert.equal(closed, 2);
  assert.deepEqual(await Promise.all(readers), [
    { value: undefined, done: true }, { value: undefined, done: true },
  ]);
  assert.deepEqual(results[0], [{ id: model.value, name: model.displayName, description: model.description }]);
  assert.deepEqual(captured.map((options) => options.env?.ANTHROPIC_API_KEY), ["first-key", "second-key"]);
  assert.equal(process.env.ANTHROPIC_API_KEY, ambient);
  for (const options of captured) {
    assert.equal(options.env?.HOME, "/fixture/home");
    assert.equal(options.env?.CLAUDE_CONFIG_DIR, "/fixture/home/.claude");
    assert.equal(options.env?.ANTHROPIC_BASE_URL, "https://claude.invalid");
    assert.equal(options.env?.ANTHROPIC_AUTH_TOKEN, undefined);
    assert.equal(options.env?.CLAUDE_CODE_OAUTH_TOKEN, undefined);
    assert.equal(options.cwd, "/fixture/work");
    assert.deepEqual(options.tools, []);
    assert.deepEqual(options.settingSources, []);
    assert.deepEqual(options.mcpServers, {});
    assert.equal(options.strictMcpConfig, true);
    assert.equal(options.persistSession, false);
    assert.equal(options.resume, undefined);
    assert.equal(options.model, undefined);
    assert.equal(options.abortController?.signal.aborted, true);
    assert.equal((await options.canUseTool!("Bash", {}, { signal: new AbortController().signal, toolUseID: "test" })).behavior, "deny");
  }
});

test("Claude errors, timeout, disconnection and startup failures release input and close once", async () => {
  for (const mode of ["failure", "timeout", "disconnect", "startup"] as const) {
    const controller = new AbortController();
    let closed = 0;
    let reader: Promise<IteratorResult<SDKUserMessage>> | undefined;
    const pending = discoverClaudeModels(request(), { ...context(), signal: controller.signal }, ({ prompt }) => {
      reader = prompt[Symbol.asyncIterator]().next();
      if (mode === "startup") throw new Error("startup failure");
      return {
        async supportedModels() {
          if (mode === "failure") throw new Error("fixture-claude-key rejected");
          return new Promise<never>(() => {});
        },
        close() { closed++; },
      };
    });
    if (mode === "timeout") setImmediate(() => controller.abort(new Error("lookup timed out")));
    if (mode === "disconnect") controller.abort(new Error("client disconnected"));
    await assert.rejects(pending, /rejected|timed out|disconnected|startup failure/);
    assert.equal(closed, mode === "startup" ? 0 : 1);
    assert.deepEqual(await reader, { value: undefined, done: true });
  }
  const controller = new AbortController();
  controller.abort(new Error("already cancelled"));
  await assert.rejects(discoverClaudeModels(request(), { ...context(), signal: controller.signal }, () => {
    assert.fail("pre-cancelled query must not start");
  }), /already cancelled/);
  assert.equal(modelDiscoveryError(new Error("fixture-claude-key rejected"), [request().apiKey]), "[REDACTED] rejected");
});

test("Claude provider cleans isolated directories without creating runs", async () => {
  const root = await mkdtemp(join(tmpdir(), "catbot-claude-discovery-"));
  try {
    for (const fail of [false, true]) {
      const provider = createClaudeProvider(undefined, ({ options }) => ({
        async supportedModels() {
          assert.match(options.cwd!, /model-discovery\/request-.*\/workspace$/);
          assert.match(options.env!.HOME!, /model-discovery\/request-.*\/home$/);
          if (fail) throw new Error("fixture failure");
          return [model];
        },
        close() {},
      }));
      const pending = discoverModels(request(), root, new AbortController().signal, new ProviderRegistry([provider]));
      if (fail) await assert.rejects(pending, /fixture failure/);
      else assert.equal((await pending).models[0].id, model.value);
      assert.deepEqual(await readdir(join(root, "model-discovery")), []);
      assert.deepEqual(await readdir(root), ["model-discovery"]);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("real Claude SDK writes only initialize to a child and waits for process exit before cleanup", { timeout: 5000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "catbot-claude-control-"));
  const cli = join(root, "fixture.js"), observed = join(root, "requests.jsonl");
  await writeFile(cli, `
    const fs = require('node:fs');
    const rl = require('node:readline').createInterface({input:process.stdin});
    rl.on('line', line => {
      const message = JSON.parse(line);
      fs.appendFileSync(${JSON.stringify(observed)}, JSON.stringify(message)+'\\n');
      process.stdout.write(JSON.stringify({type:'control_response',response:{subtype:'success',request_id:message.request_id,response:{commands:[],models:[${JSON.stringify(model)}]}}})+'\\n');
    });
  `);
  let exited = false;
  try {
    const models = await discoverClaudeModels(request(), { home: root, cwd: root, signal: new AbortController().signal }, (input) => query({
      ...input,
      options: {
        ...input.options,
        pathToClaudeCodeExecutable: cli,
        spawnClaudeCodeProcess(options) {
          const process = input.options.spawnClaudeCodeProcess!(options);
          process.once("exit", () => { exited = true; });
          return process;
        },
      },
    }));
    assert.equal(exited, true);
    assert.equal(models[0].id, model.value);
    const requests = (await readFile(observed, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
    assert.deepEqual(requests.map((message) => [message.type, message.request.subtype]), [["control_request", "initialize"]]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("bundled Claude CLI reads catalog metadata without a real key or user input", { skip: process.env.CATBOT_TEST_CLAUDE_METADATA !== "1", timeout: 25000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "catbot-claude-bundled-"));
  try {
    const models = await discoverClaudeModels({ config: { provider: "claude", baseUrl: "http://127.0.0.1:1" }, apiKey: "fixture-invalid-metadata-only" }, {
      home: root,
      cwd: root,
      signal: AbortSignal.timeout(18000),
    });
    // A CLI catalog is not proof of authentication or inference availability.
    assert.ok(models.length > 0);
    assert.ok(models.every((model) => model.id && model.name));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});


test("Claude cancellation returns promptly and reaps a child that ignores SIGTERM", { timeout: 5000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "catbot-claude-cancel-"));
  const cli = join(root, "fixture.js"), ready = join(root, "ready");
  await writeFile(cli, `
    const fs = require('node:fs');
    process.on('SIGTERM', () => {});
    require('node:readline').createInterface({input:process.stdin}).on('line', line => {
      fs.writeFileSync(${JSON.stringify(ready)}, line);
    });
    setInterval(() => {}, 1000);
  `);
  const controller = new AbortController();
  let cleanup: Promise<void> | undefined;
  let exited = false;
  try {
    const pending = discoverClaudeModels(request(), {
      home: root,
      cwd: root,
      signal: controller.signal,
      deferCleanup: (work) => { cleanup = work; },
    }, (input) => query({
      ...input,
      options: {
        ...input.options,
        pathToClaudeCodeExecutable: cli,
        spawnClaudeCodeProcess(options) {
          const process = input.options.spawnClaudeCodeProcess!(options);
          process.once("exit", () => { exited = true; });
          return process;
        },
      },
    }));
    const rejected = assert.rejects(pending, /metadata cancelled/);
    let initialized = false;
    for (let attempt = 0; attempt < 100; attempt++) {
      try {
        const message = JSON.parse(await readFile(ready, "utf8"));
        assert.equal(message.type, "control_request");
        assert.equal(message.request.subtype, "initialize");
        initialized = true;
        break;
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      }
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    assert.equal(initialized, true);
    controller.abort(new Error("metadata cancelled"));
    await rejected;
    assert.ok(cleanup, "cancel should leave process cleanup with the directory owner");
    await cleanup;
    assert.equal(exited, true, "even a stubborn process must exit");
  } finally {
    controller.abort();
    await cleanup;
    await rm(root, { recursive: true, force: true });
  }
});
