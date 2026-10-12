import { join } from "node:path";
import {
  unstable_v2_createSession,
  type SessionOptions,
} from "@tencent-ai/agent-sdk";
import type {
  ModelDiscoveryContext,
  ModelDiscoveryRequest,
  ModelOption,
} from "../contracts.js";
import { codebuddyEnvironment } from "./codebuddy-environment.js";
import { deniedBuiltins } from "../shared/permissions.js";

// Deliberately excludes send/stream: discovery can only read CLI metadata.
export interface CodeBuddyModelSession {
  connect(): Promise<void>;
  getAvailableModels(): Promise<
    { modelId: string; name: string; description?: string }[]
  >;
  close(): void;
}
export type CodeBuddySessionFactory = (
  options: SessionOptions,
) => CodeBuddyModelSession;

export async function discoverCodeBuddyModels(
  input: ModelDiscoveryRequest,
  context: ModelDiscoveryContext,
  createSession: CodeBuddySessionFactory = unstable_v2_createSession,
): Promise<ModelOption[]> {
  context.signal.throwIfAborted();
  const environment = codebuddyEnvironment(input, context.home);
  const session = createSession({
    ...environment,
    env: {
      ...environment.env,
      CODEBUDDY_CONFIG_DIR: join(context.home, ".codebuddy"),
      CODEBUDDY_PROJECT_CONFIG_DIR: join(context.cwd, ".codebuddy"),
    },
    cwd: context.cwd,
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
    // Do not pass requestTimeoutMs: SDK 0.3.268 forwards an unsupported
    // --request-timeout-ms flag to its bundled CLI. The request signal owns
    // the deadline and closes the process on timeout instead.
  });
  let rejectAbort: (reason?: unknown) => void;
  const aborted = new Promise<never>((_, reject) => {
    rejectAbort = reject;
  });
  // SDK 0.3.268 can still spawn after close() while its asynchronous version
  // read is pending. Prewarm first; connect() only creates the process and does
  // not send initialize, a prompt, or wait for any upstream model request.
  const startup = Promise.resolve().then(() => {
    context.signal.throwIfAborted();
    return session.connect();
  });
  let closing: Promise<void> | undefined;
  const close = () => {
    closing ??= startup
      .catch(() => {})
      // Drain initialization microtasks before closing: the pinned SDK also
      // starts a transport from control requests without checking closed.
      .then(() => new Promise<void>((resolve) => setImmediate(resolve)))
      .then(() => session.close());
    return closing;
  };
  const onAbort = () => {
    void close().catch(() => {});
    rejectAbort(
      context.signal.reason ?? new Error("Model discovery cancelled"),
    );
  };
  context.signal.addEventListener("abort", onAbort, { once: true });
  try {
    context.signal.throwIfAborted();
    const models = await Promise.race([
      startup.then(() => {
        context.signal.throwIfAborted();
        return session.getAvailableModels();
      }),
      aborted,
    ]);
    return models.map(({ modelId, name, description }) => ({
      id: modelId,
      name,
      ...(description ? { description } : {}),
    }));
  } finally {
    context.signal.removeEventListener("abort", onAbort);
    const cleanup = close();
    if (context.signal.aborted) {
      // A stuck local filesystem/exec must not hold the HTTP cancellation open.
      // The owner retains the temporary directory until late startup is closed.
      context.deferCleanup?.(cleanup);
      void cleanup.catch(() => {});
    } else {
      await cleanup;
    }
  }
}
