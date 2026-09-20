import { useCallback } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import type { Application } from "./types";

/** Selection is page-local URL state; unscoped navigation must stay unscoped. */
export function useWorkspaceSelection(
  applications: Application[],
  _ready: boolean,
  preview: boolean,
  fixedScope?: { applicationId: string; environmentId: string },
  globalEnvironmentKey = "",
) {
  const location = useLocation(),
    navigate = useNavigate();
  const params = new URLSearchParams(location.search);
  const applicationId =
    fixedScope?.applicationId ?? params.get("application") ?? "";
  const environmentId =
    fixedScope?.environmentId ?? params.get("environment") ?? "";
  const application = applications.find((a) => a.id === applicationId) ?? null;
  const environments =
    application?.environments?.filter(
      (e) => !e.archived || e.id === environmentId,
    ) ?? [];
  const scopedEnvironments = globalEnvironmentKey
    ? environments.filter(e => (e.key || `id:${e.id}`) === globalEnvironmentKey)
    : environments;
  // An explicit object target must never change when a viewing scope changes.
  const environment = fixedScope || environmentId
    ? environments.find(e => e.id === environmentId) ?? null
    : globalEnvironmentKey
      ? scopedEnvironments.length === 1 ? scopedEnvironments[0] : null
      : environments.find(e => e.id === environmentId) ?? (environments.length === 1 ? environments[0] : null);
  const setScope = useCallback(
    (app: string, env: string) => {
      if (fixedScope) return;
      const next = new URLSearchParams(location.search);
      for (const key of [
        "application",
        "environment",
        "applications",
        "environments",
        "scope",
      ])
        next.delete(key);
      if (app) next.set("application", app);
      if (env) next.set("environment", env);
      if (preview) next.set("preview", "1");
      navigate({
        pathname: location.pathname,
        search: next.toString(),
        hash: location.hash,
      });
    },
    [
      fixedScope,
      location.pathname,
      location.search,
      location.hash,
      navigate,
      preview,
    ],
  );
  return {
    application,
    environments,
    environment,
    setScope,
    setApplication: (id: string) => setScope(id, ""),
    setEnvironment: (id: string) => setScope(applicationId, id),
  };
}
