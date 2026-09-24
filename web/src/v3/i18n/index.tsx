import { useCallback, useMemo } from "react";
import { usePreferences, type Locale } from "../app/preferences";

/** A bilingual literal. Page copy is written inline as `tx(zh, en)` so text stays next to its layout. */
export type L = { zh: string; en: string };
export const l = (zh: string, en: string): L => ({ zh, en });

export function pick(locale: Locale, value: L | string): string {
  if (typeof value === "string") return value;
  return locale === "zh-CN" ? value.zh : value.en;
}

function interpolate(text: string, vars?: Record<string, string | number>) {
  if (!vars) return text;
  return text.replace(/\{(\w+)\}/g, (_, k) => String(vars[k] ?? `{${k}}`));
}

export function useI18n() {
  const locale = usePreferences((s) => s.locale);
  const zh = locale === "zh-CN";
  const tx = useCallback(
    (zhText: string, enText: string, vars?: Record<string, string | number>) =>
      interpolate(zh ? zhText : enText, vars),
    [zh],
  );
  const tl = useCallback((value: L | string) => pick(locale, value), [locale]);
  const fmt = useMemo(() => {
    const num = new Intl.NumberFormat(locale);
    const pct = new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 1 });
    const time = new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", hour12: false });
    const date = new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" });
    const dateTime = new Intl.DateTimeFormat(locale, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    });
    const rel = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
    return {
      num: (v: number) => num.format(v),
      pct: (v: number) => pct.format(v),
      time: (iso: string) => time.format(new Date(iso)),
      date: (iso: string) => date.format(new Date(iso)),
      dateTime: (iso: string) => dateTime.format(new Date(iso)),
      ago: (iso: string) => {
        const diff = (new Date(iso).getTime() - Date.now()) / 1000;
        const abs = Math.abs(diff);
        if (abs < 60) return rel.format(Math.round(diff), "second");
        if (abs < 3600) return rel.format(Math.round(diff / 60), "minute");
        if (abs < 86400) return rel.format(Math.round(diff / 3600), "hour");
        return rel.format(Math.round(diff / 86400), "day");
      },
      duration: (seconds: number) => {
        if (seconds < 60) return zh ? `${Math.round(seconds)} 秒` : `${Math.round(seconds)}s`;
        const m = Math.floor(seconds / 60);
        if (m < 60) return zh ? `${m} 分钟` : `${m}m`;
        const h = Math.floor(m / 60);
        return zh ? `${h} 小时 ${m % 60} 分` : `${h}h ${m % 60}m`;
      },
    };
  }, [locale, zh]);
  return { locale, zh, tx, tl, fmt };
}
