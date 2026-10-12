/**
 * Alert rule enable / disable through GitOps (D11 §11.4–11.6). Sample data only: the executor isn't built yet, so
 * every state of the review, execution and rule list pages comes from here.
 */
import { mock, MockError } from "../router";
import { l } from "../../i18n";
import { at } from "../time";

const P = (zh: string, en: string) => l(zh, en);
const SHA = (s: string) => `${s}${"0".repeat(40 - s.length)}`;
const REPO = { connector_id: "gitlab-lab", project_id: "12", project_path: "aiops-lab/prometheus-rules", web_url: "https://gitlab.lab.internal/aiops-lab/prometheus-rules", target_branch: "main", path: "rules/prod/checkout-api.yml", base_sha: SHA("1a2b3c4") };
const GROUP = "redstring-prod-checkout-api";

type Rule = { name: string; expr: string; for: string; severity: string; summary: unknown };
const RULES: Record<string, Rule> = {
  CheckoutHigh5xx: { name: "CheckoutHigh5xx", expr: 'sum(rate(http_requests_total{service="checkout-api",code=~"5.."}[5m]))\n  / sum(rate(http_requests_total{service="checkout-api"}[5m])) > 0.02', for: "5m", severity: "P2", summary: P("checkout-api 5xx 比例超过 2%", "checkout-api 5xx ratio above 2%") },
  InventoryLag: { name: "InventoryLag", expr: 'max(kafka_consumergroup_lag{group="inventory-worker"}) > 5000', for: "10m", severity: "P3", summary: P("库存消费积压超过 5000", "Inventory consumer lag above 5000") },
  CheckoutLegacyErrors: { name: "CheckoutLegacyErrors", expr: 'sum(rate(checkout_legacy_errors_total[5m])) > 1', for: "5m", severity: "P3", summary: P("旧版错误计数（已被 CheckoutHigh5xx 取代）", "Legacy error counter (replaced by CheckoutHigh5xx)") },
  OrderQueueDepth: { name: "OrderQueueDepth", expr: 'max(order_queue_depth{service="order-service"}) > 200', for: "5m", severity: "P2", summary: P("下单队列积压超过 200", "Order queue above 200") },
  PaymentTimeout: { name: "PaymentTimeout", expr: 'sum(rate(payment_callback_timeouts_total[5m])) > 0.5', for: "5m", severity: "P2", summary: P("支付回调超时", "Payment callback timeouts") },
  CheckoutRestarts: { name: "CheckoutRestarts", expr: 'increase(kube_pod_container_status_restarts_total{namespace="aiops-lab",container="api"}[15m]) > 3', for: "0m", severity: "P3", summary: P("实例 15 分钟内重启超过 3 次", "More than 3 restarts in 15 min") },
  CartRetryRatio: { name: "CartRetryRatio", expr: 'sum(rate(cart_retries_total[5m])) / sum(rate(cart_requests_total[5m])) > 0.2', for: "10m", severity: "P3", summary: P("购物车重试占比过高", "High cart retry ratio") },
};

const ruleLines = (r: Rule) => [
  `      - alert: ${r.name}`,
  ...r.expr.split("\n").map((x, i) => (i === 0 ? `        expr: ${x}` : `        ${x.trim()}`)),
  `        for: ${r.for}`,
  "        labels:",
  `          severity: ${r.severity}`,
  "          team: trade",
  "        annotations:",
  `          summary: ${r.name}`,
];
const context = ["groups:", `  - name: ${GROUP}`, "    rules:", "      - alert: CheckoutSlowCheckout", "        expr: histogram_quantile(0.99, …) > 1.5", "        for: 10m"];

type ProposalSpec = { op: "enable" | "disable"; rule: string; status: "pending" | "approved" | "executed"; execution?: string };
const PROPOSALS: Record<string, ProposalSpec> = {
  "PRP-112": { op: "enable", rule: "CheckoutHigh5xx", status: "pending" },
  "PRP-113": { op: "enable", rule: "InventoryLag", status: "pending" },
  "PRP-114": { op: "disable", rule: "CheckoutLegacyErrors", status: "pending" },
  "PRP-115": { op: "enable", rule: "CheckoutHigh5xx", status: "pending" },
  // An older proposal: no difference was recorded and the binding names no project path or web URL.
  "PRP-116": { op: "enable", rule: "InventoryLag", status: "pending" },
  "PRP-109": { op: "enable", rule: "CheckoutHigh5xx", status: "executed", execution: "EXE-91" },
  "PRP-110": { op: "enable", rule: "OrderQueueDepth", status: "approved", execution: "EXE-90" },
  "PRP-111": { op: "enable", rule: "PaymentTimeout", status: "approved", execution: "EXE-92" },
  "PRP-108": { op: "enable", rule: "CheckoutHigh5xx", status: "approved", execution: "EXE-93" },
  "PRP-107": { op: "enable", rule: "CheckoutHigh5xx", status: "approved", execution: "EXE-94" },
  "PRP-106": { op: "enable", rule: "CheckoutHigh5xx", status: "approved", execution: "EXE-95" },
  "PRP-105": { op: "disable", rule: "CartRetryRatio", status: "approved", execution: "EXE-96" },
  "PRP-104": { op: "disable", rule: "CheckoutLegacyErrors", status: "executed", execution: "EXE-97" },
  "PRP-103": { op: "disable", rule: "CheckoutRestarts", status: "approved", execution: "EXE-98" },
  "PRP-117": { op: "enable", rule: "CheckoutHigh5xx", status: "executed", execution: "EXE-99" },
  "PRP-118": { op: "enable", rule: "InventoryLag", status: "approved", execution: "EXE-89" },
};
export const isAlertRuleProposal = (id: string) => id in PROPOSALS;

export function alertRuleProposal(id: string) {
  const spec = PROPOSALS[id];
  const r = RULES[spec.rule];
  const enable = spec.op === "enable";
  const body = ruleLines(r);
  const lines = [...context.map((text) => ({ op: " ", text })), ...body.map((text) => ({ op: enable ? "+" : "-", text }))];
  return {
    id,
    title: enable ? P(`启用告警规则 · checkout-api · ${r.name}`, `Enable alert rule · checkout-api · ${r.name}`) : P(`停用告警规则 · checkout-api · ${r.name}`, `Disable alert rule · checkout-api · ${r.name}`),
    status: spec.status,
    risk: enable ? "low" : "medium",
    impact: enable ? P("新增一条告警规则；不改其他规则，不发送通知以外的动作。", "Adds one alert rule; no other rule changes, nothing runs beyond notifications.") : P("删掉这条规则后，这类问题只由 CheckoutHigh5xx 覆盖。", "Once removed, this kind of problem is covered only by CheckoutHigh5xx."),
    reversible: true,
    proposed_by: "王晨",
    proposed_at: at("14:20"),
    investigation_id: "INV-231",
    target: `${REPO.project_path} · ${REPO.path}`,
    diff: { path: REPO.path, lines: lines.map((x) => `${x.op}${x.text}`) },
    method: P("两人批准后执行器提交、fast-forward 合并，Prometheus 加载后读回", "After two approvals the executor commits and fast-forward merges; read back once Prometheus loads it"),
    evidence: [{ n: 1, icon: "activity", title: P("INC-1042 期间 5xx 比例 8.4%，现有规则只在 P1 触发", "5xx hit 8.4% during INC-1042; the existing rule only fires at P1"), desc: P("Prometheus · 14:05–14:22", "Prometheus · 14:05–14:22") }],
    verification: [],
    verification_note: "",
    checklist: enable
      ? [P("规则和整文件差异我都看过了", "I've read the rule and the whole-file difference"), P("目标仓库、分支、文件正确", "Repository, branch and file are right"), P("这条规则不会和现有规则重复告警", "This rule won't alert twice with an existing one")]
      : [P("规则和整文件差异我都看过了", "I've read the rule and the whole-file difference"), P("目标仓库、分支、文件正确", "Repository, branch and file are right"), P("停用后这类问题仍有其他规则或人工覆盖", "After disabling, other rules or people still cover this kind of problem")],
    requirement: P("需要两位非发起人用通行密钥批准，生产和非生产都一样。", "Two people other than the proposer approve with a passkey, in production and non-production alike."),
    discussion: [],
    // PRP-117 also carries the D37 decision record (the others take approvers from the execution).
    decision: id === "PRP-117" ? { state: "approved", decided_at: at("11:03"), approvers: [{ id: "u-li", name: "李明", method: "passkey", at: at("11:02") }, { id: "u-zhou", name: "周航", method: "passkey", at: at("11:03") }], rejected_by: null } : undefined,
    execution: spec.execution ? { execution_id: spec.execution, approved_at: at("14:29"), action_kind: `prometheus.alert_rule.${spec.op}`, status: spec.status === "executed" ? (spec.op === "enable" ? "enabled" : "disabled") : "executing", upstream_id: null, unavailable: {} } : null,
    action_kind: `prometheus.alert_rule.${spec.op}`,
    rollback: null,
    alert_rule: {
      operation: spec.op,
      rule: { name: r.name, group: GROUP, expr: r.expr, for: r.for, severity: r.severity, labels: { application: "checkout-api", environment: "prod", severity: r.severity, team: "trade" }, annotations: { summary: r.summary } },
      repository: id === "PRP-116" ? { ...REPO, connector_id: null, project_path: null, web_url: null } : REPO,
      change: id === "PRP-116" ? null : { lines, content_sha256: "5c1e9a7d3b2f8e4c6a1d9b7f3e5c2a8d4b6f1e9c7a3d5b2f8e6c4a1d9b7f3e5c" },
      prometheus: { group: GROUP, file: `/etc/prometheus-gitops/current/${REPO.path}`, readback_timeout_seconds: 300 },
      merge: { method: "fast_forward", by: "executor" },
    },
  };
}

type Exec = { proposal: string; op: "enable" | "disable"; state: string; steps: ([string, string] | [string, string, string])[]; mr?: boolean; merged?: boolean; readback: Record<string, unknown>; failure?: { code: string; message: string } | null; observed?: string; updated?: string; noCommit?: boolean };
const pendingReadback = { state: "pending", checked_at: null, first_seen_at: null, health: null, last_evaluation_at: null, last_error: null, reload: null };
const EXECS: Record<string, Exec> = {
  // Like the lab's run: the first commit's outcome was unknown, reconciled read-only, then committed again.
  "EXE-91": { proposal: "PRP-109", op: "enable", state: "enabled", steps: [["reserved", "14:30:02"], ["committing", "14:30:02"], ["uncertain", "14:30:04", "prepare_unknown"], ["committing", "14:30:06"], ["merging", "14:30:09"], ["merged", "14:30:14"], ["loading", "14:30:15"], ["enabled", "14:30:59"]], mr: true, merged: true, readback: { state: "loaded", checked_at: at("14:30:59"), first_seen_at: at("14:30:44"), health: "ok", last_evaluation_at: at("14:30:58"), last_error: null, reload: { last_successful: true, last_success_at: at("14:30:43") } } },
  "EXE-90": { proposal: "PRP-110", op: "enable", state: "needs_attention", steps: [["reserved", "14:30:02"], ["needs_attention", "14:30:05", "stale_base"]], readback: pendingReadback, failure: { code: "stale_base", message: "main moved from 1a2b3c4 to e91f0a2 before commit" } },
  // Like nonprod 1e20c087…: stopped at plan_changed before writing anything (no commit).
  "EXE-89": { proposal: "PRP-118", op: "enable", state: "needs_attention", noCommit: true, steps: [["reserved", "14:30:02"], ["committing", "14:30:02"], ["needs_attention", "14:30:03", "plan_changed"]], readback: pendingReadback, failure: { code: "plan_changed", message: "The target file or branch changed before the change was written." } },
  "EXE-92": { proposal: "PRP-111", op: "enable", state: "merging", steps: [["reserved", "14:30:02"], ["committing", "14:30:06"], ["merging", "14:30:09"]], mr: true, readback: pendingReadback },
  "EXE-93": { proposal: "PRP-108", op: "enable", state: "uncertain", steps: [["reserved", "14:30:02"], ["committing", "14:30:06"], ["merging", "14:30:09"], ["uncertain", "14:30:21", "merge_unknown"]], mr: true, readback: pendingReadback },
  // PA-90: the timeout is its own step, and readback.reload says how Prometheus's last reload went.
  "EXE-94": { proposal: "PRP-107", op: "enable", state: "needs_attention", steps: [["reserved", "14:30:02"], ["committing", "14:30:06"], ["merging", "14:30:09"], ["merged", "14:30:14"], ["loading", "14:30:15"], ["needs_attention", "14:35:14", "load_timeout"]], mr: true, merged: true, readback: { state: "timeout", checked_at: at("14:35:14"), first_seen_at: null, health: null, last_evaluation_at: null, last_error: null, reload: { last_successful: false, last_success_at: at("14:12:40"), unavailable: null } }, failure: { code: "load_timeout", message: "rule not loaded within 300s; last reload failed" } },
  "EXE-95": { proposal: "PRP-106", op: "enable", state: "failed", steps: [["reserved", "14:30:02"], ["committing", "14:30:06"], ["merging", "14:30:09"], ["merged", "14:30:14"], ["loading", "14:30:15"], ["failed", "14:30:59", "rule_health_error"]], mr: true, merged: true, readback: { state: "error", checked_at: at("14:30:59"), first_seen_at: at("14:30:44"), health: "err", last_evaluation_at: at("14:30:58"), last_error: "many-to-many matching not allowed", reload: { last_successful: true, last_success_at: at("14:30:43") } }, failure: { code: "rule_health_error", message: "health=err" }, observed: "r-gl-5xx" },
  "EXE-96": { proposal: "PRP-105", op: "disable", state: "loading", steps: [["reserved", "14:40:02"], ["committing", "14:40:05"], ["merging", "14:40:08"], ["merged", "14:40:12"], ["loading", "14:40:13"]], mr: true, merged: true, readback: pendingReadback },
  "EXE-98": { proposal: "PRP-103", op: "disable", state: "needs_attention", steps: [["reserved", "13:50:02"], ["committing", "13:50:05"], ["merging", "13:50:08"], ["needs_attention", "13:52:40", "mr_closed"]], mr: true, readback: pendingReadback, failure: { code: "mr_closed", message: "merge request closed by a user" } },
  // Like nonprod 0d27bb65…: the load timed out, the executor later read the rule (enabled), then a person rechecked.
  "EXE-99": { proposal: "PRP-117", op: "enable", state: "enabled", steps: [["reserved", "11:03:58"], ["committing", "11:03:59"], ["merging", "11:04:01"], ["merged", "11:04:02"], ["loading", "11:04:02"], ["needs_attention", "11:09:02", "load_timeout"], ["enabled", "11:10:07"], ["recheck", "11:10:25", "manual"]], mr: true, merged: true, readback: { state: "loaded", checked_at: at("11:10:25"), first_seen_at: at("11:10:07"), health: "ok", last_evaluation_at: at("11:10:20"), last_error: null, reload: { last_successful: true, last_success_at: at("11:09:58"), unavailable: null } } },
  "EXE-97": { proposal: "PRP-104", op: "disable", state: "disabled", steps: [["reserved", "13:10:02"], ["committing", "13:10:05"], ["merging", "13:10:08"], ["merged", "13:10:12"], ["loading", "13:10:13"], ["disabled", "13:10:51"]], mr: true, merged: true, readback: { state: "absent", checked_at: at("13:10:51"), first_seen_at: null, health: null, last_evaluation_at: null, last_error: null, reload: { last_successful: true, last_success_at: at("13:10:40") } } },
};
// A real-length hex ID (as the server issues them) for the long-ID layout.
EXECS["b85115ba94f5fc6768d58d5fe6d37e21"] = EXECS["EXE-91"];
export const isAlertRuleExecution = (id: string) => id in EXECS;

export function alertRuleExecution(id: string) {
  const x = EXECS[id];
  const reached = (s: string) => x.steps.some(([st]) => st === s);
  const iid = 57 + Number(id.slice(-1));
  return {
    id,
    kind: `prometheus.alert_rule.${x.op}`,
    proposal_id: x.proposal,
    investigation_id: "INV-231",
    application_id: "checkout-api",
    environment_id: "prod",
    created_at: at(x.steps[0][1]),
    updated_at: at(x.updated ?? x.steps.at(-1)![1]),
    approved_at: at("14:29"),
    approvers: [
      { name: "李明", verified_at: at("14:27") },
      { name: "周航", verified_at: at("14:29") },
    ],
    alert_rule: {
      proposal_id: x.proposal,
      operation: x.op,
      state: x.state,
      steps: x.steps.map(([state, t, reason]) => ({ state, at: at(t), reason: reason ?? null })),
      repository: {
        project_id: REPO.project_id,
        project_path: REPO.project_path,
        source_branch: `redstring/alert-rule/${id}`,
        target_branch: REPO.target_branch,
        path: REPO.path,
        commit_sha: reached("committing") && !x.noCommit ? SHA("7c2d9e1") : null,
        mr_iid: x.mr ? iid : null,
        mr_url: x.mr ? `${REPO.web_url}/-/merge_requests/${iid}` : null,
        merged_sha: x.merged ? SHA("7c2d9e1") : null,
        merged_at: x.merged ? at(x.steps.find(([s]) => s === "merged")![1]) : null,
      },
      readback: x.readback,
      failure: x.failure ?? null,
      observed_rule_id: x.observed ?? null,
    },
  };
}

/** Rules in effect that show each lifecycle state (§11.6), appended to the sample rule list. */
export const LIFECYCLE_RULES = [
  { id: "r-gl-5xx", name: "CheckoutHigh5xx", state: "ok", lifecycle: { state: "active", proposal_id: null, execution_id: null } },
  { id: "r-gl-legacy", name: "CheckoutLegacyErrors", state: "ok", lifecycle: { state: "disable_pending_approval", proposal_id: "PRP-114", execution_id: null } },
  { id: "r-gl-cart", name: "CartRetryRatio", state: "ok", lifecycle: { state: "disabling", proposal_id: "PRP-105", execution_id: "EXE-96" } },
  { id: "r-gl-restarts", name: "CheckoutRestarts", state: "firing", lifecycle: { state: "needs_attention", proposal_id: "PRP-103", execution_id: "EXE-98" } },
];
export const RULE_PENDING = [
  { proposal_id: "PRP-113", execution_id: null, operation: "enable", rule_name: "InventoryLag", group: GROUP, application_id: "checkout-api", environment_id: "prod", state: "pending_approval", updated_at: at("14:24"), href: "/review/PRP-113" },
  { proposal_id: "PRP-112", execution_id: null, operation: "enable", rule_name: "CheckoutHigh5xx", group: GROUP, application_id: "checkout-api", environment_id: "prod", state: "pending_approval", updated_at: at("14:20"), href: "/review/PRP-112" },
  { proposal_id: "PRP-111", execution_id: "EXE-92", operation: "enable", rule_name: "PaymentTimeout", group: GROUP, application_id: "checkout-api", environment_id: "prod", state: "executing", updated_at: at("14:30"), href: "/executions/EXE-92" },
  { proposal_id: "PRP-110", execution_id: "EXE-90", operation: "enable", rule_name: "OrderQueueDepth", group: GROUP, application_id: "checkout-api", environment_id: "prod", state: "needs_attention", updated_at: at("14:30"), href: "/executions/EXE-90" },
  { proposal_id: "PRP-106", execution_id: "EXE-95", operation: "enable", rule_name: "CheckoutHigh5xx", group: GROUP, application_id: "checkout-api", environment_id: "prod", state: "failed", updated_at: at("14:31"), href: "/executions/EXE-95" },
];

// Read-only recheck (D11 §5.4): one observation; the page rereads the execution.
mock("POST", "/executions/:id/recheck", ({ params }) => ({ proposal_id: EXECS[params.id]?.proposal ?? "", execution_id: params.id, status: EXECS[params.id]?.state ?? "merged", observed_rule_id: EXECS[params.id]?.observed }));
// Disable (D11 decision 7): only rules in the executor-merged rule file; the others are 422 disable_unavailable.
mock("POST", "/alert-rules/:id/disable", ({ params, body }) => {
  if (!LIFECYCLE_RULES.some((r) => r.id === params.id)) throw new MockError(422, "disable_unavailable", "This rule isn't in a file_pull binding");
  if (!(body as { investigation_id?: string })?.investigation_id) throw new MockError(422, "invalid_request", "investigation_id is required");
  return { proposal_id: "PRP-114", status: "pending_approval" };
});
