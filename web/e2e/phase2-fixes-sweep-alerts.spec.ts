import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

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
