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
    await page.goto(origin + "/executions/00000000000000000000000000000000");
    await expect(page.getByText("找不到这条记录")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("来源连接失败")).toHaveCount(0);
  });
});
