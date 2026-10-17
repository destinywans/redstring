import { useMemo } from "react";
import { useGet } from "../api/client";
import { serviceApplicationId } from "../api/ids";
import type { ServiceCatalog } from "../api/types";
import { useEnv } from "../app/environment";

/*
 * Names for the opaque IDs records carry (PA-111, PA-166). Each lookup gives null when it can't resolve the ID, so the
 * caller keeps the original value and no name is invented.
 */

/** Catalog services (by catalog ID or by application ID) and the instance's environments. */
export function useNames() {
  const { data: svc } = useGet<ServiceCatalog>("/services", undefined, { staleTime: 5 * 60_000 });
  const environments = useEnv((s) => s.instance?.environments);
  return useMemo(() => {
    const byService = new Map<string, string>();
    const byApplication = new Map<string, string>();
    for (const s of svc?.items ?? []) {
      byService.set(s.id, s.name);
      // An application has the same name in every environment; the first one listed is as good as any.
      const app = serviceApplicationId(s.id);
      if (app && !byApplication.has(app)) byApplication.set(app, s.name);
    }
    return {
      /** A catalog service ID or an application ID → the service name. */
      service: (id: string | null | undefined) => (id ? (byService.get(id) ?? byApplication.get(serviceApplicationId(id) ?? id) ?? null) : null),
      /** An environment ID → the instance's label for it. */
      env: (id: string | null | undefined) => (id ? (environments?.find((e) => e.id === id)?.label ?? null) : null),
    };
  }, [svc, environments]);
}
