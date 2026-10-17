import { test, type Page, type Route } from "@playwright/test";

/**
 * Shared helpers for the WAN-434 fix checks (phase2-fixes*.spec.ts). Each check pins the page to fixed API answers
 * with page.route, so it runs on any data and never writes. Real records are read in the page: *.redstring.test
 * resolves only through the browser's host rules, and the session cookie applies there.
 */
export const zh = () => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "zh-CN", theme: "light" }));
export const en = () => localStorage.setItem("aiops-v3-preferences", JSON.stringify({ locale: "en-US", theme: "light" }));
export const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
/** Text of every toast shown so far. */
export const toasts = (page: Page) => page.locator(".v3-toast").allInnerTexts();
/** Matches one API path exactly, with or without a query string. */
export const at = (path: string) => (url: URL) => url.pathname === "/api/v1" + path;

/** GET an API path in the page (prod environment header); { status, body }. Call after page.goto(origin + "/healthz"). */
export async function read(page: Page, path: string): Promise<{ status: number; body: any }> {
  return page.evaluate(async (p) => {
    const r = await fetch("/api/v1" + p, { headers: { "X-AIOps-Environment": "prod" } });
    return { status: r.status, body: await r.json().catch(() => null) };
  }, path);
}

/** The first item ID a list endpoint returns, or skip the test when there is none. */
export async function someId(page: Page, path: string, what: string, pick: (item: any) => string | undefined = (i) => i.id): Promise<string> {
  const { body } = await read(page, path);
  const id = ((body?.items ?? []) as any[]).map(pick).find(Boolean);
  test.skip(!id, `no ${what}`);
  return id!;
}

/** Answers GET path with the real answer, patched (the real one is read first, in the page). */
export async function patchRead(page: Page, path: string, patch: (real: any) => unknown) {
  const real = (await read(page, path)).body;
  await page.route(at(path), (r) => json(r, 200, patch(real)));
}
