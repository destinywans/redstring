import { readFileSync, existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { Page } from "@playwright/test";

/** Acceptance instances started by tools/acceptance/acceptance.py; secrets stay in .local/acceptance. */
export const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
export const LOCAL = resolve(ROOT, ".local/acceptance");

export type InstanceName = "prod" | "nonprod";
export const INSTANCES: Record<InstanceName, { origin: string; env: string }> = {
  prod: { origin: "https://prod.redstring.test:18443", env: "prod" },
  nonprod: { origin: "https://nonprod.redstring.test:18444", env: "test" },
};

export function spki(): string {
  const file = resolve(LOCAL, "tls/spki.txt");
  if (!existsSync(file)) throw new Error("Run tools/acceptance/acceptance.py tls first");
  return readFileSync(file, "utf8").trim();
}

export type Account = { email: string; password: string };
export function admin(instance: InstanceName): Account {
  const s = JSON.parse(readFileSync(resolve(LOCAL, instance, "secrets.json"), "utf8"));
  return { email: s.admin_email, password: s.admin_password };
}

/** Role accounts created by tools/acceptance/accounts.py (invite → accept → assign); empty when not set up. */
export function roleAccounts(instance: InstanceName): Record<string, Account> {
  const s = JSON.parse(readFileSync(resolve(LOCAL, instance, "secrets.json"), "utf8"));
  const out: Record<string, Account> = {};
  for (const [name, a] of Object.entries((s.accounts ?? {}) as Record<string, Account>)) out[name] = { email: a.email, password: a.password };
  return out;
}

/**
 * Sign in through the API from inside the page (the login page itself is covered by its own spec):
 * the request goes through Chromium's network stack (host-resolver rules, TLS pinning, Secure cookies).
 */
export async function signIn(page: Page, instance: InstanceName, account: Account) {
  await page.goto(INSTANCES[instance].origin + "/healthz");
  const status = await page.evaluate(async (a) => {
    const r = await fetch("/api/v1/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(a) });
    return r.status;
  }, account);
  if (status !== 200) throw new Error(`login failed with HTTP ${status}`);
}
