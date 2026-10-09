import { chromium, type FullConfig } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, admin, signIn, type InstanceName } from "./env";

/** One admin session per instance, reused by every test through storageState. */
export default async function globalSetup(config: FullConfig) {
  mkdirSync(resolve(LOCAL, "e2e"), { recursive: true });
  const browser = await chromium.launch(config.projects[0].use.launchOptions);
  for (const name of Object.keys(INSTANCES) as InstanceName[]) {
    const context = await browser.newContext();
    const page = await context.newPage();
    await signIn(page, name, admin(name));
    await context.storageState({ path: statePath(name, "admin") });
    await context.close();
  }
  await browser.close();
}

export function statePath(instance: InstanceName, role: string) {
  return resolve(LOCAL, "e2e", `state-${instance}-${role}.json`);
}
