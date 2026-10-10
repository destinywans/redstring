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

/** Null unless the list stopped at the search budget; search_unattested keeps the "total missing" banner. */
export function coveredWindow(d: TraceList | undefined, fetchedAt: number): CoveredWindow | null {
  if (!d?.partial || d.partial_reason !== "search_budget" || !d.window_start_at) return null;
  const start = Date.parse(d.window_start_at);
  if (!Number.isFinite(start)) return null;
  const until = d.window_end_at ? Date.parse(d.window_end_at) : NaN;
  const end = Math.max(start, Number.isFinite(until) ? until : fetchedAt || Date.now());
  return { start, end, seconds: (end - start) / 1000 };
}

export const isScopeLimit = (e: unknown) => e instanceof ApiError && e.status === 413 && e.code === "trace_scope_limit";

/** PA-79: 422 range_exceeds_lookback — the range goes back further than the deployment keeps searchable. */
export function lookbackOf(e: unknown): { seconds: number | null; maxRange: string | null } | null {
  if (!(e instanceof ApiError) || e.status !== 422 || e.code !== "range_exceeds_lookback") return null;
  const seconds = typeof e.detail.max_lookback_seconds === "number" && e.detail.max_lookback_seconds > 0 ? e.detail.max_lookback_seconds : null;
  const maxRange = typeof e.detail.max_range === "string" && e.detail.max_range ? e.detail.max_range : null;
  return { seconds, maxRange };
}

const RANGE_SECONDS: Record<string, number> = { m: 60, h: 3600, d: 86400 };
/** Seconds of a range value such as "7d". */
export const rangeSeconds = (r: string) => {
  const m = /^(\d+)([mhd])$/.exec(r);
  return m ? Number(m[1]) * RANGE_SECONDS[m[2]] : null;
};

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
    /** A length as a noun: "7 天" / "7 days". */
    const length = (seconds: number) => {
      if (seconds % 86400 === 0) { const n = seconds / 86400; return n === 1 ? tx("1 天", "1 day") : tx("{n} 天", "{n} days", { n }); }
      if (seconds % 3600 === 0) { const n = seconds / 3600; return n === 1 ? tx("1 小时", "1 hour") : tx("{n} 小时", "{n} hours", { n }); }
      const n = Math.max(1, Math.round(seconds / 60));
      return n === 1 ? tx("1 分钟", "1 minute") : tx("{n} 分钟", "{n} minutes", { n });
    };
    /** Five evenly spaced labels from where the search stopped to now. */
    const ticks = (w: CoveredWindow) =>
      Array.from({ length: 5 }, (_, i) => {
        const at = w.start + ((w.end - w.start) * i) / 4;
        const iso = new Date(at).toISOString();
        return w.seconds < 3600 ? clock.format(at) : w.seconds < 86400 ? fmt.time(iso) : fmt.dateTime(iso);
      });
    return { clock: (ms: number) => clock.format(ms), span, length, range: (r: string) => ranges[r] ?? r, ticks };
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
        <span className="muted">{tx("想看更早的链路，请加上服务或 TraceQL 条件。", "To see earlier traces, add a service or a TraceQL condition.")}</span>
        {actions && <Row gap={6} wrap>{actions}</Row>}
      </div>
    </div>
  );
}

/** Replaces the list on 413: why nothing can be listed and how to narrow the query. The range doesn't matter here — only the newest second's density does. */
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
        </ul>
        {actions && <Row gap={6} wrap>{actions}</Row>}
      </div>
    </Card>
  );
}

/** Replaces the list on 422 range_exceeds_lookback: how far back traces are kept, and a switch to the longest range within it. */
export function LookbackCard({ seconds, maxRange, action }: { seconds: number | null; maxRange: string | null; action: ReactNode }) {
  const { tx } = useI18n();
  const wt = useWindowText();
  const kept = seconds ?? (maxRange ? rangeSeconds(maxRange) : null);
  const within = maxRange ? rangeSeconds(maxRange) : null;
  return (
    <Card>
      <div className="tr-dense" role="alert">
        <span className="tr-dense-ico info">
          <Icon name="clock" size={16} />
        </span>
        <b>{tx("所选范围超出了链路的保留时间", "The selected range goes back further than traces are kept")}</b>
        <p>
          {kept !== null
            ? within !== null
              ? tx("这个部署只保留最近 {d}的链路，请选 {m}以内的范围。", "This deployment keeps only the last {d} of traces; choose a range within {m}.", { d: wt.length(kept), m: wt.length(within) })
              : tx("这个部署只保留最近 {d}的链路，请选更短的范围。", "This deployment keeps only the last {d} of traces; choose a shorter range.", { d: wt.length(kept) })
            : tx("这个部署能搜索的时间比所选范围短，请选更短的范围。", "This deployment can't search back that far; choose a shorter range.")}
        </p>
        {action && <Row gap={6} wrap>{action}</Row>}
      </div>
    </Card>
  );
}
