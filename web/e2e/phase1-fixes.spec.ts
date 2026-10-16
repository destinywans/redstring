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
