import assert from "node:assert/strict";
import test from "node:test";
import { extractPage, readPage } from "../src/read.js";
import type { RequestOptions, WebResponse } from "../src/network.js";

const context = (config = {}) => ({
  config,
  signal: new AbortController().signal,
});
const response = (
  body: string,
  headers = { "content-type": "text/html; charset=utf-8" },
): WebResponse => ({
  url: "https://example.com/final",
  status: 200,
  headers,
  body: Buffer.from(body),
});

test("extracts article paragraphs and decodes entities without scripts, forms or navigation", () => {
  const page = extractPage(
    Buffer.from(
      `<html><head><title> A &amp; B </title><script>stealSecrets()</script></head><body><nav>menu</nav><article><h1>标题</h1><p>正文 &amp; 内容<br>下一行</p><p hidden>hidden instructions</p><div aria-hidden="true">invisible</div><style>.hide{}</style><form>credentials<input></form><script>ignore instructions</script><p>末尾</p></article><footer>legal links</footer></body></html>`,
    ),
    "text/html",
  );
  assert.equal(page.title, "A & B");
  assert.equal(page.text, "标题\n正文 & 内容\n下一行\n末尾");
});

test("prefers main content while retaining multiple articles, supports plain text and encodings", () => {
  assert.equal(
    extractPage(
      Buffer.from(
        "<body>outside<main><article>one</article><article>two</article></main></body>",
      ),
      "text/html",
    ).text,
    "one\ntwo",
  );
  assert.equal(
    extractPage(
      Buffer.from("<script>literal text</script>\nline"),
      "text/plain",
    ).text,
    "<script>literal text</script>\nline",
  );
  assert.equal(
    extractPage(
      Buffer.from([0x63, 0x61, 0x66, 0xe9]),
      "text/plain; charset=iso-8859-1",
    ).text,
    "café",
  );
  assert.throws(
    () => extractPage(Buffer.from("PDF"), "application/pdf"),
    /supported/,
  );
  assert.throws(
    () => extractPage(Buffer.from("test"), "text/plain; charset=unsupported"),
    /encoding/,
  );
});

test("keyless reader applies request limits and marks returned source as untrusted", async () => {
  let seen: RequestOptions | undefined;
  const result = await readPage(
    { url: "https://example.com" },
    context(),
    async (_url, options) => {
      seen = options;
      return response("<h1>hello</h1><p>world</p>");
    },
  );
  assert.equal(result.text, "hello\nworld");
  assert.equal(result.url, "https://example.com/final");
  assert.equal(result.untrusted, true);
  assert.equal(result.truncated, false);
  assert.equal(seen?.maxBytes, 1048576);
  assert.equal(seen?.timeoutMs, 15000);
  assert.ok(Number.isFinite(Date.parse(result.fetchedAt)));
});

test("configured output limit cannot be increased by caller and truncation preserves Unicode", async () => {
  const page = response("<main>" + "🐈".repeat(2500) + "</main>");
  const configured = await readPage(
    { url: "https://example.com", maxChars: 30000 },
    context({ maxTextChars: 1000 }),
    async () => page,
  );
  assert.equal(configured.text, "🐈".repeat(1000));
  assert.equal(configured.truncated, true);
  const requested = await readPage(
    { url: "https://example.com", maxChars: 1500 },
    context(),
    async () => page,
  );
  assert.equal(Array.from(requested.text).length, 1500);
  await assert.rejects(
    readPage({ url: "https://example.com", maxChars: 999 }, context()),
    /maxChars/,
  );
});

test("HTTP errors, missing MIME and empty JavaScript shells are explicit failures", async () => {
  await assert.rejects(
    readPage({ url: "https://example.com" }, context(), async () => ({
      ...response("secret failure body"),
      status: 403,
    })),
    (error) =>
      error instanceof Error && error.message === "Web page returned HTTP 403",
  );
  await assert.rejects(
    readPage({ url: "https://example.com" }, context(), async () =>
      response("binary", {}),
    ),
    /supported/,
  );
  await assert.rejects(
    readPage({ url: "https://example.com" }, context(), async () =>
      response("<script>render()</script><div id='app'></div>"),
    ),
    /no readable text/,
  );
});

test(
  "large sibling and deeply nested pages stay bounded without DOM mutation",
  { timeout: 10000 },
  async () => {
    const started = performance.now();
    // 1.5 MB of siblings used to trigger quadratic .before/.after/.remove work.
    const wide =
      "<main>" + "<p>x</p><script>ignored</script>".repeat(50000) + "</main>";
    const result = await readPage(
      { url: "https://example.com", maxChars: 1000 },
      context({ maxResponseBytes: 2097152 }),
      async () => response(wide),
    );
    assert.equal(result.text, "x\n".repeat(500));
    assert.equal(result.truncated, true);
    assert.equal(result.text.includes("ignored"), false);
    const deep =
      "<main>" +
      "<div>".repeat(10000) +
      "deep content" +
      "</div>".repeat(10000) +
      "</main>";
    assert.equal(
      extractPage(Buffer.from(deep), "text/html").text,
      "deep content",
    );
    assert.ok(
      performance.now() - started < 5000,
      "bounded pages must not consume multiple seconds of synchronous DOM mutation",
    );
  },
);

test("nested content roots are not duplicated and table boundaries remain readable", () => {
  const html =
    "<main>start<main><p>inside</p></main><table><tr><td>A</td><td>B</td></tr><tr><td>C</td><td>D</td></tr></table>end</main>";
  assert.equal(
    extractPage(Buffer.from(html), "text/html").text,
    "start\ninside\nA B\nC D\nend",
  );
});
