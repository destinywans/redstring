import { test, expect, type Page } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, read, zh } from "./support/checks";

/**
 * Remaining parts of the sweep fixes (WAN-434): knowledge search hits without a vector score (PA-222) and the add
 * connector wizard / models page defaults (PA-224). Every read and write is pinned with page.route, so this runs on
 * any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-rest-connectors.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

/**
 * PA-222 (WAN-581): in a hybrid search, a hit from a public web page or an upload has no vector index; the server says
 * vector=null with its corpus, and the page marks the score unknown instead of “vector 0.00”.
 */
test.describe("knowledge search hits without a vector score (PA-222)", () => {
  test("web and upload hits in a hybrid search don't print vector 0.00", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const hits = [
      { title: "检查文档", snippet: "文档片段", keyword: 1, vector: 0.42, score: 0.65, corpus: "documents" },
      { title: "检查网页", snippet: "网页片段", keyword: 0.8, vector: null, score: 0.32, corpus: "public_web_sources" },
      { title: "检查上传", snippet: "上传片段", keyword: 0.6, vector: null, score: 0.24, corpus: "upload_sources" },
    ];
    await page.route(at("/knowledge/search"), (r) =>
      r.request().method() === "POST" ? json(r, 200, { hits, mode: "hybrid", degraded: false, corpora: [] }) : r.fallback(),
    );
    await page.goto(origin + "/knowledge-sources");
    await page.getByRole("textbox", { name: "检索问题" }).fill("检查", { timeout: 20_000 });
    await page.getByRole("button", { name: "检索", exact: true }).click();
    const doc = page.locator(".ret-hit", { hasText: "检查文档" });
    await expect(doc).toContainText("向量 0.42");
    await expect(doc.locator(".unk")).toHaveCount(0);
    for (const title of ["检查网页", "检查上传"]) {
      const hit = page.locator(".ret-hit", { hasText: title });
      await expect(hit).toContainText("关键词");
      await expect(hit).not.toContainText("向量 0.00");
      await expect(hit.locator(".unk")).toHaveCount(1);
      // Nothing failed: these sources have no vector index, so the mark doesn't say the source is unavailable.
      await expect(hit.locator(".unk")).not.toHaveAttribute("aria-label", /暂时不可用/);
    }
    expect(m.pageErrors).toEqual([]);
  });

  test("a document hit outside the vector candidates says so instead of vector 0.00", async ({ page }) => {
    await page.addInitScript(zh);
    const hits = [
      { title: "检查候选", snippet: "候选片段", keyword: 1, vector: 0.42, score: 0.65, corpus: "documents" },
      // Found by keyword only: its vector similarity was never computed (below the candidate line), not 0.
      { title: "检查仅关键词", snippet: "关键词片段", keyword: 0.75, vector: null, score: 0.3, corpus: "documents" },
    ];
    await page.route(at("/knowledge/search"), (r) =>
      r.request().method() === "POST" ? json(r, 200, { hits, mode: "hybrid", degraded: false, corpora: [] }) : r.fallback(),
    );
    await page.goto(origin + "/knowledge-sources");
    await page.getByRole("textbox", { name: "检索问题" }).fill("检查", { timeout: 20_000 });
    await page.getByRole("button", { name: "检索", exact: true }).click();
    const hit = page.locator(".ret-hit", { hasText: "检查仅关键词" });
    await expect(hit).not.toContainText("向量 0.00");
    await expect(hit.locator(".unk")).toHaveAttribute("aria-label", /未进入向量候选/);
  });
});

/**
 * PA-224 (WAN-583): the add connector wizard listed types the server doesn't register (reading them failed), and
 * created every type with GitLab's tool names, which the server refuses for any other type (422 invalid_tools); the
 * models page selected the sample ID openai:gpt-5 on real data.
 *
 * The types the server registers and the read-only tools it accepts for each, as in internal/v3integrations/catalog.go
 * (the wizard offers the ones that also have a place in its grid).
 */
const SERVER_TOOLS: Record<string, string[]> = {
  gitlab: ["list_pipelines", "get_pipeline", "get_commit", "get_commit_diff", "list_merge_requests", "list_deployments"],
  jenkins: ["list_builds"],
  argo: ["get_app_status"],
  prometheus: ["query_metrics"],
  elasticsearch: ["search_logs"],
  grafana: ["list_dashboards"],
  kubernetes: ["read_workloads"],
  vault: [],
};
const SHOWN = [["gitlab", "GitLab"], ["jenkins", "Jenkins"], ["argo", "Argo CD"], ["prometheus", "Prometheus"], ["elasticsearch", "Elasticsearch"], ["grafana", "Grafana"], ["kubernetes", "Kubernetes"], ["vault", "Vault"]] as const;
const HIDDEN = [["githubactions", "GitHub Actions"], ["mcp", "MCP"], ["slack", "Slack"], ["confluence", "Confluence"]] as const;
const AUTH: Record<string, string[]> = {
  gitlab: ["oauth", "pat"], jenkins: ["basic", "bearer"], argo: ["bearer"], prometheus: ["none", "bearer"], elasticsearch: ["api_key", "bearer", "basic"],
  grafana: ["none", "bearer"], kubernetes: ["bearer"], vault: ["bearer"],
};
/** GET /connector-types/{type} as the server answers it: no tools, scopes or webhook address are advertised. */
const connectorType = (type: string) => ({
  brand: type, name: SHOWN.find(([id]) => id === type)?.[1] ?? type, kind: "检查", version: "",
  auth_methods: (AUTH[type] ?? []).map((id) => ({ id, label: `认证 ${id}`, desc: "" })), scopes: [], tools: [], webhook_url: "",
});
const typeGrid = (page: Page) => page.locator(".type-grid button");
/** A type button's text ends with its name (a brand without an icon shows two letters before it). */
const named = (name: string) => new RegExp(`${name}$`);

test.describe("add connector wizard follows the server's connector types (PA-224)", () => {
  test("the type list offers only the types the server registers", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    // The real server: every offered type reads, the dropped ones don't exist there.
    for (const [type] of SHOWN) expect((await read(page, `/connector-types/${type}`)).status, type).toBe(200);
    for (const [type] of HIDDEN) expect((await read(page, `/connector-types/${type}`)).status, type).toBe(404);
    await page.goto(origin + "/integrations/new");
    await page.getByRole("button", { name: "上一步" }).click({ timeout: 20_000 });
    await expect(typeGrid(page)).toHaveText(SHOWN.map(([, name]) => named(name)));
  });

  test("creating each type sends only tools the server accepts for that type", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route((url) => url.pathname.startsWith("/api/v1/connector-types/"), (r) =>
      json(r, 200, connectorType(decodeURIComponent(new URL(r.request().url()).pathname.split("/").pop() ?? ""))),
    );
    const sent: Record<string, unknown>[] = [];
    await page.route(at("/connectors"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      sent.push(r.request().postDataJSON());
      return json(r, 201, { id: "conn-pa224-check" });
    });
    for (const [type, name] of SHOWN) {
      await page.goto(origin + "/integrations/new");
      await page.getByRole("button", { name: "上一步" }).click({ timeout: 20_000 });
      await typeGrid(page).filter({ hasText: named(name) }).click();
      await page.getByRole("button", { name: /下一步/ }).click();
      await page.getByRole("textbox", { name: /地址/ }).fill("https://check.example.invalid");
      await page.getByRole("radio").first().check();
      for (let i = 0; i < 3; i++) await page.getByRole("button", { name: /下一步/ }).click();
      const posted = page.waitForRequest((q) => q.method() === "POST" && new URL(q.url()).pathname === "/api/v1/connectors");
      await page.getByRole("button", { name: "创建连接器" }).click();
      await posted;
      const body = sent.at(-1) as { type: string; tools: string[] };
      expect(body.type).toBe(type);
      expect(body.tools.filter((tool) => !SERVER_TOOLS[type].includes(tool)), `${type} tools`).toEqual([]);
    }
    // GitLab keeps the four tools it was created with before.
    expect((sent.find((b) => b.type === "gitlab") as { tools: string[] }).tools.sort()).toEqual(["get_commit_diff", "get_pipeline", "list_merge_requests", "list_pipelines"]);
    expect(m.pageErrors).toEqual([]);
  });
});

test.describe("add connector wizard starts each type on an auth method it accepts (PA-224)", () => {
  test("after choosing a type one of its auth methods is selected, and create sends it", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route((url) => url.pathname.startsWith("/api/v1/connector-types/"), (r) =>
      json(r, 200, connectorType(decodeURIComponent(new URL(r.request().url()).pathname.split("/").pop() ?? ""))),
    );
    const sent: { type: string; auth: string }[] = [];
    await page.route(at("/connectors"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      sent.push(r.request().postDataJSON());
      return json(r, 201, { id: "conn-pa224-auth" });
    });
    for (const [type, name] of SHOWN) {
      await page.goto(origin + "/integrations/new");
      await page.getByRole("button", { name: "上一步" }).click({ timeout: 20_000 });
      await typeGrid(page).filter({ hasText: named(name) }).click();
      await page.getByRole("button", { name: /下一步/ }).click();
      // Nobody picks a method here: the form must already hold one the type accepts.
      await expect(page.getByRole("radio", { checked: true }), `${type} auth`).toHaveCount(1);
      await page.getByRole("textbox", { name: /地址/ }).fill("https://check.example.invalid");
      for (let i = 0; i < 3; i++) await page.getByRole("button", { name: /下一步/ }).click();
      const posted = page.waitForRequest((q) => q.method() === "POST" && new URL(q.url()).pathname === "/api/v1/connectors");
      await page.getByRole("button", { name: "创建连接器" }).click();
      await posted;
      expect(AUTH[type], `${type} sent auth`).toContain(sent.at(-1)!.auth);
    }
  });
});

test.describe("models page selection on real data (PA-224)", () => {
  test("selects the environment's default model, never the sample ID", async ({ page }) => {
    await page.addInitScript(zh);
    const model = (id: string, name: string, isDefault: boolean) => ({
      id, name, provider: "检查", brand: "openai", connector: "check", api: "chat_completions", reasoning: { kind: "none", levels: [] },
      tools: true, vision: false, context_k: 64, cost: "—", available: true, used_by: "检查", scope: "检查范围", status: "unverified", is_default: isDefault,
    });
    await page.route(at("/models/admin"), (r) =>
      json(r, 200, {
        providers: [{ brand: "openai", name: "检查提供方", api: "Chat Completions", models: 2, status: "unverified" }],
        items: [model("check:first", "检查模型甲", false), model("check:default", "检查默认模型", true)],
        detail: { model: "check:default", api_chain: [{ api: "Chat Completions API", note: "检查接口" }], mapping: [], checks: [], roles: "检查角色", agents: "检查 Agent", limits: "检查限额" },
      }),
    );
    await page.goto(origin + "/integrations/models");
    await expect(page.locator(".v3-table tbody tr.on")).toContainText("检查默认模型", { timeout: 20_000 });
    await expect(page.locator(".v3-table tbody tr.on")).toHaveCount(1);
    await expect(page.getByText("检查接口", { exact: true })).toBeVisible();
    await expect(page.locator(".v3-page-body")).not.toContainText("openai:gpt-5");
  });
});
