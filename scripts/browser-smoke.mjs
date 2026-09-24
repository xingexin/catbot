import { chromium } from "@playwright/test";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { resolve, dirname } from "node:path";
import assert from "node:assert/strict";
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const env = Object.fromEntries(
  (await readFile(resolve(root, ".env"), "utf8"))
    .split("\n")
    .filter((s) => s.includes("="))
    .map((s) => {
      const i = s.indexOf("=");
      return [s.slice(0, i), s.slice(i + 1)];
    }),
);
const base = process.env.TEST_WEB_URL ?? "http://127.0.0.1:" + env.WEB_PORT;
const out = resolve(root, "data/acceptance");
await mkdir(out, { recursive: true });
const browser = await chromium.launch({
  headless: true,
  executablePath:
    process.env.CHROME_PATH ??
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
});
const context = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
});
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
async function api(path, body) {
  const response = await context.request.fetch(base + "/api" + path, {
    method: body === undefined ? "GET" : "POST",
    ...(body === undefined ? {} : { data: body }),
  });
  const data = await response.json();
  assert(response.ok(), "API failed: " + path + " " + JSON.stringify(data));
  return data;
}
async function waitRun(id) {
  for (let i = 0; i < 200; i++) {
    const run = await api("/runs/" + id);
    if (!["queued", "running"].includes(run.status)) {
      assert.equal(run.status, "completed", run.error);
      return run;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error("run timed out");
}
const report = { protocols: [], browser: [], video: null, errors };
try {
  await page.goto(base);
  await page.getByLabel("管理员密码").fill(env.ADMIN_PASSWORD);
  await page.getByRole("button", { name: /登\s*录/ }).click();
  await page.getByRole("heading", { name: "对话", exact: true }).waitFor();
  report.browser.push("login");
  for (const directory of ["example", "mail", "video"])
    await api("/plugins/register", { directory });
  for (const protocol of ["openai-chat", "openai-responses", "anthropic"]) {
    const config = await api("/configs", {
      id: "acceptance-" + protocol,
      name: "[本地验收] " + protocol,
      kind: "api",
      protocol,
      model: "fixture-only",
      baseUrl: "http://fixture:9090/v1",
      maxSteps: 4,
      maxTokens: 1024,
      timeoutSec: 60,
      capabilities: { tools: true, stream: true, images: true },
    });
    const session = await api("/sessions", {
      id: "acceptance-" + protocol,
      title: "[本地验收] " + protocol,
      configId: config.id,
      personaId: "secretary",
    });
    const run = await api("/sessions/" + session.id + "/messages", {
      message: "调用示例工具回显 hello",
      requestId: crypto.randomUUID(),
    });
    const done = await waitRun(run.id);
    assert.match(done.result, /工具调用已完成/);
    report.protocols.push({
      protocol,
      status: "passed",
      runId: done.id,
      live: false,
    });
  }
  await page.getByRole("button", { name: /刷\s*新/ }).click();
  await page.getByRole("menuitem", { name: "执行配置" }).click();
  await page.getByRole("button", { name: "添加配置" }).click();
  await page.getByLabel("配置名称").fill("[浏览器验收] API");
  await page.getByLabel("Model", { exact: true }).fill("fixture-only");
  await page.getByLabel("Base URL").fill("http://fixture:9090/v1");
  await page.getByRole("button", { name: /确\s*定/ }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  report.browser.push("create execution config through form");
  await page.getByRole("menuitem", { name: "对话" }).click();
  await page.getByRole("button", { name: /本地验收.*openai-chat/ }).click();
  const prompt = "再调用一次示例工具 · " + crypto.randomUUID().slice(0, 8);
  await page
    .getByPlaceholder("说说你想做的事… Shift + Enter 换行")
    .fill(prompt);
  const [sent] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes("/messages") &&
        response.request().method() === "POST",
    ),
    page.getByRole("button", { name: /发\s*送/ }).click(),
  ]);
  const submitted = await sent.json();
  await waitRun(submitted.id);
  await page.getByText(prompt, { exact: true }).waitFor();
  await page.waitForFunction(
    () => document.querySelector("textarea")?.value === "",
  );
  await page
    .getByRole("button", { name: /停\s*止/ })
    .waitFor({ state: "hidden", timeout: 20000 });
  await page
    .getByText("工具调用已完成（本地协议验收端点）。", { exact: true })
    .first()
    .waitFor();
  await page.mouse.move(1400, 70);
  await page.waitForFunction(() => {
    const pane = document.querySelector(".messages");
    if (!pane?.lastElementChild) return false;
    const bounds = pane.getBoundingClientRect();
    const end = pane.lastElementChild.getBoundingClientRect();
    return end.bottom <= bounds.bottom + 1 && end.top >= bounds.top;
  });
  await page.screenshot({
    path: resolve(out, "conversation.png"),
    fullPage: true,
  });
  report.browser.push("SSE chat and plugin tool result");
  for (const label of [
    "定时任务",
    "文件与解析",
    "人格",
    "插件",
    "运行记录",
    "系统与凭证",
  ]) {
    await page.getByRole("menuitem", { name: label }).click();
    await page.getByRole("heading", { name: label, exact: true }).waitFor();
    report.browser.push(label);
  }
  await page.getByRole("menuitem", { name: "人格" }).click();
  await page.getByRole("button", { name: "创建人格" }).click();
  await page.getByLabel("名称", { exact: true }).fill("验收人格");
  await page.getByLabel("系统提示词", { exact: true }).fill("简洁、可靠。");
  await page.getByRole("button", { name: /确\s*定/ }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page.screenshot({ path: resolve(out, "personas.png"), fullPage: true });
  report.browser.push("persona create");
  const task = await api("/tasks", {
    name: "[本地验收] 两步工具任务",
    kind: "manual",
    timeZone: "Asia/Shanghai",
    configId: "acceptance-openai-chat",
    personaId: "secretary",
    sessionId: "acceptance-openai-chat",
    notify: true,
    steps: [
      {
        id: "one",
        kind: "tool",
        tool: "example__echo",
        arguments: { text: "step one" },
      },
      {
        id: "two",
        kind: "tool",
        tool: "example__echo",
        arguments: { text: "${steps.one.text}" },
        delaySec: 1,
      },
    ],
  });
  const trigger = await api("/tasks/" + task.id + "/trigger", {});
  for (let i = 0; i < 100; i++) {
    const xs = await api("/executions"),
      x = xs.find((x) => x.id === trigger.executionId);
    if (x && x.status !== "running") {
      assert.equal(x.status, "completed", JSON.stringify(x));
      assert.equal(x.results.two.text, "step one");
      report.browser.push("Temporal task with typed step reference");
      break;
    }
    if (i === 99) throw new Error("task timed out");
    await new Promise((r) => setTimeout(r, 300));
  }
  // Real FFmpeg processing with deterministic ASR/vision fixture responses.
  const docker = [
    "--context",
    process.env.TEST_DOCKER_CONTEXT ?? "colima-secretary",
  ];
  execFileSync(
    "docker",
    [
      ...docker,
      "exec",
      "secretary-backend-1",
      "ffmpeg",
      "-nostdin",
      "-v",
      "error",
      "-f",
      "lavfi",
      "-i",
      "color=c=green:s=320x240:d=5",
      "-f",
      "lavfi",
      "-i",
      "sine=frequency=440:duration=5",
      "-c:v",
      "mpeg4",
      "-c:a",
      "aac",
      "-shortest",
      "-y",
      "/tmp/acceptance.mp4",
    ],
    { stdio: "pipe" },
  );
  execFileSync(
    "docker",
    [
      ...docker,
      "cp",
      "secretary-backend-1:/tmp/acceptance.mp4",
      resolve(out, "fixture.mp4"),
    ],
    { stdio: "pipe" },
  );
  const upload = await context.request.post(base + "/api/files", {
    multipart: {
      file: {
        name: "acceptance.mp4",
        mimeType: "video/mp4",
        buffer: await readFile(resolve(out, "fixture.mp4")),
      },
    },
  });
  assert(upload.ok());
  const artifact = await upload.json();
  await api("/plugins/video/configure", {
    config: {
      transcriptionConfigId: "acceptance-openai-chat",
      transcriptionModel: "fixture-asr",
      visionConfigId: "acceptance-openai-chat",
    },
    grants: ["files", "models", "storage"],
  });
  await api("/plugins/video/enable", { enabled: true });
  const videoTask = await api("/tasks", {
    name: "[本地验收] 视频解析",
    kind: "manual",
    configId: "acceptance-openai-chat",
    personaId: "secretary",
    steps: [
      {
        id: "parse",
        kind: "tool",
        tool: "video__parse",
        arguments: { artifactId: artifact.id },
      },
    ],
  });
  const videoRun = await api("/tasks/" + videoTask.id + "/trigger", {});
  for (let i = 0; i < 200; i++) {
    const xs = await api("/executions"),
      x = xs.find((x) => x.id === videoRun.executionId);
    if (x && x.status !== "running") {
      assert.equal(x.status, "completed", JSON.stringify(x));
      assert.equal(x.results.parse.complete, true);
      report.video = {
        status: "passed",
        executionId: x.id,
        realFFmpeg: true,
        liveModels: false,
      };
      break;
    }
    if (i === 199) throw new Error("video timed out");
    await new Promise((r) => setTimeout(r, 300));
  }
  await page.getByRole("menuitem", { name: "文件与解析" }).click();
  await page.getByRole("button", { name: /刷\s*新/ }).click();
  await page.screenshot({
    path: resolve(out, "artifacts.png"),
    fullPage: true,
  });
  assert.deepEqual(errors, []);
  await writeFile(
    resolve(out, "browser-report.json"),
    JSON.stringify(report, null, 2),
  );
  process.stdout.write(JSON.stringify(report, null, 2) + "\n");
} catch (error) {
  await page.screenshot({ path: resolve(out, "failure.png"), fullPage: true });
  process.stderr.write(JSON.stringify({ pageErrors: errors }) + "\n");
  throw error;
} finally {
  await browser.close();
}
