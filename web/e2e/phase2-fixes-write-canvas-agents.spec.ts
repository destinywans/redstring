import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * Write-path fixes before round 3 (WAN-434): the investigation canvas and agents. After a write, each page shows what
 * the server keeps and says what the server did (D63). Every check pins the page to fixed API answers with page.route,
 * writes included, so it runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-write-canvas-agents.spec.ts
 */
const origin = INSTANCES.prod.origin;

/* ---------------- PA-235: an investigation canvas branch after a reply ---------------- */

const INV = "INV-write-check";
const context = (version: string, messages: number) => ({ context: { version, tokens: null, messages, excludes: [] }, versions: [] });
/** A root and one branch; `branch` patches the branch the way the server reports it. */
const graph = (branch: Record<string, unknown> = {}) => ({
  investigation: { id: INV, title: "写路径检查调查", status: "active", env: "prod", participants: [] },
  loop: [],
  tray: [],
  nodes: [
    { id: "r1", kind: "root", title: "检查根节点", subtitle: "主调查", status: "active", x: 0, y: 0, w: 280, summary: null, layout_version: 1, ...context("v1", 0) },
    { id: "b1", kind: "branch", parent: "r1", title: "检查分支", subtitle: "调查分支", status: "active", x: 400, y: 0, w: 280, summary: null, layout_version: 1, ...context("v1", 0), ...branch },
  ],
});
const REPLY = "检查回复：超时来自重试放大";
const branchCard = (page: Page, id: string) => page.locator(`article.cv-node[data-node="${id}"]`);

test.describe("an investigation canvas shows what the server keeps after a reply (PA-235)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a replied branch isn't marked confirmed, and its message count is the server's", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    let replied = false;
    // Once the reply is stored the server reports the branch active again, with its summary and its real count.
    await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 200, replied ? graph({ summary: REPLY, ...context("v1", 4) }) : graph()));
    await page.route(at(`/investigations/${INV}/nodes/b1/replies`), (r) => json(r, 200, { investigation_id: INV, node_id: "b1", replies: [], has_more: false, next_cursor: null }));
    await page.route(at(`/investigations/${INV}/nodes/b1/messages`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      replied = true;
      return json(r, 200, { summary: REPLY, rollback_suggestion: null });
    });
    await page.goto(origin + `/canvas/${INV}`);
    const card = branchCard(page, "b1");
    await card.getByText("检查分支", { exact: true }).click({ timeout: 20_000 });
    const input = page.getByRole("textbox", { name: "在这个分支继续追问" });
    await input.fill("检查提问");
    await input.press("Enter");
    await expect(card).toContainText(REPLY, { timeout: 20_000 });
    // The local "confirmed" used to land 900 ms after the reply: judge after that.
    await page.waitForTimeout(1500);
    // The branch is selected and no longer running, so its edge isn't live.
    await expect(page.locator("path.cv-edge.flow")).toHaveCount(0);
    // The inspector opens on the context tab: the count the server reports, not the local count + 2.
    await expect(page.locator(".dp")).toContainText("+4 条消息");
    await expect(page.locator(".dp")).not.toContainText("+2 条消息");
    // Zoomed out, the compact card has no "confirmed" tag: the server has no such state.
    const zoomOut = page.getByRole("button", { name: "缩小", exact: true });
    for (let i = 0; i < 3; i++) await zoomOut.click();
    await expect(page.locator('article.cv-node.lite[data-node="b1"]')).toBeVisible();
    await expect(page.locator(".cv-lite-tag", { hasText: "已确认" })).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("a new branch shows what the server returned, not a made-up summary", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 200, graph()));
    await page.route(at(`/investigations/${INV}/branches`), (r) =>
      r.request().method() === "POST"
        ? json(r, 201, { id: "b2", kind: "branch", parent: "b1", title: "新分支 1", subtitle: "调查分支", status: "active", x: 750, y: 40, w: 260, summary: null, layout_version: 0, ...context("v0", 0) })
        : r.fallback(),
    );
    await page.goto(origin + `/canvas/${INV}`);
    await branchCard(page, "b1").getByText("检查分支", { exact: true }).click({ timeout: 20_000 });
    await page.getByRole("toolbar", { name: "画布工具" }).getByRole("button", { name: "派生" }).click();
    const fresh = branchCard(page, "b2");
    await expect(fresh).toBeVisible();
    // The local placeholder used to land 1.2 s after the fork: judge after that.
    await page.waitForTimeout(1800);
    await expect(fresh).not.toContainText("等待你的第一个问题");
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- PA-236: where an agent rollback lands ---------------- */

const AGENT = "agent-write-check";
/** An agent detail; `release` patches the release the way the server reports it. */
const agent = (release: Record<string, unknown>) => ({
  id: AGENT, icon: "bot", name: "写路径检查 Agent", desc: "", version: "v2", level: "L0", score: null, runs_7d: null, success: null, avg_tokens: null, status: "stable",
  revision: 7, owner: "", account: null, summary: "检查用",
  versions: [
    { version: "v2", status: "stable", traffic: null, configured_traffic: "100%", score: null, success: null, tokens: null, created: "2026-10-18T01:00:00Z" },
    { version: "v1", status: "retired", traffic: null, configured_traffic: "0%", score: null, success: null, tokens: null, created: "2026-10-17T01:00:00Z" },
  ],
  release: { from: "v2", to: "", current: null, target: 0, configured_canary_percent: 0, gates: null, tokens: null, missing: [], ...release },
  diff: null, config: null,
});

test.describe("an agent rollback names where it lands (PA-236)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("without a canary the button and the toast name the previous stable version", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const sent: unknown[] = [];
    await page.route(at(`/agents/${AGENT}`), (r) => json(r, 200, agent({ rollback_to: "v1" })));
    await page.route(at(`/agents/${AGENT}/rollback`), (r) => {
      sent.push(r.request().postDataJSON());
      return json(r, 200, { id: "rel-check", to: "v1", configured_percent: 100, revision: 8, configured_canary_percent: 0, observed_traffic: null, routing_connected: false, release_effect: "configuration_only" });
    });
    await page.goto(origin + `/agents/${AGENT}`);
    await expect(page.getByRole("heading", { name: "写路径检查 Agent" })).toBeVisible({ timeout: 20_000 });
    // The release starts from the stable v2, but a rollback without a canary goes back to v1.
    await expect(page.getByRole("button", { name: "回滚到 v2" })).toHaveCount(0);
    await page.getByRole("button", { name: "回滚到 v1" }).click();
    await expect(page.locator(".v3-toast")).toContainText("已回滚到 v1");
    await expect(page.locator(".v3-toast")).not.toContainText("v2");
    expect(sent).toEqual([{ expected_revision: 7 }]);
    expect(m.pageErrors).toEqual([]);
  });

  test("with a canary the rollback stays on the stable version and says the canary was cleared (control)", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/agents/${AGENT}`), (r) => json(r, 200, agent({ to: "v3", target: 50, configured_canary_percent: 50, rollback_to: "v2" })));
    await page.route(at(`/agents/${AGENT}/rollback`), (r) => json(r, 200, { id: "rel-check", to: "v2", configured_percent: 100, revision: 8, configured_canary_percent: 0 }));
    await page.goto(origin + `/agents/${AGENT}`);
    await page.getByRole("button", { name: "回滚到 v2" }).click({ timeout: 20_000 });
    await expect(page.locator(".v3-toast")).toContainText("已回滚到 v2，灰度流量清零");
  });

  test("with nothing to roll back to the button names no version, and the refusal is said", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/agents/${AGENT}`), (r) => json(r, 200, { ...agent({ from: "v1", rollback_to: null }), version: "v1", versions: [agent({}).versions[1]] }));
    await page.route(at(`/agents/${AGENT}/rollback`), (r) => json(r, 409, { error: { code: "rollback_unavailable", message: "没有可回滚到的上一个版本" } }));
    await page.goto(origin + `/agents/${AGENT}`);
    await expect(page.getByRole("heading", { name: "写路径检查 Agent" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: /^回滚到/ })).toHaveCount(0);
    await page.getByRole("button", { name: "回滚", exact: true }).click();
    await expect(page.locator(".v3-toast")).toContainText("没有可回滚到的上一个版本");
  });
});
