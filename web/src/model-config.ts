import { api } from "./api";

// Existing API strings are decoded at this boundary; UI decisions use enums.
export enum ExecutionKind {
  Unknown = 0,
  API = 1,
  SDK = 2,
}
export enum SDKProvider {
  Unknown = 0,
  CodeBuddy = 1,
  Claude = 2,
  Codex = 3,
}
export enum ModelProtocol {
  Unknown = 0,
  ChatCompletions = 1,
  Responses = 2,
  Messages = 3,
}
export enum CodeBuddyEnvironment {
  Unknown = 0,
  Inherit = 1,
  IOA = 2,
  CN = 3,
  International = 4,
}
export enum ModelListSource {
  Unknown = 0,
  Provider = 1,
}

export function executionKind(value: unknown): ExecutionKind {
  switch (value) {
    case "api":
      return ExecutionKind.API;
    case "sdk":
      return ExecutionKind.SDK;
    default:
      return ExecutionKind.Unknown;
  }
}
export function sdkProvider(value: unknown): SDKProvider {
  switch (value) {
    case "codebuddy":
      return SDKProvider.CodeBuddy;
    case "claude":
      return SDKProvider.Claude;
    case "codex":
      return SDKProvider.Codex;
    default:
      return SDKProvider.Unknown;
  }
}
export function modelProtocol(value: unknown): ModelProtocol {
  switch (value) {
    case "openai-chat":
      return ModelProtocol.ChatCompletions;
    case "openai-responses":
      return ModelProtocol.Responses;
    case "anthropic":
      return ModelProtocol.Messages;
    default:
      return ModelProtocol.Unknown;
  }
}
export const codeBuddyEnvironmentOptions = [
  { value: CodeBuddyEnvironment.Inherit, label: "跟随部署配置" },
  { value: CodeBuddyEnvironment.IOA, label: "iOA（企业环境）" },
  { value: CodeBuddyEnvironment.CN, label: "中国站" },
  { value: CodeBuddyEnvironment.International, label: "国际站" },
];

type ConfigDraft = Record<string, any>;
export function isCodeBuddy(config: ConfigDraft): boolean {
  return (
    executionKind(config.kind) === ExecutionKind.SDK &&
    sdkProvider(config.provider) === SDKProvider.CodeBuddy
  );
}
export function modelEnvironmentLabel(config: ConfigDraft): string | undefined {
  if (!isCodeBuddy(config)) return undefined;
  const value =
    config.codebuddyEnvironment == null ||
    config.codebuddyEnvironment === CodeBuddyEnvironment.Unknown
      ? CodeBuddyEnvironment.Inherit
      : config.codebuddyEnvironment;
  return (
    codeBuddyEnvironmentOptions.find((option) => option.value === value)
      ?.label ?? "未知环境"
  );
}
export function codeBuddyEnvironment(value: unknown): CodeBuddyEnvironment {
  // Unset/zero records predate explicit per-configuration environments.
  if (value == null || value === CodeBuddyEnvironment.Unknown)
    return CodeBuddyEnvironment.Inherit;
  if (codeBuddyEnvironmentOptions.some((option) => option.value === value))
    return value as CodeBuddyEnvironment;
  throw new Error("未知的 CodeBuddy 环境，请重新选择");
}
export function initialModelConfig(config: ConfigDraft): ConfigDraft {
  const sdk = executionKind(config.kind) === ExecutionKind.SDK;
  const result: ConfigDraft = {
    kind: "api",
    protocol: "openai-chat",
    maxSteps: 12,
    maxTokens: 4096,
    maxInputBytes: 98304,
    timeoutSec: 180,
    ...config,
    capabilities: {
      tools: true,
      stream: true,
      images: false,
      resume: sdk,
      ...config.capabilities,
    },
  };
  if (
    isCodeBuddy(result) &&
    (result.codebuddyEnvironment == null ||
      result.codebuddyEnvironment === CodeBuddyEnvironment.Unknown)
  )
    result.codebuddyEnvironment = CodeBuddyEnvironment.Inherit;
  return result;
}
export function normalizeModelConfig(config: ConfigDraft): ConfigDraft {
  const result = { ...config };
  const kind = executionKind(result.kind);
  if (kind === ExecutionKind.Unknown) throw new Error("请选择有效的接入方式");
  if (
    kind === ExecutionKind.SDK &&
    sdkProvider(result.provider) === SDKProvider.Unknown
  )
    throw new Error("请选择有效的 Agent SDK");
  if (
    kind === ExecutionKind.API &&
    modelProtocol(result.protocol) === ModelProtocol.Unknown
  )
    throw new Error("请选择有效的接口协议");
  if (isCodeBuddy(result))
    result.codebuddyEnvironment = codeBuddyEnvironment(
      result.codebuddyEnvironment,
    );
  else delete result.codebuddyEnvironment;
  return result;
}
export function canListModels(config: ConfigDraft): boolean {
  const kind = executionKind(config.kind);
  return (
    (kind === ExecutionKind.API &&
      modelProtocol(config.protocol) !== ModelProtocol.Unknown) ||
    (kind === ExecutionKind.SDK &&
      (sdkProvider(config.provider) === SDKProvider.CodeBuddy ||
        sdkProvider(config.provider) === SDKProvider.Claude))
  );
}
export function modelAuthContextChanged(
  previous: ConfigDraft,
  next: ConfigDraft,
): boolean {
  const previousKind = executionKind(previous.kind);
  const nextKind = executionKind(next.kind);
  if (
    previousKind === ExecutionKind.Unknown ||
    nextKind === ExecutionKind.Unknown ||
    previousKind !== nextKind
  )
    return true;
  if (nextKind !== ExecutionKind.SDK) return false;
  const previousProvider = sdkProvider(previous.provider);
  const nextProvider = sdkProvider(next.provider);
  return (
    previousProvider === SDKProvider.Unknown ||
    nextProvider === SDKProvider.Unknown ||
    previousProvider !== nextProvider
  );
}
export function modelConnectionKey(config: ConfigDraft): string {
  return JSON.stringify([
    config.kind,
    config.provider,
    config.protocol,
    config.baseUrl ?? "",
    config.credentialId ?? "",
    config.codebuddyEnvironment ?? CodeBuddyEnvironment.Inherit,
  ]);
}
export interface AvailableModel {
  id: string;
  name: string;
  description?: string;
}
export interface AvailableModels {
  models: AvailableModel[];
  source: ModelListSource;
}
export function readAvailableModels(value: unknown): AvailableModels {
  const result = value as Partial<AvailableModels> | null;
  if (
    !result ||
    result.source !== ModelListSource.Provider ||
    !Array.isArray(result.models)
  )
    throw new Error("服务返回的模型列表格式不正确，可继续手动填写模型名称");
  const seen = new Set<string>();
  const models: AvailableModel[] = [];
  for (const item of result.models) {
    if (
      !item ||
      typeof item.id !== "string" ||
      !item.id.trim() ||
      typeof item.name !== "string" ||
      (item.description !== undefined && typeof item.description !== "string")
    )
      throw new Error("服务返回的模型列表格式不正确，可继续手动填写模型名称");
    if (!seen.has(item.id)) {
      seen.add(item.id);
      models.push({
        id: item.id,
        name: item.name || item.id,
        ...(item.description === undefined
          ? {}
          : { description: item.description }),
      });
    }
  }
  return { models, source: result.source };
}

// Generation and connection identity checks prevent stale network responses from
// repainting a different editor or a changed account, even before effects run.
export class ModelLookup {
  private generation = 0;
  invalidate(): void {
    this.generation += 1;
  }
  async load(
    config: ConfigDraft,
    currentKey: () => string,
    request: (config: ConfigDraft) => Promise<unknown> = (value) =>
      api("/configs/models", value),
  ): Promise<AvailableModels | undefined> {
    const generation = ++this.generation;
    const key = modelConnectionKey(config);
    const current = () =>
      generation === this.generation && key === currentKey();
    try {
      const result = await request(normalizeModelConfig(config));
      return current() ? readAvailableModels(result) : undefined;
    } catch (error) {
      if (current()) throw error;
      return undefined;
    }
  }
}
