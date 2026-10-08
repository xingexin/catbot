import type { RunRequest } from "../contracts.js";
import { instructions } from "./prompt.js";
import { isolatedEnvironment } from "./environment.js";
import { deniedBuiltins } from "./permissions.js";

export function commonOptions(
  input: RunRequest,
  cwd: string,
  home: string,
  abortController: AbortController,
) {
  return {
    cwd,
    model: input.config.model,
    resume: input.nativeId || undefined,
    maxTurns: input.config.maxSteps,
    includePartialMessages: true,
    settingSources: [],
    systemPrompt: instructions(input),
    abortController,
    disallowedTools: [...deniedBuiltins],
    // allowedTools auto-approves calls; it is not an exclusive allowlist.
    // Keep the permission callback in control instead of bypassing it.
    mcpServers: {
      secretary: {
        type: "http" as const,
        url: input.gatewayUrl,
        headers: { Authorization: "Bearer " + input.gatewayToken },
      },
    },
    env: isolatedEnvironment(home),
  };
}
