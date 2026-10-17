import { useRef } from "react";
import { useMutation, useQuery, useQueryClient, type UseQueryOptions } from "@tanstack/react-query";
import { currentLocale, usePreferences } from "../app/preferences";
import { handleMock } from "../mock/router";
import { currentEnv, useEnv } from "../app/environment";

/** `mock` serves the contract from in-browser fixtures; `http` calls the Go API under /api/v1. */
export const API_MODE: "mock" | "http" = import.meta.env.VITE_API_MODE === "http" ? "http" : "mock";

/**
 * The project page the sample-data badge links to, set only when building the public demo
 * (`VITE_DEMO_ABOUT_URL`, with an optional English page in `VITE_DEMO_ABOUT_URL_EN`). Other builds
 * leave it unset and the badge stays a plain label.
 */
const aboutZh = import.meta.env.VITE_DEMO_ABOUT_URL;
export const DEMO_ABOUT_URL = aboutZh ? { zh: aboutZh, en: import.meta.env.VITE_DEMO_ABOUT_URL_EN || aboutZh } : null;

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    /** The rest of the error object, e.g. `max_range` on 422 range_exceeds_lookback (PA-79). */
    public detail: Record<string, unknown> = {},
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export type Query = Record<string, string | number | boolean | undefined | null | string[]>;

function qs(query?: Query) {
  if (!query) return "";
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === "") continue;
    if (Array.isArray(v)) v.forEach((x) => p.append(k, x));
    else p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const RETURN_TITLE_KEY = "aiops-v3-return-title";
export const SIGNED_IN_KEY = "aiops-v3-signed-in";
let redirecting = false;

/**
 * Whether a business call's 401 means the session is gone. Every server 401 says `unauthorized`, whether the session
 * expired, was revoked or is missing, or a route checked its own credentials (an inbound receiver's source token or
 * signature, PA-106), so the code cannot tell them apart. GET /auth/me checks the session alone: 401 only when no
 * live session remains, 503 when the session store can't be read (PA-59). Anything but its 401 keeps the user signed
 * in. Concurrent 401s share one check.
 */
let sessionCheck: Promise<boolean> | null = null;
function sessionGone(): Promise<boolean> {
  sessionCheck ??= fetch("/api/v1/auth/me", { credentials: "same-origin", headers: { "Accept-Language": currentLocale() } })
    .then((r) => r.status === 401)
    .catch(() => false)
    .finally(() => {
      sessionCheck = null;
    });
  return sessionCheck;
}

/** The session is gone: go to /login and come back afterwards. */
function toLogin() {
  if (redirecting || location.pathname === "/login") return;
  redirecting = true;
  let hadSession = false;
  try {
    hadSession = localStorage.getItem(SIGNED_IN_KEY) === "1";
    sessionStorage.setItem(RETURN_TITLE_KEY, document.title.replace(/ · Redstring$/, ""));
  } catch {
    /* convenience only */
  }
  const next = encodeURIComponent(location.pathname + location.search);
  location.assign(`/login?next=${next}${hadSession ? "&expired=1" : ""}`);
}

/** A fresh idempotency key; writes carry one so the server can replay instead of repeating them. */
export const newIdempotencyKey = () =>
  typeof crypto.randomUUID === "function" ? crypto.randomUUID() : Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");

export async function api<T>(
  method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE",
  path: string,
  opts: { query?: Query; body?: unknown; idempotencyKey?: string; headers?: Record<string, string> } = {},
): Promise<T> {
  return (await apiWithMeta<T>(method, path, opts)).data;
}

/** Like `api`, but also returns the response ETag for reads whose writes need `If-Match` (null in mock mode or when absent). */
export async function apiWithMeta<T>(
  method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE",
  path: string,
  opts: { query?: Query; body?: unknown; idempotencyKey?: string; headers?: Record<string, string> } = {},
): Promise<{ data: T; etag: string | null; revision?: string | null }> {
  if (API_MODE === "mock") return { data: await handleMock<T>(method, path, opts.query ?? {}, opts.body), etag: null };
  const res = await fetch(`/api/v1${path}${qs(opts.query)}`, {
    method,
    credentials: "same-origin",
    headers: {
      "Accept-Language": currentLocale(),
      // Global environment (see app/environment.ts); production instances ignore it.
      ...(currentEnv() ? { "X-AIOps-Environment": currentEnv() } : {}),
      ...(opts.body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(method !== "GET" ? { "Idempotency-Key": opts.idempotencyKey ?? newIdempotencyKey() } : {}),
      ...opts.headers,
    },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });
  if (res.status === 401 && !path.startsWith("/auth/") && (await sessionGone())) toLogin();
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new ApiError(res.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `HTTP ${res.status}`, body?.error ?? {});
  }
  const etag = res.headers.get("ETag");
  // Role matrices carry their revision in their own header (PUT needs it back as If-Role-Matrix-Revision).
  const revision = res.headers.get("X-Role-Matrix-Revision");
  if (res.status === 204) return { data: undefined as T, etag, revision };
  return { data: (await res.json()) as T, etag, revision };
}

/** Typed GET bound to the current locale, so switching language refetches server-localised text. */
export function useGet<T>(path: string | null, query?: Query, options?: Partial<UseQueryOptions<T>>) {
  const locale = usePreferences((s) => s.locale);
  const env = useEnv((s) => s.env);
  // Business reads wait for /instance to settle the environment; firing earlier sends no
  // X-AIOps-Environment (422 on non-production instances) and then refetches under the real key.
  const ready = useEnv((s) => s.instance !== null) || path === "/instance" || API_MODE === "mock";
  return useQuery<T>({
    // Switching environment refetches, like switching language. /instance is what settles the environment and
    // doesn't depend on it; keying it by env fetched it twice on every fresh load (PA-14).
    queryKey: [path, query ?? {}, locale, path === "/instance" ? "" : env],
    queryFn: () => api<T>("GET", path!, { query }),
    // Keep showing the same resource while it refetches for another environment or language;
    // a different path or query is a different resource and must not show the old one.
    placeholderData: (prev, prevQuery) =>
      prevQuery && prevQuery.queryKey[0] === path && JSON.stringify(prevQuery.queryKey[1]) === JSON.stringify(query ?? {}) ? prev : undefined,
    staleTime: 30_000,
    ...options,
    enabled: path !== null && ready && (options?.enabled ?? true),
  });
}

/** `useGet` for resources saved with `If-Match`: returns `{ data, etag }` so the page can send the revision back. */
export function useGetWithEtag<T>(path: string | null, options?: Partial<UseQueryOptions<{ data: T; etag: string | null; revision?: string | null }>>) {
  const locale = usePreferences((s) => s.locale);
  const env = useEnv((s) => s.env);
  const ready = useEnv((s) => s.instance !== null) || API_MODE === "mock";
  return useQuery<{ data: T; etag: string | null; revision?: string | null }>({
    queryKey: [path, { etag: true }, locale, env],
    queryFn: () => apiWithMeta<T>("GET", path!),
    staleTime: 30_000,
    ...options,
    enabled: path !== null && ready && (options?.enabled ?? true),
  });
}

export function useSend<TBody, TResult = unknown>(
  method: "POST" | "PATCH" | "PUT" | "DELETE",
  path: string | ((body: TBody) => string),
  invalidate: string[] = [],
  /** What goes on the wire when the path already carries part of the variables (PA-142: `{ id, enabled }` → `{ enabled }`). */
  toBody?: (body: TBody) => unknown,
) {
  const client = useQueryClient();
  // Re-sending the identical request while it is in flight or after it failed (double click, retry after a
  // dropped connection) reuses its key, so the server replays instead of repeating it. Success starts afresh.
  const last = useRef<{ sig: string; key: string } | null>(null);
  const m = useMutation<TResult, ApiError, TBody>({
    mutationFn: (body) => {
      const url = typeof path === "function" ? path(body) : path;
      const sent = toBody ? toBody(body) : body;
      const sig = `${method} ${url} ${JSON.stringify(sent ?? null)}`;
      if (last.current?.sig !== sig) last.current = { sig, key: newIdempotencyKey() };
      return api<TResult>(method, url, { body: sent, idempotencyKey: last.current.key });
    },
    onSuccess: () => {
      last.current = null;
      for (const prefix of invalidate)
        client.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === "string" && (q.queryKey[0] as string).startsWith(prefix) });
    },
  });
  // A caller that passes no onError used to fail silently (the button just stopped spinning): say why instead (PA-136).
  // Loaded lazily: overlays → core → … would otherwise import this module back while it initialises.
  type Opts = Parameters<typeof m.mutate>[1];
  const mutate = (vars: TBody, opts?: Opts) =>
    m.mutate(vars, {
      ...opts,
      onError:
        opts?.onError ??
        ((e) =>
          void import("../ui/overlays").then(({ toast }) =>
            toast(e?.message || (currentLocale() === "en-US" ? "That didn't work; try again later" : "操作失败，请稍后重试"), { tone: "danger" }),
          )),
    });
  return { ...m, mutate: mutate as typeof m.mutate };
}
