// Real management UI acceptance; no models, QQ messages or plugin mutations.
import { chromium } from "@playwright/test";
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";

const env = Object.fromEntries(
  (await readFile(new URL("../.env", import.meta.url), "utf8"))
    .split("\n")
    .filter((line) => line.includes("=") && !line.startsWith("#"))
    .map((line) => [
      line.slice(0, line.indexOf("=")),
      line.slice(line.indexOf("=") + 1),
    ]),
);
const base =
  process.env.TEST_WEB_URL ?? `http://127.0.0.1:${env.WEB_PORT || "5173"}`;
const out = new URL("../data/acceptance/", import.meta.url);
await mkdir(out, { recursive: true });
const suffix = randomUUID().slice(0, 8);
const name = "[验收] 人格 " + suffix;
const copyName = name + " 副本";
const importName = name + " 导入";
const report = {
  date: new Date().toISOString(),
  status: "running",
  checks: [],
  liveModelCalls: false,
  qqMessagesSent: false,
  pluginMutations: false,
  createdPersonaIds: [],
};
const browser = await chromium.launch({
  headless: true,
  executablePath:
    process.env.CHROME_PATH ??
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
});
const context = await browser.newContext({
  viewport: { width: 1440, height: 1100 },
  acceptDownloads: true,
});
const page = await context.newPage();
page.setDefaultTimeout(15000);
const pageErrors = [];
page.on("pageerror", (error) => pageErrors.push(error.message));
await page.route("**/api/**", async (route) => {
  const request = route.request(),
    path = new URL(request.url()).pathname;
  if (
    request.method() !== "GET" &&
    (/\/messages$/.test(path) ||
      path.startsWith("/api/qq") ||
      (/^\/api\/plugins\//.test(path) &&
        path !== "/api/plugins/example/health"))
  ) {
    pageErrors.push(
      "Blocked unexpected side-effect request: " +
        request.method() +
        " " +
        path,
    );
    await route.abort();
    return;
  }
  await route.continue();
});
let originalDefaultId;
let originalPersonas = [];
let loggedIn = false;
async function api(path, body, method = body === undefined ? "GET" : "POST") {
  const response = await context.request.fetch(base + "/api" + path, {
    method,
    data: body,
  });
  const value = await response.json();
  assert(response.ok(), JSON.stringify(value));
  return value;
}
function card(title) {
  return page.locator(".ant-card").filter({
    has: page.locator(".ant-card-head-title").getByText(title, { exact: true }),
  });
}
async function saveDialog(dialog, path = "/personas") {
  const [response] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.url() === base + "/api" + path && r.request().method() === "POST",
    ),
    dialog.getByRole("button", { name: /确\s*定/ }).click(),
  ]);
  const value = await response.json();
  assert.equal(response.status(), 200, JSON.stringify(value));
  if (
    path === "/personas" &&
    !originalPersonas.some((p) => p.id === value.id) &&
    !report.createdPersonaIds.includes(value.id)
  )
    report.createdPersonaIds.push(value.id);
  await dialog.waitFor({ state: "hidden" });
  return value;
}
async function restoreDefault() {
  if (!loggedIn) return;
  const current = await api("/personas");
  if (originalDefaultId) {
    const original = current.find((p) => p.id === originalDefaultId);
    assert(original, "Original default persona disappeared");
    if (!original.default)
      await api("/personas", { ...original, default: true });
  } else {
    for (const item of current.filter(
      (p) => p.default && report.createdPersonaIds.includes(p.id),
    ))
      await api("/personas", { ...item, default: false });
  }
  const defaults = (await api("/personas"))
    .filter((p) => p.default)
    .map((p) => p.id);
  assert.deepEqual(
    defaults,
    originalDefaultId ? [originalDefaultId] : [],
    "Original default persona was not restored",
  );
  report.defaultRestored = true;
}
async function removeThroughUI(title, id, expectedStatus = 200) {
  await card(title)
    .getByRole("button", { name: /删\s*除/ })
    .click();
  const [response] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.url() === base + "/api/personas/" + id &&
        r.request().method() === "DELETE",
    ),
    page
      .locator(".ant-popconfirm:visible")
      .getByRole("button", { name: /确\s*定/ })
      .click(),
  ]);
  const value = await response.json();
  assert.equal(response.status(), expectedStatus, JSON.stringify(value));
  return value;
}
try {
  await page.goto(base);
  await page.getByLabel("管理员密码").fill(env.ADMIN_PASSWORD);
  const [login] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith("/api/login")),
    page.getByRole("button", { name: "进入工作台" }).click(),
  ]);
  assert.equal(login.status(), 200);
  loggedIn = true;
  originalPersonas = await api("/personas");
  const defaults = originalPersonas.filter((p) => p.default);
  assert.ok(defaults.length <= 1, "Multiple initial default personas");
  originalDefaultId = defaults[0]?.id;
  const selectedConfig = (await api("/configs")).find(
    (config) =>
      config.id ===
      (process.env.TEST_PERSONA_CONFIG ?? "acceptance-openai-chat"),
  );
  assert(
    selectedConfig,
    "Existing acceptance model configuration is required; this test does not create one",
  );
  const originalPlugins = await api("/plugins");
  const example = originalPlugins.find((p) => p.id === "example");
  assert(
    example?.enabled,
    "Example plugin is unavailable; test will not enable it",
  );
  await page.getByRole("menuitem", { name: "人格" }).click();
  await page.getByRole("button", { name: "创建人格" }).click();
  const dialog = page.getByRole("dialog", { name: "人格设置", exact: true });
  await dialog.getByLabel("名称", { exact: true }).fill(name);
  await dialog
    .getByLabel("介绍", { exact: true })
    .fill("浏览器验收专用人格，不调用模型");
  await dialog
    .getByLabel("系统提示词", { exact: true })
    .fill("用简洁中文回复，不能凭空宣称操作成功。");
  await dialog.getByLabel("行为偏好", { exact: true }).fill("先说明实际结果。");
  const examples = [
    { role: "user", content: "你好" },
    { role: "assistant", content: "你好，请告诉我需要处理的事。" },
  ];
  await dialog
    .getByLabel("示例对话（JSON，role 为 user 或 assistant）", { exact: true })
    .fill(JSON.stringify(examples));
  await dialog.getByRole("checkbox", { name: "使用所有已授权工具" }).uncheck();
  await dialog.screenshot({
    path: new URL("persona-editor.png", out).pathname,
  });
  let original = await saveDialog(dialog);
  assert.deepEqual(original.tools, []);
  assert.deepEqual(original.examples, examples);
  assert.equal(original.version, 1);
  report.checks.push(
    "create persona with examples, preferences and explicit empty tool allowlist",
  );
  await card(name)
    .getByRole("button", { name: /编\s*辑/ })
    .click();
  await dialog.getByLabel("介绍", { exact: true }).fill("浏览器编辑验收已保存");
  original = await saveDialog(dialog);
  assert.equal(original.version, 2);
  assert.equal(original.description, "浏览器编辑验收已保存");
  report.checks.push("edit increments persona version");
  await card(name)
    .getByRole("button", { name: /复\s*制/ })
    .click();
  assert.equal(
    await dialog.getByLabel("名称", { exact: true }).inputValue(),
    copyName,
  );
  const copy = await saveDialog(dialog);
  assert.notEqual(copy.id, original.id);
  assert.equal(copy.default, false);
  assert.deepEqual(copy.examples, examples);
  assert.deepEqual(copy.tools, []);
  report.checks.push("copy gets a distinct ID and preserves persona content");
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    card(name)
      .getByRole("button", { name: /导\s*出/ })
      .click(),
  ]);
  const exported = JSON.parse(await readFile(await download.path(), "utf8"));
  assert.equal(exported.id, original.id);
  assert.deepEqual(exported.examples, examples);
  const [chooser] = await Promise.all([
    page.waitForEvent("filechooser"),
    page.getByRole("button", { name: "导入 JSON" }).click(),
  ]);
  const [importResponse] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.url() === base + "/api/personas" && r.request().method() === "POST",
    ),
    chooser.setFiles({
      name: "persona.json",
      mimeType: "application/json",
      buffer: Buffer.from(
        JSON.stringify({ ...exported, name: importName, default: true }),
      ),
    }),
  ]);
  assert.equal(importResponse.status(), 200);
  const imported = await importResponse.json();
  report.createdPersonaIds.push(imported.id);
  assert.notEqual(imported.id, original.id);
  assert.equal(imported.default, false);
  assert.deepEqual(imported.examples, examples);
  report.checks.push(
    "export/import round trip creates an independent non-default persona",
  );
  await card(name)
    .getByRole("button", { name: /编\s*辑/ })
    .click();
  await dialog.getByRole("checkbox", { name: "默认人格" }).check();
  original = await saveDialog(dialog);
  assert.equal(original.default, true);
  assert.deepEqual(
    (await api("/personas")).filter((p) => p.default).map((p) => p.id),
    [original.id],
  );
  await page.getByRole("menuitem", { name: "对话" }).click();
  await page.getByRole("button", { name: "新对话" }).click();
  const sessionDialog = page.getByRole("dialog", {
    name: "会话设置",
    exact: true,
  });
  await sessionDialog
    .getByLabel("标题", { exact: true })
    .fill("[验收] 人格绑定 " + suffix);
  await sessionDialog
    .getByLabel("模型配置", { exact: true })
    .fill(selectedConfig.name);
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: selectedConfig.name })
    .click();
  const session = await saveDialog(sessionDialog, "/sessions");
  report.sessionId = session.id;
  assert.equal(session.personaId, original.id);
  assert.equal(session.channel, "web");
  assert.equal(session.messages.length, 0);
  await restoreDefault();
  report.checks.push(
    "temporary default selected for a new Web session and original default restored",
  );
  await page.getByRole("menuitem", { name: "人格" }).click();
  const rejected = await removeThroughUI(name, original.id, 400);
  assert.match(rejected.error, /conversation/);
  assert((await api("/personas")).some((p) => p.id === original.id));
  report.checks.push("referenced persona deletion rejected by real backend");
  await page.getByRole("menuitem", { name: "对话" }).click();
  await page.getByRole("button", { name: "会话设置", exact: true }).click();
  await sessionDialog.getByLabel("人格", { exact: true }).fill(copyName);
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: copyName })
    .click();
  const rebound = await saveDialog(sessionDialog, "/sessions");
  assert.equal(rebound.personaId, copy.id);
  report.retainedPersonaId = copy.id;
  report.checks.push(
    "session persona binding can be changed without a model call",
  );
  await page.getByRole("menuitem", { name: "人格" }).click();
  await removeThroughUI(name, original.id);
  await card(name).waitFor({ state: "hidden" });
  await removeThroughUI(importName, imported.id);
  await card(importName).waitFor({ state: "hidden" });
  report.checks.push("unreferenced acceptance personas can be deleted");
  await page.getByRole("menuitem", { name: "插件" }).click();
  const pluginCard = card(example.manifest.name);
  const [health] = await Promise.all([
    page.waitForResponse(
      (r) => r.url() === base + "/api/plugins/example/health",
    ),
    pluginCard.getByRole("button", { name: /检\s*查/ }).click(),
  ]);
  assert.equal(health.status(), 200);
  assert.equal((await health.json()).status, "ok");
  const [logs] = await Promise.all([
    page.waitForResponse((r) => r.url() === base + "/api/plugins/example/logs"),
    pluginCard.getByRole("button", { name: /日\s*志/ }).click(),
  ]);
  assert.equal(logs.status(), 200);
  const logData = await logs.json();
  const logDrawer = page.getByRole("dialog", { name: "详情" });
  await logDrawer.locator("pre.json").waitFor();
  assert.deepEqual(
    JSON.parse(await logDrawer.locator("pre.json").innerText()),
    logData,
  );
  await logDrawer.screenshot({
    path: new URL("example-plugin-logs.png", out).pathname,
    animations: "disabled",
  });
  report.logDrawerContentVerified = true;
  report.checks.push(
    "example plugin health and log drawer read through real management UI",
  );
  const currentPlugins = await api("/plugins");
  assert.deepEqual(
    currentPlugins
      .map((p) => ({
        id: p.id,
        enabled: p.enabled,
        version: p.manifest.version,
      }))
      .sort((a, b) => a.id.localeCompare(b.id)),
    originalPlugins
      .map((p) => ({
        id: p.id,
        enabled: p.enabled,
        version: p.manifest.version,
      }))
      .sort((a, b) => a.id.localeCompare(b.id)),
    "Plugin state unexpectedly changed",
  );
  assert.equal(pageErrors.length, 0, pageErrors.join("\n"));
  report.status = "passed";
} catch (error) {
  report.status = "failed";
  report.error = error.message;
  report.validationErrors = await page
    .locator(".ant-form-item-explain-error")
    .allTextContents()
    .catch(() => []);
  await page
    .screenshot({
      path: new URL("persona-plugin-failed.png", out).pathname,
      fullPage: true,
    })
    .catch(() => {});
  process.exitCode = 1;
} finally {
  try {
    await restoreDefault();
  } catch (error) {
    report.status = "failed";
    report.restoreError = error.message;
    process.exitCode = 1;
  }
  report.finishedAt = new Date().toISOString();
  await writeFile(
    new URL("persona-plugin-report.json", out),
    JSON.stringify(report, null, 2) + "\n",
  );
  await browser.close();
  console.log(JSON.stringify(report, null, 2));
}
