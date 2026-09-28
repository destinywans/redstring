import { create } from "zustand";
import type { InstanceInfo } from "../api/types";

const KEY = "aiops-v3-env";

function initialEnv(): string {
  try {
    const fromUrl = new URLSearchParams(location.search).get("env");
    return fromUrl || localStorage.getItem(KEY) || "";
  } catch {
    return "";
  }
}

type EnvState = {
  instance: InstanceInfo | null;
  /** Current environment id; "all" = every non-production environment, view only. */
  env: string;
  setInstance: (info: InstanceInfo) => void;
  setEnv: (id: string) => void;
};

/**
 * Global environment: everything on screen, the AI's evidence scope and the executor target follow it.
 * A production instance has exactly one environment and no switcher.
 */
export const useEnv = create<EnvState>((set, get) => ({
  instance: null,
  env: initialEnv(),
  setInstance: (info) => {
    const cur = get().env;
    const valid = cur === "all" ? info.tier === "nonprod" : info.environments.some((e) => e.id === cur);
    const env = valid ? cur : info.default_env;
    set({ instance: info, env });
    try {
      // A shared ?env= link switches this viewer too, so later navigation stays in that environment.
      localStorage.setItem(KEY, env);
    } catch {
      /* per-viewer convenience only */
    }
  },
  setEnv: (id) => {
    set({ env: id });
    try {
      localStorage.setItem(KEY, id);
      // Keep the environment in the address so a shared link opens in the same environment.
      const url = new URL(location.href);
      url.searchParams.set("env", id);
      history.replaceState(history.state, "", url);
    } catch {
      /* per-viewer convenience only */
    }
  },
}));

export const currentEnv = () => useEnv.getState().env;
