export enum SearchProvider {
  Unknown = 0,
  Tavily = 1,
  Brave = 2,
}

export interface SearchConfig {
  provider: SearchProvider;
  tavilyApiKey: string;
  braveApiKey: string;
  apiKey: string;
  timeoutSec: number;
  maxResults: number;
  maxResponseBytes: number;
  maxTextChars: number;
}

export function parseConfig(config: Record<string, unknown>): SearchConfig {
  if (!config || typeof config !== "object" || Array.isArray(config)) {
    throw new Error("联网搜索插件配置必须是对象");
  }
  const provider =
    config.provider === undefined ? SearchProvider.Tavily : config.provider;
  if (provider !== SearchProvider.Tavily && provider !== SearchProvider.Brave) {
    throw new Error("请选择有效的搜索服务：Tavily（1）或 Brave（2）");
  }
  if ("apiKey" in config) {
    throw new Error(
      "请分别配置 Tavily API Key 和 Brave API Key，不能共用 apiKey 字段",
    );
  }
  const tavilyApiKey = secret(config.tavilyApiKey, "Tavily");
  const braveApiKey = secret(config.braveApiKey, "Brave");
  return {
    provider,
    tavilyApiKey,
    braveApiKey,
    apiKey: provider === SearchProvider.Tavily ? tavilyApiKey : braveApiKey,
    timeoutSec: integer(config, "timeoutSec", 15, 1, 60),
    maxResults: integer(config, "maxResults", 5, 1, 10),
    maxResponseBytes: integer(
      config,
      "maxResponseBytes",
      1048576,
      65536,
      2097152,
    ),
    maxTextChars: integer(config, "maxTextChars", 12000, 1000, 30000),
  };
}

function integer(
  config: Record<string, unknown>,
  key: string,
  fallback: number,
  min: number,
  max: number,
): number {
  const value = config[key] === undefined ? fallback : config[key];
  if (
    typeof value !== "number" ||
    !Number.isInteger(value) ||
    value < min ||
    value > max
  ) {
    throw new Error(`${key} 必须是 ${min} 到 ${max} 之间的整数`);
  }
  return value;
}

function secret(value: unknown, label: string): string {
  if (value === undefined) return "";
  if (
    typeof value !== "string" ||
    value.length > 1024 ||
    /[\x00-\x1f\x7f]/u.test(value)
  ) {
    throw new Error(`${label} API Key 格式无效，请重新配置`);
  }
  return value.trim();
}
