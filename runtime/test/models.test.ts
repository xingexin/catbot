import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readdir, rm, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  unstable_v2_createSession,
  type SessionOptions,
} from "@tencent-ai/agent-sdk";
import {
  CodeBuddyEnvironment,
  ModelListSource,
  type ModelDiscoveryRequest,
} from "../src/contracts.js";
import { codebuddyOptions } from "../src/providers/codebuddy.js";
import { codebuddyEnvironment } from "../src/providers/codebuddy-environment.js";
import { discoverCodeBuddyModels } from "../src/providers/codebuddy-models.js";
import {
  discoverModels,
  modelDiscoveryError,
  ModelDiscoveryUnsupportedError,
} from "../src/models.js";
import { ProviderRegistry } from "../src/registry.js";
import { validModelDiscovery, validRun } from "../src/validation.js";

function request(
  environment = CodeBuddyEnvironment.IOA,
): ModelDiscoveryRequest {
  return {
    config: { provider: "codebuddy", codebuddyEnvironment: environment },
    apiKey: "fixture-key",
  };
}

const context = () => ({
  home: "/fixture/home",
  cwd: "/fixture/work",
  signal: new AbortController().signal,
});

test("CodeBuddy environments override only the child and retain legacy inheritance", () => {
  const saved = process.env.CODEBUDDY_INTERNET_ENVIRONMENT;
  process.env.CODEBUDDY_INTERNET_ENVIRONMENT = "ioa";
  try {
    for (const [selected, expected, authEnvironment] of [
      [CodeBuddyEnvironment.Unknown, "ioa", undefined],
      [CodeBuddyEnvironment.Inherit, "ioa", undefined],
      [CodeBuddyEnvironment.IOA, "ioa", "ioa"],
      [CodeBuddyEnvironment.CN, "internal", "internal"],
      [CodeBuddyEnvironment.International, undefined, "external"],
    ] as const) {
      const options = codebuddyEnvironment(request(selected), "/fixture/home");
      assert.equal(options.env.CODEBUDDY_INTERNET_ENVIRONMENT, expected);
      assert.equal(options.environment, authEnvironment);
      assert.equal(options.env.CODEBUDDY_API_KEY, "fixture-key");
      assert.equal(process.env.CODEBUDDY_INTERNET_ENVIRONMENT, "ioa");
      assert.ok(Object.hasOwn(options.env, "CODEBUDDY_INTERNET_ENVIRONMENT"));
    }
    assert.equal(
      codebuddyEnvironment(
        { config: { provider: "codebuddy" } },
        "/fixture/home",
      ).env.CODEBUDDY_INTERNET_ENVIRONMENT,
      "ioa",
    );
    assert.throws(
      () => codebuddyEnvironment(request(99), "/fixture/home"),
      /Unsupported/,
    );
  } finally {
    if (saved === undefined) delete process.env.CODEBUDDY_INTERNET_ENVIRONMENT;
    else process.env.CODEBUDDY_INTERNET_ENVIRONMENT = saved;
  }
});

test("execution and model discovery use the same explicit environment and endpoint", async () => {
  const input = {
    ...request(CodeBuddyEnvironment.CN),
    config: {
      ...request(CodeBuddyEnvironment.CN).config,
      baseUrl: "https://models.invalid",
      model: "fixture",
      maxSteps: 1,
    },
    runId: "run",
    sessionId: "session",
    persona: { systemPrompt: "test" },
    history: [],
    prompt: "hello",
    gatewayUrl: "http://core/mcp",
    gatewayToken: "fixture",
  };
  const run = codebuddyOptions(input, {
    ...context(),
    emit: async () => {},
    controller: new AbortController(),
  });
  let captured: SessionOptions | undefined;
  await discoverCodeBuddyModels(input, context(), (options) => {
    captured = options;
    return {
      async connect() {},
      async getAvailableModels() {
        return [];
      },
      close() {},
    };
  });
  assert.equal(
    run.env?.CODEBUDDY_INTERNET_ENVIRONMENT,
    captured?.env?.CODEBUDDY_INTERNET_ENVIRONMENT,
  );
  assert.equal(run.environment, captured?.environment);
  assert.equal(run.env?.CODEBUDDY_BASE_URL, captured?.env?.CODEBUDDY_BASE_URL);
});

test("concurrent model discovery cannot execute a prompt or mix credentials and environments", async () => {
  const captured: SessionOptions[] = [];
  let closed = 0;
  const requests = [
    request(CodeBuddyEnvironment.IOA),
    { ...request(CodeBuddyEnvironment.CN), apiKey: "different-key" },
  ];
  const responses = await Promise.all(
    requests.map((input) =>
      discoverCodeBuddyModels(input, context(), (options) => {
        captured.push(options);
        return {
          async connect() {},
          async getAvailableModels() {
            return [
              {
                modelId: options.environment!,
                name: "Fixture",
                description: "metadata",
              },
            ];
          },
          close() {
            closed++;
          },
          send() {
            throw new Error("Must never generate");
          },
          stream() {
            throw new Error("Must never generate");
          },
        };
      }),
    ),
  );
  assert.equal(closed, 2);
  assert.deepEqual(
    responses.map((models) => models[0].id),
    ["ioa", "internal"],
  );
  assert.deepEqual(
    captured.map((options) => options.env?.CODEBUDDY_API_KEY),
    ["fixture-key", "different-key"],
  );
  for (const options of captured) {
    assert.deepEqual(options.tools, []);
    assert.deepEqual(options.settingSources, []);
    assert.deepEqual(options.mcpServers, {});
    assert.equal(options.strictMcpConfig, true);
    assert.equal(options.persistSession, false);
    assert.equal(options.requestTimeoutMs, undefined);
    assert.equal(options.model, undefined);
    assert.equal(options.cwd, "/fixture/work");
    assert.equal(options.env?.HOME, "/fixture/home");
    assert.equal(options.env?.CODEBUDDY_CONFIG_DIR, "/fixture/home/.codebuddy");
    assert.equal(
      options.env?.CODEBUDDY_PROJECT_CONFIG_DIR,
      "/fixture/work/.codebuddy",
    );
    assert.equal(
      (
        await options.canUseTool!(
          "Bash",
          {},
          { signal: new AbortController().signal, toolUseID: "test" },
        )
      ).behavior,
      "deny",
    );
  }
});

test("SDK errors, timeout, and client cancellation all close metadata sessions exactly once", async () => {
  for (const mode of ["failure", "timeout", "disconnect"] as const) {
    const controller = new AbortController();
    let closed = 0;
    let cleanup: Promise<void> | undefined;
    const pending = discoverCodeBuddyModels(
      request(),
      {
        ...context(),
        signal: controller.signal,
        deferCleanup: (work) => {
          cleanup = work;
        },
      },
      () => ({
        async connect() {},
        async getAvailableModels() {
          if (mode === "failure") throw new Error("upstream failed");
          return new Promise<never>(() => {});
        },
        close() {
          closed++;
        },
      }),
    );
    if (mode === "timeout")
      setTimeout(() => controller.abort(new Error("lookup timed out")), 5);
    else if (mode === "disconnect")
      controller.abort(new Error("client disconnected"));
    await assert.rejects(pending, /failed|timed out|disconnected/);
    await cleanup;
    assert.equal(closed, 1);
  }
  const controller = new AbortController();
  controller.abort(new Error("before lookup"));
  await assert.rejects(
    discoverCodeBuddyModels(
      request(),
      { ...context(), signal: controller.signal },
      () => {
        throw new Error("should not construct");
      },
    ),
    /before lookup/,
  );
});

test("discovery temporary state is removed on success and failure and no run is created", async () => {
  const root = await mkdtemp(join(tmpdir(), "catbot-models-test-"));
  try {
    const observed: string[] = [];
    for (const fail of [false, true]) {
      const registry = new ProviderRegistry([
        {
          name: "codebuddy",
          execute: async () => {
            throw new Error("No execution allowed");
          },
          discoverModels: async (_, context) => {
            observed.push(context.home);
            assert.match(context.home, /request-/);
            assert.equal((await readdir(join(context.home, ".."))).length, 2);
            if (fail) throw new Error("fixture failure");
            return [
              { id: "model-a", name: "A" },
              { id: "model-a", name: "A" },
              { id: "model-b", name: "" },
            ];
          },
        },
      ]);
      const result = discoverModels(
        request(),
        root,
        new AbortController().signal,
        registry,
      );
      if (fail) await assert.rejects(result, /fixture failure/);
      else
        assert.deepEqual(await result, {
          source: ModelListSource.Live,
          models: [
            { id: "model-a", name: "A" },
            { id: "model-b", name: "model-b" },
          ],
        });
      assert.deepEqual(await readdir(join(root, "model-discovery")), []);
    }
    assert.notEqual(observed[0], observed[1]);
    assert.deepEqual(await readdir(root), ["model-discovery"]);
    await assert.rejects(
      discoverModels(
        request(),
        root,
        new AbortController().signal,
        new ProviderRegistry([{ name: "codebuddy", execute: async () => {} }]),
      ),
      ModelDiscoveryUnsupportedError,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("model boundary rejects invalid enum and URL input and sanitizes credential-bearing failures", () => {
  assert.equal(validModelDiscovery(request()), true);
  assert.equal(
    validModelDiscovery({ config: { provider: "codebuddy" } }),
    true,
  );
  for (const codebuddyEnvironment of [null, "ioa", "2", -1, 1.2, 99]) {
    assert.equal(
      validModelDiscovery({
        ...request(),
        config: { ...request().config, codebuddyEnvironment },
      }),
      false,
    );
    assert.equal(
      validRun({
        runId: "r",
        sessionId: "s",
        config: {
          provider: "codebuddy",
          model: "a",
          maxSteps: 1,
          codebuddyEnvironment,
        },
        persona: { systemPrompt: "a" },
        prompt: "hello",
        gatewayToken: "x",
        gatewayUrl: "http://core",
      }),
      false,
    );
  }
  for (const baseUrl of ["file:///etc", "https://user:password@host", "bad"])
    assert.equal(
      validModelDiscovery({
        ...request(),
        config: { ...request().config, baseUrl },
      }),
      false,
    );
  const secret = 'secret-"quoted"';
  assert.equal(
    modelDiscoveryError(new Error("upstream rejected " + secret), [secret]),
    "upstream rejected [REDACTED]",
  );
  assert.equal(
    modelDiscoveryError(new Error("bad\0text"), []),
    "模型列表读取失败",
  );
});

test("cancelling during SDK prewarm waits for spawn before close, skips metadata, and cleans failed starts", async () => {
  for (const failedStart of [false, true]) {
    const controller = new AbortController();
    const calls: string[] = [];
    let start!: () => void;
    const started = new Promise<void>((resolve) => {
      start = resolve;
    });
    let complete!: () => void;
    let cleanup: Promise<void> | undefined;
    const pending = discoverCodeBuddyModels(
      request(),
      {
        ...context(),
        signal: controller.signal,
        deferCleanup: (work) => {
          cleanup = work;
        },
      },
      () => ({
        async connect() {
          calls.push("prewarm");
          start();
          await new Promise<void>((resolve) => {
            complete = resolve;
          });
          calls.push("spawn settled");
          if (failedStart) throw new Error("spawn failed");
        },
        async getAvailableModels() {
          calls.push("metadata");
          return [];
        },
        close() {
          calls.push("close");
        },
      }),
    );
    await started;
    controller.abort(new Error("cancel during prewarm"));
    assert.deepEqual(calls, ["prewarm"]);
    await assert.rejects(pending, /cancel during prewarm/);
    assert.ok(
      cleanup,
      "cancelled request registers cleanup without waiting for startup",
    );
    complete();
    await cleanup;
    assert.deepEqual(calls, ["prewarm", "spawn settled", "close"]);
  }
});

test(
  "pinned SDK reads model control metadata from a real child without sending a user message",
  { timeout: 5000 },
  async () => {
    const root = await mkdtemp(join(tmpdir(), "catbot-sdk-control-test-"));
    const cli = join(root, "fixture.cjs"),
      observed = join(root, "requests.jsonl");
    await writeFile(
      cli,
      `
    const fs = require('node:fs');
    const rl = require('node:readline').createInterface({input: process.stdin});
    rl.on('line', line => {
      const message = JSON.parse(line);
      fs.appendFileSync(${JSON.stringify(observed)}, JSON.stringify(message)+'\\n');
      const response = message.request.subtype === 'get_available_models'
        ? {availableModels: [{modelId: 'fixture-model', name: 'Fixture'}]} : {};
      process.stdout.write(JSON.stringify({type: 'control_response', response: {subtype:'success', request_id: message.request_id, response}})+'\\n');
    });
  `,
    );
    try {
      const models = await discoverCodeBuddyModels(
        request(),
        { home: root, cwd: root, signal: new AbortController().signal },
        (options) =>
          unstable_v2_createSession({
            ...options,
            pathToCodebuddyCode: cli,
          }),
      );
      assert.deepEqual(models, [{ id: "fixture-model", name: "Fixture" }]);
      const requests = (await readFile(observed, "utf8"))
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line));
      assert.deepEqual(
        requests.map((message) => message.type),
        ["control_request", "control_request"],
      );
      assert.deepEqual(
        requests.map((message) => message.request.subtype),
        ["initialize", "get_available_models"],
      );
      assert.equal(requests[0].request.environment, "ioa");
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  },
);

test(
  "cancelled discovery returns before late startup but retains its directory until deferred close",
  { timeout: 1000 },
  async () => {
    const root = await mkdtemp(join(tmpdir(), "catbot-late-start-test-"));
    const controller = new AbortController();
    let finishCleanup!: () => void;
    const cleanup = new Promise<void>((resolve) => {
      finishCleanup = resolve;
    });
    try {
      const registry = new ProviderRegistry([
        {
          name: "codebuddy",
          execute: async () => {},
          discoverModels: async (_, context) => {
            context.deferCleanup!(cleanup);
            controller.abort(new Error("request cancelled"));
            throw controller.signal.reason;
          },
        },
      ]);
      await assert.rejects(
        discoverModels(request(), root, controller.signal, registry),
        /request cancelled/,
      );
      assert.equal((await readdir(join(root, "model-discovery"))).length, 1);
      finishCleanup();
      for (let attempt = 0; attempt < 20; attempt++) {
        if ((await readdir(join(root, "model-discovery"))).length === 0) return;
        await new Promise((resolve) => setTimeout(resolve, 5));
      }
      assert.fail("deferred temporary directory was not cleaned");
    } finally {
      finishCleanup();
      await rm(root, { recursive: true, force: true });
    }
  },
);

test(
  "bundled CodeBuddy CLI can discover its environment catalog without a generation request",
  { skip: process.env.CATBOT_TEST_CODEBUDDY_METADATA !== "1", timeout: 25000 },
  async () => {
    const root = await mkdtemp(join(tmpdir(), "catbot-bundled-metadata-test-"));
    try {
      const models = await discoverCodeBuddyModels(
        { ...request(), apiKey: "fixture-invalid-for-metadata-test" },
        { home: root, cwd: root, signal: AbortSignal.timeout(18000) },
        (options) =>
          unstable_v2_createSession({
            ...options,
            env: {
              ...options.env,
              CODEBUDDY_BASE_URL: undefined,
              ANTHROPIC_API_KEY: undefined,
              ANTHROPIC_AUTH_TOKEN: undefined,
              ANTHROPIC_BASE_URL: undefined,
            },
          }),
      );
      // SDK directory discovery is not account authorization; the deliberately
      // invalid key prevents this check being mistaken for a live model call.
      assert.ok(
        models.length > 0,
        "the bundled CLI should expose a model catalog",
      );
      assert.ok(models.every((model) => model.id && model.name));
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  },
);
