import { parseConfig, SearchProvider } from "./config.js";
import { safeRequest } from "./network.js";

export interface SearchContext {
  config: Record<string, unknown>;
  signal: AbortSignal;
}

export interface SearchResult {
  title: string;
  url: string;
  snippet: string;
}

export interface SearchResponse {
  query: string;
  provider: SearchProvider;
  results: SearchResult[];
  fetchedAt: string;
  untrusted: true;
}

const providerLabels: Readonly<Record<SearchProvider, string>> = {
  [SearchProvider.Unknown]: "未知",
  [SearchProvider.Tavily]: "Tavily",
  [SearchProvider.Brave]: "Brave",
};

// Provider protocol details stay here; callers see the same bounded result shape.
export async function searchWeb(
  args: { query: string; limit?: number },
  context: SearchContext,
  request: typeof safeRequest = safeRequest,
): Promise<SearchResponse> {
  const config = parseConfig(context.config);
  if (
    !args ||
    typeof args.query !== "string" ||
    !args.query.trim() ||
    args.query.length > 500
  ) {
    throw new Error("搜索关键词必须是 1 到 500 个字符的非空文本");
  }
  if (
    args.limit !== undefined &&
    (!Number.isInteger(args.limit) || args.limit < 1 || args.limit > 10)
  ) {
    throw new Error("搜索结果条数必须是 1 到 10 之间的整数");
  }
  if (context.signal.aborted) throw new Error("联网搜索已取消");
  const label = providerLabels[config.provider];
  if (!config.apiKey) {
    throw new Error(
      `尚未配置 ${label} 搜索 API Key，请在“插件 → 联网搜索”的配置中填写“${label} API Key”；模型 API Key 不能代替搜索 Key。网页读取无需 Key。`,
    );
  }
  const query = args.query.trim();
  const limit = Math.min(args.limit ?? config.maxResults, config.maxResults);
  const options = {
    signal: context.signal,
    timeoutMs: config.timeoutSec * 1000,
    maxBytes: config.maxResponseBytes,
    maxRedirects: 0,
  };
  let response: Awaited<ReturnType<typeof safeRequest>>;
  try {
    switch (config.provider) {
      case SearchProvider.Tavily:
        response = await request("https://api.tavily.com/search", {
          ...options,
          method: "POST",
          headers: {
            Authorization: `Bearer ${config.apiKey}`,
            "Content-Type": "application/json",
            Accept: "application/json",
          },
          body: JSON.stringify({
            query,
            max_results: limit,
            search_depth: "basic",
            topic: "general",
            include_answer: false,
            include_raw_content: false,
            include_images: false,
            auto_parameters: false,
          }),
        });
        break;
      case SearchProvider.Brave: {
        const url = new URL("https://api.search.brave.com/res/v1/web/search");
        url.searchParams.set("q", query);
        url.searchParams.set("count", String(limit));
        url.searchParams.set("text_decorations", "false");
        url.searchParams.set("result_filter", "web");
        response = await request(url.href, {
          ...options,
          method: "GET",
          headers: {
            "X-Subscription-Token": config.apiKey,
            Accept: "application/json",
          },
        });
        break;
      }
      default:
        throw new Error("未知搜索服务");
    }
  } catch {
    // Network and provider error text can contain request headers or body echoes.
    if (context.signal.aborted) throw new Error("联网搜索已取消");
    throw new Error(
      `${label} 搜索请求未完成，请检查服务网络、超时和响应大小限制；没有自动重试`,
    );
  }
  if (context.signal.aborted) throw new Error("联网搜索已取消");
  checkStatus(response.status, label);
  let data: unknown;
  try {
    data = JSON.parse(response.body.toString("utf8"));
  } catch {
    throw malformed(label);
  }
  if (!record(data)) throw malformed(label);
  let rows: unknown;
  switch (config.provider) {
    case SearchProvider.Tavily:
      rows = data.results;
      break;
    case SearchProvider.Brave:
      // Brave omits nullable web results for a successful search with no matches.
      // An arbitrary empty/error object must still fail validation.
      if (
        (data.web === undefined || data.web === null) &&
        data.type === "search" &&
        record(data.query) &&
        typeof data.query.original === "string" &&
        data.query.original.trim() &&
        data.error === undefined
      ) {
        rows = [];
      } else {
        rows = record(data.web) ? data.web.results : undefined;
      }
      break;
    default:
      throw new Error("未知搜索服务");
  }
  if (!Array.isArray(rows)) throw malformed(label);
  // Validate before slicing: an error envelope cannot masquerade as no results.
  const results = rows
    .map((row) => normalize(row, config.provider, label))
    .slice(0, limit);
  return {
    query,
    provider: config.provider,
    results,
    fetchedAt: new Date().toISOString(),
    untrusted: true,
  };
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function malformed(label: string): Error {
  return new Error(
    `${label} 返回了无效的搜索结果格式，请稍后重试或检查服务状态`,
  );
}

function checkStatus(status: number, label: string): void {
  if (status >= 200 && status < 300) return;
  if (status === 401 || status === 403) {
    throw new Error(
      `${label} 搜索鉴权失败（HTTP ${status}），请检查该服务的 API Key 和访问权限`,
    );
  }
  if (status === 402 || status === 429 || status === 432 || status === 433) {
    throw new Error(
      `${label} 搜索额度不足或请求受限（HTTP ${status}），请检查服务配额后重试`,
    );
  }
  if (status >= 300 && status < 400) {
    throw new Error(`${label} 搜索接口发生重定向，已停止请求以保护 API Key`);
  }
  throw new Error(
    `${label} 搜索服务请求失败（HTTP ${status}），请检查服务状态后重试`,
  );
}

function normalize(
  value: unknown,
  provider: SearchProvider,
  label: string,
): SearchResult {
  if (
    !record(value) ||
    typeof value.title !== "string" ||
    !value.title.trim() ||
    typeof value.url !== "string" ||
    value.url.length > 4096
  ) {
    throw malformed(label);
  }
  let url: URL;
  try {
    url = new URL(value.url);
  } catch {
    throw malformed(label);
  }
  if (
    (url.protocol !== "https:" && url.protocol !== "http:") ||
    !url.hostname ||
    url.href.length > 4096 ||
    url.username ||
    url.password
  ) {
    throw malformed(label);
  }
  const snippet =
    provider === SearchProvider.Tavily
      ? value.content
      : (value.description ?? "");
  if (snippet !== undefined && typeof snippet !== "string")
    throw malformed(label);
  return {
    title: value.title.trim().slice(0, 500),
    url: url.href,
    snippet: (snippet ?? "").trim().slice(0, 2000),
  };
}
