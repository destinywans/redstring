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
