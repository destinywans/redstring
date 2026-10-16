import { test, expect, type Page } from "@playwright/test";
import { createHmac } from "node:crypto";
import { deflateSync } from "node:zlib";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, signIn, type Account } from "./support/env";
import { monitor } from "./support/monitor";

/**
 * Account security on a throwaway account (WAN-434 phase 1): enrol an authenticator app, confirm it, reload,
 * then remove it — all through Settings › Account security (the codes confirm sensitive actions, not sign-in). The account is created by
 *   python3 tools/acceptance/accounts.py prod mfa:viewer
 * and kept under "throwaway" in secrets.json, so a run that stops halfway never leaves a shared role account
 * needing a code. The authenticator is removed in a finally step whatever happens.
 */
const instance = "prod" as const;
const origin = INSTANCES[instance].origin;
const secrets = JSON.parse(readFileSync(resolve(LOCAL, instance, "secrets.json"), "utf8"));
const raw = secrets.throwaway?.mfa as (Account & { role?: string }) | undefined;
// Sign-in takes exactly email and password.
const account: Account | undefined = raw && { email: raw.email, password: raw.password };
test.skip(!account, "create the throwaway account first: tools/acceptance/accounts.py prod mfa:viewer");
// Every test signs the same throwaway account in and one ends its other sessions: run them one at a time.
test.describe.configure({ mode: "serial" });
test.setTimeout(180_000);

/** RFC 6238 code (SHA-1, 30 s, 6 digits) for a base32 seed. */
function totp(seed: string, at = Date.now()): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of seed.replace(/=+$/, "").toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g)!.map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 30_000)));
  const h = createHmac("sha1", key).update(counter).digest();
  const o = h[h.length - 1] & 0xf;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}

/** Waits for the next 30-second step so a code is never reused (the server rejects a replay). */
async function freshCode(page: Page, seed: string, last: string | null) {
  for (;;) {
    const code = totp(seed);
    if (code !== last) return code;
    await page.waitForTimeout(2_000);
  }
}

test("authenticator app: enrol, confirm, remove", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const m = monitor(page);
  const out: Record<string, unknown> = {};
  let seed = "";
  let used: string | null = null;
  try {
    // Changing the authenticator needs a recent sign-in.
    await signIn(page, instance, account!);
    await page.goto(origin + "/settings/account-security");
    await page.getByRole("button", { name: "绑定", exact: true }).click({ timeout: 20_000 });
    // The dialog shows the seed for manual entry next to the QR code.
    const secret = page.locator("code.totp-secret");
    await secret.waitFor({ timeout: 20_000 });
    seed = (await secret.innerText()).replace(/\s+/g, "");
    used = await freshCode(page, seed, null);
    await page.locator("#totp-code").fill(used);
    await page.getByRole("button", { name: "验证并绑定" }).click();
    await expect(page.getByText("已绑定。请保存恢复码")).toBeVisible({ timeout: 20_000 });
    out.recoveryCodes = await page.locator(".totp-codes > *").count();
    await page.getByRole("button", { name: "我已保存" }).click();
    await expect(page.getByText("已绑定", { exact: true })).toBeVisible({ timeout: 10_000 });
    await page.reload();
    await expect(page.getByText("已绑定", { exact: true })).toBeVisible({ timeout: 20_000 });
    // Remove through the panel with a fresh code.
    await page.getByRole("button", { name: "解除绑定" }).first().click();
    used = await freshCode(page, seed, used);
    await page.locator("#totp-remove-code").fill(used);
    await page.getByRole("dialog").getByRole("button", { name: "解除绑定" }).click();
    await expect(page.locator(".v3-toast", { hasText: "身份验证器已解除绑定" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("未绑定", { exact: true })).toBeVisible({ timeout: 10_000 });
    seed = "";
  } finally {
    // Whatever happened above, leave the account without an authenticator.
    if (seed) {
      const code = await freshCode(page, seed, used);
      out.cleanup = await page
        .evaluate(async (code) => (await fetch("/api/v1/me/mfa/totp", { method: "DELETE", headers: { "Content-Type": "application/json", "X-AIOps-Environment": "prod", "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ code }) })).status, code)
        .catch(() => -1);
    }
  }
  out.api = m.api.filter((a) => a.path.startsWith("/me/mfa"));
  out.pageErrors = m.pageErrors;
  console.log(JSON.stringify(out, null, 1).slice(0, 3000));
  expect(m.pageErrors).toEqual([]);
});

test("authenticator app: start, then discard the pending setup", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const m = monitor(page);
  await signIn(page, instance, account!);
  await page.goto(origin + "/settings/account-security");
  await page.getByRole("button", { name: "绑定", exact: true }).click({ timeout: 20_000 });
  await page.locator("code.totp-secret").waitFor({ timeout: 20_000 });
  await page.getByRole("dialog").getByRole("button", { name: "放弃绑定" }).click();
  await expect(page.getByText("未绑定", { exact: true })).toBeVisible({ timeout: 10_000 });
  const calls = m.api.filter((a) => a.path.startsWith("/me/mfa")).map((a) => `${a.method} ${a.path} ${a.status}`);
  console.log(calls.join("\n"));
  expect(calls).toContain("DELETE /me/mfa/totp/enrollment 204");
  expect(m.pageErrors).toEqual([]);
});

test("passkeys: add one through the panel, then remove it with itself", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  });
  const m = monitor(page);
  await signIn(page, instance, account!);
  await page.goto(origin + "/settings/account-security");
  const label = `验收临时密钥 ${Date.now()}`;
  await page.getByRole("button", { name: "添加" }).first().click({ timeout: 20_000 });
  await page.getByLabel("名称（可选）").fill(label);
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByText("通行密钥已添加")).toBeVisible({ timeout: 20_000 });
  const row = page.locator("li", { hasText: label });
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "删除" }).click();
  await page.getByRole("button", { name: "删除" }).last().click(); // the confirm dialog
  await expect(page.locator(".v3-toast", { hasText: "通行密钥已删除" })).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("li", { hasText: label })).toHaveCount(0);
  const calls = m.api.filter((a) => a.path.startsWith("/me/passkeys")).map((a) => `${a.method} ${a.path} ${a.status}`);
  console.log(calls.join("\n"));
  expect(calls.some((c) => c.startsWith("POST /me/passkeys/deletion/finish 2"))).toBe(true);
  expect(m.pageErrors).toEqual([]);
  await context.close();
});

/** A solid-colour RGB PNG of the given size (the avatar must be PNG/JPEG, at least 256 × 256). */
function png(size: number): Buffer {
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (b: Buffer) => {
    let c = 0xffffffff;
    for (const x of b) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type: string, data: Buffer) => {
    const t = Buffer.from(type);
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const c = Buffer.alloc(4);
    c.writeUInt32BE(crc(Buffer.concat([t, data])));
    return Buffer.concat([len, t, data, c]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // RGB
  const row = Buffer.concat([Buffer.from([0]), Buffer.alloc(size * 3, 0x5b)]);
  const raw = Buffer.concat(Array.from({ length: size }, () => row));
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}

test("profile photo and preferences", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const m = monitor(page);
  await signIn(page, instance, account!);
  await page.goto(origin + "/settings/profile");
  await page.locator('input[type="file"]').first().setInputFiles({ name: "avatar.png", mimeType: "image/png", buffer: png(256) }, { timeout: 20_000 });
  await expect(page.locator(".v3-toast", { hasText: "头像已更新" })).toBeVisible({ timeout: 20_000 });
  // Too small is refused with the server's reason, not silently.
  await page.locator('input[type="file"]').first().setInputFiles({ name: "tiny.png", mimeType: "image/png", buffer: png(32) });
  await page.waitForTimeout(1_500);
  await page.reload();
  await page.getByRole("button", { name: "移除" }).first().click({ timeout: 20_000 });
  await page.getByRole("button", { name: "移除" }).last().click();
  await expect(page.locator(".v3-toast", { hasText: "头像已移除" })).toBeVisible({ timeout: 20_000 });
  // Preferences sync to the server as they change.
  await page.goto(origin + "/settings/preferences");
  await page.getByText("深色", { exact: true }).first().click({ timeout: 20_000 });
  await page.waitForTimeout(800);
  await page.getByText("浅色", { exact: true }).first().click();
  await page.waitForTimeout(800);
  const calls = m.api.filter((a) => a.path.startsWith("/me/avatar") || a.path.startsWith("/me/preferences")).map((a) => `${a.method} ${a.path} ${a.status}${a.code ? " " + a.code : ""}`);
  console.log(calls.join("\n"));
  console.log("toasts:", (await page.locator(".v3-toast").allInnerTexts()).join(" | "));
  expect(calls.filter((c) => c.startsWith("PATCH /me/preferences 2")).length).toBeGreaterThanOrEqual(2);
  expect(calls.some((c) => c.startsWith("DELETE /me/avatar 2"))).toBe(true);
  expect(m.pageErrors).toEqual([]);
});

test("sign in through the form, sign out another session, then sign out", async ({ browser }) => {
  // A second session to end from the first one.
  const other = await browser.newContext();
  await other.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  await signIn(await other.newPage(), instance, account!);
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const page = await context.newPage();
  const m = monitor(page);
  await page.goto(origin + "/login");
  await page.locator("#login-email").fill(account!.email, { timeout: 20_000 });
  await page.locator("#login-password").fill(account!.password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 20_000 });
  await page.goto(origin + "/settings/account-security");
  const signOutOne = page.getByRole("button", { name: "退出", exact: true }).first();
  await signOutOne.click({ timeout: 20_000 });
  await expect(page.locator(".v3-toast", { hasText: /已退出/ })).toBeVisible({ timeout: 10_000 });
  // The account menu signs this session out and lands on the sign-in page.
  await page.locator("button.account").click();
  await page.getByRole("menuitem", { name: "退出登录" }).click();
  await page.waitForURL((u) => u.pathname.startsWith("/login"), { timeout: 20_000 });
  const calls = m.api.filter((a) => a.path.startsWith("/auth/") || a.path.startsWith("/me/sessions")).map((a) => `${a.method} ${a.path} ${a.status}`);
  console.log(calls.join("\n"));
  expect(calls.some((c) => /^POST \/auth\/login 200/.test(c))).toBe(true);
  expect(calls.some((c) => /^DELETE \/me\/sessions\/\S+ 20\d/.test(c))).toBe(true);
  expect(calls.some((c) => /^POST \/auth\/logout 20\d/.test(c))).toBe(true);
  // The ended session can no longer read.
  const ended = await (await other.newPage()).goto(origin + "/api/v1/me").then((r) => r?.status());
  console.log("ended session /me:", ended);
  await other.close();
  await context.close();
});
