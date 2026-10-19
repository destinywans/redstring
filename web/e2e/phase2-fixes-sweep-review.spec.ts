import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, patchRead, zh } from "./support/checks";

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
  ] as const) {
    test(`an alert rule execution in ${status}${code ? ` (${code})` : ""} reads as on the execution page: ${label}`, async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      let body = proposal({ status: "approved", action_kind: ALERT_RULE, decision: decision(), execution: execution(ALERT_RULE, status, code) });
      await page.route(at(`/proposals/${reviewId}`), (r) => json(r, 200, body));
      await page.goto(origin + `/review/${reviewId}`);
      const card = page.locator(".approve-card");
      await expect(card).toContainText("批准结果", { timeout: 20_000 });
      await expect(card).toContainText(label);
      await expect(card).not.toContainText("执行中");
      if (code === "load_timeout") {
        // Waiting for the provider without a timeout is still running.
        body = proposal({ status: "approved", action_kind: ALERT_RULE, decision: decision(), execution: execution(ALERT_RULE, status, null) });
        await page.reload();
        await expect(card).toContainText("执行中", { timeout: 20_000 });
        await expect(card).not.toContainText("需要处理");
      }
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
    // Both halves need someone who may approve: the acceptance admin holds no proposals.approve, and for them the
    // header says 待审批 whatever the vote.
    await page.goto(origin + "/healthz");
    await patchRead(page, "/me", (me) => ({
      ...me,
      permissions: Object.fromEntries(Object.entries((me?.permissions ?? {}) as Record<string, Record<string, Record<string, string>>>).map(([env, mods]) => [env, { ...mods, proposals: { ...(mods?.proposals ?? {}), approve: "conditional" } }])),
    }));
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

// PA-263 (WAN-617): the review page decided "already voted" once, at render; with the page left open past the vote's
// expiry it still said 待审批 instead of asking this approver again.
test.describe("a lapsed first vote while the page is open (PA-263)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("once my_vote.expires_at passes the header asks you to review again", async ({ page }) => {
    await page.addInitScript(zh);
    const now = Date.now();
    await page.clock.install({ time: now });
    const body = proposal({ action_kind: "alertmanager.silence.create", my_vote: { verified_at: new Date(now - 60_000).toISOString(), expires_at: new Date(now + 2 * 60_000).toISOString() } });
    await page.route(at(`/proposals/${reviewId}`), (r) => json(r, 200, body));
    await page.goto(origin + "/healthz");
    await patchRead(page, "/me", (me) => ({
      ...me,
      permissions: Object.fromEntries(Object.entries((me?.permissions ?? {}) as Record<string, Record<string, Record<string, string>>>).map(([env, mods]) => [env, { ...mods, proposals: { ...(mods?.proposals ?? {}), approve: "conditional" } }])),
    }));
    await page.goto(origin + `/review/${reviewId}`);
    const top = page.locator(".v3-topbar");
    await expect(top).toContainText("待审批", { timeout: 20_000 });
    await page.clock.fastForward("05:00");
    await expect(top).toContainText("待你复核", { timeout: 10_000 });
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

// Gate fixtures (PA-228): the signed-in admin's own /me with one grant taken away, so the entry must name it.
const withoutGrant = async (page: Page, module: string, action: string) => {
  await page.goto(origin + "/healthz");
  await patchRead(page, "/me", (me) => ({
    ...me,
    permissions: Object.fromEntries(Object.entries((me?.permissions ?? {}) as Record<string, Record<string, Record<string, string>>>).map(([env, mods]) => [env, { ...mods, [module]: Object.fromEntries(Object.entries(mods?.[module] ?? {}).filter(([a]) => a !== action)) }])),
  }));
};
const gateOf = (page: Page, entry: ReturnType<Page["getByRole"]>) => page.locator(".perm-gate", { has: entry }).locator(".sr-only");
const alertRuleExecution = {
  id: execId,
  kind: "prometheus.alert_rule.enable",
  proposal_id: "pa228-prp",
  investigation_id: "inv-pa228-check",
  application_id: "app-check",
  environment_id: "prod",
  approved_at: at0,
  approvers: [],
  created_at: at0,
  updated_at: at0,
  alert_rule: {
    proposal_id: "pa228-prp",
    operation: "enable",
    state: "needs_attention",
    steps: [
      { state: "committing", at: at0 },
      { state: "merging", at: at0 },
      { state: "merged", at: at0 },
      { state: "loading", at: at0 },
      { state: "needs_attention", at: at0, reason: "load_timeout" },
    ],
    repository: { project_id: "6", project_path: "ops/rules", source_branch: "aiops/pa228", target_branch: "main", path: "rules/checkout.yaml", commit_sha: null, mr_iid: null, mr_url: null, merged_sha: null, merged_at: null },
    readback: { state: "timeout", checked_at: null, first_seen_at: null, health: null, last_evaluation_at: null, last_error: null, reload: null },
    failure: { code: "load_timeout", message: "check" },
  },
};
const routeRoot = (receivers: string[]) => ({
  root: {
    id: "pa228-root",
    title: "prod",
    matchers: [{ k: "env", op: "=", value_redacted: false, value: "prod" }],
    receiver_refs: receivers,
    group_by: [],
    group_wait: "30s",
    group_interval: "5m",
    repeat_interval: "4h",
    continue: false,
    conditional: false,
    children: [],
  },
  root_unavailable: null,
  config_revision: null,
  partial: false,
  unavailable: [],
});

// PA-228 (WAN-587): write entries that stayed enabled without the grant their route declares: publishing an
// execution report (knowledge.manage), rechecking an alert rule execution (alert_rules.change), testing routing
// (alert_rules.manage) and proposing to end a silence early (silences.manage).
test.describe("write entries gated by the route's grant (PA-228)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("publishing an execution report needs knowledge · manage", async ({ page }) => {
    await page.addInitScript(zh);
    await withoutGrant(page, "knowledge", "manage");
    const report = { title: "pa228 report", summary: "check", closed_by: "Check Closer", approved_by: "Check Approver", closed_at: at0, published: false, published_by: null, published_at: null };
    await pinExecution(page, plainExecution({ report }));
    let posted = false;
    await page.route(at(`/executions/${execId}/report/publish`), (r) => ((posted = true), json(r, 403, { error: { code: "forbidden", message: "check" } })));
    await page.goto(origin + `/executions/${execId}`);
    const entry = page.getByRole("button", { name: "确认发布" });
    await expect(entry).toBeDisabled({ timeout: 20_000 });
    await expect(gateOf(page, entry)).toHaveText("需要 知识来源与检索·管理 权限");
    await entry.click({ force: true });
    await page.waitForTimeout(300);
    expect(posted).toBe(false);
  });

  test("rechecking an alert rule execution needs alert rules · change", async ({ page }) => {
    await page.addInitScript(zh);
    await withoutGrant(page, "alert_rules", "change");
    await pinExecution(page, alertRuleExecution);
    await page.route(at("/proposals/pa228-prp"), (r) => json(r, 404, { error: { code: "not_found", message: "check" } }));
    let posted = false;
    await page.route(at(`/executions/${execId}/recheck`), (r) => ((posted = true), json(r, 403, { error: { code: "forbidden", message: "check" } })));
    await page.goto(origin + `/executions/${execId}`);
    const entry = page.getByRole("button", { name: "重新核对" });
    await expect(entry).toBeDisabled({ timeout: 20_000 });
    await expect(gateOf(page, entry)).toHaveText("需要 告警规则与路由·修改 权限");
    await entry.click({ force: true });
    await page.waitForTimeout(300);
    expect(posted).toBe(false);
  });

  test("testing routing needs alert rules · manage, also from the keyboard", async ({ page }) => {
    await page.addInitScript(zh);
    await withoutGrant(page, "alert_rules", "manage");
    await page.route(at("/notification-routes"), (r) => json(r, 200, routeRoot(["pa228-receiver"])));
    let posted = false;
    await page.route(at("/notification-routes/test"), (r) => ((posted = true), json(r, 403, { error: { code: "forbidden", message: "check" } })));
    await page.goto(origin + "/routing");
    const entry = page.getByRole("button", { name: "测试", exact: true });
    await expect(entry).toBeDisabled({ timeout: 20_000 });
    await expect(gateOf(page, entry)).toHaveText("需要 告警规则与路由·管理 权限");
    const labels = page.getByRole("textbox", { name: "标签" });
    await labels.fill("application=checkout-api");
    await labels.press("Enter");
    await page.waitForTimeout(300);
    expect(posted).toBe(false);
  });

  test("proposing to end a silence early needs silences · manage", async ({ page }) => {
    await page.addInitScript(zh);
    await withoutGrant(page, "silences", "manage");
    await pinSilences(page, [silence]);
    await page.goto(origin + "/silences");
    const row = page.locator("tr", { hasText: "pa228 check" });
    const entry = row.getByRole("button", { name: "提议提前结束" });
    await expect(entry).toBeDisabled({ timeout: 20_000 });
    await expect(row.locator(".perm-gate .sr-only")).toHaveText("需要 静默·管理 权限");
  });
});

// PA-229 (WAN-588): wording that didn't match the flow. The review page's generic card promised a 180 s watch after
// execution (a silence is rechecked before it is written); a rollback that had already ended still said verification
// and the rollout hadn't started yet; the routing page's Receivers tab was a bare header without a route tree, and its
// channel column was always empty on a real server.
test.describe("wording that matches the flow (PA-229)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a silence proposal's checks are titled as checks before writing, not a watch after it", async ({ page }) => {
    await page.addInitScript(zh);
    await pinProposal(page, proposal({ action_kind: "alertmanager.silence.create" }));
    await page.goto(origin + `/review/${reviewId}`);
    await expect(page.getByText("执行前复核", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("执行后自动观测 180 秒")).toHaveCount(0);
    await expect(page.getByText("Recheck the digest before writing")).toBeVisible();
  });

  test("a rollback that already ended doesn't say verification or the rollout hasn't started yet", async ({ page }) => {
    await page.addInitScript(zh);
    await pinExecution(page, rolloutExecution(false));
    await page.goto(origin + `/executions/${execId}`);
    await expect(page.getByText("写入结果不明", { exact: false }).first()).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("还没有开始验证")).toHaveCount(0);
    await expect(page.getByText("还没有开始滚动")).toHaveCount(0);
    await expect(page.getByText("没有进行验证")).toBeVisible();
    await expect(page.getByText("没有滚动记录")).toBeVisible();
  });

  test("the Receivers tab explains a missing route tree and doesn't leave the channel blank", async ({ page }) => {
    await page.addInitScript(zh);
    let body: unknown = { root: null, root_unavailable: "not_configured", config_revision: null, partial: false, unavailable: [] };
    await page.route(at("/notification-routes"), (r) => json(r, 200, body));
    await page.goto(origin + "/routing");
    await page.getByRole("tab", { name: "接收人" }).click({ timeout: 20_000 });
    await expect(page.getByText("这个环境还没有通知路由")).toBeVisible();
    body = routeRoot(["pa229-receiver"]);
    await page.reload();
    await page.getByRole("tab", { name: "接收人" }).click({ timeout: 20_000 });
    const row = page.locator("tr", { hasText: "pa229-receiver" });
    await expect(row).toBeVisible();
    await expect(row.locator("td").nth(1)).toContainText("—");
  });
});
