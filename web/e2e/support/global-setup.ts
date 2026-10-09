import { chromium, type FullConfig } from "@playwright/test";
import { existsSync, mkdirSync } from "node:fs";
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
      if (existsSync(saved)) {
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

export function statePath(instance: InstanceName, role: string) {
  return resolve(LOCAL, "e2e", `state-${instance}-${role}.json`);
}
