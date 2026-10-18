import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, read, zh } from "./support/checks";

/**
 * Unknown states that say why honestly (WAN-434 between rounds 2 and 3). The real-data checks only read; the rest pin
 * the page with page.route, so they run on any data and never write.
 *
 *   PA-191 (WAN-548): a dependency list that the CMDB relations can't express, and an environment with no alert rule
 *   source, no longer read as "the source failed or timed out; refresh to try again".
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-reasons.spec.ts
 */
const prod = INSTANCES.prod.origin;
const failed = "来源连接失败或超时，刷新页面可再试一次。";
const failedEn = "The source failed or timed out; refresh to try again.";

/** checkout-api's service ID in prod, read in the page, or skip. */
async function checkoutApi(page: Page): Promise<string> {
  const { body } = await read(page, "/services");
  const id = ((body?.items ?? []) as { id: string; name: string }[]).find((s) => s.name === "checkout-api")?.id;
  test.skip(!id, "no checkout-api in the prod catalog");
  return id!;
}

test.describe("dependencies the CMDB relations can't express (PA-191)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("real data: checkout-api's unknown dependencies have their own reason and the page doesn't blame the source", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(prod + "/healthz");
    const id = await checkoutApi(page);
    const { status, body } = await read(page, `/services/${id}`);
    expect(status).toBe(200);
    test.skip(body.deps !== null, "checkout-api's dependencies are known now");
    expect(body.unavailable.deps).toBe("unmapped_relation");
    expect(body.unavailable["tabs.deps"]).toBe("unmapped_relation");
    await page.goto(prod + `/services/${id}`);
    await expect(page.locator(".unk-block", { hasText: "关系无法表示为依赖" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("CMDB 里有关联不能对应到你可见的服务之间的依赖")).toBeVisible();
    await expect(page.getByText(failed)).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("pinned, English: the unmapped-relation reason reads as a property of the data", async ({ page }) => {
    await page.addInitScript(en);
    await page.goto(prod + "/healthz");
    const id = await checkoutApi(page);
    const real = (await read(page, `/services/${id}`)).body;
    await page.route(at(`/services/${id}`), (r) =>
      json(r, 200, { ...real, deps: null, tabs: { ...real.tabs, deps: null }, unavailable: { ...real.unavailable, deps: "unmapped_relation", "tabs.deps": "unmapped_relation" } }),
    );
    await page.goto(prod + `/services/${id}`);
    await expect(page.locator(".unk-block", { hasText: "Relations don't map to dependencies" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("so no list is shown rather than an incomplete one")).toBeVisible();
    await expect(page.getByText(failedEn)).toHaveCount(0);
  });
});
