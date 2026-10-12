import type { AuthEnvironment } from "@tencent-ai/agent-sdk";
import {
  CodeBuddyEnvironment,
  type ModelDiscoveryRequest,
} from "../contracts.js";
import { isolatedEnvironment } from "../shared/environment.js";

export function codebuddyEnvironment(
  input: ModelDiscoveryRequest,
  home: string,
): { env: Record<string, string | undefined>; environment?: AuthEnvironment } {
  // The SDK merges its env with process.env; an explicit undefined removes an
  // inherited setting in the child without mutating the runtime's environment.
  const env: Record<string, string | undefined> = isolatedEnvironment(home);
  if (input.apiKey) env.CODEBUDDY_API_KEY = input.apiKey;
  if (input.config.baseUrl) env.CODEBUDDY_BASE_URL = input.config.baseUrl;
  switch (input.config.codebuddyEnvironment ?? CodeBuddyEnvironment.Inherit) {
    case CodeBuddyEnvironment.Unknown: // Existing snapshots did not have this field.
    case CodeBuddyEnvironment.Inherit:
      env.CODEBUDDY_INTERNET_ENVIRONMENT =
        process.env.CODEBUDDY_INTERNET_ENVIRONMENT;
      return { env };
    case CodeBuddyEnvironment.IOA:
      env.CODEBUDDY_INTERNET_ENVIRONMENT = "ioa";
      return { env, environment: "ioa" };
    case CodeBuddyEnvironment.CN:
      env.CODEBUDDY_INTERNET_ENVIRONMENT = "internal";
      return { env, environment: "internal" };
    case CodeBuddyEnvironment.International:
      env.CODEBUDDY_INTERNET_ENVIRONMENT = undefined;
      return { env, environment: "external" };
    default:
      throw new Error("Unsupported CodeBuddy environment");
  }
}
