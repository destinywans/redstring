import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * WAN-434 phase 2, the category sweep before round 3: review, execution, silence, routing and audit pages (group 1).
 * Every check pins the page to fixed API answers with page.route (see support/checks.ts), so it runs on any data
 * and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-sweep-review.spec.ts
 */
const origin = INSTANCES.prod.origin;
const at0 = "2026-10-17T09:00:00Z";

// PA-206 (WAN-565): outcome "unknown" (a silence awaiting its read-only check or a manual one) was listed as a red
// "failed", and a silence that lapsed before it was sent had no result at all.
const auditRow = (id: string, action: string, result: string | null) => ({
  id,
  time: at0,
  actor: { kind: "system", name: "silence-executor" },
  action,
  target: "pa206-check",
  result,
  source: "management_audit",
  ...(result === null ? { unavailable: { result: "unavailable" } } : {}),
});
const auditView = (items: object[]) => ({ items, counts: { all: items.length, user: 0, agent: 0, system: items.length }, volume: [[0, 0, items.length]] });

test.describe("audit results that aren't failures (PA-206)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("an unconfirmed write reads as awaiting a check and a lapsed one as expired, never failed", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/audit"), (r) =>
      json(r, 200, auditView([auditRow("9003", "v3.silence_create.manual_review", "unconfirmed"), auditRow("9002", "v3.silence_create.unknown", "unconfirmed"), auditRow("9001", "v3.silence_create.expired", "expired")])),
    );
    await page.goto(origin + "/settings/audit");
    const rows = page.locator("tr", { hasText: "pa206-check" });
    await expect(rows).toHaveCount(3, { timeout: 20_000 });
    await expect(page.locator("tr", { hasText: "v3.silence_create.unknown" })).toContainText("结果待核对");
    await expect(page.locator("tr", { hasText: "v3.silence_create.manual_review" })).toContainText("结果待核对");
    await expect(page.locator("tr", { hasText: "v3.silence_create.expired" })).toContainText("已过期，未执行");
    for (const text of ["失败", "未知"]) await expect(rows.filter({ hasText: text })).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });
});
