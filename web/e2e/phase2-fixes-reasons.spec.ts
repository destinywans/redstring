import { existsSync } from "node:fs";
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
const nonprod = INSTANCES.nonprod.origin;
const failed = "来源连接失败或超时，刷新页面可再试一次。";
const failedEn = "The source failed or timed out; refresh to try again.";

/** GET an API path in the page with the given environment header; { status, body }. */
async function readIn(page: Page, path: string, env: string): Promise<{ status: number; body: any }> {
  return page.evaluate(
    async ([p, e]) => {
      const r = await fetch("/api/v1" + p, { headers: { "X-AIOps-Environment": e } });
      return { status: r.status, body: await r.json().catch(() => null) };
    },
    [path, env] as const,
  );
}

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

test.describe("an environment without an alert rule source (PA-191)", () => {
  test("real data: nonprod dev's rule list is not configured for every role, never a failed source", async ({ browser }) => {
    let checked = 0;
    for (const role of ["admin", "operator", "viewer", "approver1", "approver2", "scoped"]) {
      const state = statePath("nonprod", role);
      if (!existsSync(state)) continue;
      const context = await browser.newContext({ storageState: state });
      const page = await context.newPage();
      await page.goto(nonprod + "/healthz");
      const { status, body } = await readIn(page, "/alert-rules", "dev");
      expect(status, `${role}: ${JSON.stringify(body)}`).toBe(200);
      // A role that sees no dev application gets the known empty scope instead (PA-09).
      if (body.empty_reason !== "no_authorized_target") {
        expect(body.items, role).toBeNull();
        expect(body.counts, role).toBeNull();
        expect(body.unavailable, role).toMatchObject({ items: "not_configured", counts: "not_configured" });
      }
      await context.close();
      checked++;
    }
    expect(checked).toBeGreaterThan(0);
  });

  test.describe("as admin", () => {
    test.use({ storageState: statePath("nonprod", "admin"), viewport: { width: 1440, height: 1000 } });

    test("real data: the all view is unknown rather than partial counts, and test still lists its rules", async ({ page }) => {
      await page.goto(nonprod + "/healthz");
      const all = await readIn(page, "/alert-rules", "all");
      expect(all.status, JSON.stringify(all.body)).toBe(200);
      expect(all.body.counts).toBeNull();
      expect(all.body.unavailable.items).toBe("not_configured");
      const testEnv = await readIn(page, "/alert-rules", "test");
      expect(testEnv.status, JSON.stringify(testEnv.body)).toBe(200);
      expect(Array.isArray(testEnv.body.items)).toBe(true);
      expect(testEnv.body.counts).not.toBeNull();
    });

    test("real data: the dev rules page says no rule source is connected, not that the source failed", async ({ page }) => {
      await page.addInitScript(zh);
      await page.addInitScript(() => localStorage.setItem("aiops-v3-env", "dev"));
      const m = monitor(page);
      await page.goto(nonprod + "/alert-rules?env=dev");
      await expect(page.locator(".unk-block", { hasText: "告警规则来源未接入" })).toBeVisible({ timeout: 20_000 });
      await expect(page.getByText(failed)).toHaveCount(0);
      expect(m.pageErrors).toEqual([]);
    });
  });
});
