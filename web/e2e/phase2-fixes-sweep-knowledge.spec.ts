import { test, expect, type Page, type Route } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * Sweep fixes before round 3 (WAN-434), group 3: knowledge, conversations, canvases, agents and the home page. Every
 * check pins the page to fixed API answers with page.route, writes included, so it runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-sweep-knowledge.spec.ts
 */
const origin = INSTANCES.prod.origin;
const unavailable = (r: Route, code = "check_unavailable") => json(r, 503, { error: { code, message: "check" } });
/** Holds a read until the test ends, so the page stays in its loading state. */
const hang = () => new Promise<void>(() => {});
const noData = (page: Page) => page.getByText("没有数据", { exact: true });

/* ---------------- PA-211: Runbook reviewers ---------------- */

const RUNBOOK = "rb-sweep-check";
const runbook = (patch: Record<string, unknown> = {}) => ({
  id: RUNBOOK, title: "清扫检查 Runbook", version: "v2", status: "draft", base: "blank", source_kind: "blank", source_ref: "", owner: "检查人",
  scope: "workspace", environment_ids: ["prod"], application_id: "", team_id: "", tags: [], when: "检查用",
  steps: [{ n: 1, kind: "check", title: "核对", body: "x", readonly: true }],
  reviewers: [], versions: [], usage: { refs: null, resolve_rate: null, weekly: null, highlight: null }, incidents: null, last_verified: null,
  complete_v3_dto: false, partial: true, unknown_fields: ["pending_reviewers"],
  ...patch,
});

test.describe("runbook reviewers (PA-211)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("an approved reviewer reads as approved, never as optional", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const reviewers = [
      { name: "复核甲", state: "approved" },
      { name: "复核乙", state: "pending" },
      { name: "复核丙", state: "withdrawn" },
    ];
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, runbook({ reviewers })));
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    const review = page.locator(".v3-section", { has: page.locator("h3", { hasText: /^复核$/ }) });
    await expect(review).toContainText("复核甲", { timeout: 20_000 });
    await expect(review.locator(".v3-row", { hasText: "复核甲" })).toContainText("已批准");
    await expect(review.locator(".v3-row", { hasText: "复核乙" })).toContainText("待复核");
    await expect(review.locator(".v3-row", { hasText: "复核丙" }).locator(".unk")).toHaveCount(1);
    await expect(review).not.toContainText("可选");
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- PA-212: Agent run steps ---------------- */

const RUN = "run-sweep-check";
const RUN_DETAIL = {
  id: RUN, agent: "检查 Agent", agent_id: "agent-check", application_id: "app", environment_id: "prod", version: "v1", trigger: "manual", status: "failed",
  error_code: null, started_at: "2026-10-18T01:00:00Z", finished_at: "2026-10-18T01:00:05Z",
  chain: [{ label: "Agent", value: "检查 Agent", icon: "bot" }, { label: "Version", value: "v1", icon: "version" }],
  stats: [{ k: "status", v: "failed" }],
  steps: [
    { id: "s-ok", icon: "tool", name: "检查读取", start: 0, dur: 420, meta: "ok", kind: "tool", connector: "tool-a" },
    { id: "s-failed", icon: "tool", name: "检查查询", start: 500, dur: 1234, meta: "failed", kind: "tool", connector: "tool-b" },
    { id: "s-blocked", icon: "tool", name: "检查写入", start: 1800, dur: null, meta: "blocked", kind: "tool", connector: "tool-c" },
    { id: "s-unknown", icon: "tool", name: "检查其他", start: 2000, dur: 30, meta: "unknown", kind: "tool", connector: "tool-d" },
  ],
  total: 5000, context: null, budget: null, isolated: null, memory: null,
};

test.describe("agent run steps (PA-212)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("each step says its own result, and the duration column shows the duration", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at(`/agent-runs/${RUN}`), (r) => json(r, 200, RUN_DETAIL));
    await page.goto(origin + `/agents/runs/${RUN}`);
    await page.getByRole("tab", { name: "运行概览" }).click({ timeout: 20_000 });
    const card = page.locator(".v3-card", { has: page.getByText(/步骤详情/) });
    const open = async (name: string) => {
      await page.locator("button.g-row", { hasText: name }).click();
      await expect(card).toContainText(name);
    };
    await open("检查查询");
    await expect(card).toContainText("失败");
    await expect(card).not.toContainText("成功");
    await expect(card).toContainText("耗时 1.2 s");
    await open("检查写入");
    await expect(card).toContainText("已拦截");
    await expect(card).not.toContainText("成功");
    await expect(card.locator(".unk")).toHaveCount(1);
    await expect(card).not.toContainText("blocked");
    await open("检查其他");
    await expect(card).toContainText("结果未知");
    await expect(card).toContainText("耗时 30 ms");
    await open("检查读取");
    await expect(card).toContainText("成功");
    // The timeline's right column is the duration, not the raw span status.
    for (const raw of ["ok", "failed", "blocked", "unknown"]) await expect(page.locator(".g-row .meta", { hasText: new RegExp(`^${raw}$`) })).toHaveCount(0);
    await expect(page.locator("button.g-row", { hasText: "检查查询" }).locator(".meta")).toHaveText("1.2 s");
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- PA-215: lists that failed or are still loading ---------------- */

test.describe("lists never claim there is no data when the read failed (PA-215)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const [name, path, route] of [
    ["runbooks", "/runbooks", "/runbooks"],
    ["postmortems", "/postmortems", "/postmortems"],
    ["conversations", "/conversations", "/conversations"],
    ["memory", "/memory", "/memory"],
  ] as const) {
    test(`${name}: a failed read says why`, async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      await page.route(at(route), (r) => unavailable(r));
      await page.goto(origin + path);
      await expect(page.locator(".v3-table .unk-page")).toBeVisible({ timeout: 20_000 });
      await expect(page.locator(".v3-table .unk-page")).toContainText("暂时不可用");
      await expect(noData(page)).toHaveCount(0);
      expect(m.pageErrors).toEqual([]);
    });
  }

  test("memory: a read in flight shows the loading rows, not “no data”", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/memory"), () => hang());
    await page.goto(origin + "/memory");
    await expect(page.locator(".v3-table .skeleton").first()).toBeVisible({ timeout: 20_000 });
    await expect(noData(page)).toHaveCount(0);
  });

  test("an empty list still says there is no data", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/postmortems"), (r) => json(r, 200, { kpis: [], items: [], causes: null, impact_by_service: null, followups: null, unknown_fields: [], page: 1, page_size: 1000, total: 0 }));
    await page.goto(origin + "/postmortems");
    await expect(noData(page)).toBeVisible({ timeout: 20_000 });
  });
});

/* ---------------- PA-216: evaluation runs that ended without results ---------------- */

const EXP = "exp-sweep-check";
const experiment = (patch: Record<string, unknown>) => ({
  id: EXP, title: "清扫检查实验", meta: "", status: "invalid", baseline_version: "v1", candidate_version: "v2", dataset: null,
  metrics: null, cases: null, radar: null, gate_failed: null, execution: null,
  ...patch,
});

test.describe("evaluation results (PA-216)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const [status, want] of [
    ["failed", "这次运行失败了"],
    ["cancelled", "这次运行已取消"],
    ["ambiguous", "这次运行的结果不明"],
  ] as const) {
    test(`a ${status} run says so instead of “results show once it has run”`, async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at(`/evals/${EXP}`), (r) => json(r, 200, experiment({ execution: { case_id: "case-1", status } })));
      await page.goto(origin + `/agents/evals?id=${EXP}`);
      await expect(page.getByText(want)).toBeVisible({ timeout: 20_000 });
      await expect(page.getByText("这个实验还没有结果")).toHaveCount(0);
      await expect(page.getByText("运行完成后")).toHaveCount(0);
    });
  }

  test("an invalid experiment without a run record says it is invalid", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/evals/${EXP}`), (r) => json(r, 200, experiment({})));
    await page.goto(origin + `/agents/evals?id=${EXP}`);
    await expect(page.getByText("这个实验已失效，没有结果")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("运行完成后")).toHaveCount(0);
  });

  test("a pending experiment still says results come once it has run", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/evals/${EXP}`), (r) => json(r, 200, experiment({ status: "pending" })));
    await page.goto(origin + `/agents/evals?id=${EXP}`);
    await expect(page.getByText("这个实验还没有结果")).toBeVisible({ timeout: 20_000 });
  });
});

/* ---------------- PA-218: postmortem list states, tabs and service facet ---------------- */

const PM_ROWS = [
  { id: "pm-review", title: "订单 延迟复盘", workflow_status: "review", application_id: "svc-review" },
  { id: "pm-draft", title: "库存 告警复盘", workflow_status: "draft", application_id: "svc-draft" },
  { id: "pm-published", title: "支付 超时复盘", workflow_status: "published", application_id: "svc-published" },
].map((p) => ({
  ...p, incident_id: "inc-" + p.id, severity: "P2", date: "2026-10-17T08:00:00Z", duration: "20 分钟", cause: "未知", root_cause: null, actions: "未知 / 1",
  // The server folds review into draft for its two-state display; the real state is workflow_status.
  status: p.workflow_status === "review" ? "draft" : p.workflow_status,
}));
/** Answers like the server: the tab filters on the stored state. */
const postmortems = (r: Route) => {
  const tab = new URL(r.request().url()).searchParams.get("tab") ?? "all";
  const items = PM_ROWS.filter((p) => tab === "all" || p.workflow_status === tab);
  return json(r, 200, { kpis: [], items, causes: null, impact_by_service: null, followups: null, unknown_fields: [], page: 1, page_size: 1000, total: items.length });
};

test.describe("postmortem list (PA-218)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a postmortem in review reads as awaiting review, and nothing claims an AI wrote it", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/postmortems"), postmortems);
    await page.goto(origin + "/postmortems");
    const row = (title: string) => page.locator(".v3-table tr", { hasText: title });
    await expect(row("订单 延迟复盘")).toContainText("待审核", { timeout: 20_000 });
    await expect(row("库存 告警复盘")).toContainText("草稿");
    await expect(row("支付 超时复盘")).toContainText("已发布");
    await expect(page.locator(".v3-table").getByText("AI 草稿")).toHaveCount(0);
    await expect(page.getByRole("button", { name: /AI 草稿/ })).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("the unpublished tab lists drafts and postmortems in review, not published ones", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/postmortems"), postmortems);
    await page.goto(origin + "/postmortems");
    await page.getByRole("button", { name: /^未发布/ }).click({ timeout: 20_000 });
    await expect(page.locator(".v3-table tr", { hasText: "订单 延迟复盘" })).toBeVisible();
    await expect(page.locator(".v3-table tr", { hasText: "库存 告警复盘" })).toBeVisible();
    await expect(page.locator(".v3-table tr", { hasText: "支付 超时复盘" })).toHaveCount(0);
  });

  test("the service filter offers the records' services, not the first word of each title", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/postmortems"), postmortems);
    await page.goto(origin + "/postmortems");
    await expect(page.locator(".v3-table tr", { hasText: "订单 延迟复盘" })).toBeVisible({ timeout: 20_000 });
    await page.getByRole("button", { name: /^服务/ }).click();
    const menu = page.getByRole("listbox");
    await expect(menu.getByRole("option", { name: "svc-review" })).toBeVisible();
    for (const word of ["订单", "库存", "支付"]) await expect(menu.getByRole("option", { name: word })).toHaveCount(0);
  });
});

/* ---------------- PA-219: investigation canvas and the rollback drawer it opens ---------------- */

const INV = "INV-sweep-check";
const CANVAS = { investigation: { id: INV, title: "清扫检查调查", status: "active", env: "prod", participants: [] }, loop: [], nodes: [], tray: [] };

test.describe("investigation canvas reads (PA-219)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a failed canvas read says why instead of an empty canvas with zero counts", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at(`/investigations/${INV}/canvas`), (r) => unavailable(r));
    await page.goto(origin + `/canvas/${INV}`);
    await expect(page.locator(".unk-page")).toContainText("暂时不可用", { timeout: 20_000 });
    await expect(page.getByText(/0 个分支/)).toHaveCount(0);
    await expect(page.locator(".cv-status")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("an investigation that doesn't exist here reads as not found", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 404, { error: { code: "not_found", message: "check" } }));
    await page.goto(origin + `/canvas/${INV}`);
    await expect(page.getByText("找不到这条记录")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText(/0 个分支/)).toHaveCount(0);
  });

  const openDrawer = async (page: Page) => {
    await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 200, CANVAS));
    await page.goto(origin + `/canvas/${INV}`);
    await page.getByRole("button", { name: "提议回滚" }).click({ timeout: 20_000 });
  };

  test("rollback drawer: a failed open-investigations read says so, not that the investigation has no service", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/canvases"), (r) => unavailable(r));
    await openDrawer(page);
    await expect(page.getByText("读不到开放调查列表")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("这个调查没有关联到当前环境里的服务")).toHaveCount(0);
  });

  test("rollback drawer: an investigation missing from the open list says so instead of an endless skeleton", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/canvases"), (r) => json(r, 200, { items: [], total: 0 }));
    await openDrawer(page);
    await expect(page.getByText("开放调查里没有这个调查")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("这个调查没有关联到当前环境里的服务")).toHaveCount(0);
  });
});

/* ---------------- PA-220: generic canvas merge and write-back ---------------- */

const DOC = "cv-sweep-check";
const DOC_CANVAS = {
  id: DOC, title: "清扫检查画布", kind: "analysis", status: "active", env: "prod", participants: [], note: "",
  nodes: [{ id: "n1", x: 0, y: 0, w: 280, icon: "file", tone: "accent", title: "主线", sub: "检查", blocks: [] }], edges: [], artifacts: [], threads: {},
};

test.describe("generic canvas toolbar (PA-220)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("real mode has no merge or write-back that only shows a toast", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at(`/canvases/${DOC}`), (r) => json(r, 200, DOC_CANVAS));
    await page.goto(origin + `/canvas/${DOC}`);
    const toolbar = page.getByRole("toolbar", { name: "画布工具" });
    await expect(toolbar.getByRole("button", { name: "自动布局" })).toBeVisible({ timeout: 20_000 });
    await expect(toolbar.getByRole("button", { name: "合并" })).toHaveCount(0);
    await expect(toolbar.getByRole("button", { name: "回写" })).toHaveCount(0);
    await expect(page.getByText("可撤销")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });
});
