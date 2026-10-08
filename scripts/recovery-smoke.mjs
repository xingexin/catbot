import { readFile, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import { composeContainer, dockerEnvironment } from "./deployment.mjs";
const env = Object.fromEntries(
  (await readFile(".env", "utf8"))
    .split("\n")
    .filter((s) => s.includes("="))
    .map((s) => {
      const i = s.indexOf("=");
      return [s.slice(0, i), s.slice(i + 1)];
    }),
);
const base = "http://127.0.0.1:" + env.WEB_PORT;
const login = await fetch(base + "/api/login", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ password: env.ADMIN_PASSWORD }),
});
assert(login.ok);
const cookie = login.headers.get("set-cookie").split(";")[0];
async function api(path, body) {
  const res = await fetch(base + "/api" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json", Cookie: cookie },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const value = await res.json();
  assert(res.ok, JSON.stringify(value));
  return value;
}
const task = await api("/tasks", {
  name: "[恢复验收] 持久化 Timer",
  kind: "once",
  runAt: new Date(Date.now() + 1500).toISOString(),
  configId: "acceptance-openai-chat",
  personaId: "secretary",
  steps: [
    {
      id: "first",
      kind: "tool",
      tool: "example__echo",
      arguments: { text: "durable" },
    },
    {
      id: "second",
      kind: "tool",
      tool: "example__echo",
      arguments: { text: "${steps.first.text}" },
      delaySec: 20,
    },
  ],
});
let execution;
for (let i = 0; i < 100; i++) {
  execution = (await api("/executions")).find((x) => x.taskId === task.id);
  if (execution?.results?.first) break;
  await new Promise((r) => setTimeout(r, 150));
}
assert(execution?.results?.first, "first step did not persist");
assert.equal(
  execution.status,
  "running",
  "timer did not retain the running workflow",
);
const started = Date.now();
execFileSync(
  "docker",
  [
    "restart",
    "--time",
    "2",
    composeContainer("backend"),
  ],
  { stdio: "pipe", env: dockerEnvironment },
);
for (let i = 0; i < 200; i++) {
  try {
    const current = (await api("/executions")).find(
      (x) => x.id === execution.id,
    );
    if (current?.status === "completed") {
      assert.equal(current.results.first.text, "durable");
      assert.equal(current.results.second.text, "durable");
      const report = {
        status: "passed",
        taskId: task.id,
        executionId: execution.id,
        workerRestart: true,
        persistedTimer: true,
        elapsedMs: Date.now() - started,
      };
      await writeFile(
        "data/acceptance/recovery-report.json",
        JSON.stringify(report, null, 2),
      );
      process.stdout.write(JSON.stringify(report) + "\n");
      process.exit(0);
    }
    if (current && current.status !== "running")
      throw new Error(JSON.stringify(current));
  } catch (error) {
    if (i > 40) throw error;
  }
  await new Promise((r) => setTimeout(r, 250));
}
throw new Error("recovery timed out");
