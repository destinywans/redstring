import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * Checks for the severity defects fixed before the v0.1.0 release (PA-246). Alerts from an outside Alertmanager often
 * carry a severity outside the shared table (major, page, minor) or none at all; the server answers `severity: null`
 * with `severity_reason`. Every check pins the API with page.route and answers every write itself, so it runs on any
 * data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-release.spec.ts
 */
const origin = INSTANCES.prod.origin;
const at0 = "2026-10-19T09:00:00Z";
const UNKNOWN = "级别未知";
/** A P1–P4 badge; an unknown severity must not show one. */
const levelBadge = /^P[1-4]$/;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

/* ---------------- new incident from alerts whose severity is unknown (PA-246) ---------------- */
const ODD = { id: "alert-check-odd", severity: null, severity_reason: "unclassified", text: "CheckOddSeverity", at: at0 };
const BLANK = { id: "alert-check-blank", severity: null, severity_reason: "not_filled", text: "CheckBlankSeverity", at: at0 };
const KNOWN = { id: "alert-check-known", severity: "P2", text: "CheckKnownSeverity", at: at0 };
const draft = (severity: string | null, hint: string, alerts: object[]) => ({
  title: "检查用事件", severity, severity_hint: hint, env: "prod", services: [{ id: "app-check", tier: null }], impact: null,
  alerts, commander: null, participants: null, war_room: null, source: "persisted_alerts", partial: true,
  unavailable: [...(severity ? [] : ["severity"]), "impact", "service.tier", "commander", "participants", "war_room", "ai_analysis"],
});

/** Opens the new incident drawer on a pinned draft; every POST /incidents is answered here and recorded. */
async function openDraft(page: Page, body: ReturnType<typeof draft>) {
  const posted: Record<string, unknown>[] = [];
  await page.route(at("/incidents/draft"), (r) => json(r, 200, body));
  await page.route(at("/incidents"), (r) => {
    if (r.request().method() !== "POST") return r.fallback();
    posted.push(r.request().postDataJSON());
    return json(r, 201, { id: "INC-check-release" });
  });
  await page.goto(origin + `/incidents?new=incident&alerts=${body.alerts.map((a) => (a as { id: string }).id).join(",")}`);
  const drawer = page.getByRole("dialog", { name: "新建事件" });
  await expect(drawer.locator(".f-alert-row").first()).toBeVisible({ timeout: 20_000 });
  return { drawer, posted };
}

test.describe("new incident: alerts whose severity is unknown can start one (PA-246)", () => {
  test("no known level: nothing is pre-selected, rows say 级别未知, and the chosen level is sent", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const { drawer, posted } = await openDraft(page, draft(null, "所选告警的级别未知，请选择级别", [ODD, BLANK]));
    await expect(drawer).not.toContainText("没能从所选告警预填");
    for (const text of [ODD.text, BLANK.text]) {
      const row = drawer.locator(".f-alert-row", { hasText: text });
      await expect(row.getByText(UNKNOWN, { exact: true })).toBeVisible();
      await expect(row.locator(".v3-badge", { hasText: levelBadge })).toHaveCount(0);
    }
    await expect(drawer).toContainText("所选告警的级别未知，请选择级别");
    const levels = drawer.getByRole("radiogroup", { name: "级别" });
    await expect(levels.getByRole("radio", { checked: true })).toHaveCount(0);
    const create = drawer.getByRole("button", { name: "创建事件" });
    await expect(create).toBeDisabled();
    await levels.getByRole("radio", { name: "P2" }).click();
    await expect(create).toBeEnabled();
    await create.click();
    await expect(page.locator(".v3-toast", { hasText: "INC-check-release" })).toBeVisible();
    expect(posted).toHaveLength(1);
    expect(posted[0]).toMatchObject({ severity: "P2", alert_ids: [ODD.id, BLANK.id] });
    expect(m.pageErrors).toEqual([]);
  });

  test("some known: the highest known level is pre-selected and the unknown row says 级别未知", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const hint = "已知级别中的最高级；另有 1 条告警级别未知，请确认";
    const { drawer } = await openDraft(page, draft("P2", hint, [KNOWN, ODD]));
    await expect(drawer).toContainText(hint);
    await expect(drawer.getByRole("radiogroup", { name: "级别" }).getByRole("radio", { checked: true })).toHaveText("P2");
    const odd = drawer.locator(".f-alert-row", { hasText: ODD.text });
    await expect(odd.getByText(UNKNOWN, { exact: true })).toBeVisible();
    await expect(odd.locator(".v3-badge", { hasText: levelBadge })).toHaveCount(0);
    await expect(drawer.locator(".f-alert-row", { hasText: KNOWN.text }).locator(".v3-badge", { hasText: "P2" })).toBeVisible();
    await expect(drawer.getByRole("button", { name: "创建事件" })).toBeEnabled();
    expect(m.pageErrors).toEqual([]);
  });
});
