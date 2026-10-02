// Read-only deployment checks. Does not bind a QQ account or send messages.
import { chromium } from "@playwright/test";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import assert from "node:assert/strict";
const env = Object.fromEntries(
  (await readFile(new URL("../.env", import.meta.url), "utf8"))
    .split("\n")
    .filter((x) => x.includes("=") && !x.startsWith("#"))
    .map((x) => [x.slice(0, x.indexOf("=")), x.slice(x.indexOf("=") + 1)]),
);
const base =
  process.env.TEST_WEB_URL ?? "http://127.0.0.1:" + (env.WEB_PORT || "5173");
const browser = await chromium.launch({
  headless: true,
  executablePath:
    process.env.CHROME_PATH ??
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
});
const out = new URL("../data/acceptance/", import.meta.url);
const suffix = process.env.TEST_REPORT_SUFFIX ?? "";
await mkdir(out, { recursive: true });
const context = await browser.newContext({
  viewport: { width: 1440, height: 1200 },
});
const page = await context.newPage();
page.setDefaultTimeout(20_000);
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
try {
  await page.goto(base);
  await page.getByLabel("管理员密码").fill(env.ADMIN_PASSWORD);
  const [loginResponse] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith("/api/login")),
    page.getByRole("button", { name: /登\s*录/ }).click(),
  ]);
  assert.equal(loginResponse.status(), 200, "admin login failed");
  await page.getByRole("menuitem", { name: "系统与凭证" }).click();
  await page.getByText("个人 QQ · OneBot 通道", { exact: true }).waitFor();
  await page.getByRole("button", { name: "刷新连接" }).click();
  const response = await context.request.get(base + "/api/qq");
  assert.equal(response.status(), 200);
  const data = await response.json();
  if (process.env.EXPECT_LOCAL_NAPCAT !== undefined)
    assert.equal(
      !!data.napcatWebUrl,
      process.env.EXPECT_LOCAL_NAPCAT === "true",
    );
  const localLogin = page.getByRole("link", { name: "打开 NapCat 登录" });
  if (data.napcatWebUrl) await localLogin.waitFor();
  else
    assert.equal(
      await localLogin.count(),
      0,
      "unused local login link visible",
    );
  const health = await context.request.get(base + "/api/status");
  const services = await health.json();
  assert.equal(services.database, "ok", "database not ready");
  assert.equal(services.temporal, "ok", "Temporal not ready");
  assert.equal(typeof services.runtime, "object", "SDK runtime not ready");
  const configs = await (
    await context.request.get(base + "/api/configs")
  ).json();
  const selected = configs.find((c) => c.id === data.onebot.configId);
  if (selected)
    await page.getByText(selected.name, { exact: true }).first().waitFor();
  await page.getByText("active", { exact: true }).first().waitFor();
  assert.deepEqual(
    data.strategies.map((s) => s.provider),
    ["official", "onebot"],
  );
  assert(!JSON.stringify(data).includes(env.ONEBOT_TOKEN), "token leaked");
  await page.screenshot({
    path: new URL(`qq-settings${suffix}.png`, out).pathname,
    fullPage: true,
  });
  const forged = await context.request.post(base + "/qq/onebot/events", {
    data: { post_type: "message", message_type: "private", message: "ignored" },
    headers: { "X-Signature": "sha1=00" },
  });
  assert.equal(forged.status(), 401);
  const unauthenticated = await browser.newContext();
  const blocked = await unauthenticated.request.get(base + "/api/qq");
  assert.equal(blocked.status(), 401);
  await unauthenticated.close();
  if (data.napcatWebUrl) {
    const napcat = await context.request.get(data.napcatWebUrl);
    assert.equal(napcat.status(), 200);
  }
  assert.equal(errors.length, 0, errors.join("\n"));
  const report = {
    date: new Date().toISOString(),
    status: "passed",
    checks: [
      "admin settings rendered",
      "both strategies visible",
      "status API",
      "secret redaction",
      "callback signature enforcement",
      "admin authentication",
      data.napcatWebUrl
        ? "NapCat WebUI reachable"
        : "unused NapCat login hidden",
      "no page errors",
    ],
    channels: data.strategies.map(({ provider, state }) => ({
      provider,
      state,
    })),
    realQQMessaging: "not_verified_requires_login_and_bound_peer",
  };
  await writeFile(
    new URL(`qq-browser-report${suffix}.json`, out),
    JSON.stringify(report, null, 2) + "\n",
  );
  console.log(JSON.stringify(report, null, 2));
} finally {
  await browser.close();
}
