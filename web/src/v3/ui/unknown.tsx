import { useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { Btn, Icon, Tile, type IconName } from "./core";
import { ApiError, useGet } from "../api/client";
import type { Me } from "../api/types";
import { useI18n } from "../i18n";
import "./unknown.css";

/**
 * Unknown / not-connected states (design approved 2026-10-09). The backend returns null plus a
 * reason when it has no trustworthy evidence; the UI shows that honestly and never renders an
 * unknown as 0, "healthy" or "not on call".
 */
export type UnknownReason = "not_connected" | "not_configured" | "no_authorized_target" | "not_generated" | "unavailable" | "stale" | (string & {});

export function useReasonText() {
  const { tx } = useI18n();
  return (reason: UnknownReason | null | undefined, source?: string) => {
    switch (reason) {
      case "not_connected":
      case "not_configured":
        return {
          label: tx("未接入", "Not connected"),
          desc: source ? tx("还没有连接{s}。", "{s} is not connected yet.", { s: source }) : tx("还没有连接这类数据来源。", "This data source is not connected yet."),
          configurable: true,
        };
      case "no_authorized_target":
        return { label: tx("暂无可见对象", "Nothing visible"), desc: tx("当前环境里没有你有权查看的对象。", "There is nothing you can view in this environment."), configurable: false };
      case "not_generated":
        return { label: tx("尚未生成", "Not generated yet"), desc: tx("有新数据后会自动生成。", "It is generated when new data arrives."), configurable: false };
      case "unavailable":
        return { label: tx("暂时不可用", "Temporarily unavailable"), desc: tx("来源连接失败或超时，稍后会自动重试。", "The source failed or timed out; it is retried automatically."), configurable: false };
      case "search_unattested":
        return { label: tx("未声明搜索完整", "Search not declared complete"), desc: tx("这个部署没有声明 Tempo 搜索完整，无法给出分布和总数。", "This deployment doesn't declare Tempo search as complete, so no distributions or totals can be given."), configurable: false };
      case "incomplete_traces":
        return { label: tx("跳过了不完整的链路", "Incomplete traces skipped"), desc: tx("有链路缺少部分 Span 被跳过，这次不给出总数、分面和分布。", "Traces missing spans were skipped, so no totals, facets or charts this time."), configurable: false };
      case "stale":
        return { label: tx("数据已过期", "Out of date"), desc: tx("最近一次成功采集已过期。", "The last successful collection is out of date."), configurable: false };
      default:
        return { label: tx("暂无数据", "No data"), desc: tx("后端没有给出这项数据。", "The server returned no value for this."), configurable: false };
    }
  };
}

function useIsAdmin() {
  const { data } = useGet<Me>("/me", undefined, { staleTime: 5 * 60_000 });
  return !!data?.roles.includes("admin");
}

/** The action under an unknown: admins can go and configure; everyone else is told who can. */
function ConfigureAction({ to = "/integrations", compact }: { to?: string; compact?: boolean }) {
  const { tx } = useI18n();
  const admin = useIsAdmin();
  if (!admin) return <span className="unk-who">{tx("请管理员接入", "Ask an admin to connect it")}</span>;
  return compact ? (
    <Link className="unk-link" to={to}>
      {tx("去集成中心配置", "Configure in Integrations")}
    </Link>
  ) : (
    <Btn h={26} to={to}>
      {tx("去配置", "Configure")}
    </Btn>
  );
}

/** Inline unknown value: a faint dash; hover or focus explains what is missing and how to fix it. */
export function UnknownValue({ reason, source, to, label }: { reason?: UnknownReason | null; source?: string; to?: string; label?: ReactNode }) {
  const text = useReasonText()(reason, source);
  const [rect, setRect] = useState<DOMRect | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const show = (el: HTMLElement) => {
    window.clearTimeout(timer.current);
    setRect(el.getBoundingClientRect());
  };
  const hide = () => {
    timer.current = window.setTimeout(() => setRect(null), 120);
  };
  return (
    <span
      className="unk"
      tabIndex={0}
      role="note"
      aria-label={`${text.label}. ${text.desc}`}
      onMouseEnter={(e) => show(e.currentTarget)}
      onMouseLeave={hide}
      onFocus={(e) => show(e.currentTarget)}
      onBlur={hide}
    >
      {label ?? "—"}
      {rect &&
        createPortal(
          <span
            className="unk-card"
            style={{ left: Math.max(8, Math.min(rect.left - 8, window.innerWidth - 260)), top: rect.bottom + 6 }}
            onMouseEnter={() => window.clearTimeout(timer.current)}
            onMouseLeave={hide}
          >
            <b>
              {source ? `${source}：` : ""}
              {text.label}
            </b>
            <span>{text.desc}</span>
            {text.configurable && <ConfigureAction to={to} compact />}
          </span>,
          document.body,
        )}
    </span>
  );
}

/** A whole card without a source: say what is missing; admins get a configure button. */
export function Unavailable({ reason, source, title, desc, icon = "plug", to }: { reason?: UnknownReason | null; source?: string; title?: ReactNode; desc?: ReactNode; icon?: IconName; to?: string }) {
  const text = useReasonText()(reason, source);
  return (
    <div className="unk-block">
      <Tile icon={icon} size={30} />
      <b>{title ?? (source && text.configurable ? `${text.label}${source}` : text.label)}</b>
      <p>{desc ?? text.desc}</p>
      {text.configurable && <ConfigureAction to={to} />}
    </div>
  );
}

/** One line above a list when some columns have no data. */
export function PartialBanner({ fields, children }: { fields: string[]; children?: ReactNode }) {
  const { tx } = useI18n();
  // A banner with its own text has nothing to list (TracesPage, RoutingPage).
  if (!fields.length && !children) return null;
  return (
    <div className="unk-banner" role="status">
      <Icon name="info" size={14} />
      <span className="grow">
        {children ?? tx("部分数据暂缺：{f}。悬停“—”查看原因。", "Some data is missing: {f}. Hover a “—” for the reason.", { f: fields.join(tx("、", ", ")) })}
      </span>
    </div>
  );
}

/** A page whose only source is not connected. `configure={false}` drops the integrations hint when the fix lies elsewhere. */
export function PageUnavailable({ title, desc, steps, to = "/integrations", icon = "plug", configure = true, detail }: { title: ReactNode; desc: ReactNode; steps?: ReactNode[]; to?: string; icon?: IconName; configure?: boolean; detail?: ReactNode }) {
  const { tx } = useI18n();
  const admin = useIsAdmin();
  return (
    <div className="unk-page">
      <Tile icon={icon} size={36} />
      <b>{title}</b>
      <p>{desc}</p>
      {detail && <code className="unk-detail">{detail}</code>}
      {steps && steps.length > 0 && (
        <ol>
          {steps.map((s, i) => (
            <li key={i}>{s}</li>
          ))}
        </ol>
      )}
      {!configure ? null : admin ? (
        <Btn kind="primary" h={30} to={to}>
          {tx("去集成中心", "Open Integrations")}
        </Btn>
      ) : (
        <span className="unk-who">{tx("请管理员接入后再来查看。", "Ask an admin to connect it, then come back.")}</span>
      )}
    </div>
  );
}

/** Reasons arrive keyed by field; returns the reason for one field or a fallback. */
export const reasonFor = (map: Record<string, string> | null | undefined, field: string, fallback?: UnknownReason): UnknownReason | undefined => map?.[field] ?? fallback;

/** The first reason among several field paths of a V3 `unavailable` map (e.g. `slo.target`, `slo.budget_left`). */
export const anyReason = (map: Record<string, string> | null | undefined, ...paths: string[]): UnknownReason | undefined => paths.map((p) => map?.[p]).find(Boolean);

/**
 * Labels the unknown paths of one or more `unavailable` maps for a PartialBanner, de-duplicated.
 * `labels` maps a path (or its first segment) to user-facing text; unmapped paths are skipped.
 */
export function unknownLabels(maps: (Record<string, string> | null | undefined)[], labels: Record<string, string>) {
  const out = new Set<string>();
  for (const m of maps)
    for (const path of Object.keys(m ?? {})) {
      const label = labels[path] ?? labels[path.split(/[.[]/)[0]];
      if (label) out.add(label);
    }
  return [...out];
}

/** Maps a failed read to an unknown reason: 501 and "not configured" mean not connected; other 5xx are unavailable. */
export function errorReason(error: unknown): UnknownReason {
  if (!(error instanceof ApiError)) return "unavailable";
  if (error.status === 501 || /not_configured|not_connected/.test(error.code)) return "not_connected";
  return "unavailable";
}

/**
 * A page whose main read failed: says why instead of an empty table or an endless skeleton.
 * Admins also see the server's reason code, which is what they need to fix the source.
 */
export function LoadError({ error, source, title, icon }: { error: unknown; source?: string; title?: ReactNode; icon?: IconName }) {
  const { tx } = useI18n();
  const admin = useIsAdmin();
  const reasonText = useReasonText();
  if (error instanceof ApiError && error.status === 403)
    return <PageUnavailable icon="lock" configure={false} title={tx("没有权限查看", "You can't view this")} desc={tx("需要管理员在当前环境授予查看权限。", "An admin needs to grant view access in this environment.")} />;
  // A missing record is not a source failure and retrying won't bring it back (PA-125).
  if (error instanceof ApiError && error.status === 404)
    return <PageUnavailable icon="search" configure={false} title={tx("找不到这条记录", "Not found")} desc={tx("它可能不存在，或在当前环境里看不到。", "It may not exist, or isn't visible in this environment.")} detail={admin ? `${error.status} ${error.code}` : undefined} />;
  const reason = errorReason(error);
  const text = reasonText(reason, source);
  return (
    <PageUnavailable
      icon={icon ?? (reason === "not_connected" ? "plug" : "alert")}
      configure={text.configurable}
      title={title ?? (source && text.configurable ? `${text.label}${source}` : text.label)}
      desc={text.desc}
      detail={admin && error instanceof ApiError ? `${error.status} ${error.code}` : undefined}
    />
  );
}

/** Suffixes of `<prefix>.<id>` keys in an `unavailable` map, e.g. the missing panels "signals.p99" → ["p99"]. */
export const missingParts = (map: Record<string, string> | null | undefined, prefix: string) =>
  Object.keys(map ?? {})
    .filter((k) => k.startsWith(prefix + "."))
    .map((k) => k.slice(prefix.length + 1));
