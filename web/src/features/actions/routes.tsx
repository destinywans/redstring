import { createContext, useContext } from "react";
import { useLocation, useSearchParams, type To } from "react-router-dom";

/**
 * Where approval and recovery live. Standalone they are pages; inside the
 * investigation canvas they are side panels addressed by `?panel=&plan=` on
 * the canvas URL, so links keep the operator on the canvas.
 */
export type ActionRoutes = {
  embedded: boolean;
  params: URLSearchParams;
  setParams: (next: Record<string, string>) => void;
  approval: (plan: string, inv: string) => To;
  recovery: (plan: string, inv: string) => To;
  investigation: (inv?: string) => To;
};

const EmbeddedInvestigation = createContext<string | null>(null);
export const ActionPanelProvider = EmbeddedInvestigation.Provider;

export function useActionRoutes(): ActionRoutes {
  const embeddedIn = useContext(EmbeddedInvestigation);
  const [search, setSearch] = useSearchParams();
  const { pathname } = useLocation();
  if (!embeddedIn)
    return {
      embedded: false,
      params: search,
      setParams: (next) => setSearch(next),
      approval: (plan, inv) =>
        `/actions?plan=${encodeURIComponent(plan)}&inv=${encodeURIComponent(inv)}`,
      recovery: (plan, inv) =>
        `/recovery?plan=${encodeURIComponent(plan)}&inv=${encodeURIComponent(inv)}`,
      investigation: (inv) =>
        inv ? `/investigations/${encodeURIComponent(inv)}` : "/investigations",
    };
  const panel = (values: Record<string, string>): To => {
    const next = new URLSearchParams(search);
    for (const key of ["panel", "plan", "execution"]) next.delete(key);
    for (const [key, value] of Object.entries(values))
      if (value) next.set(key, value);
    return { pathname, search: `?${next}` };
  };
  const params = new URLSearchParams();
  params.set("inv", embeddedIn);
  const plan = search.get("plan");
  if (plan) params.set("plan", plan);
  return {
    embedded: true,
    params,
    setParams: (next) =>
      setSearch((current) => {
        const updated = new URLSearchParams(current);
        if (next.plan) updated.set("plan", next.plan);
        else updated.delete("plan");
        return updated;
      }),
    approval: (planId) => panel({ panel: "approval", plan: planId }),
    recovery: (planId) => panel({ panel: "recovery", plan: planId }),
    investigation: () => panel({}),
  };
}
