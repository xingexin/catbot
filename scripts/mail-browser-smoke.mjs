// Real admin login and read APIs; every mailbox, task and plugin mutation is
// intercepted in this browser. This does not contact IMAP, send QQ, or save keys.
import { chromium, expect } from "@playwright/test";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import assert from "node:assert/strict";

const env = Object.fromEntries(
  (await readFile(new URL("../.env", import.meta.url), "utf8"))
    .split("\n")
    .filter((line) => line.includes("=") && !line.trimStart().startsWith("#"))
    .map((line) => [
      line.slice(0, line.indexOf("=")).trim(),
      line
        .slice(line.indexOf("=") + 1)
        .trim()
        .replace(/^(['"])(.*)\1$/, "$2"),
    ]),
);
const base =
  process.env.TEST_WEB_URL ?? `http://127.0.0.1:${env.WEB_PORT || "5173"}`;
const output = new URL("../data/acceptance/", import.meta.url);
await mkdir(output, { recursive: true });
const browser = await chromium.launch({
  headless: true,
  executablePath:
    process.env.CHROME_PATH ??
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
});
const context = await browser.newContext({
  viewport: { width: 1500, height: 1200 },
});
const page = await context.newPage();
page.setDefaultTimeout(20000);
const errors = [];
const unexpectedWrites = [];
const reads = new Set();
const actions = [];
page.on("pageerror", (error) => errors.push(error.message));

const fixturePassword = "ui-fixture-not-an-imap-credential";
const fixtureConfig = {
  id: "ui-mail-model",
  name: "UI 邮箱测试模型",
  kind: "api",
  protocol: "openai-chat",
  model: "fixture-only",
};
const fixturePersona = {
  id: "ui-mail-persona",
  name: "UI 邮箱测试人格",
  tools: null,
};
const fixtureSession = {
  id: "ui-mail-session",
  title: "UI 邮箱提醒 Web 会话",
  channel: "web",
  configId: fixtureConfig.id,
  personaId: fixturePersona.id,
  messages: [],
};
let fixturePlugin = {
  id: "mail",
  manifest: JSON.parse(
    await readFile(
      new URL("../plugins/mail/plugin.json", import.meta.url),
      "utf8",
    ),
  ),
  enabled: false,
  config: {},
  secrets: {},
  grants: [],
};
let fixtureTasks = [];
let fixtureExecutions = [];
const configRequests = [];
const watchRequests = [];
let connectionChecks = 0;
function execution(initialized) {
  return {
    id: "ui-mail-execution-" + (fixtureExecutions.length + 1),
    taskId: "ui-mail-watch",
    status: "completed",
    startedAt: new Date(
      Date.now() + fixtureExecutions.length * 1000,
    ).toISOString(),
    finishedAt: new Date().toISOString(),
    results: {
      watch: {
        changed: false,
        initialized,
        messages: [],
        notificationText: "",
        cursor: { uidValidity: "fixture", uid: 20 },
      },
    },
  };
}

await page.route("**/api/**", async (route) => {
  const request = route.request();
  const path = new URL(request.url()).pathname;
  const method = request.method();
  if (method === "GET") {
    const overlays = {
      "/api/plugins": [fixturePlugin],
      "/api/configs": [fixtureConfig],
      "/api/personas": [fixturePersona],
      "/api/sessions": [fixtureSession],
      "/api/tasks": fixtureTasks,
      "/api/executions": fixtureExecutions,
    };
    if (!Object.hasOwn(overlays, path)) {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    assert.equal(response.status(), 200, `read API unavailable: ${path}`);
    reads.add(path);
    const fixtures = overlays[path];
    const ids = new Set(fixtures.map((row) => row.id));
    await route.fulfill({
      json: [
        ...fixtures,
        ...(await response.json()).filter((row) => !ids.has(row.id)),
      ],
    });
    return;
  }
  if (path === "/api/login") {
    await route.continue();
    return;
  }
  const value = request.postDataJSON();
  if (path === "/api/plugins/mail/configure" && method === "POST") {
    configRequests.push(structuredClone(value));
    const { password, ...config } = value.config;
    if (password !== undefined) assert.equal(password, fixturePassword);
    fixturePlugin = {
      ...fixturePlugin,
      config,
      grants: value.grants,
      secrets: password
        ? { password: "ui-mail-secret-reference" }
        : fixturePlugin.secrets,
    };
    await route.fulfill({ json: fixturePlugin });
    return;
  }
  if (path === "/api/plugins/mail/enable" && method === "POST") {
    fixturePlugin = { ...fixturePlugin, enabled: value.enabled };
    actions.push(value.enabled ? "enable" : "disable");
    await route.fulfill({ json: { ok: true } });
    return;
  }
  if (path === "/api/mail/connection" && method === "POST") {
    assert.equal(fixturePlugin.enabled, true);
    connectionChecks++;
    await route.fulfill({
      json: {
        connected: true,
        folder: value.folder,
        messageCount: 37,
        unreadCount: 3,
        checkedAt: new Date().toISOString(),
      },
    });
    return;
  }
  if (path === "/api/mail/watch" && method === "POST") {
    watchRequests.push(structuredClone(value));
    const task = {
      id: value.id ?? "ui-mail-watch",
      name: value.name,
      kind: "recurring",
      status: "active",
      paused: false,
      sessionId: value.sessionId,
      configId: value.configId,
      personaId: value.personaId,
      cron:
        value.intervalMinutes === 60
          ? "0 * * * *"
          : `*/${value.intervalMinutes} * * * *`,
      timeZone: "Asia/Shanghai",
      notify: true,
      notifyWhen: "${steps.watch.changed}",
      notifyText: "${steps.watch.notificationText}",
      steps: [
        {
          id: "watch",
          kind: "tool",
          tool: "mail__watch",
          arguments: {
            folder: value.folder,
            includeExisting: value.includeExisting,
            monitorId: value.id ?? "ui-mail-watch",
            limit: 20,
          },
        },
      ],
    };
    fixtureTasks = [task];
    if (fixtureExecutions.length === 0) fixtureExecutions = [execution(true)];
    await route.fulfill({ json: task });
    return;
  }
  const taskAction =
    /^\/api\/tasks\/ui-mail-watch\/(pause|resume|trigger|cancel)$/.exec(
      path,
    )?.[1];
  if (taskAction && method === "POST") {
    actions.push(taskAction);
    const task = fixtureTasks[0];
    if (taskAction === "trigger")
      fixtureExecutions = [execution(false), ...fixtureExecutions];
    else if (taskAction === "cancel")
      fixtureTasks = [{ ...task, paused: true, status: "cancelled" }];
    else
      fixtureTasks = [
        {
          ...task,
          paused: taskAction === "pause",
          status: taskAction === "pause" ? "paused" : "active",
        },
      ];
    await route.fulfill({
      json:
        taskAction === "trigger"
          ? { executionId: fixtureExecutions[0].id }
          : fixtureTasks[0],
    });
    return;
  }
  unexpectedWrites.push(`${method} ${path}`);
  await route.fulfill({
    status: 409,
    json: { error: "Browser regression blocked an unexpected mutation" },
  });
});

async function saveAndWait(button, path) {
  await expect(page.locator(".ant-spin-spinning")).toHaveCount(0);
  await expect(button).toBeEnabled();
  const [response] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith(path) && response.request().method() === "POST",
    ),
    button.click(),
  ]);
  assert.equal(response.status(), 200);
}
async function selectOption(form, label, option) {
  await form.getByLabel(label, { exact: true }).press("ArrowDown");
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: option })
    .click();
  await expect(page.locator(".ant-select-dropdown:visible")).toHaveCount(0);
}
try {
  await page.goto(base);
  await page.getByLabel("管理员密码").fill(env.ADMIN_PASSWORD);
  await saveAndWait(
    page.getByRole("button", { name: /登\s*录/ }),
    "/api/login",
  );
  await page.getByRole("menuitem", { name: "邮箱监听" }).click();
  const settings = page.locator("form#mail-settings");
  const watcher = page.locator("form#mail-watch");
  await settings
    .getByLabel("IMAP 服务器", { exact: true })
    .fill("imap.fixture.example.test");
  await settings
    .getByLabel("邮箱账号", { exact: true })
    .fill("ui-fixture@example.test");
  await settings
    .getByLabel("IMAP 授权码", { exact: true })
    .fill(fixturePassword);
  await expect(
    settings.getByRole("checkbox", { name: "标记邮件已读" }),
  ).not.toBeChecked();
  await expect(
    settings.getByRole("checkbox", { name: "读取邮件" }),
  ).toBeChecked();
  await saveAndWait(
    settings.getByRole("button", { name: "保存邮箱配置" }),
    "/api/plugins/mail/configure",
  );
  await expect(settings.getByLabel("IMAP 授权码", { exact: true })).toHaveValue(
    "",
  );
  await expect(
    settings.getByRole("button", { name: "启用邮箱", exact: true }),
  ).toBeEnabled();
  await saveAndWait(
    settings.getByRole("button", { name: "启用邮箱", exact: true }),
    "/api/plugins/mail/enable",
  );
  await expect(
    settings.getByRole("button", { name: "测试邮箱连接" }),
  ).toBeEnabled();

  await settings
    .getByLabel("IMAP 服务器", { exact: true })
    .fill("imap.edited.example.test");
  await expect(
    settings.getByRole("button", { name: "测试邮箱连接" }),
  ).toBeDisabled();
  await saveAndWait(
    settings.getByRole("button", { name: "保存邮箱配置" }),
    "/api/plugins/mail/configure",
  );
  assert.equal(configRequests[0].config.password, fixturePassword);
  assert.equal(
    configRequests[1].config.password,
    undefined,
    "blank password overwrote saved secret",
  );
  assert.equal(fixturePlugin.secrets.password, "ui-mail-secret-reference");
  await expect(settings.getByLabel("IMAP 授权码", { exact: true })).toHaveValue(
    "",
  );
  await expect(
    settings.getByRole("button", { name: "测试邮箱连接" }),
  ).toBeEnabled();
  await saveAndWait(
    settings.getByRole("button", { name: "测试邮箱连接" }),
    "/api/mail/connection",
  );
  await page.getByText(/共 37 封邮件，3 封未读/).waitFor();

  await watcher.getByLabel("监听名称", { exact: true }).fill("UI 邮箱监听回归");
  await selectOption(watcher, "提醒发送到", "UI 邮箱提醒 Web 会话");
  await expect(
    watcher
      .locator(".ant-select-selection-item")
      .filter({ hasText: fixtureConfig.name }),
  ).toBeVisible();
  await expect(
    watcher
      .locator(".ant-select-selection-item")
      .filter({ hasText: fixturePersona.name }),
  ).toBeVisible();
  await expect(
    watcher.getByRole("checkbox", { name: "首次检查也提醒已有邮件" }),
  ).not.toBeChecked();
  await saveAndWait(
    watcher.getByRole("button", { name: "创建监听" }),
    "/api/mail/watch",
  );
  assert.equal(watchRequests[0].sessionId, fixtureSession.id);
  assert.equal(watchRequests[0].configId, fixtureConfig.id);
  assert.equal(watchRequests[0].personaId, fixturePersona.id);
  assert.equal(watchRequests[0].includeExisting, false);
  assert.equal(watchRequests[0].intervalMinutes, 5);
  assert.equal(watchRequests[0].folder, "INBOX");
  const row = page
    .getByRole("row")
    .filter({
      has: page.getByRole("button", { name: "立即检查", exact: true }),
    })
    .filter({ hasText: "UI 邮箱监听回归" });
  await row.getByRole("button", { name: "已建立基线，等待新邮件" }).waitFor();
  await saveAndWait(
    row.getByRole("button", { name: "立即检查", exact: true }),
    "/api/tasks/ui-mail-watch/trigger",
  );
  await row.getByRole("button", { name: "无新邮件", exact: true }).waitFor();
  await saveAndWait(
    row.getByRole("button", { name: /暂\s*停/ }),
    "/api/tasks/ui-mail-watch/pause",
  );
  await row.getByText("已暂停", { exact: true }).waitFor();
  await saveAndWait(
    row.getByRole("button", { name: /恢\s*复/ }),
    "/api/tasks/ui-mail-watch/resume",
  );
  await row.getByText("运行中", { exact: true }).waitFor();
  await row.getByRole("button", { name: /编\s*辑/ }).click();
  await selectOption(watcher, "检查频率", "每 10 分钟");
  await saveAndWait(
    watcher.getByRole("button", { name: "保存监听", exact: true }),
    "/api/mail/watch",
  );
  assert.equal(watchRequests[1].id, "ui-mail-watch");
  assert.equal(watchRequests[1].intervalMinutes, 10);
  assert.equal(fixtureTasks.length, 1);
  await row.getByText(/每 10 分钟/).waitFor();
  await expect(page.locator(".ant-message-notice")).toHaveCount(0, {
    timeout: 10000,
  });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: new URL("mail-settings.png", output).pathname,
    fullPage: true,
  });
  assert.equal(errors.length, 0, errors.join("\n"));
  assert.deepEqual(unexpectedWrites, []);
  assert.deepEqual(actions, ["enable", "trigger", "pause", "resume"]);
  assert.equal(connectionChecks, 1);
  for (const path of [
    "plugins",
    "configs",
    "personas",
    "sessions",
    "tasks",
    "executions",
  ])
    assert(reads.has("/api/" + path));
  const report = {
    date: new Date().toISOString(),
    status: "passed",
    checks: [
      "real administrator login and read API responses",
      "email configuration saved through normal UI",
      "secret input cleared after save and blank password preserves credential reference",
      "mail.write permission is not enabled by default",
      "unsaved connection changes block connection test",
      "separate enable and saved-config connection test",
      "Web notification target automatically supplies model and persona",
      "quiet initial baseline and subsequent no-new-mail display",
      "manual check, pause and resume actions",
      "frequency edit preserves watcher identity",
      "no page errors or unintended writes",
    ],
    mutations: "intercepted in browser, except real administrator login",
    mailboxCalls: "mocked; no real mailbox contacted",
    qqMessagesSent: 0,
    credentialsPersisted: false,
    fixture:
      "UI mock regression is separate from TLS IMAP protocol tests and live platform acceptance",
  };
  await writeFile(
    new URL("mail-settings-report.json", output),
    JSON.stringify(report, null, 2) + "\n",
  );
  console.log(JSON.stringify(report, null, 2));
} catch (error) {
  await page
    .screenshot({
      path: new URL("mail-settings-failure.png", output).pathname,
      fullPage: true,
    })
    .catch(() => {});
  throw error;
} finally {
  await browser.close();
}
