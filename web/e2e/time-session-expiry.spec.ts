import { test, expect, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, roleAccounts } from "./support/env";
import { monitor } from "./support/monitor";

/**
 * Release acceptance round, time injection T-1 (docs/plans/2026-10-18-release-wrap-up.md §3.1): a session that expires
 * (a) on a list page, (b) while a conversation reply streams, (c) while a form is being filled in.
 *
 * Signs the operator in once per case through the login page (not the saved e2e state, so a running regression keeps
 * its sessions), finds that session's row by the hash of this cookie only (the value is never printed) and moves its
 * expires_at to two minutes from now in the acceptance PG. Not part of the regression; run on purpose:
 *   T1_CASE=list|stream|form npx playwright test -c e2e/playwright.config.ts time-session-expiry.spec.ts
 * Uses one real model call in the stream case.
 */
const instance = "prod" as const;
const origin = INSTANCES[instance].origin;
const OUT = resolve(LOCAL, "e2e/time-injection");
mkdirSync(OUT, { recursive: true });
const only = process.env.T1_CASE;

test.use({ viewport: { width: 1440, height: 900 } });
test.setTimeout(600_000);

async function login(page: Page) {
  const account = roleAccounts(instance).operator;
  if (!account) throw new Error("operator account not set up");
  await page.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  await page.goto(origin + "/login");
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator(".v3-login-submit").click();
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 30_000 });
}

/** Shorten this browser's own session to two minutes; returns the database clock when it now expires. */
async function expireSoon(page: Page): Promise<string> {
  const cookie = (await page.context().cookies(origin)).find((c) => c.name === "aiops_session");
  if (!cookie) throw new Error("no session cookie");
  const hash = createHash("sha256").update(cookie.value).digest("hex");
  const out = execFileSync("docker", ["exec", "aiops-acceptance-pg", "psql", "-U", "aiops", "-d", "redstring_prod", "-Atc",
    `UPDATE sessions SET expires_at = clock_timestamp() + interval '2 minutes' WHERE token_hash = '${hash}' RETURNING expires_at`], { encoding: "utf8" });
  const at = out.trim().split("\n")[0];
  if (!at) throw new Error("session row not found");
  return at;
}

async function reloginAndReturn(page: Page, log: Record<string, unknown>) {
  const account = roleAccounts(instance).operator;
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator(".v3-login-submit").click();
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 30_000 });
  await page.waitForTimeout(2000);
  log.after_relogin = page.url().replace(origin, "");
}

const displayName = (page: Page) => page.locator("label.v3-field", { has: page.locator(".label", { hasText: /^显示名$/ }) }).locator("input");

async function snapshot(page: Page, name: string) {
  await page.screenshot({ path: resolve(OUT, `t1-${name}.png`), fullPage: true });
  return (await page.locator("body").innerText()).slice(0, 1500);
}

test("T-1 (a) list page", async ({ page }) => {
  test.skip(!!only && only !== "list");
  const m = monitor(page);
  const log: Record<string, unknown> = { case: "list" };
  await login(page);
  await page.goto(origin + "/alerts");
  await page.waitForTimeout(3000);
  log.expires_at = await expireSoon(page);
  log.shortened_at = new Date().toISOString();
  // Stay on the page past the expiry; record whether it notices by itself before anyone clicks.
  await page.waitForTimeout(150_000);
  log.url_after_idle = page.url().replace(origin, "");
  log.text_after_idle = await snapshot(page, "list-idle");
  if (!page.url().includes("/login")) {
    await page.getByRole("link", { name: "总览" }).first().click();
    await page.waitForTimeout(5000);
  }
  log.url_after_click = page.url().replace(origin, "");
  log.text_after_click = await snapshot(page, "list-after");
  if (page.url().includes("/login")) await reloginAndReturn(page, log);
  log.api = m.api.filter((c) => c.status >= 400);
  log.pageErrors = m.pageErrors;
  writeFileSync(resolve(OUT, "t1-list.json"), JSON.stringify(log, null, 2));
  expect(String(log.url_after_click)).toContain("expired=1");
});

test("T-1 (b) conversation streaming", async ({ page }) => {
  test.skip(!!only && only !== "stream");
  const m = monitor(page);
  const log: Record<string, unknown> = { case: "stream" };
  const events: string[] = [];
  page.on("response", (r) => {
    if (r.url().includes("/events")) events.push(`${new Date().toISOString()} ${r.status()} ${r.url().split("/api/v1")[1]}`);
  });
  await login(page);
  await page.goto(origin + "/conversations/new");
  const box = page.getByLabel("向 AI 下指令");
  await box.waitFor({ timeout: 20_000 });
  log.expires_at = await expireSoon(page);
  // Send a few seconds before the expiry so the reply is still streaming when the session ends (a reply takes 10-40 s).
  await page.waitForTimeout(Number(process.env.T1_SEND_AFTER_MS ?? 117_000));
  log.sent_at = new Date().toISOString();
  await box.fill("checkout-api 最近 30 分钟的 5xx 错误率和日志里有什么异常？先查指标，再查日志，给出结论和证据。");
  await box.press("Enter");
  // Toasts are short-lived: collect what is said while the stream crosses the expiry.
  const said = new Set<string>();
  for (let i = 0; i < 20; i++) {
    for (const t of await page.locator(".v3-toast").allInnerTexts()) said.add(`${new Date().toISOString()} ${t}`);
    if (i === 5) await snapshot(page, "stream-12s");
    await page.waitForTimeout(2000);
  }
  log.toasts = [...said];
  let waited = 40;
  for (const s of [30, 60, 120, 180]) {
    await page.waitForTimeout((s - waited) * 1000);
    waited = s;
    log[`url_${s}s`] = page.url().replace(origin, "");
    log[`text_${s}s`] = (await snapshot(page, `stream-${s}s`)).slice(-800);
  }
  // Then act on the page: the next call must lead to the login page.
  if (!page.url().includes("/login")) {
    await page.getByRole("link", { name: "总览" }).first().click();
    await page.waitForTimeout(5000);
  }
  log.url_after_click = page.url().replace(origin, "");
  if (page.url().includes("/login")) {
    log.login_text = await snapshot(page, "stream-login");
    await reloginAndReturn(page, log);
  }
  log.events = events;
  log.api = m.api.filter((c) => c.status >= 400);
  log.pageErrors = m.pageErrors;
  writeFileSync(resolve(OUT, "t1-stream.json"), JSON.stringify(log, null, 2));
});

test("T-1 (c) form being filled in", async ({ page }) => {
  test.skip(!!only && only !== "form");
  const m = monitor(page);
  const log: Record<string, unknown> = { case: "form" };
  await login(page);
  await page.goto(origin + "/settings/profile");
  const field = displayName(page);
  await field.waitFor({ timeout: 20_000 });
  const original = await field.inputValue();
  log.original_length = original.length;
  log.expires_at = await expireSoon(page);
  await field.fill(original + " T1");
  await page.waitForTimeout(150_000);
  await page.getByRole("button", { name: "保存" }).click();
  await page.waitForTimeout(5000);
  log.url_after_save = page.url().replace(origin, "");
  log.text_after_save = await snapshot(page, "form-after-save");
  log.saved_toast = log.text_after_save && String(log.text_after_save).includes("个人资料已保存");
  if (page.url().includes("/login")) await reloginAndReturn(page, log);
  await page.waitForTimeout(2000);
  const now = await displayName(page).inputValue().catch(() => null);
  log.display_name_unchanged = now === original;
  log.api = m.api.filter((c) => c.status >= 400 || c.method !== "GET");
  log.pageErrors = m.pageErrors;
  writeFileSync(resolve(OUT, "t1-form.json"), JSON.stringify(log, null, 2));
  expect(log.saved_toast).toBeFalsy();
  expect(log.display_name_unchanged).toBe(true);
});
