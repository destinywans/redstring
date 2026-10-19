import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, read, zh } from "./support/checks";

/**
 * Checks for the incident fixes between rounds 2 and 3 of WAN-434 phase 2: PA-189 (roles and links panels), PA-190
 * (linked alert times in the new incident drawer) and the incident part of PA-166 (server English in the Chinese
 * UI). Page checks pin the API with page.route; the real-data checks only read. Nothing writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-incidents.spec.ts
 */
const origin = INSTANCES.prod.origin;
const ID = "INC-check-incident";
const at0 = "2026-10-18T00:29:59.286Z";
const UNAVAILABLE = ["source_id", "work_status", "alert_status", "summary", "alert_started_at", "duration", "duration_basis", "assignee", "stage", "impact.peak", "impact.budget_used", "impact.budget_left", "impact.series", "timeline.system_events", "links.alert_group", "links.proposal", "links.change", "resolved_at"];
/** A human-confirmed case as GET /incidents/{id} returns it. */
const incident = (patch: Record<string, unknown>) => ({
  id: ID,
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
  impact: { peak: null, budget_used: null, budget_left: null, note: "部分用户下单失败", series: null },
  impact_provenance: "human_confirmed",
  roles: [],
  links: [
    { kind: "investigation", label: "检查用调查", href: "/canvas/INV-check" },
    { kind: "alert", label: "LabHigh5xxRate", href: "/incidents/alert-check" },
  ],
  investigation_id: "INV-check",
  alert_ids: ["alert-check"],
  participants: [],
  partial: true,
  unavailable: [...UNAVAILABLE, "commander"],
  ...patch,
});

/** Opens the incident page on one pinned incident. */
async function openIncident(page: Page, detail: Record<string, unknown>) {
  const summary = { id: ID, severity: detail.severity, title: detail.title, status: detail.status, duration: null, commander: detail.commander };
  await page.route(at("/incidents"), (r) => (r.request().method() === "GET" ? json(r, 200, { items: [summary], counts: { active: 1 } }) : r.fallback()));
  await page.route(at(`/incidents/${ID}`), (r) => json(r, 200, detail));
  await page.route(at(`/incidents/${ID}/changes`), (r) => json(r, 503, { error: { code: "changes_unavailable", message: "Check: not pinned" } }));
  await page.goto(origin + `/incidents/${ID}`);
}
const panel = (page: Page, title: string) => page.locator(".v3-panel", { has: page.locator("h2", { hasText: new RegExp(`^${title}$`) }) });
/** GET an API path in the page in one request language; { status, body }. Call after page.goto(origin + "/healthz"). */
async function readIn(page: Page, path: string, language: string): Promise<{ status: number; body: any }> {
  return page.evaluate(async ([p, l]) => {
    const r = await fetch("/api/v1" + p, { headers: { "X-AIOps-Environment": "prod", "Accept-Language": l } });
    return { status: r.status, body: await r.json().catch(() => null) };
  }, [path, language] as const);
}
/** The KV row of a panel whose key is exactly k. */
const row = (page: Page, title: string, k: string) => panel(page, title).locator(".v3-kv").filter({ has: page.locator(".k", { hasText: new RegExp(`^${k}$`) }) });

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

test.describe("incident roles and links (PA-189)", () => {
  test("links show the investigation and alerts; what has no source is marked unknown, not left blank", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await openIncident(page, incident({}));
    await expect(panel(page, "关联")).toBeVisible({ timeout: 20_000 });
    await expect(row(page, "关联", "调查").getByRole("link", { name: /检查用调查/ })).toHaveAttribute("href", "/canvas/INV-check");
    await expect(row(page, "关联", "告警").getByRole("link", { name: /LabHigh5xxRate/ })).toHaveAttribute("href", "/incidents/alert-check");
    for (const k of ["告警组", "提案", "变更"]) await expect(row(page, "关联", k).locator(".unk")).toBeVisible();
    // Roles: no commander was entered and a case has no assignee; both are unknown, not an empty panel.
    const roles = panel(page, "角色");
    await expect(roles).toContainText("指挥");
    await expect(roles).toContainText("负责人");
    await expect(roles.locator(".unk")).toHaveCount(2);
    // The timeline says it holds manual notes only.
    await expect(page.getByText("时间线只含人工记录；状态变化等系统事件还没有接入")).toBeVisible();
    expect(m.pageErrors).toEqual([]);
  });

  test("a case's entered commander shows under roles", async ({ page }) => {
    await page.addInitScript(zh);
    await openIncident(page, incident({ commander: "值班甲", commander_provenance: "human_confirmed", unavailable: UNAVAILABLE }));
    const roles = panel(page, "角色");
    await expect(roles).toContainText("值班甲", { timeout: 20_000 });
    await expect(roles.locator(".v3-row", { hasText: "值班甲" })).toContainText("指挥");
    // Only the assignee is unknown now.
    await expect(roles.locator(".unk")).toHaveCount(1);
  });

  test("an alert's assignee role is named in the UI language, not as its code", async ({ page }) => {
    await page.addInitScript(en);
    const alert = incident({
      record_kind: undefined,
      status_revision: undefined,
      assignee_id: "u-check",
      assignee: { id: "u-check", name: "Check Owner" },
      roles: [{ role: "assignee", name: "Check Owner" }],
      links: [{ kind: "investigation", label: "Check investigation", href: "/canvas/INV-check" }],
      alert_ids: undefined,
      unavailable: ["duration", "duration_basis", "commander", "stage", "impact.peak", "impact.budget_used", "impact.budget_left", "impact.note", "impact.series", "timeline.system_events", "links.alert_group", "links.proposal", "links.change"],
    });
    await openIncident(page, alert);
    const roles = panel(page, "Roles");
    await expect(roles.locator(".v3-row", { hasText: "Check Owner" })).toContainText("Assignee", { timeout: 20_000 });
    await expect(roles).not.toContainText("assignee");
    await expect(roles.locator(".v3-row", { hasText: "Commander" }).locator(".unk")).toBeVisible();
    await expect(row(page, "Links", "Investigation").getByRole("link", { name: /Check investigation/ })).toBeVisible();
    await expect(page.getByText("Only manual notes are shown; status changes and other system events aren't in the timeline yet")).toBeVisible();
  });

  test("real data: incidents with an investigation link it, and cases link their alerts", async ({ page }) => {
    await page.goto(origin + "/healthz");
    // The round 2 record first (INC-48293cc3…, investigation INV-416acceb…), then whatever the list has.
    const listed = ((await read(page, "/incidents?status=all")).body?.items ?? []).map((i: { id: string }) => i.id);
    const ids = ["INC-48293cc3172c218800517940484d46ae", ...listed].slice(0, 40);
    let checked = 0;
    for (const id of ids) {
      const { status, body: d } = await read(page, `/incidents/${encodeURIComponent(id)}`);
      if (status !== 200 || !d?.investigation_id) continue;
      const links = (d.links as { kind: string; label: string; href: string }[]).map((l) => `${l.kind} ${l.href}`);
      expect(links, id).toContain(`investigation /canvas/${d.investigation_id}`);
      for (const a of d.alert_ids ?? []) expect(links, id).toContain(`alert /incidents/${a}`);
      expect((d.links as { label: string }[]).every((l) => l.label.trim() !== ""), id).toBe(true);
      expect(d.unavailable, id).not.toContain("links.investigation");
      checked++;
    }
    test.skip(checked === 0, "no incident with an investigation");
  });
});

/** The new incident drawer pre-filled from one pinned draft. */
async function openDraft(page: Page) {
  await page.route(at("/incidents/draft"), (r) =>
    json(r, 200, {
      title: "检查用事件", severity: "P2", severity_hint: "所选告警中的最高级别", env: "prod", services: [{ id: "app-check", tier: null }], impact: null,
      alerts: [{ id: "alert-check", severity: "P2", text: "LabHigh5xxRate", at: at0 }],
      commander: null, participants: null, war_room: null, source: "persisted_alerts", partial: true,
      unavailable: ["impact", "service.tier", "commander", "participants", "war_room", "ai_analysis"],
    }),
  );
  await page.goto(origin + "/incidents?new=incident&alerts=alert-check");
}

test.describe("new incident drawer times (PA-190)", () => {
  test("a linked alert's time is formatted in the app language, not shown as ISO", async ({ page }) => {
    await page.addInitScript(zh);
    await openDraft(page);
    const alertRow = page.getByRole("dialog", { name: "新建事件" }).locator(".f-alert-row", { hasText: "LabHigh5xxRate" });
    await expect(alertRow).toBeVisible({ timeout: 20_000 });
    await expect(alertRow).not.toContainText("2026-10-18T");
    await expect(alertRow).toContainText(/\d{1,2}:\d{2}/);
  });
});

test.describe("server English in the incident flow (PA-166)", () => {
  test("a refused create is said in Chinese; an unknown code keeps the server's message", async ({ page }) => {
    await page.addInitScript(zh);
    let answer = { status: 409, code: "incident_conflict", message: "Alert changed or is already linked to an incident" };
    await page.route(at("/incidents"), (r) => (r.request().method() === "POST" ? json(r, answer.status, { error: { code: answer.code, message: answer.message } }) : r.fallback()));
    await openDraft(page);
    const drawer = page.getByRole("dialog", { name: "新建事件" });
    await expect(drawer.locator(".f-alert-row", { hasText: "LabHigh5xxRate" })).toBeVisible({ timeout: 20_000 });
    await drawer.getByRole("button", { name: "创建事件" }).click();
    await expect(page.locator(".v3-toast", { hasText: "所选告警刚刚变化，或已经关联了事件" })).toBeVisible();
    await expect(page.locator(".v3-toast", { hasText: "Alert changed or is already linked" })).toHaveCount(0);
    answer = { status: 503, code: "check_unknown_code", message: "Check: unknown refusal" };
    await drawer.getByRole("button", { name: "创建事件" }).click();
    await expect(page.locator(".v3-toast", { hasText: "Check: unknown refusal" })).toBeVisible();
  });

  // The drafted severity hint needs an alert that can still start an incident (firing, not yet in one); the
  // acceptance data has one only during a drill, and regress.py fails a skip: incident_draft_text_test.go covers it.

  test("real data: a root branch's fixed title follows the request language", async ({ page }) => {
    await page.goto(origin + "/healthz");
    const ids = ((await read(page, "/canvases?kind=investigation")).body?.items ?? []).map((i: { id: string }) => i.id).slice(0, 30);
    let fixed = 0;
    for (const id of ids) {
      const path = `/investigations/${encodeURIComponent(id)}/canvas`;
      const [zhGraph, enGraph] = [await readIn(page, path, "zh-CN"), await readIn(page, path, "en-US")];
      if (zhGraph.status !== 200 || enGraph.status !== 200) continue;
      const root = (g: { nodes: { kind: string; title: string }[] }) => g.nodes.find((n) => n.kind === "root")?.title;
      const zhRoot = root(zhGraph.body);
      const enRoot = root(enGraph.body);
      // Alert-started investigations store "Main investigation", canvas-started ones "主调查".
      if (zhRoot !== "主调查" && zhRoot !== "Main investigation" && enRoot !== "主调查" && enRoot !== "Main investigation") continue;
      expect([zhRoot, enRoot], id).toEqual(["主调查", "Main investigation"]);
      fixed++;
    }
    test.skip(fixed === 0, "no investigation whose root keeps its fixed title");
  });
});

test.describe("starting an investigation needs events.change (PA-252)", () => {
  test.use({ storageState: statePath("prod", "viewer") });

  test("a read-only member sees Investigate gated with the grant it needs and sends nothing", async ({ page }) => {
    await page.addInitScript(zh);
    const opened: string[] = [];
    await page.route(at(`/incidents/${ID}/investigation`), (r) => {
      opened.push(r.request().method());
      return json(r, 403, { error: { code: "forbidden", message: "Check: must not be sent" } });
    });
    await openIncident(page, incident({ investigation_id: null, links: [{ kind: "alert", label: "LabHigh5xxRate", href: "/incidents/alert-check" }] }));
    const start = page.getByRole("button", { name: "发起调查", exact: true });
    await expect(start).toBeVisible({ timeout: 20_000 });
    await expect(start).toBeDisabled();
    await expect(page.locator(".perm-gate", { has: start })).toContainText("需要 事件与告警·修改 权限");
    await start.click({ force: true });
    await page.waitForTimeout(500);
    expect(opened).toEqual([]);
  });

  test("a linked investigation still opens for a read-only member, without the POST", async ({ page }) => {
    await page.addInitScript(zh);
    const opened: string[] = [];
    await page.route(at(`/incidents/${ID}/investigation`), (r) => {
      opened.push(r.request().method());
      return r.fallback();
    });
    await openIncident(page, incident({}));
    const open = page.getByRole("button", { name: "打开调查画布", exact: true });
    await expect(open).toBeEnabled({ timeout: 20_000 });
    await open.click();
    await page.waitForURL(/\/canvas\/INV-check$/);
    expect(opened).toEqual([]);
  });
});
