import { test, expect, type Page, type Route } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";

/**
 * Regression checks for the defects fixed during WAN-434 phase 2 (between rounds). As in phase1-fixes.spec.ts, each
 * check pins the page to fixed API answers with page.route, so it runs on any data and never writes: the real-data
 * verification is in the registry (docs/releases/2026-10-16-defect-convergence.md).
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes.spec.ts
 */
const origin = INSTANCES.prod.origin;
const zh = () => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" }));
const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
/** Text of every toast shown so far. */
const toasts = (page: Page) => page.locator(".v3-toast").allInnerTexts();
/** Matches one API path exactly, with or without a query string. */
const at = (path: string) => (url: URL) => url.pathname === "/api/v1" + path;

/** An alert ID from the inbox, read in the page so the session cookie applies. */
async function someAlert(page: Page): Promise<string> {
  const items = await page.evaluate(async () => {
    const r = await fetch("/api/v1/inbox", { headers: { "X-AIOps-Environment": "prod" } });
    return ((await r.json()) as { items?: { alert_id?: string }[] }).items ?? [];
  });
  const id = items.find((i) => i.alert_id)?.alert_id;
  test.skip(!id, "no alert in the inbox to open");
  return id!;
}

/** Answers GET /alerts/{id} with the real detail, patched. */
async function patchAlert(page: Page, id: string, patch: Record<string, unknown>) {
  const real = await page.evaluate(async (id) => (await fetch(`/api/v1/alerts/${id}`, { headers: { "X-AIOps-Environment": "prod" } })).json(), id);
  await page.route(at(`/alerts/${id}`), (r) => json(r, 200, { ...(real as object), ...patch }));
}

test.describe("inbox investigate (PA-167)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

  test("starts the alert's group investigation and opens it", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/healthz");
    const id = await someAlert(page);
    await patchAlert(page, id, { investigation_id: "", triage: null, group_id: "group:check" });
    let sent: Record<string, unknown> | null = null;
    await page.route(at("/alert-groups/bulk"), (r) => {
      sent = r.request().postDataJSON();
      return json(r, 200, { action: "investigate", results: [{ id: "group:check", outcome: "investigating", matched: 1, changed: 1, investigation_id: "inv-check", proposal_id: null, error: null }] });
    });
    await page.route(at("/investigations/inv-check"), (r) => json(r, 404, { error: { code: "not_found", message: "check" } }));
    await page.goto(origin + `/inbox?item=alert%3A${id}`);
    await page.getByRole("button", { name: "发起调查" }).click({ timeout: 20_000 });
    await expect(page).toHaveURL(/\/canvas\/inv-check$/);
    expect(sent).toEqual({ action: "investigate", ids: ["group:check"] });
    expect(m.pageErrors).toEqual([]);
  });

  test("a refused group says why in words", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someAlert(page);
    await patchAlert(page, id, { investigation_id: "", triage: null, group_id: "group:check" });
    await page.route(at("/alert-groups/bulk"), (r) => json(r, 207, { action: "investigate", results: [{ id: "group:check", outcome: "conflict", matched: 1, changed: 0, investigation_id: null, proposal_id: null, error: "conflict" }] }));
    await page.goto(origin + `/inbox?item=alert%3A${id}`);
    await page.getByRole("button", { name: "发起调查" }).click({ timeout: 20_000 });
    await expect.poll(async () => (await toasts(page)).join(" ")).toContain("请从事件页发起调查");
    expect(page.url()).not.toContain("/canvases");
  });
});

/** A service ID from the catalog, read in the page. */
async function someService(page: Page): Promise<string> {
  const items = await page.evaluate(async () => {
    const r = await fetch("/api/v1/services", { headers: { "X-AIOps-Environment": "prod" } });
    return ((await r.json()) as { items?: { id: string }[] }).items ?? [];
  });
  test.skip(!items.length, "no service in the catalog");
  return items[0].id;
}

test.describe("rollback drawer revision times (PA-172)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a reused ReplicaSet's revision says its time is unknown, not its old creation", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someService(page);
    const digest = "a".repeat(64);
    const candidate = { change_cause: null, containers: [], changes: [], template_digest: digest };
    await page.route(at(`/services/${id}/rollback-candidates`), (r) =>
      json(r, 200, {
        service_id: id, application_id: "app", environment_id: "prod", verification: null, blocked_reason: null, observed_at: "2026-10-17T10:40:00Z",
        workload: { connector_id: "k8s", namespace: "lab", deployment: "checkout-api", uid: "u", paused: false, current_revision: "87", current_template_digest: digest, replicas: { desired: 1, updated: 1, available: 1 } },
        candidates: [
          { ...candidate, revision: "87", current: true, created_at: "2026-10-11T12:37:30Z", at: null, time_basis: "unknown", time_unknown_reason: "replicaset_reused", not_before: "2026-10-16T09:13:00Z", not_after: "2026-10-17T10:35:00Z" },
          { ...candidate, revision: "86", current: false, created_at: "2026-10-16T07:57:00Z", at: "2026-10-16T07:57:00Z", time_basis: "replicaset_created", time_unknown_reason: null, not_before: null, not_after: null, template_digest: "b".repeat(64) },
        ],
      }),
    );
    await page.goto(origin + `/services/${encodeURIComponent(id)}`);
    await page.getByRole("button", { name: "回滚" }).click({ timeout: 20_000 });
    const drawer = page.getByRole("dialog");
    await expect(drawer.getByText("修订 86", { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(drawer).toContainText("时间未知 · 在");
    await expect(drawer).not.toContainText("10月11日");
  });
});

/** An investigation canvas ID, read in the page. */
async function someInvestigation(page: Page): Promise<string> {
  const items = await page.evaluate(async () => {
    const r = await fetch("/api/v1/canvases?kind=investigation", { headers: { "X-AIOps-Environment": "prod" } });
    return ((await r.json()) as { items?: { id: string }[] }).items ?? [];
  });
  test.skip(!items.length, "no investigation canvas");
  return items[0].id;
}

/** Answers GET /investigations/{id}/canvas with the real graph, its investigation patched. */
async function patchCanvas(page: Page, id: string, patch: Record<string, unknown>) {
  const real = (await page.evaluate(async (id) => (await fetch(`/api/v1/investigations/${id}/canvas`, { headers: { "X-AIOps-Environment": "prod" } })).json(), id)) as { investigation: object };
  await page.route(at(`/investigations/${id}/canvas`), (r) => json(r, 200, { ...real, investigation: { ...real.investigation, ...patch } }));
}

test.describe("canvas without a conversation (PA-168)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the chat switch says why there is no conversation", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someInvestigation(page);
    await patchCanvas(page, id, { conversation_id: null });
    await page.goto(origin + `/canvas/${id}`);
    await page.getByRole("radio", { name: "对话" }).or(page.getByRole("button", { name: "对话", exact: true })).first().click({ timeout: 20_000 });
    await expect.poll(async () => (await toasts(page)).join(" ")).toContain("没有对应的对话");
  });
});

test.describe("canvas toolbar as a viewer (PA-177)", () => {
  test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });

  test("fork and write back are disabled with the grant they need", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someInvestigation(page);
    await page.goto(origin + `/canvas/${id}`);
    const toolbar = page.getByRole("toolbar", { name: "画布工具" });
    for (const name of ["派生", "回写"]) await expect(toolbar.getByRole("button", { name })).toBeDisabled({ timeout: 20_000 });
  });
});

test.describe("coverage shares the server can't give (PA-181)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("show a dash, not a bare percent sign", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/coverage"), (r) =>
      json(r, 200, { kpis: [], types: [{ label: "检查用类型", pct: null, tone: "ok" }], columns: [], rows: [{ service: "检查用服务", tier: "T1", cells: {}, score: null }], impact: { answers: "", flagged: "", delta: "", lowered: "", by_type: [], eval_failures: 0 }, fixes: [] }),
    );
    await page.goto(origin + "/integrations/coverage");
    await expect(page.getByText("检查用类型")).toBeVisible({ timeout: 20_000 });
    const text = await page.locator("#root").innerText();
    expect(text).not.toMatch(/(^|\s)%/m);
    expect(text).toContain("—");
  });
});

test.describe("unknown revision time across days (PA-180)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("both bounds carry their date when they fall on different days", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someService(page);
    const digest = "a".repeat(64);
    const now = Date.now();
    const candidate = { change_cause: null, containers: [], changes: [], template_digest: digest, current: true, created_at: "2026-10-11T12:37:30Z", at: null, time_basis: "unknown", time_unknown_reason: "replicaset_reused" };
    await page.route(at(`/services/${id}/rollback-candidates`), (r) =>
      json(r, 200, {
        service_id: id, application_id: "app", environment_id: "prod", verification: null, blocked_reason: null, observed_at: new Date(now).toISOString(),
        workload: { connector_id: "k8s", namespace: "lab", deployment: "checkout-api", uid: "u", paused: false, current_revision: "87", current_template_digest: digest, replicas: { desired: 1, updated: 1, available: 1 } },
        // From yesterday to five minutes ago: the later bound is today.
        candidates: [{ ...candidate, revision: "87", not_before: new Date(now - 26 * 3600_000).toISOString(), not_after: new Date(now - 5 * 60_000).toISOString() }],
      }),
    );
    await page.goto(origin + `/services/${encodeURIComponent(id)}`);
    await page.getByRole("button", { name: "回滚" }).click({ timeout: 20_000 });
    await expect(page.getByRole("dialog")).toContainText(/在 \d+月\d+日 \d\d:\d\d 到 \d+月\d+日 \d\d:\d\d 之间/, { timeout: 20_000 });
  });
});

test.describe("time ranges reach the server (PA-170)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const [route, api, from] of [
    ["/", "/home", "最近 24 小时"],
    ["/settings/audit", "/audit", "最近 7 天"],
    ["/agents/runs", "/agent-runs", "最近 24 小时"],
  ] as const) {
    test(`${route}: picking a range refetches with it`, async ({ page }) => {
      await page.addInitScript(zh);
      await page.goto(origin + route);
      const asked = page.waitForRequest((r) => {
        const u = new URL(r.url());
        return u.pathname === "/api/v1" + api && u.searchParams.get("range") === "1h";
      }, { timeout: 20_000 });
      await page.getByRole("button", { name: from }).first().click({ timeout: 20_000 });
      await page.getByRole("option", { name: "最近 1 小时" }).click();
      await asked;
      await expect(page.locator("#root")).not.toContainText(api === "/audit" ? "最近 7 天" : "最近 24 小时");
    });
  }
});

test.describe("audit share from the data (PA-183)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the agent share is worked out from the counts, not the sample's 67%", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/audit"), (r) => json(r, 200, { items: [], counts: { all: 8, user: 2, agent: 6, system: 0 }, volume: [[0, 0, 0]] }));
    await page.goto(origin + "/settings/audit");
    await expect(page.getByText("Agent 操作占 75%")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#root")).not.toContainText("67%");
    await expect(page.locator("#root")).not.toContainText("全部可追溯到运行 ID");
  });
});

test.describe("inbox tabs without a source (PA-178)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

  test("mentions says why it is empty instead of a blank list", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/inbox");
    await page.getByText("提及", { exact: true }).click({ timeout: 20_000 });
    await expect(page.getByText("提及和订阅还没有来源")).toBeVisible({ timeout: 20_000 });
  });
});

test.describe("empty canvas and template lists (PA-182)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("say there is no data instead of a blank area", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/canvases"), (r) => json(r, 200, { items: [], counts: { all: 0 } }));
    await page.route(at("/investigation-templates"), (r) => json(r, 200, { items: [], counts: { all: 0, official: 0, team: 0 } }));
    await page.goto(origin + "/canvases");
    await expect(page.getByText("没有数据", { exact: true })).toBeVisible({ timeout: 20_000 });
    await page.goto(origin + "/templates");
    await expect(page.getByText("没有数据", { exact: true })).toBeVisible({ timeout: 20_000 });
  });
});

test.describe("source states say the actual cause (PA-171)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

  test("a metrics panel without its query doesn't say Prometheus is missing", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someService(page);
    // The real answer is read again in the page (*.redstring.test resolves only through the browser's host rules),
    // marked so this handler lets that read through, then patched.
    await page.route(at("/metrics/dashboard"), async (r) => {
      if (r.request().headers()["x-check-real"]) return r.fallback();
      const body = (await page.evaluate(async (url) => (await fetch(url, { headers: { "x-check-real": "1", "X-AIOps-Environment": "prod" } })).json(), r.request().url())) as Record<string, unknown>;
      return json(r, 200, { ...body, instances: null, unavailable: { ...((body.unavailable as object) ?? {}), instances: "not_configured" } });
    });
    await page.goto(origin + `/metrics?service=${encodeURIComponent(id)}`);
    await expect(page.getByText("Prometheus已接入，但还没有为这一项配置查询。").first()).toBeVisible({ timeout: 30_000 });
    await expect(page.locator("#root")).not.toContainText("还没有连接Prometheus");
  });

  test("resilience without sources says not connected, not a failed source", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someService(page);
    await page.goto(origin + `/services/${encodeURIComponent(id)}/resilience`);
    await expect(page.getByText("未接入", { exact: false }).first()).toBeVisible({ timeout: 30_000 });
    await expect(page.locator("#root")).not.toContainText("来源连接失败或超时");
  });
});

test.describe("manual service registration (PA-175)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a bad name is caught with its format, and a refusal is worded", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/services/new");
    await page.getByRole("radio", { name: "手动填写" }).click({ timeout: 20_000 });
    const name = page.getByPlaceholder("recommendation-api");
    await name.fill("bad name!");
    await expect(page.getByText("只能用字母、数字、点、下划线和连字符")).toBeVisible();
    await expect(page.getByRole("button", { name: "登记服务" })).toBeDisabled();
    // A refusal the form can't foresee is still said in the page language.
    await page.route(at("/services"), (r) => (r.request().method() === "POST" ? json(r, 422, { error: { code: "invalid_service", message: "duplicate service in request" } }) : r.fallback()));
    await name.fill("check-api");
    await page.getByRole("button", { name: "登记服务" }).click();
    await expect.poll(async () => (await toasts(page)).join(" ")).toContain("同一个服务填了两次");
  });
});
