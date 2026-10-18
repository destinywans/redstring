import { chromium, type FullConfig } from "@playwright/test";
import { existsSync, mkdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, admin, roleAccounts, signIn, type InstanceName } from "./env";

/** One session per instance and account (admin plus role accounts), reused through storageState. */
export default async function globalSetup(config: FullConfig) {
  mkdirSync(resolve(LOCAL, "e2e"), { recursive: true });
  const browser = await chromium.launch(config.projects[0].use.launchOptions);
  for (const name of Object.keys(INSTANCES) as InstanceName[]) {
    for (const [role, account] of [["admin", admin(name)] as const, ...Object.entries(roleAccounts(name))]) {
      // Reuse a saved session while it is still valid; logging in every run trips the login rate limit.
      const saved = statePath(name, role);
      // A session that ends within the next hour would expire in the middle of a long run (PA-203: the 12-hour
      // sessions ran out halfway through a regression and every later page landed on the sign-in page).
      if (existsSync(saved) && !expiresSoon(saved)) {
        const probe = await browser.newContext({ storageState: saved });
        const probePage = await probe.newPage();
        await probePage.goto(INSTANCES[name].origin + "/healthz");
        const ok = await probePage.evaluate(async (env) => (await fetch("/api/v1/me", { headers: { "X-AIOps-Environment": env } })).status === 200, INSTANCES[name].env).catch(() => false);
        await probe.close();
        if (ok) continue;
      }
      const context = await browser.newContext();
      const page = await context.newPage();
      await signIn(page, name, account);
      await context.storageState({ path: statePath(name, role) });
      await context.close();
    }
  }
  await browser.close();
}

/** True when the saved session cookie expires within the hour (or has no expiry to read). */
function expiresSoon(path: string): boolean {
  const state = JSON.parse(readFileSync(path, "utf8")) as { cookies?: { name: string; expires?: number }[] };
  const session = state.cookies?.find((c) => c.name === "aiops_session");
  return !session?.expires || session.expires <= 0 || session.expires * 1000 - Date.now() < 60 * 60 * 1000;
}

export function statePath(instance: InstanceName, role: string) {
  return resolve(LOCAL, "e2e", `state-${instance}-${role}.json`);
}
