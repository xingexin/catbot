import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";

// Uses a credential already stored in the server vault; never reads the API key.
const env = Object.fromEntries(
  (await readFile(new URL("../deploy/.env", import.meta.url), "utf8"))
    .split("\n")
    .filter((line) => line.includes("=") && !line.startsWith("#"))
    .map((line) => [
      line.slice(0, line.indexOf("=")),
      line.slice(line.indexOf("=") + 1),
    ]),
);
const base =
  process.env.TEST_BASE_URL ?? "http://127.0.0.1:" + (env.WEB_PORT || "5173");
const configId = process.env.TEST_CODEBUDDY_CONFIG ?? "codebuddy-ioa-glm53";
const login = await fetch(base + "/api/login", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ password: env.ADMIN_PASSWORD }),
});
assert(login.ok, "Admin login failed");
const cookie = login.headers.get("set-cookie").split(";")[0];
async function api(path, data) {
  const res = await fetch(base + "/api" + path, {
    method: data === undefined ? "GET" : "POST",
    headers: { Cookie: cookie, "Content-Type": "application/json" },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  const value = await res.json();
  assert(res.ok, JSON.stringify(value));
  return value;
}
const config = (await api("/configs")).find((item) => item.id === configId);
assert(config, "CodeBuddy configuration does not exist");
assert.equal(config.kind, "sdk");
assert.equal(config.provider, "codebuddy");
assert.equal(config.model, "glm-5.3");
const id = "codebuddy-live-" + randomUUID();
await api("/personas", {
  id,
  name: "[真实联调] CodeBuddy 回显",
  systemPrompt: "按要求简洁作答。工具执行须报告实际结果。",
  tools: ["example__echo"],
});
await api("/sessions", {
  id,
  title: "[真实联调] GLM 5.3 对话与插件",
  configId,
  personaId: id,
});
const report = {
  date: new Date().toISOString(),
  model: config.model,
  sessionId: id,
  status: "running",
  runs: [],
};
await mkdir("data/acceptance", { recursive: true });
const reportPath = "data/acceptance/codebuddy-live-report.json";
async function save() {
  await writeFile(reportPath, JSON.stringify(report, null, 2));
}
async function run(message) {
  const queued = await api("/sessions/" + id + "/messages", {
    message,
    requestId: randomUUID(),
  });
  console.log("Started " + queued.id);
  const response = await fetch(base + "/api/runs/" + queued.id + "/events", {
    headers: { Cookie: cookie },
    signal: AbortSignal.timeout(180_000),
  });
  assert(response.ok);
  const events = (await response.text())
    .split("\n")
    .filter((line) => line.startsWith("data:"))
    .map((line) => JSON.parse(line.slice(5)));
  const result = await api("/runs/" + queued.id);
  const record = {
    id: result.id,
    status: result.status,
    nativeId: result.nativeId,
    result: result.result,
    error: result.error,
    usage: result.usage,
    streamed: events.some((event) => event.type === "text.delta"),
    events: events.filter((event) =>
      ["sdk.initialized", "tool.started", "tool.completed", "error"].includes(
        event.type,
      ),
    ),
  };
  report.runs.push(record);
  await save();
  console.log(JSON.stringify(record));
  assert.equal(result.status, "completed", result.error);
  for (const event of record.events.filter(
    (event) => event.type === "sdk.initialized",
  )) {
    const nativeTools = (event.data.tools ?? []).filter(
      (name) => !/^mcp__secretary__[a-zA-Z0-9_-]+$/.test(name),
    );
    assert.deepEqual(
      nativeTools,
      [],
      "Unexpected native SDK tools remain advertised: " +
        nativeTools.join(", "),
    );
  }
  return record;
}
try {
  const marker = "glm53-" + randomUUID().slice(0, 8);
  const first = await run(
    "记住验证标记 " + marker + "。只回复 CODEBUDDY_GLM53_OK，不要调用工具。",
  );
  assert.match(first.result, /CODEBUDDY_GLM53_OK/);
  assert(first.streamed, "Missing streaming text");
  const second = await run(
    "请通过工具调用接口实际调用 mcp__secretary__example__echo，参数 text 使用我上一条消息中的验证标记。收到结果后报告原文和字符数。",
  );
  assert(first.nativeId);
  assert.equal(
    second.nativeId,
    first.nativeId,
    "Native session was not resumed",
  );
  const calls = second.events.filter(
    (event) =>
      event.type === "tool.completed" && event.data.name === "example__echo",
  );
  assert.equal(calls.length, 1, "Expected one actual echo tool completion");
  assert.equal(calls[0].data.isError, false);
  assert.equal(
    calls[0].data.result.text,
    marker,
    "Resumed context/tool input mismatch",
  );
  assert(
    second.result.includes(marker),
    "Missing actual tool result in final reply",
  );
  report.status = "passed";
} catch (error) {
  report.status = "failed";
  report.error = error.message;
  process.exitCode = 1;
} finally {
  await save();
  console.log(
    "CodeBuddy live verification: " + report.status + " (" + reportPath + ")",
  );
}
