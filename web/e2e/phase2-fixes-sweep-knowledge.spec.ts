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

/* ---------------- PA-221: main reads that failed on other pages ---------------- */

test.describe("pages say why their main read failed (PA-221)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const [name, path, route, before] of [
    ["home", "/", "/home", null],
    ["runbook detail", `/runbooks/${RUNBOOK}`, `/runbooks/${RUNBOOK}`, null],
    ["new postmortem: candidates", "/postmortems/new", "/postmortems/candidates", null],
    ["new postmortem: draft", "/postmortems/new?incident=inc-sweep-check", "/postmortems/draft", null],
    ["generic canvas", `/canvas/${DOC}`, `/canvases/${DOC}`, null],
    ["canvas list", "/canvases", "/canvases", null],
    ["templates", "/templates", "/investigation-templates", null],
    ["agent runs", "/agents/runs", "/agent-runs", null],
    ["agent run detail", `/agents/runs/${RUN}`, `/agent-runs/${RUN}`, "运行概览"],
    ["agent lifecycle", "/agents/lifecycle", "/agents/lifecycle", null],
    ["agent tools", "/agents/tools", "/agent-tools", null],
    ["prompts", "/agents/prompts", "/prompts", null],
    ["guardrails", "/agents/guardrails", "/guardrails", null],
    ["experiment", `/agents/evals?id=${EXP}`, `/evals/${EXP}`, null],
  ] as const) {
    test(`${name}: a failed read shows the reason, not an endless skeleton`, async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      await page.route(at(route), (r) => unavailable(r));
      await page.goto(origin + path);
      if (before) await page.getByRole("tab", { name: before }).click({ timeout: 20_000 });
      await expect(page.locator(".unk-page")).toContainText("暂时不可用", { timeout: 20_000 });
      expect(m.pageErrors).toEqual([]);
    });
  }

  test("new postmortem: an incident that can't be drafted says so", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/postmortems/draft"), (r) => json(r, 404, { error: { code: "not_found", message: "Incident is not eligible for a postmortem draft" } }));
    await page.goto(origin + "/postmortems/new?incident=inc-sweep-check");
    await expect(page.getByText("这个事件不能起草复盘")).toBeVisible({ timeout: 20_000 });
  });

  test("prompts: a failed prompt read says why", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/prompts"), (r) => json(r, 200, { items: [{ id: "pr-sweep", name: "sweep.check", kind: "prompt", version: "v1" }] }));
    await page.route(at("/prompts/pr-sweep"), (r) => unavailable(r));
    await page.goto(origin + "/agents/prompts");
    await expect(page.locator(".unk-page")).toContainText("暂时不可用", { timeout: 20_000 });
  });

  test("model picker: a failed model list isn't “no matching models”", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/models"), (r) => unavailable(r));
    await page.goto(origin + "/conversations/new");
    await page.getByRole("button", { name: "选择模型与推理强度" }).click({ timeout: 20_000 });
    await expect(page.getByText("读不到模型列表，暂时不能切换模型。")).toBeVisible();
    await expect(page.getByText("没有匹配的模型")).toHaveCount(0);
  });
});

/* ---------------- PA-222: unknowns shown as 0 or blank ---------------- */

const HOME = (patch: Record<string, unknown> = {}) => ({
  greeting: { name: "检查人", summary: "" }, briefing: null, suggestions: [], kpis: [],
  attention: [{ id: "att-sweep", severity: null, service: "检查服务", env: "prod", symptom: "检查症状", meta: "", ai: null, href: "/incidents/inc-sweep", alert_state: "firing" }],
  firing_services: 1, todos: [], services: [], alert_trend: null,
  availability: { todo_actions: "not_connected", suggestions: "not_generated", alert_trend: "not_connected", oncall: "not_connected" },
  activity: [], recent: [],
  ...patch,
});
const runbookRow = (id: string, title: string, resolve_rate: number | null) => ({
  id, title, tags: [], scope: "workspace", owner: "检查人", version: "v1", status: "published", refs_30d: null, resolve_rate, last_verified: null, stale: false,
});

test.describe("unknowns are not zero (PA-222)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("home: with no to-do source the page doesn't count 0 items waiting", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/home"), (r) => json(r, 200, HOME()));
    await page.goto(origin + "/");
    await expect(page.locator(".home-greet")).toContainText("1 个服务异常", { timeout: 20_000 });
    await expect(page.locator(".home-greet")).not.toContainText("项等待你处理");
    const queue = page.locator(".home-card", { has: page.locator("h2", { hasText: "待我处理" }) });
    await expect(queue.locator(".v3-card-head .v3-badge")).toHaveCount(0);
    await expect(queue).toContainText("还没有待办来源");
    expect(m.pageErrors).toEqual([]);
  });

  test("home: an attention item without a known severity shows the unknown mark, not an empty badge", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/home"), (r) => json(r, 200, HOME()));
    await page.goto(origin + "/");
    const item = page.locator(".attn", { hasText: "检查服务" });
    await expect(item.locator(".unk")).toHaveCount(1, { timeout: 20_000 });
  });

  test("home: a real to-do source still counts its items", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/home"), (r) => json(r, 200, HOME({ availability: {} })));
    await page.goto(origin + "/");
    await expect(page.locator(".home-greet")).toContainText("0 项等待你处理", { timeout: 20_000 });
  });

  test("a KPI without a value shows the unknown mark", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/postmortems"), (r) =>
      json(r, 200, { kpis: [{ id: "k-sweep", label: "检查指标", value: null }], items: [], causes: null, impact_by_service: null, followups: null, unknown_fields: [], page: 1, page_size: 1000, total: 0 }),
    );
    await page.goto(origin + "/postmortems");
    await expect(page.locator(".v3-kpi", { hasText: "检查指标" }).locator(".unk")).toHaveCount(1, { timeout: 20_000 });
  });

  test("runbooks: sorting by resolve rate keeps an unknown rate apart from 0%", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/runbooks"), (r) =>
      json(r, 200, { items: [runbookRow("rb-zero", "检查零", 0), runbookRow("rb-unknown", "检查未知", null)], counts: { all: 2, mine: 0, review: 0, stale: 0 }, kpis: null, suggestions: null, unknown_fields: [] }),
    );
    await page.goto(origin + "/runbooks");
    await page.getByRole("columnheader", { name: "解决率" }).click({ timeout: 20_000 });
    await expect(page.locator(".v3-table tbody tr").first()).toContainText("检查未知");
  });

  test("knowledge search: a keyword-only search doesn't print vector 0.00", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/knowledge/search"), (r) =>
      r.request().method() === "POST"
        ? json(r, 200, { hits: [{ title: "检查命中", snippet: "检查片段", keyword: 0.8, vector: 0, score: 0.8 }], mode: "keyword", degraded: true, corpora: [] })
        : r.fallback(),
    );
    await page.goto(origin + "/knowledge/sources");
    await page.getByRole("textbox", { name: "检索问题" }).fill("检查", { timeout: 20_000 });
    await page.getByRole("button", { name: "检索", exact: true }).click();
    const hit = page.locator(".ret-hit", { hasText: "检查命中" });
    await expect(hit).toContainText("关键词 0.80");
    await expect(hit).not.toContainText("向量 0.00");
    await expect(hit.locator(".unk")).toHaveCount(1);
  });

  test("agent release: no configured share and no traffic isn't “configured 0%”", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/agents/agent-sweep"), (r) =>
      json(r, 200, {
        id: "agent-sweep", icon: "bot", name: "检查 Agent", desc: "", version: "v2", level: "L1", score: null, runs_7d: null, success: null, avg_tokens: null, status: "canary",
        revision: 1, owner: "", account: null, summary: "检查", versions: [],
        release: { from: "v1", to: "v2", current: null, target: 10, gates: null, tokens: null }, diff: null, config: null,
      }),
    );
    await page.goto(origin + "/agents/agent-sweep");
    await expect(page.getByText(/v2 配置比例/)).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("v2 配置 0%")).toHaveCount(0);
  });
});

/* ---------------- PA-223: unknown reasons that don't match the facts ---------------- */

/** The explanation an unknown mark gives (its accessible name is "<label>. <description>"). */
const reasonOf = (unk: ReturnType<Page["locator"]>) => unk.getAttribute("aria-label");

test.describe("unknown reasons match the facts (PA-223)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("runbooks: AI suggestions the server never produces don't promise to appear later", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/runbooks"), (r) => json(r, 200, { items: [], counts: { all: 0, mine: 0, review: 0, stale: 0 }, kpis: null, suggestions: null, unknown_fields: ["suggestions"] }));
    await page.goto(origin + "/runbooks");
    await expect(page.getByText("平台目前不生成 Runbook 修订建议。")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText(/AI 会在这里提出修订建议/)).toHaveCount(0);
  });

  test("postmortems: statistics and follow-ups the server never produces don't promise to appear later", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/postmortems"), (r) => json(r, 200, { kpis: [], items: [], causes: null, impact_by_service: null, followups: null, unknown_fields: [], page: 1, page_size: 1000, total: 0 }));
    await page.goto(origin + "/postmortems");
    await expect(page.getByText("平台目前不跟踪改进项的完成情况。")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("平台目前不统计复盘的根因分类和影响时长。")).toBeVisible();
    await expect(page.getByText(/复盘发布后/)).toHaveCount(0);
  });

  test("agent release: unknown eval gates aren't “generated when new data arrives”", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/agents/agent-sweep"), (r) =>
      json(r, 200, {
        id: "agent-sweep", icon: "bot", name: "检查 Agent", desc: "", version: "v2", level: "L1", score: null, runs_7d: null, success: null, avg_tokens: null, status: "canary",
        revision: 1, owner: "", account: null, summary: "检查", versions: [],
        release: { from: "v1", to: "v2", current: null, target: 10, configured_canary_percent: 10, gates: null, tokens: null }, diff: null, config: null,
      }),
    );
    await page.goto(origin + "/agents/agent-sweep");
    const gates = page.locator(".v3-row", { has: page.getByText("评测门禁", { exact: true }) }).locator(".unk");
    await expect(gates).toHaveCount(1, { timeout: 20_000 });
    expect(await reasonOf(gates)).not.toContain("自动生成");
  });

  test("home: a watched service's unknown health doesn't claim a source to connect", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/home"), (r) =>
      json(r, 200, HOME({ services: [{ id: "svc-sweep", name: "检查服务甲", tier: null, status: null, budget_pct: null, p99: null, error_rate: null, series: null }] })),
    );
    await page.goto(origin + "/");
    const row = page.locator(".home-card tr", { hasText: "检查服务甲" });
    await expect(row.locator(".unk").first()).toBeVisible({ timeout: 20_000 });
    expect(await reasonOf(row.locator(".unk").first())).not.toMatch(/未接入|还没有连接/);
  });
});

/* ---------------- PA-225: AI and asking flows ---------------- */

const CONV = "conv-sweep-check";
const CONVERSATION = {
  id: CONV, title: "清扫检查对话", mode: "chat", status: "active", scope: { inferred: false }, messages: [], artifacts: [],
  side: { connectors: [], context: { used: null, budget: null, parts: [] }, recalled: [] },
};
const BRANCHED = {
  ...CANVAS,
  nodes: [
    { id: "r1", kind: "root", title: "检查根节点", subtitle: "", status: "active", x: 0, y: 0, w: 280 },
    { id: "b1", kind: "branch", parent: "r1", title: "检查分支", subtitle: "", status: "active", x: 400, y: 0, w: 280, summary: null },
  ],
};

test.describe("AI flows say what they do and recover from failures (PA-225)", () => {
  test.describe("as admin", () => {
    test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

    test("new canvas: the AI draft says it uses only the title, goal and services", async ({ page }) => {
      await page.addInitScript(zh);
      await page.goto(origin + "/canvases");
      await page.getByRole("button", { name: "新建画布" }).first().click({ timeout: 20_000 });
      await expect(page.getByText("只根据标题、目标和所选服务起草，不读取指标或 Runbook")).toBeVisible();
      await expect(page.getByText("基于服务映射、历史指标和已有 Runbook")).toHaveCount(0);
    });

    test("ask AI: a failed request says so and doesn't keep generating", async ({ page }) => {
      await page.addInitScript(zh);
      let posts = 0;
      await page.route(at("/conversations"), (r) => (r.request().method() === "POST" ? (posts++, json(r, 503, { error: { code: "conversation_unavailable", message: "对话服务暂时不可用" } })) : r.fallback()));
      await page.route(at("/postmortems"), postmortems);
      await page.goto(origin + "/postmortems");
      await page.getByRole("button", { name: /问 AI/ }).click({ timeout: 20_000 });
      const card = page.locator(".ask-card");
      await card.getByRole("button", { name: "解释这页的异常" }).click();
      await expect(page.locator(".v3-toast")).toContainText("对话服务暂时不可用");
      await expect(card.getByText("生成中")).toHaveCount(0);
      // Not stuck in "sending": the same suggestion sends again.
      await card.getByRole("button", { name: "解释这页的异常" }).click();
      await expect.poll(() => posts).toBe(2);
    });

    test("conversation: a send the server rejects says why and frees the composer", async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at(`/conversations/${CONV}`), (r) => json(r, 200, CONVERSATION));
      await page.route(at(`/conversations/${CONV}/messages`), (r) =>
        r.request().method() === "POST" ? json(r, 413, { error: { code: "message_too_large", message: "消息超过长度上限" } }) : r.fallback(),
      );
      await page.goto(origin + `/conversations/${CONV}`);
      const box = page.getByRole("textbox", { name: "向 AI 下指令" });
      await box.fill("检查消息", { timeout: 20_000 });
      await box.press("Enter");
      await expect(page.locator(".v3-toast")).toContainText("消息超过长度上限");
      await expect(page.getByRole("button", { name: "停止生成" })).toHaveCount(0);
      await expect(page.locator("button.send .v3-spinner")).toHaveCount(0);
    });
  });

  test.describe("without the grant", () => {
    test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });

    test("ask AI: the suggestions are disabled like the composer", async ({ page }) => {
      await page.addInitScript(zh);
      let posts = 0;
      await page.route(at("/conversations"), (r) => (r.request().method() === "POST" ? (posts++, json(r, 403, { error: { code: "forbidden", message: "check" } })) : r.fallback()));
      await page.route(at("/postmortems"), postmortems);
      await page.goto(origin + "/postmortems");
      await page.getByRole("button", { name: /问 AI/ }).click({ timeout: 20_000 });
      const suggestion = page.locator(".ask-card").getByRole("button", { name: "解释这页的异常" });
      await expect(suggestion).toBeDisabled();
      await expect(page.locator(".ask-card .perm-gate")).toHaveCount(1);
      expect(posts).toBe(0);
    });

    test("investigation canvas: the branch's inline follow-up is gated like the toolbar", async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 200, BRANCHED));
      await page.goto(origin + `/canvas/${INV}`);
      await page.getByText("检查分支", { exact: true }).first().click({ timeout: 20_000 });
      const input = page.getByRole("textbox", { name: "在这个分支继续追问" });
      await expect(input).toBeDisabled();
      await expect(input).toHaveAttribute("placeholder", /需要 画布·修改 权限/);
    });
  });
});

/* ---------------- PA-228: write entries gated by the route's module and action ---------------- */

test.describe("write entries are gated at the entry (PA-228)", () => {
  test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });

  test("new canvas: the dialog a ?new=investigation link opens can't be submitted without canvas.change", async ({ page }) => {
    await page.addInitScript(zh);
    let posted = 0;
    await page.route(at("/canvases"), (r) => (r.request().method() === "POST" ? (posted++, json(r, 403, { error: { code: "forbidden", message: "check" } })) : r.fallback()));
    await page.goto(origin + "/canvases?new=investigation");
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("button", { name: "新建画布" })).toBeDisabled({ timeout: 20_000 });
    await expect(dialog.locator(".perm-gate")).toContainText("需要 画布·修改 权限");
    expect(posted).toBe(0);
  });

  test("templates: new template needs canvas.change", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/investigation-templates"), (r) => json(r, 200, { items: [], counts: {} }));
    await page.goto(origin + "/templates");
    await expect(page.getByRole("button", { name: "新建模板" })).toBeDisabled({ timeout: 20_000 });
  });

  test("runbook detail: submitting for review and the read-only check need knowledge.manage", async ({ page }) => {
    await page.addInitScript(zh);
    let posted = 0;
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, runbook()));
    await page.route((u) => u.pathname.startsWith(`/api/v1/runbooks/${RUNBOOK}/`), (r) => (posted++, json(r, 403, { error: { code: "forbidden", message: "check" } })));
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    await expect(page.getByRole("button", { name: "提交复核" })).toBeDisabled({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: "校验只读步骤" })).toBeDisabled();
    expect(posted).toBe(0);
  });

  test("investigation canvas: dragging a node doesn't try to save its layout without canvas.change", async ({ page }) => {
    await page.addInitScript(zh);
    let patched = 0;
    await page.route(at(`/investigations/${INV}/canvas`), (r) => json(r, 200, BRANCHED));
    await page.route((u) => u.pathname.startsWith(`/api/v1/investigations/${INV}/nodes/`), (r) =>
      r.request().method() === "PATCH" ? (patched++, json(r, 403, { error: { code: "forbidden", message: "check" } })) : r.fallback(),
    );
    await page.goto(origin + `/canvas/${INV}`);
    const node = page.getByText("检查分支", { exact: true }).first();
    const box = await node.boundingBox({ timeout: 20_000 });
    if (!box) throw new Error("node not laid out");
    await page.mouse.move(box.x + 10, box.y + 5);
    await page.mouse.down();
    await page.mouse.move(box.x + 160, box.y + 120, { steps: 8 });
    await page.mouse.up();
    await page.waitForTimeout(800);
    expect(patched).toBe(0);
  });
});
