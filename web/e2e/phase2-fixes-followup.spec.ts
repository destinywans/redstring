import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { at, json, zh } from "./support/checks";

/**
 * Checks for the defects the parallel fix sessions found while fixing (PA-185, PA-186, PA-187). Route-pinned, no
 * writes; real-data verification is in the registry (docs/releases/2026-10-16-defect-convergence.md).
 */
const origin = INSTANCES.prod.origin;
const APP = "app-followup-check";
const SERVICE = Buffer.from(`${APP}\u0000prod`).toString("base64url");
const NAME = "后续检查服务";
const CATALOG = {
  kpis: [],
  facets: [],
  items: [{ id: SERVICE, name: NAME, desc: "", tier: null, team: null, oncall: null, status: null, slo: { target: null, budget_left: null }, release: null, alerts: null, connectors: null, unavailable: {} }],
};

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

test.describe("add chip search (PA-185)", () => {
  test("a service is found by its name, not only its catalog ID", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/services"), (r) => json(r, 200, CATALOG));
    await page.goto(origin + "/canvases?new=plan");
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: /服务/ }).last().click({ timeout: 20_000 });
    await page.locator(".demo-add-search input").fill("后续检查");
    await expect(page.locator(".demo-menu button", { hasText: NAME })).toBeVisible();
  });
});

test.describe("new incident services (PA-186)", () => {
  test("the drafted service can't be joined by another one the server would refuse", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/services"), (r) => json(r, 200, CATALOG));
    await page.route(at("/incidents/draft"), (r) =>
      json(r, 200, {
        title: "检查用事件", severity: "P2", severity_hint: "", env: "prod", services: [{ id: APP, tier: null }], impact: null,
        alerts: [{ id: "alert-check", severity: "P2", text: "LabHigh5xxRate", at: "2026-10-17T02:00:00Z" }],
        commander: null, participants: null, war_room: null,
      }),
    );
    await page.goto(origin + "/incidents?new=incident&alerts=alert-check");
    const drawer = page.getByRole("dialog", { name: "新建事件" });
    await expect(drawer.getByText(NAME, { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(drawer.getByRole("button", { name: /^影响服务/ })).toHaveCount(0);
  });
});

test.describe("conversation token usage (PA-187)", () => {
  test("unknown usage shows a dash, not 0k", async ({ page }) => {
    await page.addInitScript(zh);
    const conv = { id: "conv-check", title: "检查用对话", mode: "chat", status: "active", source: "manual", scope: "prod", participants: [], size: "1", updated_at: "2026-10-17T10:00:00Z", tokens: null };
    await page.route(at("/conversations"), (r) => json(r, 200, { items: [conv], total: 1, page: 1, page_size: 50, stats: [], counts: { mine: 1, team: 0, triage: 0, archived: 0 } }));
    await page.goto(origin + "/conversations");
    const row = page.locator("tbody tr", { hasText: "检查用对话" });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).not.toContainText("0k");
    await expect(row).toContainText("—");
  });
});
