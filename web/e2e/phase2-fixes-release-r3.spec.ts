import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { at, json, zh } from "./support/checks";

/**
 * Checks for round 3 defects fixed before the v0.1.0 release. Every check pins the API with page.route and answers
 * every write itself, so it runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-release-r3.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

/* ---------------- new template dialog lists general canvases past the first page (PA-253) ---------------- */
const canvas = (id: string, title: string, kind: string, at: string) => ({
  id, title, kind, status: kind === "investigation" ? "open" : "active", meta: "检查用", participants: [], updated_at: at,
  href: `/canvas/${id}`, services: [], thumb: { nodes: [], edges: [] },
});
// The most recently updated 20 canvases are all investigations; the two plan canvases are older.
const investigations = Array.from({ length: 20 }, (_, i) =>
  canvas(`cv-check-inv-${i}`, `检查用调查 ${i}`, "investigation", `2026-10-19T08:${String(59 - i).padStart(2, "0")}:00Z`),
);
const plans = [canvas("cv-check-plan-1", "检查用计划画布一", "plan", "2026-10-18T09:00:00Z"), canvas("cv-check-plan-2", "检查用计划画布二", "plan", "2026-10-17T09:00:00Z")];
const counts = { all: 22, investigation: 20, plan: 2, review: 0, analysis: 0, custom: 0 };

test("the new template dialog offers general canvases even when the first page is all investigations", async ({ page }) => {
  await page.addInitScript(zh);
  const posted: Record<string, unknown>[] = [];
  await page.route(at("/canvases"), (r) => {
    const url = new URL(r.request().url());
    const kind = url.searchParams.get("kind") ?? "";
    const items = kind === "" ? investigations : kind === "plan" ? plans : kind === "investigation" ? investigations : [];
    return json(r, 200, { items, counts, total: kind === "" ? counts.all : (counts as Record<string, number>)[kind] ?? 0, page: 1, page_size: Number(url.searchParams.get("page_size") ?? 20) });
  });
  await page.route(at("/canvas-templates"), (r) => {
    if (r.request().method() !== "POST") return r.fallback();
    posted.push(r.request().postDataJSON());
    return json(r, 201, { id: "tpl-check", kind: "plan", name: "检查用", desc: "", nodes: "0" });
  });
  await page.goto(origin + "/templates");
  await page.getByRole("button", { name: "新建模板" }).click({ timeout: 20_000 });
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator(".tpl-pick", { hasText: "检查用计划画布一" })).toBeVisible({ timeout: 10_000 });
  await expect(dialog.locator(".tpl-pick", { hasText: "检查用计划画布二" })).toBeVisible();
  await expect(dialog.locator(".tpl-pick", { hasText: "检查用调查" })).toHaveCount(0);
  await dialog.getByRole("button", { name: "保存模板" }).click();
  await expect.poll(() => posted.length).toBe(1);
  expect(posted[0].from_canvas).toBe("cv-check-plan-1");
});
