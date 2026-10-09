import { test, expect, type Browser, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";

/**
 * Golden path · two-person passkey approval of a pending silence proposal (prod).
 * GOLDEN_PROPOSAL=<id> python3 tools/acceptance/... creates it beforehand (operator, POST /silences).
 * Each approver registers a passkey through Account security with a CDP virtual authenticator,
 * then approves on the review page. Writes evidence to .local/acceptance/e2e/golden/.
 */
const proposal = process.env.GOLDEN_PROPOSAL ?? "";
const origin = INSTANCES.prod.origin;
const OUT = resolve(LOCAL, "e2e/golden");
mkdirSync(OUT, { recursive: true });
test.setTimeout(180_000);
test.skip(!proposal, "set GOLDEN_PROPOSAL to a pending proposal id");

async function approverPage(browser: Browser, role: string) {
  const context = await browser.newContext({ storageState: statePath("prod", role), viewport: { width: 1440, height: 1000 } });
  await context.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  });
  return { context, page };
}

/**
 * Registers a passkey with the page's virtual authenticator. GOLDEN_UI_REGISTER=1 uses the Account
 * security panel; otherwise the same three API calls run in-page (the panel is covered separately).
 */
async function registerPasskey(page: Page, label: string) {
  if (process.env.GOLDEN_UI_REGISTER) {
    await page.goto(origin + "/settings/account-security");
    await page.getByRole("button", { name: "添加" }).first().click();
    await page.getByLabel("名称（可选）").fill(label);
    await page.getByRole("button", { name: "继续" }).click();
    await expect(page.getByText("通行密钥已添加")).toBeVisible({ timeout: 20_000 });
    return;
  }
  await page.goto(origin + "/");
  const status = await page.evaluate(async (label) => {
    const env = { "Content-Type": "application/json", "X-AIOps-Environment": "prod" };
    const b64 = (s: string) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=")), (c) => c.charCodeAt(0));
    const url = (b: ArrayBuffer) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    const res = await fetch("/api/v1/me/passkeys/registration/options", { method: "POST", headers: { "X-AIOps-Environment": "prod", "Idempotency-Key": crypto.randomUUID() } });
    const o = await res.json();
    if (!o.options) return `${res.status} ${JSON.stringify(o)}`;
    const pk = o.options.publicKey;
    const cred = (await navigator.credentials.create({ publicKey: { ...pk, challenge: b64(pk.challenge), user: { ...pk.user, id: b64(pk.user.id) },
      excludeCredentials: (pk.excludeCredentials ?? []).map((c: { id: string }) => ({ ...c, id: b64(c.id) })) } })) as PublicKeyCredential;
    const r = cred.response as AuthenticatorAttestationResponse;
    const credential = { id: cred.id, rawId: url(cred.rawId), type: cred.type, clientExtensionResults: cred.getClientExtensionResults(),
      response: { attestationObject: url(r.attestationObject), clientDataJSON: url(r.clientDataJSON), transports: r.getTransports() } };
    const f = await fetch("/api/v1/me/passkeys/registration/finish", { method: "POST", headers: { ...env, "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ challenge_id: o.id, credential, label }) });
    return f.status < 300 ? "ok" : `${f.status} ${await f.text()}`;
  }, label);
  expect(status).toBe("ok");
}

async function approve(page: Page, expected: RegExp, shot: string) {
  await page.goto(origin + "/review/" + proposal);
  const boxes = page.locator("fieldset.checklist input[type=checkbox]");
  await boxes.first().waitFor({ timeout: 20_000 });
  for (const box of await boxes.all()) await box.check();
  await page.getByRole("button", { name: "用通行密钥批准" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(expected)).toBeVisible({ timeout: 30_000 });
  await page.screenshot({ path: resolve(OUT, shot), fullPage: true });
  return (await dialog.innerText()).trim();
}

test("silence proposal: two approvers with passkeys", async ({ browser }) => {
  const results: Record<string, unknown> = { proposal };
  for (const [role, expected] of [
    ["approver1", /还需要 1 位审批人/],
    ["approver2", /已批准，执行器开始执行/],
  ] as const) {
    const { context, page } = await approverPage(browser, role);
    const m = monitor(page);
    await registerPasskey(page, `验收密钥 ${role}`);
    results[role] = await approve(page, expected, `approval-${role}.png`);
    results[`${role}-api`] = m.api.filter((a) => a.path?.includes("/approve") || a.path?.includes("/passkeys"));
    expect.soft(m.pageErrors).toEqual([]);
    await context.close();
  }
  writeFileSync(resolve(OUT, "approval.json"), JSON.stringify(results, null, 2));
});
