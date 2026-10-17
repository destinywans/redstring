import { test, expect, type Page } from "@playwright/test";
import { INSTANCES, type InstanceName } from "./support/env";
import { statePath } from "./support/global-setup";

/**
 * Backend text in the request language (PA-113, PA-166; WAN-434 phase 2). Unlike phase2-fixes.spec.ts these checks
 * read the real deployed API from inside the page (so the session cookie applies), once with Accept-Language zh-CN
 * and once with en-US. They only send GET requests and never write. A check skips when the instance has no data for
 * it. Silence proposal reviews (PA-121), Agent lifecycle rejections and the source probe need a write, and the
 * postmortem draft titles (PA-113) need a resolved incident without a postmortem, which the acceptance data never has:
 * the Go tests cover those instead.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-text.spec.ts
 */
type Lang = "zh-CN" | "en-US";

async function get<T>(page: Page, env: string, path: string, lang: Lang): Promise<{ status: number; body: T }> {
  return page.evaluate(
    async ({ env, path, lang }) => {
      const r = await fetch("/api/v1" + path, { headers: { "X-AIOps-Environment": env, "Accept-Language": lang } });
      return { status: r.status, body: (await r.json().catch(() => null)) as T };
    },
    { env, path, lang },
  );
}

const both = <T>(zh: T, en: T) => [["zh-CN", zh], ["en-US", en]] as const;

for (const name of Object.keys(INSTANCES) as InstanceName[]) {
  const { origin, env } = INSTANCES[name];

  test.describe(`backend text in the request language · ${name}`, () => {
    test.use({ storageState: statePath(name, "admin") });
    test.beforeEach(async ({ page }) => {
      await page.goto(origin + "/healthz");
    });

    test("postmortem list KPIs and unknown markers (PA-166)", async ({ page }) => {
      type Summary = { kpis: { id: string; label: string; unit?: string }[]; items: { cause: string; actions: string }[] };
      for (const [lang, want] of both(
        { count: "可见复盘数", mttr: "平均恢复时长（已报告事件）", unit: "分钟", cause: "未知", actions: "未知 / " },
        { count: "Visible postmortems", mttr: "Mean resolution, reported incidents", unit: "min", cause: "Unknown", actions: "unknown / " },
      )) {
        const { status, body } = await get<Summary>(page, env, "/postmortems", lang);
        expect(status, lang).toBe(200);
        expect(body.kpis.find((k) => k.id === "postmortem_count")?.label, lang).toBe(want.count);
        const mttr = body.kpis.find((k) => k.id === "reported_mttr");
        if (mttr) expect([mttr.label, mttr.unit], lang).toEqual([want.mttr, want.unit]);
        for (const item of body.items) {
          expect(item.cause, lang).toBe(want.cause);
          expect(item.actions.startsWith(want.actions), `${lang}: ${item.actions}`).toBe(true);
        }
      }
    });

    test("Runbook drafted from a published postmortem (PA-113)", async ({ page }) => {
      const { body: list } = await get<{ items?: { id: string; status: string }[] }>(page, env, "/postmortems", "zh-CN");
      const published = list?.items?.find((p) => p.status === "published")?.id;
      test.skip(!published, "no published postmortem");
      for (const [lang, prefix] of both("核对：", "Review: ")) {
        const { status, body } = await get<{ title: string; steps: { kind: string; title: string }[] }>(
          page, env, `/runbooks/draft?source=postmortem&from=${encodeURIComponent(published!)}`, lang);
        test.skip(status === 422, "the published postmortem is not a current Runbook source");
        expect(status, lang).toBe(200);
        for (const step of body.steps.filter((s) => s.kind === "check")) {
          expect(step.title.startsWith(prefix), `${lang}: ${step.title}`).toBe(true);
        }
        expect(body.title.startsWith(lang === "zh-CN" ? "由复盘 " : "Runbook from postmortem "), lang).toBe(true);
      }
    });

    test("model page used-by and scope columns (PA-166)", async ({ page }) => {
      type Admin = { items: { used_by: string; scope: string }[] };
      const probe = await get<Admin>(page, env, "/models/admin", "zh-CN");
      test.skip(probe.status === 503, "model bindings are not configured on this instance");
      for (const [lang, want] of both({ used: ["暂无使用记录", "历史："], scope: "环境：" }, { used: ["No recorded use", "Historical: "], scope: "Environment: " })) {
        const { status, body } = await get<Admin>(page, env, "/models/admin", lang);
        expect(status, lang).toBe(200);
        for (const item of body.items) {
          expect(want.used.some((w) => item.used_by.startsWith(w)), `${lang}: ${item.used_by}`).toBe(true);
          expect(item.scope.startsWith(want.scope), `${lang}: ${item.scope}`).toBe(true);
        }
      }
    });

    // Only prod lists Agent tools in the acceptance data; nonprod would always skip, and regress.py fails a skip.
    if (name === "prod")
      test("Agent tool approval column (PA-166)", async ({ page }) => {
        type Tools = { items: { approval: string }[] };
        const probe = await get<Tools>(page, env, "/agent-tools", "zh-CN");
        test.skip(probe.status !== 200 || !probe.body.items?.length, "no Agent tool is visible");
        for (const [lang, allowed] of both(["无审批要求", "需人工批准"], ["No approval configured", "Human approval required"])) {
          const { status, body } = await get<Tools>(page, env, "/agent-tools", lang);
          expect(status, lang).toBe(200);
          for (const item of body.items) expect(allowed, `${lang}: ${item.approval}`).toContain(item.approval);
        }
      });
  });
}
