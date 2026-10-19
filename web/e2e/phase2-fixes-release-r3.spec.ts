import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { at, json, zh } from "./support/checks";

/**
 * Checks for round 3 defects fixed before the v0.1.0 release. Every check pins the API with page.route and answers
 * every write itself, so it runs on any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-release-r3.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "operator"), viewport: { width: 1440, height: 1000 } });

/* ---------------- new template dialog lists general canvases past the first page (PA-253) ---------------- */
const canvas = (id: string, title: string, kind: string, at: string) => ({
  id, title, kind, status: kind === "investigation" ? "open" : "active", meta: "检查用", participants: [], updated_at: at,
  href: `/canvas/${id}`, services: [], thumb: { nodes: [], edges: [] },
});
// The most recently updated 20 canvases are all investigations; the two plan canvases are older.
const investigations = Array.from({ length: 20 }, (_, i) =>
  canvas(`cv-check-inv-${i}`, `检查用调查 ${i}`, "investigation", `2026-10-19T08:${String(59 - i).padStart(2, "0")}:00Z`),
);
const plans = [canvas("cv-check-plan-1", "检查用计划画布一", "plan", "2026-10-18T09:00:00Z"), canvas("cv-check-plan-2", "检查用计划画布二", "plan", "2026-10-17T09:00:00Z")];
const counts = { all: 22, investigation: 20, plan: 2, review: 0, analysis: 0, custom: 0 };

test("the new template dialog offers general canvases even when the first page is all investigations", async ({ page }) => {
  await page.addInitScript(zh);
  const posted: Record<string, unknown>[] = [];
  await page.route(at("/canvases"), (r) => {
    const url = new URL(r.request().url());
    const kind = url.searchParams.get("kind") ?? "";
    const items = kind === "" ? investigations : kind === "plan" ? plans : kind === "investigation" ? investigations : [];
    return json(r, 200, { items, counts, total: kind === "" ? counts.all : (counts as Record<string, number>)[kind] ?? 0, page: 1, page_size: Number(url.searchParams.get("page_size") ?? 20) });
  });
  await page.route(at("/canvas-templates"), (r) => {
    if (r.request().method() !== "POST") return r.fallback();
    posted.push(r.request().postDataJSON());
    return json(r, 201, { id: "tpl-check", kind: "plan", name: "检查用", desc: "", nodes: "0" });
  });
  await page.goto(origin + "/templates");
  await page.getByRole("button", { name: "新建模板" }).click({ timeout: 20_000 });
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator(".tpl-pick", { hasText: "检查用计划画布一" })).toBeVisible({ timeout: 10_000 });
  await expect(dialog.locator(".tpl-pick", { hasText: "检查用计划画布二" })).toBeVisible();
  await expect(dialog.locator(".tpl-pick", { hasText: "检查用调查" })).toHaveCount(0);
  await dialog.getByRole("button", { name: "保存模板" }).click();
  await expect.poll(() => posted.length).toBe(1);
  expect(posted[0].from_canvas).toBe("cv-check-plan-1");
});

/* ---------------- rollback review says the revision is the one read when proposed (PA-257) ---------------- */
// A pending rollback proposal is a frozen document: from_revision is what the proposer read. Calling it "current" was
// a claim about the cluster the page never checks (two nonprod proposals said "current 80" while checkout-api was 91).
const at0 = "2026-10-19T09:00:00Z";
const rollbackProposal = (changes: object[]) => ({
  id: "pa257-check", title: "检查用回滚提案", status: "pending", risk: "medium", impact: "checkout-api in nonprod", reversible: true,
  proposed_by: "check-proposer", proposed_by_name: "Check Proposer", proposed_at: at0, investigation_id: "inv-pa257-check",
  target: "Deployment checkout/checkout-api", diff: null, method: "Kubernetes rollback", evidence: [], verification: [], verification_note: "",
  checklist: [], requirement: "Two approvers with passkeys", discussion: [], action_kind: "kubernetes.deployment.rollback",
  rollback: {
    service_id: "svc-check", application_id: "app-check", environment_id: "prod", namespace: "checkout", deployment: "checkout-api",
    from_revision: "80", target_revision: "79", containers: [{ name: "app", image: "registry.example.invalid/checkout:1.2.3" }], changes,
    verification: { observation_seconds: 360, request_rate_query_id: "q-rate", error_ratio_query_id: "q-err", sample_age_query_id: "q-age", max_error_ratio: 0.05, min_request_rate: 0.1 },
    reason: "检查用",
  },
  alert_rule: null, execution: null, decision: null, my_vote: null,
});

test.describe("rollback review revision wording (PA-257)", () => {
  test.use({ storageState: statePath("prod", "admin") });
  for (const [label, changes] of [
    ["with template differences", [{ container: "app", field: "image", name: null, from: "registry.example.invalid/checkout:1.2.4", to: "registry.example.invalid/checkout:1.2.3", value_hidden: false }]],
    ["without template differences", []],
  ] as const) {
    test(`a pending rollback names the revision read when proposed, not the current one (${label})`, async ({ page }) => {
      await page.addInitScript(zh);
      await page.route(at("/proposals/pa257-check"), (r) => json(r, 200, rollbackProposal([...changes])));
      await page.goto(origin + "/review/pa257-check");
      await expect(page.getByText("提案时 80 → 回滚到 79")).toBeVisible({ timeout: 20_000 });
      await expect(page.getByText(/当前 80/)).toHaveCount(0);
      await expect(page.getByText("与当前相同")).toHaveCount(0);
      await expect(page.getByText(/提案时读取的 Deployment/).first()).toBeVisible();
    });
  }
});

/* ---------------- a tool step with a failed call isn't summed up as a green check (PA-266) ---------------- */
const conversation = (calls: object[]) => ({
  id: "conv-pa266-check", title: "检查用对话", mode: "chat", status: "active", scope: { inferred: true },
  messages: [
    { id: "m1", role: "user", author: "Check", created_at: at0, parts: [{ type: "text", markdown: "checkout-api 怎么了" }] },
    { id: "m2", role: "assistant", author: "Redstring", created_at: at0, parts: [
      { type: "tool_calls", summary: `${calls.length} tool calls`, calls, tokens: 1200, duration_s: 2 },
      { type: "text", markdown: "检查用回答" },
    ] },
  ],
  artifacts: [], side: { connectors: [], context: { used: null, budget: null, parts: [] }, recalled: [] },
});
const call = (id: string, status: "ok" | "error") => ({ id, connector: "kubernetes", tool: id === "c1" ? "list_services" : "read_service_metrics", args: "{}", duration_ms: 120, status });

test.describe("tool step summary (PA-266)", () => {
  test.use({ storageState: statePath("prod", "admin") });
  test("a failed call marks the folded tool step as failed; all ok keeps the check", async ({ page }) => {
    await page.addInitScript(zh);
    let calls = [call("c1", "error"), call("c2", "ok")];
    await page.route(at("/conversations/conv-pa266-check"), (r) => json(r, 200, conversation(calls)));
    await page.goto(origin + "/conversations/conv-pa266-check");
    const head = page.locator(".part-tools .head").first();
    await expect(head).toContainText("调用了 2 个工具", { timeout: 20_000 });
    await expect(head.locator('svg.v3-i[style*="var(--danger)"]')).toHaveCount(1);
    await expect(head.locator('svg.v3-i[style*="var(--ok)"]')).toHaveCount(0);
    calls = [call("c1", "ok"), call("c2", "ok")];
    await page.reload();
    await expect(head).toContainText("调用了 2 个工具", { timeout: 20_000 });
    await expect(head.locator('svg.v3-i[style*="var(--ok)"]')).toHaveCount(1);
    await expect(head.locator('svg.v3-i[style*="var(--danger)"]')).toHaveCount(0);
  });
});
