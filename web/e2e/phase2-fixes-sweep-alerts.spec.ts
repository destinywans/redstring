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
    await page.locator(".rule-item", { hasText: "CheckFresh" }).click();
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
