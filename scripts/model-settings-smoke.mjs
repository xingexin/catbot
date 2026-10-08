// Browser integration check: real login/read APIs, intercepted credential/config
// writes. Does not change stored models, credentials, QQ bindings or call a model.
import { chromium } from "@playwright/test";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import assert from "node:assert/strict";

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
const browser = await chromium.launch({
  headless: true,
  executablePath:
    process.env.CHROME_PATH ??
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
});
const context = await browser.newContext({
  viewport: { width: 1440, height: 1100 },
});
const page = await context.newPage();
page.setDefaultTimeout(15000);
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
const savedConfigs = [];
const savedCredentials = [];
const fixtureKey = "ui-only-not-a-real-key";
const out = new URL("../data/acceptance/", import.meta.url);
await mkdir(out, { recursive: true });
for (const [path, rows] of [
  ["configs", savedConfigs],
  ["secrets", savedCredentials],
]) {
  await page.route(`**/api/${path}`, async (route) => {
    if (route.request().method() === "POST") {
      const value = route.request().postDataJSON();
      if (path === "configs") {
        assert(
          !JSON.stringify(value).includes(fixtureKey),
          "API Key leaked into configuration",
        );
        const record = {
          ...value,
          id: value.id ?? `ui-model-${savedConfigs.length}`,
        };
        rows.push(record);
        await route.fulfill({ json: record });
      } else {
        assert.equal(value.value, fixtureKey);
        const record = { id: "ui-credential", name: value.name };
        rows.push(record);
        await new Promise((resolve) => setTimeout(resolve, 600));
        await route.fulfill({ json: record });
      }
      return;
    }
    const response = await route.fetch();
    assert.equal(response.status(), 200);
    await route.fulfill({ json: [...(await response.json()), ...rows] });
  });
}
try {
  await page.goto(base);
  await page.getByLabel("管理员密码").fill(env.ADMIN_PASSWORD);
  const [login] = await Promise.all([
    page.waitForResponse((response) => response.url().endsWith("/api/login")),
    page.getByRole("button", { name: /登\s*录/ }).click(),
  ]);
  assert.equal(login.status(), 200);
  await page.getByRole("menuitem", { name: "系统与凭证" }).click();
  const models = page.locator(".model-connections");
  await models.getByText("模型接入", { exact: true }).waitFor();
  await page.getByRole("menuitem", { name: "模型配置" }).waitFor();
  await models.getByRole("button", { name: "添加模型 API" }).click();
  const dialog = page.getByRole("dialog", { name: "模型配置", exact: true });
  await dialog.getByLabel("配置名称", { exact: true }).fill("UI 模型接入验证");
  await dialog.getByLabel("Model", { exact: true }).fill("fixture-only");
  assert(
    await dialog.getByLabel("最大输出 Token", { exact: true }).isVisible(),
  );
  await dialog
    .getByRole("checkbox", { name: "流式输出", exact: true })
    .uncheck();
  await dialog.getByRole("checkbox", { name: "图片输入", exact: true }).check();
  await dialog
    .getByLabel("Base URL", { exact: true })
    .fill("http://fixture.invalid/v1");
  await dialog.getByLabel("接口协议", { exact: true }).press("ArrowDown");
  for (const label of [
    "OpenAI Chat Completions",
    "OpenAI Responses",
    "Anthropic Messages",
  ]) {
    await page
      .locator(".ant-select-dropdown:visible .ant-select-item-option")
      .filter({ hasText: label })
      .waitFor();
  }
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: "OpenAI Responses" })
    .click();
  await dialog
    .getByRole("button", { name: "添加 API Key", exact: true })
    .click();
  const keyDialog = page.getByRole("dialog", {
    name: "添加 API Key",
    exact: true,
  });
  await keyDialog.getByRole("button", { name: /取\s*消/ }).click();
  assert.equal(
    await dialog.getByLabel("配置名称", { exact: true }).inputValue(),
    "UI 模型接入验证",
  );
  await dialog
    .getByRole("button", { name: "添加 API Key", exact: true })
    .click();
  await keyDialog.getByLabel("凭证名称", { exact: true }).fill("UI Key 验证");
  await keyDialog.getByLabel("API Key", { exact: true }).fill(fixtureKey);
  await Promise.all([
    page.waitForRequest(
      (request) =>
        request.url().endsWith("/api/secrets") && request.method() === "POST",
    ),
    keyDialog.getByRole("button", { name: "保存并选用" }).click(),
  ]);
  await keyDialog.press("Escape");
  assert(await dialog.isVisible(), "saving credential closed the model draft");
  await keyDialog.waitFor({ state: "hidden" });
  await dialog.getByText("UI Key 验证", { exact: true }).waitFor();
  assert.equal(
    await dialog.getByLabel("Base URL", { exact: true }).inputValue(),
    "http://fixture.invalid/v1",
  );
  await dialog.getByRole("button", { name: /确\s*定/ }).click();
  await dialog.waitFor({ state: "hidden" });
  assert.equal(savedConfigs[0].kind, "api");
  assert.equal(savedConfigs[0].protocol, "openai-responses");
  assert.equal(savedConfigs[0].credentialId, "ui-credential");
  assert.equal(savedConfigs[0].capabilities.stream, false);
  assert.equal(savedConfigs[0].capabilities.images, true);
  await models.getByLabel("搜索模型接入").fill("UI 模型接入验证");
  await models.getByText("UI 模型接入验证", { exact: true }).waitFor();
  await models.getByRole("button", { name: "添加 Agent SDK" }).click();
  await dialog
    .getByLabel("配置名称", { exact: true })
    .fill("UI CodeBuddy 验证");
  await dialog.getByLabel("Agent SDK", { exact: true }).press("ArrowDown");
  for (const label of [
    "CodeBuddy Agent SDK",
    "Claude Agent SDK",
    "Codex SDK",
  ]) {
    await page
      .locator(".ant-select-dropdown:visible .ant-select-item-option")
      .filter({ hasText: label })
      .waitFor();
  }
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: "CodeBuddy Agent SDK" })
    .click();
  await dialog.getByLabel("Model", { exact: true }).fill("glm-5.3");
  // Switching an image-capable API draft to SDK clears unsupported settings.
  await dialog.getByLabel("接入方式", { exact: true }).press("ArrowDown");
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: "模型 API 直连" })
    .click();
  await dialog.getByRole("checkbox", { name: "图片输入", exact: true }).check();
  await dialog
    .getByRole("checkbox", { name: "流式输出", exact: true })
    .uncheck();
  await dialog
    .getByRole("checkbox", { name: "工具调用", exact: true })
    .uncheck();
  await dialog.getByLabel("接入方式", { exact: true }).press("ArrowDown");
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: "Agent SDK" })
    .click();
  assert.equal(
    await dialog
      .getByRole("checkbox", { name: "图片输入", exact: true })
      .count(),
    0,
  );
  assert.equal(
    await dialog
      .getByRole("checkbox", { name: "流式输出", exact: true })
      .count(),
    0,
  );
  assert.equal(
    await dialog.getByLabel("最大输出 Token", { exact: true }).count(),
    0,
  );
  assert.equal(
    await dialog
      .getByRole("checkbox", { name: "工具调用", exact: true })
      .isChecked(),
    false,
  );
  await dialog.getByText(/SDK 提供流式输出和会话恢复/).waitFor();
  await dialog.getByLabel("Model", { exact: true }).click();
  await page.locator(".ant-select-dropdown:visible").waitFor({ state: "hidden" });
  await dialog.screenshot({
    path: new URL("sdk-capabilities.png", out).pathname,
    animations: "disabled",
  });
  await dialog.getByRole("button", { name: /确\s*定/ }).click();
  await dialog.waitFor({ state: "hidden" });
  assert.equal(savedConfigs[1].kind, "sdk");
  assert.equal(savedConfigs[1].provider, "codebuddy");
  assert.equal(savedConfigs[1].capabilities.tools, false);
  // Ant Form omits unmounted fields from validateFields(). The backend sets
  // SDK stream/resume true and images defaults false (covered by HTTP tests).
  assert.notEqual(savedConfigs[1].capabilities.images, true);
  assert.notEqual(savedConfigs[1].capabilities.stream, false);
  assert.notEqual(savedConfigs[1].capabilities.resume, false);
  // The legacy menu edits the same data set, under the requested clearer name.
  await page.getByRole("menuitem", { name: "模型配置" }).click();
  await page.getByText("UI CodeBuddy 验证", { exact: true }).waitFor();
  await page.getByRole("menuitem", { name: "系统与凭证" }).click();
  await models.getByLabel("搜索模型接入").fill("codebuddy");
  await models.getByText("CodeBuddy iOA · GLM 5.3", { exact: true }).waitFor();
  await page.screenshot({
    path: new URL("model-settings.png", out).pathname,
    fullPage: true,
  });
  assert.equal(errors.length, 0, errors.join("\n"));
  const report = {
    date: new Date().toISOString(),
    status: "passed",
    checks: [
      "model access in settings",
      "renamed model configuration menu and dialog",
      "three API protocols",
      "credential cancellation preserves draft",
      "Escape while saving credential preserves model draft",
      "credential automatically selected without plaintext in model payload",
      "CodeBuddy SDK selection",
      "API image and streaming settings remain editable",
      "SDK hides unsupported image and output-token controls and fixed streaming control",
      "API to SDK switch clears images and retains tool opt-out in saved payload",
      "existing configurations visible",
      "no page errors",
    ],
    writes: "intercepted_in_browser_only",
    liveModelCalls: false,
  };
  await writeFile(
    new URL("model-settings-report.json", out),
    JSON.stringify(report, null, 2) + "\n",
  );
  console.log(JSON.stringify(report, null, 2));
} finally {
  await browser.close();
}
