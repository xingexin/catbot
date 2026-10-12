import { spawn, type ChildProcess } from "node:child_process";
import { join } from "node:path";
import {
  query as claudeQuery,
  type ModelInfo,
  type Options,
  type SDKUserMessage,
} from "@anthropic-ai/claude-agent-sdk";
import type {
  ModelDiscoveryContext,
  ModelDiscoveryRequest,
  ModelOption,
} from "../contracts.js";
import { isolatedEnvironment } from "../shared/environment.js";
import { deniedBuiltins } from "../shared/permissions.js";

// Exposes only initialization metadata and cleanup, never a generation method.
export interface ClaudeModelQuery {
  supportedModels(): Promise<ModelInfo[]>;
  close(): void;
}
export type ClaudeModelQueryFactory = (input: {
  prompt: AsyncIterable<SDKUserMessage>;
  options: Options;
}) => ClaudeModelQuery;

export async function discoverClaudeModels(
  input: ModelDiscoveryRequest,
  context: ModelDiscoveryContext,
  query: ClaudeModelQueryFactory = claudeQuery,
): Promise<ModelOption[]> {
  context.signal.throwIfAborted();
  const controller = new AbortController();
  let releaseInput!: () => void;
  const inputFinished = new Promise<void>((resolve) => {
    releaseInput = resolve;
  });
  // An empty string is still a user message. Keep stdin open for initialize,
  // but never yield a message, including on cancellation or cleanup.
  async function* metadataInput(): AsyncGenerator<SDKUserMessage> {
    await inputFinished;
  }
  let child: ChildProcess | undefined;
  let exited = Promise.resolve();
  const env = {
    ...isolatedEnvironment(context.home),
    CLAUDE_CONFIG_DIR: join(context.home, ".claude"),
    ...(input.apiKey ? { ANTHROPIC_API_KEY: input.apiKey } : {}),
    ...(input.config.baseUrl
      ? { ANTHROPIC_BASE_URL: input.config.baseUrl }
      : {}),
  };
  let session: ClaudeModelQuery | undefined;
  let rejectAbort!: (reason?: unknown) => void;
  const aborted = new Promise<never>((_, reject) => {
    rejectAbort = reject;
  });
  // A synchronous factory failure can precede attaching the race below.
  void aborted.catch(() => {});
  const onAbort = () => {
    controller.abort(context.signal.reason);
    rejectAbort(context.signal.reason ?? new Error("Model discovery cancelled"));
  };
  context.signal.addEventListener("abort", onAbort, { once: true });
  try {
    context.signal.throwIfAborted();
    session = query({
      prompt: metadataInput(),
      options: {
        env,
        cwd: context.cwd,
        abortController: controller,
        settingSources: [],
        tools: [],
        disallowedTools: [...deniedBuiltins],
        canUseTool: async () => ({
          behavior: "deny",
          message: "Model discovery cannot execute tools",
        }),
        strictMcpConfig: true,
        mcpServers: {},
        persistSession: false,
        maxTurns: 1,
        // Retain the isolated directory until the actual process exits. The
        // SDK's close() returns before its native process has necessarily exited.
        spawnClaudeCodeProcess(options) {
          controller.signal.throwIfAborted();
          const process = spawn(options.command, options.args, {
            cwd: options.cwd,
            env: options.env,
            signal: options.signal,
            stdio: ["pipe", "pipe", "ignore"],
            windowsHide: true,
          });
          child = process;
          exited = new Promise<void>((resolve) => process.once("close", resolve));
          // The SDK also handles process errors; this covers synchronous factory
          // failure before it has attached its listeners without logging secrets.
          process.on("error", () => {});
          return process;
        },
      },
    });
    const models = await Promise.race([session.supportedModels(), aborted]);
    return models.map(({ value, displayName, description }) => ({
      id: value,
      name: displayName,
      ...(description ? { description } : {}),
    }));
  } finally {
    context.signal.removeEventListener("abort", onAbort);
    releaseInput();
    try {
      session?.close();
    } finally {
      controller.abort(new Error("Model discovery finished"));
      let forceKill: ReturnType<typeof setTimeout> | undefined;
      if (child && child.exitCode === null && child.signalCode === null) {
        child.kill("SIGTERM");
        forceKill = setTimeout(() => child?.kill("SIGKILL"), 1000);
        forceKill.unref();
      }
      const cleanup = exited.finally(() => clearTimeout(forceKill));
      if (context.signal.aborted) {
        context.deferCleanup?.(cleanup);
        void cleanup.catch(() => {});
      } else {
        await cleanup;
      }
    }
  }
}
