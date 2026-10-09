import { test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, type InstanceName } from "./support/env";
import { statePath } from "./support/global-setup";

/**
 * Ad-hoc screenshots for review: SHOT_ROUTES="/a,/b" SHOT_INSTANCE=prod SHOT_LOCALE=zh-CN SHOT_THEME=light,
 * optional SHOT_TAG to prefix the file names (e.g. a fault-injection run) and SHOT_ROLE (default admin).
 * Images go to .local/acceptance/e2e/shots/.
 */
const routes = (process.env.SHOT_ROUTES ?? "").split(",").filter(Boolean);
const instance = (process.env.SHOT_INSTANCE ?? "prod") as InstanceName;
const locale = process.env.SHOT_LOCALE ?? "zh-CN";
const theme = process.env.SHOT_THEME ?? "light";
const tag = process.env.SHOT_TAG ? `${process.env.SHOT_TAG}-` : "";
const role = process.env.SHOT_ROLE ?? "admin";
const OUT = resolve(LOCAL, "e2e/shots");
mkdirSync(OUT, { recursive: true });

test.use({ storageState: statePath(instance, role), viewport: { width: 1440, height: 1000 } });
for (const route of routes) {
  test(`shot ${route}`, async ({ page }) => {
    await page.addInitScript((c) => localStorage.setItem("aiops-v3-preferences", JSON.stringify(c)), { locale, theme });
    await page.goto(INSTANCES[instance].origin + route);
    await page.waitForFunction(() => !document.getElementById("boot") && !document.querySelector("[data-loading]"), undefined, { timeout: 20_000 });
    await page.waitForLoadState("networkidle").catch(() => undefined);
    await page.waitForTimeout(600);
    await page.screenshot({ path: resolve(OUT, `${tag}${instance}-${locale}-${theme}-${route.replace(/[/?=&]+/g, "_") || "home"}.png`), fullPage: true });
  });
}
