import { useEffect, useMemo, useState } from "react";
import { useI18n } from "../i18n";

/** RFC3339 instants (the backend sends up to nanoseconds). */
const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:\d{2})$/;
/** The same instants inside display text (e.g. "Checked 2026-10-10T02:21:22Z"). */
const ISO_IN_TEXT = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:\d{2})/g;

/** Epoch milliseconds of a backend timestamp; null for anything else (the demo's preformatted text, "unknown"). */
export function parseStamp(v: string | number | null | undefined): number | null {
  if (typeof v === "number") return Number.isFinite(v) ? v : null;
  if (!v) return null;
  if (!ISO.test(v)) return null;
  // JS dates keep milliseconds; longer fractions don't parse everywhere.
  const t = Date.parse(v.replace(/(\.\d{3})\d+/, "$1"));
  return Number.isFinite(t) ? t : null;
}

/**
 * The current time, refreshed every `ms` (half a minute by default), for anything a page shows relative to now: a vote
 * that lapses, a silence that ends, today and yesterday. Without it such text stays as it was at render (PA-263).
 */
export function useNow(ms = 30_000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

/** A present value always formats to text; only null/undefined give null. */
type Format = { (v: string | number): string; (v: string | number | null | undefined): string | null };

/**
 * Times in the app's language (not the browser's) and the viewer's time zone (PA-74, PA-82). Every helper takes a
 * backend timestamp (or epoch ms) and returns null for null/undefined; anything that isn't a timestamp — demo text,
 * "unknown" — comes back unchanged.
 */
export function useWhen() {
  const { locale } = useI18n();
  return useMemo(() => whenFormats(locale), [locale]);
}

/** The formatters behind useWhen for one locale. */
export function whenFormats(locale: string) {
  const f = (o: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat(locale, { hourCycle: "h23", ...o });
  const dayMinute = f({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  const daySecond = f({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const minute = f({ hour: "2-digit", minute: "2-digit" });
  const second = f({ hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const milli = f({ hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 });
  const day = f({ month: "short", day: "numeric" });
  const utcDay = f({ month: "short", day: "numeric", timeZone: "UTC" });
  const dayYear = f({ year: "numeric", month: "short", day: "numeric" });
  const sameDay = (t: number) => new Date(t).toDateString() === new Date().toDateString();
  const sameYear = (t: number) => new Date(t).getFullYear() === new Date().getFullYear();
  const fmt = (fn: (t: number) => string) =>
    ((v: string | number | null | undefined) => {
      if (v === null || v === undefined) return null;
      if (v === "") return "";
      const t = parseStamp(v);
      return t === null ? String(v) : fn(t);
    }) as Format;
  return {
    /** Date and minute: "10月10日 10:21". */
    at: fmt((t) => dayMinute.format(t)),
    /** Date and second, for audit-style records. */
    stamp: fmt((t) => daySecond.format(t)),
    /** Minute clock, e.g. chart axis labels. */
    time: fmt((t) => minute.format(t)),
    /** Seconds clock for ledger steps. */
    clock: fmt((t) => second.format(t)),
    /** Minute clock today, date and minute otherwise. */
    short: fmt((t) => (sameDay(t) ? minute.format(t) : dayMinute.format(t))),
    /** Trace and log rows: milliseconds, with the date when it isn't today. */
    precise: fmt((t) => (sameDay(t) ? milli.format(t) : `${day.format(t)} ${milli.format(t)}`)),
    /** Date only (with the year when it isn't this year). */
    date: fmt((t) => (sameYear(t) ? day.format(t) : dayYear.format(t))),
    /** Seconds clock today, date and second otherwise (delivery logs). */
    log: fmt((t) => (sameDay(t) ? second.format(t) : daySecond.format(t))),
    /** The UTC calendar day a bucket stands for (buckets cut at UTC midnight), in the app's language. */
    utcDate: fmt((t) => utcDay.format(t)),
    /** Axis labels from instants (PA-83): clock times within a day, date and time over a few days, dates beyond. */
    axis: (times: readonly (string | null)[] | null | undefined): string[] => {
      const ts = (times ?? []).map((v) => parseStamp(v));
      if (!ts.length || ts.some((t) => t === null)) return [];
      const span = (ts[ts.length - 1] as number) - (ts[0] as number);
      const f = span < 22 * 3600_000 ? minute : span < 6 * 86400_000 ? dayMinute : day;
      return ts.map((t) => f.format(t as number));
    },
    /** Display text with timestamps inside it, each shown as date and minute. */
    text: (v: string | null | undefined) =>
      v ? v.replace(ISO_IN_TEXT, (m) => { const t = parseStamp(m); return t === null ? m : dayMinute.format(t); }) : (v ?? null),
  };
}

/** At most `max` labels, evenly picked (first and last kept), for a label row laid out with space-between. */
export function thinLabels(labels: readonly string[], max: number): string[] {
  if (labels.length <= max || max < 2) return [...labels];
  return Array.from({ length: max }, (_, i) => labels[Math.round((i * (labels.length - 1)) / (max - 1))]);
}
