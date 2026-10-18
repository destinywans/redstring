import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, patchRead, read, someId, zh } from "./support/checks";

/**
 * WAN-434 phase 2 batch between rounds 2 and 3 (see support/checks.ts: fixed API answers, runs on any data, never
 * writes).
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-gates.spec.ts
 */
const origin = INSTANCES.prod.origin;
const at0 = "2026-10-17T09:00:00Z";

// PA-192 (WAN-549): the sidebar's "Agents & versions" pointed at the sample agent "investigator" (404 on a real server).
const agentId = "pa192-check";
const agent = { id: agentId, icon: "cpu", name: "检查用 Agent", desc: "检查用", version: "v1", level: "L0", score: null, runs_7d: null, success: null, avg_tokens: null, status: "paused" };
const lifecycle = (agents: object[]) => ({ stages: null, agents, todos: null, missing: ["stages", "todos"] });
const agentDetail = {
  ...agent,
  revision: 1,
  owner: "check-owner",
  account: null,
  summary: "检查用",
  versions: [{ version: "v1", status: "draft", traffic: null, score: null, success: null, tokens: null, created: at0 }],
  release: { from: "v1", to: "", current: null, target: 0, gates: null, tokens: null },
  diff: null,
  config: null,
};

test.describe("Agents & versions entry (PA-192)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the sidebar entry opens the first listed agent, never the sample id", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.route(at("/agents/lifecycle"), (r) => json(r, 200, lifecycle([agent])));
    await page.route(at(`/agents/${agentId}`), (r) => json(r, 200, agentDetail));
    await page.goto(origin + "/agents/lifecycle");
    await page.locator("a.nav-child", { hasText: "Agent 与版本" }).click({ timeout: 20_000 });
    await expect(page).toHaveURL(origin + "/agents");
    await expect(page.getByRole("heading", { name: "检查用 Agent" })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("a.nav-child.active")).toContainText("Agent 与版本");
    await expect(page.getByText("找不到这条记录")).toHaveCount(0);
    expect(m.api.filter((c) => c.path.startsWith("/agents/investigator"))).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  test("with no agents the page says so instead of loading for ever", async ({ page }) => {
    await page.addInitScript(zh);
    await page.route(at("/agents/lifecycle"), (r) => json(r, 200, lifecycle([])));
    await page.goto(origin + "/agents");
    await expect(page.getByText("还没有你能查看的 Agent")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("找不到这条记录")).toHaveCount(0);
  });
});

// PA-198 (WAN-556): a context the server hasn't measured (tokens / budget null) read as "0.0k / 28k tokens · 0%".
test.describe("unknown context usage on the canvas (PA-198)", () => {
  test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

  test("the node inspector says the input budget is unknown, not 0.0k / 28k", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    await page.goto(origin + "/healthz");
    const id = await someId(page, "/canvases?kind=investigation", "investigation canvas");
    await patchRead(page, `/investigations/${id}/canvas`, (real) => ({ ...real, nodes: (real.nodes as { context: object }[]).map((n) => ({ ...n, context: { ...n.context, tokens: null } })) }));
    await page.goto(origin + `/canvas/${id}`);
    const node = page.locator("article.cv-node").first();
    await node.waitFor({ timeout: 20_000 });
    await node.click({ position: { x: 40, y: 16 } });
    const panel = page.locator(".dp");
    await panel.getByRole("tab", { name: /^上下文/ }).click({ timeout: 10_000 });
    await expect(panel).toContainText("输入预算");
    await expect(panel).toContainText("这条分支当前的上下文用量还没有测量");
    await expect(panel).not.toContainText("0.0k");
    await expect(panel).not.toContainText("28k tokens");
    await expect(panel.locator(".ctx-bar")).toHaveCount(0);
    expect(m.pageErrors).toEqual([]);
  });

  test("the write-back preview says the context impact is unknown, not +0.00k", async ({ page }) => {
    await page.addInitScript(zh);
    await page.goto(origin + "/healthz");
    // A branch to write back from (read-only lookups; the preview itself is pinned below).
    let inv: { id: string; branch: string } | null = null;
    for (const c of ((await read(page, "/canvases?kind=investigation")).body?.items ?? []) as { id: string }[]) {
      const branch = ((await read(page, `/investigations/${c.id}/canvas`)).body?.nodes as { id: string; kind: string }[] | undefined)?.find((n) => n.kind === "branch");
      if (branch) {
        inv = { id: c.id, branch: branch.id };
        break;
      }
    }
    test.skip(!inv, "no investigation with a branch");
    await page.route((u) => u.pathname.endsWith("/writeback-preview"), (r) =>
      json(r, 200, {
        from: { id: inv!.branch, title: "检查用分支", version: "v1" }, to: { id: "target-check", title: "主线", version: "v3", next_version: "v4", hops: 1 },
        conclusions: [{ id: "msg-check", text: "检查用结论", evidence: [], recommended: true }], timeline: [], checks: [], budget: { before: null, after: null, limit: null },
        target: "target-check", source_revision: 5, target_revision: 3,
      }),
    );
    await page.goto(origin + `/canvas/${inv!.id}`);
    await page.locator(`article.cv-node[data-node="${inv!.branch}"]`).click({ position: { x: 40, y: 16 }, timeout: 20_000 });
    await page.getByRole("toolbar", { name: "画布工具" }).getByRole("button", { name: "回写" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("上下文影响", { timeout: 10_000 });
    await expect(dialog).toContainText("主线的上下文用量和输入预算还没有测量");
    await expect(dialog).not.toContainText("+0.00k");
    await expect(dialog).not.toContainText("0k 输入预算");
  });
});

// PA-199 (WAN-557): "Bring into investigation" opens a new conversation; without conversation.change it was clickable
// and the new-conversation page only then said the permission was missing.
const changes = (incident: string) => ({
  incident_id: incident,
  service_id: "svc-check",
  environment_id: "prod",
  alert_started_at: at0,
  window: { start_at: "2026-10-17T08:00:00Z", end_at: "2026-10-17T09:15:00Z" },
  items: [
    {
      kind: "gitlab_deployment", id: "gitlab:deployment:42:7", at: "2026-10-17T08:50:00Z", offset_seconds: -600, basis: "time_adjacent", status: "success",
      sha: "0123456789abcdef0123456789abcdef01234567", short_sha: "01234567", ref: "main", href: "/changes?service_id=svc-check&range=24h",
      pipeline_id: null, merge_request: null, kubernetes_revision: null, web_url: null,
    },
  ],
  untimed_revisions: [],
  coverage: { gitlab: "complete", kubernetes: "not_configured" },
  note: "time_adjacent_not_causal",
  unavailable: {},
});

for (const [role, gated] of [["approver2", true], ["admin", false]] as const) {
  test.describe(`bring changes into an investigation as ${role} (PA-199)`, () => {
    // change-approver views everything but holds no conversation.change; admin holds it.
    test.use({ storageState: statePath("prod", role), viewport: { width: 1440, height: 1000 } });

    test(gated ? "the entry is disabled and names the permission" : "the entry stays enabled", async ({ page }) => {
      await page.addInitScript(zh);
      await page.goto(origin + "/healthz");
      const id = await someId(page, "/incidents?status=all", "incident");
      await page.route(at(`/incidents/${id}/changes`), (r) => json(r, 200, changes(id)));
      await page.goto(origin + `/incidents/${id}`);
      const entry = page.getByRole("button", { name: "带入调查" });
      await entry.waitFor({ timeout: 20_000 });
      const gate = page.locator(".perm-gate", { has: entry });
      if (gated) {
        await expect(entry).toBeDisabled();
        await expect(gate.locator(".sr-only")).toHaveText("需要 对话与画布·修改 权限");
        await entry.click({ force: true });
        await page.waitForTimeout(500);
        expect(new URL(page.url()).pathname).toBe(`/incidents/${id}`);
      } else {
        await expect(entry).toBeEnabled();
        await expect(gate).toHaveCount(0);
      }
    });
  });
}
