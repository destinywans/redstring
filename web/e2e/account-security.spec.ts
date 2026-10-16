import { test, expect, type Page } from "@playwright/test";
import { createHmac } from "node:crypto";
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
const account: Account | undefined = secrets.throwaway?.mfa;
test.skip(!account, "create the throwaway account first: tools/acceptance/accounts.py prod mfa:viewer");
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
