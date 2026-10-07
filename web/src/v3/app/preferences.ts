import { create } from "zustand";

export type ThemePreference = "light" | "dark" | "system";
export type Locale = "zh-CN" | "en-US";
export type Density = "comfortable" | "compact";

type Preferences = {
  theme: ThemePreference;
  locale: Locale;
  density: Density;
  reduceMotion: boolean;
  setTheme: (theme: ThemePreference) => void;
  setLocale: (locale: Locale) => void;
  setDensity: (density: Density) => void;
  setReduceMotion: (value: boolean) => void;
};

const KEY = "aiops-v3-preferences";

function load(): Partial<Preferences> {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "{}");
    // Stored data is per-viewer and may be corrupt; anything but an object means "no saved preferences".
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}

function initialLocale(): Locale {
  const saved = load().locale;
  if (saved === "zh-CN" || saved === "en-US") return saved;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en-US";
}

function persist(state: Preferences) {
  try {
    localStorage.setItem(
      KEY,
      JSON.stringify({
        theme: state.theme,
        locale: state.locale,
        density: state.density,
        reduceMotion: state.reduceMotion,
      }),
    );
  } catch {
    /* Preferences stay in memory when storage is unavailable. */
  }
}

/** Per-user interface preferences. Stored locally now; `PATCH /me/preferences` syncs them once the backend exists. */
export const usePreferences = create<Preferences>((set, get) => {
  const saved = load();
  const update = (patch: Partial<Preferences>) => {
    set(patch);
    persist(get());
  };
  return {
    theme: saved.theme ?? "system",
    locale: initialLocale(),
    density: saved.density ?? "comfortable",
    reduceMotion: saved.reduceMotion ?? false,
    setTheme: (theme) => update({ theme }),
    setLocale: (locale) => update({ locale }),
    setDensity: (density) => update({ density }),
    setReduceMotion: (reduceMotion) => update({ reduceMotion }),
  };
});

/** Current locale outside React (API client headers, mock resolution). */
export const currentLocale = () => usePreferences.getState().locale;
