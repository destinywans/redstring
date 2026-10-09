import { test, expect } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, type InstanceName } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor, type PageReport } from "./support/monitor";

/**
 * Phase 1 · every static v3 route, loaded against the real backend.
 * Records each /api/v1 call, console error and uncaught exception; the summary
 * (tools/acceptance/crawl-summary.py) classifies them. A page fails on uncaught
 * exceptions or a blank root; API errors are reported for triage, not asserted here.
 */
export const STATIC_ROUTES = [
  "/", "/settings/preferences", "/conversations", "/conversations/new", "/canvases", "/templates",
  "/inbox", "/incidents", "/incidents?new=incident", "/alerts", "/alert-rules", "/alert-rules/new", "/routing", "/silences",
  "/runbooks", "/runbooks/new", "/postmortems", "/postmortems/new", "/memory", "/knowledge-sources", "/knowledge-sources/new",
  "/services", "/services/new", "/resources", "/metrics", "/traces", "/logs", "/changes",
  "/integrations", "/integrations/new", "/integrations/sync", "/integrations/models", "/integrations/mcp",
  "/integrations/ingest", "/integrations/ingest/push", "/integrations/ingest/stack", "/integrations/mapping",
  "/integrations/coverage", "/integrations/webhooks",
  "/agents/lifecycle", "/agents/prompts", "/agents/tools", "/agents/evals", "/agents/runs", "/agents/guardrails",
  "/settings/profile", "/settings/notifications", "/settings/account-security", "/settings/ai", "/settings/workspace",
  "/settings/members", "/settings/roles", "/settings/security", "/settings/audit",
];

const COMBOS = [
  { locale: "zh-CN", theme: "dark" },
  { locale: "en-US", theme: "light" },
];
// CRAWL_OUT keeps a fault-injection run apart from the baseline results.
const OUT = resolve(LOCAL, process.env.CRAWL_OUT ?? "e2e/crawl");
mkdirSync(OUT, { recursive: true });

for (const instance of Object.keys(INSTANCES) as InstanceName[]) {
  for (const combo of COMBOS) {
    test.describe(`${instance} · ${combo.locale} · ${combo.theme}`, () => {
      test.use({ storageState: statePath(instance, "admin") });
      for (const route of STATIC_ROUTES) {
        test(route, async ({ page }) => {
          await page.addInitScript((c) => {
            localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: c.locale, theme: c.theme }));
          }, combo);
          const m = monitor(page);
          await page.goto(INSTANCES[instance].origin + route);
          // React replaces the #boot placeholder on mount; only then do page queries start.
          const mounted = await page.waitForFunction(() => !document.getElementById("boot"), undefined, { timeout: 20_000 }).then(() => true, () => false);
          // Lazy page code may still be arriving; a page stuck loading for 20s counts as blank.
          await page.waitForFunction(() => !document.querySelector("[data-loading]"), undefined, { timeout: 20_000 }).catch(() => undefined);
          await m.quiet();
          expect.soft(mounted, "app mounted within 20s").toBe(true);
          await m.settle();
          // Pages that auto-select the first item fetch its detail after the list; give that a beat, then settle again.
          await page.waitForTimeout(800);
          await m.quiet(800, 8_000);
          await m.settle();
          // Render errors caught by PageBoundary leave no pageerror; read the fallback marker instead.
          const caught = await page.locator("[data-page-error]").first().getAttribute("data-page-error", { timeout: 500 }).catch(() => null);
          if (caught) m.pageErrors.push(`PageBoundary: ${caught}`);
          const blank = await page.evaluate(() => (document.getElementById("root")?.innerText ?? "").trim().length < 20);
          const key = `${instance}-${combo.locale}-${combo.theme}-${route.replace(/[/?=]+/g, "_") || "home"}`;
          const shot = resolve(OUT, `${key}.png`);
          await page.screenshot({ path: shot });
          const report: PageReport = {
            route, instance, locale: combo.locale, theme: combo.theme,
            api: m.api, consoleErrors: m.consoleErrors, pageErrors: m.pageErrors, blank,
            title: await page.title(), screenshot: shot,
          };
          writeFileSync(resolve(OUT, `${key}.json`), JSON.stringify(report, null, 2));
          expect.soft(m.pageErrors, "uncaught exceptions").toEqual([]);
          expect.soft(blank, "page rendered nothing").toBe(false);
        });
      }
    });
  }
}
