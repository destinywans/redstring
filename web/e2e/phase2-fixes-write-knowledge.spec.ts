import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, toasts, zh } from "./support/checks";

/**
 * Write-path sweep fixes before round 3 (WAN-434), group H1: what a page shows after a write must be what the server
 * now holds (D63), and a refused write says why in the page's language, not in the server's English. Every read and
 * write is pinned with page.route, so this runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-write-knowledge.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

/* ---------------- PA-233: submitting a runbook for review ---------------- */

const RUNBOOK = "rb-pa233-check";
const runbook = (patch: Record<string, unknown> = {}) => ({
  id: RUNBOOK, title: "提交检查 Runbook", version: "v1", status: "draft", base: "blank", source_kind: "blank", source_ref: "", owner: "检查人",
  scope: "workspace", environment_ids: ["prod"], application_id: "", team_id: "", tags: [], when: "检查用",
  steps: [{ n: 1, kind: "check", title: "核对", body: "x", readonly: true }],
  reviewers: [], versions: [], usage: { refs: null, resolve_rate: null, weekly: null, highlight: null }, incidents: null, last_verified: null,
  complete_v3_dto: false, partial: true, unknown_fields: [],
  ...patch,
});
const IN_REVIEW = runbook({ status: "review", reviewers: [{ name: "复核甲", state: "pending" }] });
const stateBadge = (page: Page) => page.locator(".v3-row", { has: page.locator("h1", { hasText: "提交检查 Runbook" }) }).locator(".v3-badge");
const reviewSection = (page: Page) => page.locator(".v3-section", { has: page.locator("h3", { hasText: /^复核$/ }) });

test.describe("runbook submit for review re-reads the runbook (PA-233)", () => {
  test("after the server accepts, the badge, the button and the reviewers say in review", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    let submitted = false;
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, submitted ? IN_REVIEW : runbook()));
    await page.route(at(`/runbooks/${RUNBOOK}/submit`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      submitted = true;
      return json(r, 200, { ok: true, status: "review" });
    });
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    await expect(stateBadge(page)).toHaveText("v1 草稿 · 基于 blank", { timeout: 20_000 });
    const submit = page.getByRole("button", { name: "提交复核" });
    await submit.click();
    await expect.poll(() => toasts(page)).toContain("已提交复核");
    await expect(stateBadge(page)).toHaveText("v1 待复核 · 基于 blank");
    await expect(submit).toBeDisabled();
    await expect(reviewSection(page).locator(".v3-row", { hasText: "复核甲" })).toContainText("待复核");
    expect(m.pageErrors).toEqual([]);
  });

  test("when the re-read fails, the badge still follows the server's answer", async ({ page }) => {
    await page.addInitScript(zh);
    let submitted = false;
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => (submitted ? json(r, 503, { error: { code: "knowledge_unavailable", message: "check" } }) : json(r, 200, runbook())));
    await page.route(at(`/runbooks/${RUNBOOK}/submit`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      submitted = true;
      return json(r, 200, { ok: true, status: "review" });
    });
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    await expect(stateBadge(page)).toHaveText("v1 草稿 · 基于 blank", { timeout: 20_000 });
    await page.getByRole("button", { name: "提交复核" }).click();
    await expect(stateBadge(page)).toHaveText("v1 待复核 · 基于 blank");
    await expect(page.getByRole("button", { name: "提交复核" })).toBeDisabled();
  });

  test("a submit refused because the runbook is no longer a draft says so in Chinese and re-reads it", async ({ page }) => {
    await page.addInitScript(zh);
    let refused = false;
    // Someone else submitted it meanwhile: the server refuses, and a fresh read says in review.
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, refused ? IN_REVIEW : runbook()));
    await page.route(at(`/runbooks/${RUNBOOK}/submit`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      refused = true;
      return json(r, 409, { error: { code: "invalid_state", message: "Only drafts can be submitted" } });
    });
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    await expect(stateBadge(page)).toHaveText("v1 草稿 · 基于 blank", { timeout: 20_000 });
    await page.getByRole("button", { name: "提交复核" }).click();
    await expect.poll(() => toasts(page)).toContainEqual(expect.stringContaining("已经不是草稿"));
    expect((await toasts(page)).join("\n")).not.toContain("Only drafts can be submitted");
    await expect(stateBadge(page)).toHaveText("v1 待复核 · 基于 blank");
    await expect(page.getByRole("button", { name: "提交复核" })).toBeDisabled();
  });

  test("other refused submits don't show the server's English either", async ({ page }) => {
    await page.addInitScript(zh);
    const answers = [
      [403, "forbidden", "Only the maintainer can submit this draft"],
      [409, "conflict", "Runbook changed; reload and retry"],
      [503, "knowledge_unavailable", "Knowledge operation failed"],
    ] as const;
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, runbook()));
    for (const [status, code, message] of answers) {
      await page.route(at(`/runbooks/${RUNBOOK}/submit`), (r) => (r.request().method() === "POST" ? json(r, status, { error: { code, message } }) : r.fallback()));
      // A fresh load per answer, so only this answer's toast is on screen.
      await page.goto(origin + `/runbooks/${RUNBOOK}`);
      await expect(stateBadge(page)).toHaveText("v1 草稿 · 基于 blank", { timeout: 20_000 });
      await page.getByRole("button", { name: "提交复核" }).click();
      await expect.poll(() => toasts(page), code).toHaveLength(1);
      expect((await toasts(page))[0], code).not.toContain(message);
      await page.unroute(at(`/runbooks/${RUNBOOK}/submit`));
    }
  });
});
