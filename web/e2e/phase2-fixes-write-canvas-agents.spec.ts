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

/* ---------------- PA-237: the viewer's vote on an agent run ---------------- */

const RUN = "run-write-check";
const run = (myVote: "up" | "down" | null) => ({
  id: RUN, agent: "写路径检查 Agent", agent_id: AGENT, application_id: "app", environment_id: "prod", version: "v1", trigger: "manual", status: "success",
  error_code: null, started_at: "2026-10-18T01:00:00Z", finished_at: "2026-10-18T01:00:05Z",
  chain: [{ label: "Agent", value: "写路径检查 Agent", icon: "bot" }, { label: "Version", value: "v1", icon: "version" }],
  stats: [{ k: "status", v: "success" }], steps: [], total: 5000, context: null, budget: null, isolated: null, memory: null,
  feedback: { up: myVote === "up" ? 1 : 0, down: myVote === "down" ? 1 : 0, my_vote: myVote }, partial: true, missing: [],
});
const vote = (page: Page, name: "有用" | "无用") => page.getByRole("button", { name, exact: true });

test.describe("an agent run shows the viewer's saved vote (PA-237)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a vote saved earlier is highlighted when the run opens", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at(`/agent-runs/${RUN}`), (r) => json(r, 200, run("down")));
    await page.goto(origin + `/agents/runs/${RUN}`);
    await expect(vote(page, "无用")).toHaveClass(/active/, { timeout: 20_000 });
    await expect(vote(page, "有用")).not.toHaveClass(/active/);
    expect(m.pageErrors).toEqual([]);
  });

  test("a new vote says what was saved, promises no improvement queue, and still shows after a reload", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    let saved: "up" | "down" | null = null;
    await page.route(at(`/agent-runs/${RUN}`), (r) => json(r, 200, run(saved)));
    await page.route(at(`/agent-runs/${RUN}/feedback`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      saved = "down";
      return json(r, 200, { vote: "down", feedback: { up: 0, down: 1, my_vote: "down" } });
    });
    await page.goto(origin + `/agents/runs/${RUN}`);
    await expect(vote(page, "无用")).not.toHaveClass(/active/, { timeout: 20_000 });
    await vote(page, "无用").click();
    await expect(page.locator(".v3-toast")).toContainText("已标记为无用");
    await expect(page.locator(".v3-toast")).not.toContainText("改进队列");
    await expect(vote(page, "无用")).toHaveClass(/active/);
    await page.reload();
    await expect(vote(page, "无用")).toHaveClass(/active/, { timeout: 20_000 });
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- PA-244 (P3): repeated writes and what a create says ---------------- */

test.describe("small write-path fixes on canvases and agents (PA-244)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("W10: a double click on fork creates one branch", async ({ page }) => {
    await page.addInitScript(zh);
    let posts = 0;
    await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 200, graph()));
    await page.route(at(`/investigations/${INV}/branches`), async (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      const n = ++posts;
      // Answer slowly, so the second click lands while the first fork is still in flight.
      await new Promise((done) => setTimeout(done, 500));
      return json(r, 201, { id: `b-new-${n}`, kind: "branch", parent: "b1", title: `新分支 ${n}`, subtitle: "调查分支", status: "active", x: 750, y: 40 * n, w: 260, summary: null, layout_version: 0, ...context("v0", 0) });
    });
    await page.goto(origin + `/canvas/${INV}`);
    await branchCard(page, "b1").getByText("检查分支", { exact: true }).click({ timeout: 20_000 });
    await page.getByRole("toolbar", { name: "画布工具" }).getByRole("button", { name: "派生" }).dblclick();
    await expect(branchCard(page, "b-new-1")).toBeVisible();
    await page.waitForTimeout(1000);
    expect(posts).toBe(1);
  });

  test("W12: registering an agent that already exists doesn't say it was created", async ({ page }) => {
    await page.addInitScript(zh);
    const service = Buffer.from("app-write-check\u0000prod").toString("base64url");
    const sent: unknown[] = [];
    await page.route(at("/agents/lifecycle"), (r) => json(r, 200, { stages: null, agents: [], todos: null }));
    await page.route(at("/services"), (r) => json(r, 200, { items: [{ id: service, name: "写路径检查服务", tier: "t2" }] }));
    await page.route(at("/agents"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      sent.push(r.request().postDataJSON());
      return json(r, 200, { id: AGENT, created: false, application_id: "app-write-check", environment_id: "prod" });
    });
    await page.goto(origin + "/agents/lifecycle");
    await page.getByRole("button", { name: "新建 Agent" }).click({ timeout: 20_000 });
    const dialog = page.getByRole("dialog");
    await dialog.getByPlaceholder("例如：容量巡检").fill("写路径检查 Agent");
    await dialog.getByPlaceholder("capacity-patrol").fill(AGENT);
    await dialog.getByRole("button", { name: "创建 Agent" }).click();
    const toast = page.locator(".v3-toast", { hasText: "写路径检查 Agent" });
    await expect(toast).toContainText("已存在");
    await expect(toast).not.toContainText("已创建");
    await expect(toast).not.toContainText("暂停");
    expect(sent).toEqual([{ id: AGENT, application_id: "app-write-check", name: "写路径检查 Agent", summary: "" }]);
  });

  test("W13: a new canvas with an AI draft says the draft is done, not still being written", async ({ page }) => {
    await page.addInitScript(zh);
    const DOC = "cv-write-check";
    await page.route(at("/canvas-templates"), (r) => json(r, 200, { items: [] }));
    await page.route(at("/canvases"), (r) =>
      r.request().method() === "POST" ? json(r, 201, { id: DOC, href: `/canvas/${DOC}` }) : json(r, 200, { items: [], total: 0 }),
    );
    await page.route(at(`/canvases/${DOC}`), (r) =>
      json(r, 200, { id: DOC, title: "写路径检查画布", kind: "plan", status: "active", env: "prod", participants: [], note: "", nodes: [{ id: "n1", x: 0, y: 0, w: 280, icon: "file", tone: "accent", title: "AI 起草的节点", sub: "", blocks: [] }], edges: [], artifacts: [], threads: {} }),
    );
    await page.goto(origin + "/canvases");
    await page.getByRole("button", { name: "新建画布" }).first().click({ timeout: 20_000 });
    const dialog = page.getByRole("dialog");
    await dialog.getByPlaceholder("例如：Q4 容量评估").fill("写路径检查画布");
    await dialog.getByRole("button", { name: "创建画布" }).click();
    const toast = page.locator(".v3-toast", { hasText: "画布已创建" });
    await expect(toast).toBeVisible();
    await expect(toast).not.toContainText("正在起草");
    await expect(toast).toContainText("已起草");
  });
});
