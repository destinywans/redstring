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

/**
 * A page that embeds Redstring can ask for its look in the address (`?theme=dark&lang=en`). The asked-for
 * values apply to this visit only: nothing is saved until the person changes a preference themselves.
 */
function asked(): { theme?: ThemePreference; locale?: Locale } {
  const query = new URLSearchParams(location.search);
  const theme = query.get("theme");
  const lang = query.get("lang");
  return {
    theme: theme === "light" || theme === "dark" || theme === "system" ? theme : undefined,
    locale: lang === "zh" ? "zh-CN" : lang === "en" ? "en-US" : undefined,
  };
}

function initialLocale(): Locale {
  const saved = load().locale;
  if (saved === "zh-CN" || saved === "en-US") return saved;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en-US";
}

/** Saves what the person changed on top of what was saved, so values asked for by an embedding page stay unsaved. */
function persist(state: Partial<Preferences>) {
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
export const usePreferences = create<Preferences>((set) => {
  const saved = load();
  const address = asked();
  const update = (patch: Partial<Preferences>) => {
    set(patch);
    persist({ ...load(), ...patch });
  };
  return {
    theme: address.theme ?? saved.theme ?? "system",
    locale: address.locale ?? initialLocale(),
    density: saved.density ?? "comfortable",
    reduceMotion: saved.reduceMotion ?? false,
    setTheme: (theme) => update({ theme }),
    setLocale: (locale) => update({ locale }),
    setDensity: (density) => update({ density }),
    setReduceMotion: (reduceMotion) => update({ reduceMotion }),
  };
});

/**
 * While embedded, the host page can keep Redstring in step with its own theme and language by posting
 * `{ type: "redstring:preferences", theme?, lang? }`. Only the page that directly frames Redstring is
 * listened to, and only when its origin is listed in VITE_EMBED_ORIGINS (comma separated, e.g. the public
 * demo's https://wans.run); without it no message is.
 * Like the address, these apply to this visit and are not saved.
 */
const embedOrigins = (import.meta.env.VITE_EMBED_ORIGINS ?? "")
  .split(",")
  .map((origin) => origin.trim())
  .filter(Boolean);
if (window.parent !== window && embedOrigins.length) {
  window.addEventListener("message", (event) => {
    if (event.source !== window.parent || !embedOrigins.includes(event.origin)) return;
    const data = event.data as { type?: unknown; theme?: unknown; lang?: unknown } | null;
    if (!data || data.type !== "redstring:preferences") return;
    const patch: Partial<Preferences> = {};
    if (data.theme === "light" || data.theme === "dark" || data.theme === "system") patch.theme = data.theme;
    if (data.lang === "zh" || data.lang === "en") patch.locale = data.lang === "zh" ? "zh-CN" : "en-US";
    usePreferences.setState(patch);
  });
}

/** Current locale outside React (API client headers, mock resolution). */
export const currentLocale = () => usePreferences.getState().locale;
