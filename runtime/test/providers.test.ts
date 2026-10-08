import test from "node:test";
import assert from "node:assert/strict";
import type { Options as ClaudeOptions } from "@anthropic-ai/claude-agent-sdk";
import type { Options as BuddyOptions } from "@tencent-ai/agent-sdk";
import type {
  CodexOptions,
  ThreadEvent,
  ThreadOptions,
} from "@openai/codex-sdk";
import type {
  ExecutionContext,
  RunEvent,
  RunRequest,
  SDKQuery,
} from "../src/contracts.js";
import { createClaudeProvider } from "../src/providers/claude.js";
import { createCodeBuddyProvider } from "../src/providers/codebuddy.js";
import {
  createCodexProvider,
  type CodexClient,
} from "../src/providers/codex.js";

const input: RunRequest = {
  runId: "run",
  sessionId: "session",
  config: {
    provider: "claude",
    model: "fixture",
    baseUrl: "https://fixture.invalid",
    maxSteps: 7,
  },
  persona: {
    systemPrompt: "PERSONA",
    examples: [{ role: "assistant", content: "EXAMPLE" }],
  },
  history: [{ role: "user", content: "HISTORY" }],
  prompt: "CURRENT",
  apiKey: "fixture-key",
  gatewayUrl: "http://core/internal/mcp",
  gatewayToken: "fixture-token",
};
function context() {
  const events: RunEvent[] = [];
  const value: ExecutionContext = {
    home: "/home/isolated",
    cwd: "/work/session",
    controller: new AbortController(),
    emit: async (event) => {
      events.push(event);
    },
  };
  return { events, value };
}
async function* iterable<T>(events: readonly T[]): AsyncGenerator<T> {
  yield* events;
}

for (const provider of ["claude", "codebuddy"] as const) {
  const create = (query: SDKQuery<ClaudeOptions | BuddyOptions>) =>
    provider === "claude"
      ? createClaudeProvider(query)
      : createCodeBuddyProvider(query);
  test(`${provider} retains native resume, tools, private environment, stream and usage contracts`, async () => {
    const { events, value } = context();
    let captured:
      Parameters<SDKQuery<ClaudeOptions | BuddyOptions>>[0] | undefined;
    const adapter = create((request) => {
      captured = request;
      return iterable([
        {
          type: "system",
          subtype: "init",
          session_id: "native-1",
          model: "fixture",
          tools: ["mcp__secretary__echo"],
          mcp_servers: [{ name: "secretary", status: "connected" }],
        },
        {
          type: "stream_event",
          event: {
            type: "content_block_delta",
            delta: { type: "text_delta", text: "hello" },
          },
        },
        {
          type: "stream_event",
          event: {
            type: "content_block_delta",
            delta: { type: "input_json_delta", text: "not text" },
          },
        },
        {
          type: "result",
          subtype: "success",
          result: "hello",
          usage: { output_tokens: 3 },
        },
      ]);
    });
    await adapter.execute(
      { ...input, nativeId: "native-1", config: { ...input.config, provider } },
      value,
    );
    assert.ok(captured);
    assert.equal(captured.prompt, "\nuser: CURRENT");
    const options = captured.options;
    assert.equal(options.resume, "native-1");
    assert.equal(options.abortController, value.controller);
    assert.equal(options.cwd, value.cwd);
    assert.equal(options.env?.HOME, value.home);
    assert.equal(options.maxTurns, 7);
    assert.deepEqual(options.tools, []);
    assert.deepEqual(options.settingSources, []);
    assert.equal(options.includePartialMessages, true);
    assert.match(String(options.systemPrompt), /PERSONA/);
    assert.equal("allowedTools" in options, false);
    assert.equal(
      options.env?.[
        provider === "claude" ? "ANTHROPIC_API_KEY" : "CODEBUDDY_API_KEY"
      ],
      "fixture-key",
    );
    assert.equal(
      options.env?.[
        provider === "claude" ? "ANTHROPIC_BASE_URL" : "CODEBUDDY_BASE_URL"
      ],
      "https://fixture.invalid",
    );
    assert.equal(
      options.env?.[
        provider === "claude" ? "CODEBUDDY_API_KEY" : "ANTHROPIC_API_KEY"
      ],
      undefined,
    );
    assert.equal(typeof options.canUseTool, "function");
    const mcp = options.mcpServers?.secretary as {
      headers: Record<string, string>;
      alwaysLoad?: boolean;
    };
    assert.equal(mcp.headers.Authorization, "Bearer fixture-token");
    assert.equal(mcp.alwaysLoad, provider === "codebuddy" ? true : undefined);
    if (provider === "codebuddy")
      assert.equal((options as BuddyOptions).strictMcpConfig, true);
    assert.deepEqual(events, [
      { type: "native.session", data: { id: "native-1" } },
      {
        type: "sdk.initialized",
        data: {
          model: "fixture",
          tools: ["mcp__secretary__echo"],
          mcpServers: [{ name: "secretary", status: "connected" }],
        },
      },
      { type: "text.delta", data: { text: "hello" } },
      {
        type: "completed",
        data: { text: "hello", usage: { output_tokens: 3 } },
      },
    ]);
  });

  test(`${provider} propagates SDK failures, incomplete streams and malformed output without reporting completion`, async () => {
    for (const [raw, error] of [
      [
        [
          {
            type: "result",
            is_error: true,
            errors: ["rate limited", "retry later"],
          },
        ],
        /rate limited; retry later/,
      ],
      [[{ type: "result", subtype: "error_max_turns" }], /did not complete/],
      [[], /without a result/],
      [[{ type: "result", result: "bad\0text" }], /NUL/],
    ] as [unknown[], RegExp][]) {
      const { events, value } = context();
      await assert.rejects(
        create(() => iterable(raw)).execute(input, value),
        error,
      );
      assert.equal(
        events.some((event) => event.type === "completed"),
        false,
      );
    }
    const { events, value } = context();
    value.controller.abort(new Error("cancelled by caller"));
    await assert.rejects(
      create(async function* ({ options }) {
        options.abortController?.signal.throwIfAborted();
      }).execute(input, value),
      /cancelled by caller/,
    );
    assert.equal(events.length, 0);
  });
}

function codexFixture(raw: readonly ThreadEvent[]) {
  const observed: {
    options?: CodexOptions;
    threadOptions?: ThreadOptions;
    resumed?: string;
    starts: number;
    prompt?: unknown;
    signal?: AbortSignal;
  } = { starts: 0 };
  const client: CodexClient = {
    startThread(options) {
      observed.starts++;
      observed.threadOptions = options;
      return {
        async runStreamed(prompt, options) {
          observed.prompt = prompt;
          observed.signal = options?.signal;
          options?.signal?.throwIfAborted();
          return { events: iterable(raw) };
        },
      };
    },
    resumeThread(id, options) {
      observed.resumed = id;
      const thread = this.startThread(options);
      observed.starts--;
      return thread;
    },
  };
  return {
    observed,
    adapter: createCodexProvider((options) => {
      observed.options = options;
      return client;
    }),
  };
}

test("Codex preserves fresh/resumed thread selection, incremental text, MCP activity and completion usage", async () => {
  const usage = { input_tokens: 9, cached_input_tokens: 2, output_tokens: 4 };
  const raw: ThreadEvent[] = [
    { type: "thread.started", thread_id: "codex-native" },
    {
      type: "item.started",
      item: {
        id: "call",
        type: "mcp_tool_call",
        server: "secretary",
        tool: "example__echo",
        arguments: {},
        status: "in_progress",
      },
    },
    {
      type: "item.completed",
      item: {
        id: "call",
        type: "mcp_tool_call",
        server: "secretary",
        tool: "example__echo",
        arguments: {},
        status: "completed",
      },
    },
    {
      type: "item.updated",
      item: { id: "text", type: "agent_message", text: "hel" },
    },
    {
      type: "item.updated",
      item: { id: "text", type: "agent_message", text: "hello" },
    },
    {
      type: "item.completed",
      item: { id: "text", type: "agent_message", text: "hello" },
    },
    { type: "turn.completed", usage },
  ];
  for (const nativeId of [undefined, "codex-native"]) {
    const { events, value } = context();
    const { adapter, observed } = codexFixture(raw);
    await adapter.execute(
      { ...input, nativeId, config: { ...input.config, provider: "codex" } },
      value,
    );
    assert.equal(observed.resumed, nativeId);
    assert.equal(observed.starts, nativeId ? 0 : 1);
    assert.equal(observed.signal, value.controller.signal);
    assert.equal(observed.options?.apiKey, "fixture-key");
    assert.equal(observed.options?.baseUrl, "https://fixture.invalid");
    assert.equal(observed.threadOptions?.sandboxMode, "read-only");
    assert.equal(observed.threadOptions?.approvalPolicy, "never");
    assert.equal(observed.threadOptions?.webSearchMode, "disabled");
    assert.equal(observed.threadOptions?.networkAccessEnabled, false);
    assert.equal(observed.threadOptions?.workingDirectory, value.cwd);
    if (nativeId) assert.equal(observed.prompt, "\nuser: CURRENT");
    else
      assert.match(
        String(observed.prompt),
        /EXAMPLE\nuser: HISTORY\nuser: CURRENT/,
      );
    assert.deepEqual(events, [
      { type: "native.session", data: { id: "codex-native" } },
      {
        type: "sdk.tool.started",
        data: { name: "example__echo", status: "in_progress", callId: "call" },
      },
      {
        type: "sdk.tool.completed",
        data: { name: "example__echo", status: "completed", callId: "call" },
      },
      { type: "text.delta", data: { text: "hel" } },
      { type: "text.delta", data: { text: "lo" } },
      { type: "text.delta", data: { text: "" } },
      { type: "completed", data: { text: "hello", usage } },
    ]);
  }
});

test("Codex preserves error, missing-completion, malformed text and cancellation failures", async () => {
  for (const [raw, error] of [
    [[{ type: "error", message: "rate limited" }], /rate limited/],
    [
      [{ type: "turn.failed", error: { message: "failed upstream" } }],
      /failed upstream/,
    ],
    [[], /without turn completion/],
    [
      [
        {
          type: "item.completed",
          item: { id: "text", type: "agent_message", text: "bad\0text" },
        },
      ],
      /NUL/,
    ],
  ] as [ThreadEvent[], RegExp][]) {
    const { events, value } = context();
    await assert.rejects(
      codexFixture(raw).adapter.execute(input, value),
      error,
    );
    assert.equal(
      events.some((event) => event.type === "completed"),
      false,
    );
  }
  const { value } = context();
  value.controller.abort(new Error("cancelled by caller"));
  await assert.rejects(
    codexFixture([]).adapter.execute(input, value),
    /cancelled by caller/,
  );
});
