import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, patchRead, someId, toasts, zh } from "./support/checks";

/**
 * Regression checks for the investigation canvas fixes of WAN-434 phase 2 (see support/checks.ts: fixed API answers,
 * runs on any data, never writes).
 *
 * PA-116 (POST /canvases without services) has no check here: the page always sends services, and the only way to
 * tell the fix apart is to create a canvas. internal/v3canvas/create_http_test.go covers it.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-canvas.spec.ts
 */
const origin = INSTANCES.prod.origin;
const trayItem = { id: "EVD-check", kind: "table", title: "检查用证据", source: "prometheus", body: { type: "table", rows: [["kind", "metrics"], ["content", "检查用内容"]] } };

test.describe("evidence tray in real mode (PA-105)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("tray items can't be dragged onto the canvas and nothing calls the legacy pin route", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/healthz");
    const id = await someId(page, "/canvases?kind=investigation", "investigation canvas");
    await patchRead(page, `/investigations/${id}/canvas`, (real) => ({ ...real, tray: [trayItem] }));
    const pins: string[] = [];
    await page.route(at(`/investigations/${id}/evidence`), (r) => {
      pins.push(r.request().method());
      return json(r, 501, { error: { code: "v3_not_implemented", message: "check" } });
    });
    await page.goto(origin + `/canvas/${id}`);
    await page.getByRole("toolbar", { name: "画布工具" }).getByRole("button", { name: "证据", exact: true }).click({ timeout: 20_000 });
    const tray = page.getByRole("complementary", { name: "证据托盘" });
    await expect(tray).toContainText("回写的“仅证据”", { timeout: 20_000 });
    const item = tray.locator(".cv-tray-item", { hasText: "检查用证据" });
    await expect(item).toHaveAttribute("draggable", "false");
    await item.dragTo(page.locator(".cv-area"));
    await page.waitForTimeout(800);
    expect(pins).toEqual([]);
    expect((await toasts(page)).join(" ")).not.toContain("证据节点");
    await expect(page.locator(".cv-status")).toContainText("1 条证据");
    await expect(page.locator(".cv-status")).not.toContainText("拖入");
    expect(m.pageErrors).toEqual([]);
  });

  test("a branch without evidence doesn't point at the tray", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someId(page, "/canvases?kind=investigation", "investigation canvas");
    await patchRead(page, `/investigations/${id}/canvas`, (real) => ({ ...real, nodes: (real.nodes as object[]).map((n) => ({ ...n, evidence_refs: undefined })) }));
    await page.goto(origin + `/canvas/${id}`);
    const node = page.locator("article.cv-node").first();
    await node.waitFor({ timeout: 20_000 });
    await node.click({ position: { x: 40, y: 16 } });
    await page.locator(".dp").getByRole("tab", { name: /^证据/ }).click({ timeout: 10_000 });
    await expect(page.locator(".dp")).toContainText("AI 回复读取数据时，证据会记在这里");
    await expect(page.locator(".dp")).not.toContainText("证据托盘拖入");
  });
});

test.describe("node reply failures say why (PA-267)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the reply list names timeouts, interruptions and unusable answers", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/healthz");
    const id = await someId(page, "/canvases?kind=investigation", "investigation canvas");
    const failed = (rid: string, code: string) => ({
      id: rid, text: "检查用问题", author: "Admin", answer: null, state: "failed", failure_code: code,
      model: { id: "check", name: "check", provider: "check" }, reasoning: null,
      created_at: "2026-10-27T09:11:57Z", completed_at: null, evidence_ids: [], rollback_suggestion: null,
    });
    const replies = /^\/api\/v1\/investigations\/[^/]+\/nodes\/([^/]+)\/replies$/;
    await page.route((url) => replies.test(url.pathname), (r) =>
      json(r, 200, {
        investigation_id: id, node_id: new URL(r.request().url()).pathname.match(replies)?.[1], has_more: false, next_cursor: null,
        replies: [failed("INR-check-1", "reply_timeout"), failed("INR-check-2", "reply_interrupted"), failed("INR-check-3", "model_reply_invalid")],
      }),
    );
    await page.goto(origin + `/canvas/${id}`);
    const node = page.locator("article.cv-node").first();
    await node.waitFor({ timeout: 20_000 });
    await node.click({ position: { x: 40, y: 16 } });
    await page.locator(".dp").getByRole("tab", { name: /^对话/ }).click({ timeout: 10_000 });
    const panel = page.locator(".dp");
    await expect(panel).toContainText("回复超过了时间上限", { timeout: 10_000 });
    await expect(panel).toContainText("回复被中断了");
    await expect(panel).toContainText("模型没有给出可用的回答");
    await expect(panel).not.toContainText("原因未知");
    expect(m.pageErrors).toEqual([]);
  });
});
