import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * WAN-434 phase 2 batch between rounds 2 and 3 (see support/checks.ts: fixed API answers, runs on any data, never
 * writes).
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-gates.spec.ts
 */
const origin = INSTANCES.prod.origin;
const at0 = "2026-10-17T09:00:00Z";

// PA-192 (WAN-549): the sidebar's "Agents & versions" pointed at the sample agent "investigator" (404 on a real server).
const agentId = "pa192-check";
const agent = { id: agentId, icon: "cpu", name: "检查用 Agent", desc: "检查用", version: "v1", level: "L0", score: null, runs_7d: null, success: null, avg_tokens: null, status: "paused" };
const lifecycle = (agents: object[]) => ({ stages: null, agents, todos: null, missing: ["stages", "todos"] });
const agentDetail = {
  ...agent,
  revision: 1,
  owner: "check-owner",
  account: null,
  summary: "检查用",
  versions: [{ version: "v1", status: "draft", traffic: null, score: null, success: null, tokens: null, created: at0 }],
  release: { from: "v1", to: "", current: null, target: 0, gates: null, tokens: null },
  diff: null,
  config: null,
};

test.describe("Agents & versions entry (PA-192)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the sidebar entry opens the first listed agent, never the sample id", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/agents/lifecycle"), (r) => json(r, 200, lifecycle([agent])));
    await page.route(at(`/agents/${agentId}`), (r) => json(r, 200, agentDetail));
    await page.goto(origin + "/agents/lifecycle");
    await page.locator("a.nav-child", { hasText: "Agent 与版本" }).click({ timeout: 20_000 });
    await expect(page).toHaveURL(origin + "/agents");
    await expect(page.getByRole("heading", { name: "检查用 Agent" })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("a.nav-child.active")).toContainText("Agent 与版本");
    await expect(page.getByText("找不到这条记录")).toHaveCount(0);
    expect(m.api.filter((c) => c.path.startsWith("/agents/investigator"))).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  test("with no agents the page says so instead of loading for ever", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/agents/lifecycle"), (r) => json(r, 200, lifecycle([])));
    await page.goto(origin + "/agents");
    await expect(page.getByText("还没有你能查看的 Agent")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("找不到这条记录")).toHaveCount(0);
  });
});
