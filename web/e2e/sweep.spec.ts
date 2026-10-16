import { test } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, type InstanceName } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";

/**
 * Coverage sweep (WAN-434 phase 1): run one scripted UI scenario on a real instance and record what the
 * user saw, so a path is judged on the page as well as on the API.
 *
 *   SWEEP_FILE=/abs/scenario.json npx playwright test -c e2e/playwright.config.ts sweep.spec.ts
 *
 * Scenario JSON: { "name", "instance"?: "prod", "role"?: "operator", "locale"?: "zh-CN", "theme"?: "light", "steps": [...] }
 * Steps, run in order; the first failing step stops the run (the report still says which):
 *   { "goto": "/route" }                      open a route and wait for the page to finish loading
 *   { "click": "visible text" }               click the first element with exactly this text
 *   { "clickRole": { "role": "button", "name": "保存模板" } }
 *   { "clickSelector": "css" }                click the first match
 *   { "fill": { "label": "模板名称", "value": "x" } }   fill an input by its accessible label
 *   { "waitText": "text", "timeout"?: ms }     wait until the text is visible
 *   { "wait": ms }
 *   { "shot": "label" }                       full-page screenshot
 *   { "text": "css" }                         record the inner text of the first match
 * Writes .local/acceptance/e2e/sweep/<name>.json: every /api/v1 call, toasts seen, page errors, console
 * errors and the step log. Screenshots go to .local/acceptance/e2e/sweep/<name>-<label>.png.
 */
const file = process.env.SWEEP_FILE;
const scenario = file ? JSON.parse(readFileSync(file, "utf8")) : null;
const OUT = resolve(LOCAL, "e2e/sweep");
mkdirSync(OUT, { recursive: true });

type Step = Record<string, unknown>;

if (scenario) {
  const instance = (scenario.instance ?? "prod") as InstanceName;
  const role = scenario.role ?? "admin";
  test.use({ storageState: statePath(instance, role), viewport: { width: 1440, height: 1000 } });

  test(`sweep ${scenario.name}`, async ({ page }) => {
    test.setTimeout(10 * 60_000);
    await page.addInitScript((c) => localStorage.setItem("aiops-v3-preferences", JSON.stringify(c)), {
      locale: scenario.locale ?? "zh-CN",
      theme: scenario.theme ?? "light",
    });
    const m = monitor(page);
    const toasts = new Set<string>();
    const log: { step: Step; ok: boolean; note?: string }[] = [];
    const texts: Record<string, string> = {};
    const collectToasts = async () => {
      for (const t of await page.locator(".v3-toast").allInnerTexts().catch(() => [])) toasts.add(t.trim());
    };
    const loaded = async () => {
      await page.waitForFunction(() => !document.getElementById("boot") && !document.querySelector("[data-loading]"), undefined, { timeout: 20_000 }).catch(() => undefined);
      await m.quiet();
      await collectToasts();
    };
    let failed: string | null = null;
    for (const step of scenario.steps as Step[]) {
      try {
        if (step.goto) {
          await page.goto(INSTANCES[instance].origin + String(step.goto));
          await loaded();
        } else if (step.click) {
          await page.getByText(String(step.click), { exact: true }).first().click({ timeout: 10_000 });
          await loaded();
        } else if (step.clickRole) {
          const r = step.clickRole as { role: Parameters<typeof page.getByRole>[0]; name: string };
          await page.getByRole(r.role, { name: r.name }).first().click({ timeout: 10_000 });
          await loaded();
        } else if (step.clickSelector) {
          await page.locator(String(step.clickSelector)).first().click({ timeout: 10_000 });
          await loaded();
        } else if (step.fill) {
          const f = step.fill as { label: string; value: string };
          await page.getByLabel(f.label).first().fill(f.value, { timeout: 10_000 });
        } else if (step.waitText) {
          await page.getByText(String(step.waitText)).first().waitFor({ state: "visible", timeout: Number(step.timeout ?? 15_000) });
          await collectToasts();
        } else if (step.wait) {
          await page.waitForTimeout(Number(step.wait));
          await collectToasts();
        } else if (step.shot) {
          await collectToasts();
          await page.screenshot({ path: resolve(OUT, `${scenario.name}-${step.shot}.png`), fullPage: true });
        } else if (step.text) {
          texts[String(step.text)] = (await page.locator(String(step.text)).first().innerText({ timeout: 5_000 })).slice(0, 2000);
        }
        log.push({ step, ok: true });
      } catch (e) {
        failed = `${JSON.stringify(step)}: ${String(e).split("\n")[0]}`;
        log.push({ step, ok: false, note: failed });
        await page.screenshot({ path: resolve(OUT, `${scenario.name}-failed.png`), fullPage: true }).catch(() => undefined);
        break;
      }
    }
    await m.settle();
    await collectToasts();
    writeFileSync(
      resolve(OUT, `${scenario.name}.json`),
      JSON.stringify({ name: scenario.name, instance, role, at: new Date().toISOString(), failed, log, toasts: [...toasts], texts,
        api: m.api, pageErrors: m.pageErrors, consoleErrors: m.consoleErrors }, null, 1),
    );
  });
}
