import { test, expect } from "@playwright/test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { writeFileSync } from "node:fs";

/**
 * Golden path · member invitation on prod: the admin invites (real SMTP relay → acceptance
 * mail sink), the invitee opens /invite from the login page, sets name and password, then
 * signs in through the login page. GOLDEN_INVITEE overrides the address (must be new).
 */
const origin = INSTANCES.prod.origin;
const invitee = process.env.GOLDEN_INVITEE ?? `viewer-${Date.now()}@prod.redstring.test`;
const password = `Accept-${Math.random().toString(36).slice(2)}-pass`;
test.setTimeout(120_000);

function codeFor(address: string, since: number) {
  const dir = resolve(LOCAL, "mail/inbox");
  for (const name of readdirSync(dir).sort().reverse()) {
    const file = resolve(dir, name);
    if (statSync(file).mtimeMs < since) continue;
    const mail = readFileSync(file, "utf8");
    if (!mail.includes(`To: ${address}\r\n`)) continue;
    const found = /^One-time invitation code: (\S+)\r?$/m.exec(mail);
    if (found) return found[1];
  }
  return null;
}

test("invited member joins through /invite and signs in", async ({ browser }) => {
  const admin = await browser.newContext({ storageState: statePath("prod", "admin") });
  const adminPage = await admin.newPage();
  await adminPage.goto(origin + "/healthz");
  const since = Date.now() - 1000;
  const status = await adminPage.evaluate(async (email) => {
    const r = await fetch("/api/v1/members/invitations", { method: "POST", headers: { "Content-Type": "application/json", "X-AIOps-Environment": "prod", "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ email, role: "viewer" }) });
    return r.status;
  }, invitee);
  expect(status).toBe(200);
  await admin.close();
  let code: string | null = null;
  for (let i = 0; i < 20 && !code; i++) {
    code = codeFor(invitee, since);
    if (!code) await new Promise((r) => setTimeout(r, 500));
  }
  expect(code, "invitation mail reached the sink").toBeTruthy();

  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  await context.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const page = await context.newPage();
  await page.goto(origin + "/login");
  await page.getByRole("link", { name: "用邀请码加入" }).click();
  await expect(page).toHaveURL(/\/invite/);
  await page.getByLabel("邀请码").fill(code!);
  await page.getByLabel("姓名").fill("Invited Viewer");
  const secrets = page.locator("input[type=password]");
  await secrets.nth(0).fill(password);
  await secrets.nth(1).fill(password);
  await page.getByRole("button", { name: "创建账号" }).click();
  await expect(page.getByText("账号已创建。")).toBeVisible({ timeout: 20_000 });
  await page.screenshot({ path: resolve(LOCAL, "e2e/golden/invite-accepted.png") });

  const m = monitor(page);
  await page.getByRole("link", { name: "去登录" }).first().click();
  await page.getByLabel("工作邮箱").fill(invitee);
  await page.locator("input[type=password]").fill(password);
  await page.getByRole("button", { name: "登录" }).click();
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 20_000 });
  // D6 (PA-55): inviting as 只读成员 grants read-only access to every environment, so the member
  // lands in the app shell, not on the "no access" page.
  await expect(page.getByRole("link", { name: "总览" }).first()).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("你还没有任何环境的访问权限")).toBeHidden();
  await page.screenshot({ path: resolve(LOCAL, "e2e/golden/invite-first-login.png"), fullPage: true });

  // The admin then grants the operator role through the real access API.
  const admin2 = await browser.newContext({ storageState: statePath("prod", "admin") });
  const ap = await admin2.newPage();
  await ap.goto(origin + "/healthz");
  const grant = await ap.evaluate(async (email) => {
    const h = { "Content-Type": "application/json", "X-AIOps-Environment": "prod" };
    const members = await (await fetch(`/api/v1/members?q=${encodeURIComponent(email)}`, { headers: h })).json();
    const id = members.items.find((x: { email: string }) => x.email === email)?.id;
    const access = await (await fetch(`/api/v1/members/${id}/access`, { headers: h })).json();
    const role = await fetch("/api/v1/roles/operator", { headers: h });
    const revisions: Record<string, string> = {};
    for (const r of access.roles) if (r.origin !== "legacy") revisions[r.id] = String(r.matrix_revision);
    revisions.operator = role.headers.get("X-Role-Matrix-Revision") ?? "";
    const put = await fetch(`/api/v1/members/${id}/access`, { method: "PUT", headers: { ...h, "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ expected_revision: access.revision, role_ids: Object.keys(revisions).sort(), role_revisions: revisions, enabled: true }) });
    return put.status;
  }, invitee);
  expect(grant).toBe(200);
  await admin2.close();

  // An access change revokes the member's sessions (docs/api/v3-member-access.md): back to sign-in.
  await page.reload();
  await page.waitForURL((url) => url.pathname.startsWith("/login"), { timeout: 20_000 });
  await page.getByLabel("工作邮箱").fill(invitee);
  await page.locator("input[type=password]").fill(password);
  await page.getByRole("button", { name: "登录" }).click();
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 20_000 });
  await page.waitForLoadState("networkidle").catch(() => undefined);
  const me = await page.evaluate(async () => { const r = await fetch("/api/v1/me", { headers: { "X-AIOps-Environment": "prod" } }); return { status: r.status, body: await r.text() }; });
  expect(me.status, me.body.slice(0, 300)).toBe(200);
  expect(JSON.parse(me.body).roles).toContain("operator");
  await m.quiet(2000, 15_000);
  await page.screenshot({ path: resolve(LOCAL, "e2e/golden/invite-after-grant.png"), fullPage: true });
  writeFileSync(resolve(LOCAL, "e2e/golden/invite-first-login.json"), JSON.stringify({ invitee, api: m.api, pageErrors: m.pageErrors }, null, 2));
  expect(m.pageErrors).toEqual([]);
  await context.close();
});
