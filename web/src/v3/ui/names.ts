import { useMemo } from "react";
import { API_MODE, useGet } from "../api/client";
import { serviceApplicationId } from "../api/ids";
import type { Me, MembersView, ServiceCatalog } from "../api/types";
import { useEnv } from "../app/environment";
import { usePermission } from "./permission";

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

/**
 * The member directory. It needs members.view: roles without it get no list instead of a 403 (PA-126), and real mode
 * waits for /me so the read isn't sent before the grants are known. `hidden`: the directory can't be read here.
 */
export function useMembers() {
  const { data: me, error: meError } = useGet<Me>("/me");
  const need = usePermission()(["members", "view"]);
  const known = API_MODE === "mock" || !!me || !!meError;
  const readable = known && need === null;
  const { data, error } = useGet<MembersView>(readable ? "/members" : null, { filter: "all" }, { staleTime: 5 * 60_000 });
  return { me, items: data?.items, hidden: (known && !readable) || !!error };
}

/** User ID → member name; the viewer's own ID resolves without the directory. `hidden` as in useMembers. */
export function useMemberNames() {
  const { me, items, hidden } = useMembers();
  return useMemo(() => {
    const people = new Map((items ?? []).map((m) => [m.id, m.name]));
    if (me) people.set(me.id, me.name);
    return { member: (id: string | null | undefined) => (id ? (people.get(id) ?? null) : null), hidden };
  }, [me, items, hidden]);
}
