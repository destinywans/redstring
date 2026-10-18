import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, toasts, zh } from "./support/checks";

/**
 * Knowledge fixes between rounds 2 and 3 (WAN-434): the Runbook dry-run says which steps failed and why (PA-194).
 * Every check answers the API with fixed records (page.route), the dry-run POST included, so it runs on any data and
 * never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-knowledge.spec.ts
 */
const origin = INSTANCES.prod.origin;

/* ---------------- PA-194: dry-run reasons ---------------- */

const RUNBOOK = "rb-knowledge-check";
/** The shape a postmortem draft saves: read-only checks without connectors and one action step. */
const RUNBOOK_DETAIL = {
  id: RUNBOOK, title: "知识检查 Runbook", version: "v1", status: "draft", base: "blank", source_kind: "blank", source_ref: "", owner: "检查人",
  scope: "workspace", environment_ids: ["prod"], application_id: "", team_id: "", tags: [], when: "检查用",
  steps: [
    { n: 1, kind: "check", title: "核对：影响", body: "x", readonly: true },
    { n: 2, kind: "check", title: "核对：根因", body: "x", readonly: true },
    { n: 3, kind: "check", title: "核对：时间线", body: "x", readonly: true },
    { n: 4, kind: "action", title: "加回滚门禁", body: "x", approval: true },
    { n: 5, kind: "verify", title: "确认恢复", body: "x" },
  ],
  reviewers: [], versions: [], usage: { refs: null, resolve_rate: null, weekly: null, highlight: null }, incidents: null, last_verified: null,
  complete_v3_dto: false, partial: true, unknown_fields: [],
};
const check = (step: number, reasons: string[]) => ({ step, validated: reasons.length === 0, reasons, executed: false, requires_approval: reasons.includes("action_step") });
const FAILED = {
  id: "preview-check", steps: 5, ok: false, mode: "validation_only",
  checks: [check(1, ["no_connector"]), check(2, ["no_connector"]), check(3, ["no_connector"]), check(4, ["action_step", "not_readonly", "no_connector"]), check(5, ["not_readonly", "no_connector"])],
};

test.describe("runbook dry-run reasons (PA-194)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  for (const [name, init, button, want, notWant] of [
    [
      "zh", zh, "校验只读步骤",
      ["结构校验没有通过：", "第 4 步是动作步骤，含动作步骤的 Runbook 不会通过只读校验", "第 5 步没有标为只读", "第 1–3、5 步没有绑定连接器"],
      ["请检查步骤类型、只读标记和连接器", "第 4 步没有"],
    ],
    [
      "en", en, "Check the read-only steps",
      ["Structure check failed: ", "step 4 is an action step, and a runbook with action steps never passes the read-only check", "step 5 isn't marked read-only", "steps 1–3, 5 have no connector"],
      ["review step kinds", "step 4 has", "step 4 isn't"],
    ],
  ] as const) {
    test(`the toast names the failing steps and why (${name})`, async ({ page }) => {
      await page.addInitScript(init);
      const m = monitor(page);
      let posted = 0;
      await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, RUNBOOK_DETAIL));
      await page.route(at(`/runbooks/${RUNBOOK}/dry-run`), (r) => (r.request().method() === "POST" ? (posted++, json(r, 200, FAILED)) : r.fallback()));
      await page.goto(origin + `/runbooks/${RUNBOOK}`);
      await page.getByRole("button", { name: button }).click({ timeout: 20_000 });
      await expect(page.locator(".v3-toast")).toHaveCount(1);
      const text = (await toasts(page)).join("\n");
      for (const w of want) expect(text).toContain(w);
      for (const w of notWant) expect(text).not.toContain(w);
      expect(posted).toBe(1);
      expect(m.pageErrors).toEqual([]);
    });
  }

  test("a server that gives no reasons still names the failing steps", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, RUNBOOK_DETAIL));
    const bare = { ...FAILED, checks: FAILED.checks.map(({ reasons: _, ...c }) => c) };
    await page.route(at(`/runbooks/${RUNBOOK}/dry-run`), (r) => (r.request().method() === "POST" ? json(r, 200, bare) : r.fallback()));
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    await page.getByRole("button", { name: "校验只读步骤" }).click({ timeout: 20_000 });
    await expect(page.locator(".v3-toast")).toContainText("结构校验没有通过：第 1–5 步不符合要求");
  });

  test("a passing check still says how many steps passed", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at(`/runbooks/${RUNBOOK}`), (r) => json(r, 200, RUNBOOK_DETAIL));
    const passed = { ...FAILED, steps: 2, ok: true, checks: [check(1, []), check(2, [])] };
    await page.route(at(`/runbooks/${RUNBOOK}/dry-run`), (r) => (r.request().method() === "POST" ? json(r, 200, passed) : r.fallback()));
    await page.goto(origin + `/runbooks/${RUNBOOK}`);
    await page.getByRole("button", { name: "校验只读步骤" }).click({ timeout: 20_000 });
    await expect(page.locator(".v3-toast")).toContainText("结构校验通过：2 个步骤");
  });
});
