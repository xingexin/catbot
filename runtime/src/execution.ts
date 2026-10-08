import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import type { RunRequest, Sink } from "./contracts.js";
import { providerRegistry, type ProviderRegistry } from "./registry.js";

export async function execute(
  input: RunRequest,
  dataDir: string,
  controller: AbortController,
  emit: Sink,
  registry: ProviderRegistry = providerRegistry,
): Promise<void> {
  if (
    !/^[a-zA-Z0-9:_-]{1,240}$/.test(input.runId) ||
    !/^[a-zA-Z0-9:_-]{1,240}$/.test(input.sessionId)
  )
    throw new Error("Invalid execution identifier");
  const provider = registry.get(input.config.provider);
  const home = join(dataDir, "homes", input.config.provider);
  const cwd = join(
    dataDir,
    "workspaces",
    input.config.provider,
    input.sessionId,
  );
  await mkdir(home, { recursive: true, mode: 0o700 });
  await mkdir(cwd, { recursive: true, mode: 0o700 });
  await provider.execute(input, { home, cwd, controller, emit });
}
