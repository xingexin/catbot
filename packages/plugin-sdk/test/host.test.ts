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
