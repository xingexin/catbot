import {
  query as claudeQuery,
  type Options as ClaudeOptions,
} from "@anthropic-ai/claude-agent-sdk";
import type {
  RunRequest,
  ExecutionContext,
  SDKProvider,
  SDKQuery,
} from "../contracts.js";
import { commonOptions } from "../shared/options.js";
import { promptFor } from "../shared/prompt.js";
import { secretaryToolPermission } from "../shared/permissions.js";
import { emitQueryEvents } from "../shared/events.js";

export function claudeOptions(
  input: RunRequest,
  { cwd, home, controller }: ExecutionContext,
): ClaudeOptions {
  const common = commonOptions(input, cwd, home, controller);
  const env = { ...common.env };
  if (input.apiKey) env.ANTHROPIC_API_KEY = input.apiKey;
  if (input.config.baseUrl) env.ANTHROPIC_BASE_URL = input.config.baseUrl;
  return { ...common, env, canUseTool: secretaryToolPermission, tools: [] };
}

export function createClaudeProvider(
  query: SDKQuery<ClaudeOptions> = claudeQuery,
): SDKProvider {
  return {
    name: "claude",
    async execute(input, context) {
      await emitQueryEvents(
        query({
          prompt: promptFor(input),
          options: claudeOptions(input, context),
        }),
        context.emit,
      );
    },
  };
}
