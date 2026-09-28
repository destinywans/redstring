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

export async function api<T>(
  method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE",
  path: string,
  opts: { query?: Query; body?: unknown } = {},
): Promise<T> {
  if (API_MODE === "mock") return handleMock<T>(method, path, opts.query ?? {}, opts.body);
  const res = await fetch(`/api/v1${path}${qs(opts.query)}`, {
    method,
    credentials: "same-origin",
    headers: {
      "Accept-Language": currentLocale(),
      // Global environment (see app/environment.ts); production instances ignore it.
      ...(currentEnv() ? { "X-AIOps-Environment": currentEnv() } : {}),
      ...(opts.body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new ApiError(res.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

/** Typed GET bound to the current locale, so switching language refetches server-localised text. */
export function useGet<T>(path: string | null, query?: Query, options?: Partial<UseQueryOptions<T>>) {
  const locale = usePreferences((s) => s.locale);
  const env = useEnv((s) => s.env);
  return useQuery<T>({
    // Switching environment refetches, like switching language.
    queryKey: [path, query ?? {}, locale, env],
    queryFn: () => api<T>("GET", path!, { query }),
    // Keep showing the same resource while it refetches for another environment or language;
    // a different path or query is a different resource and must not show the old one.
    placeholderData: (prev, prevQuery) =>
      prevQuery && prevQuery.queryKey[0] === path && JSON.stringify(prevQuery.queryKey[1]) === JSON.stringify(query ?? {}) ? prev : undefined,
    enabled: path !== null,
    staleTime: 30_000,
    ...options,
  });
}

export function useSend<TBody, TResult = unknown>(
  method: "POST" | "PATCH" | "PUT" | "DELETE",
  path: string | ((body: TBody) => string),
  invalidate: string[] = [],
) {
  const client = useQueryClient();
  return useMutation<TResult, ApiError, TBody>({
    mutationFn: (body) => api<TResult>(method, typeof path === "function" ? path(body) : path, { body }),
    onSuccess: () => {
      for (const prefix of invalidate)
        client.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === "string" && (q.queryKey[0] as string).startsWith(prefix) });
    },
  });
}
