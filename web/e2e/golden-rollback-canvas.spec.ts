import { test, expect } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";

/**
 * Golden path · propose a rollback from the AI suggestion on the investigation canvas (PA-72, prod).
 * GOLDEN_INVESTIGATION=<INV id> whose node carries a rollback_suggestion (tools/acceptance/rollback.py --ui
 * asks the AI first). The operator presses the node hint, opens the suggestion card in the inspector,
 * opens the drawer (investigation locked, revision preselected), writes a reason and submits.
 * The proposal id goes to .local/acceptance/e2e/golden/rollback-canvas-proposal.txt.
 */
const investigation = process.env.GOLDEN_INVESTIGATION ?? "";
const origin = INSTANCES.prod.origin;
const OUT = resolve(LOCAL, "e2e/golden");
mkdirSync(OUT, { recursive: true });
test.setTimeout(120_000);
test.skip(!investigation, "set GOLDEN_INVESTIGATION to an investigation whose node has a rollback suggestion");
test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

test("rollback proposed from the canvas suggestion", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const m = monitor(page);
  await page.goto(`${origin}/canvas/${investigation}`);
  const hint = page.getByRole("button", { name: /^建议回滚到修订 (\d+)，查看详情$/ });
  await expect(hint).toBeVisible({ timeout: 30_000 });
  const revision = (await hint.getAttribute("aria-label"))?.match(/修订 (\d+)/)?.[1] ?? "";
  await page.screenshot({ path: resolve(OUT, "rollback-canvas-1-node.png"), fullPage: true });

  await hint.click();
  const open = page.getByRole("button", { name: "打开回滚提案" });
  await expect(open).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText(`建议回滚到修订 ${revision}`).last()).toBeVisible();
  await page.screenshot({ path: resolve(OUT, "rollback-canvas-2-card.png"), fullPage: true });

  await open.click();
  await expect(page.getByText(`已按 AI 建议预选修订 ${revision}，请确认后再提交。`)).toBeVisible({ timeout: 20_000 });
  await page.screenshot({ path: resolve(OUT, "rollback-canvas-3-drawer.png"), fullPage: true });

  await page.getByLabel("回滚原因").fill(`按画布上的 AI 建议回到修订 ${revision}：当前修订注入了 /checkout 5xx`);
  await page.getByRole("button", { name: "提交回滚提案" }).click();
  const done = page.getByText(/已提交回滚提案 (\w+)，等待两人批准/);
  await expect(done).toBeVisible({ timeout: 20_000 });
  const proposal = (await done.innerText()).match(/已提交回滚提案 (\w+)，/)?.[1] ?? "";
  await page.screenshot({ path: resolve(OUT, "rollback-canvas-4-submitted.png"), fullPage: true });
  expect(proposal).toMatch(/^[0-9a-f]{16,}$/);
  writeFileSync(resolve(OUT, "rollback-canvas-proposal.txt"), proposal);
  writeFileSync(resolve(OUT, "rollback-canvas.json"), JSON.stringify({ investigation, revision, proposal,
    api: m.api.filter((a) => /rollback|canvas|investigations/.test(a.path ?? "")) }, null, 2));
  expect.soft(m.pageErrors).toEqual([]);
});
