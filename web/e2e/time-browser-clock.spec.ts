import { test, expect, type Page, type Route } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { read } from "./support/checks";

/**
 * Release acceptance round, time injection T-4 (docs/plans/2026-10-18-release-wrap-up.md §3.1): the browser clock moves
 * forward with page.clock while the page stays open. Front-end display only: each case pins one API answer with
 * page.route (the real answer, patched), never writes, and records what the page says before and after.
 *   npx playwright test -c e2e/playwright.config.ts time-browser-clock.spec.ts
 * Not part of the regression (the file name does not start with phase2-fixes).
 */
const origin = INSTANCES.prod.origin;
const OUT = resolve(LOCAL, "e2e/time-injection");
mkdirSync(OUT, { recursive: true });
const zh = () => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" }));
const at = (path: string) => (url: URL) => url.pathname === "/api/v1" + path;
const record = (name: string, log: unknown) => writeFileSync(resolve(OUT, `t4-${name}.json`), JSON.stringify(log, null, 2));

/** Answer GET path (matched by pathname) with the real answer, patched; the real one is read first, in the page. */
async function patch(page: Page, path: string, edit: (body: any) => any) {
  if (!page.url().startsWith(origin)) await page.goto(origin + "/healthz");
  const real = (await read(page, path)).body;
  const body = JSON.stringify(edit(real));
  const pathname = path.split("?")[0];
  await page.route(at(pathname), (r: Route) => (r.request().method() === "GET" ? r.fulfill({ status: 200, contentType: "application/json", body }) : r.fallback()));
}

const main = (page: Page) => page.locator("main").innerText().catch(() => page.locator("#root").innerText());

test.describe("as approver1", () => {
  test.use({ storageState: statePath("prod", "approver1"), viewport: { width: 1440, height: 1000 } });

  test("vote validity (my_vote.expires_at) after the clock passes it", async ({ page }) => {
    const id = readFileSync(resolve(LOCAL, "e2e/golden/rollback-proposal.txt"), "utf8").trim();
    const t0 = Date.now();
    await page.clock.install({ time: t0 });
    await page.addInitScript(zh);
    const m = monitor(page);
    await patch(page, `/proposals/${id}`, (p) => ({
        ...p,
        status: "pending",
        decision: null,
        execution: null,
        proposed_by: "someone-else",
        proposed_by_name: "检查用发起人",
        my_vote: { verified_at: new Date(t0 - 60_000).toISOString(), expires_at: new Date(t0 + 120_000).toISOString() },
      }));
    await page.goto(`${origin}/review/${id}`);
    await page.waitForTimeout(4000);
    const before = await main(page);
    await page.clock.fastForward("05:00");
    await page.waitForTimeout(1500);
    const after = await main(page);
    await page.screenshot({ path: resolve(OUT, "t4-vote-after.png"), fullPage: true });
    const log = {
      proposal: id,
      before_awaiting_you: before.includes("待你复核"),
      after_awaiting_you: after.includes("待你复核"),
      before_head: before.slice(0, 600),
      after_head: after.slice(0, 600),
      pageErrors: m.pageErrors,
    };
    record("vote", log);
    // While the vote counts the page must not ask this approver again; once it lapsed it should (or say to refresh).
    expect.soft(log.before_awaiting_you, "vote still valid: not awaiting this approver").toBe(false);
    expect.soft(log.after_awaiting_you, "vote lapsed: awaiting this approver again").toBe(true);
  });

  test("passkey dialog after five minutes", async ({ page }) => {
    const id = readFileSync(resolve(LOCAL, "e2e/golden/rollback-proposal.txt"), "utf8").trim();
    const t0 = Date.now();
    await page.clock.install({ time: t0 });
    await page.addInitScript(zh);
    // No authenticator answers: the request waits until the browser's own WebAuthn timeout (real time, not page.clock).
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "credentials", {
        value: { get: (o: { signal?: AbortSignal }) => new Promise((_, reject) => o.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))) },
      });
    });
    const m = monitor(page);
    await patch(page, `/proposals/${id}`, (p) => ({ ...p, status: "pending", decision: null, execution: null, proposed_by: "someone-else", proposed_by_name: "检查用发起人", my_vote: null }));
    // Fixed options, so nothing reaches the server's approval path.
    await page.route(at(`/proposals/${id}/approve/options`), (r) =>
      r.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ challenge_id: "t4-check", action_digest: "t4c0ffee00000000000000000000beef", options: { publicKey: { challenge: "dDQtY2hlY2s", timeout: 300000, rpId: "prod.redstring.test", allowCredentials: [], userVerification: "required" } } }),
      }),
    );
    await page.goto(`${origin}/review/${id}`);
    const boxes = page.locator("fieldset.checklist input[type=checkbox]");
    await boxes.first().waitFor({ timeout: 20_000 });
    for (const box of await boxes.all()) await box.check();
    await page.getByRole("button", { name: "用通行密钥批准" }).click();
    await page.waitForTimeout(3000);
    const before = await page.getByRole("dialog").last().innerText().catch(() => "");
    await page.clock.fastForward("06:00");
    await page.waitForTimeout(1500);
    const after = await page.getByRole("dialog").last().innerText().catch(() => "");
    await page.screenshot({ path: resolve(OUT, "t4-passkey-after.png"), fullPage: true });
    record("passkey", { proposal: id, before, after, api: m.api.filter((c) => c.method !== "GET"), pageErrors: m.pageErrors });
  });
});

test.describe("as admin", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("silence remaining time after the window ends", async ({ page }) => {
    const t0 = Date.now();
    await page.clock.install({ time: t0 });
    await page.addInitScript(zh);
    const m = monitor(page);
    await patch(page, "/silences?state=active", (b) => {
        const one = {
          id: "sil-t4-check",
          matchers: [{ k: "alertname", v: "T4Check" }],
          state: "active",
          starts_at: new Date(t0 - 27 * 60_000).toISOString(),
          ends_at: new Date(t0 + 3 * 60_000).toISOString(),
          remaining: { pct: 10, text: "剩余 3 分钟" },
          affects: 0,
          creator: "检查用",
          reason: "T-4 浏览器时钟检查",
        };
        return { ...b, items: [one, ...(b.items ?? [])], counts: { ...(b.counts ?? {}), active: (b.counts?.active ?? 0) + 1 } };
      });
    await page.goto(`${origin}/silences`);
    await expect(page.getByText("T4Check").first()).toBeVisible({ timeout: 20_000 });
    const row = () => page.locator("tr", { hasText: "T4Check" }).first().innerText().catch(() => "");
    const before = await row();
    await page.clock.fastForward("10:00");
    await page.waitForTimeout(1500);
    const after = await row();
    await page.screenshot({ path: resolve(OUT, "t4-silence-after.png"), fullPage: true });
    record("silence", { before, after, pageErrors: m.pageErrors });
  });

  test("inbox today / yesterday across midnight", async ({ page }) => {
    // 23:55 today in the browser's time zone; the item arrived at 23:50.
    const d = new Date();
    d.setHours(23, 55, 0, 0);
    const t0 = d.getTime();
    await page.clock.install({ time: t0 });
    await page.addInitScript(zh);
    const m = monitor(page);
    let title = "";
    await patch(page, "/inbox", (b) => {
        const items = b.items ?? [];
        if (!items.length) return b;
        title = items[0].title ?? "";
        items[0] = { ...items[0], at: new Date(t0 - 5 * 60_000).toISOString() };
        return { ...b, items };
      });
    await page.goto(`${origin}/inbox`);
    await page.waitForTimeout(5000);
    const side = () => page.locator(".v3-split .side").first().innerText().catch(() => "");
    const before = await side();
    await page.clock.fastForward("15:00");
    await page.waitForTimeout(1500);
    const after = await side();
    await page.screenshot({ path: resolve(OUT, "t4-inbox-after.png"), fullPage: true });
    // Section headers in order with the first item's title, before and after midnight.
    const order = (t: string) => t.split("\n").filter((l) => ["今天", "昨天", "更早"].includes(l.trim()) || (title && l.includes(title))).slice(0, 6);
    record("inbox", { title, before: order(before), after: order(after), pageErrors: m.pageErrors });
  });
});
