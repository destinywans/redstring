import { useMemo, type ReactNode } from "react";
import { ApiError } from "../../api/client";
import type { TraceList } from "../../api/types";
import { Card, Icon, Row } from "../../ui/core";
import { useI18n } from "../../i18n";

/**
 * PA-78: the trace list when the bounded search hits its budget (approved design SbzsTUZuG5KbaWzHN7kTLj).
 * The search goes newest-first and stops after 40 traces or 64 searches, so only [window_start_at, now] of
 * the chosen range is covered; a 413 means even the newest second holds more than a bounded search can read.
 */
export type CoveredWindow = { start: number; end: number; seconds: number };

/** Reads the provisional plan-a fields in one place; null unless the list stopped at the search budget. */
export function coveredWindow(d: TraceList | undefined, fetchedAt: number): CoveredWindow | null {
  if (!d?.partial || d.partial_reason !== "search_budget" || !d.window_start_at) return null;
  const start = Date.parse(d.window_start_at);
  if (!Number.isFinite(start)) return null;
  const end = Math.max(start, fetchedAt || Date.now());
  return { start, end, seconds: (end - start) / 1000 };
}

export const isScopeLimit = (e: unknown) => e instanceof ApiError && e.status === 413 && e.code === "trace_scope_limit";

/** Adds `status = error` to a TraceQL query; with `||` it is added to each branch, since && binds tighter. */
export function withErrors(q: string) {
  const t = q.trim();
  if (/\bstatus\s*=\s*error\b/.test(t)) return t;
  const inner = t.replace(/^\{\s*/, "").replace(/\s*\}$/, "").trim();
  if (!inner) return "{ status = error }";
  return `{ ${inner.split("||").map((x) => `${x.trim()} && status = error`).join(" || ")} }`;
}

export function useWindowText() {
  const { tx, locale, fmt } = useI18n();
  return useMemo(() => {
    const clock = new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
    const span = (seconds: number) => {
      if (seconds < 90) return tx("{n} 秒", "{n} seconds", { n: Math.max(1, Math.round(seconds)) });
      const m = Math.round(seconds / 60);
      if (m < 90) return m === 1 ? tx("1 分钟", "1 minute") : tx("{n} 分钟", "{n} minutes", { n: m });
      const h = Math.floor(m / 60);
      return m % 60 ? tx("{h} 小时 {m} 分钟", "{h} h {m} min", { h, m: m % 60 }) : tx("{h} 小时", "{h} hours", { h });
    };
    const ranges: Record<string, string> = {
      "15m": tx("15 分钟", "15-minute"),
      "30m": tx("30 分钟", "30-minute"),
      "1h": tx("1 小时", "1-hour"),
      "6h": tx("6 小时", "6-hour"),
      "24h": tx("24 小时", "24-hour"),
      "7d": tx("7 天", "7-day"),
      "30d": tx("30 天", "30-day"),
    };
    /** Five evenly spaced labels from where the search stopped to now. */
    const ticks = (w: CoveredWindow) =>
      Array.from({ length: 5 }, (_, i) => {
        const at = w.start + ((w.end - w.start) * i) / 4;
        const iso = new Date(at).toISOString();
        return w.seconds < 3600 ? clock.format(at) : w.seconds < 86400 ? fmt.time(iso) : fmt.dateTime(iso);
      });
    return { clock: (ms: number) => clock.format(ms), span, range: (r: string) => ranges[r] ?? r, ticks };
  }, [tx, locale, fmt]);
}

/** Under the charts: the axis spans only what was covered. */
export function useAxisNote(w: CoveredWindow | null, range: string) {
  const { tx } = useI18n();
  const wt = useWindowText();
  if (!w) return null;
  return tx("横轴：实际覆盖的 {w}（所选 {r}）", "X axis: the {w} covered (of the selected {r} range)", { w: wt.span(w.seconds), r: wt.range(range) });
}

/** Above the list: how much was covered, where the search stopped, and how to narrow it. */
export function BudgetNotice({ count, w, range, actions }: { count: number; w: CoveredWindow; range: string; actions: ReactNode }) {
  const { tx } = useI18n();
  const wt = useWindowText();
  const [pre, post = ""] = tx(
    "只显示了最近 {b}的 {n} 条链路：搜索在 {t} 停下，达到了单次搜索的上限，所选的 {r}没有全部覆盖。",
    "Showing {n} traces from the last {b} only: the search stopped at {t} when it reached its limit, so the selected {r} range isn't fully covered.",
    { n: count, t: wt.clock(w.start), r: wt.range(range) },
  ).split("{b}");
  return (
    <div className="tr-limit" role="status">
      <Icon name="info" size={14} />
      <div className="grow">
        <span>
          {pre}
          <b>{wt.span(w.seconds)}</b>
          {post}
        </span>
        <span className="muted">{tx("想看更早的链路，请缩小范围：加上服务或 TraceQL 条件，或者选更短的时间范围。", "To see earlier traces, narrow it down: add a service or a TraceQL condition, or choose a shorter range.")}</span>
        {actions && <Row gap={6} wrap>{actions}</Row>}
      </div>
    </div>
  );
}

/** Replaces the list on 413: why nothing can be listed and how to narrow the query. */
export function DenseCard({ serviceExample, actions }: { serviceExample: string | null; actions: ReactNode }) {
  const { tx } = useI18n();
  return (
    <Card>
      <div className="tr-dense" role="alert">
        <span className="tr-dense-ico">
          <Icon name="alert" size={16} />
        </span>
        <b>{tx("这段时间的链路太密，没法列出", "Too many traces in this window to list")}</b>
        <p>{tx("最近 1 秒内就有 20 条以上链路，有界搜索覆盖不了。缩小范围后再搜索：", "More than 20 traces arrived within one second, more than a bounded search can cover. Narrow it down and search again:")}</p>
        <ul>
          {serviceExample && (
            <li>
              {tx("选一个服务，或者在 TraceQL 里加", "pick one service, or add")} <code>resource.service.name = "{serviceExample}"</code>
            </li>
          )}
          <li>
            {tx("只看错误：", "only errors:")} <code>status = error</code>
            {tx("；或只看慢的：", "; or only slow ones:")} <code>duration &gt; 1s</code>
          </li>
          <li>{tx("选更短的时间范围（最短 15 分钟）", "choose a shorter range (down to 15 minutes)")}</li>
        </ul>
        {actions && <Row gap={6} wrap>{actions}</Row>}
      </div>
    </Card>
  );
}
