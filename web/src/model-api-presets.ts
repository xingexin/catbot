import {
  ExecutionKind,
  ModelProtocol,
  executionKind,
  modelProtocol,
} from "./model-config";

export enum APIProviderPreset {
  Unknown = 0,
  Custom = 1,
  OpenAI = 2,
  Anthropic = 3,
  DeepSeek = 4,
  Kimi = 5,
  GLM = 6,
}

type ConfigDraft = Record<string, any>;
interface APIPreset {
  value: APIProviderPreset;
  label: string;
  protocol: ModelProtocol;
  baseUrl: string;
  compatibleProtocols: ModelProtocol[];
  aliases?: string[];
}
const presets: APIPreset[] = [
  {
    value: APIProviderPreset.OpenAI,
    label: "OpenAI（GPT）",
    protocol: ModelProtocol.Responses,
    baseUrl: "https://api.openai.com/v1",
    compatibleProtocols: [
      ModelProtocol.Responses,
      ModelProtocol.ChatCompletions,
    ],
  },
  {
    value: APIProviderPreset.Anthropic,
    label: "Anthropic（Claude）",
    protocol: ModelProtocol.Messages,
    baseUrl: "https://api.anthropic.com",
    compatibleProtocols: [ModelProtocol.Messages],
    aliases: ["https://api.anthropic.com/v1"],
  },
  {
    value: APIProviderPreset.DeepSeek,
    label: "DeepSeek（吃白饭的大肥鱼）",
    protocol: ModelProtocol.ChatCompletions,
    baseUrl: "https://api.deepseek.com",
    compatibleProtocols: [ModelProtocol.ChatCompletions],
  },
  {
    value: APIProviderPreset.Kimi,
    label: "Kimi（月之暗面）",
    protocol: ModelProtocol.ChatCompletions,
    baseUrl: "https://api.moonshot.cn/v1",
    compatibleProtocols: [ModelProtocol.ChatCompletions],
  },
  {
    value: APIProviderPreset.GLM,
    label: "GLM（智谱）",
    protocol: ModelProtocol.ChatCompletions,
    baseUrl: "https://open.bigmodel.cn/api/paas/v4",
    compatibleProtocols: [ModelProtocol.ChatCompletions],
  },
];
export const apiProviderOptions = [
  { value: APIProviderPreset.Custom, label: "自定义兼容服务" },
  ...presets.map(({ value, label }) => ({ value, label })),
];
function protocolWire(protocol: ModelProtocol): string {
  switch (protocol) {
    case ModelProtocol.ChatCompletions:
      return "openai-chat";
    case ModelProtocol.Responses:
      return "openai-responses";
    case ModelProtocol.Messages:
      return "anthropic";
    default:
      throw new Error("未知的预设协议");
  }
}
function canonicalEndpoint(value: unknown): string | undefined {
  if (typeof value !== "string" || !value.trim()) return undefined;
  try {
    const url = new URL(value.trim());
    if (url.username || url.password || url.search || url.hash)
      return undefined;
    return url.origin + url.pathname.replace(/\/+$/, "");
  } catch {
    return undefined;
  }
}
export function inferAPIProvider(config: ConfigDraft): APIProviderPreset {
  if (executionKind(config.kind) !== ExecutionKind.API)
    return APIProviderPreset.Unknown;
  const endpoint = canonicalEndpoint(config.baseUrl);
  const protocol = modelProtocol(config.protocol);
  if (endpoint === undefined) return APIProviderPreset.Custom;
  return (
    presets.find(
      (preset) =>
        preset.compatibleProtocols.includes(protocol) &&
        [preset.baseUrl, ...(preset.aliases ?? [])].some(
          (base) => canonicalEndpoint(base) === endpoint,
        ),
    )?.value ?? APIProviderPreset.Custom
  );
}
export function apiEndpointKey(config: ConfigDraft): string {
  return JSON.stringify([config.protocol, config.baseUrl ?? ""]);
}
function endpointOrigin(value: unknown): string | undefined {
  if (typeof value !== "string" || !value.trim()) return undefined;
  try {
    const url = new URL(value.trim());
    if (
      (url.protocol !== "https:" && url.protocol !== "http:") ||
      url.username ||
      url.password
    )
      return undefined;
    return url.origin;
  } catch {
    return undefined;
  }
}
export function modelOriginChanged(previous: unknown, next: unknown): boolean {
  const origin = endpointOrigin(previous);
  return origin === undefined || origin !== endpointOrigin(next);
}
export function applyModelEndpoint(
  config: ConfigDraft,
  baseUrl: string,
): ConfigDraft {
  return {
    ...config,
    baseUrl,
    ...(modelOriginChanged(config.baseUrl, baseUrl)
      ? { credentialId: undefined }
      : {}),
  };
}
// Presets are editor conveniences, not persisted provider types. Changing the
// origin clears the old key selection so it cannot be sent to a different host.
export function applyAPIProvider(
  config: ConfigDraft,
  selected: APIProviderPreset,
): ConfigDraft {
  if (executionKind(config.kind) !== ExecutionKind.API)
    throw new Error("服务商快捷配置仅适用于模型 API 直连");
  if (selected === APIProviderPreset.Custom) return { ...config };
  const preset = presets.find((value) => value.value === selected);
  if (!preset) throw new Error("请选择有效的服务商快捷配置");
  return {
    ...applyModelEndpoint(config, preset.baseUrl),
    protocol: protocolWire(preset.protocol),
  };
}
