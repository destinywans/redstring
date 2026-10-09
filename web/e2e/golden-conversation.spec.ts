import { test, expect } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, type InstanceName } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";

/**
 * Golden path · conversation with the real model (DeepSeek) and read-only tools.
 * GOLDEN_INSTANCE=prod GOLDEN_QUESTION="…" to override. Uses real model calls: run on purpose.
 */
const instance = (process.env.GOLDEN_INSTANCE ?? "prod") as InstanceName;
const question = process.env.GOLDEN_QUESTION ?? "checkout-api 最近 15 分钟的 5xx 错误多吗？先看指标，再看相关日志，给出结论和证据。";
const OUT = resolve(LOCAL, "e2e/golden");
mkdirSync(OUT, { recursive: true });

test.use({ storageState: statePath(instance, "admin"), viewport: { width: 1440, height: 1000 } });
test.setTimeout(240_000);

test("conversation streams a grounded answer", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" })));
  const m = monitor(page);
  const events: string[] = [];
  page.on("response", (r) => {
    if (r.url().includes("/events")) events.push(`${r.status()} ${r.url().split("/api/v1")[1]}`);
  });
  await page.goto(INSTANCES[instance].origin + "/conversations/new");
  const box = page.getByLabel("向 AI 下指令");
  await box.waitFor({ timeout: 20_000 });
  await box.fill(question);
  await box.press("Enter");
  await page.waitForURL(/\/conversations\/[^/]+$/, { timeout: 30_000 });
  const started = Date.now();
  // The reply is done when the composer accepts input again and the stream has gone quiet.
  await m.quiet(4000, 180_000);
  const elapsed = Math.round((Date.now() - started) / 1000);
  await page.screenshot({ path: resolve(OUT, `conversation-${instance}.png`), fullPage: true });
  const text = await page.locator("main").innerText();
  writeFileSync(resolve(OUT, `conversation-${instance}.json`), JSON.stringify({ url: page.url(), elapsed, events, api: m.api, pageErrors: m.pageErrors, consoleErrors: m.consoleErrors, text }, null, 2));
  expect.soft(m.pageErrors).toEqual([]);
  expect(text.length).toBeGreaterThan(200);
});
