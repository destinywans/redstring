import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * Release-gate P0 fixes found by the G1 severity review (WAN-434, wrap-up plan G1).
 *
 * PA-248 (WAN-608): the pull ingest list showed a green 可用 for every configured source, while the server only checks
 * that the connector is enabled and bound; it never reads the upstream. The list is pinned with page.route.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-release-p0.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

test.describe("pull ingest says what it checked (PA-248)", () => {
  test("a configured source reads 已配置 in a neutral badge, not a green 可用", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const item = (id: string, name: string, state: string) => ({
      id, name, application_id: "app-check", environment_id: "prod", connector_id: "conn-check", connector_name: "检查连接器", query_id: "check_query", state, href: `/ingest/pull/${id}`,
    });
    await page.route(at("/ingest/pull"), (r) =>
      json(r, 200, { items: [item("pull-ready", "检查已配置来源", "ready"), item("pull-down", "检查不可用来源", "source_unavailable")], empty_reason: null, unavailable: {} }),
    );
    await page.goto(origin + "/integrations/ingest");
    const ready = page.locator("tr", { hasText: "检查已配置来源" });
    await expect(ready).toContainText("已配置", { timeout: 20_000 });
    await expect(ready).not.toContainText("可用");
    await expect(ready.locator(".v3-badge")).not.toHaveClass(/t-ok/);
    await expect(page.locator("tr", { hasText: "检查不可用来源" })).toContainText("来源不可用");
    expect(m.pageErrors).toEqual([]);
  });
});
