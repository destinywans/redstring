import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import type { Application } from "./types";

/**
 * The URL owns an explicitly addressed selection. Navigation to an unscoped
 * route carries the current selection forward and writes it into that URL.
 * Resolve both IDs against the authorized catalog before exposing them to pages.
 */
export function useWorkspaceSelection(
  applications: Application[],
  ready: boolean,
  preview: boolean,
  fixedScope?: { applicationId: string; environmentId: string },
) {
  const location = useLocation();
  const navigate = useNavigate();
  const params = new URLSearchParams(location.search);
  const [selection, rememberSelection] = useState(() => ({
    applicationId: params.get("application") ?? "",
    environmentId: params.get("environment") ?? "",
  }));
  const { applicationId, environmentId } = selection;
  const setScope = useCallback(
    (applicationId: string, environmentId: string) => {
      rememberSelection((previous) =>
        previous.applicationId === applicationId &&
        previous.environmentId === environmentId
          ? previous
          : { applicationId, environmentId },
      );
    },
    [],
  );
  const setApplication = useCallback(
    (applicationId: string) => setScope(applicationId, ""),
    [setScope],
  );
  const setEnvironment = useCallback(
    (environmentId: string) =>
      rememberSelection((previous) => ({ ...previous, environmentId })),
    [],
  );
  const resolvedLocation = useRef(location.key);
  const navigated = resolvedLocation.current !== location.key;
  const requestedApplication =
    fixedScope?.applicationId ??
    (navigated && params.has("application")
      ? params.get("application")
      : applicationId);
  const requestedEnvironment =
    fixedScope?.environmentId ??
    (navigated && params.has("environment")
      ? params.get("environment")
      : environmentId);
  const application =
    applications.find((a) => a.id === requestedApplication) ??
    (fixedScope
      ? null
      : (applications.find((a) => !a.archived) ?? applications[0] ?? null));
  const environments =
    application?.environments?.filter(
      (e) => !e.archived || e.id === fixedScope?.environmentId,
    ) ?? [];
  const environment =
    environments.find((e) => e.id === requestedEnvironment) ??
    (fixedScope?.environmentId ? null : (environments[0] ?? null));

  useEffect(() => {
    // A failed or unfinished catalog request must not erase a deep-linked ID.
    if (!ready) return;
    const nextApplication = application?.id ?? "";
    const nextEnvironment = environment?.id ?? "";
    resolvedLocation.current = location.key;
    setScope(nextApplication, nextEnvironment);
    const next = new URLSearchParams(location.search);
    if (nextApplication) next.set("application", nextApplication);
    else next.delete("application");
    if (nextEnvironment) next.set("environment", nextEnvironment);
    else next.delete("environment");
    if (preview) next.set("preview", "1");
    if (next.toString() !== new URLSearchParams(location.search).toString()) {
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
    ready,
    application?.id,
    environment?.id,
    applicationId,
    environmentId,
    location.key,
    location.pathname,
    location.search,
    location.hash,
    navigate,
    preview,
    setScope,
  ]);

  return {
    application,
    environments,
    environment,
    setApplication,
    setEnvironment,
    setScope,
  };
}
