import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, patchRead, zh } from "./support/checks";

/**
 * PA-231 (WAN-590): routes authorize home, traces, logs, models and workspace, and the four built-in roles hold those
 * grants, but the role matrix had no rows for them, so a custom role could never be given them. The permission gates
 * also named the workspace module differently from the matrix row an admin has to turn on.
 *
 * The built-in role checks read the real role detail (GET only) with the role list pinned to that one role; every
 * other read is pinned with page.route. Nothing reaches the server as a write: role PUTs are answered here.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-rest-roles.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

const ROUTE_ROWS = ["总览", "链路", "日志", "模型", "工作区设置"];
const ACTIONS = ["查看", "创建 / 编辑", "删除", "批准", "管理"];
const cell = (page: Page, resource: string, action: string) => page.getByRole("button", { name: `${resource} · ${action}`, exact: true });

/** Role writes are answered here and recorded, never sent to the server. */
async function holdRoleWrites(page: Page) {
  const writes: string[] = [];
  await page.route(
    (url) => url.pathname.startsWith("/api/v1/roles"),
    (route) => {
      if (route.request().method() === "GET") return route.fallback();
      writes.push(`${route.request().method()} ${new URL(route.request().url()).pathname}`);
      return route.fulfill({ status: 200, contentType: "application/json", headers: { "X-Role-Matrix-Revision": "2" }, body: JSON.stringify({ ok: true }) });
    },
  );
  return writes;
}

/** Opens the roles page on one real built-in role: the list is pinned to it, its detail is the server's. */
async function openBuiltInRole(page: Page, id: string) {
  await page.addInitScript(zh);
  await page.goto(origin + "/healthz");
  const writes = await holdRoleWrites(page);
  await patchRead(page, "/roles", (real: { items?: { id: string }[] }) => ({ ...real, items: (real.items ?? []).filter((x) => x.id === id) }));
  const m = monitor(page);
  await page.goto(origin + "/settings/roles");
  await expect(cell(page, "链路", "查看")).toBeVisible({ timeout: 20_000 });
  return { writes, m };
}

test.describe("route modules in the role matrix (PA-231)", () => {
  test("a built-in operator shows the route-module grants it already holds", async ({ page }) => {
    const { writes, m } = await openBuiltInRole(page, "operator");
    for (const row of ROUTE_ROWS) await expect(cell(page, row, "查看")).toHaveAttribute("aria-pressed", "true");
    for (const row of ROUTE_ROWS) await expect(cell(page, row, "批准")).toHaveAttribute("aria-pressed", "false");
    // The page doesn't offer to save a matrix it only read.
    await expect(page.getByRole("button", { name: "保存权限" })).toHaveCount(0);
    expect(writes).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  test("the built-in admin shows view, change, delete and manage for each route module, never approve", async ({ page }) => {
    const { writes, m } = await openBuiltInRole(page, "admin");
    for (const row of ROUTE_ROWS) {
      for (const action of ACTIONS) await expect(cell(page, row, action)).toHaveAttribute("aria-pressed", action === "批准" ? "false" : "true");
    }
    expect(writes).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  // Control: the page sends the new rows back with the rest, so a custom role can be given them.
  test("a custom role's route-module cell is saved with every other row unchanged", async ({ page }) => {
    await page.addInitScript(zh);
    const resources = ["对话", "画布", "事件与告警", "告警规则与路由", "静默", "写操作提案", "Runbook", "知识来源与检索", "服务目录", "连接器与映射", "Agent 配置与发布", "成员与角色", "审计日志", "资源目录与关系", ...ROUTE_ROWS];
    const matrix = resources.map((resource) => ({ resource, grants: [resource === "事件与告警" ? 1 : 0, 0, 0, 0, 0] }));
    const detail = { id: "custom-pa231", name: "Check role", members: 1, kind: "business", desc: "", member_names: ["Check Member"], scope: ["global"], actions: ACTIONS, matrix, environment_access: ["global"], notes: "" };
    await page.route(at("/roles"), (r) => json(r, 200, { items: [{ id: detail.id, name: detail.name, members: 1, kind: "business" }] }));
    let saved: { matrix?: { resource: string; grants: number[] }[] } | undefined;
    let revision: string | undefined;
    await page.route(at(`/roles/${detail.id}`), (r) => {
      if (r.request().method() !== "GET") {
        saved = r.request().postDataJSON();
        revision = r.request().headers()["if-role-matrix-revision"];
        return r.fulfill({ status: 200, contentType: "application/json", headers: { "X-Role-Matrix-Revision": "2" }, body: JSON.stringify({ ok: true }) });
      }
      return r.fulfill({ status: 200, contentType: "application/json", headers: { "X-Role-Matrix-Revision": "1" }, body: JSON.stringify(detail) });
    });
    await page.goto(origin + "/settings/roles");
    await cell(page, "链路", "查看").click({ timeout: 20_000 });
    await page.getByRole("button", { name: "保存权限" }).click();
    await expect.poll(() => saved?.matrix?.length).toBe(resources.length);
    expect(revision).toBe("1");
    expect(saved!.matrix).toEqual(matrix.map((row) => (row.resource === "链路" ? { resource: "链路", grants: [1, 0, 0, 0, 0] } : row)));
  });
});

type Grants = Record<string, Record<string, Record<string, string>>>;
/** /me without the given "module.action" grants in every environment. */
async function meWithout(page: Page, drop: string[]) {
  await patchRead(page, "/me", (real: { permissions?: Grants }) => {
    const permissions: Grants = {};
    for (const [env, modules] of Object.entries(real.permissions ?? {})) {
      permissions[env] = {};
      for (const [module, actions] of Object.entries(modules ?? {})) permissions[env][module] = Object.fromEntries(Object.entries(actions).filter(([action]) => !drop.includes(`${module}.${action}`)));
    }
    return { ...real, permissions };
  });
}
const gateReason = (page: Page, text: string) => page.locator(".perm-gate", { hasText: text }).locator(".sr-only");

test.describe("permission gates name the matrix row (PA-231)", () => {
  test("saving workspace settings names the 工作区设置 row", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    await meWithout(page, ["workspace.manage"]);
    await page.route((url) => url.pathname === "/api/v1/workspace", (r) => (r.request().method() === "GET" ? r.fallback() : json(r, 409, { error: { code: "check", message: "check: no write" } })));
    await page.goto(origin + "/settings/workspace");
    await expect(page.getByRole("button", { name: "保存", exact: true })).toBeDisabled({ timeout: 20_000 });
    await expect(gateReason(page, "保存")).toHaveText("需要 工作区设置·管理 权限");
  });

  test("English names the Workspace settings row", async ({ page }) => {
    await page.addInitScript(en);
    await page.goto(origin + "/healthz");
    await meWithout(page, ["workspace.manage"]);
    await page.route((url) => url.pathname === "/api/v1/workspace", (r) => (r.request().method() === "GET" ? r.fallback() : json(r, 409, { error: { code: "check", message: "check: no write" } })));
    await page.goto(origin + "/settings/workspace");
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled({ timeout: 20_000 });
    await expect(gateReason(page, "Save")).toHaveText("Needs Workspace settings · Manage permission");
  });
});

/**
 * PA-232 (WAN-591): the 对话与画布 row mapped conversation only, so a role holding conversation showed the combined row
 * granted while canvas routes refused it. The row is split into 对话 and 画布; the real custom role on prod holds
 * conversation.view and no canvas grant (GET only).
 */
test.describe("conversation and canvas are separate matrix rows (PA-232)", () => {
  test("a custom role with conversation only shows 对话 granted and 画布 empty", async ({ page }) => {
    const { writes, m } = await openBuiltInRole(page, "custom-knowledge-reviewer");
    await expect(page.getByRole("button", { name: /^对话与画布 · / })).toHaveCount(0);
    await expect(cell(page, "对话", "查看")).toHaveAttribute("aria-pressed", "true");
    for (const action of ACTIONS) await expect(cell(page, "画布", action)).toHaveAttribute("aria-pressed", "false");
    expect(writes).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });
});
