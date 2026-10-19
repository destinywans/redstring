import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { at, json, read, zh } from "./support/checks";

/**
 * PA-263 (WAN-617, round 3 time injection T-4): pages that show something relative to now computed it once, at render.
 * Left open past a boundary they kept saying it: a silence that had ended stayed 生效中 with its old remaining time,
 * the inbox kept yesterday's alerts under 今天 after midnight, and "x 分钟前" never moved. The browser clock is moved
 * with page.clock; answers are pinned with page.route or read from the instance and patched. Nothing writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-time.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

test.describe("time moves while the page is open (PA-263)", () => {
  test("a silence that ends while you watch is no longer shown as active, and its remaining time counts down", async ({ page }) => {
    await page.addInitScript(zh);
    const now = Date.now();
    await page.clock.install({ time: now });
    const silence = {
      id: "pa263-silence",
      matchers: [{ k: "alertname", v: "PA263Check" }],
      state: "active",
      starts_at: new Date(now - 20 * 60_000).toISOString(),
      // Half a minute of slack so loading the page doesn't take it under 10 minutes.
      ends_at: new Date(now + 10.5 * 60_000).toISOString(),
      remaining: { pct: 33, text: "10 分" },
      affects: 0,
      creator: "检查用",
      reason: "PA-263 检查",
    };
    await page.route(at("/silences"), (r) => (r.request().method() === "GET" ? json(r, 200, { items: [silence], counts: { active: 1, planned: 0, expired: 0 }, empty_reason: null, unavailable: null }) : r.fallback()));
    await page.goto(origin + "/silences");
    const row = page.locator("tr", { hasText: "PA263Check" }).first();
    await expect(row).toContainText("生效中", { timeout: 20_000 });
    await expect(row).toContainText("10 分");
    // Four minutes on, the remaining time follows the clock rather than the answer read at load.
    await page.clock.fastForward("04:00");
    await expect(row).toContainText("6 分", { timeout: 10_000 });
    // Past the end it is not active any more.
    await page.clock.fastForward("10:00");
    await expect(row).not.toContainText("生效中", { timeout: 10_000 });
    await expect(row).toContainText("已过期");
    await expect(row).not.toContainText("分");
  });

  test("an alert from just before midnight moves from 今天 to 昨天 once midnight passes", async ({ page }) => {
    await page.addInitScript(zh);
    const d = new Date();
    d.setHours(23, 55, 0, 0);
    const t0 = d.getTime();
    await page.goto(origin + "/healthz");
    const real = (await read(page, "/inbox?tab=all")).body;
    test.skip(!real?.items?.length, "no inbox items to pin");
    const first = { ...real.items[0], at: new Date(t0 - 5 * 60_000).toISOString() };
    // Only this item, so the group headers belong to it.
    await page.route(at("/inbox"), (r) => json(r, 200, { ...real, items: [first] }));
    await page.clock.install({ time: t0 });
    await page.goto(origin + "/inbox");
    const side = page.locator(".v3-split .side").first();
    await expect(side.getByText("今天", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(side.getByText("昨天", { exact: true })).toHaveCount(0);
    await page.clock.fastForward("15:00");
    await expect(side.getByText("昨天", { exact: true })).toBeVisible({ timeout: 10_000 });
    await expect(side.getByText("今天", { exact: true })).toHaveCount(0);
  });

  test("“x 分钟前” in the conversation list moves with the clock", async ({ page }) => {
    await page.addInitScript(zh);
    const now = Date.now();
    await page.goto(origin + "/healthz");
    const real = (await read(page, "/conversations?tab=mine")).body;
    test.skip(!real?.items?.length, "no conversations to pin");
    const first = { ...real.items[0], title: "PA-263 检查对话", updated_at: new Date(now - 60_000).toISOString() };
    await page.route(at("/conversations"), (r) => (r.request().method() === "GET" ? json(r, 200, { ...real, items: [first] }) : r.fallback()));
    await page.clock.install({ time: now });
    await page.goto(origin + "/conversations");
    const row = page.locator("tr", { hasText: "PA-263 检查对话" }).first();
    await expect(row).toContainText("1分钟前", { timeout: 20_000 });
    await page.clock.fastForward("30:00");
    await expect(row).toContainText("31分钟前", { timeout: 10_000 });
  });
});
