import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";

// The model key stays in the server vault. This creates only a Web notification.
const env = Object.fromEntries(
  (await readFile(".env", "utf8"))
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
  const response = await fetch(base + "/api" + path, {
    method: data === undefined ? "GET" : "POST",
    headers: { Cookie: cookie, "Content-Type": "application/json" },
    body: data === undefined ? undefined : JSON.stringify(data),
    signal: AbortSignal.timeout(15000),
  });
  const value = await response.json();
  assert(response.ok, JSON.stringify(value));
  return value;
}
const config = (await api("/configs")).find((item) => item.id === configId);
assert.equal(config?.kind, "sdk");
assert.equal(config?.provider, "codebuddy");
assert.equal(config?.model, "glm-5.3");
const id = "codebuddy-task-live-" + randomUUID();
const marker = "scheduled-" + randomUUID().slice(0, 12);
const report = {
  date: new Date().toISOString(),
  model: config.model,
  sessionId: id,
  marker,
  status: "running",
  platform: "web_only",
  externalMessageSent: false,
};
const reportPath = "data/acceptance/codebuddy-task-live-report.json";
await mkdir("data/acceptance", { recursive: true });
async function save() {
  await writeFile(reportPath, JSON.stringify(report, null, 2));
}
try {
  await api("/personas", {
    id,
    name: "[真实联调] CodeBuddy 定时提醒",
    systemPrompt:
      "按照用户要求创建单次任务，所有操作只能通过秘书 MCP 工具完成。不要重复创建任务，报告实际结果。",
    tools: ["system__task_create", "example__echo"],
  });
  const session = await api("/sessions", {
    id,
    title: "[真实联调] GLM 5.3 创建定时提醒",
    configId,
    personaId: id,
    channel: "web",
  });
  assert.notEqual(session.channel, "qq");
  const taskRequest = {
    name: "[真实联调] CodeBuddy 提醒 " + marker,
    kind: "once",
    timeZone: "Asia/Shanghai",
    runAt: new Date(Date.now() + 90000).toISOString(),
    notify: true,
    notifyText: "${steps.reminder.text}",
    steps: [
      {
        id: "reminder",
        kind: "tool",
        tool: "example__echo",
        arguments: { text: marker },
      },
    ],
  };
  report.requestedRunAt = taskRequest.runAt;
  const run = await api("/sessions/" + id + "/messages", {
    message:
      "请实际调用一次 mcp__secretary__system__task_create 创建以下单次提醒，严格使用给定参数。不要立刻执行 echo，不要修改时间，不要创建多个任务，也不要执行其他动作。成功后简短告诉我任务 ID：\n" +
      JSON.stringify(taskRequest),
    requestId: randomUUID(),
  });
  report.runId = run.id;
  console.log("Started Agent task creation " + run.id);
  const stream = await fetch(base + "/api/runs/" + run.id + "/events", {
    headers: { Cookie: cookie },
    signal: AbortSignal.timeout(180000),
  });
  assert(stream.ok);
  const events = (await stream.text())
    .split("\n")
    .filter((line) => line.startsWith("data:"))
    .map((line) => JSON.parse(line.slice(5)));
  const result = await api("/runs/" + run.id);
  report.runStatus = result.status;
  report.runError = result.error;
  const calls = events.filter(
    (event) =>
      event.type === "tool.completed" &&
      event.data.name === "system__task_create",
  );
  report.taskCreationCalls = calls.length;
  assert.equal(result.status, "completed", result.error);
  assert.equal(calls.length, 1, "Expected exactly one actual task creation");
  assert.equal(calls[0].data.isError, false, calls[0].data.error);
  const task = calls[0].data.result;
  report.taskId = task.id;
  assert.equal(task.sessionId, id);
  assert.equal(task.kind, "once");
  assert.equal(task.notify, true);
  assert.equal(task.notifyText, taskRequest.notifyText);
  assert.equal(
    new Date(task.runAt).getTime(),
    new Date(taskRequest.runAt).getTime(),
  );
  assert.deepEqual(
    task.steps.map(({ id, kind, tool, arguments: args }) => ({
      id,
      kind,
      tool,
      arguments: args,
    })),
    taskRequest.steps,
  );
  for (const event of events.filter(
    (event) => event.type === "sdk.initialized",
  ))
    assert.ok(
      event.data.tools.every((name) => name.startsWith("mcp__secretary__")),
      "Unexpected native tool advertised",
    );
  report.status = "task_created";
  await save();
  console.log(
    "Task created " +
      task.id +
      "; awaiting real Temporal execution at " +
      task.runAt,
  );
  const deadline = Date.now() + 180000;
  while (Date.now() < deadline) {
    const executions = (await api("/executions")).filter(
      (item) => item.taskId === task.id,
    );
    assert.ok(
      executions.length <= 1,
      "Single reminder created duplicate executions",
    );
    const execution = executions[0];
    if (execution && !["queued", "running"].includes(execution.status)) {
      report.execution = {
        id: execution.id,
        status: execution.status,
        results: execution.results,
        error: execution.error,
        startedAt: execution.startedAt,
        finishedAt: execution.finishedAt,
      };
      assert.equal(execution.status, "completed", execution.error);
      assert.equal(execution.results.reminder.text, marker);
      const notifications = (await api("/notifications")).filter(
        (item) => item.taskId === task.id,
      );
      assert.equal(
        notifications.length,
        1,
        "Expected one persisted Web notification",
      );
      const notification = notifications[0];
      assert.equal(notification.sessionId, id);
      assert.equal(notification.status, "saved");
      assert.equal(notification.text, marker);
      report.notification = {
        id: notification.id,
        status: notification.status,
        text: notification.text,
        operationId: notification.operationId,
        attempts: notification.attempts,
      };
      report.status = "passed";
      break;
    }
    await new Promise((resolve) => setTimeout(resolve, 2000));
  }
  assert.equal(
    report.status,
    "passed",
    "Timed out waiting for Temporal execution and Web notification",
  );
} catch (error) {
  report.status = "failed";
  report.error = error.message;
  process.exitCode = 1;
} finally {
  report.finishedAt = new Date().toISOString();
  await save();
  console.log(JSON.stringify(report));
  console.log(
    "CodeBuddy scheduled task verification: " +
      report.status +
      " (" +
      reportPath +
      ")",
  );
}
