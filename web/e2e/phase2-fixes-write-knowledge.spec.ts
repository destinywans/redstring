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

/* ---------------- PA-234: testing a connector ---------------- */

const CONNECTOR = "conn-pa234-check";
type ConnectorState = { status: "ok" | "unverified" | "error"; checked_at: string | null; meta: string };
const connector = (s: ConnectorState) => ({ id: CONNECTOR, brand: "prometheus", name: "测试检查连接器", kind: "指标", category: "observe", tags: [], ...s });
const connectorList = (s: ConnectorState) => ({
  items: [connector(s)],
  counts: { ok: s.status === "ok" ? 1 : 0, unverified: s.status === "unverified" ? 1 : 0, error: s.status === "error" ? 1 : 0 },
  categories: [{ id: "all", label: "全部", count: 1 }],
});
const connectorDetail = (s: ConnectorState) => ({ ...connector(s), scope: "prod", address: "https://check.example.invalid", auth: { text: "无认证" }, services: [], queries: [], tools: [] });
/** Answers the connector list and detail with `state()` as the server holds it now. */
async function pinConnector(page: Page, state: () => ConnectorState) {
  await page.route(at("/connectors"), (r) => (r.request().method() === "GET" ? json(r, 200, connectorList(state())) : r.fallback()));
  await page.route(at(`/connectors/${CONNECTOR}`), (r) => (r.request().method() === "GET" ? json(r, 200, connectorDetail(state())) : r.fallback()));
}
const card = (page: Page) => page.locator(".conn-card", { hasText: "测试检查连接器" });
const aside = (page: Page) => page.locator("aside.aside");
const checkedRow = (page: Page) => aside(page).locator(".v3-kv", { hasText: "检查" });
const PASSED: ConnectorState = { status: "ok", checked_at: "2026-10-01T01:00:00Z", meta: "连接检查通过" };
const FAILED: ConnectorState = { status: "error", checked_at: "2026-10-18T02:30:00Z", meta: "连接检查失败：connection_failed" };

test.describe("testing a connector re-reads its state (PA-234)", () => {
  test("a failed test turns a connected connector into an error everywhere, with a Chinese reason", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    let tested = false;
    await pinConnector(page, () => (tested ? FAILED : PASSED));
    await page.route(at(`/connectors/${CONNECTOR}/test`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      // The server writes status=error and the check time, commits, then answers 503 (internal/v3integrations/service.go).
      tested = true;
      return json(r, 503, { error: { code: "connector_test_failed", message: "Connector connection check failed", details: { reason: "connection_failed", latency_ms: 5000 } } });
    });
    await page.goto(origin + "/integrations");
    await card(page).click({ timeout: 20_000 });
    await expect(aside(page).locator(".v3-badge").first()).toHaveText("已连接");
    const checkedBefore = await checkedRow(page).innerText();
    await aside(page).getByRole("button", { name: "测试连接" }).click();
    await expect.poll(() => toasts(page)).toHaveLength(1);
    const shown = (await toasts(page))[0];
    expect(shown).not.toContain("Connector connection check failed");
    expect(shown).toContain("连接测试没有通过");
    await expect(aside(page).locator(".v3-badge").first()).toHaveText("异常");
    await expect(checkedRow(page)).not.toHaveText(checkedBefore);
    // The list re-reads too: the card's dot, its meta and the counts say error.
    await expect(card(page).locator(".v3-dot.pulse")).toHaveCount(1);
    await expect(card(page)).toContainText("连接检查失败");
    await expect(page.locator(".v3-badge", { hasText: /^异常 1$/ })).toHaveCount(1);
    await expect(page.locator(".v3-badge", { hasText: /^已连接 1$/ })).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("a passed test turns an unverified connector into connected", async ({ page }) => {
    await page.addInitScript(zh);
    let tested = false;
    await pinConnector(page, () => (tested ? { status: "ok", checked_at: "2026-10-18T02:30:00Z", meta: "连接检查通过" } : { status: "unverified", checked_at: null, meta: "尚未检查连接" }));
    await page.route(at(`/connectors/${CONNECTOR}/test`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      tested = true;
      return json(r, 200, { ok: true, latency_ms: 42 });
    });
    await page.goto(origin + "/integrations");
    await card(page).click({ timeout: 20_000 });
    await expect(aside(page).locator(".v3-badge").first()).toHaveText("未验证");
    await expect(checkedRow(page)).toContainText("尚未检查");
    await aside(page).getByRole("button", { name: "测试连接" }).click();
    await expect.poll(() => toasts(page)).toContain("连接正常 · 42 ms");
    await expect(aside(page).locator(".v3-badge").first()).toHaveText("已连接");
    await expect(checkedRow(page)).not.toContainText("尚未检查");
    await expect(card(page)).toContainText("连接检查通过");
    await expect(page.locator(".v3-badge", { hasText: /^已连接 1$/ })).toHaveCount(1);
    await expect(page.locator(".v3-badge", { hasText: /^未验证 1$/ })).toHaveCount(0);
  });

  test("a test the server refuses before probing doesn't show the server's English", async ({ page }) => {
    await page.addInitScript(zh);
    const answers = [
      [409, "connector_disabled", "Complete connector credentials and scope before testing"],
      [503, "credential_not_authorized", "Connector credential has no server-side binding"],
      [503, "credential_unavailable", "Vault credential is unavailable"],
      [422, "invalid_connector", "Connector settings cannot be tested"],
      [409, "connector_changed", "Connector configuration changed during connection check"],
      [503, "storage_unavailable", "Connector storage is unavailable"],
    ] as const;
    await pinConnector(page, () => PASSED);
    for (const [status, code, message] of answers) {
      await page.route(at(`/connectors/${CONNECTOR}/test`), (r) => (r.request().method() === "POST" ? json(r, status, { error: { code, message } }) : r.fallback()));
      // A fresh load per answer, so only this answer's toast is on screen.
      await page.goto(origin + "/integrations");
      await card(page).click({ timeout: 20_000 });
      await aside(page).getByRole("button", { name: "测试连接" }).click();
      await expect.poll(() => toasts(page), code).toHaveLength(1);
      expect((await toasts(page))[0], code).not.toContain(message);
      await page.unroute(at(`/connectors/${CONNECTOR}/test`));
    }
  });
});

/* ---------------- PA-243: saving a new runbook as a draft ---------------- */

const SAVED = "rb-pa243-check";

test.describe("saving a new runbook's draft doesn't leave a second one to create (PA-243)", () => {
  test("after the draft is saved the page is that runbook's detail, and nothing else is created", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    let created = 0;
    await page.route(at("/runbooks"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      created++;
      return json(r, 201, { id: SAVED });
    });
    await page.route(at(`/runbooks/${SAVED}`), (r) => json(r, 200, { ...runbook(), id: SAVED, title: "检查用：保存后的草稿" }));
    await page.goto(origin + "/runbooks/new?source=blank");
    await page.getByLabel("标题").fill("检查用：保存后的草稿", { timeout: 20_000 });
    await page.getByRole("button", { name: "保存草稿" }).click();
    await expect.poll(() => toasts(page)).toContain("草稿已保存");
    await expect(page).toHaveURL(new RegExp(`/runbooks/${SAVED}$`));
    await expect(page.locator("h1", { hasText: "检查用：保存后的草稿" })).toBeVisible();
    // The new page's save and submit buttons are gone: nothing on screen can POST /runbooks again.
    await expect(page.getByRole("button", { name: "保存草稿" })).toHaveCount(0);
    expect(created).toBe(1);
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- PA-241: expired API tokens ---------------- */

test.describe("expired API tokens (PA-241)", () => {
  test("expired tokens aren't counted, and the list and the side panel mark them expired", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const hours = (h: number) => new Date(Date.now() + h * 3_600_000).toISOString();
    const token = (id: string, name: string, expires: string, revoked: string | null = null) => ({ id, name, environment_id: "prod", scopes: ["connectors:view"], created_at: hours(-48), expires_at: expires, revoked_at: revoked });
    // The server lists live tokens first (internal/v3tokens/lifecycle.go) and gives no state field.
    await page.route(at("/api-tokens"), (r) =>
      r.request().method() === "GET"
        ? json(r, 200, {
            items: [
              token("tok-live", "检查用有效令牌", hours(24)),
              token("tok-expired", "检查用过期令牌", hours(-1)),
              token("tok-revoked", "检查用吊销令牌", hours(24), hours(-2)),
            ],
          })
        : r.fallback(),
    );
    await page.goto(origin + "/integrations/webhooks");
    const tokensTab = page.getByRole("tab", { name: /API 令牌/ });
    await expect(tokensTab.locator(".count")).toHaveText("1", { timeout: 20_000 });
    // The inbound tab's side panel lists the same tokens.
    const side = page.locator("aside.aside .v3-section", { has: page.locator("h3", { hasText: /^API 令牌$/ }) });
    await expect(side.locator("li", { hasText: "检查用过期令牌" })).toContainText("已过期");
    await expect(side.locator("li", { hasText: "检查用有效令牌" })).not.toContainText("已过期");
    await expect(side).not.toContainText("检查用吊销令牌");
    await tokensTab.click();
    const list = page.locator(".v3-content ul.plain-list");
    await expect(list.locator("li", { hasText: "检查用过期令牌" })).toContainText("已过期");
    await expect(list.locator("li", { hasText: "检查用有效令牌" })).not.toContainText("已过期");
    await expect(list).not.toContainText("检查用吊销令牌");
    expect(m.pageErrors).toEqual([]);
  });
});
