import test from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { Host, normalizeResult } from "../src/index.js";
test("host scopes requests with a bearer token and encodes storage keys", async () => {
  let observed = "";
  const server = createServer((req, res) => {
    observed = req.url ?? "";
    assert.equal(req.headers.authorization, "Bearer fixture");
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ value: { cursor: 7 } }));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const host = new Host(
      "http://127.0.0.1:" + (server.address() as any).port,
      "fixture",
    );
    assert.deepEqual(await host.get("folder/a"), { cursor: 7 });
    assert.equal(observed, "/internal/plugin/kv/folder%2Fa");
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((e) => (e ? reject(e) : resolve())),
    );
  }
});
test("primitive outputs are normalized to MCP structured objects", () => {
  assert.deepEqual(normalizeResult("hello"), { value: "hello" });
  assert.deepEqual(normalizeResult({ ok: true }), { ok: true });
});

test("plugin notification and model requests carry stable operation metadata", async () => {
  const requests: { path: string; body: unknown }[] = [];
  const server = createServer(async (req, res) => {
    assert.equal(req.headers.authorization, "Bearer fixture");
    assert.equal(req.headers["x-secretary-operation-id"], "run-fixture:tool-1");
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    requests.push({
      path: req.url ?? "",
      body: JSON.parse(Buffer.concat(chunks).toString()),
    });
    res.setHeader("Content-Type", "application/json");
    res.end(
      JSON.stringify(
        req.url?.endsWith("/notifications")
          ? { id: "notification-fixture", status: "saved" }
          : { text: "summary", usage: { output_tokens: 3 } },
      ),
    );
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const host = new Host(
      "http://127.0.0.1:" + (server.address() as any).port,
      "fixture",
      "run-fixture:tool-1",
    );
    assert.deepEqual(
      await host.notify("web-session", "新结果", "stable-notification"),
      { id: "notification-fixture", status: "saved" },
    );
    assert.deepEqual(await host.generate("model", "summarize"), {
      text: "summary",
      usage: { output_tokens: 3 },
    });
    assert.deepEqual(requests, [
      {
        path: "/internal/plugin/notifications",
        body: {
          sessionId: "web-session",
          text: "新结果",
          operationId: "stable-notification",
        },
      },
      {
        path: "/internal/plugin/generate",
        body: { configId: "model", prompt: "summarize", images: [] },
      },
    ]);
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((e) => (e ? reject(e) : resolve())),
    );
  }
});
