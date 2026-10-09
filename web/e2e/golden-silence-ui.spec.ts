import { test, expect } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";

/**
 * Golden path · the operator proposes a silence through the /silences drawer (approved design).
 * GOLDEN_INVESTIGATION=<INV-…> must be an open investigation of the target service.
 * Writes the proposal id to .local/acceptance/e2e/golden/silence-proposal.txt for golden-approval.
 */
const investigation = process.env.GOLDEN_INVESTIGATION ?? "";
const alertname = process.env.GOLDEN_ALERTNAME ?? "LabNoBusinessTraffic";
const OUT = resolve(LOCAL, "e2e/golden");
mkdirSync(OUT, { recursive: true });
test.skip(!investigation, "set GOLDEN_INVESTIGATION");
test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

test("operator proposes a silence from the drawer", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const m = monitor(page);
  await page.goto(INSTANCES.prod.origin + "/silences?new=silence");
  const drawer = page.getByRole("dialog").filter({ hasText: "提议静默" });
  await drawer.waitFor({ timeout: 20_000 });
  const section = drawer.locator("section, div").filter({ has: page.getByText("关联的调查", { exact: true }) }).last();
  await section.getByRole("button").first().click();
  await page.getByText(investigation, { exact: false }).last().click();
  await expect(drawer.getByText(/应用 .* 来自这个调查/)).toBeVisible();
  await drawer.getByRole("button", { name: "条件" }).click();
  await drawer.getByPlaceholder("alertname").last().fill("alertname");
  const values = drawer.locator("input.mono, input[class*=mono]");
  await values.last().fill(alertname);
  await drawer.getByPlaceholder(/发布回滚中/).fill("验收：界面提议静默");
  await drawer.getByRole("button", { name: "提交静默提案" }).click();
  const link = page.locator('a[href^="/review/"]').first();
  await expect(link).toBeVisible({ timeout: 20_000 });
  const id = (await link.getAttribute("href"))!.split("/").pop()!;
  await page.screenshot({ path: resolve(OUT, "silence-proposed.png"), fullPage: true });
  writeFileSync(resolve(OUT, "silence-proposal.txt"), id + "\n");
  writeFileSync(resolve(OUT, "silence-proposal.json"), JSON.stringify({ id, api: m.api.filter((a) => a.path?.startsWith("/silences")), pageErrors: m.pageErrors }, null, 2));
  expect(m.pageErrors).toEqual([]);
});
