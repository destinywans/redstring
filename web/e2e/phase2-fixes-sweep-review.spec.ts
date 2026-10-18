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

// Review page fixtures (PA-205, PA-219, PA-229): one proposal, pinned with page.route.
const reviewId = "pa205-check";
const proposal = (patch: Record<string, unknown> = {}) => ({
  id: reviewId,
  title: "Check: enable an alert rule",
  status: "pending",
  risk: "medium",
  impact: "checkout-api in prod",
  reversible: true,
  proposed_by: "check-proposer",
  proposed_by_name: "Check Proposer",
  proposed_at: at0,
  investigation_id: "inv-pa205-check",
  target: "rules/checkout.yaml",
  diff: { path: "rules/checkout.yaml", lines: [] },
  method: "Reviewed GitOps merge",
  evidence: [],
  verification: ["Recheck the digest before writing"],
  verification_note: "",
  checklist: ["Target confirmed"],
  requirement: "Two approvers with passkeys",
  discussion: [],
  action_kind: null,
  rollback: null,
  alert_rule: null,
  execution: null,
  decision: null,
  ...patch,
});
const approvers = [
  { id: "u-check-a", name: "Check Approver A", method: "passkey", at: at0 },
  { id: "u-check-b", name: "Check Approver B", method: "passkey", at: at0 },
];
const decision = (patch: Record<string, unknown> = {}) => ({ state: "approved", decided_at: at0, approvers, rejected_by: null, reason: null, revoked: null, ...patch });
const execution = (kind: string, status: string | null, failure_code: string | null = null) => ({
  execution_id: "pa205-exec",
  approved_at: at0,
  action_kind: kind,
  status,
  upstream_id: null,
  failure_code,
  unavailable: { upstream_id: "not_generated", ...(status === null ? { status: "not_generated" } : {}) },
});
const pinProposal = (page: Page, body: unknown) => page.route(at(`/proposals/${reviewId}`), (r) => json(r, 200, body));

// PA-205 (WAN-564): the review page read alert rule executor states it didn't know (stale_remote, a load timeout,
// uncertain) as "executing" while the execution page said "needs attention"; an expired approval said two votes
// never came in; the header ignored expiry and withdrawal; and a first approver was still asked to review.
test.describe("review page states (PA-205)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  const ALERT_RULE = "prometheus.alert_rule.enable";
  for (const [status, code, label] of [
    ["stale_remote", null, "需要处理"],
    ["awaiting_provider", "load_timeout", "需要处理"],
    ["uncertain", null, "对账中"],
    ["failed", "rule_health_error", "求值出错"],
    ["awaiting_provider", null, "执行中"],
  ] as const) {
    test(`an alert rule execution in ${status}${code ? ` (${code})` : ""} reads as on the execution page: ${label}`, async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      await pinProposal(page, proposal({ status: "approved", action_kind: ALERT_RULE, decision: decision(), execution: execution(ALERT_RULE, status, code) }));
      await page.goto(origin + `/review/${reviewId}`);
      const card = page.locator(".approve-card");
      await expect(card).toContainText("批准结果", { timeout: 20_000 });
      await expect(card).toContainText(label);
      if (label !== "执行中") await expect(card).not.toContainText("执行中");
      expect(m.pageErrors).toEqual([]);
    });
  }

  test("an expired approval says both approved and no executor took it up; the header says expired", async ({ page }) => {
    await page.addInitScript(zh);
    const SILENCE = "alertmanager.silence.create";
    await pinProposal(page, proposal({ status: "approved", action_kind: SILENCE, decision: decision({ state: "expired" }), execution: execution(SILENCE, null) }));
    await page.goto(origin + `/review/${reviewId}`);
    const card = page.locator(".approve-card");
    await expect(card).toContainText("两人已批准", { timeout: 20_000 });
    await expect(card).toContainText("执行器没有在有效期内接手");
    await expect(card).toContainText("没有执行");
    await expect(card).not.toContainText("审批期限内没有凑齐两票");
    const top = page.locator(".v3-topbar");
    await expect(top).toContainText("已过期");
    await expect(top).not.toContainText("已批准");
  });

  test("a withdrawn approval reads as withdrawn in the header", async ({ page }) => {
    await page.addInitScript(zh);
    const SILENCE = "alertmanager.silence.create";
    await pinProposal(page, proposal({ status: "approved", action_kind: SILENCE, decision: decision({ revoked: { id: "u-check-a", name: "Check Approver A", at: at0 } }), execution: execution(SILENCE, null) }));
    await page.goto(origin + `/review/${reviewId}`);
    await expect(page.locator(".approve-card")).toContainText("撤回了批准", { timeout: 20_000 });
    const top = page.locator(".v3-topbar");
    await expect(top).toContainText("已撤回");
    await expect(top).not.toContainText("已批准");
  });

  test("after casting the first vote the header no longer asks you to review", async ({ page }) => {
    await page.addInitScript(zh);
    const now = Date.now();
    const pending = (my_vote: unknown) => proposal({ action_kind: "alertmanager.silence.create", my_vote });
    let body: unknown = pending({ verified_at: new Date(now - 60_000).toISOString(), expires_at: new Date(now + 14 * 60_000).toISOString() });
    await page.route(at(`/proposals/${reviewId}`), (r) => json(r, 200, body));
    await page.goto(origin + `/review/${reviewId}`);
    const top = page.locator(".v3-topbar");
    await expect(top).toContainText("待审批", { timeout: 20_000 });
    await expect(top).not.toContainText("待你复核");
    // Without a counted vote of yours the approver is asked, as before.
    body = pending(null);
    await page.reload();
    await expect(top).toContainText("待你复核", { timeout: 20_000 });
  });
});

// PA-219 (WAN-578): a proposal the page couldn't read (another environment's, removed, no grant, server down) left the
// review page on its skeleton for ever.
test.describe("review page read failures (PA-219)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const [status, code, text] of [
    [404, "not_found", "找不到这条记录"],
    [403, "forbidden", "没有权限查看"],
    [503, "proposal_unavailable", "暂时不可用"],
  ] as const) {
    test(`a ${status} says why instead of loading for ever`, async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at(`/proposals/${reviewId}`), (r) => json(r, status, { error: { code, message: "check" } }));
      await page.goto(origin + `/review/${reviewId}`);
      await expect(page.getByText(text, { exact: true })).toBeVisible({ timeout: 20_000 });
      await expect(page.locator(".v3-page-body .skeleton")).toHaveCount(0);
    });
  }
});
