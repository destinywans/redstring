import type { Page, Response } from "@playwright/test";

export type ApiCall = { method: string; path: string; status: number; code?: string; message?: string; ms: number };
export type PageReport = {
  route: string;
  instance: string;
  /** Account the page was loaded as (admin unless CRAWL_ROLE says otherwise). */
  role?: string;
  /** Permission gates on the page: entries rendered disabled with the permission to ask for (PA-64). */
  gates?: { reason: string; disabled: boolean }[];
  locale: string;
  theme: string;
  api: ApiCall[];
  consoleErrors: string[];
  pageErrors: string[];
  blank: boolean;
  /** Sample-data markers visible on a real page (PA-21, PA-109, PA-129): sample values posing as real ones. */
  sample?: string[];
  title: string;
  screenshot?: string;
};

/**
 * Strings that only the bundled sample data contains: sample incident, canvas and report IDs, the sample on-call person,
 * the sample service and Feishu channel. None exists in the acceptance data, so on a real page each one is sample
 * content shown as if it were real.
 */
export const SAMPLE_MARKERS = ["INV-231", "INC-1042", "EXE-51", "王晨", "Wang Chen", "#trade-incident", "payment-gateway", "run_7f3a"];

/** Sample markers in the page's visible text and link targets, each with a little context. */
export async function sampleLeaks(page: Page): Promise<string[]> {
  return page.evaluate((markers) => {
    const text = document.getElementById("root")?.innerText ?? "";
    const hrefs = Array.from(document.querySelectorAll("#root a[href]")).map((a) => a.getAttribute("href") ?? "");
    const out: string[] = [];
    for (const m of markers) {
      const i = text.indexOf(m);
      if (i >= 0) out.push(`${m}: …${text.slice(Math.max(0, i - 30), i + m.length + 30).replace(/\s+/g, " ")}…`);
      const h = hrefs.find((x) => x.includes(m));
      if (h) out.push(`${m}: link ${h}`);
    }
    return out;
  }, SAMPLE_MARKERS);
}

/** Collects every /api/v1 response, console error and uncaught exception while a page is open. */
export function monitor(page: Page) {
  const api: ApiCall[] = [];
  const consoleErrors: string[] = [];
  const pageErrors: string[] = [];
  const started = new Map<string, number>();
  const pending: Promise<void>[] = [];

  page.on("request", (req) => started.set(req.url() + req.method(), Date.now()));
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text().slice(0, 500));
  });
  page.on("pageerror", (err) => pageErrors.push(`${err.name}: ${err.message}`.slice(0, 500)));
  page.on("response", (res: Response) => {
    const url = new URL(res.url());
    if (!url.pathname.startsWith("/api/v1/")) return;
    const call: ApiCall = {
      method: res.request().method(),
      path: url.pathname.replace(/^\/api\/v1/, "") + (url.search ? url.search : ""),
      status: res.status(),
      ms: Date.now() - (started.get(res.url() + res.request().method()) ?? Date.now()),
    };
    api.push(call);
    if (res.status() >= 400) {
      pending.push(
        res
          .json()
          .then((b) => {
            call.code = b?.error?.code;
            call.message = String(b?.error?.message ?? "").slice(0, 300);
          })
          .catch(() => undefined),
      );
    }
  });
  let inflight = 0;
  let lastActivity = Date.now();
  page.on("request", (req) => {
    if (new URL(req.url()).pathname.startsWith("/api/v1/")) inflight++, (lastActivity = Date.now());
  });
  const done = (req: { url(): string }) => {
    if (new URL(req.url()).pathname.startsWith("/api/v1/")) inflight = Math.max(0, inflight - 1), (lastActivity = Date.now());
  };
  page.on("requestfinished", done);
  page.on("requestfailed", done);
  return {
    api,
    consoleErrors,
    pageErrors,
    /** Wait until no API call is in flight and none started for `quietMs` (pages that poll never reach networkidle). */
    async quiet(quietMs = 1200, maxMs = 10_000) {
      const until = Date.now() + maxMs;
      while (Date.now() < until && (inflight > 0 || Date.now() - lastActivity < quietMs)) await page.waitForTimeout(150);
    },
    async settle() {
      await Promise.all(pending);
    },
  };
}
