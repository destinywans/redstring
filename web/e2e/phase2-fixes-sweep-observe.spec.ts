import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, zh } from "./support/checks";

/**
 * Sweep by defect class, group 4 (WAN-434): observe, integrations, the roles / AI preferences / members settings and
 * the shell. Every read is pinned with page.route, so this runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-sweep-observe.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

const role = (id: string) => ({
  id,
  name: "Check role",
  members: 1,
  kind: "business",
  desc: "Check role for the sweep",
  member_names: ["Check Member"],
  scope: ["global"],
  actions: ["查看", "创建 / 编辑", "删除", "批准", "管理"],
  matrix: [{ resource: "Check resource", grants: [1, 0, 0, 0, 0] }],
  notes: "",
});
async function pinRole(page: Page, id: string) {
  await page.route(at("/roles"), (r) => json(r, 200, { items: [{ id, name: "Check role", members: 1, kind: "business" }] }));
  await page.route(at(`/roles/${id}`), (r) => r.fulfill({ status: 200, contentType: "application/json", headers: { "X-Role-Matrix-Revision": "1" }, body: JSON.stringify(role(id)) }));
}

/** PA-207 (WAN-566): the History tab showed a made-up change by a sample person; the server keeps no per-role history. */
test.describe("role history on the real page (PA-207)", () => {
  test("says where role changes are recorded instead of a sample change", async ({ page }) => {
    await page.addInitScript(zh);
    await pinRole(page, "pa207-check");
    const m = monitor(page);
    await page.goto(origin + "/settings/roles");
    await expect(page.getByRole("heading", { name: "Check role" })).toBeVisible({ timeout: 20_000 });
    await page.getByRole("tab", { name: "变更记录" }).click();
    await expect(page.getByText("陈雨")).toHaveCount(0);
    await expect(page.getByText("9/20")).toHaveCount(0);
    await expect(page.getByText("还没有按角色整理的变更记录", { exact: false })).toBeVisible();
    await expect(page.getByText("保存权限会写入审计日志", { exact: false })).toBeVisible();
    expect(m.pageErrors).toEqual([]);
  });

  test("English says the same", async ({ page }) => {
    await page.addInitScript(en);
    await pinRole(page, "pa207-check");
    await page.goto(origin + "/settings/roles");
    await expect(page.getByRole("heading", { name: "Check role" })).toBeVisible({ timeout: 20_000 });
    await page.getByRole("tab", { name: "History" }).click();
    await expect(page.getByText("Chen Yu")).toHaveCount(0);
    await expect(page.getByText("No per-role change history is kept yet", { exact: false })).toBeVisible();
  });
});
