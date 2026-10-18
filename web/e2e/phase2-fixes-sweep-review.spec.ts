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

// Silence page fixtures (PA-213, PA-228).
const silence = { id: "pa228-silence", matchers: [{ k: "application", v: "checkout-api" }], state: "active", starts_at: at0, ends_at: "2099-01-01T00:00:00Z", affects: 1, creator: "Check Creator", reason: "pa228 check" };
const silences = (items: object[]) => ({ items, counts: { active: items.length, planned: 0, expired: 0 }, empty_reason: null, unavailable: {} });
const pinSilences = async (page: Page, items: object[]) => {
  await page.route(at("/silences"), (r) => json(r, 200, silences(items)));
  await page.route(at("/inhibitions"), (r) => json(r, 200, { items: [] }));
};

// PA-213 (WAN-572): proposals submitted from the silence page kept a fixed "pending review" badge, read once from
// session storage and never again, even after they were approved, rejected or ran.
test.describe("proposals submitted on the silence page (PA-213)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("say they were submitted and leave the current state to the proposal page", async ({ page }) => {
    await page.addInitScript(zh);
    await page.addInitScript(() =>
      sessionStorage.setItem("aiops-v3-silence-proposals", JSON.stringify([{ proposal_id: "pa213-check", matchers: [{ k: "application", op: "=", v: "checkout-api" }], duration: 3600, at: Date.now() }])),
    );
    await pinSilences(page, []);
    await page.goto(origin + "/silences");
    const table = page.locator("table", { hasText: "你在本页提交的提案" });
    await expect(table).toContainText("pa213-check", { timeout: 20_000 });
    await expect(table).toContainText("已提交");
    await expect(table).not.toContainText("待审批");
    await expect(table.getByRole("link", { name: /pa213-check/ })).toHaveAttribute("href", "/review/pa213-check");
  });
});

// Execution fixtures (PA-223, PA-228, PA-229): a plain execution record and a Kubernetes rollback.
const execId = "pa223-exec";
const plainExecution = (patch: Record<string, unknown> = {}) => ({
  id: execId,
  plan_id: "plan-pa223-check",
  investigation_id: "inv-pa223-check",
  application_id: "app-check",
  environment_id: "prod",
  proposal_id: null,
  status: "succeeded",
  status_source: "executor",
  attempt: 1,
  error: "",
  created_at: at0,
  updated_at: at0,
  approval: { decision: "approved", approved_by: "Check Approver", digest_match: true },
  steps: [{ seq: 1, type: "execution.started", time: at0 }],
  steps_truncated: false,
  observation: { recovered: true, started_at: at0, finished_at: "2026-10-17T09:05:00Z", samples: 20, requests: 1200, reason: null },
  report: null,
  partial: false,
  missing: [],
  ...patch,
});
const rolloutExecution = (written: boolean) => ({
  id: execId,
  kind: "kubernetes.deployment.rollback",
  proposal_id: "prp-pa223-check",
  investigation_id: "inv-pa223-check",
  application_id: "app-check",
  environment_id: "prod",
  status: "needs_attention",
  status_reason: "reconcile_unknown",
  approved_at: at0,
  approvers: [{ name: "Check Approver", verified_at: at0 }],
  created_at: at0,
  updated_at: "2026-10-17T09:20:00Z",
  target: { namespace: "checkout", deployment: "checkout-api", from_revision: "12", target_revision: "11" },
  timeline: [
    { state: "reserved", at: at0 },
    { state: "dispatching", at: "2026-10-17T09:00:01Z" },
    ...(written ? [{ state: "applied", at: "2026-10-17T09:00:02Z" }, { state: "rolling_out", at: "2026-10-17T09:00:03Z" }] : [{ state: "unknown", at: "2026-10-17T09:00:02Z" }]),
    { state: "needs_attention", at: "2026-10-17T09:20:00Z" },
  ],
  rollout: written ? { desired: 2, updated: 1, available: 1, current_revision: "13", observed_at: "2026-10-17T09:00:30Z" } : null,
  verification: null,
});
const pinExecution = (page: Page, body: unknown) => page.route(at(`/executions/${execId}`), (r) => json(r, 200, body));

// PA-223 (WAN-582): reasons that didn't match what happened. A recovery check the executor couldn't judge said "the
// source failed, refresh to retry"; a rollback that was written and then couldn't read its Deployment said the write's
// outcome was unknown.
test.describe("unknown reasons on execution pages (PA-223)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a recovery check the executor couldn't judge says so, not that the source failed", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await pinExecution(page, plainExecution({ observation: { recovered: null, started_at: at0, finished_at: "2026-10-17T09:05:00Z", samples: 0, requests: null, reason: "pa223: no samples in the window" } }));
    await page.goto(origin + `/executions/${execId}`);
    const card = page.locator(".v3-card", { hasText: "恢复观测" });
    await expect(card).toContainText("pa223: no samples in the window", { timeout: 20_000 });
    await expect(card).toContainText("无法判定");
    await expect(card.locator(".unk")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("a rollback written and then unreadable says it was written; one never confirmed says the write is unknown", async ({ page }) => {
    await page.addInitScript(zh);
    let body = rolloutExecution(true);
    await page.route(at(`/executions/${execId}`), (r) => json(r, 200, body));
    await page.goto(origin + `/executions/${execId}`);
    const card = page.locator(".v3-card", { hasText: "需要人工处理" }).filter({ hasText: "执行器" });
    await expect(card).toContainText("已经写入", { timeout: 20_000 });
    await expect(card).toContainText("读不到 Deployment");
    await expect(card).not.toContainText("写入结果不明");
    body = rolloutExecution(false);
    await page.reload();
    await expect(card).toContainText("写入结果不明", { timeout: 20_000 });
    await expect(card).not.toContainText("已经写入");
  });
});
