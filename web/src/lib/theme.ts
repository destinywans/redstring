import { useEffect, useState } from "react";

type Theme = "dark" | "light";
type Preference = Theme | "system";
const key = "aiops-theme";
function readPreference(): Preference {
  try {
    const value = localStorage.getItem(key);
    if (value === "light" || value === "dark") return value;
  } catch {
    /* Pre-paint DOM theme remains usable when storage is unavailable. */
  }
  return "system";
}

/** Keep the effective palette, icon, system preference and browser chrome in sync. */
export function useTheme() {
  const [preference, setPreference] = useState<Preference>(readPreference);
  const [theme, setTheme] = useState<Theme>(() =>
    document.documentElement.dataset.theme === "dark" ? "dark" : "light",
  );
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    let transitionTimer: ReturnType<typeof setTimeout> | undefined;
    const apply = () => {
      const next =
        preference === "system"
          ? media.matches
            ? "dark"
            : "light"
          : preference;
      const root = document.documentElement;
      if (root.dataset.theme !== next) {
        clearTimeout(transitionTimer);
        if (!matchMedia("(prefers-reduced-motion: reduce)").matches) {
          root.dataset.themeChanging = "true";
          transitionTimer = setTimeout(() => {
            delete root.dataset.themeChanging;
          }, 850);
        } else {
          delete root.dataset.themeChanging;
        }
      }
      root.dataset.theme = next;
      document
        .querySelector('meta[name="theme-color"]')
        ?.setAttribute("content", next === "dark" ? "#030405" : "#f7f8fa");
      setTheme(next);
    };
    apply();
    media.addEventListener("change", apply);
    return () => {
      media.removeEventListener("change", apply);
      clearTimeout(transitionTimer);
      delete document.documentElement.dataset.themeChanging;
    };
  }, [preference]);
  useEffect(() => {
    const sync = () => setPreference(readPreference());
    const storage = (event: StorageEvent) => {
      if (event.key === key || event.key === null) sync();
    };
    const visible = () => {
      if (document.visibilityState === "visible") sync();
    };
    window.addEventListener("storage", storage);
    window.addEventListener("pageshow", sync);
    document.addEventListener("visibilitychange", visible);
    return () => {
      window.removeEventListener("storage", storage);
      window.removeEventListener("pageshow", sync);
      document.removeEventListener("visibilitychange", visible);
    };
  }, []);
  const toggleTheme = () => {
    const next = theme === "dark" ? "light" : "dark";
    try {
      localStorage.setItem(key, next);
    } catch {
      /* Persisting is optional. */
    }
    setPreference(next);
  };
  return { theme, toggleTheme };
}
