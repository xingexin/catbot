import {
  Codex,
  type CodexOptions,
  type ThreadOptions,
  type Thread,
} from "@openai/codex-sdk";
import type { RunRequest, SDKProvider } from "../contracts.js";
import { isolatedEnvironment } from "../shared/environment.js";
import { instructions, promptFor } from "../shared/prompt.js";
import { sdkText } from "../shared/events.js";

export function codexOptions(input: RunRequest, home: string): CodexOptions {
  return {
    apiKey: input.apiKey || undefined,
    baseUrl: input.config.baseUrl || undefined,
    env: {
      ...isolatedEnvironment(home),
      SECRETARY_MCP_TOKEN: input.gatewayToken,
    },
    config: {
      developer_instructions: instructions(input),
      features: {
        shell_tool: false,
        unified_exec: false,
        shell_snapshot: false,
      },
      mcp_servers: {
        secretary: {
          url: input.gatewayUrl,
          bearer_token_env_var: "SECRETARY_MCP_TOKEN",
          required: true,
        },
      },
    },
  };
}
export interface CodexClient {
  startThread(options: ThreadOptions): Pick<Thread, "runStreamed">;
  resumeThread(id: string, options: ThreadOptions): Pick<Thread, "runStreamed">;
}

export function createCodexProvider(
  createClient: (options: CodexOptions) => CodexClient = (options) =>
    new Codex(options),
): SDKProvider {
  return {
    name: "codex",
    async execute(input, { home, cwd, controller, emit }) {
      const sdk = createClient(codexOptions(input, home));
      const opts: ThreadOptions = {
        model: input.config.model,
        workingDirectory: cwd,
        skipGitRepoCheck: true,
        sandboxMode: "read-only",
        approvalPolicy: "never",
        webSearchMode: "disabled",
        networkAccessEnabled: false,
      };
      const thread = input.nativeId
        ? sdk.resumeThread(input.nativeId, opts)
        : sdk.startThread(opts);
      const response = await thread.runStreamed(promptFor(input), {
        signal: controller.signal,
      });
      let text = "",
        finished = false;
      const previous = new Map<string, string>();
      for await (const event of response.events) {
        if (event.type === "thread.started")
          await emit({ type: "native.session", data: { id: event.thread_id } });
        if (event.type === "error") throw new Error(event.message);
        if (event.type === "turn.failed") throw new Error(event.error.message);
        if ("item" in event) {
          const item = event.item;
          if (item.type === "agent_message") {
            const current = sdkText(item.text);
            const old = previous.get(item.id) ?? "";
            if (current.startsWith(old))
              await emit({
                type: "text.delta",
                data: { text: current.slice(old.length) },
              });
            previous.set(item.id, current);
            if (event.type === "item.completed") text = current;
          }
          if (
            item.type === "mcp_tool_call" &&
            (event.type === "item.started" || event.type === "item.completed")
          ) {
            await emit({
              type:
                event.type === "item.started"
                  ? "sdk.tool.started"
                  : "sdk.tool.completed",
              data: { name: item.tool, status: item.status, callId: item.id },
            });
          }
        }
        if (event.type === "turn.completed") {
          finished = true;
          await emit({ type: "completed", data: { text, usage: event.usage } });
        }
      }
      if (!finished)
        throw new Error("Codex stream ended without turn completion");
    },
  };
}
