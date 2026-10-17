import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, zh } from "./support/checks";

/**
 * PA-173 (WAN-507): the rejection reason reaches the proposer on the review page (option A: in the decision card),
 * and the reject dialog no longer promises the AI picks it up. The proposal detail is pinned with page.route, so this
 * runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-rejection.spec.ts
 */
const origin = INSTANCES.prod.origin;
const id = "pa173-check";
const at0 = "2026-10-17T09:00:00Z";
const proposal = (patch: Record<string, unknown>) => ({
  id,
  title: "Check: roll back checkout-api",
  status: "pending",
  risk: "medium",
  impact: "checkout-api in prod",
  reversible: true,
  proposed_by: "check-proposer",
  proposed_by_name: "Check Proposer",
  proposed_at: at0,
  investigation_id: "inv-pa173-check",
  target: "deployment/checkout-api",
  diff: { path: "deployment/checkout-api", lines: [] },
  method: "rollback",
  evidence: [],
  verification: [],
  verification_note: "",
  checklist: ["Target confirmed"],
  requirement: "Two approvers with passkeys",
  discussion: [],
  execution: null,
  decision: null,
  ...patch,
});
const rejected = (reason: string | null) =>
  proposal({ status: "rejected", decision: { state: "rejected", decided_at: at0, approvers: [], rejected_by: { id: "u-check", name: "Check Approver", at: at0 }, reason, revoked: null } });

test.describe("rejection reason on the review page (PA-173)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

  test("the decision card shows the reason and who rejected; nothing points to the discussion", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at(`/proposals/${id}`), (r) => json(r, 200, rejected("Rollback window overlaps the release freeze")));
    await page.goto(origin + `/review/${id}`);
    await expect(page.getByText("审批人拒绝了这个提案")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("没有执行，也不会执行。", { exact: true })).toBeVisible();
    await expect(page.getByText("拒绝理由", { exact: true })).toBeVisible();
    await expect(page.getByText("Rollback window overlaps the release freeze")).toBeVisible();
    await expect(page.locator(".v3-kv", { hasText: "拒绝人" })).toContainText("Check Approver");
    await expect(page.locator(".v3-kv", { hasText: "拒绝于" })).toBeVisible();
    await expect(page.getByText("拒绝说明在下方讨论里")).toHaveCount(0);
    await expect(page.getByText("拒绝时没有填写理由")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("a rejection without a reason says none was given", async ({ page }) => {
    await page.addInitScript(en);
    await page.route(at(`/proposals/${id}`), (r) => json(r, 200, rejected(null)));
    await page.goto(origin + `/review/${id}`);
    await expect(page.getByText("An approver rejected this proposal")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("No reason was given")).toBeVisible();
    await expect(page.getByText("The reason is in the discussion below")).toHaveCount(0);
  });
});

test.describe("reject dialog (PA-173)", () => {
  // Rejecting needs the approve grant.
  test.use({ storageState: statePath("prod", "approver1"), viewport: { width: 1440, height: 1000 } });

  test("says the proposer sees the reason, not that the AI takes it as a constraint", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/proposals/${id}`), (r) => json(r, 200, proposal({})));
    let posted = false;
    await page.route(at(`/proposals/${id}/reject`), (r) => {
      posted = true;
      return json(r, 409, { error: { code: "proposal_closed", message: "check" } });
    });
    await page.goto(origin + `/review/${id}`);
    await page.getByRole("button", { name: "拒绝", exact: true }).click({ timeout: 20_000 });
    await expect(page.getByText("说明拒绝原因，发起人会在审阅页看到。")).toBeVisible();
    await expect(page.getByText("AI 会把它作为新的约束")).toHaveCount(0);
    await page.getByRole("button", { name: "取消", exact: true }).click();
    expect(posted).toBe(false);
  });
});
