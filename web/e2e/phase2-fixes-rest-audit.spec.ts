import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * WAN-434 phase 2, the rest after the category sweep: audit results (PA-230).
 * Every check pins the page to fixed API answers with page.route (see support/checks.ts), so it runs on any data
 * and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-rest-audit.spec.ts
 */
const origin = INSTANCES.prod.origin;
const at0 = "2026-10-18T09:00:00Z";

// PA-230 (WAN-589): a rollout or an alert rule execution that stopped for a person read as "result unknown", and an
// outcome the server can't classify said the source had failed or timed out.
const auditRow = (id: string, action: string, result: string | null) => ({
  id,
  time: at0,
  actor: { kind: "system", name: "rollout-executor" },
  action,
  target: "pa230-check",
  result,
  source: "management_audit",
  ...(result === null ? { unavailable: { result: "unclassified" } } : {}),
});
const auditView = (items: object[]) => ({ items, counts: { all: items.length, user: 0, agent: 0, system: items.length }, volume: [[0, 0, items.length]] });

test.describe("audit results of lifecycle steps (PA-230)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a step that stopped for a person reads as needing attention, and an unclassified one doesn't blame the source", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/audit"), (r) =>
      json(r, 200, auditView([
        auditRow("9104", "v3.rollout.needs_attention", "needs_attention"),
        auditRow("9103", "v3.alert_rule.enable.execution_state", "needs_attention"),
        auditRow("9102", "v3.rollout.dispatching", "pending"),
        auditRow("9101", "v3.check.unclassifiable", null),
      ])),
    );
    await page.goto(origin + "/settings/audit");
    const rows = page.locator("tr", { hasText: "pa230-check" });
    await expect(rows).toHaveCount(4, { timeout: 20_000 });
    for (const action of ["v3.rollout.needs_attention", "v3.alert_rule.enable.execution_state"]) {
      const row = page.locator("tr", { hasText: action });
      await expect(row).toContainText("需要人工处理");
      await expect(row).not.toContainText("未知");
      await expect(row).not.toContainText("失败");
    }
    // Control: a progress step reads as pending, as before.
    await expect(page.locator("tr", { hasText: "v3.rollout.dispatching" })).toContainText("进行中");
    const unknown = page.locator("tr", { hasText: "v3.check.unclassifiable" }).locator(".unk");
    await expect(unknown).toHaveText("结果未知");
    await expect(unknown).not.toHaveAttribute("aria-label", /来源连接失败|超时/);
    // The result filter names the new result like the rows do.
    await page.locator(".demo-chip", { hasText: /^结果$/ }).getByRole("button").click();
    await expect(page.getByRole("option", { name: /需要人工处理/ })).toBeVisible();
    expect(m.pageErrors).toEqual([]);
  });
});
