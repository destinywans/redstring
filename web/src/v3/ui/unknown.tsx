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
export type UnknownReason = "not_connected" | "not_configured" | "no_authorized_target" | "not_generated" | "unavailable" | "stale" | "unmapped_relation" | "not_tracked" | "not_filled" | "unclassified" | "not_in_candidates" | (string & {});

/**
 * English source names arrive as "a log source", "log patterns" or "Prometheus". A heading or a "No … yet" sentence
 * needs the bare noun phrase, capitalised when it starts a heading (PA-193); Chinese names pass through unchanged.
 * `{s}` is the name as given, `{b}` the bare phrase and `{S}` the capitalised one.
 */
const bareSource = (s: string) => s.replace(/^(?:a|an|the)\s+/i, "");
const sourceVars = (s: string) => {
  const b = bareSource(s);
  return { s, b, S: b.charAt(0).toUpperCase() + b.slice(1) };
};

/** `title` is the heading of a block that names its source; without one the block shows `label`. */
type ReasonText = { label: string; title?: string; desc: string; configurable: boolean };

export function useReasonText() {
  const { tx } = useI18n();
  return (reason: UnknownReason | null | undefined, source?: string): ReasonText => {
    const v = source ? sourceVars(source) : undefined;
    switch (reason) {
      case "not_connected":
      case "not_configured":
        return {
          label: tx("未接入", "Not connected"),
          title: v && tx("未接入{s}", "{S} not connected", v),
          desc: v ? tx("还没有连接{s}。", "No {b} connected yet.", v) : tx("还没有连接这类数据来源。", "This data source is not connected yet."),
          configurable: true,
        };
      // A source that is connected but has no query set up for this item (a metrics panel, PA-171).
      case "query_not_configured":
        return {
          label: tx("未配置查询", "No query set up"),
          title: v && tx("{s}未配置查询", "No query set up in {b}", v),
          desc: v ? tx("{s}已接入，但还没有为这一项配置查询。", "{S} is connected, but no query is set up for this item.", v) : tx("还没有为这一项配置查询。", "No query is set up for this item."),
          configurable: true,
        };
      case "no_authorized_target":
        return { label: tx("暂无可见对象", "Nothing visible"), desc: tx("当前环境里没有你有权查看的对象。", "There is nothing you can view in this environment."), configurable: false };
      case "not_generated":
        return { label: tx("尚未生成", "Not generated yet"), desc: tx("有新数据后会自动生成。", "It is generated when new data arrives."), configurable: false };
      case "unavailable":
        return { label: tx("暂时不可用", "Temporarily unavailable"), desc: tx("来源连接失败或超时，刷新页面可再试一次。", "The source failed or timed out; refresh the page to try again."), configurable: false };
      case "search_unattested":
        return { label: tx("未声明搜索完整", "Search not declared complete"), desc: tx("这个部署没有声明 Tempo 搜索完整，无法给出分布和总数。", "This deployment doesn't declare Tempo search as complete, so no distributions or totals can be given."), configurable: false };
      case "incomplete_traces":
        return { label: tx("跳过了不完整的链路", "Incomplete traces skipped"), desc: tx("有链路缺少部分 Span 被跳过，这次不给出总数、分面和分布。", "Traces missing spans were skipped, so no totals, facets or charts this time."), configurable: false };
      case "stale":
        return { label: tx("数据已过期", "Out of date"), desc: tx("最近一次成功采集已过期。", "The last successful collection is out of date."), configurable: false };
      // Recorded relations that don't map to service dependencies: nothing failed and a refresh won't change it (PA-191).
      case "unmapped_relation":
        return {
          label: tx("关系无法表示为依赖", "Relations don't map to dependencies"),
          desc: tx("CMDB 里有关联不能对应到你可见的服务之间的依赖（比如非依赖类关系或共享资源），为免列表不完整，这里不列出。", "Some CMDB relations don't map to dependencies between services you can see (such as non-dependency relations or shared resources), so no list is shown rather than an incomplete one."),
          configurable: false,
        };
      // Front-end reasons for fields no integration can supply: nothing records them yet, or nobody filled them in.
      case "not_tracked":
        return { label: tx("未记录", "Not recorded"), desc: tx("平台目前不记录这项数据。", "The platform doesn't record this yet."), configurable: false };
      case "not_filled":
        return { label: tx("未填写", "Not filled in"), desc: tx("还没有人填写这项信息。", "Nobody has filled this in yet."), configurable: false };
      // A keyword hit whose vector similarity was never computed: it ranked below the vector candidates (PA-222).
      case "not_in_candidates":
        return { label: tx("未进入向量候选", "Not a vector candidate"), desc: tx("这段内容由关键词检索命中，向量相似度没排进候选，所以没有分数，并不是 0。", "Keyword search found this passage; its vector similarity didn't rank among the candidates, so it has no score rather than 0."), configurable: false };
      // A recorded value the server can't classify, such as an audit outcome it has no mapping for (PA-230).
      case "unclassified":
        return { label: tx("无法归类", "Unclassified"), desc: tx("平台还不能归类这个记录值，详情里有原始内容。", "The platform can't classify this recorded value yet; the details show it as recorded."), configurable: false };
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
  const { tx } = useI18n();
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
            <b>{source ? tx("{s}：{l}", "{S}: {l}", { ...sourceVars(source), l: text.label }) : text.label}</b>
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
      <b>{title ?? text.title ?? text.label}</b>
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
  // A page read per environment refuses "all environments"; picking one is the fix, not a retry (PA-221).
  if (error instanceof ApiError && error.status === 422 && error.code === "invalid_environment")
    return <PageUnavailable icon="layers" configure={false} title={tx("需要选择一个环境", "Choose one environment")} desc={tx("这一页按单个环境显示，先在右上角选择一个具体环境再查看。", "This page shows one environment at a time; choose a single environment (top right) to see it.")} />;
  // A missing record is not a source failure and retrying won't bring it back (PA-125).
  if (error instanceof ApiError && error.status === 404)
    return <PageUnavailable icon="search" configure={false} title={tx("找不到这条记录", "Not found")} desc={tx("它可能不存在，或在当前环境里看不到。", "It may not exist, or isn't visible in this environment.")} detail={admin ? `${error.status} ${error.code}` : undefined} />;
  const reason = errorReason(error);
  const text = reasonText(reason, source);
  return (
    <PageUnavailable
      icon={icon ?? (reason === "not_connected" ? "plug" : "alert")}
      configure={text.configurable}
      title={title ?? text.title ?? text.label}
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
