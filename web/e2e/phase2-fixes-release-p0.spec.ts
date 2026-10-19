import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * Release-gate P0 fixes found by the G1 severity review (WAN-434, wrap-up plan G1).
 *
 * PA-245 (WAN-604, fixed with the release batch): with no node selected, 派生 looked for a node with the ID "root";
 * real roots have other IDs, so the click did nothing. The canvas and the fork request are pinned with page.route.
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

const INV = "INV-release-check";
const nodeContext = (version: string) => ({ context: { version, tokens: null, messages: 0, excludes: [] }, versions: [] });
const CANVAS = {
  investigation: { id: INV, title: "发布检查调查", status: "active", env: "prod", participants: [] },
  loop: [],
  tray: [],
  nodes: [
    { id: "root-7f3a", kind: "root", title: "检查根节点", subtitle: "", status: "active", x: 0, y: 0, w: 280, layout_version: 1, ...nodeContext("v1") },
    { id: "b1", kind: "branch", parent: "root-7f3a", title: "检查分支", subtitle: "", status: "active", x: 400, y: 0, w: 280, summary: null, layout_version: 1, ...nodeContext("v1.1") },
  ],
};

test.describe("fork with nothing selected (PA-245)", () => {
  test("派生 forks from the real root when no node is selected", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 200, CANVAS));
    const forks: { parent_id?: string }[] = [];
    await page.route(at(`/investigations/${INV}/branches`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      forks.push(r.request().postDataJSON());
      return json(r, 201, { id: "b-new", kind: "branch", parent: "root-7f3a", title: "新分支 2", subtitle: "", status: "active", x: 350, y: 70, w: 280, summary: null, layout_version: 1, ...nodeContext("v1.2") });
    });
    await page.goto(origin + `/canvas/${INV}`);
    await expect(page.getByText("检查根节点", { exact: true }).first()).toBeVisible({ timeout: 20_000 });
    await page.getByRole("button", { name: "派生", exact: true }).click();
    await expect.poll(() => forks.length).toBe(1);
    expect(forks[0].parent_id).toBe("root-7f3a");
    expect(m.pageErrors).toEqual([]);
  });
});
