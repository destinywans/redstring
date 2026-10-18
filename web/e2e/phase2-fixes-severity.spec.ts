import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, patchRead, zh } from "./support/checks";

/**
 * Checks for alert severity before round 3 (PA-239, PA-240): a severity the server can't map onto P1–P4, or an alert
 * that carried none, arrives as `severity: null` with `severity_reason`, and every events page says 级别未知 instead
 * of failing, showing an empty badge or a guessed level. Every check pins the API with page.route, so it runs on any
 * data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-severity.spec.ts
 */
const origin = INSTANCES.prod.origin;
const at0 = "2026-10-18T09:00:00Z";
const UNKNOWN = "级别未知";
/** A P1–P4 badge; an unknown severity must not show one. */
const levelBadge = /^P[1-4]$/;

/* ---------------- alerts page ---------------- */
const GROUP = "ag-check-odd";
const alertGroup = (reason: string) => ({
  id: GROUP,
  severity: null,
  severity_reason: reason,
  labels: [{ k: "alertname", v: "CheckOddSeverity" }],
  service: "app-check",
  alert_name: "CheckOddSeverity",
  count: 1,
  state: "firing",
  since: at0,
  last_seen: at0,
  notify: { channel: null, text: "", status: null },
  badges: [],
  owner: null,
  inhibited: null,
  instances: [{ id: "check-odd-1", value: null, state: "firing", duration: "5m", series: null, pending: null }],
  environment_key: "prod",
  unavailable: ["severity", "single_owner", "inhibition"],
});
const groupPolicy = { group_by: ["alertname"], route: "root", timing: [], timeline: [], inhibition: null, partial: true, unavailable: ["inhibition"] };

test.describe("alerts: a group whose severity is unknown says 级别未知 (PA-239, PA-240)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const reason of ["unclassified", "not_filled"]) {
    test(`severity null (${reason}): the group is listed with 级别未知, no P badge`, async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      await page.route(at("/alert-groups"), (r) => json(r, 200, { items: [alertGroup(reason)], counts: { firing: 1, pending: 0, silenced: null } }));
      await page.route(at(`/alert-groups/${GROUP}/policy`), (r) => json(r, 200, groupPolicy));
      await page.goto(origin + "/alerts");
      const card = page.locator(".alert-group", { hasText: "CheckOddSeverity" });
      await expect(card).toBeVisible({ timeout: 20_000 });
      await expect(card.getByText(UNKNOWN, { exact: true })).toBeVisible();
      await expect(card.locator(".v3-badge", { hasText: levelBadge })).toHaveCount(0);
      expect(m.pageErrors).toEqual([]);
    });
  }
});

/* ---------------- incidents page ---------------- */
const ODD = "alert-check-odd";
const oddSummary = { id: ODD, severity: null, severity_reason: "unclassified", title: "CheckOddSeverity", status: "active", duration: "5m", commander: "" };
/** GET /incidents/{id} for an alert record (not a case) whose severity is unknown. */
const oddDetail = {
  participants: [],
  id: ODD,
  application_id: "app-check",
  environment_id: "env-check",
  environment_key: "prod",
  source_id: "src-check",
  severity: null,
  severity_reason: "unclassified",
  title: "CheckOddSeverity",
  status: "active",
  work_status: "open",
  alert_status: "firing",
  summary: "",
  alert_started_at: at0,
  updated_at: at0,
  resolved_at: null,
  duration: null,
  duration_basis: null,
  assignee_id: "",
  assignee: null,
  commander: null,
  stage: null,
  timeline: [],
  timeline_scope: "manual_only",
  impact: { peak: null, budget_used: null, budget_left: null, note: null, series: null },
  roles: [],
  links: [],
  investigation_id: "",
  partial: true,
  unavailable: ["severity", "duration", "duration_basis", "commander", "stage", "impact.peak", "impact.budget_used", "impact.budget_left", "impact.note", "impact.series", "timeline.system_events", "links.alert_group", "links.proposal", "links.change"],
};

test.describe("incidents: an alert record whose severity is unknown says 级别未知, not P4 (PA-239)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("list row and detail header show 级别未知 and no P badge", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/incidents"), (r) => (r.request().method() === "GET" ? json(r, 200, { items: [oddSummary], counts: { all: 1, active: 1, resolved: 0 } }) : r.fallback()));
    await page.route(at(`/incidents/${ODD}`), (r) => json(r, 200, oddDetail));
    await page.route(at(`/incidents/${ODD}/changes`), (r) => json(r, 503, { error: { code: "changes_unavailable", message: "Check: not pinned" } }));
    await page.goto(origin + `/incidents/${ODD}`);
    const row = page.locator(".inc-item", { hasText: ODD });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row.getByText(UNKNOWN, { exact: true })).toBeVisible();
    await expect(row.locator(".v3-badge", { hasText: levelBadge })).toHaveCount(0);
    const header = page.locator(".v3-content").first();
    await expect(header.getByRole("heading", { name: "CheckOddSeverity" })).toBeVisible();
    await expect(header.getByText(UNKNOWN, { exact: true }).first()).toBeVisible();
    await expect(header.locator(".v3-badge", { hasText: levelBadge })).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- inbox ---------------- */
const ALERT = "alert-check-odd-inbox";
const inboxItem = { id: "inbox-check-odd", kind: "alert", tone: "neutral", title: "CheckOddSeverity", sub: "app-check", at: new Date().toISOString(), unread: true, href: "/inbox?item=inbox-check-odd", alert_id: ALERT };
const alertDetail = (reason: string) => ({
  id: ALERT,
  revision: "1",
  severity: null,
  severity_reason: reason,
  title: "CheckOddSeverity",
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
  unavailable: ["source", "rule", "triage", "series", "labels", "runbook", "grouping.group", "grouping.inhibited", "grouping.change", "severity"],
});
async function pinInbox(page: Page, reason: string) {
  await page.route(at("/inbox"), (r) => json(r, 200, { items: [inboxItem], counts: { all: 1, mine: 0, mentions: null, subscribed: null } }));
  await page.route(at(`/alerts/${ALERT}`), (r) => json(r, 200, alertDetail(reason)));
  await page.route(at(`/incidents/${ALERT}`), (r) => json(r, 404, { error: { code: "not_found", message: "Check" } }));
}

test.describe("inbox: an alert whose severity is unknown says 级别未知 (PA-239, PA-240)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const reason of ["unclassified", "not_filled"]) {
    test(`severity null (${reason}): the detail says 级别未知 and the missing-data line names 级别`, async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      await pinInbox(page, reason);
      await page.goto(origin + "/inbox?item=inbox-check-odd");
      const detail = page.locator(".alert-detail");
      await expect(detail.getByRole("heading", { name: "CheckOddSeverity" })).toBeVisible({ timeout: 20_000 });
      await expect(detail.getByText(UNKNOWN, { exact: true })).toBeVisible();
      await expect(detail.locator(".v3-badge", { hasText: levelBadge })).toHaveCount(0);
      // "severity" in unavailable is the severity, not grouping.
      await expect(page.locator(".unk-banner")).toContainText("级别");
      expect(m.pageErrors).toEqual([]);
    });
  }
});

/* ---------------- home ---------------- */
test.describe("home: an attention item whose severity is unknown says 级别未知 (PA-239)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the attention row shows 级别未知 instead of a bare dash", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await patchRead(page, "/home", (real) => ({
      ...real,
      attention: [{ id: ODD, severity: null, severity_reason: "unclassified", service: "app-check", env: "prod", symptom: "CheckOddSeverity", meta: "已入库告警", at: at0, ai: null, href: `/incidents/${ODD}`, alert_state: "firing" }],
    }));
    await page.goto(origin + "/");
    const row = page.locator(".attn", { hasText: "CheckOddSeverity" });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row.getByText(UNKNOWN, { exact: true })).toBeVisible();
    await expect(row.locator(".v3-badge", { hasText: levelBadge })).toHaveCount(0);
  });
});
