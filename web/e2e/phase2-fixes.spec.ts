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
