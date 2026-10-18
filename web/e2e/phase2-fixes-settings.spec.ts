import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, patchRead, zh } from "./support/checks";

/**
 * PA-200 (WAN-558): settings pages a role can't view say so instead of an endless skeleton, and their write entries
 * are disabled with the permission to ask for (PA-64). The refused reads are pinned to 403 and /me is the real answer
 * with the module's grants taken out, so this runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-settings.spec.ts
 */
const origin = INSTANCES.prod.origin;
const forbidden = { error: { code: "forbidden", message: "check: permission denied" } };

type Grants = Record<string, Record<string, Record<string, string>>>;
/** /me without the given module grants (whole modules, or "module.action") in every environment. */
async function meWithout(page: Page, drop: string[]) {
  await patchRead(page, "/me", (real: { permissions?: Grants }) => {
    const permissions: Grants = {};
    for (const [env, modules] of Object.entries(real.permissions ?? {})) {
      permissions[env] = {};
      for (const [module, actions] of Object.entries(modules ?? {})) {
        if (drop.includes(module)) continue;
        permissions[env][module] = Object.fromEntries(Object.entries(actions).filter(([action]) => !drop.includes(`${module}.${action}`)));
      }
    }
    return { ...real, permissions };
  });
}

/** The screen-reader reason of the permission gate around the entry with this text. */
const gateReason = (page: Page, text: string) => page.locator(".perm-gate", { hasText: text }).locator(".sr-only");

async function expectLock(page: Page) {
  await expect(page.getByText("没有权限查看", { exact: true })).toBeVisible({ timeout: 20_000 });
  await expect(page.getByText("需要管理员在当前环境授予查看权限。")).toBeVisible();
  await expect(page.locator(".v3-page-body .skeleton")).toHaveCount(0);
}

test.describe("settings pages a role can't view (PA-200)", () => {
  test.use({ storageState: statePath("prod", "approver1"), viewport: { width: 1440, height: 1000 } });

  test("members & teams: the lock instead of the skeleton; sync and invite need members.manage", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await meWithout(page, ["members", "roles"]);
    await page.route(at("/members"), (r) => json(r, 403, forbidden));
    await page.route(at("/roles"), (r) => json(r, 403, forbidden));
    const m = monitor(page);
    await page.goto(origin + "/settings/members");
    await expectLock(page);
    await expect(page.getByRole("button", { name: "从飞书同步", exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "邀请成员", exact: true })).toBeDisabled();
    await expect(gateReason(page, "从飞书同步")).toHaveText("需要 成员·管理 权限");
    await expect(gateReason(page, "邀请成员")).toHaveText("需要 成员·管理 权限");
    expect(m.pageErrors).toEqual([]);
  });

  test("roles & permissions: the lock instead of the skeleton, and no role detail is requested", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await meWithout(page, ["members", "roles"]);
    await page.route(at("/roles"), (r) => json(r, 403, forbidden));
    const m = monitor(page);
    await page.goto(origin + "/settings/roles");
    await expectLock(page);
    // Creating a role stays in the sample (PA-163); the real page has no entry for it.
    await expect(page.getByRole("button", { name: "新建角色", exact: true })).toHaveCount(0);
    await m.quiet();
    expect(m.api.filter((c) => c.path.startsWith("/roles/"))).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  test("audit log: the lock instead of the skeleton; export needs audit.view", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await meWithout(page, ["audit"]);
    await page.route(at("/audit"), (r) => json(r, 403, forbidden));
    const m = monitor(page);
    await page.goto(origin + "/settings/audit");
    await expectLock(page);
    await expect(page.getByRole("button", { name: "导出", exact: true })).toBeDisabled();
    await expect(gateReason(page, "导出")).toHaveText("需要 审计日志·查看 权限");
    expect(m.pageErrors).toEqual([]);
  });

  test("workspace general: the lock when the read is refused", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await meWithout(page, ["workspace"]);
    await page.route(at("/workspace"), (r) => json(r, 403, forbidden));
    const m = monitor(page);
    await page.goto(origin + "/settings/workspace");
    await expectLock(page);
    await expect(gateReason(page, "保存")).toHaveText("需要 工作区·管理 权限");
    expect(m.pageErrors).toEqual([]);
  });

  test("workspace general: a viewer of the workspace sees it but saving needs workspace.manage", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await meWithout(page, ["workspace.manage"]);
    await page.goto(origin + "/settings/workspace");
    await expect(page.getByText("基本信息", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
    await expect(gateReason(page, "保存")).toHaveText("需要 工作区·管理 权限");
  });
});

/**
 * PA-204 (WAN-563): the scope row's add chip only changed local state and offered sample values (app=payment …), so
 * real mode shows only the role's scope badges (D52, like PA-134 and PA-163). The role is pinned with page.route.
 */
test.describe("role scope on the real page (PA-204)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("shows the role's scope without an add chip or sample values", async ({ page }) => {
    await page.addInitScript(zh);
    const id = "pa204-check";
    await page.route(at("/roles"), (r) => json(r, 200, { items: [{ id, name: "Check role", members: 1, kind: "business" }] }));
    await page.route(at(`/roles/${id}`), (r) =>
      r.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { "X-Role-Matrix-Revision": "1" },
        body: JSON.stringify({
          id,
          name: "Check role",
          members: 1,
          kind: "business",
          desc: "Check role for PA-204",
          member_names: ["Check Member"],
          scope: ["global"],
          actions: ["查看", "创建 / 编辑", "删除", "批准", "管理"],
          matrix: [{ resource: "Check resource", grants: [1, 0, 0, 0, 0] }],
          notes: "",
        }),
      }),
    );
    const m = monitor(page);
    await page.goto(origin + "/settings/roles");
    await expect(page.getByRole("heading", { name: "Check role" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("global", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "范围", exact: true })).toHaveCount(0);
    await expect(page.locator(".v3-page-body .demo-chip")).toHaveCount(0);
    for (const sample of ["app=payment", "app=search", "env=staging"]) await expect(page.getByText(sample)).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });
});
