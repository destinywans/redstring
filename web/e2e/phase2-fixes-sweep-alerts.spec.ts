import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, patchRead, toasts, zh } from "./support/checks";

/**
 * Checks for the events and alerts part of the WAN-434 category sweep before round 3 (PA-210 to PA-229, events,
 * alerts, rules, incidents, inbox, peeks and the changes page). Every check pins the API with page.route, so it runs
 * on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-sweep-alerts.spec.ts
 */
const origin = INSTANCES.prod.origin;
const at0 = "2026-10-18T09:00:00Z";

/* ---------------- alert rules ---------------- */
const RULE_UNAVAILABLE = ["query_ref", "preview", "condition", "evaluation", "runbook", "route_path", "ai_triage", "annotation"];
const ruleSummary = (id: string, name: string, state: string) => ({ id, folder: "check-group", name, state, lifecycle: { state: "active", proposal_id: null, execution_id: null } });
/** GET /alert-rules/{id} as the server answers for a rule read from its Prometheus file. */
const ruleDetail = (id: string, name: string, state: string, lastError: string | null) => ({
  id,
  folder: "check-group",
  name,
  state,
  environment_id: "prod",
  application_id: "app-check",
  datasource: "prometheus",
  query_ref: null,
  expr: "up{job=\"check\"} == 0",
  for_seconds: 60,
  preview: null,
  condition: null,
  evaluation: null,
  evaluation_interval_seconds: 30,
  evaluation_ms: 2,
  labels: [{ k: "application", v: "app-check" }, { k: "environment", v: "prod" }],
  annotations: {},
  annotation: null,
  runbook: null,
  route_path: null,
  ai_triage: null,
  last_eval: at0,
  last_error: lastError,
  partial: true,
  unavailable: RULE_UNAVAILABLE,
});
const ruleList = (items: unknown[], pending: unknown[] = []) => ({ items, counts: { all: items.length, firing: 0, scheduled: 0 }, empty_reason: null, unavailable: {}, pending });

/** Pins the rule list and each rule's detail. */
async function pinRules(page: Page, rules: { id: string; name: string; state: string; lastError?: string | null }[], pending: unknown[] = []) {
  await page.route(at("/alert-rules"), (r) => json(r, 200, ruleList(rules.map((x) => ruleSummary(x.id, x.name, x.state)), pending)));
  for (const x of rules) await page.route(at(`/alert-rules/${x.id}`), (r) => json(r, 200, ruleDetail(x.id, x.name, x.state, x.lastError ?? null)));
}

test.describe("alert rules say when Prometheus can't evaluate a rule (PA-210)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a failing rule shows 求值出错 with the provider's error, an unevaluated one 健康未知, neither 正常", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await pinRules(page, [
      { id: "prom-check-broken", name: "CheckBroken", state: "error", lastError: "query timed out" },
      { id: "prom-check-fresh", name: "CheckFresh", state: "unknown" },
    ]);
    await page.goto(origin + "/alert-rules?q=CheckBroken");
    const editor = page.locator(".rule-editor");
    await expect(editor.getByRole("heading", { name: "CheckBroken" })).toBeVisible({ timeout: 20_000 });
    await expect(editor.locator(".v3-badge", { hasText: "求值出错" })).toBeVisible();
    await expect(editor).toContainText("query timed out");
    await expect(editor.locator(".v3-badge", { hasText: /^正常$/ })).toHaveCount(0);
    // ?q= narrows the list to the first rule, so open the second one by its own search.
    await page.goto(origin + "/alert-rules?q=CheckFresh");
    await expect(editor.getByRole("heading", { name: "CheckFresh" })).toBeVisible();
    await expect(editor.locator(".v3-badge", { hasText: "健康未知" })).toBeVisible();
    await expect(editor.locator(".v3-badge", { hasText: /^正常$/ })).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });
});

test.describe("an enable or disable whose approval lapsed is not shown as running (PA-217)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  const change = (proposal: string, operation: "enable" | "disable", state: string) => ({
    proposal_id: proposal,
    execution_id: null,
    operation,
    rule_name: `Check${operation}`,
    group: "check-group",
    application_id: "app-check",
    environment_id: "prod",
    state,
    updated_at: at0,
    href: `/review/${proposal}`,
  });

  test("an expired enable and disable say 批准已失效, not 启用中 or 停用中", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await pinRules(page, [{ id: "prom-check-ok", name: "CheckOk", state: "ok" }], [change("p-check-enable", "enable", "expired"), change("p-check-disable", "disable", "expired")]);
    await page.goto(origin + "/alert-rules");
    const pending = page.locator(".rule-item", { hasText: "Checkenable" });
    await expect(pending).toBeVisible({ timeout: 20_000 });
    await expect(pending.locator(".v3-badge")).toHaveText("批准已失效");
    await expect(page.locator(".rule-item", { hasText: "Checkdisable" }).locator(".v3-badge")).toHaveText("批准已失效");
    await expect(page.locator(".side")).not.toContainText("启用中");
    await expect(page.locator(".side")).not.toContainText("停用中");
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- alerts page ---------------- */
const GROUP = "ag-check";
/** One real alert group as GET /alert-groups returns it: claimed by `owner`, one instance with no value or series. */
const alertGroup = (patch: Record<string, unknown> = {}) => ({
  id: GROUP,
  severity: "P2",
  labels: [{ k: "alertname", v: "CheckHighErrorRate" }],
  service: "app-check",
  alert_name: "CheckHighErrorRate",
  count: 1,
  state: "firing",
  since: at0,
  last_seen: at0,
  notify: { channel: null, text: "", status: null },
  badges: [],
  owner: "Check Owner",
  inhibited: null,
  instances: [{ id: "check-instance-1", value: null, state: "firing", duration: "5m", series: null, pending: null }],
  environment_key: "prod",
  unavailable: ["instances[].value", "instances[].series", "inhibited"],
  ...patch,
});
const groupPolicy = {
  group_by: ["alertname"],
  route: "root",
  timing: [],
  timeline: [{ time: at0, channel: "webhook", text: "Check notification", done: null }],
  inhibition: null,
  partial: true,
  unavailable: ["inhibition", "timeline[].done"],
};
/** Pins the alerts page to one claimed group; `state`/`show_inhibited` requests the server refuses answer 503 as it does. */
async function pinAlerts(page: Page, group = alertGroup()) {
  await page.route(at("/alert-groups"), (r) => {
    const q = new URL(r.request().url()).searchParams;
    if (q.get("state") === "silenced") return json(r, 503, { error: { code: "silence_state_unavailable", message: "Check: silence state is not tracked" } });
    if (q.get("show_inhibited") === "false") return json(r, 503, { error: { code: "inhibition_unavailable", message: "Check: inhibition is not tracked" } });
    return json(r, 200, { items: [group], counts: { firing: 1, pending: 0, silenced: null } });
  });
  await page.route(at(`/alert-groups/${GROUP}/policy`), (r) => json(r, 200, groupPolicy));
}
/** Hovers the group owner's name and returns the person card. */
async function personCard(page: Page) {
  await page.locator(".alert-group .peek-target", { hasText: "Check Owner" }).hover({ timeout: 20_000 });
  return page.locator(".v3-peek");
}

test.describe("the person card never says off call when that is unknown (PA-214)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  const peek = at(`/people/${encodeURIComponent("Check Owner")}/peek`);

  for (const [what, status, body] of [
    ["without members.view (404)", 404, { error: { code: "not_found", message: "Person not found" } }],
    ["when the read fails (503)", 503, { error: { code: "person_peek_unavailable", message: "Person preview unavailable" } }],
    ["with on-call null", 200, { id: "u-check", name: "Check Owner", role: null, oncall: null, load: null }],
  ] as const) {
    test(`${what}: on call is unknown, not 非值班`, async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      await pinAlerts(page);
      await page.route(peek, (r) => json(r, status, body));
      await page.goto(origin + "/alerts");
      const card = await personCard(page);
      await expect(card).toContainText("Check Owner");
      await expect(card.locator(".unk", { hasText: "值班 —" })).toBeVisible();
      await expect(card).not.toContainText("非值班");
      await expect(card).not.toContainText("…");
      expect(m.pageErrors).toEqual([]);
    });
  }

  test("while it loads: no 非值班", async ({ page }) => {
    await page.addInitScript(zh);
    await pinAlerts(page);
    let release!: () => void;
    const held = new Promise<void>((done) => (release = done));
    await page.route(peek, async (r) => {
      await held;
      await json(r, 404, { error: { code: "not_found", message: "Person not found" } });
    });
    await page.goto(origin + "/alerts");
    const card = await personCard(page);
    await expect(card).toContainText("Check Owner");
    await expect(card).not.toContainText("非值班");
    release();
    await expect(card.locator(".unk", { hasText: "值班 —" })).toBeVisible();
  });
});

test.describe("the service card says why it can't load (PA-221)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a failed service preview explains instead of an endless skeleton", async ({ page }) => {
    await page.addInitScript(zh);
    await pinAlerts(page);
    await page.route((u) => /^\/api\/v1\/services\/[^/]+\/peek$/.test(u.pathname), (r) => json(r, 503, { error: { code: "service_peek_unavailable", message: "Check" } }));
    await page.goto(origin + "/alerts");
    await page.locator(".alert-group .peek-target").first().hover({ timeout: 20_000 });
    const card = page.locator(".v3-peek");
    await expect(card.locator(".unk-block")).toContainText("暂时不可用", { timeout: 10_000 });
    await expect(card.locator(".skeleton")).toHaveCount(0);
  });
});

test.describe("the service card doesn't call warn alerting (PA-209)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("warn reads as needs attention, as in the service catalog", async ({ page }) => {
    await page.addInitScript(zh);
    await pinAlerts(page);
    // warn also comes from an error budget under half or a 1 h burn of 1 or more, with no alert firing.
    const svc = { id: "app-check", name: "check-svc", tier: null, team: null, env: "prod", status: "warn", kpis: [], owner: null, oncall: null, change: null };
    await page.route((u) => /^\/api\/v1\/services\/[^/]+\/peek$/.test(u.pathname), (r) => json(r, 200, svc));
    await page.goto(origin + "/alerts");
    await page.locator(".alert-group .peek-target").first().hover({ timeout: 20_000 });
    const card = page.locator(".v3-peek");
    await expect(card).toContainText("check-svc", { timeout: 10_000 });
    await expect(card.getByText("需要关注", { exact: true })).toBeVisible();
    await expect(card.getByText("告警", { exact: true })).toHaveCount(0);
  });
});

/* ---------------- incidents ---------------- */
type Grants = Record<string, Record<string, Record<string, string>>>;
/** /me without the given "module.action" grants in every environment (the real answer, patched). */
async function meWithout(page: Page, drop: string[]) {
  await page.goto(origin + "/healthz");
  await patchRead(page, "/me", (real: { permissions?: Grants }) => {
    const permissions: Grants = {};
    for (const [env, modules] of Object.entries(real.permissions ?? {})) {
      permissions[env] = {};
      for (const [module, actions] of Object.entries(modules ?? {})) permissions[env][module] = Object.fromEntries(Object.entries(actions).filter(([action]) => !drop.includes(`${module}.${action}`)));
    }
    return { ...real, permissions };
  });
}
/** The screen-reader reason of the permission gate around the entry with this text. */
const gateReason = (page: Page, text: string) => page.locator(".perm-gate", { hasText: text }).locator(".sr-only");
const NEEDS_EVENTS = "需要 事件与告警·修改 权限";

const INC = "INC-check-sweep";
/** A human-confirmed case as GET /incidents/{id} returns it. */
const incident = (patch: Record<string, unknown> = {}) => ({
  id: INC,
  record_kind: "case",
  application_id: "app-check",
  environment_id: "env-check",
  environment_key: "prod",
  source_id: null,
  severity: "P2",
  title: "检查用事件",
  status: "active",
  status_revision: 1,
  resolved_status_event_id: null,
  work_status: null,
  alert_status: null,
  summary: null,
  created_at: at0,
  alert_started_at: null,
  updated_at: at0,
  resolved_at: null,
  duration: null,
  duration_basis: null,
  assignee_id: null,
  assignee: null,
  commander: null,
  commander_provenance: "unknown",
  stage: null,
  timeline: [],
  timeline_scope: "manual_only",
  impact: { peak: null, budget_used: null, budget_left: null, note: null, series: null },
  impact_provenance: "unknown",
  roles: [],
  links: [],
  investigation_id: "INV-check",
  alert_ids: ["alert-check"],
  participants: [],
  partial: true,
  unavailable: ["source_id", "work_status", "alert_status", "summary", "duration", "duration_basis", "assignee", "stage", "impact.peak", "impact.budget_used", "impact.budget_left", "impact.series", "timeline.system_events", "commander"],
  ...patch,
});
/** Pins the incident list (`items` summaries) and, when given, one incident detail. */
async function pinIncidents(page: Page, items: Record<string, unknown>[], detail?: Record<string, unknown>) {
  await page.route(at("/incidents"), (r) => (r.request().method() === "GET" ? json(r, 200, { items, counts: { active: items.filter((x) => x.status === "active").length } }) : r.fallback()));
  if (detail) {
    await page.route(at(`/incidents/${detail.id}`), (r) => json(r, 200, detail));
    await page.route(at(`/incidents/${detail.id}/changes`), (r) => json(r, 503, { error: { code: "changes_unavailable", message: "Check: not pinned" } }));
  }
}
const summaryOf = (x: Record<string, unknown>) => ({ id: x.id, severity: x.severity, title: x.title, status: x.status, duration: null, commander: null });

test.describe("the incidents page explains an empty list or a record it can't read (PA-219)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("no active incidents: the main area says so instead of a skeleton", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await pinIncidents(page, []);
    await page.goto(origin + "/incidents");
    await expect(page.locator(".main").getByText("没有进行中的事件")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".main .skeleton")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("an incident that doesn't exist says not found", async ({ page }) => {
    await page.addInitScript(zh);
    await pinIncidents(page, []);
    await page.route(at("/incidents/INC-check-missing"), (r) => json(r, 404, { error: { code: "not_found", message: "Incident not found" } }));
    await page.goto(origin + "/incidents/INC-check-missing");
    await expect(page.getByText("找不到这条记录", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".main .skeleton")).toHaveCount(0);
  });

  test("a failed incident read says the source is unavailable", async ({ page }) => {
    await page.addInitScript(zh);
    await pinIncidents(page, [summaryOf(incident())]);
    await page.route(at(`/incidents/${INC}`), (r) => json(r, 503, { error: { code: "events_unavailable", message: "Check" } }));
    await page.goto(origin + `/incidents/${INC}`);
    await expect(page.locator(".main").getByText("暂时不可用", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".main .skeleton")).toHaveCount(0);
  });

  test("a failed list read says so", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/incidents"), (r) => json(r, 503, { error: { code: "events_unavailable", message: "Check" } }));
    await page.goto(origin + "/incidents");
    await expect(page.locator(".main").getByText("暂时不可用", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".main .skeleton")).toHaveCount(0);
  });
});

/* ---------------- inbox ---------------- */
const ALERT = "alert-check-sweep";
const inboxItem = { id: "inbox-check-1", kind: "alert", tone: "danger", title: "CheckHighErrorRate", sub: "app-check", at: new Date().toISOString(), unread: true, href: `/inbox?item=inbox-check-1`, alert_id: ALERT };
/** GET /alerts/{id} as the server answers before any occurrence binding exists. */
const alertDetail = {
  id: ALERT,
  revision: "1",
  severity: "P2",
  title: "CheckHighErrorRate",
  state: "firing",
  source: null,
  source_id: "src-check",
  env: "prod",
  since: at0,
  rule: null,
  triage: null,
  series: null,
  labels: null,
  summary: "Check summary",
  runbook: null,
  notifications: [],
  grouping: { group: null, inhibited: null, incident: { id: ALERT }, change: null },
  group_id: null,
  unavailable: ["source", "rule", "triage", "series", "labels", "runbook", "grouping.group", "grouping.inhibited", "grouping.change"],
};
async function pinInbox(page: Page, detail: (r: import("@playwright/test").Route) => Promise<void> = (r) => json(r, 200, alertDetail)) {
  await page.route(at("/inbox"), (r) => json(r, 200, { items: [inboxItem], counts: { all: 1, mine: 0, mentions: 0 } }));
  await page.route(at(`/alerts/${ALERT}`), detail);
  await page.route(at(`/incidents/${ALERT}`), (r) => json(r, 404, { error: { code: "not_found", message: "Check" } }));
}

test.describe("the inbox only does what the server does (PA-220, PA-221)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("real mode has no mark-all-read: the server has no read state", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await pinInbox(page);
    await page.goto(origin + "/inbox");
    await expect(page.locator(".inbox-item", { hasText: "CheckHighErrorRate" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: "全部标为已读" })).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  for (const [what, status, code, text] of [
    ["an alert from another environment (404)", 404, "not_found", "找不到这条记录"],
    ["a failed alert read (503)", 503, "events_unavailable", "暂时不可用"],
  ] as const) {
    test(`${what} explains instead of an endless skeleton`, async ({ page }) => {
      await page.addInitScript(zh);
      await pinInbox(page, (r) => json(r, status, { error: { code, message: "Check" } }));
      await page.goto(origin + "/inbox?item=inbox-check-1");
      await expect(page.locator(".main").getByText(text, { exact: true })).toBeVisible({ timeout: 20_000 });
      await expect(page.locator(".main .skeleton")).toHaveCount(0);
    });
  }
});

test.describe("inbox: creating an incident needs events.change (PA-228)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("without the grant, More › Create incident names the permission and doesn't open the drawer", async ({ page }) => {
    await page.addInitScript(zh);
    await meWithout(page, ["events.change"]);
    await pinInbox(page);
    await page.goto(origin + "/inbox?item=inbox-check-1");
    await expect(page.getByRole("heading", { name: "CheckHighErrorRate" })).toBeVisible({ timeout: 20_000 });
    await page.getByRole("button", { name: "更多操作" }).click();
    const item = page.getByRole("menuitem", { name: /创建事件/ });
    await expect(item).toContainText(NEEDS_EVENTS);
    await item.click();
    await expect(page).toHaveURL(/\/inbox/);
    expect((await toasts(page)).join("\n")).toContain(NEEDS_EVENTS);
  });
});

test.describe("alerts and rules say why a list or rule can't be read (PA-221)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the Silenced tab says silence state isn't tracked instead of a blank list", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await pinAlerts(page);
    await page.goto(origin + "/alerts");
    await expect(page.locator(".alert-group").first()).toBeVisible({ timeout: 20_000 });
    await page.locator(".v3-pills button", { hasText: "已静默" }).click();
    await expect(page.locator(".unk-block", { hasText: "还不能按静默筛选" })).toBeVisible();
    await expect(page.locator(".alert-group")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("hiding inhibited alerts says inhibition isn't tracked instead of a blank list", async ({ page }) => {
    await page.addInitScript(zh);
    await pinAlerts(page);
    await page.goto(origin + "/alerts");
    await expect(page.locator(".alert-group").first()).toBeVisible({ timeout: 20_000 });
    await page.getByRole("switch", { name: "显示被抑制的告警" }).click();
    await expect(page.locator(".unk-block", { hasText: "还不能隐藏被抑制的告警" })).toBeVisible();
  });

  test("an empty tab says there is nothing", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/alert-groups"), (r) => json(r, 200, { items: [], counts: { firing: 0, pending: 0, silenced: null } }));
    await page.goto(origin + "/alerts");
    await expect(page.locator(".v3-content").getByText("没有数据", { exact: true })).toBeVisible({ timeout: 20_000 });
  });

  test("a rule whose detail can't be read says so instead of an endless skeleton", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/alert-rules"), (r) => json(r, 200, ruleList([ruleSummary("prom-check-gone", "CheckGone", "ok")])));
    await page.route(at("/alert-rules/prom-check-gone"), (r) => json(r, 503, { error: { code: "rule_source_partial", message: "Check" } }));
    await page.goto(origin + "/alert-rules");
    await expect(page.locator(".main").getByText("暂时不可用", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".main .skeleton")).toHaveCount(0);
  });
});

/** Every unknown mark in `scope` explains itself as untracked, never as a failed source or data still to come. */
async function expectUntracked(scope: import("@playwright/test").Locator, count: number) {
  const marks = scope.locator(".unk");
  await expect(marks).toHaveCount(count);
  for (const label of await marks.evaluateAll((els) => els.map((e) => e.getAttribute("aria-label") ?? ""))) {
    expect(label).toContain("未记录");
    expect(label).not.toContain("连接失败");
    expect(label).not.toContain("有新数据后会自动生成");
  }
}

test.describe("fields the server never fills say they aren't tracked (PA-223)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("alerts: current value, last 30 min, notification delivery and inhibition", async ({ page }) => {
    await page.addInitScript(zh);
    await pinAlerts(page);
    await page.goto(origin + "/alerts");
    await expect(page.locator(".ag-body")).toBeVisible({ timeout: 20_000 });
    await expectUntracked(page.locator(".ag-body"), 2);
    await expect(page.locator(".aside").getByText("Check notification")).toBeVisible();
    await expectUntracked(page.locator(".aside"), 2);
  });

  test("incident impact peak and budget", async ({ page }) => {
    await page.addInitScript(zh);
    await pinIncidents(page, [summaryOf(incident())], incident());
    await page.goto(origin + `/incidents/${INC}`);
    const impact = page.locator(".v3-panel", { has: page.locator("h2", { hasText: /^影响$/ }) });
    await expect(impact).toBeVisible({ timeout: 20_000 });
    await expectUntracked(impact, 2);
  });

  test("inbox alert: source, rule, value chart, labels and grouping", async ({ page }) => {
    await page.addInitScript(zh);
    await pinInbox(page);
    await page.goto(origin + "/inbox?item=inbox-check-1");
    const detail = page.locator(".alert-detail");
    await expect(detail.getByRole("heading", { name: "CheckHighErrorRate" })).toBeVisible({ timeout: 20_000 });
    // source, rule, value chart, labels, group, inhibits, change
    await expectUntracked(detail, 7);
  });
});

test.describe("real controls don't pretend to do what the server doesn't (PA-224)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("alerts: no grouping picker that never reaches the request", async ({ page }) => {
    await page.addInitScript(zh);
    await pinAlerts(page);
    await page.goto(origin + "/alerts");
    await expect(page.locator(".alert-group").first()).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".demo-chip", { hasText: "分组" })).toHaveCount(0);
  });

  test("new rule: no YAML tab that edits nothing, and import starts empty", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/alert-rules/new?tab=import");
    const box = page.locator(".v3-content textarea");
    await expect(box).toBeVisible({ timeout: 20_000 });
    await expect(box).toHaveValue("");
    await expect(page.getByRole("tab", { name: "YAML" })).toHaveCount(0);
    await expect(page.locator("#root")).not.toContainText("双向同步");
    await expect(page.locator("#root")).not.toContainText("从已接入的 Prometheus 拉取");
  });

  test("new rule import: the one request that writes is the import; the result only goes back", async ({ page }) => {
    await page.addInitScript(zh);
    let posts = 0;
    await page.route(at("/alert-rules/import"), (r) => {
      posts++;
      return json(r, 201, { status: "pending_review", items: [{ name: "CheckRule", status: "new", note: "新规则" }], candidates: [] });
    });
    await page.goto(origin + "/alert-rules/new?tab=import");
    await page.locator(".v3-content textarea").fill("groups: []", { timeout: 20_000 });
    await page.getByRole("button", { name: "导入为草稿" }).click();
    await expect(page.getByText("CheckRule")).toBeVisible();
    expect(posts).toBe(1);
    expect((await toasts(page)).join("\n")).toContain("已导入为草稿");
    const back = page.getByRole("button", { name: "返回告警规则" });
    await expect(back).toBeVisible();
    await expect(page.getByRole("button", { name: "导入为草稿" })).toHaveCount(1);
    await back.click();
    await expect(page).toHaveURL(/\/alert-rules$/);
    expect(posts).toBe(1);
  });
});

test.describe("new rule preview legend follows the real query (PA-229)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the legend names the query result and the threshold without a 5xx ratio or a % unit", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/alert-rules/preview"), (r) => json(r, 200, { valid: true, series_count: 1, source: "Prometheus · prod", values: [0, 1, 2, 3], current: 3, fires: null }));
    const service = Buffer.from("app-check\u0000prod").toString("base64url");
    await page.goto(origin + `/alert-rules/new?service=${service}`);
    await page.locator("textarea[placeholder^='sum(rate']").fill("up == 0", { timeout: 20_000 });
    const legend = page.locator(".f-right .v3-panel").first();
    await expect(legend).toContainText("查询结果", { timeout: 10_000 });
    await expect(legend).toContainText("阈值 1");
    await expect(legend).not.toContainText("5xx");
    await expect(legend).not.toContainText("阈值 1%");
  });
});

test.describe("the new incident drawer says what the investigation canvas really is (PA-225)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a blank investigation linked to the incident, nothing brought in automatically", async ({ page }) => {
    await page.addInitScript(zh);
    await pinIncidents(page, []);
    await page.goto(origin + "/incidents?new=incident");
    const drawer = page.locator(".v3-drawer");
    await expect(drawer.getByText("同时创建调查画布")).toBeVisible({ timeout: 20_000 });
    await expect(drawer).toContainText("空白调查");
    await expect(drawer).not.toContainText("自动带入告警、最近变更和相关指标");
    await expect(drawer).not.toContainText("「故障调查」模板");
  });
});

test.describe("the incident stepper ticks only stages the status proves (PA-226)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  const steps = (page: Page) => page.locator(".v3-stepper");

  for (const [status, done, on, absent] of [
    ["active", [], "发现", ["确认"]],
    ["mitigated", ["发现"], "缓解", ["确认"]],
    // A case can go straight from active to resolved, so mitigation isn't claimed.
    ["resolved", ["发现"], "解决", ["确认", "缓解"]],
  ] as const) {
    test(`${status}: done ${done.join(", ") || "none"}, current ${on}, no postmortem tick`, async ({ page }) => {
      await page.addInitScript(zh);
      const detail = incident({ status, status_revision: 3 });
      await pinIncidents(page, [summaryOf(detail)], detail);
      await page.goto(origin + `/incidents/${INC}`);
      await expect(steps(page)).toBeVisible({ timeout: 20_000 });
      await expect(steps(page).locator(".step.done")).toHaveText([...done]);
      // The current step shows its number before the label.
      await expect(steps(page).locator(".step.on")).toHaveText(new RegExp(`^\\d${on}$`));
      for (const a of absent) await expect(steps(page)).not.toContainText(a);
      await expect(steps(page).locator(".step.done, .step.on").filter({ hasText: "复盘" })).toHaveCount(0);
    });
  }
});

test.describe("incident writes need events.change (PA-228)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("without the grant, Update status and the timeline note are disabled and name the permission", async ({ page }) => {
    await page.addInitScript(zh);
    await meWithout(page, ["events.change"]);
    const detail = incident();
    await pinIncidents(page, [summaryOf(detail)], detail);
    let posted = false;
    await page.route((u) => u.pathname.startsWith(`/api/v1/incidents/${INC}/`) && !u.pathname.endsWith("/changes"), (r) => {
      if (r.request().method() !== "GET") posted = true;
      return r.fallback();
    });
    await page.goto(origin + `/incidents/${INC}`);
    await expect(page.locator(".v3-stepper")).toBeVisible({ timeout: 20_000 });
    await expect(gateReason(page, "更新状态")).toHaveText(NEEDS_EVENTS);
    await expect(page.getByRole("button", { name: "更新状态" })).toBeDisabled();
    const note = page.getByRole("textbox", { name: "补充时间线" });
    await expect(note).toBeDisabled();
    await expect(page.locator(".perm-gate", { has: note }).locator(".sr-only")).toHaveText(NEEDS_EVENTS);
    await expect(page.getByRole("button", { name: "添加" })).toBeDisabled();
    expect(posted).toBe(false);
  });

  test("the new incident drawer's create button names the permission too", async ({ page }) => {
    await page.addInitScript(zh);
    await meWithout(page, ["events.change"]);
    await pinIncidents(page, []);
    await page.goto(origin + "/incidents?new=incident");
    await expect(page.locator(".v3-drawer")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".v3-drawer .perm-gate", { hasText: "创建事件" }).locator(".sr-only")).toHaveText(NEEDS_EVENTS);
  });
});

/* ---------------- changes ---------------- */
const SERVICE = Buffer.from("app-check\u0000prod").toString("base64url");
const PIPELINE_STATES = { waiting_for_resource: "等待资源", preparing: "准备中", waiting_for_callback: "等待回调", canceling: "取消中", scheduled: "已排期" } as const;
const pipeline = (n: number, status: string) => ({ id: `gitlab:pipeline:${n}`, pipeline_id: n, status, ref: "main", sha: `${n}`.padEnd(40, "a"), short_sha: `${n}`.padEnd(8, "a"), source: "push", created_at: at0, updated_at: at0, web_url: null, merge_request_iid: null, deployment_ids: [] });
const changesView = {
  service_id: SERVICE,
  environment_id: "env-check",
  source: "gitlab",
  window: { start_at: "2026-10-17T09:00:00Z", end_at: at0 },
  project: { connector_id: "gitlab-check", project_id: "42", path: "check/app", web_url: "https://gitlab.example.invalid/check/app", deploy_environment: "production", refs: ["main"] },
  kpis: [],
  series_start_at: "2026-10-17T09:00:00Z",
  series_step_seconds: 3600,
  deployments: [],
  pipelines: Object.keys(PIPELINE_STATES).map((s, i) => pipeline(101 + i, s)),
  merge_requests: [],
  alerts: null,
  kubernetes_revisions: null,
  coverage: { gitlab: "complete", alerts: "unknown", kubernetes: "not_configured" },
  captured_at: at0,
  unavailable: {},
};
const job = (id: number, name: string, status: string, manual = false) => ({ id, name, status, manual, started_at: null, finished_at: null, duration_seconds: null, web_url: null });
const pipelineDetail = {
  id: "gitlab:pipeline:101",
  pipeline_id: 101,
  status: "canceled",
  ref: "main",
  sha: "101".padEnd(40, "a"),
  source: "push",
  user: { username: "check" },
  created_at: at0,
  started_at: null,
  finished_at: null,
  duration_seconds: null,
  web_url: null,
  stages: [{ name: "test", status: "canceled", jobs: [job(1, "check-canceled", "canceled"), job(2, "check-skipped", "skipped"), job(3, "check-manual", "manual", true), job(4, "check-pending", "pending")] }],
  commit: null,
  merge_request_iid: null,
  adjacent_alerts: null,
  unavailable: {},
};
async function openPipeline(page: Page) {
  await page.route(at("/changes"), (r) => json(r, 200, changesView));
  await page.route(at("/changes/pipelines/101"), (r) => json(r, 200, pipelineDetail));
  await page.goto(origin + `/changes?service_id=${SERVICE}&range=24h&focus=gitlab:pipeline:101`);
  await page.locator(".chg-table tr.clickable", { hasText: "#101" }).click({ timeout: 20_000 });
  await expect(page.locator(".v3-drawer .dag")).toBeVisible();
}

test.describe("GitLab states the server allows are said in words (PA-166)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("pipeline states have Chinese labels, and canceled, skipped and manual jobs say what they are", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await openPipeline(page);
    const table = page.locator(".chg-table");
    for (const [raw, label] of Object.entries(PIPELINE_STATES)) {
      await expect(table.locator(".v3-badge", { hasText: label })).toHaveCount(1);
      await expect(table).not.toContainText(raw);
    }
    const tile = (name: string) => page.locator(".v3-drawer .dag-job", { hasText: name });
    await expect(tile("check-canceled")).toHaveAttribute("title", /已取消/);
    await expect(tile("check-skipped")).toHaveAttribute("title", /已跳过/);
    await expect(tile("check-manual")).toHaveAttribute("title", /待手动/);
    await expect(tile("check-pending")).toHaveAttribute("title", /等待中/);
    expect(m.pageErrors).toEqual([]);
  });
});

test.describe("pipeline drawer actions name the permission they need (PA-228)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("without conversation.change, bringing it into an investigation is disabled", async ({ page }) => {
    await page.addInitScript(zh);
    await meWithout(page, ["conversation.change"]);
    await openPipeline(page);
    await expect(gateReason(page, "带入调查")).toHaveText("需要 对话·修改 权限");
    await expect(page.locator(".v3-drawer .perm-gate", { hasText: "提议回滚" })).toHaveCount(0);
  });

  test("without proposals.change, proposing a rollback is disabled", async ({ page }) => {
    await page.addInitScript(zh);
    await meWithout(page, ["proposals.change"]);
    await openPipeline(page);
    await expect(gateReason(page, "提议回滚")).toHaveText("需要 写操作提案·修改 权限");
    await expect(page.getByRole("button", { name: "提议回滚" })).toBeDisabled();
  });
});
