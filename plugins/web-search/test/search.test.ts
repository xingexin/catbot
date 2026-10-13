import test from "node:test";
import assert from "node:assert/strict";
import { parseConfig, SearchProvider } from "../src/config.js";
import { searchWeb } from "../src/search.js";
import type { safeRequest } from "../src/network.js";

type Request = typeof safeRequest;
const controller = () => new AbortController();
const context = (config: Record<string, unknown> = {}) => ({
  config: {
    tavilyApiKey: "test-search-secret",
    braveApiKey: "test-brave-secret",
    ...config,
  },
  signal: controller().signal,
});
const tavilyRow = (id = "1") => ({
  title: `Result ${id}`,
  url: `https://example.com/${id}`,
  content: `Snippet ${id}`,
});
function setup(body: unknown, status = 200) {
  const calls: Array<{ url: string; options: Parameters<Request>[1] }> = [];
  const request: Request = async (url, options) => {
    calls.push({ url, options });
    return {
      url,
      status,
      headers: { "content-type": "application/json" },
      body: Buffer.from(JSON.stringify(body)),
    };
  };
  return { request, calls };
}

test("configuration uses fixed numeric providers and bounded numeric limits", () => {
  assert.deepEqual(parseConfig({}), {
    provider: SearchProvider.Tavily,
    tavilyApiKey: "",
    braveApiKey: "",
    apiKey: "",
    timeoutSec: 15,
    maxResults: 5,
    maxResponseBytes: 1048576,
    maxTextChars: 12000,
  });
  assert.equal(
    parseConfig({ provider: SearchProvider.Brave, braveApiKey: " test " })
      .apiKey,
    "test",
  );
  for (const config of [
    { provider: SearchProvider.Unknown },
    { provider: "1" },
    { provider: "tavily" },
    { provider: 3 },
    { provider: null },
    { provider: 1.5 },
    { tavilyApiKey: 123 },
    { tavilyApiKey: null },
    { tavilyApiKey: "secret\r\nheader" },
    { tavilyApiKey: "x".repeat(1025) },
    { braveApiKey: 123 },
    { braveApiKey: null },
    { braveApiKey: "secret\r\nheader" },
    { braveApiKey: "x".repeat(1025) },
    { apiKey: "old-shared-secret" },
    { timeoutSec: 0 },
    { timeoutSec: 61 },
    { timeoutSec: "15" },
    { maxResults: 0 },
    { maxResults: 11 },
    { maxResults: 1.5 },
    { maxResponseBytes: 65535 },
    { maxResponseBytes: 2097153 },
    { maxTextChars: 999 },
    { maxTextChars: 30001 },
    { maxTextChars: null },
  ])
    assert.throws(() => parseConfig(config));
});

test("Tavily uses bounded basic search without provider-generated answer or raw body", async () => {
  const { request, calls } = setup({ results: [tavilyRow()] });
  const ctx = context();
  const result = await searchWeb(
    { query: "  中文 hello  ", limit: 3 },
    ctx,
    request,
  );
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "https://api.tavily.com/search");
  const options = calls[0].options;
  assert.equal(options.method, "POST");
  assert.equal(options.headers?.Authorization, "Bearer test-search-secret");
  assert.equal(options.maxRedirects, 0);
  assert.equal(options.timeoutMs, 15000);
  assert.equal(options.maxBytes, 1048576);
  assert.equal(options.signal, ctx.signal);
  assert.deepEqual(JSON.parse(options.body!), {
    query: "中文 hello",
    max_results: 3,
    search_depth: "basic",
    topic: "general",
    include_answer: false,
    include_raw_content: false,
    include_images: false,
    auto_parameters: false,
  });
  assert.deepEqual(result.results, [
    { title: "Result 1", url: "https://example.com/1", snippet: "Snippet 1" },
  ]);
  assert.equal(result.provider, SearchProvider.Tavily);
  assert.equal(result.query, "中文 hello");
  assert.equal(result.untrusted, true);
  assert.ok(Number.isFinite(Date.parse(result.fetchedAt)));
  assert.ok(!JSON.stringify(result).includes("test-search-secret"));
});

test("Brave uses its authentication header and encoded query, normalizing web results", async () => {
  const { request, calls } = setup({
    web: {
      results: [
        {
          title: "Brave result",
          url: "https://example.com/brave",
          description: "Relevant excerpt",
        },
      ],
    },
  });
  const result = await searchWeb(
    { query: "中文 + symbol & test" },
    context({ provider: SearchProvider.Brave }),
    request,
  );
  assert.equal(calls.length, 1);
  const url = new URL(calls[0].url);
  assert.equal(
    url.origin + url.pathname,
    "https://api.search.brave.com/res/v1/web/search",
  );
  assert.equal(url.searchParams.get("q"), "中文 + symbol & test");
  assert.equal(url.searchParams.get("count"), "5");
  assert.equal(url.searchParams.get("text_decorations"), "false");
  assert.equal(url.searchParams.get("result_filter"), "web");
  assert.ok(!url.href.includes("test-search-secret"));
  assert.equal(
    calls[0].options.headers?.["X-Subscription-Token"],
    "test-brave-secret",
  );
  assert.equal(calls[0].options.headers?.Authorization, undefined);
  assert.equal(calls[0].options.body, undefined);
  assert.equal(calls[0].options.maxRedirects, 0);
  assert.equal(result.provider, SearchProvider.Brave);
  assert.deepEqual(result.results, [
    {
      title: "Brave result",
      url: "https://example.com/brave",
      snippet: "Relevant excerpt",
    },
  ]);
});

test("search limits cannot exceed administrator ceiling and returned text stays bounded", async () => {
  const long = {
    ...tavilyRow(),
    title: "T".repeat(1000),
    content: "S".repeat(4000),
  };
  const { request, calls } = setup({
    results: [long, tavilyRow("2"), tavilyRow("3")],
  });
  const result = await searchWeb(
    { query: "bounded", limit: 10 },
    context({ maxResults: 2, timeoutSec: 8, maxResponseBytes: 65536 }),
    request,
  );
  assert.equal(JSON.parse(calls[0].options.body!).max_results, 2);
  assert.equal(calls[0].options.timeoutMs, 8000);
  assert.equal(calls[0].options.maxBytes, 65536);
  assert.equal(result.results.length, 2);
  assert.equal(result.results[0].title.length, 500);
  assert.equal(result.results[0].snippet.length, 2000);
});

test("missing Key and invalid arguments fail before a request is sent", async () => {
  const { request, calls } = setup({ results: [] });
  await assert.rejects(
    searchWeb({ query: "x" }, context({ tavilyApiKey: " " }), request),
    /尚未配置 Tavily.*API Key/,
  );
  await assert.rejects(
    searchWeb(
      { query: "x" },
      context({ provider: SearchProvider.Brave, braveApiKey: "" }),
      request,
    ),
    /尚未配置 Brave/,
  );
  for (const args of [
    { query: " " },
    { query: "x".repeat(501) },
    { query: "x", limit: 0 },
    { query: "x", limit: 11 },
    { query: "x", limit: 1.5 },
  ])
    await assert.rejects(searchWeb(args, context(), request));
  await assert.rejects(searchWeb({ query: 12 } as any, context(), request));
  await assert.rejects(
    searchWeb({ query: "x", limit: "2" } as any, context(), request),
  );
  assert.equal(calls.length, 0);
});

test("empty arrays are real empty results; malformed envelopes never become successful empty results", async () => {
  for (const [provider, body] of [
    [SearchProvider.Tavily, { results: [] }],
    [SearchProvider.Brave, { web: { results: [] } }],
  ] as const) {
    const { request } = setup(body);
    assert.deepEqual(
      (await searchWeb({ query: "nothing" }, context({ provider }), request))
        .results,
      [],
    );
  }
  for (const body of [
    null,
    [],
    {},
    { error: "bad key" },
    { results: null },
    { results: {} },
    { results: [null] },
    { results: [{ title: "x" }] },
  ]) {
    const { request } = setup(body);
    await assert.rejects(
      searchWeb({ query: "x" }, context(), request),
      /无效的搜索结果格式/,
    );
  }
  for (const body of [{}, { web: [] }, { web: { results: null } }]) {
    const { request } = setup(body);
    await assert.rejects(
      searchWeb(
        { query: "x" },
        context({ provider: SearchProvider.Brave }),
        request,
      ),
      /无效的搜索结果格式/,
    );
  }
  const invalidJSON: Request = async (url) => ({
    url,
    status: 200,
    headers: {},
    body: Buffer.from("test-search-secret"),
  });
  await assert.rejects(
    searchWeb({ query: "x" }, context(), invalidJSON),
    (error: Error) => {
      assert.ok(!error.message.includes("test-search-secret"));
      return /无效的搜索结果格式/.test(error.message);
    },
  );
});

test("unsafe URLs and malformed result entries are never returned as sources", async () => {
  for (const fields of [
    { url: "javascript:alert(1)" },
    { url: "file:///etc/passwd" },
    { url: "https://user:pass@example.com" },
    { url: "/relative" },
    { url: `https://example.com/${"中".repeat(1300)}` },
    { url: "" },
    { url: `https://example.com/${"x".repeat(4096)}` },
    { title: " " },
    { title: 123 },
    { content: {} },
    { content: null },
  ]) {
    const { request } = setup({ results: [{ ...tavilyRow(), ...fields }] });
    await assert.rejects(
      searchWeb({ query: "x" }, context(), request),
      /无效的搜索结果格式/,
    );
  }
});

test("provider failures are actionable without echoing provider bodies or retrying", async () => {
  for (const [status, pattern] of [
    [401, /鉴权失败/],
    [403, /鉴权失败/],
    [429, /额度不足或请求受限/],
    [432, /额度不足或请求受限/],
    [500, /服务请求失败/],
    [302, /重定向/],
  ] as const) {
    const { request, calls } = setup({ error: "test-search-secret" }, status);
    await assert.rejects(
      searchWeb({ query: "x" }, context(), request),
      (error: Error) => {
        assert.match(error.message, pattern);
        assert.ok(!error.message.includes("test-search-secret"));
        return true;
      },
    );
    assert.equal(calls.length, 1);
  }
});

test("network failures do not expose request secrets and never trigger provider fallback", async () => {
  let count = 0;
  const request: Request = async () => {
    count++;
    throw new Error("Bearer test-search-secret exposed by upstream");
  };
  await assert.rejects(
    searchWeb({ query: "x" }, context(), request),
    (error: Error) => {
      assert.match(error.message, /没有自动重试/);
      assert.ok(!error.message.includes("test-search-secret"));
      return true;
    },
  );
  assert.equal(count, 1);
});

test("cancellation is respected before and after network execution without exposing abort reasons", async () => {
  const before = controller();
  before.abort(new Error("test-search-secret"));
  const { request, calls } = setup({ results: [] });
  await assert.rejects(
    searchWeb({ query: "x" }, { ...context(), signal: before.signal }, request),
    /已取消/,
  );
  assert.equal(calls.length, 0);
  const during = controller();
  let count = 0;
  const cancel: Request = async (url) => {
    count++;
    during.abort(new Error("test-search-secret"));
    return {
      url,
      status: 200,
      headers: {},
      body: Buffer.from('{"results":[]}'),
    };
  };
  await assert.rejects(
    searchWeb({ query: "x" }, { ...context(), signal: during.signal }, cancel),
    /已取消/,
  );
  assert.equal(count, 1);
});

test("provider changes use only that provider's configured credential", async () => {
  const { request, calls } = setup({ web: { results: [] } });
  for (const config of [
    { provider: SearchProvider.Brave, tavilyApiKey: "tavily-only" },
    { provider: SearchProvider.Tavily, braveApiKey: "brave-only" },
  ]) {
    await assert.rejects(
      searchWeb(
        { query: "x" },
        { config, signal: controller().signal },
        request,
      ),
      /尚未配置/,
    );
  }
  assert.equal(calls.length, 0);
  await searchWeb(
    { query: "x" },
    context({ provider: SearchProvider.Brave }),
    request,
  );
  assert.equal(
    calls[0].options.headers?.["X-Subscription-Token"],
    "test-brave-secret",
  );
  assert.ok(!JSON.stringify(calls[0]).includes("test-search-secret"));
  const tavily = setup({ results: [] });
  await searchWeb({ query: "x" }, context(), tavily.request);
  assert.equal(
    tavily.calls[0].options.headers?.Authorization,
    "Bearer test-search-secret",
  );
  assert.ok(!JSON.stringify(tavily.calls[0]).includes("test-brave-secret"));
});

test("Brave nullable web results require a recognizable successful response", async () => {
  for (const body of [
    { type: "search", query: { original: "x" } },
    { type: "search", query: { original: "x" }, web: null },
  ]) {
    const { request } = setup(body);
    const result = await searchWeb(
      { query: "x" },
      context({ provider: SearchProvider.Brave }),
      request,
    );
    assert.deepEqual(result.results, []);
  }
  for (const body of [
    { type: "ErrorResponse", query: { original: "x" }, web: null },
    { type: "search", query: null, web: null },
    { type: "search", query: { original: "" }, web: null },
    { type: "search", query: { original: "x" }, error: { message: "failed" } },
    { type: "search", query: { original: "x" }, web: { results: null } },
  ]) {
    const { request } = setup(body);
    await assert.rejects(
      searchWeb(
        { query: "x" },
        context({ provider: SearchProvider.Brave }),
        request,
      ),
      /无效的搜索结果格式/,
    );
  }
});

test("Brave nullable description becomes an empty snippet without discarding its source", async () => {
  const { request } = setup({
    web: {
      results: [
        { title: "Source", url: "https://example.com/", description: null },
      ],
    },
  });
  const result = await searchWeb(
    { query: "x" },
    context({ provider: SearchProvider.Brave }),
    request,
  );
  assert.deepEqual(result.results, [
    { title: "Source", url: "https://example.com/", snippet: "" },
  ]);
});
