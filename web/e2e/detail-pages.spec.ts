import { test, expect, type Page } from "@playwright/test";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { INSTANCES, LOCAL, type InstanceName } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor, sampleLeaks, type PageReport } from "./support/monitor";

/**
 * T5 (WAN-434) · detail pages and the tabs inside them, against the real backend.
 * crawl.spec.ts only loads the static routes; PA-88 broke on an incident detail page and 0ec6b62d on a tab.
 * For each instance and kind of object: read real ids through the API (as the signed-in admin, from inside
 * the page so Chromium's resolver and certificate pinning apply), load up to MAX_IDS detail pages, then click
 * every tab of every [role=tab] list (the project's Tabs component renders role="tab") one by one.
 * Each load and each tab click writes a PageReport (same shape as the crawl, crawl-summary.py reads it) to
 * DETAIL_OUT (default e2e/detail-crawl); a page fails on uncaught exceptions, a PageBoundary fallback or a blank root.
 *
 * An alert group has no address of its own (`/alerts/:id` only redirects an old link to the incident), so the alerts
 * kind opens /alerts and clicks group rows, which is how the page selects one and loads its policy.
 * Evaluation ids in the route are case ids (EvalCasePage), taken from the cases of the listed experiments.
 *
 * A kind with no object on an instance is skipped, with the reason in the report annotations; that is expected,
 * not a failure. On 2026-10-16 four were skipped that way: prod review and executions (no proposal or execution
 * reachable from the lists below), nonprod agent-runs and evals (none exist). Rerun after drills create them.
 *
 * Needs no write: it only navigates and clicks tabs. DETAIL_WITH_DRAFTS=1 also opens /postmortems/new?incident=…
 * (the AI drafts there, so it costs a model call per incident). DETAIL_ROLE picks the account (admin by default).
 */
const ROLE = process.env.DETAIL_ROLE ?? "admin";
const OUT = resolve(LOCAL, process.env.DETAIL_OUT ?? "e2e/detail-crawl");
const MAX_IDS = 3;
const LOCALE = { locale: "zh-CN", theme: "dark" };
mkdirSync(OUT, { recursive: true });

type Body = unknown;
/** The rows of a list response: a bare array, or the first array-valued field (items, rules, agents, runs …). */
function rows(body: Body): Record<string, unknown>[] {
  if (Array.isArray(body)) return body as Record<string, unknown>[];
  if (body && typeof body === "object") {
    const o = body as Record<string, unknown>;
    for (const key of ["items", "rules", "agents", "runs", "records", "executions"]) if (Array.isArray(o[key])) return o[key] as Record<string, unknown>[];
    for (const v of Object.values(o)) if (Array.isArray(v)) return v as Record<string, unknown>[];
  }
  return [];
}
/** Every string stored under `key` anywhere in a JSON document (ids that sit inside nested lifecycle blocks). */
function collect(body: Body, key: string, out: string[] = []): string[] {
  if (Array.isArray(body)) body.forEach((x) => collect(x, key, out));
  else if (body && typeof body === "object") {
    for (const [k, v] of Object.entries(body as Record<string, unknown>)) {
      if (k === key && typeof v === "string" && v) out.push(v);
      else collect(v, key, out);
    }
  }
  return out;
}
const uniq = <T,>(xs: T[]) => [...new Set(xs)];
const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);

type Get = (path: string) => Promise<Body>;
type Kind = {
  name: string;
  paths: (get: Get, instance: InstanceName) => Promise<string[]>;
  /** Extra interaction on a loaded page, for objects that are selected in the page rather than addressed. */
  explore?: (page: Page, report: (route: string, key: string) => Promise<void>, failures: string[]) => Promise<void>;
};
/**
 * Proposal and execution ids recorded by the prod drills (tools/acceptance/drill.py, rollback.py, delivery_drill.py)
 * and by golden-silence-ui: proposals have no list endpoint, so these are the only way to reach prod review and
 * execution pages (WAN-434 phase 1).
 */
function recorded(instance: InstanceName, key: string): string[] {
  const dir = resolve(LOCAL, "e2e/drills");
  const found: string[] = [];
  // A report names its instance only when it is nonprod (alert-rule-nonprod.json); the rest ran on prod.
  const mine = (f: string) => f.endsWith(".json") && f.includes("nonprod") === (instance === "nonprod");
  for (const f of existsSync(dir) ? readdirSync(dir).filter(mine) : []) {
    try {
      found.push(...collect(JSON.parse(readFileSync(resolve(dir, f), "utf8")), key));
    } catch {
      /* a partial drill report: skip it */
    }
  }
  const silence = resolve(LOCAL, "e2e/golden/silence-proposal.txt");
  if (instance === "prod" && key === "proposal_id" && existsSync(silence)) found.unshift(readFileSync(silence, "utf8").trim());
  return found;
}
const ids = (list: string) => async (get: (p: string) => Promise<Body>) =>
  rows(await get(list)).map((r) => str(r.id)).filter((x): x is string => !!x);

export const KINDS: Kind[] = [
  { name: "incidents", paths: async (get) => (await ids("/incidents")(get)).map((id) => `/incidents/${id}`) },
  {
    name: "alerts",
    paths: async (get) => ((await ids("/alert-groups")(get)).length ? ["/alerts"] : []),
    explore: async (page, report, failures) => {
      const heads = page.locator(".alert-group .ag-head");
      const n = Math.min(await heads.count(), MAX_IDS);
      // The first group is selected on load; the others are clicked in turn, each loading its policy.
      for (let i = 1; i < n; i++) {
        await heads.nth(i).click({ timeout: 5_000 }).catch((e) => failures.push(`/alerts [group ${i}]: click failed (${String(e).split("\n")[0].slice(0, 120)})`));
        await report(`/alerts [group ${i}]`, `/alerts-group-${i}`);
      }
    },
  },
  {
    // One of each canvas kind first, so investigation, plan and review canvases are all opened.
    name: "canvases",
    paths: async (get) => {
      const all = rows(await get("/canvases?page_size=50"));
      const firstOfKind = uniq(all.map((r) => str(r.kind))).map((k) => all.find((r) => r.kind === k)!);
      return [...firstOfKind, ...all].map((r) => str(r.href) ?? (str(r.id) ? `/canvas/${r.id}` : "")).filter((p) => p.startsWith("/"));
    },
  },
  {
    // Proposals have no list of their own; they are linked from silences, rule lifecycles and the inbox.
    name: "review",
    paths: async (get, instance) => {
      const found: string[] = recorded(instance, "proposal_id");
      for (const src of ["/silences", "/alert-rules", "/inbox"]) found.push(...collect(await get(src), "proposal_id"));
      // An incident's own record can link its proposals too.
      for (const id of (await ids("/incidents")(get)).slice(0, 5)) found.push(...collect(await get(`/incidents/${id}`), "proposal_id"));
      return found.map((id) => `/review/${id}`);
    },
  },
  {
    name: "executions",
    paths: async (get, instance) => {
      const found: string[] = recorded(instance, "execution_id");
      for (const src of ["/silences", "/alert-rules"]) found.push(...collect(await get(src), "execution_id"));
      const deployments = await get("/deployments/executions");
      found.push(...collect(deployments, "execution_id"), ...rows(deployments).map((r) => str(r.id)).filter((x): x is string => !!x));
      return found.map((id) => `/executions/${id}`);
    },
  },
  { name: "runbooks", paths: async (get) => (await ids("/runbooks")(get)).map((id) => `/runbooks/${id}`) },
  { name: "conversations", paths: async (get) => (await ids("/conversations")(get)).map((id) => `/conversations/${id}`) },
  { name: "agents", paths: async (get) => (await ids("/agents/lifecycle")(get)).map((id) => `/agents/${id}`) },
  { name: "agent-runs", paths: async (get) => (await ids("/agent-runs")(get)).map((id) => `/agents/runs/${id}`) },
  {
    // The route takes a case id: read the cases of the listed experiments until enough are found.
    name: "evals",
    paths: async (get) => {
      const found: string[] = [];
      for (const id of (await ids("/evals")(get)).slice(0, 5)) {
        const experiment = (await get(`/evals/${id}`)) as { cases?: { id?: string }[] | null } | null;
        for (const c of experiment?.cases ?? []) if (c.id) found.push(c.id);
        if (found.length >= MAX_IDS) break;
      }
      return found.map((id) => `/agents/evals/${id}`);
    },
  },
  {
    name: "traces",
    paths: async (get) => rows(await get("/traces")).map((r) => str(r.id) ?? str(r.trace_id)).filter((x): x is string => !!x).map((id) => `/traces/${id}`),
  },
  {
    name: "services",
    paths: async (get) => (await ids("/services")(get)).flatMap((id) => [`/services/${id}`, `/services/${id}/resilience`]),
  },
  ...(process.env.DETAIL_WITH_DRAFTS
    ? [{ name: "postmortem-drafts", paths: async (get: (p: string) => Promise<Body>) => (await ids("/incidents")(get)).map((id) => `/postmortems/new?incident=${id}`) }]
    : []),
];

/** GET through the page itself; a failed read yields null so one broken list does not hide the other kinds. */
function reader(page: Page, instance: InstanceName) {
  return async (path: string): Promise<Body> =>
    page.evaluate(
      async ([p, env]) => {
        const r = await fetch(`/api/v1${p}`, { headers: { "X-AIOps-Environment": env }, credentials: "same-origin" });
        return r.ok ? await r.json() : null;
      },
      [path, INSTANCES[instance].env] as const,
    );
}

const slug = (s: string) => s.replace(/[^A-Za-z0-9]+/g, "_").replace(/^_|_$/g, "").slice(0, 120);

for (const instance of Object.keys(INSTANCES) as InstanceName[]) {
  test.describe(`${instance} · detail pages`, () => {
    test.use({ storageState: statePath(instance, ROLE) });
    for (const kind of KINDS) {
      test(kind.name, async ({ page }) => {
        test.setTimeout(300_000);
        await page.addInitScript((c) => localStorage.setItem("aiops-v3-preferences", JSON.stringify(c)), LOCALE);
        await page.goto(INSTANCES[instance].origin + "/healthz");
        const targets = uniq(await kind.paths(reader(page, instance), instance).catch(() => [])).slice(0, MAX_IDS * (kind.name === "services" ? 2 : 1));
        test.skip(targets.length === 0, `no ${kind.name} on ${instance} to open (empty list, or the list could not be read)`);
        const failures: string[] = [];

        // One monitor for the whole test; each report takes what arrived since the previous one.
        const m = monitor(page);
        let seen = { api: 0, console: 0, errors: 0 };
        const report = async (route: string, key: string) => {
          await m.quiet(800, 8_000);
          await m.settle();
          const caught = await page.locator("[data-page-error]").first().getAttribute("data-page-error", { timeout: 500 }).catch(() => null);
          if (caught) m.pageErrors.push(`PageBoundary: ${caught}`);
          const blank = await page.evaluate(() => (document.getElementById("root")?.innerText ?? "").trim().length < 20);
          const sample = await sampleLeaks(page);
          const gates = await page.evaluate(() =>
            Array.from(document.querySelectorAll(".perm-gate")).map((g) => ({
              reason: (g.querySelector(".sr-only")?.textContent ?? "").trim(),
              disabled: !!g.querySelector("[disabled], [aria-disabled='true']"),
            })),
          );
          const entry: PageReport = {
            route, instance, role: ROLE, gates, ...LOCALE,
            api: m.api.slice(seen.api), consoleErrors: m.consoleErrors.slice(seen.console), pageErrors: m.pageErrors.slice(seen.errors), blank, sample,
            title: await page.title(),
          };
          seen = { api: m.api.length, console: m.consoleErrors.length, errors: m.pageErrors.length };
          writeFileSync(resolve(OUT, `${instance}-${slug(key)}.json`), JSON.stringify(entry, null, 2));
          if (entry.pageErrors.length) failures.push(`${route}: ${entry.pageErrors[0]}`);
          if (blank) failures.push(`${route}: blank page`);
          if (sample.length) failures.push(`${route}: sample data shown as real: ${sample.join("; ")}`);
        };

        for (const path of targets) {
          await page.goto(INSTANCES[instance].origin + path);
          const mounted = await page.waitForFunction(() => !document.getElementById("boot"), undefined, { timeout: 20_000 }).then(() => true, () => false);
          if (!mounted) failures.push(`${path}: app did not mount within 20s`);
          await page.waitForFunction(() => !document.querySelector("[data-loading]"), undefined, { timeout: 20_000 }).catch(() => undefined);
          await report(path, path);

          // Every tab list, every tab: the one already selected was loaded above, the others are clicked in turn.
          const lists = await page.locator('[role="tablist"]').count();
          for (let li = 0; li < lists; li++) {
            const labels = await page.locator('[role="tablist"]').nth(li).locator('[role="tab"]').allInnerTexts().catch(() => []);
            for (let ti = 0; ti < labels.length; ti++) {
              const tab = page.locator('[role="tablist"]').nth(li).locator('[role="tab"]').nth(ti);
              if ((await tab.getAttribute("aria-selected").catch(() => null)) === "true" || !(await tab.isEnabled().catch(() => false))) continue;
              const name = labels[ti].replace(/\s+/g, " ").trim().slice(0, 40) || `tab${ti}`;
              await tab.click({ timeout: 5_000 }).catch((e) => failures.push(`${path} [tab ${name}]: click failed (${String(e).split("\n")[0].slice(0, 120)})`));
              await report(`${path} [tab ${name}]`, `${path}-tab-${li}-${ti}-${name}`);
            }
          }
          await kind.explore?.(page, report, failures);
        }
        expect(failures, "uncaught exceptions, error fallbacks or blank pages").toEqual([]);
      });
    }
  });
}
