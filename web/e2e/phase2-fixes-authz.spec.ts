import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { zh, json, at, patchRead } from "./support/checks";

/**
 * WAN-434 phase 2 checks for PA-106 (Webhook redelivery and business 401s). Each check pins the page to fixed API
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

test.describe("business 401s (PA-106)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  // A route that checks its own credentials (an inbound receiver's source signature) answers like this.
  const sourceRejected = { error: { code: "unauthorized", message: "Invalid source signature" } };

  test("a business 401 with a live session stays on the page", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await page.route(at("/webhooks"), (r) => json(r, 401, sourceRejected));
    const checked = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/v1/auth/me", { timeout: 20_000 });
    await page.goto(origin + "/integrations/webhooks");
    // The client asks the session itself before signing out; the real session is alive.
    expect((await checked).status()).toBe(200);
    await page.waitForTimeout(1_500);
    expect(new URL(page.url()).pathname).toBe("/integrations/webhooks");
  });

  test("a 401 while the session store is unavailable stays on the page (PA-59)", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await page.route(at("/webhooks"), (r) => json(r, 401, sourceRejected));
    await page.route(at("/auth/me"), (r) => json(r, 503, { error: { code: "session_store_unavailable", message: "Sessions cannot be checked right now; retry shortly" } }));
    const checked = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/v1/auth/me", { timeout: 20_000 });
    await page.goto(origin + "/integrations/webhooks");
    expect((await checked).status()).toBe(503);
    await page.waitForTimeout(1_500);
    expect(new URL(page.url()).pathname).toBe("/integrations/webhooks");
  });

  test("a 401 whose session is gone still goes to sign-in", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    // Both answers come from page.route: the real session stays signed in.
    await page.route(at("/webhooks"), (r) => json(r, 401, { error: { code: "unauthorized", message: "Session expired" } }));
    await page.route(at("/auth/me"), (r) => json(r, 401, { error: { code: "unauthorized", message: "Session expired" } }));
    await page.goto(origin + "/integrations/webhooks");
    await page.waitForURL((u) => u.pathname === "/login", { timeout: 20_000 });
    expect(new URL(page.url()).searchParams.get("next")).toBe("/integrations/webhooks");
  });
});
