import {
  query as buddyQuery,
  type Options as BuddyOptions,
} from "@tencent-ai/agent-sdk";
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

export function codebuddyOptions(
  input: RunRequest,
  { cwd, home, controller }: ExecutionContext,
): BuddyOptions {
  const common = commonOptions(input, cwd, home, controller);
  const env = { ...common.env };
  if (input.apiKey) env.CODEBUDDY_API_KEY = input.apiKey;
  if (input.config.baseUrl) env.CODEBUDDY_BASE_URL = input.config.baseUrl;
  if (process.env.CODEBUDDY_INTERNET_ENVIRONMENT)
    env.CODEBUDDY_INTERNET_ENVIRONMENT =
      process.env.CODEBUDDY_INTERNET_ENVIRONMENT;
  // Supported by the installed CodeBuddy runtime, ahead of its HTTP config types.
  const mcpServers = {
    secretary: {
      ...common.mcpServers.secretary,
      // Tools must be present at startup because native ToolSearch is disabled.
      alwaysLoad: true,
    },
  };
  return {
    ...common,
    env,
    canUseTool: secretaryToolPermission,
    tools: [],
    strictMcpConfig: true,
    mcpServers,
  };
}

export function createCodeBuddyProvider(
  query: SDKQuery<BuddyOptions> = buddyQuery,
): SDKProvider {
  return {
    name: "codebuddy",
    async execute(input, context) {
      await emitQueryEvents(
        query({
          prompt: promptFor(input),
          options: codebuddyOptions(input, context),
        }),
        context.emit,
      );
    },
  };
}
