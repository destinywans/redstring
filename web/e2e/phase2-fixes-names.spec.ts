import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, patchRead, read, zh } from "./support/checks";

/**
 * Names instead of internal IDs (PA-111, PA-166) and the Runbook dry-run copy (PA-112). Every check answers the API
 * with records that carry made-up internal IDs (page.route) and a catalog that names them, so it runs on any data and
 * never writes; the page has to show the names and none of the IDs.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-names.spec.ts
 */
const origin = INSTANCES.prod.origin;

/** A catalog with one service: catalog IDs are base64url of "application NUL environment". */
const APP = "app-names-check";
const SERVICE = Buffer.from(`${APP}\u0000prod`).toString("base64url");
const NAME = "名称检查服务";
const CATALOG = {
  kpis: [],
  facets: [],
  items: [{ id: SERVICE, name: NAME, desc: "", tier: null, team: null, oncall: null, status: null, slo: { target: null, budget_left: null }, release: null, alerts: null, connectors: null, unavailable: {} }],
};
const routeCatalog = (page: Page) => page.route(at("/services"), (r) => json(r, 200, CATALOG));

const OWNER = "usr-names-check-owner";
const OWNER_NAME = "名称检查成员";
const MEMBERS = {
  org: [],
  groups: [],
  items: [{ id: OWNER, name: OWNER_NAME, email: "owner@names.check", department: "", roles: [], phone: "", mfa: "none", source: "system", status: "active", last_login: "" }],
  counts: { all: 1, admin: 0, disabled: 0, no_mfa: null },
  mfa_grace_days: null,
};

/** The instance's label for prod, read in the page. Call after page.goto(origin + "/healthz"). */
async function prodLabel(page: Page): Promise<string> {
  const { body } = await read(page, "/instance");
  const label = ((body?.environments ?? []) as { id: string; label: string }[]).find((e) => e.id === "prod")?.label;
  test.skip(!label, "the instance has no prod environment");
  return label!;
}

/* ---------------- PA-111 / PA-112: Runbook scope and dry-run ---------------- */

const RUNBOOK = "rb-names-check";
const RUNBOOK_SCOPE = `app:${APP} · env:prod`;
const RUNBOOK_LIST = {
  items: [{ id: RUNBOOK, title: "名称检查 Runbook", tags: [], scope: RUNBOOK_SCOPE, owner: "检查人", version: "v1", status: "draft", refs_30d: null, resolve_rate: null, last_verified: null, stale: null, environment_ids: ["prod"], application_id: APP, team_id: "" }],
  counts: { all: 1, mine: 0, review: 0, stale: 0 },
  kpis: null,
  suggestions: null,
};
const RUNBOOK_DETAIL = {
  id: RUNBOOK, title: "名称检查 Runbook", version: "v1", status: "draft", base: "blank", source_kind: "blank", source_ref: "", owner: "检查人",
  scope: RUNBOOK_SCOPE, environment_ids: ["prod"], application_id: APP, team_id: "", tags: [], when: "检查用", steps: [], reviewers: [], versions: [],
  usage: { refs: null, resolve_rate: null, weekly: null, highlight: null }, incidents: null, last_verified: null,
  complete_v3_dto: false, partial: true, unknown_fields: [],
};

test.describe("runbook scope names (PA-111)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the list and the detail show the service and environment names, not app:<ID> · env:prod", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const env = await prodLabel(page);
    await routeCatalog(page);
    await page.route(at("/runbooks"), (r) => json(r, 200, RUNBOOK_LIST));
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, RUNBOOK_DETAIL));
    await page.goto(origin + "/runbooks");
    await expect(page.getByText(`${NAME} · ${env}`, { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#root")).not.toContainText("app:");
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    await expect(page.getByText(`${NAME} · ${env}`, { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#root")).not.toContainText(APP);
  });
});

test.describe("runbook dry-run copy (PA-112)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("on the production instance the dry run is described as a step check, not a staging walkthrough", async ({ page }) => {
    await page.addInitScript(zh);
    await routeCatalog(page);
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, RUNBOOK_DETAIL));
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    // Not clicked: the check saves an audited preview.
    await expect(page.getByRole("button", { name: "校验只读步骤" })).toBeVisible({ timeout: 20_000 });
    const side = page.locator(".v3-split > .aside");
    await expect(side).toContainText("不连接任何系统，也不执行步骤");
    await expect(side).not.toContainText("预发");
  });
});

/* ---------------- PA-166: agent owner and version dates ---------------- */

/** An agent ID from the lifecycle list, or skip. */
async function someAgent(page: Page): Promise<string> {
  const { body } = await read(page, "/agents/lifecycle");
  const id = ((body?.agents ?? []) as { id?: string }[]).map((a) => a.id).find(Boolean);
  test.skip(!id, "no agent");
  return id!;
}
const CHECK_VERSION = { version: "v-names-check", status: "draft", traffic: null, configured_traffic: null, score: null, success: null, tokens: null, delta: null, created: "2026-10-16T06:03:00Z" };
/** Answers GET /agents/{id} with the real detail, its owner replaced and a check version listed first. */
const patchAgent = (page: Page, id: string, owner: string) => patchRead(page, `/agents/${id}`, (real) => ({ ...real, owner, versions: [CHECK_VERSION, ...(real?.versions ?? [])] }));

test.describe("agent owner and version dates (PA-166)", () => {
  // The browser speaks English while the app is set to Chinese: dates must follow the app.
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 }, locale: "en-US" });

  test("the owner's user ID resolves to the member name; version dates use the app language", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someAgent(page);
    await patchAgent(page, id, OWNER);
    await page.route(at("/members"), (r) => json(r, 200, MEMBERS));
    await page.goto(origin + `/agents/${encodeURIComponent(id)}`);
    await expect(page.getByText(`负责人 ${OWNER_NAME}`)).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#root")).not.toContainText(OWNER);
    const row = page.locator("tr", { hasText: CHECK_VERSION.version });
    await expect(row).toContainText(/10月1[67]日 \d{2}:03/);
    await expect(row).not.toContainText("2026");
  });

  test("without members.view the owner isn't shown as an ID and the member list isn't read", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/healthz");
    const id = await someAgent(page);
    await patchAgent(page, id, OWNER);
    await patchRead(page, "/me", (me) => ({ ...me, permissions: { prod: { ...(me?.permissions?.prod ?? {}), members: {} } } }));
    await page.goto(origin + `/agents/${encodeURIComponent(id)}`);
    await expect(page.getByText("负责人 其他成员")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#root")).not.toContainText(OWNER);
    await m.quiet();
    expect(m.api.filter((c) => c.path.startsWith("/members"))).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  test("the viewer's own ID resolves from /me without members.view", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someAgent(page);
    const me = (await read(page, "/me")).body as { id: string; name: string };
    await patchAgent(page, id, me.id);
    await patchRead(page, "/me", (real) => ({ ...real, permissions: { prod: { ...(real?.permissions?.prod ?? {}), members: {} } } }));
    await page.goto(origin + `/agents/${encodeURIComponent(id)}`);
    await expect(page.getByText(`负责人 ${me.name}`)).toBeVisible({ timeout: 20_000 });
  });
});

/* ---------------- PA-166: new incident drawer ---------------- */

test.describe("new incident drawer services (PA-166)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the drafted service and an added one show their catalog names", async ({ page }) => {
    await page.addInitScript(zh);
    await routeCatalog(page);
    // The draft names its service by application ID.
    await page.route(at("/incidents/draft"), (r) =>
      json(r, 200, {
        title: "检查用事件", severity: "P2", severity_hint: "", env: "prod", services: [{ id: APP, tier: null }], impact: null,
        alerts: [{ id: "alert-check", severity: "P2", text: "LabHigh5xxRate", at: "2026-10-17T02:00:00Z" }],
        commander: null, participants: null, war_room: null,
      }),
    );
    await page.goto(origin + "/incidents?new=incident&alerts=alert-check");
    const drawer = page.getByRole("dialog", { name: "新建事件" });
    await expect(drawer.getByText(NAME, { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(drawer).not.toContainText(APP);
    await drawer.getByRole("button", { name: "服务", exact: true }).click();
    await page.locator(".demo-menu button", { hasText: NAME }).click();
    await expect(drawer.locator(".demo-added")).toHaveText(NAME);
    await expect(drawer).not.toContainText(SERVICE);
  });
});

/* ---------------- PA-166: conversations ---------------- */

/** A chat conversation ID, or skip. */
async function someConversation(page: Page): Promise<string> {
  const { body } = await read(page, "/conversations");
  const id = ((body?.items ?? []) as { id?: string; mode?: string }[]).find((c) => c.mode === "chat")?.id;
  test.skip(!id, "no chat conversation");
  return id!;
}

test.describe("conversation scope names (PA-166)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the header, the side panel and open as canvas show the service name", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someConversation(page);
    await routeCatalog(page);
    await patchRead(page, `/conversations/${id}`, (real) => ({ ...real, investigation_id: null, scope: { service: SERVICE, env: "prod", inferred: false } }));
    await page.goto(origin + `/conversations/${id}`);
    await expect(page.locator(".conv-side").getByText(NAME, { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(".v3-topbar .v3-badge", { hasText: NAME })).toBeVisible();
    await expect(page.locator("#root")).not.toContainText(SERVICE);
    // The new investigation canvas starts with the conversation's service.
    await page.getByRole("link", { name: "转为画布" }).click();
    await expect(page).toHaveURL(/\/canvases\?new=investigation/);
    const dialog = page.getByRole("dialog");
    await expect(dialog.locator(".demo-added")).toHaveText(NAME, { timeout: 20_000 });
    await expect(dialog).not.toContainText(SERVICE);
  });

  test("the list's scope line shows the service and environment names", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const env = await prodLabel(page);
    await routeCatalog(page);
    await page.route(at("/conversations"), (r) =>
      json(r, 200, {
        items: [{ id: "conv-names-check", title: "名称检查对话", mode: "chat", status: "active", source: "manual", scope: `${SERVICE} · prod`, participants: [], size: "1 轮", updated_at: "2026-10-17T10:00:00Z", tokens: 1000 }],
        total: 1, page: 1, page_size: 50, stats: [], counts: { mine: 1, team: 0, triage: 0, archived: 0 },
      }),
    );
    await page.goto(origin + "/conversations");
    await expect(page.getByText(`${NAME} · ${env}`, { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#root")).not.toContainText(SERVICE);
  });
});

/* ---------------- PA-166: metrics dashboard ---------------- */

test.describe("metrics dashboard names (PA-166)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the dashboard title and the service label show the service name", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    // Any real service's dashboard stands in for the check service's (it carries no service ID).
    const real = ((await read(page, "/services")).body?.items ?? []) as { id: string }[];
    const app = real.length ? Buffer.from(real[0].id, "base64url").toString().split("\u0000")[0] : "";
    const dash = app ? await read(page, `/metrics/dashboard?service=${encodeURIComponent(app)}&range=1h`) : null;
    test.skip(dash?.status !== 200, "no readable metrics dashboard");
    await routeCatalog(page);
    await page.route(at("/metrics/dashboard"), (r) => json(r, 200, dash!.body));
    await page.goto(origin + `/metrics?service=${SERVICE}`);
    await expect(page.locator(".v3-labelchip", { hasText: "service" })).toContainText(NAME, { timeout: 20_000 });
    await expect(page.getByText(`${NAME} 黄金信号`)).toBeVisible();
    await expect(page.locator("#root")).not.toContainText(SERVICE);
  });
});

/* ---------------- PA-166: agent runs ---------------- */

test.describe("agent run scope names (PA-166)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("a run's scope shows the environment and service names", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const env = await prodLabel(page);
    await routeCatalog(page);
    const row = {
      id: "run-names-check", agent: "名称检查 Agent", agent_id: "agent-names-check", application_id: APP, environment_id: "prod", version: "v1", trigger: "manual",
      scope: `prod / ${APP}`, status: "success", steps: null, tokens: null, duration_ms: null, started_at: "2026-10-17T10:00:00Z", feedback: null,
    };
    await patchRead(page, "/agent-runs", (real) => {
      const base = Array.isArray(real?.items) ? real : { kpis: [], trend: null, durations: null, counts: null, items: [] };
      return { ...base, items: [row, ...base.items] };
    });
    await page.goto(origin + "/agents/runs");
    await expect(page.getByText(`${env} / ${NAME}`, { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#root")).not.toContainText(APP);
  });
});
