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
