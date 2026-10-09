import { useRef } from "react";
import { useMutation, useQuery, useQueryClient, type UseQueryOptions } from "@tanstack/react-query";
import { currentLocale, usePreferences } from "../app/preferences";
import { handleMock } from "../mock/router";
import { currentEnv, useEnv } from "../app/environment";

/** `mock` serves the contract from in-browser fixtures; `http` calls the Go API under /api/v1. */
export const API_MODE: "mock" | "http" = import.meta.env.VITE_API_MODE === "http" ? "http" : "mock";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
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

/** A 401 from any business call means the session is gone: go to /login and come back afterwards. */
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
): Promise<{ data: T; etag: string | null }> {
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
  if (res.status === 401 && !path.startsWith("/auth/")) toLogin();
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new ApiError(res.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `HTTP ${res.status}`);
  }
  const etag = res.headers.get("ETag");
  if (res.status === 204) return { data: undefined as T, etag };
  return { data: (await res.json()) as T, etag };
}

/** Typed GET bound to the current locale, so switching language refetches server-localised text. */
export function useGet<T>(path: string | null, query?: Query, options?: Partial<UseQueryOptions<T>>) {
  const locale = usePreferences((s) => s.locale);
  const env = useEnv((s) => s.env);
  // Business reads wait for /instance to settle the environment; firing earlier sends no
  // X-AIOps-Environment (422 on non-production instances) and then refetches under the real key.
  const ready = useEnv((s) => s.instance !== null) || path === "/instance" || API_MODE === "mock";
  return useQuery<T>({
    // Switching environment refetches, like switching language.
    queryKey: [path, query ?? {}, locale, env],
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
export function useGetWithEtag<T>(path: string | null, options?: Partial<UseQueryOptions<{ data: T; etag: string | null }>>) {
  const locale = usePreferences((s) => s.locale);
  const env = useEnv((s) => s.env);
  const ready = useEnv((s) => s.instance !== null) || API_MODE === "mock";
  return useQuery<{ data: T; etag: string | null }>({
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
) {
  const client = useQueryClient();
  // Re-sending the identical request while it is in flight or after it failed (double click, retry after a
  // dropped connection) reuses its key, so the server replays instead of repeating it. Success starts afresh.
  const last = useRef<{ sig: string; key: string } | null>(null);
  return useMutation<TResult, ApiError, TBody>({
    mutationFn: (body) => {
      const url = typeof path === "function" ? path(body) : path;
      const sig = `${method} ${url} ${JSON.stringify(body ?? null)}`;
      if (last.current?.sig !== sig) last.current = { sig, key: newIdempotencyKey() };
      return api<TResult>(method, url, { body, idempotencyKey: last.current.key });
    },
    onSuccess: () => {
      last.current = null;
      for (const prefix of invalidate)
        client.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === "string" && (q.queryKey[0] as string).startsWith(prefix) });
    },
  });
}
