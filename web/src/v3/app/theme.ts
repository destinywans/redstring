import { useEffect } from "react";
import { usePreferences } from "./preferences";

/** Apply theme, language and motion preferences to the document root. */
export function useApplyPreferences() {
  const { theme, locale, reduceMotion, density } = usePreferences();
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    let timer: ReturnType<typeof setTimeout> | undefined;
    const apply = () => {
      const next = theme === "system" ? (media.matches ? "dark" : "light") : theme;
      const root = document.documentElement;
      if (root.dataset.v3Theme && root.dataset.v3Theme !== next) {
        root.dataset.v3ThemeChanging = "";
        clearTimeout(timer);
        timer = setTimeout(() => delete root.dataset.v3ThemeChanging, 320);
      }
      root.dataset.v3Theme = next;
      document.querySelector('meta[name="theme-color"]')?.setAttribute("content", next === "dark" ? "#0E0E10" : "#FFFFFF");
    };
    apply();
    media.addEventListener("change", apply);
    return () => {
      media.removeEventListener("change", apply);
      clearTimeout(timer);
    };
  }, [theme]);
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
  useEffect(() => {
    document.documentElement.toggleAttribute("data-v3-reduce-motion", reduceMotion);
    document.documentElement.dataset.v3Density = density;
  }, [reduceMotion, density]);
}
