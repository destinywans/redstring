import { createContext, useContext, useEffect, useMemo } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import type { Application } from "./types";

export type EnvironmentOption = {
  key: string;
  label: string;
  environmentIds: string[];
};

/** Environment keys are shared viewing scopes; concrete environment IDs remain action targets. */
export function useEnvironmentScopeState(
  userId: string | undefined,
  applications: Application[],
  ready: boolean,
) {
  const location = useLocation();
  const navigate = useNavigate();
  const storageKey = userId ? `aiops.environment-scope.${userId}` : "";
  const saved = (() => {
    if (!storageKey) return "";
    try {
      return localStorage.getItem(storageKey) ?? "";
    } catch {
      return "";
    }
  })();
  const params = new URLSearchParams(location.search);
  const selected = params.get("environment_scope");
  const key = selected === null ? saved : selected === "all" ? "" : selected;
  const options = useMemo(() => {
    const groups = new Map<string, { names: Set<string>; ids: Set<string> }>();
    for (const app of applications) {
      if (app.archived) continue;
      for (const env of app.environments ?? []) {
        if (env.archived) continue;
        const groupKey = env.key || `id:${env.id}`;
        const group = groups.get(groupKey) ?? {
          names: new Set<string>(),
          ids: new Set<string>(),
        };
        group.names.add(env.name || env.key || env.id);
        group.ids.add(env.id);
        groups.set(groupKey, group);
      }
    }
    const rows = Array.from(groups, ([groupKey, group]) => ({
      key: groupKey,
      label: [...group.names]
        .sort((a, b) => a.localeCompare(b, "zh-CN"))
        .join(" / "),
      environmentIds: [...group.ids].sort(),
    }));
    const counts = new Map<string, number>();
    rows.forEach((row) =>
      counts.set(row.label, (counts.get(row.label) ?? 0) + 1),
    );
    return rows
      .map((row) => ({
        ...row,
        label:
          (counts.get(row.label) ?? 0) > 1
            ? `${row.label} · ${row.key}`
            : row.label,
      }))
      .sort((a, b) => a.label.localeCompare(b.label, "zh-CN"));
  }, [applications]);
  const option = options.find((row) => row.key === key);
  useEffect(() => {
    if (!userId) return;
    try {
      localStorage.setItem(storageKey, key);
    } catch {
      /* Private storage may be unavailable. */
    }
    if (!new URLSearchParams(location.search).has("environment_scope")) {
      const next = new URLSearchParams(location.search);
      next.set("environment_scope", key || "all");
      navigate(
        {
          pathname: location.pathname,
          search: next.toString(),
          hash: location.hash,
        },
        { replace: true },
      );
    }
  }, [
    userId,
    storageKey,
    key,
    location.pathname,
    location.search,
    location.hash,
    navigate,
  ]);
  const withScope = (to: string) => {
    const url = new URL(to, window.location.origin);
    if (!url.searchParams.has("environment_scope"))
      url.searchParams.set("environment_scope", key || "all");
    return `${url.pathname}${url.search}${url.hash}`;
  };
  const setEnvironmentScope = (nextKey: string) => {
    const next = new URLSearchParams(location.search);
    next.set("environment_scope", nextKey || "all");
    // Explicit object links and frozen session targets must not be retargeted here.
    navigate({
      pathname: location.pathname,
      search: next.toString(),
      hash: location.hash,
    });
  };
  return {
    key,
    label: key ? (option?.label ?? "当前环境不可用") : "全部环境",
    options,
    environmentIds: option?.environmentIds ?? [],
    loading: !ready,
    hasMatches: !key || !!option,
    withScope,
    setEnvironmentScope,
  };
}
export type EnvironmentScope = ReturnType<typeof useEnvironmentScopeState>;
export const EnvironmentScopeContext = createContext<EnvironmentScope | null>(
  null,
);
export function useEnvironmentScope() {
  const context = useContext(EnvironmentScopeContext);
  if (!context) throw new Error("Environment scope is unavailable");
  return context;
}
