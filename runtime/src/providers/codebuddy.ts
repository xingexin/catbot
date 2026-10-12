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
import {
  discoverCodeBuddyModels,
  type CodeBuddySessionFactory,
} from "./codebuddy-models.js";
import { codebuddyEnvironment } from "./codebuddy-environment.js";
import { commonOptions } from "../shared/options.js";
import { promptFor } from "../shared/prompt.js";
import { secretaryToolPermission } from "../shared/permissions.js";
import { emitQueryEvents } from "../shared/events.js";

export function codebuddyOptions(
  input: RunRequest,
  { cwd, home, controller }: ExecutionContext,
): BuddyOptions {
  const common = commonOptions(input, cwd, home, controller);
  const environment = codebuddyEnvironment(input, home);
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
    ...environment,
    canUseTool: secretaryToolPermission,
    tools: [],
    strictMcpConfig: true,
    mcpServers,
  };
}

export function createCodeBuddyProvider(
  query: SDKQuery<BuddyOptions> = buddyQuery,
  sessionFactory?: CodeBuddySessionFactory,
): SDKProvider {
  return {
    name: "codebuddy",
    discoverModels: (input, context) =>
      discoverCodeBuddyModels(input, context, sessionFactory),
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
