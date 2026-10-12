import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { join } from "node:path";
import {
  ModelListSource,
  type ModelDiscoveryRequest,
  type ModelOption,
} from "./contracts.js";
import { providerRegistry, type ProviderRegistry } from "./registry.js";
import { safeEvent } from "./validation.js";

export class ModelDiscoveryUnsupportedError extends Error {}

export async function discoverModels(
  input: ModelDiscoveryRequest,
  dataDir: string,
  signal: AbortSignal,
  registry: ProviderRegistry = providerRegistry,
): Promise<{ models: ModelOption[]; source: ModelListSource }> {
  signal.throwIfAborted();
  const provider = registry.get(input.config.provider);
  if (!provider.discoverModels)
    throw new ModelDiscoveryUnsupportedError(
      "此 Agent SDK 暂不提供模型发现，请手动填写模型名称",
    );
  const parent = join(dataDir, "model-discovery");
  await mkdir(parent, { recursive: true, mode: 0o700 });
  const temporary = await mkdtemp(join(parent, "request-"));
  let deferredCleanup: Promise<void> | undefined;
  try {
    const home = join(temporary, "home"),
      cwd = join(temporary, "workspace");
    await Promise.all([
      mkdir(home, { mode: 0o700 }),
      mkdir(cwd, { mode: 0o700 }),
    ]);
    signal.throwIfAborted();
    const models = normalizeModels(
      await provider.discoverModels(input, {
        home,
        cwd,
        signal,
        deferCleanup: (cleanup) => {
          deferredCleanup = Promise.all([deferredCleanup, cleanup]).then(
            () => {},
          );
          void deferredCleanup.catch(() => {});
        },
      }),
    );
    return { models, source: ModelListSource.Live };
  } finally {
    const remove = () => rm(temporary, { recursive: true, force: true });
    if (deferredCleanup && signal.aborted) {
      void deferredCleanup.finally(remove).catch(() => {
        process.stderr.write("Model discovery temporary cleanup failed\n");
      });
    } else {
      await remove();
    }
  }
}

function normalizeModels(value: unknown): ModelOption[] {
  if (!Array.isArray(value) || value.length > 2000)
    throw new Error("SDK 返回的模型列表无效");
  const options = new Map<string, ModelOption>();
  for (const entry of value) {
    if (
      !entry ||
      typeof entry.id !== "string" ||
      !entry.id.trim() ||
      entry.id.length > 256 ||
      entry.id.includes("\0") ||
      typeof entry.name !== "string" ||
      entry.name.length > 1024 ||
      entry.name.includes("\0") ||
      (entry.description !== undefined &&
        (typeof entry.description !== "string" ||
          entry.description.length > 4096 ||
          entry.description.includes("\0")))
    )
      throw new Error("SDK 返回的模型列表无效");
    options.set(entry.id, {
      id: entry.id,
      name: entry.name || entry.id,
      ...(entry.description ? { description: entry.description } : {}),
    });
  }
  return [...options.values()];
}

export function modelDiscoveryError(
  error: unknown,
  secrets: (string | undefined)[],
): string {
  const message = error instanceof Error ? error.message : "模型列表读取失败";
  try {
    return (
      JSON.parse(safeEvent({ message }, secrets).json) as { message: string }
    ).message;
  } catch {
    return "模型列表读取失败";
  }
}
