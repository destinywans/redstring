import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * First-run fixes found by the G6 clean-clone quickstart (WAN-434, wrap-up plan G6). An empty database is what a new
 * user sees first; both pages are pinned with page.route, so the checks run on any data and never write.
 *
 * PA-249 (WAN-609): with no alert groups, nothing is selected, the policy is never read, and the 聚合与通知 panel stayed
 * a skeleton forever.
 *
 * PA-250 (WAN-610): without a model bindings file, /models/admin answers 503 model_binding_unavailable and the page said
 * the source failed or timed out; nothing failed, the bindings are not set up.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-release-g6.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

test.describe("alerts with no groups (PA-249)", () => {
  test("the 聚合与通知 panel says nothing is selected instead of loading forever", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const policyReads: string[] = [];
    await page.route(at("/alert-groups"), (r) => json(r, 200, { items: [], counts: { firing: 0, pending: 0, silenced: null, resolved: 0 } }));
    await page.route((url) => /^\/api\/v1\/alert-groups\/[^/]+\/policy$/.test(url.pathname), (r) => {
      policyReads.push(r.request().url());
      return r.fallback();
    });
    await page.goto(origin + "/alerts");
    const aside = page.locator("aside.aside", { hasText: "聚合与通知" });
    await expect(aside).toContainText("没有选中的告警组", { timeout: 20_000 });
    await expect(aside.locator(".skeleton")).toHaveCount(0);
    expect(policyReads).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });
});

const failModels = (page: import("@playwright/test").Page, code: string) =>
  page.route(at("/models/admin"), (r) => json(r, 503, { error: { code, message: "check: pinned" } }));

test.describe("models without bindings (PA-250)", () => {
  test("an unconfigured binding reads as not set up, not as a failed connection", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await failModels(page, "model_binding_unavailable");
    await page.goto(origin + "/integrations/models");
    const body = page.locator(".v3-page-body");
    await expect(body).toContainText("这个环境还没有绑定模型", { timeout: 20_000 });
    await expect(body).not.toContainText("连接失败");
    await expect(body).not.toContainText("暂时不可用");
    expect(m.pageErrors).toEqual([]);
  });

  // Control: another 503 is still a source that couldn't be read.
  test("another 503 still says the source is temporarily unavailable", async ({ page }) => {
    await page.addInitScript(zh);
    await failModels(page, "model_admin_unavailable");
    await page.goto(origin + "/integrations/models");
    await expect(page.locator(".v3-page-body")).toContainText("暂时不可用", { timeout: 20_000 });
  });
});
