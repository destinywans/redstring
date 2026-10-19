import { test, expect, type Page, type Route } from "@playwright/test";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";

/**
 * Regression checks for the defects fixed during the WAN-434 phase 1 sweep. Each check pins the page to fixed API
 * answers with page.route, so it runs on any data and never writes: the real-data verification is in the registry
 * (docs/releases/2026-10-16-defect-convergence.md).
 *
 *   npx playwright test -c e2e/playwright.config.ts phase1-fixes.spec.ts
 */
const origin = INSTANCES.prod.origin;
const zh = () => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" }));
const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
/** Text of every toast shown so far. */
const toasts = (page: Page) => page.locator(".v3-toast").allInnerTexts();
/** Matches one API path exactly, with or without a query string. */
const at = (path: string) => (url: URL) => url.pathname === "/api/v1" + path;

const PM_DRAFT = {
  incident: { id: "INC-check", title: "LabHigh5xxRate", severity: "P2", duration: null, started_at: null, closed_at: null },
  sections: [{ key: "summary", title: "Incident summary", text: "" }],
  timeline: [{ at: "2026-10-16T11:38:52Z", text: "incident.mitigated", tone: "info" }],
  actions: [],
  source_revision: "rev-check-1",
};

test.describe("new postmortem (PA-127, PA-129)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("saving echoes the previewed source and shows the server's refusal; no sample numbers or checks", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    let sent: Record<string, unknown> | null = null;
    await page.route(at("/postmortems/draft"), (r) => json(r, 200, PM_DRAFT));
    await page.route(at("/postmortems"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      sent = r.request().postDataJSON();
      return json(r, 409, { error: { code: "source_changed", message: "检查用：事件刚刚变化" } });
    });
    await page.goto(origin + "/postmortems/new?incident=INC-check");
    await expect(page.getByText("INC-check 复盘：LabHigh5xxRate")).toBeVisible({ timeout: 20_000 });
    // PA-129: the sample's impact numbers, AI checks, publish targets and runbook follow-up stay in the sample data.
    const body = await page.locator("#root").innerText();
    for (const sample of ["12k", "23%", "根因引用了证据", "Confluence", "生成 Runbook 修订", "AI 已起草"]) expect.soft(body, sample).not.toContain(sample);
    // PA-127: the save carries source_revision, and a refusal is shown instead of an uncaught error.
    await page.getByRole("button", { name: "保存草稿" }).click();
    await expect.poll(() => toasts(page)).toContain("检查用：事件刚刚变化");
    expect(sent).toMatchObject({ incident_id: "INC-check", source_revision: "rev-check-1", status: "draft" });
    expect(m.pageErrors).toEqual([]);
  });
});

test.describe("new postmortem as an operator (PA-127)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

  test("saving is gated on knowledge.manage", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/postmortems/draft"), (r) => json(r, 200, PM_DRAFT));
    await page.goto(origin + "/postmortems/new?incident=INC-check");
    await expect(page.getByText("INC-check 复盘：LabHigh5xxRate")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: "保存草稿" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "提交审阅" })).toBeDisabled();
  });
});

test.describe("new runbook (PA-124)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("drafts from a published postmortem, keeps the typed title and sends the source hash", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    let sent: Record<string, unknown> | null = null;
    let draftedFrom = "";
    const pms = [
      { id: "PM-draft", title: "draft one", status: "draft", date: null, incident_id: "INC-a" },
      { id: "PM-check", title: "LabHigh5xxRate", status: "published", date: "2026-10-09T17:59:51Z", incident_id: "INC-b" },
    ];
    await page.route(at("/postmortems"), (r) => (r.request().method() === "GET" ? json(r, 200, { items: pms }) : r.fallback()));
    await page.route(at("/runbooks/draft"), (r) => {
      const u = new URL(r.request().url());
      draftedFrom = `${u.searchParams.get("source")}:${u.searchParams.get("from")}`;
      if (u.searchParams.get("source") !== "postmortem") return json(r, 422, { error: { code: "invalid_source", message: "检查用：缺少来源" } });
      return json(r, 200, { title: "Runbook from postmortem INC-b", applies_to: [], trigger: "", sources: ["postmortem:PM-check"], source_sha256: "sha-check",
        steps: [{ kind: "check", title: "Review: Impact", body: "x" }, { kind: "action", title: "加回滚门禁", approval: true }] });
    });
    await page.route(at("/runbooks"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      sent = r.request().postDataJSON();
      return json(r, 201, { id: "rb-check" });
    });
    await page.goto(origin + "/runbooks/new");
    // The start dialog lists published postmortems only, by the postmortem's ID.
    await expect(page.getByText("INC-b · LabHigh5xxRate")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("INC-a · draft one")).toHaveCount(0);
    await page.getByRole("button", { name: "开始起草" }).click();
    await expect.poll(() => draftedFrom).toBe("postmortem:PM-check");
    await expect(page.getByText("加回滚门禁")).toBeVisible();
    await expect(page.getByText("操作", { exact: true }).first()).toBeVisible();
    const text = await page.locator("#root").innerText();
    for (const sample of ["判断阈值来自单次事件", "数据库慢查询", "team=trade"]) expect.soft(text, sample).not.toContain(sample);
    await page.getByLabel("标题").fill("检查用：改过的标题");
    await page.getByRole("button", { name: "保存草稿" }).click();
    await expect.poll(() => toasts(page)).toContain("草稿已保存");
    expect(sent).toMatchObject({ title: "检查用：改过的标题", source: "postmortem", from: "PM-check", expected_source_sha256: "sha-check", status: "draft" });
    // A starting point the server can't draft from says so instead of showing an empty "AI drafted" editor.
    await page.goto(origin + "/runbooks/new?source=template");
    await expect(page.getByText("没能从这个来源起草：检查用：缺少来源")).toBeVisible({ timeout: 20_000 });
    expect(m.pageErrors).toEqual([]);
  });

  test("a blank runbook saves the typed title", async ({ page }) => {
    await page.addInitScript(zh);
    let sent: Record<string, unknown> | null = null;
    await page.route(at("/runbooks"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      sent = r.request().postDataJSON();
      return json(r, 422, { error: { code: "invalid_runbook", message: "检查用：保存被拒" } });
    });
    await page.goto(origin + "/runbooks/new?source=blank");
    await page.getByLabel("标题").fill("检查用：空白标题");
    await page.getByRole("button", { name: "保存草稿" }).click();
    await expect.poll(() => toasts(page)).toContain("检查用：保存被拒");
    expect(sent).toMatchObject({ title: "检查用：空白标题", source: "blank", status: "draft" });
    expect(sent).not.toHaveProperty("from");
  });
});

/** The newest alert in the operator's inbox, as the inbox lists it; the checks patch its detail with page.route. */
async function someAlert(page: Page): Promise<string> {
  // In the page, so *.redstring.test resolves through the browser's host rules and the session cookie applies.
  const items = await page.evaluate(async () => {
    const r = await fetch("/api/v1/inbox", { headers: { "X-AIOps-Environment": "prod" } });
    return (((await r.json()) as { items?: { alert_id?: string }[] }).items ?? []);
  });
  const id = items.find((i) => i.alert_id)?.alert_id;
  test.skip(!id, "no alert in the inbox to open");
  return id!;
}

/** Answers GET /alerts/{id} with the real detail (read in the page, like someAlert), patched. */
async function patchAlert(page: Page, id: string, patch: Record<string, unknown>) {
  const real = await page.evaluate(async (id) => (await fetch(`/api/v1/alerts/${id}`, { headers: { "X-AIOps-Environment": "prod" } })).json(), id);
  await page.route(at(`/alerts/${id}`), (r) => json(r, 200, { ...(real as object), ...patch }));
}

test.describe("inbox claim (PA-117, PA-118)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

  test("a resolved alert can't be claimed; a claim says only what happened", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await someAlert(page);
    await patchAlert(page, id, { state: "resolved", assignee_id: null });
    await page.goto(origin + `/inbox?item=alert%3A${id}`);
    await expect(page.getByRole("button", { name: "认领" })).toBeDisabled({ timeout: 20_000 });

    await page.unroute(at(`/alerts/${id}`));
    await patchAlert(page, id, { state: "firing", assignee_id: null });
    let claims = 0;
    await page.route(at(`/alerts/${id}/claim`), (r) => (claims++ === 0 ? json(r, 200, {}) : json(r, 409, { error: { code: "already_claimed_or_closed", message: "Alert is assigned to another user or no longer active" } })));
    await page.goto(origin + `/inbox?item=alert%3A${id}`);
    await page.getByRole("button", { name: "认领" }).click({ timeout: 20_000 });
    await expect.poll(() => toasts(page)).toContain("已认领");
    expect((await toasts(page)).join(" ")).not.toContain("值班升级");
    // A claim someone else won is said in the interface language, not the server's English.
    await page.goto(origin + `/inbox?item=alert%3A${id}`);
    await page.getByRole("button", { name: "认领" }).click({ timeout: 20_000 });
    await expect.poll(() => toasts(page)).toContain("这条告警已被他人认领或已经结束");
  });
});

test.describe("alert group actions (PA-120)", () => {
  test.describe("as an operator", () => {
    test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
    test("item outcomes are worded, never raw codes", async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at("/alert-groups/bulk"), (r) => {
        const { ids, action } = r.request().postDataJSON() as { ids: string[]; action: string };
        return json(r, 207, { action, results: ids.map((id) => ({ id, outcome: action === "investigate" ? "conflict" : "not_connected", matched: 1, changed: 0, investigation_id: null, proposal_id: null, error: action === "investigate" ? "conflict" : "not_connected" })) });
      });
      await page.goto(origin + "/alerts");
      await page.getByText("已恢复", { exact: true }).click({ timeout: 20_000 });
      const box = page.locator(".alert-group input[type=checkbox]").first();
      test.skip(!(await box.waitFor({ state: "visible", timeout: 10_000 }).then(() => true, () => false)), "no alert group to select");
      await box.check();
      await page.locator(".bulk-bar button", { hasText: "发起调查" }).click();
      await expect.poll(async () => (await toasts(page)).join(" ")).toContain("请从事件页发起调查");
      await box.check();
      await page.locator(".bulk-bar button", { hasText: "提议静默" }).click();
      await expect.poll(async () => (await toasts(page)).join(" ")).toContain("请在静默页按条件提议静默");
      const all = (await toasts(page)).join(" ");
      expect(all).not.toMatch(/conflict|not_connected/);
    });
  });
  test.describe("as a viewer", () => {
    test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });
    test("group actions are disabled with the grant they need", async ({ page }) => {
      await page.addInitScript(zh);
      await page.goto(origin + "/alerts");
      await page.getByText("已恢复", { exact: true }).click({ timeout: 20_000 });
      const box = page.locator(".alert-group input[type=checkbox]").first();
      test.skip(!(await box.waitFor({ state: "visible", timeout: 10_000 }).then(() => true, () => false)), "no alert group to select");
      await box.check();
      for (const name of ["认领", "提议静默", "发起调查"]) await expect(page.locator(".bulk-bar button", { hasText: name })).toBeDisabled();
    });
  });
});

test.describe("proposal discussion (PA-122)", () => {
  test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });
  test("a role without proposals.change can't type a comment", async ({ page }) => {
    const file = resolve(LOCAL, "e2e/golden/silence-proposal.txt");
    test.skip(!existsSync(file), "no proposal from golden-silence-ui yet");
    await page.addInitScript(zh);
    await page.goto(origin + "/review/" + readFileSync(file, "utf8").trim());
    await expect(page.getByLabel("回复讨论")).toBeDisabled({ timeout: 20_000 });
  });
});

test.describe("missing records (PA-125)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("an execution that isn't there says not found, not a source failure", async ({ page }) => {
    await page.addInitScript(zh);
    test.setTimeout(120_000);
    await page.goto(origin + "/executions/00000000000000000000000000000000");
    // The page keeps asking for a minute (a just-approved execution may not be recorded yet), then says so.
    await expect(page.getByText("找不到这条记录")).toBeVisible({ timeout: 90_000 });
    await expect(page.getByText("来源连接失败")).toHaveCount(0);
  });
});

test.describe("alert group names (PA-130)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("groups show the service by name, not the application ID", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/alerts");
    await page.getByText("已恢复", { exact: true }).click({ timeout: 20_000 });
    const head = page.locator(".alert-group .ag-head").first();
    test.skip(!(await head.waitFor({ state: "visible", timeout: 10_000 }).then(() => true, () => false)), "no alert group to read");
    // The row shows the raw ID until the service catalog has loaded; the name must replace it.
    await expect.poll(() => head.innerText(), { timeout: 15_000, message: "a 32-hex application ID in the group row" }).not.toMatch(/\b[0-9a-f]{32}\b/);
  });

  test("groups name their alert (PA-130 remainder)", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/alerts");
    const groups = page.waitForResponse((r) => { const u = new URL(r.url()); return u.pathname === "/api/v1/alert-groups" && u.searchParams.get("state") === "resolved" && r.status() === 200; }, { timeout: 20_000 });
    await page.getByText("已恢复", { exact: true }).click({ timeout: 20_000 });
    const first = ((await (await groups).json()) as { items: { alert_name?: string | null }[] }).items[0];
    test.skip(!first, "no alert group to read");
    expect(first.alert_name, "alert_name on the first group").toBeTruthy();
    await expect(page.locator(".alert-group .ag-head").first()).toContainText(first.alert_name!, { timeout: 15_000 });
  });
});

test.describe("silences when Alertmanager can't be read (PA-131, PA-132)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("a failed read says so instead of showing an empty list", async ({ page }) => {
    await page.addInitScript(zh);
    const down = { error: { code: "alertmanager_unavailable", message: "An authorized Alertmanager source is unavailable" } };
    await page.route(at("/silences"), (r) => (r.request().method() === "GET" ? json(r, 503, down) : r.fallback()));
    await page.route(at("/inhibitions"), (r) => (r.request().method() === "GET" ? json(r, 503, down) : r.fallback()));
    await page.goto(origin + "/silences");
    await expect(page.getByText("暂时读不到静默")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("暂时读不到抑制规则")).toBeVisible();
    await expect(page.getByText("没有数据")).toHaveCount(0);
    // PA-132: nothing retries on its own, so the text doesn't promise it.
    expect(await page.locator("#root").innerText()).not.toContain("稍后会自动重试");
  });
});

test.describe("no pretend actions in real mode (PA-134)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("member roles, role members and agent versions don't fake a success", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/settings/members");
    const box = page.locator("table input[type=checkbox], [role=row] input[type=checkbox]").nth(1);
    await box.waitFor({ timeout: 20_000 });
    await box.check();
    await expect(page.getByText(/已选 \d+ 人/)).toBeVisible();
    await expect(page.getByRole("button", { name: "分配角色" })).toHaveCount(0);
    await page.goto(origin + "/settings/roles");
    await page.getByText("角色与权限").first().waitFor({ timeout: 20_000 });
    await page.waitForTimeout(1_500);
    await expect(page.getByRole("button", { name: "管理成员" })).toHaveCount(0);
    const agent = await page.evaluate(async () => {
      const r = await fetch("/api/v1/agents/lifecycle", { headers: { "X-AIOps-Environment": "prod" } });
      const b = (await r.json()) as { items?: { id?: string }[]; agents?: { id?: string }[] };
      return (b.items ?? b.agents ?? []).find((a) => a.id)?.id ?? null;
    });
    test.skip(!agent, "no agent to open");
    await page.goto(origin + `/agents/${agent}`);
    await page.waitForTimeout(2_500);
    await expect(page.getByRole("button", { name: "新建版本" })).toHaveCount(0);
  });
});

test.describe("write failures are said (PA-136)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("a mutate call without its own error handler still shows the refusal", async ({ page }) => {
    await page.addInitScript(zh);
    // Audit export calls mutate with onSuccess only, like most write buttons.
    await page.route(at("/audit/export"), (r) => json(r, 503, { error: { code: "audit_export_unavailable", message: "检查用：导出暂不可用" } }));
    await page.goto(origin + "/settings/audit");
    await page.getByRole("button", { name: "导出" }).click({ timeout: 20_000 });
    await expect.poll(() => toasts(page)).toContain("检查用：导出暂不可用");
  });
});

test.describe("members page with unknown MFA counts (PA-137)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("no raw placeholders; directory sync says it isn't connected", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/settings/members");
    await page.getByText("成员与团队").first().waitFor({ timeout: 20_000 });
    await page.waitForTimeout(1_500);
    const text = await page.locator("#root").innerText();
    expect(text).not.toMatch(/\{n\}|\{d\}/);
    // The acceptance instances have no Feishu directory (unavailable_fields: directory_sync_source).
    await expect(page.getByRole("button", { name: "从飞书同步" })).toBeDisabled();
  });
});

test.describe("push test event (PA-138)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("disabled without a Push source; otherwise sends the picked event type", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/integrations/ingest/push");
    await expect(page.getByRole("button", { name: "发送测试事件" })).toBeDisabled({ timeout: 20_000 });
    const real = await page.evaluate(async () => (await fetch("/api/v1/ingest/push", { headers: { "X-AIOps-Environment": "prod" } })).json());
    let sent: Record<string, unknown> | null = null;
    await page.route(at("/ingest/push"), (r) => json(r, 200, { ...(real as object), availability: "configured" }));
    await page.route(at("/ingest/push/test"), (r) => {
      sent = r.request().postDataJSON();
      return json(r, 202, { code: 202, receipt_id: "rcpt-check" });
    });
    await page.reload();
    await page.getByRole("button", { name: "发送测试事件" }).click({ timeout: 20_000 });
    await expect.poll(() => toasts(page)).toContain("测试事件已发送 · 202");
    expect(sent).toEqual({ type: "change" });
  });
});

test.describe("service links into metrics and traces (PA-139)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("a catalog service ID opens its metrics dashboard and trace search", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/healthz");
    const id = await page.evaluate(async () => {
      const b = (await (await fetch("/api/v1/services", { headers: { "X-AIOps-Environment": "prod" } })).json()) as { items?: { id: string }[] };
      return b.items?.[0]?.id ?? null;
    });
    test.skip(!id, "no service in the catalog");
    await page.goto(origin + `/metrics?service=${encodeURIComponent(id!)}`);
    await page.waitForTimeout(4_000);
    await page.goto(origin + `/traces?service=${encodeURIComponent(id!)}`);
    await page.waitForTimeout(4_000);
    await m.settle();
    const dash = m.api.filter((a) => a.path.startsWith("/metrics/dashboard"));
    const traces = m.api.filter((a) => a.path.startsWith("/traces?"));
    expect(dash.length, "dashboard requested").toBeGreaterThan(0);
    expect(dash.every((a) => a.status !== 403 && a.status !== 404), JSON.stringify(dash)).toBe(true);
    expect(traces.every((a) => a.status !== 404), JSON.stringify(traces)).toBe(true);
  });
});

test.describe("SLO settings gate (PA-140)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("a role without services.manage can't open SLO settings", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const id = await page.evaluate(async () => {
      const b = (await (await fetch("/api/v1/services", { headers: { "X-AIOps-Environment": "prod" } })).json()) as { items?: { id: string }[] };
      return b.items?.[0]?.id ?? null;
    });
    test.skip(!id, "no service in the catalog");
    await page.goto(origin + `/metrics?service=${encodeURIComponent(id!)}`);
    await expect(page.getByRole("button", { name: "SLO 设置" })).toBeDisabled({ timeout: 20_000 });
  });
});

test.describe("guardrail switch (PA-142)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("the toggle sends only { enabled } with the ID in the path", async ({ page }) => {
    await page.addInitScript(zh);
    let sent: unknown = undefined;
    let path = "";
    await page.route((u) => u.pathname.startsWith("/api/v1/guardrails/"), (r) => {
      if (r.request().method() !== "PATCH") return r.fallback();
      sent = r.request().postDataJSON();
      path = new URL(r.request().url()).pathname;
      return json(r, 200, {});
    });
    await page.goto(origin + "/agents/guardrails");
    const toggle = page.locator(".v3-toggle, [role=switch]").first();
    test.skip(!(await toggle.waitFor({ state: "visible", timeout: 15_000 }).then(() => true, () => false)), "no switchable guardrail");
    await toggle.click();
    await expect.poll(() => sent).toBeDefined();
    expect(Object.keys(sent as object)).toEqual(["enabled"]);
    expect(path).toMatch(/^\/api\/v1\/guardrails\/[^/]+$/);
  });
});

const MEMORY_VIEW = {
  layers: [
    { id: "service", name: "Service facts", count: 1, description: "Current published facts attributed to visible services", evidence: { kind: "current_published_facts", fact_ids: ["fact-svc"] } },
    { id: "workspace", name: "Workspace facts", count: 1, description: "Current published facts explicitly scoped to the workspace", evidence: { kind: "current_published_facts", fact_ids: ["fact-ws"] } },
  ],
  services: [{ id: "svc-check", name: "checkout-api", count: 1 }],
  service: null,
  facts: [
    { id: "fact-svc", fact: "检查用：服务事实", source: "memory-source-check", confidence: null, confirmed: null, recalls: null, expires: null, revision: 1, state: "candidate" },
    { id: "fact-ws", fact: "检查用：工作区事实", source: "memory-source-check", confidence: null, confirmed: null, recalls: null, expires: null, revision: 1, state: "candidate" },
  ],
  conflicts: [],
  conflict: { id: "conflict-check", a: { id: "fact-svc", text: "检查用：服务事实", source: "memory-source-" + "0".repeat(64) }, b: { id: "fact-ws", text: "检查用：工作区事实", source: "memory-source-check" } },
  policy: null,
  partial: true,
};
const MEMORY_POLICY = { ttl_days: 90, proposers: "human", revision: 3 };

test.describe("memory page (PA-144, PA-145)", () => {
  const pin = async (page: Page) => {
    await page.route(at("/memory"), (r) => json(r, 200, MEMORY_VIEW));
    await page.route(at("/memory/policy"), (r) => (r.request().method() === "GET" ? json(r, 200, MEMORY_POLICY) : r.fallback()));
  };
  test.describe("as an admin", () => {
    test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
    test("server layers as tabs, no age on conflict sides, the policy shown, refusals in words", async ({ page }) => {
      await page.addInitScript(zh);
      await pin(page);
      await page.route(at("/memory/facts/fact-svc"), (r) => (r.request().method() === "PATCH" ? json(r, 422, { error: { code: "invalid_memory_input", message: "Invalid memory operation" } }) : r.fallback()));
      await page.goto(origin + "/memory");
      const tabs = page.getByRole("tablist").first();
      await expect(tabs).toContainText("工作区记忆", { timeout: 20_000 });
      for (const fake of ["情景记忆", "语义记忆"]) await expect(tabs).not.toContainText(fake);
      await expect(page.getByText("Service facts")).toHaveCount(0);
      // Each layer lists only its own facts.
      await expect(page.locator("tr", { hasText: "检查用：服务事实" })).toHaveCount(1);
      await expect(page.locator("tr", { hasText: "检查用：工作区事实" })).toHaveCount(0);
      await tabs.getByText("工作区记忆").click();
      await expect(page.locator("tr", { hasText: "检查用：工作区事实" })).toHaveCount(1);
      await expect(page.locator("tr", { hasText: "检查用：服务事实" })).toHaveCount(0);
      // The server pairs conflict sides by ID, so the page claims no age.
      const card = page.locator(".mem-conflict");
      await expect(card).toBeVisible();
      for (const age of ["较旧", "较新"]) await expect(card).not.toContainText(age);
      await expect(page.getByText("未被召回 90 天后过期；仅人工可以提出记忆")).toBeVisible();
      // An edit that isn't an excerpt of its source is refused in words.
      await tabs.getByText("服务记忆").click();
      await page.locator("tr", { hasText: "检查用：服务事实" }).getByRole("button", { name: "编辑" }).click();
      await page.getByRole("dialog").locator("textarea").fill("检查用：改写成别的话");
      await page.getByRole("dialog").getByRole("button", { name: "保存（需重新确认）" }).click();
      await expect.poll(() => toasts(page)).toContain("修改后的内容必须是来源资料里的一段原文。");
    });
  });
  test.describe("as a viewer", () => {
    test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });
    test("memory writes are disabled without knowledge.manage", async ({ page }) => {
      await page.addInitScript(zh);
      await pin(page);
      await page.goto(origin + "/memory");
      await expect(page.locator("tr", { hasText: "检查用：服务事实" })).toHaveCount(1, { timeout: 20_000 });
      const row = page.locator("tr", { hasText: "检查用：服务事实" });
      for (const name of ["确认", "编辑", "遗忘"]) await expect(row.getByRole("button", { name, exact: true })).toBeDisabled();
      for (const name of ["保留 B，废弃 A", "保留 A，废弃 B", "记忆策略", "修改策略"]) await expect(page.getByRole("button", { name })).toBeDisabled();
      // The policy itself stays readable.
      await expect(page.getByText("未被召回 90 天后过期")).toBeVisible();
    });
  });
});

test.describe("knowledge source read (PA-146)", () => {
  test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });
  test("reading a source is gated on knowledge.manage; uploads are named", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/knowledge-sources");
    const read = page.locator('button[aria-label^="读取来源"]').first();
    test.skip(!(await read.waitFor({ timeout: 15_000 }).then(() => true, () => false)), "no registered source");
    await expect(read).toBeDisabled();
    const kinds = await page.locator("tbody tr").allInnerTexts();
    expect(kinds.join("\n")).not.toMatch(/^upload$/m);
  });
});

const EVAL_RUN = {
  id: "eval-check", title: "检查用实验", dataset: null, meta: "passed", status: "passed", baseline_version: "v1", candidate_version: "v2",
  execution: { case_id: "case-check", status: "succeeded", evaluation_kind: "single_case_literal_checks" },
  metrics: { literal_checks_baseline: 1, literal_checks_candidate: 1 }, radar: null, cases: [{ id: "case-check", execution_status: "succeeded" }],
  datasets: null, evaluators: null, partial: true,
};
const EVAL_CASE = {
  id: "case-check", title: "检查用案例", queue: "检查用队列", revision: 1,
  alert: { sev: "P2", title: "LabHigh5xxRate", labels: [{ k: "service", v: "checkout-api" }] },
  evidence: [{ brand: "manual", title: "值班记录", sub: "人工提供" }], expected: { cause: "发布回滚后恢复", advice: "继续观察" },
  output: { version: "v2", meta: "Single human-supplied case; tool-free model output; usage unknown", tools: [], markdown: "检查用输出" },
  baseline: { version: "v1", score: "0.5000", text: "检查用基线" },
  labels: [{ actor: "0123456789abcdef0123456789abcdef", label: "correct", note: "检查用备注", time: "2026-10-16T13:23:51Z" }],
  auto: { score: "0.5000", note: "Fraction of explicit case-sensitive literal checks satisfied. Not a human correctness label or general Agent quality score.", rules: ["健康", "回滚"] },
};

test.describe("evaluations in real mode (PA-147, PA-148)", () => {
  const pin = async (page: Page) => {
    await page.route(at("/evals/eval-check"), (r) => json(r, 200, EVAL_RUN));
    await page.route(at("/eval-cases/case-check"), (r) => json(r, 200, EVAL_CASE));
  };
  test.describe("as an admin", () => {
    test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
    test("a run experiment says what it checked; no pretend dataset or sinks", async ({ page }) => {
      await page.addInitScript(zh);
      await pin(page);
      await page.goto(origin + "/agents/evals?id=eval-check");
      await expect(page.getByText("这个实验只评了一个人工案例")).toBeVisible({ timeout: 20_000 });
      await expect(page.getByText("通过", { exact: true })).toBeVisible();
      const body = await page.locator("#root").innerText();
      for (const sample of ["Langfuse", "Phoenix", "passed"]) expect.soft(body, sample).not.toContain(sample);
      await expect(page.locator("aside").getByText("新建", { exact: true })).toHaveCount(0);
    });
    test("the case page names literal checks and labels in words", async ({ page }) => {
      await page.addInitScript(zh);
      await pin(page);
      await page.goto(origin + "/agents/evals/case-check");
      await expect(page.getByText("字面检查")).toBeVisible({ timeout: 20_000 });
      const body = await page.locator("#root").innerText();
      for (const raw of ["LLM 评审", "Fraction of explicit", "Single human-supplied", "0123456789abcdef0123456789abcdef"]) expect.soft(body, raw).not.toContain(raw);
      await expect(page.locator(".aside").getByText("正确", { exact: true }).last()).toBeVisible();
      await expect(page.getByPlaceholder("限流配额", { exact: false })).toHaveCount(0);
    });
  });
  test.describe("as a viewer", () => {
    test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });
    test("submitting a label is gated on agents.change", async ({ page }) => {
      await page.addInitScript(zh);
      await pin(page);
      await page.goto(origin + "/agents/evals/case-check");
      await expect(page.getByRole("button", { name: "提交标注" })).toBeDisabled({ timeout: 20_000 });
    });
  });
});

test.describe("agent console writes as a viewer (PA-150)", () => {
  test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });
  test("each write entry is disabled without the agents grant it needs", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/agents/evals");
    await expect(page.getByRole("button", { name: "新建实验" })).toBeDisabled({ timeout: 20_000 });
    await page.goto(origin + "/agents/tools");
    await expect(page.getByRole("button", { name: "登记工具" })).toBeDisabled({ timeout: 20_000 });
    await page.goto(origin + "/agents/guardrails");
    const toggle = page.locator("[role=switch]").first();
    if (await toggle.waitFor({ timeout: 15_000 }).then(() => true, () => false)) await expect(toggle).toBeDisabled();
    // A recorded run and agent, when the instance has them.
    const ids = await page.evaluate(async () => {
      const h = { "X-AIOps-Environment": "prod" };
      const runs = (await (await fetch("/api/v1/agent-runs", { headers: h })).json()) as { items?: { id: string }[] };
      const agents = (await (await fetch("/api/v1/agents/lifecycle", { headers: h })).json()) as { items?: { id: string }[]; agents?: { id: string }[] };
      return { run: runs.items?.[0]?.id ?? null, agent: (agents.items ?? agents.agents)?.[0]?.id ?? null };
    });
    if (ids.run) {
      await page.goto(origin + `/agents/runs/${ids.run}`);
      for (const name of ["有用", "无用", "标注"]) await expect(page.getByRole("button", { name, exact: true })).toBeDisabled({ timeout: 20_000 });
    }
    if (ids.agent) {
      await page.goto(origin + `/agents/${ids.agent}`);
      await expect(page.getByRole("button", { name: /^回滚(到 |$)/ })).toBeDisabled({ timeout: 20_000 });
    }
  });
});

test.describe("alert rule import, preview and dry run refusals (PA-151, PA-152)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  const notConnected = (code: string, message: string) => (r: Route) => json(r, 503, { error: { code, message } });
  test("a refused import or preview is said in words, not thrown", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/alert-rules/import"), notConnected("not_connected", "Alert rule draft repository is not connected"));
    await page.route(at("/alert-rules/preview"), notConnected("rule_assistant_not_connected", "Reviewed rule query catalog is not configured"));
    const service = await page.goto(origin + "/healthz").then(() =>
      page.evaluate(async () => ((await (await fetch("/api/v1/services", { headers: { "X-AIOps-Environment": "prod" } })).json()) as { items?: { id: string }[] }).items?.[0]?.id ?? null),
    );
    test.skip(!service, "no service in the catalog");
    await page.goto(origin + `/alert-rules/new?tab=import&service=${encodeURIComponent(service!)}`);
    await page.locator("textarea").first().fill("groups: []", { timeout: 20_000 });
    // PA-224: in real mode the import button says what it does; it is the one write request.
    await page.getByRole("button", { name: "导入为草稿" }).click();
    await expect.poll(() => toasts(page)).toContain("规则草稿仓库还没有接入，暂时不能导入。");
    await page.goto(origin + `/alert-rules/new?service=${encodeURIComponent(service!)}`);
    await page.locator("textarea.mono").fill("sum(rate(http_requests_total[5m]))", { timeout: 20_000 });
    await expect(page.getByText("预览还没有接入审过的查询目录。")).toBeVisible({ timeout: 10_000 });
    expect(m.pageErrors).toEqual([]);
  });
  test("a dry run the rule's interval rules out says why", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route((u) => /\/api\/v1\/alert-rules\/[^/]+\/dry-run$/.test(u.pathname), notConnected("rule_interval_unavailable", "Rule evaluation interval is unsupported"));
    await page.goto(origin + "/alert-rules");
    const rule = page.locator("button.rule-item").last();
    test.skip(!(await rule.waitFor({ timeout: 15_000 }).then(() => true, () => false)), "no rule listed");
    await rule.click();
    await page.getByRole("button", { name: "试运行（最近 6 小时）" }).click({ timeout: 20_000 });
    await expect.poll(() => toasts(page)).toContain("这条规则的评估间隔不在 15 秒到 5 分钟之间，不能试运行。");
  });
});

test.describe("canvas node selection and node turns (PA-153, PA-154)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  const canvases = async (page: Page) => {
    await page.goto(origin + "/healthz");
    // The list is paged and investigations far outnumber general canvases, so ask for each kind (as PA-253).
    return page.evaluate(async () => {
      const first = async (kind: string) => {
        const b = (await (await fetch(`/api/v1/canvases?kind=${kind}&page_size=1`, { headers: { "X-AIOps-Environment": "prod" } })).json()) as { items?: { id: string }[] };
        return b.items?.[0]?.id ?? null;
      };
      let doc: string | null = null;
      for (const kind of ["plan", "review", "analysis", "custom"]) if (!doc) doc = await first(kind);
      return { doc, inv: await first("investigation") };
    });
  };
  test("clicking a node keeps it selected on both canvases", async ({ page }) => {
    await page.addInitScript(zh);
    const ids = await canvases(page);
    test.skip(!ids.doc && !ids.inv, "no canvas");
    for (const id of [ids.doc, ids.inv].filter(Boolean)) {
      await page.goto(origin + `/canvas/${id}`);
      const node = page.locator("article.cv-node").first();
      await node.waitFor({ timeout: 20_000 });
      await node.click({ position: { x: 40, y: 16 } });
      await page.waitForTimeout(400);
      await expect(node, `${id}: the node stays selected after the click`).toHaveClass(/selected/);
    }
  });
  test("a failed node turn is said and frees the composer", async ({ page }) => {
    await page.addInitScript(zh);
    const ids = await canvases(page);
    test.skip(!ids.doc, "no general canvas");
    await page.route((u) => /\/api\/v1\/canvases\/[^/]+\/nodes\/[^/]+\/messages$/.test(u.pathname), (r) =>
      r.request().method() === "POST" ? json(r, 503, { error: { code: "model_unavailable", message: "检查用：模型暂时不可用" } }) : r.fallback(),
    );
    await page.goto(origin + `/canvas/${ids.doc}`);
    const node = page.locator("article.cv-node").first();
    await node.waitFor({ timeout: 20_000 });
    await node.click({ position: { x: 40, y: 16 } });
    await page.getByPlaceholder("在这个节点继续…").fill("检查用问题", { timeout: 10_000 });
    await page.locator(".dp button[aria-label='发送']").click();
    await expect.poll(() => toasts(page)).toContain("检查用：模型暂时不可用");
  });
});

test.describe("investigation canvas tools and silent writes (PA-155, PA-156)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("write back needs a selected branch; sample-only compare and merge stay out", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const inv = await page.evaluate(async () => {
      const b = (await (await fetch("/api/v1/canvases", { headers: { "X-AIOps-Environment": "prod" } })).json()) as { items?: { id: string; kind?: string; type?: string }[] };
      return (b.items ?? []).find((i) => (i.kind ?? i.type) === "investigation")?.id ?? null;
    });
    test.skip(!inv, "no investigation canvas");
    await page.goto(origin + `/canvas/${inv}`);
    const tools = page.getByRole("toolbar", { name: "画布工具" });
    await tools.waitFor({ timeout: 20_000 });
    for (const sample of ["合并", "对比"]) await expect(tools.getByRole("button", { name: sample })).toHaveCount(0);
    await tools.getByRole("button", { name: "回写" }).click();
    await expect.poll(() => toasts(page)).toContain("先选中要回写的分支");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });
  test("a preference that didn't sync is said", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/me/preferences"), (r) => (r.request().method() === "PATCH" ? json(r, 503, { error: { code: "unavailable", message: "检查用" } }) : r.fallback()));
    await page.goto(origin + "/settings/preferences");
    await page.getByText("深色", { exact: true }).first().click({ timeout: 20_000 });
    await expect.poll(() => toasts(page)).toContain("偏好只在这台设备上生效，没有同步到账号");
    await page.getByText("浅色", { exact: true }).first().click();
  });
});

test.describe("write-back request (PA-157)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("the write names the ancestor and the revisions the preview read", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const inv = await page.evaluate(async () => {
      const h = { "X-AIOps-Environment": "prod" };
      const b = (await (await fetch("/api/v1/canvases", { headers: h })).json()) as { items?: { id: string; kind?: string; type?: string }[] };
      for (const c of (b.items ?? []).filter((i) => (i.kind ?? i.type) === "investigation")) {
        const g = (await (await fetch(`/api/v1/investigations/${c.id}/canvas`, { headers: h })).json()) as { nodes?: { id: string; kind: string }[] };
        const branch = g.nodes?.find((n) => n.kind === "branch");
        if (branch) return { id: c.id, branch: branch.id };
      }
      return null;
    });
    test.skip(!inv, "no investigation with a branch");
    await page.route((u) => u.pathname.endsWith("/writeback-preview"), (r) =>
      json(r, 200, {
        from: { id: inv!.branch, title: "检查用分支", version: "v1" }, to: { id: "target-check", title: "主线", version: "v3", next_version: "v4", hops: 1 },
        conclusions: [{ id: "msg-check", text: "检查用结论", evidence: [], recommended: true }], timeline: [], checks: [], budget: { before: null, after: null, limit: null },
        target: "target-check", source_revision: 5, target_revision: 3,
      }),
    );
    let sent: Record<string, unknown> | null = null;
    await page.route((u) => u.pathname.endsWith("/writeback"), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      sent = r.request().postDataJSON();
      return json(r, 409, { error: { code: "revision_conflict", message: "stale" } });
    });
    await page.goto(origin + `/canvas/${inv!.id}`);
    await page.locator(`article.cv-node[data-node="${inv!.branch}"]`).click({ position: { x: 40, y: 16 }, timeout: 20_000 });
    await page.getByRole("toolbar", { name: "画布工具" }).getByRole("button", { name: "回写" }).click();
    await page.getByRole("dialog").getByRole("button", { name: /确认回写/ }).click({ timeout: 10_000 });
    await expect.poll(() => sent).not.toBeNull();
    expect(sent).toMatchObject({ from: inv!.branch, target: "target-check", expected_source_revision: 5, expected_target_revision: 3, conclusions: ["msg-check"] });
    // A stale write is said and the dialog stays open to check again.
    await expect.poll(() => toasts(page)).toContain("分支或主线刚有变化，已重新读取，请核对后再回写");
    await expect(page.getByRole("dialog")).toBeVisible();
  });
});

test.describe("saves that need the revision they read (PA-158, PA-159, PA-160)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("a role matrix is saved with If-Role-Matrix-Revision", async ({ page }) => {
    await page.addInitScript(zh);
    let header: string | undefined;
    await page.route((u) => /\/api\/v1\/roles\/[^/]+$/.test(u.pathname), (r) => {
      if (r.request().method() !== "PUT") return r.fallback();
      header = r.request().headers()["if-role-matrix-revision"];
      return json(r, 409, { error: { code: "role_revision_conflict", message: "stale" } });
    });
    await page.goto(origin + "/settings/roles");
    const business = page.getByRole("button", { name: /知识审核/ }).first();
    test.skip(!(await business.waitFor({ timeout: 15_000 }).then(() => true, () => false)), "no business role");
    await business.click();
    await page.locator("button.perm-cell").first().click({ timeout: 10_000 });
    await page.getByRole("button", { name: "保存权限" }).click();
    await expect.poll(() => header).toMatch(/^\d+$/);
    await expect.poll(() => toasts(page)).toContain("角色刚被其他人修改，已重新读取，请核对后再保存");
  });
  test("workspace settings are saved with If-Match", async ({ page }) => {
    await page.addInitScript(zh);
    let header: string | undefined;
    await page.route(at("/workspace"), (r) => {
      if (r.request().method() !== "PUT") return r.fallback();
      header = r.request().headers()["if-match"];
      return json(r, 412, { error: { code: "precondition_failed", message: "stale" } });
    });
    await page.goto(origin + "/settings/workspace");
    const name = page.getByLabel("工作区名称").or(page.locator("input").first());
    await name.first().waitFor({ timeout: 20_000 });
    await name.first().fill((await name.first().inputValue()) + " 检查");
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect.poll(() => header).toBeTruthy();
    await expect.poll(() => toasts(page)).toContain("工作区设置刚被其他人修改，已重新读取，请核对后再保存");
  });
  test("agent rollback names the agent revision", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const agent = await page.evaluate(async () => {
      const b = (await (await fetch("/api/v1/agents/lifecycle", { headers: { "X-AIOps-Environment": "prod" } })).json()) as { agents?: { id: string }[] };
      return b.agents?.[0]?.id ?? null;
    });
    test.skip(!agent, "no agent");
    let sent: Record<string, unknown> | null = null;
    await page.route((u) => u.pathname.endsWith(`/agents/${agent}/rollback`), (r) => {
      sent = r.request().postDataJSON();
      return json(r, 409, { error: { code: "agent_revision_conflict", message: "检查用：版本已变化" } });
    });
    await page.goto(origin + `/agents/${agent}`);
    // PA-236: the button names the version it lands on, or just 回滚 when there is none.
    await page.getByRole("button", { name: /^回滚(到 |$)/ }).click({ timeout: 20_000 });
    await expect.poll(() => sent).not.toBeNull();
    expect(typeof (sent as unknown as { expected_revision?: unknown }).expected_revision).toBe("number");
  });
});

test.describe("canvas node move (PA-160)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("a moved node names its layout revision", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const inv = await page.evaluate(async () => {
      const b = (await (await fetch("/api/v1/canvases", { headers: { "X-AIOps-Environment": "prod" } })).json()) as { items?: { id: string; kind?: string; type?: string }[] };
      return (b.items ?? []).find((i) => (i.kind ?? i.type) === "investigation")?.id ?? null;
    });
    test.skip(!inv, "no investigation canvas");
    let sent: Record<string, unknown> | null = null;
    await page.route((u) => /\/api\/v1\/investigations\/[^/]+\/nodes\/[^/]+$/.test(u.pathname), (r) => {
      if (r.request().method() !== "PATCH") return r.fallback();
      sent = r.request().postDataJSON();
      return json(r, 409, { error: { code: "layout_conflict", message: "检查用" } });
    });
    await page.goto(origin + `/canvas/${inv}`);
    const node = page.locator("article.cv-node").first();
    await node.waitFor({ timeout: 20_000 });
    const box = (await node.boundingBox())!;
    await page.mouse.move(box.x + 60, box.y + 16);
    await page.mouse.down();
    await page.mouse.move(box.x + 140, box.y + 60, { steps: 8 });
    await page.mouse.up();
    await expect.poll(() => sent, { timeout: 10_000 }).not.toBeNull();
    expect(typeof (sent as unknown as { expected_layout_version?: unknown }).expected_layout_version).toBe("number");
  });
});

test.describe("webhooks and API tokens (PA-161)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("tokens are issued as the contract says, show the server's secret, and list and revoke real tokens", async ({ page }) => {
    await page.addInitScript(zh);
    let sent: Record<string, unknown> | null = null;
    let deleted = "";
    await page.route(at("/api-tokens"), (r) => {
      if (r.request().method() === "POST") {
        sent = r.request().postDataJSON();
        return json(r, 201, { id: "tok-new", name: "检查用令牌", scopes: ["connectors:view"], secret_shown_once: "aiops_pat_checksecret" });
      }
      return json(r, 200, { items: [{ id: "tok-check", name: "检查用旧令牌", environment_id: "prod", scopes: ["connectors:view"], created_at: "2026-10-16T07:00:00Z", expires_at: "2026-10-17T07:00:00Z", revoked_at: null }] });
    });
    await page.route((u) => u.pathname === "/api/v1/api-tokens/tok-check", (r) => {
      deleted = r.request().method();
      return r.fulfill({ status: 204 });
    });
    await page.goto(origin + "/integrations/webhooks");
    await expect(page.getByRole("button", { name: "新建 API 令牌" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: "新建 Webhook" })).toHaveCount(0);
    await page.getByRole("button", { name: "新建 API 令牌" }).click();
    await page.getByRole("dialog").locator("input").first().fill("检查用令牌");
    await page.getByRole("dialog").getByRole("button", { name: "创建令牌" }).click();
    await expect.poll(() => sent).not.toBeNull();
    expect(sent).toMatchObject({ scopes: ["connectors:view"] });
    expect([3600, 86400, 604800, 2592000]).toContain((sent as unknown as { ttl: number }).ttl);
    await expect(page.getByRole("dialog")).toContainText("aiops_pat_checksecret");
    await page.getByRole("dialog").getByRole("button", { name: "我已保存" }).click();
    await page.getByRole("tab", { name: /API 令牌/ }).click();
    const row = page.locator("li", { hasText: "检查用旧令牌" });
    await expect(row).toBeVisible();
    await expect(row.getByRole("button", { name: "轮换" })).toHaveCount(0);
    await row.getByRole("button", { name: "吊销" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "吊销" }).click();
    await expect.poll(() => deleted).toBe("DELETE");
  });
});

test.describe("audit export (PA-162)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("exports what the list shows and downloads the returned file", async ({ page }) => {
    await page.addInitScript(zh);
    let sent: Record<string, unknown> | null = null;
    let downloaded = false;
    await page.route(at("/audit/export"), (r) => {
      sent = r.request().postDataJSON();
      return json(r, 200, { url: "/api/v1/audit/exports/check" });
    });
    await page.route((u) => u.pathname === "/api/v1/audit/exports/check", (r) => {
      downloaded = true;
      return r.fulfill({ status: 200, contentType: "text/csv", headers: { "Content-Disposition": "attachment; filename=audit.csv" }, body: "id,at\n" });
    });
    await page.goto(origin + "/settings/audit");
    await page.getByRole("button", { name: "导出" }).click({ timeout: 20_000 });
    await expect.poll(() => sent).not.toBeNull();
    expect(sent).toMatchObject({ filter: "all" });
    await expect.poll(() => downloaded).toBe(true);
    expect((await toasts(page)).join(" ")).not.toContain("飞书");
  });
});

test.describe("no pretend actions in real mode, second sweep (PA-163)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("new role, connector edit/disable and custom event schema stay in the sample", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/settings/roles");
    await expect(page.getByRole("button", { name: "角色与权限" }).or(page.getByText("系统角色")).first()).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: "新建角色" })).toHaveCount(0);
    await page.goto(origin + "/integrations");
    await page.getByText("Prometheus", { exact: true }).first().click({ timeout: 20_000 });
    await expect(page.getByRole("button", { name: "测试连接" })).toBeVisible({ timeout: 10_000 });
    for (const name of ["编辑", "停用"]) await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
    await page.goto(origin + "/integrations/ingest/push");
    await page.waitForTimeout(3_000);
    await expect(page.getByRole("button", { name: "自定义事件 schema" })).toHaveCount(0);
  });
});

test.describe("agent release without a canary (PA-164)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("promote is disabled with the reason when no version is in canary", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    const agent = await page.evaluate(async () => {
      const h = { "X-AIOps-Environment": "prod" };
      const b = (await (await fetch("/api/v1/agents/lifecycle", { headers: h })).json()) as { agents?: { id: string }[] };
      for (const x of b.agents ?? []) {
        const d = (await (await fetch(`/api/v1/agents/${x.id}`, { headers: h })).json()) as { release?: { to?: string } };
        if (d.release && !d.release.to) return x.id;
      }
      return null;
    });
    test.skip(!agent, "every agent has a canary in progress");
    await page.goto(origin + `/agents/${agent}`);
    await expect(page.getByRole("button", { name: /^推进到/ })).toBeDisabled({ timeout: 20_000 });
    await expect(page.getByText("发布 ·  灰度")).toHaveCount(0);
  });
});

test.describe("save a canvas as a template (PA-114)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("only general canvases are offered and a refusal is said", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/canvas-templates"), (r) => (r.request().method() === "POST" ? json(r, 404, { error: { code: "not_found", message: "检查用：画布不存在" } }) : r.fallback()));
    await page.goto(origin + "/templates");
    await page.getByRole("button", { name: "新建模板" }).click({ timeout: 20_000 });
    const dialog = page.getByRole("dialog");
    await expect(dialog.locator(".tpl-pick").first()).toBeVisible({ timeout: 10_000 });
    await expect(dialog.locator(".tpl-pick", { hasText: "调查" })).toHaveCount(0);
    await dialog.getByRole("button", { name: "保存模板" }).click();
    await expect.poll(() => toasts(page)).toContain("检查用：画布不存在");
    await expect(dialog).toBeVisible();
  });
});

test.describe("conversation attachments (PA-165)", () => {
  test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
  test("an attachment is uploaded with the environment header", async ({ page }) => {
    await page.addInitScript(zh);
    let env: string | undefined;
    await page.route(at("/uploads"), (r) => {
      env = r.request().headers()["x-aiops-environment"];
      return json(r, 201, { id: "up-check", kind: "file", name: "check.md", size: 10, mime: "text/markdown", url: "/api/v1/uploads/up-check" });
    });
    await page.goto(origin + "/conversations/new");
    await page.locator('input[type="file"]').first().setInputFiles({ name: "check.md", mimeType: "text/markdown", buffer: Buffer.from("# 检查用\n") }, { timeout: 20_000 });
    await expect.poll(() => env, { timeout: 10_000 }).toBe("prod");
  });
});

test.describe("production config sync for other roles (PA-110)", () => {
  test.use({ storageState: statePath("prod", "viewer"), viewport: { width: 1440, height: 1000 } });
  test("a role without workspace.manage sees the lock and never requests the list", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/integrations/sync");
    await expect(page.getByText("没有权限查看待导入的配置")).toBeVisible({ timeout: 20_000 });
    await m.quiet();
    await m.settle();
    expect(m.api.filter((c) => c.path.startsWith("/config-sync"))).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });
});

const INCIDENT_DRAFT = {
  title: "检查用事件", severity: "P2", severity_hint: "", env: "prod", services: [{ id: "svc-check", tier: null }], impact: null,
  alerts: [{ id: "alert-check", severity: "P2", text: "LabHigh5xxRate · checkout-api", at: "2026-10-17T02:00:00Z" }],
  commander: null, participants: null, war_room: null,
};

test.describe("new incident (PA-126)", () => {
  test.describe("as an admin", () => {
    test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
    test("manual entry is held with where to start instead; a refused draft is said", async ({ page }) => {
      await page.addInitScript(zh);
      await page.goto(origin + "/incidents");
      const btn = page.getByRole("button", { name: "新建事件" });
      await expect(btn).toBeDisabled({ timeout: 20_000 });
      await expect(page.locator(".avail-gate")).toContainText("请在收件箱的告警上选「创建事件」");
      await page.route(at("/incidents/draft"), (r) => json(r, 409, { error: { code: "incident_conflict", message: "Alert changed or is already linked to an incident" } }));
      await page.goto(origin + "/incidents?new=incident&alerts=alert-check");
      await expect(page.getByText("所选告警刚刚变化，或已经关联了事件")).toBeVisible({ timeout: 20_000 });
      await expect(page.getByText(/已按所选 \d+ 条告警预填/)).toHaveCount(0);
      await expect(page.getByRole("button", { name: "创建事件" })).toBeDisabled();
    });
  });
  test.describe("as an operator", () => {
    test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });
    test("the drawer from an alert doesn't read the member list without members.view", async ({ page }) => {
      await page.addInitScript(zh);
      const m = monitor(page);
      await page.route(at("/incidents/draft"), (r) => json(r, 200, INCIDENT_DRAFT));
      await page.goto(origin + "/incidents?new=incident&alerts=alert-check");
      await expect(page.getByText("已按所选 1 条告警预填", { exact: false })).toBeVisible({ timeout: 20_000 });
      await m.quiet();
      await m.settle();
      expect(m.api.filter((c) => c.path.startsWith("/members"))).toEqual([]);
      expect(m.pageErrors).toEqual([]);
    });
  });
});

test.describe("new runbook starting points (PA-124)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("real mode offers only blank and from a postmortem", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/runbooks");
    await page.getByRole("button", { name: "新建 Runbook" }).click({ timeout: 20_000 });
    const options = page.getByRole("radiogroup", { name: "起点" }).getByRole("radio");
    await expect(options).toHaveCount(2);
    const text = await page.getByRole("radiogroup", { name: "起点" }).innerText();
    for (const held of ["从模板", "从对话或画布生成", "导入"]) expect.soft(text, held).not.toContain(held);
  });
});

test.describe("drafts without a GitOps repository (PA-123, PA-141)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });
  test("new inhibition and new route are held with the reason when the list says so", async ({ page }) => {
    await page.addInitScript(zh);
    const inhibitions = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/v1/inhibitions", { timeout: 20_000 });
    await page.goto(origin + "/silences");
    const inhib = (await (await inhibitions).json().catch(() => ({}))) as { unavailable?: string[] };
    const inhibitBtn = page.getByRole("button", { name: "新建抑制规则" }).first();
    if (inhib.unavailable?.includes("inhibition_drafts")) {
      await expect(inhibitBtn).toBeDisabled();
      await expect(page.locator(".avail-gate").filter({ hasText: "GitOps 抑制仓库" }).first()).toBeVisible();
    } else await expect(inhibitBtn).toBeEnabled();
    const routes = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/v1/notification-routes", { timeout: 20_000 });
    await page.goto(origin + "/routing");
    const tree = (await (await routes).json().catch(() => ({}))) as { unavailable?: string[] };
    const routeBtn = page.getByRole("button", { name: "新建路由" });
    if (tree.unavailable?.includes("route_drafts")) {
      await expect(routeBtn).toBeDisabled();
      await expect(page.locator(".avail-gate").filter({ hasText: "GitOps 路由仓库" }).first()).toBeVisible();
    } else await expect(routeBtn).toBeEnabled();
  });
});
