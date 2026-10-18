import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, zh } from "./support/checks";

/**
 * Sweep by defect class, group 4 (WAN-434): observe, integrations, the roles / AI preferences / members settings and
 * the shell. Every read is pinned with page.route, so this runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-sweep-observe.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

const role = (id: string) => ({
  id,
  name: "Check role",
  members: 1,
  kind: "business",
  desc: "Check role for the sweep",
  member_names: ["Check Member"],
  scope: ["global"],
  actions: ["查看", "创建 / 编辑", "删除", "批准", "管理"],
  matrix: [{ resource: "Check resource", grants: [1, 0, 0, 0, 0] }],
  notes: "",
});
async function pinRole(page: Page, id: string) {
  await page.route(at("/roles"), (r) => json(r, 200, { items: [{ id, name: "Check role", members: 1, kind: "business" }] }));
  await page.route(at(`/roles/${id}`), (r) => r.fulfill({ status: 200, contentType: "application/json", headers: { "X-Role-Matrix-Revision": "1" }, body: JSON.stringify(role(id)) }));
}

/** PA-207 (WAN-566): the History tab showed a made-up change by a sample person; the server keeps no per-role history. */
test.describe("role history on the real page (PA-207)", () => {
  test("says where role changes are recorded instead of a sample change", async ({ page }) => {
    await page.addInitScript(zh);
    await pinRole(page, "pa207-check");
    const m = monitor(page);
    await page.goto(origin + "/settings/roles");
    await expect(page.getByRole("heading", { name: "Check role" })).toBeVisible({ timeout: 20_000 });
    await page.getByRole("tab", { name: "变更记录" }).click();
    await expect(page.getByText("陈雨")).toHaveCount(0);
    await expect(page.getByText("9/20")).toHaveCount(0);
    await expect(page.getByText("还没有按角色整理的变更记录", { exact: false })).toBeVisible();
    await expect(page.getByText("保存权限会写入审计日志", { exact: false })).toBeVisible();
    expect(m.pageErrors).toEqual([]);
  });

  test("English says the same", async ({ page }) => {
    await page.addInitScript(en);
    await pinRole(page, "pa207-check");
    await page.goto(origin + "/settings/roles");
    await expect(page.getByRole("heading", { name: "Check role" })).toBeVisible({ timeout: 20_000 });
    await page.getByRole("tab", { name: "History" }).click();
    await expect(page.getByText("Chen Yu")).toHaveCount(0);
    await expect(page.getByText("No per-role change history is kept yet", { exact: false })).toBeVisible();
  });
});

const t0 = "2026-10-18T02:00:00Z";
const webhookView = (patch: Record<string, unknown> = {}) => ({
  inbound: [
    { id: "generic-check:alert", source_id: "generic-check", event_type: "alert", brand: "generic", name: "generic-check · alert", url: "/api/v1/events/alert", verify: "Bearer + HMAC-SHA256", count: "2", last_at: t0, last_code: 202, status: "ok", status_basis: "latest_receipt_in_window" },
    { id: "alertmanager:lab-check", source_id: "lab-check", event_type: "alert", brand: "prometheus", name: "lab-check", url: "/api/v1/webhooks/alertmanager/lab-check", verify: "Bearer", count: "37", last_at: t0, last_code: 202, status: "unverified", status_basis: "health_unverified" },
    { id: "bad-check:change", source_id: "bad-check", event_type: "change", brand: "generic", name: "bad-check · change", url: "/api/v1/events/change", verify: "Bearer + HMAC-SHA256", count: "0", last_at: t0, last_code: 422, status: "error", status_basis: "latest_receipt_in_window" },
  ],
  deliveries: [
    { time: t0, event: "generic-check · alert · accepted", code: "202", ms: "12 ms", source_id: "generic-check", event_type: "alert" },
    { time: "2026-10-18T01:00:00Z", event: "generic-check · alert · duplicate", code: "409", ms: "9 ms", source_id: "generic-check", event_type: "alert" },
    { time: t0, event: "bad-check · change · invalid_event", code: "422", ms: "4 ms", source_id: "bad-check", event_type: "change" },
  ],
  tokens: [],
  mcp: { enabled: false, url: "", tools: [], clients: "unknown" },
  outbound: [],
  availability: { inbound: "configured", native_inbound: "configured_health_unverified", api_tokens: "not_connected", public_mcp: "not_connected", outbound: "not_connected", receipt_window: "past_24_hours" },
  ...patch,
});
const apiTokens = { items: [{ id: "tok-check", name: "check-token", scopes: ["connectors:view"], created_at: t0, expires_at: "2026-10-19T02:00:00Z", revoked_at: null }] };
async function pinWebhooks(page: Page, view = webhookView()) {
  await page.route(at("/webhooks"), (r) => json(r, 200, view));
  await page.route(at("/api-tokens"), (r) => (r.request().method() === "GET" ? json(r, 200, apiTokens) : r.fallback()));
}

/**
 * PA-208 (WAN-567): the read-only MCP server toggle was always on, while the server says mcp.enabled=false and
 * public_mcp=not_connected; "allowed clients" showed the raw "unknown"; outbound showed 0 and the side tokens were blank.
 */
test.describe("webhooks page MCP, outbound and tokens (PA-208)", () => {
  test("the MCP toggle follows mcp.enabled and says the platform serves no MCP yet", async ({ page }) => {
    await page.addInitScript(zh);
    await pinWebhooks(page);
    const m = monitor(page);
    await page.goto(origin + "/integrations/webhooks");
    const aside = page.locator("aside.aside");
    await expect(aside.getByRole("switch", { name: "MCP 服务" })).toHaveAttribute("aria-checked", "false", { timeout: 20_000 });
    await expect(aside.getByText("平台目前还没有对外提供 MCP 服务", { exact: false })).toBeVisible();
    await expect(aside).not.toContainText("unknown");
    await page.getByRole("tab", { name: /MCP 服务/ }).click();
    const main = page.locator(".v3-split .main");
    await expect(main.getByRole("switch", { name: "MCP 服务" })).toHaveAttribute("aria-checked", "false");
    await expect(main.getByText("平台目前还没有对外提供 MCP 服务", { exact: false })).toBeVisible();
    await expect(main).not.toContainText("unknown");
    // The tab doesn't count one MCP service the platform doesn't serve.
    await expect(page.getByRole("tab", { name: /MCP 服务/ }).locator(".count")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("outbound events say not connected instead of 0", async ({ page }) => {
    await page.addInitScript(zh);
    await pinWebhooks(page);
    await page.goto(origin + "/integrations/webhooks");
    const aside = page.locator("aside.aside");
    await expect(aside.getByText("平台还没有出站事件投递", { exact: false })).toBeVisible({ timeout: 20_000 });
    const tab = page.getByRole("tab", { name: /出站事件/ });
    await expect(tab.locator(".count")).toHaveCount(0);
    await tab.click();
    await expect(page.locator(".v3-split .main").getByText("平台还没有出站事件投递", { exact: false })).toBeVisible();
  });

  test("the side token list shows the tokens the API token tab lists", async ({ page }) => {
    await page.addInitScript(zh);
    await pinWebhooks(page);
    await page.goto(origin + "/integrations/webhooks");
    await expect(page.locator("aside.aside").getByText("check-token", { exact: true })).toBeVisible({ timeout: 20_000 });
    await page.getByRole("tab", { name: /API 令牌/ }).click();
    await expect(page.locator(".v3-split .main").getByText("check-token", { exact: true })).toBeVisible();
  });

  test("English: no raw unknown, MCP not served", async ({ page }) => {
    await page.addInitScript(en);
    await pinWebhooks(page);
    await page.goto(origin + "/integrations/webhooks");
    const aside = page.locator("aside.aside");
    await expect(aside.getByRole("switch", { name: "MCP service" })).toHaveAttribute("aria-checked", "false", { timeout: 20_000 });
    await expect(aside.getByText("The platform doesn't serve MCP yet", { exact: false })).toBeVisible();
    await expect(aside).not.toContainText("unknown");
    await expect(aside.getByText("The platform doesn't send outbound events yet", { exact: false })).toBeVisible();
  });
});

const svc = (patch: Record<string, unknown> = {}) => ({
  id: "svc-check",
  name: "检查用服务",
  desc: "",
  tier: "T1",
  team: "check-team",
  oncall: null,
  status: "warn",
  slo: { target: "99.9%", budget_left: 40 },
  release: null,
  alerts: 0,
  connectors: [],
  unavailable: { oncall: "not_configured" },
  ...patch,
});
const catalog = (items: unknown[]) => ({
  kpis: [{ id: "warn", label: "告警中", value: "0", tone: "warn", series: [], unknown: 0 }],
  facets: [{ key: "status", title: "状态", items: [{ label: "warn", count: 1, on: false }] }],
  items,
});

/**
 * PA-209 (WAN-568): status warn was written "Alerting", but warn also comes from an error budget under half or a 1 h
 * burn ≥ 1 (internal/v3serviceviews/metrics.go), with 0 alerts and the "Alerting" KPI not counting it.
 */
test.describe("service status warn (PA-209)", () => {
  test("warn reads as needs attention and says where it comes from", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/services"), (r) => json(r, 200, catalog([svc()])));
    const m = monitor(page);
    await page.goto(origin + "/services");
    const row = page.locator("tbody tr", { hasText: "检查用服务" });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).not.toContainText("有告警");
    await expect(row.getByText("需要关注", { exact: true })).toBeVisible();
    await expect(row.locator("[title*='错误预算']")).toHaveCount(1);
    // The status facet names the state instead of the raw value.
    await expect(page.locator(".v3-split .side")).not.toContainText("warn");
    await expect(page.locator(".v3-split .side").getByText("需要关注", { exact: false })).toBeVisible();
    expect(m.pageErrors).toEqual([]);
  });

  test("English: warn is Needs attention, not Alerting", async ({ page }) => {
    await page.addInitScript(en);
    await page.route(at("/services"), (r) => json(r, 200, catalog([svc()])));
    await page.goto(origin + "/services");
    const row = page.locator("tbody tr", { hasText: "检查用服务" });
    await expect(row.getByText("Needs attention", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(row).not.toContainText("Alerting");
  });
});

const pushView = (patch: Record<string, unknown> = {}) => ({
  availability: "configured_unverified",
  types: ["alert", "change", "incident", "resource", "oncall"].map((id) => ({ id, icon: "bell", name: id, schema: `${id}.v1`, today: "0", rejected: "0%", tone: "ok" })),
  endpoint: { method: "POST", path: "/api/v1/events/change", host: "ops.check.test", headers: [], example: "{}", responses: [] },
  deliveries: [],
  // What the server sends: no starter stack (internal/v3ingestpush/view.go).
  stack: { enabled: false, components: [], helm: "", compose: "", estimate: "" },
  ...patch,
});

/**
 * PA-220 (WAN-579): the starter stack's "Generate manifests" downloaded an empty file and said "values.yaml
 * downloaded", "Docs" opened docs.example.internal, and the text promised connectors created automatically.
 */
test.describe("starter stack on the real page (PA-220)", () => {
  test("says the platform offers no starter stack instead of fake download and docs", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/ingest/push"), (r) => json(r, 200, pushView()));
    const m = monitor(page);
    await page.goto(origin + "/integrations/ingest/stack");
    const aside = page.locator("aside.aside");
    await expect(aside.getByText("可选配套栈", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(aside.getByText("平台还没有提供配套栈的部署清单", { exact: false })).toBeVisible();
    await expect(page.getByRole("button", { name: "生成部署清单" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "查看说明" })).toHaveCount(0);
    await expect(aside).not.toContainText("自动创建连接器");
    await expect(page.locator("a[href*='example.internal']")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });
});

/**
 * PA-227 (WAN-586): "Recent deliveries · {source}" listed every source's receipts, native sources have none (the table
 * went blank for lab-alertmanager), every response code was green, and an error source said "all deliveries in 24 h
 * were rejected (401)" while error means the latest receipt was 422.
 */
test.describe("webhook deliveries per source (PA-227)", () => {
  const panel = (page: Page) => page.locator(".v3-panel", { hasText: "最近投递" });

  test("lists only the selected source's deliveries, coloured by response", async ({ page }) => {
    await page.addInitScript(zh);
    await pinWebhooks(page);
    const m = monitor(page);
    await page.goto(origin + "/integrations/webhooks");
    await expect(panel(page)).toContainText("最近投递 · generic-check · alert", { timeout: 20_000 });
    await expect(panel(page).locator("tbody tr")).toHaveCount(2);
    await expect(panel(page)).not.toContainText("bad-check");
    await expect(panel(page).locator(".v3-badge", { hasText: "202" })).toHaveClass(/t-ok/);
    await expect(panel(page).locator(".v3-badge", { hasText: "409" })).not.toHaveClass(/t-ok/);
    expect(m.pageErrors).toEqual([]);
  });

  test("a native source says it keeps no per-delivery records", async ({ page }) => {
    await page.addInitScript(zh);
    await pinWebhooks(page);
    await page.goto(origin + "/integrations/webhooks");
    await page.locator("tbody tr", { hasText: "lab-check" }).first().click({ timeout: 20_000 });
    await expect(panel(page)).toContainText("最近投递 · lab-check");
    await expect(panel(page)).not.toContainText("generic-check");
    await expect(panel(page).getByText("原生来源不记录逐条投递", { exact: false })).toBeVisible();
  });

  test("an error source names the 422 it got, not 401", async ({ page }) => {
    await page.addInitScript(zh);
    await pinWebhooks(page);
    await page.goto(origin + "/integrations/webhooks");
    await page.locator("tbody tr", { hasText: "bad-check" }).first().click({ timeout: 20_000 });
    await expect(panel(page)).toContainText("最近投递 · bad-check · change");
    await expect(panel(page)).not.toContainText("401");
    await expect(panel(page).getByText("最近一次投递没有通过事件校验（422）", { exact: false })).toBeVisible();
    await expect(panel(page).locator(".v3-badge", { hasText: "422" })).toHaveClass(/t-danger/);
  });

  test("a source with no deliveries in the window says so", async ({ page }) => {
    await page.addInitScript(zh);
    await pinWebhooks(page, webhookView({ deliveries: [] }));
    await page.goto(origin + "/integrations/webhooks");
    await expect(panel(page).getByText("最近 24 小时没有投递", { exact: false })).toBeVisible({ timeout: 20_000 });
  });
});

const unavailable = (code: string) => ({ error: { code, message: "check: source unavailable" } });
async function expectUnavailable(page: Page, scope = page.locator(".v3-page-body")) {
  await expect(scope.getByText("暂时不可用", { exact: true })).toBeVisible({ timeout: 20_000 });
  await expect(scope.getByText("来源连接失败或超时，刷新页面可再试一次。")).toBeVisible();
  await expect(scope.locator(".skeleton")).toHaveCount(0);
}

/**
 * PA-221 (WAN-580): a failed main read kept the skeleton or left the page blank on the integration pages, trace
 * detail, topology and personal AI preferences; each now says why with the shared load error.
 */
test.describe("integration and observe pages whose main read fails (PA-221)", () => {
  for (const [route, path] of [
    ["/integrations", "/connectors"],
    ["/integrations/coverage", "/coverage"],
    ["/integrations/mapping", "/service-mappings"],
    ["/integrations/mcp", "/mcp-servers"],
    ["/integrations/models", "/models/admin"],
    ["/integrations/webhooks", "/webhooks"],
    ["/integrations/ingest/push", "/ingest/push"],
    ["/resources", "/topology"],
  ] as const) {
    test(`${route}: ${path} 503 says unavailable`, async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at(path), (r) => json(r, 503, unavailable("check_unavailable")));
      const m = monitor(page);
      await page.goto(origin + route);
      await expectUnavailable(page);
      expect(m.pageErrors).toEqual([]);
    });
  }

  test("trace detail: a trace outside retention says not found", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/traces/pa221check"), (r) => json(r, 404, { error: { code: "not_found", message: "check" } }));
    await page.goto(origin + "/traces/pa221check");
    await expect(page.locator(".v3-page-body").getByText("找不到这条记录", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".v3-page-body .skeleton")).toHaveCount(0);
  });

  test("add connector: a type that can't be read says so in the side card and can't be created", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/connector-types/gitlab"), (r) => json(r, 503, unavailable("check_unavailable")));
    await page.goto(origin + "/integrations/new");
    await expectUnavailable(page, page.locator(".wizard-side"));
    await expect(page.getByRole("button", { name: /下一步/ })).toBeDisabled();
  });

  test("connector detail: a failed read says so in the aside", async ({ page }) => {
    await page.addInitScript(zh);
    const item = { id: "conn-check", brand: "prometheus", name: "Check Prometheus", kind: "Metrics", status: "ok", tags: [], meta: "", checked_at: null };
    await page.route(at("/connectors"), (r) => json(r, 200, { items: [item], counts: { ok: 1, unverified: 0, error: 0 }, categories: [{ id: "all", label: "全部", count: 1 }] }));
    await page.route(at("/connectors/conn-check"), (r) => json(r, 503, unavailable("check_unavailable")));
    await page.goto(origin + "/integrations");
    await page.getByRole("button", { name: /Check Prometheus/ }).click({ timeout: 20_000 });
    await expectUnavailable(page, page.locator("aside.aside"));
  });

  for (const [route, path] of [
    ["/settings/ai", "/me/ai-preferences"],
    ["/integrations/models", "/models/admin"],
  ] as const) {
    test(`${route}: all environments (422) asks for one environment`, async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at(path), (r) => json(r, 422, { error: { code: "invalid_environment", message: "Choose one environment" } }));
      await page.goto(origin + route);
      await expect(page.locator(".v3-page-body").getByText("先在右上角选择一个具体环境", { exact: false })).toBeVisible({ timeout: 20_000 });
      await expect(page.locator(".v3-page-body .skeleton")).toHaveCount(0);
    });
  }
});

/**
 * PA-222 (WAN-581): unknowns shown as 0 or not at all — discovery failure as "0 found, 0 registered", the lower
 * sources badge 0, a watched service with unknown status drawn without a dot, the watched count 0 when the list
 * can't be read, and native GitLab's unknown count as the raw "unknown".
 */
test.describe("unknowns shown as 0 or blank (PA-222)", () => {
  test("register service: a failed discovery says so instead of 0 found", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/services/discover"), (r) => json(r, 503, unavailable("catalog_unavailable")));
    await page.goto(origin + "/services/new");
    await expectUnavailable(page);
    await expect(page.getByText("发现 0 个", { exact: false })).toHaveCount(0);
  });

  test("lower-environment sources: a failed read is unknown, not 0", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/integrations/lower-sources"), (r) => json(r, 503, unavailable("lower_sources_unavailable")));
    await page.goto(origin + "/integrations");
    const head = page.locator(".lower-src .v3-card-head");
    await expect(head).toBeVisible({ timeout: 20_000 });
    await expect(head.locator(".v3-badge")).not.toHaveText("0");
    await expect(head.locator(".unk")).toHaveCount(1);
  });

  test("sidebar: a watched service with unknown status shows a neutral dot that says so", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/me/watched-services"), (r) =>
      json(r, 200, { items: [{ id: "svc-check", name: "检查用服务", status: null, availability: { state: "unavailable", reason: "Current scoped status evidence is unavailable" } }] }),
    );
    await page.goto(origin + "/services");
    const item = page.locator(".v3-sidebar .watched", { hasText: "检查用服务" });
    await expect(item).toBeVisible({ timeout: 20_000 });
    await expect(item.locator("[title*='状态未知']")).toHaveCount(1);
  });

  test("services: the watched count is unknown when the watched list can't be read", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/services"), (r) => json(r, 200, catalog([svc()])));
    await page.route(at("/me/watched-services"), (r) => json(r, 503, unavailable("watch_unavailable")));
    await page.goto(origin + "/services");
    const pill = page.locator(".v3-pills button", { hasText: "我关注的" });
    await expect(pill).toBeVisible({ timeout: 20_000 });
    await expect(pill.locator(".count")).toHaveCount(0);
    await pill.click();
    await expectUnavailable(page, page.locator(".v3-split .main"));
    await expect(page.getByText("没有数据", { exact: true })).toHaveCount(0);
  });

  test("webhooks: native GitLab's unknown count is an unknown value, not the raw word", async ({ page }) => {
    await page.addInitScript(zh);
    const gitlab = { id: "gitlab:gl-check", source_id: "gl-check", event_type: "pipeline", brand: "gitlab", name: "GitLab check", url: "/api/v1/webhooks/gitlab/gl-check", verify: "X-Gitlab-Token", count: "unknown", last_at: null, last_code: null, status: "unverified", status_basis: "historical_receipt_scope_unavailable" };
    await pinWebhooks(page, webhookView({ inbound: [gitlab], deliveries: [] }));
    await page.goto(origin + "/integrations/webhooks");
    const row = page.locator("tbody tr", { hasText: "GitLab check" });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).not.toContainText("unknown");
    await expect(row.locator(".unk")).toHaveCount(2);
  });
});

/**
 * PA-223 (WAN-582): tier, team, on-call and SLO target nobody filled in were explained as "not connected, configure
 * in Integrations"; they are registration fields, so they read "not filled in".
 */
test.describe("service fields nobody filled in (PA-223)", () => {
  test("the catalog explains unfilled tier, team, on-call and SLO without pointing to Integrations", async ({ page }) => {
    await page.addInitScript(zh);
    const blank = svc({ tier: null, team: null, oncall: null, slo: { target: null, budget_left: null }, unavailable: { tier: "not_configured", team: "not_configured", oncall: "not_configured", "slo.target": "not_configured", "slo.budget_left": "not_configured" } });
    await page.route(at("/services"), (r) => json(r, 200, catalog([blank])));
    await page.goto(origin + "/services");
    const row = page.locator("tbody tr", { hasText: "检查用服务" });
    await expect(row).toBeVisible({ timeout: 20_000 });
    const marks = row.locator(".unk");
    // tier, team, on-call, SLO
    for (const i of [0, 1, 2, 3]) {
      await expect(marks.nth(i)).toHaveAttribute("aria-label", "未填写. 还没有人填写这项信息。");
    }
  });
});

const gitlabType = {
  brand: "gitlab",
  name: "GitLab",
  kind: "Code and pipelines",
  version: "v1",
  auth_methods: [{ id: "pat", label: "Personal access token", desc: "Read-only token" }],
  scopes: [{ id: "read_api", label: "read_api", required: true }],
  tools: [{ name: "list_pipelines", desc: "Read-only" }],
  webhook_url: "https://ops.check.test/api/v1/webhooks/gitlab/new",
};

/**
 * PA-224 (WAN-583) and PA-229 (WAN-588): the add-connector wizard offered sample credential sources with no effect,
 * prefilled gitlab.example.internal, said it creates webhooks automatically and "Test & create" while the server only
 * records a disabled registration; the latency chart drew a fixed 300 ms line and 2 s ceiling; the SLO card was always
 * amber with a red delta and a red note; the sidebar workspace was always "Trading platform".
 */
test.describe("add connector on the real page (PA-224, PA-229)", () => {
  test("no sample address or credential sources, and the flow says what creating does", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/connector-types/gitlab"), (r) => json(r, 200, gitlabType));
    let created = false;
    await page.route(at("/connectors"), (r) => (r.request().method() === "POST" ? ((created = true), json(r, 409, { error: { code: "connector_exists", message: "check" } })) : r.fallback()));
    const m = monitor(page);
    await page.goto(origin + "/integrations/new");
    await expect(page.getByRole("heading", { name: "连接与认证" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole("textbox", { name: "实例地址" })).toHaveValue("");
    await expect(page.locator(".wizard")).not.toContainText("example.internal");
    for (const sample of ["secret/aiops/gitlab", "aiops/gitlab-token"]) await expect(page.locator(".wizard")).not.toContainText(sample);
    await expect(page.getByText("部署方为它绑定凭据", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("自动创建 Webhook")).toHaveCount(0);
    await expect(page.getByText("自动在上述项目创建", { exact: false })).toHaveCount(0);
    for (let i = 0; i < 3; i++) await page.getByRole("button", { name: /下一步/ }).click();
    await expect(page.getByRole("button", { name: "测试并创建" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "创建连接器" })).toBeVisible();
    expect(created).toBe(false);
    expect(m.pageErrors).toEqual([]);
  });
});

const dashboard = (slo: Record<string, unknown>) => ({
  window: { label_times: ["2026-10-18T01:00:00Z", "2026-10-18T01:30:00Z", "2026-10-18T02:00:00Z"], tick_times: ["2026-10-18T01:00:00Z", "2026-10-18T02:00:00Z"], release_at: null, rollback_at: null, incident: null },
  kpis: null,
  requests: null,
  slo: { remaining: 80, availability: "99.950%", delta: "+0.050%", burn: [], note: "观测到的错误预算消耗速率", ...slo },
  latency: { p50: [10, 12, 11], p95: [20, 22, 21], p99: [30, 33, 31], heat: null, buckets: null },
  endpoints: null,
  instances: null,
  unavailable: {},
});

test.describe("metrics dashboard on the real page (PA-224)", () => {
  async function open(page: Page, slo: Record<string, unknown>) {
    await page.addInitScript(zh);
    await page.route(at("/metrics/dashboard"), (r) => json(r, 200, dashboard(slo)));
    await page.route(at("/services/svc-check/slo"), (r) => json(r, 200, { target: "99.9", window: "30d", revision: 1, unavailable: {} }));
    await page.goto(origin + "/metrics?service=svc-check");
    await expect(page.getByText("错误预算剩余", { exact: true })).toBeVisible({ timeout: 20_000 });
  }

  test("percentiles have no made-up 300 ms threshold", async ({ page }) => {
    await open(page, {});
    const latency = page.locator(".v3-panel", { hasText: "延迟分布" });
    await expect(latency.locator("svg").first()).toBeVisible();
    await expect(latency.locator(".ch-thr")).toHaveCount(0);
  });

  test("the SLO card colours follow the budget and the delta; the note isn't an alert", async ({ page }) => {
    await open(page, {});
    const slo = page.locator(".v3-panel", { hasText: "错误预算剩余" });
    await expect(slo.locator(".ch-gauge b")).toHaveAttribute("style", /--ok/);
    await expect(slo.getByText("+0.050%")).not.toHaveAttribute("style", /--danger/);
    await expect(slo.locator(".danger-note")).toHaveCount(0);
    await expect(slo.getByText("观测到的错误预算消耗速率")).toBeVisible();
  });

  test("a budget under half is amber and a missed target red", async ({ page }) => {
    await open(page, { remaining: 30, availability: "99.850%", delta: "-0.050%" });
    const slo = page.locator(".v3-panel", { hasText: "错误预算剩余" });
    await expect(slo.locator(".ch-gauge b")).toHaveAttribute("style", /--warn/);
    await expect(slo.getByText("-0.050%")).toHaveAttribute("style", /--danger/);
  });
});

test.describe("sidebar workspace name (PA-224)", () => {
  test("the workspace menu names the real workspace, not the sample one", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/workspace"), (r) => json(r, 200, { name: "检查工作区", slug: "check", timezone: "Asia/Shanghai", locale: "zh-CN", week_start: "monday", retention_days: 180, data_region: "", brand: { name: "Redstring", logo: null, pending: false } }));
    await page.goto(origin + "/services");
    await page.getByRole("button", { name: "切换工作区" }).click({ timeout: 20_000 });
    await expect(page.getByText("检查工作区 · 当前工作区", { exact: true })).toBeVisible();
    await expect(page.getByText("交易平台", { exact: false })).toHaveCount(0);
  });
});
