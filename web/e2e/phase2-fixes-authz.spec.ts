import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { zh, patchRead } from "./support/checks";

/**
 * WAN-434 phase 2 checks for PA-106 (Webhook redelivery). Each check pins the page to fixed API
 * answers with page.route, so it runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-authz.spec.ts
 */
const origin = INSTANCES.prod.origin;
const hooks = [
  { id: "gitlab:check-first", brand: "gitlab", name: "检查用 GitLab", url: "/api/v1/webhooks/gitlab/check-first", verify: "X-Gitlab-Token", count: "unknown", last_at: null, last_code: null, status: "unverified" },
  { id: "alertmanager:check-second", brand: "prometheus", name: "检查用 Alertmanager", url: "/api/v1/webhooks/alertmanager/check-second", verify: "Bearer", count: "0", last_at: null, last_code: null, status: "unverified" },
];

test.describe("webhook redelivery (PA-106)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a real instance offers no redelivery and opens on the first listed hook", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/healthz");
    await patchRead(page, "/webhooks", (real) => ({ ...real, inbound: hooks, deliveries: [] }));
    const redelivered: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/redeliver")) redelivered.push(r.url());
    });
    await page.goto(origin + "/integrations/webhooks");
    // The panel names the first hook the server listed, not a hard-coded `gitlab`.
    await expect(page.getByText("最近投递 · 检查用 GitLab")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("tr.selected")).toContainText("检查用 GitLab");
    await expect(page.getByText("重新投递", { exact: true })).toHaveCount(0);
    // Picking another hook still offers nothing to redeliver.
    await page.getByText("检查用 Alertmanager", { exact: true }).click();
    await expect(page.getByText("最近投递 · 检查用 Alertmanager")).toBeVisible();
    await expect(page.getByText("重新投递", { exact: true })).toHaveCount(0);
    expect(redelivered).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });
});
