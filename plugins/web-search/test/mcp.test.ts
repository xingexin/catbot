import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { Ajv } from "ajv";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
test("bundled plugin speaks real MCP stdio, validates inputs and fails without search credentials", async () => {
  const directory = await mkdtemp(join(tmpdir(), "catbot-web-search-"));
  const client = new Client({ name: "web-search-test", version: "1.0.0" });
  try {
    const manifest = await readFile(join(root, "plugin.json"), "utf8");
    await writeFile(join(directory, "plugin.json"), manifest);
    await build({
      entryPoints: [join(root, "src/index.ts")],
      bundle: true,
      platform: "node",
      format: "cjs",
      outfile: join(directory, "index.cjs"),
      logLevel: "silent",
    });
    const transport = new StdioClientTransport({
      command: process.execPath,
      args: [join(directory, "index.cjs")],
      cwd: directory,
      stderr: "pipe",
      env: { SECRETARY_PLUGIN_CONFIG: "{}" },
    });
    let stderr = "";
    transport.stderr?.on("data", (data) => {
      stderr += data.toString();
    });
    await client.connect(transport);
    const tools = await client.listTools();
    assert.deepEqual(
      tools.tools.map((tool) => tool.name),
      ["search", "read"],
    );
    const query = await client.callTool({
      name: "search",
      arguments: { query: "catbot" },
    });
    assert.equal(query.isError, true);
    assert.match(JSON.stringify(query), /API Key|apiKey|API key/i);
    const local = await client.callTool({
      name: "read",
      arguments: { url: "http://127.0.0.1/" },
    });
    assert.equal(local.isError, true);
    const invalid = await client.callTool({
      name: "search",
      arguments: { query: "catbot", limit: 100 },
    });
    assert.equal(invalid.isError, true);
    assert.equal(stderr, "");
  } finally {
    await client.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("manifest allows keyless reading and rejects unknown provider IDs and oversized input", async () => {
  const manifest = JSON.parse(
    await readFile(join(root, "plugin.json"), "utf8"),
  );
  assert.equal(manifest.tools[0].retrySafe, false);
  assert.equal(manifest.tools[1].retrySafe, true);
  const ajv = new Ajv({ strict: false });
  ajv.addFormat("password", true);
  const config = ajv.compile(manifest.configSchema);
  assert.equal(config({}), true);
  assert.equal(config({ provider: 1 }), true);
  assert.equal(config({ provider: 2 }), true);
  assert.equal(config({ provider: 0 }), false);
  assert.equal(config({ provider: "Tavily" }), false);
  assert.equal(config({ tavilyApiKey: "x".repeat(1025) }), false);
  const search = ajv.compile(manifest.tools[0].inputSchema);
  assert.equal(search({ query: "hello", baseUrl: "http://localhost" }), false);
  const read = ajv.compile(manifest.tools[1].inputSchema);
  assert.equal(read({ url: "https://example.com", maxChars: 100000 }), false);
  assert.deepEqual(
    manifest.tools.map((tool: any) => tool.permissions),
    [["web.search"], ["web.read"]],
  );
});
