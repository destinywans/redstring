import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { PageHeader } from "../../components/patterns/ListPage";
import ScopeFilter, { usePageScope } from "../../components/ScopeFilter";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import Pagination, { useListPagination } from "../../components/Pagination";
import { Sheet } from "../../components/ui/sheet";
import { ErrorNotice } from "../../components/ui";
import {
  RefreshCw,
  ChevronDown,
  ChevronRight,
  Network,
  Clock,
  ArrowUpRight,
} from "../../lib/icons";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { matchesSearch } from "../../lib/search";
import { timeLabel } from "./types";
import TraceNetwork, {
  type TraceEdge,
  type TraceService,
} from "./TraceNetwork";
import "./observability.css";
import "./trace-observability.css";

type Span = {
  trace_id: string;
  span_id: string;
  parent_span_id?: string;
  service: string;
  name: string;
  start: string;
  duration_ms: number;
  error: boolean;
};
type Evidence = {
  source: {
    provider: string;
    captured_at: string;
    window: { start: string; end: string };
    warnings?: string[];
  };
  spans: Span[];
  trace_count: number;
  empty: boolean;
  limit_reached: boolean;
};
type Item = {
  application_id: string;
  environment_id: string;
  result?: Evidence;
  error?: { message: string };
};
type Trace = {
  id: string;
  spans: Span[];
  start: string;
  duration: number;
  services: string[];
  errors: number;
};
type TimeWindow = { start: string; end: string };
const minutes = new Set(["15", "30", "60", "180", "360", "1440"]);
const durationLabel = (value: number) =>
  value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${value.toFixed(1)} ms`;

function validWindow(
  start: string | null,
  end: string | null,
): TimeWindow | null {
  if (!start || !end) return null;
  const a = Date.parse(start),
    b = Date.parse(end);
  return Number.isFinite(a) && Number.isFinite(b) && a < b && b - a <= 86400000
    ? { start: new Date(a).toISOString(), end: new Date(b).toISOString() }
    : null;
}

function SpanWaterfall({
  trace,
  link,
}: {
  trace: Trace;
  link: (page: "logs" | "metrics", service: string, traceId?: string) => string;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [active, setActive] = useState<string | null>(null);
  const byId = new Map(trace.spans.map((s) => [s.span_id, s]));
  const children = new Map<string, Span[]>();
  for (const s of trace.spans) {
    if (
      s.parent_span_id &&
      byId.has(s.parent_span_id) &&
      s.parent_span_id !== s.span_id
    )
      children.set(s.parent_span_id, [
        ...(children.get(s.parent_span_id) ?? []),
        s,
      ]);
  }
  const rows: { span: Span; depth: number; orphan: boolean }[] = [];
  const visited = new Set<string>();
  const walk = (span: Span, depth: number) => {
    if (visited.has(span.span_id)) return;
    visited.add(span.span_id);
    rows.push({
      span,
      depth,
      orphan: !!span.parent_span_id && !byId.has(span.parent_span_id),
    });
    if (!collapsed.has(span.span_id))
      for (const child of children.get(span.span_id) ?? [])
        walk(child, depth + 1);
    else {
      const mark = (id: string) => {
        if (visited.has(id)) return;
        visited.add(id);
        for (const c of children.get(id) ?? []) mark(c.span_id);
      };
      for (const child of children.get(span.span_id) ?? []) mark(child.span_id);
    }
  };
  trace.spans
    .filter(
      (s) =>
        !s.parent_span_id ||
        !byId.has(s.parent_span_id) ||
        s.parent_span_id === s.span_id,
    )
    .forEach((s) => walk(s, 0));
  // Malformed cycles cannot hide spans or recurse indefinitely.
  trace.spans.filter((s) => !visited.has(s.span_id)).forEach((s) => walk(s, 0));
  const selected = trace.spans.find((s) => s.span_id === active);
  return (
    <div className="trace-waterfall">
      <div className="trace-waterfall-head">
        <span>服务 / 操作</span>
        <div>
          {[0, 0.25, 0.5, 0.75, 1].map((p) => (
            <span key={p}>{durationLabel(trace.duration * p)}</span>
          ))}
        </div>
        <span>耗时</span>
      </div>
      {rows.map(({ span, depth, orphan }) => {
        const offset = Math.max(
          0,
          Date.parse(span.start) - Date.parse(trace.start),
        );
        const start = Math.min(
          100,
          (offset / Math.max(1, trace.duration)) * 100,
        );
        const width = Math.max(
          0.4,
          Math.min(
            100 - start,
            (span.duration_ms / Math.max(1, trace.duration)) * 100,
          ),
        );
        return (
          <div
            key={span.span_id}
            className={`trace-waterfall-row ${span.error ? "has-error" : ""} ${span.span_id === active ? "is-active" : ""}`}
          >
            <div
              className="trace-waterfall-name"
              style={{ paddingLeft: `${Math.min(depth, 12) * 14 + 8}px` }}
            >
              {children.has(span.span_id) ? (
                <button
                  className="trace-collapse"
                  aria-label={`${collapsed.has(span.span_id) ? "展开" : "折叠"} ${span.name}`}
                  onClick={() =>
                    setCollapsed((old) => {
                      const next = new Set(old);
                      if (next.has(span.span_id)) next.delete(span.span_id);
                      else next.add(span.span_id);
                      return next;
                    })
                  }
                >
                  {collapsed.has(span.span_id) ? (
                    <ChevronRight size={14} />
                  ) : (
                    <ChevronDown size={14} />
                  )}
                </button>
              ) : (
                <span className="trace-collapse-spacer" />
              )}
              <button
                className="trace-waterfall-select"
                onClick={() => setActive(span.span_id)}
                title={`${span.service} · ${span.name}`}
              >
                <span>{span.service}</span>
                <small>
                  {span.name}
                  {orphan ? " · 上游片段未返回" : ""}
                </small>
              </button>
            </div>
            <button
              className="trace-waterfall-track"
              onClick={() => setActive(span.span_id)}
              aria-label={`${span.name}，开始于 ${durationLabel(offset)}，持续 ${durationLabel(span.duration_ms)}`}
            >
              <span style={{ left: `${start}%`, width: `${width}%` }} />
            </button>
            <span className="trace-waterfall-duration">
              {durationLabel(span.duration_ms)}
              {span.error ? " · 错误" : ""}
            </span>
          </div>
        );
      })}
      {selected && (
        <section className="trace-span-detail">
          <h3>{selected.name}</h3>
          <dl>
            <div>
              <dt>服务</dt>
              <dd>{selected.service}</dd>
            </div>
            <div>
              <dt>Span ID</dt>
              <dd>
                <code>{selected.span_id}</code>
              </dd>
            </div>
            <div>
              <dt>父 Span ID</dt>
              <dd>
                <code>{selected.parent_span_id || "未记录"}</code>
              </dd>
            </div>
            <div>
              <dt>开始时间</dt>
              <dd>{timeLabel(selected.start)}</dd>
            </div>
          </dl>
          <div className="trace-detail-links">
            <Link className="btn" to={link("logs", selected.service, trace.id)}>
              关联日志
              <ArrowUpRight size={14} />
            </Link>
            <Link className="btn" to={link("metrics", selected.service)}>
              服务指标
              <ArrowUpRight size={14} />
            </Link>
          </div>
        </section>
      )}
    </div>
  );
}

export default function TraceObservabilityPage({
  mode,
}: {
  mode: "traces" | "topology";
}) {
  const scope = usePageScope({ globalEnvironmentOnly: false });
  const { preview } = useWorkspace();
  const [params, setParams] = useSearchParams();
  const range = minutes.has(params.get("range") || "")
    ? params.get("range")!
    : "30";
  const fixedWindow = validWindow(params.get("start"), params.get("end"));
  const invalidWindow =
    (params.has("start") || params.has("end")) && !fixedWindow;
  const traceId = params.get("trace_id")?.trim() || "";
  const invalidTraceId = !!traceId && !/^[a-f\d]{32}$/i.test(traceId);
  const [items, setItems] = useState<Item[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [queried, setQueried] = useState(false);
  const [selected, setSelected] = useState<Trace | null>(null);
  const [serviceDetail, setServiceDetail] = useState<string | null>(null);
  const [edgeDetail, setEdgeDetail] = useState<TraceEdge | null>(null);
  const [search, setSearch] = useState(traceId);
  const [status, setStatus] = useState("");
  const [minDuration, setMinDuration] = useState("");
  const [center, setCenter] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [windowUsed, setWindowUsed] = useState<TimeWindow | null>(null);
  const controller = useRef<AbortController | null>(null);
  const sequence = useRef(0);
  const priorSignature = useRef("");
  const openedTraceId = useRef("");
  const targets = scope.targets.filter((t) =>
    t.env.bindings.some((b) => b.kind === "tempo"),
  );
  const signature = JSON.stringify([
    scope.query,
    targets.map((t) => [t.env.id, t.env.binding_revision]),
    range,
    fixedWindow?.start,
    fixedWindow?.end,
    invalidWindow,
    traceId,
    preview,
  ]);
  const canQuery =
    !preview &&
    !!targets.length &&
    targets.length <= 6 &&
    !invalidWindow &&
    !invalidTraceId;

  useEffect(() => {
    setSearch(traceId);
    openedTraceId.current = "";
  }, [traceId]);
  useEffect(() => {
    const changed = priorSignature.current !== signature;
    priorSignature.current = signature;
    const seq = ++sequence.current;
    controller.current?.abort();
    if (changed) {
      setItems([]);
      setQueried(false);
      setSelected(null);
      setServiceDetail(null);
      setEdgeDetail(null);
      setWindowUsed(null);
    }
    setError(null);
    if (!canQuery) {
      setBusy(false);
      return;
    }
    const abort = new AbortController();
    controller.current = abort;
    const end = new Date();
    const window = fixedWindow ?? {
      start: new Date(end.getTime() - Number(range) * 60000).toISOString(),
      end: end.toISOString(),
    };
    setBusy(true);
    void request<{ items: Item[] }>("/observability/traces/batch", {
      method: "POST",
      signal: abort.signal,
      body: JSON.stringify({
        targets: targets.map((t) => ({
          application_id: t.app.id,
          environment_id: t.env.id,
        })),
        ...window,
        limit: 10,
        ...(traceId ? { trace_id: traceId.toLowerCase() } : {}),
      }),
    })
      .then((data) => {
        if (seq === sequence.current && !abort.signal.aborted) {
          setItems(data.items);
          setWindowUsed(window);
          setQueried(true);
        }
      })
      .catch((e) => {
        if (seq === sequence.current && !abort.signal.aborted) setError(e);
      })
      .finally(() => {
        if (seq === sequence.current && !abort.signal.aborted) setBusy(false);
      });
    return () => {
      abort.abort();
    };
    // The stable signature covers every scope, binding and time input. Refresh intentionally retains the last successful snapshot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [signature, refresh]);

  const { traces, edges, services } = useMemo(() => {
    const unique = new Map<string, Span>();
    for (const item of items)
      for (const span of item.result?.spans ?? [])
        unique.set(`${span.trace_id}:${span.span_id}`, span);
    const grouped = new Map<string, Span[]>();
    for (const span of unique.values())
      grouped.set(span.trace_id, [...(grouped.get(span.trace_id) ?? []), span]);
    const traces: Trace[] = [...grouped]
      .map(([id, spans]) => {
        spans.sort((a, b) => a.start.localeCompare(b.start));
        const start = spans[0].start;
        return {
          id,
          spans,
          start,
          duration: Math.max(
            0,
            Math.max(...spans.map((s) => Date.parse(s.start) + s.duration_ms)) -
              Date.parse(start),
          ),
          services: [...new Set(spans.map((s) => s.service))],
          errors: spans.filter((s) => s.error).length,
        };
      })
      .sort((a, b) => b.start.localeCompare(a.start));
    const pairs = new Map<string, TraceEdge>();
    for (const child of unique.values()) {
      if (!child.parent_span_id) continue;
      const parent = unique.get(`${child.trace_id}:${child.parent_span_id}`);
      if (!parent || parent.service === child.service) continue;
      const key = `${parent.service}\0${child.service}`;
      const edge = pairs.get(key) ?? {
        from: parent.service,
        to: child.service,
        count: 0,
        errors: 0,
        traces: new Set<string>(),
      };
      edge.count++;
      edge.errors += child.error ? 1 : 0;
      edge.traces.add(child.trace_id);
      pairs.set(key, edge);
    }
    const serviceMap = new Map<string, TraceService>();
    for (const trace of traces)
      for (const name of trace.services) {
        const spans = trace.spans.filter((s) => s.service === name);
        const value = serviceMap.get(name) ?? {
          name,
          spans: 0,
          errors: 0,
          traces: 0,
        };
        value.spans += spans.length;
        value.errors += spans.filter((s) => s.error).length;
        value.traces++;
        serviceMap.set(name, value);
      }
    return {
      traces,
      edges: [...pairs.values()],
      services: [...serviceMap.values()].sort((a, b) =>
        a.name.localeCompare(b.name),
      ),
    };
  }, [items]);
  useEffect(() => {
    if (!traceId || !queried || openedTraceId.current === traceId) return;
    const match = traces.find(
      (t) => t.id.toLowerCase() === traceId.toLowerCase(),
    );
    if (match) {
      openedTraceId.current = traceId;
      setSelected(match);
    }
  }, [traceId, queried, traces]);
  const filtered = traces.filter(
    (t) =>
      (!search ||
        matchesSearch(
          `${t.id} ${t.services.join(" ")} ${t.spans.map((s) => s.name).join(" ")}`,
          search,
        )) &&
      (!status || (status === "error" ? t.errors > 0 : t.errors === 0)) &&
      (!minDuration || t.duration >= Number(minDuration)),
  );
  const pages = useListPagination(
    filtered,
    signature + search + status + minDuration,
  );
  const source = items.find((i) => i.result)?.result?.source;
  const partial = items.some((i) => i.error || i.result?.limit_reached);
  const warnings = [
    ...new Set(items.flatMap((i) => i.result?.source.warnings ?? [])),
  ];
  const failed = items.filter((i) => i.error);
  const effectiveCenter = services.some((s) => s.name === center)
    ? center
    : ([...services].sort(
        (a, b) =>
          edges.filter((e) => e.from === b.name).length -
          edges.filter((e) => e.from === a.name).length,
      )[0]?.name ?? "");
  const scopedLink = (
    page: "metrics" | "logs" | "traces" | "topology",
    service?: string,
    id?: string,
  ) => {
    const next = new URLSearchParams(params);
    for (const key of ["trace_id", "search", "page", "page_size"])
      next.delete(key);
    next.set("range", range);
    if (windowUsed) {
      next.set("start", windowUsed.start);
      next.set("end", windowUsed.end);
    }
    if (service) {
      const matching = items.filter((i) =>
        i.result?.spans.some((s) => s.service === service),
      );
      if (matching.length) {
        for (const key of [
          "application",
          "environment",
          "applications",
          "environments",
          "scope",
          "environment_filter",
        ])
          next.delete(key);
        next.set(
          "applications",
          [...new Set(matching.map((i) => i.application_id))].join(","),
        );
        next.set(
          "environments",
          [...new Set(matching.map((i) => i.environment_id))].join(","),
        );
      }
    }
    if (id) next.set("trace_id", id);
    return `/observability/${page}?${next.toString()}`;
  };
  const clearExact = () => {
    const next = new URLSearchParams(params);
    next.delete("trace_id");
    setParams(next);
    setSearch("");
  };
  const submitSearch = () => {
    if (/^[a-f\d]{32}$/i.test(search.trim())) {
      const next = new URLSearchParams(params);
      next.set("trace_id", search.trim().toLowerCase());
      setParams(next);
    } else if (traceId) {
      const next = new URLSearchParams(params);
      next.delete("trace_id");
      setParams(next);
    }
  };

  return (
    <div className="management-page observability-page trace-observability-page">
      <PageHeader
        title={mode === "traces" ? "链路追踪" : "调用拓扑"}
        description={
          mode === "traces"
            ? "从请求到服务调用，定位耗时与错误。"
            : "从服务出发，探索真实请求中的上下游调用。"
        }
        actions={
          <button
            className="btn"
            disabled={busy || !canQuery}
            onClick={() => setRefresh((v) => v + 1)}
          >
            <RefreshCw size={15} />
            {busy ? "正在刷新…" : "刷新"}
          </button>
        }
      />
      <nav className="observation-tabs" aria-label="可观测性页面">
        {(
          [
            ["metrics", "指标"],
            ["logs", "日志"],
            ["traces", "链路追踪"],
            ["topology", "调用拓扑"],
          ] as const
        ).map(([page, label]) => (
          <Link
            key={page}
            className={page === mode ? "active" : ""}
            aria-current={page === mode ? "page" : undefined}
            to={scopedLink(page)}
          >
            {label}
          </Link>
        ))}
      </nav>
      <section className="card trace-query-panel">
        <ScopeFilter />
        <FilterBar label="链路搜索与筛选">
          <FilterSearch
            label="搜索链路"
            placeholder="服务、请求名称或 Trace ID"
            value={search}
            onChange={setSearch}
            onSubmit={submitSearch}
          />
          <FilterSelect
            id="trace-window"
            label="时间范围"
            emptyLabel={fixedWindow ? "指定时间段" : "最近 30 分钟"}
            values={fixedWindow ? [] : [range]}
            includeEmptyOption={false}
            onChange={(v) => {
              const next = new URLSearchParams(params);
              next.set("range", v[0] ?? "30");
              next.delete("start");
              next.delete("end");
              setParams(next);
            }}
            options={[
              { value: "15", label: "最近 15 分钟" },
              { value: "30", label: "最近 30 分钟" },
              { value: "60", label: "最近 1 小时" },
              { value: "180", label: "最近 3 小时" },
              { value: "360", label: "最近 6 小时" },
              { value: "1440", label: "最近 24 小时" },
            ]}
          />
          {mode === "traces" && (
            <>
              <FilterSelect
                id="trace-status"
                label="状态"
                values={status ? [status] : []}
                clearable
                onChange={(v) => setStatus(v[0] ?? "")}
                options={[
                  { value: "error", label: "含错误片段" },
                  { value: "normal", label: "未标记错误" },
                ]}
              />
              <label className="trace-duration-filter">
                <Clock size={14} />
                <input
                  type="number"
                  min="0"
                  max="86400000"
                  value={minDuration}
                  onChange={(e) => setMinDuration(e.target.value)}
                  placeholder="最小耗时"
                  aria-label="最小耗时（毫秒）"
                />
                <span>ms</span>
              </label>
            </>
          )}
        </FilterBar>
        {fixedWindow && (
          <div className="trace-window-label">
            {timeLabel(fixedWindow.start)} — {timeLabel(fixedWindow.end)}
            <button
              className="filter-text-button"
              onClick={() => {
                const next = new URLSearchParams(params);
                next.delete("start");
                next.delete("end");
                setParams(next);
              }}
            >
              回到最近 {range} 分钟
            </button>
          </div>
        )}
        {traceId && (
          <div className="trace-window-label">
            精确查找 <code>{traceId}</code>
            <button className="filter-text-button" onClick={clearExact}>
              清除 Trace ID
            </button>
          </div>
        )}
      </section>
      {invalidWindow && (
        <p className="management-notice">
          时间范围无效，请重新选择最近时间范围（最长 24 小时）。
        </p>
      )}
      {invalidTraceId && (
        <p className="management-notice">
          Trace ID 应为 32 位十六进制字符。请修改或清除后重试。
        </p>
      )}
      {preview && (
        <div className="card observation-empty">
          <h2>预览模式未连接观测数据</h2>
          <p>切换到真实工作区后查看已采集的链路。</p>
        </div>
      )}
      {!preview && !targets.length && (
        <div className="card observation-empty">
          <h2>当前范围尚未接入链路数据</h2>
          <p>选择已接入的应用或环境，或在连接器中配置链路来源。</p>
          <Link className="btn" to="/integrations/tools">
            查看连接器
          </Link>
        </div>
      )}
      {targets.length > 6 && (
        <div className="card observation-empty">
          <h2>请缩小查询范围</h2>
          <p>
            当前 {targets.length} 个环境具有链路来源，单次支持最多 6
            个。请选择要分析的应用。
          </p>
        </div>
      )}
      {!!error && (
        <>
          <ErrorNotice error={error} />
          {queried && (
            <p className="management-notice">
              刷新失败，保留上次成功查询的结果。最近成功时间：
              {timeLabel(source?.captured_at)}。
            </p>
          )}
        </>
      )}
      {!!failed.length && (
        <div className="trace-query-errors" role="status">
          {failed.map((i) => (
            <p key={i.environment_id}>
              {scope.targets.find((t) => t.env.id === i.environment_id)?.app
                .name ?? "应用"}
              ：{i.error?.message}
            </p>
          ))}
        </div>
      )}
      {busy && !queried && (
        <div className="card trace-loading" role="status">
          <RefreshCw size={19} />
          <span>正在读取所选范围的真实链路…</span>
        </div>
      )}
      {queried && (
        <>
          <div className="trace-sample-summary" role="status">
            <span>
              <Network size={15} />
              <strong>{traces.length}</strong> 条请求样本
            </span>
            <span>{services.length} 个服务</span>
            <span>{edges.length} 组调用关系</span>
            <span
              className={traces.some((t) => t.errors) ? "trace-error-text" : ""}
            >
              {traces.filter((t) => t.errors).length} 条含错误样本
            </span>
            <span className="trace-sample-updated">
              更新于 {timeLabel(source?.captured_at)}
            </span>
          </div>
          <p className="trace-sample-note">
            {partial ? "部分来源失败或结果达到上限。" : ""}
            当前展示有界链路样本及授权范围内的片段，不代表全部请求或完整依赖。
            {warnings.length > 0 && (
              <details>
                <summary>查询说明（{warnings.length}）</summary>
                {warnings.map((w) => (
                  <p key={w}>{w}</p>
                ))}
              </details>
            )}
          </p>
        </>
      )}
      {queried && !traces.length && (
        <div className="card observation-empty">
          <h2>
            {failed.length === items.length
              ? "链路来源暂时不可用"
              : traceId
                ? "当前范围未找到这条链路"
                : "这段时间没有可展示的链路"}
          </h2>
          <p>
            {failed.length === items.length
              ? "请检查上方来源错误，恢复后点击刷新。"
              : "可扩大时间范围或调整应用筛选。没有记录不代表服务健康，也不代表没有调用。"}
          </p>
        </div>
      )}
      {mode === "topology" && !!traces.length && (
        <>
          <TraceNetwork
            services={services}
            edges={edges}
            center={effectiveCenter}
            onCenter={setCenter}
            onService={(name) => {
              setServiceDetail(name);
              setEdgeDetail(null);
            }}
            onEdge={(edge) => {
              setEdgeDetail(edge);
              setServiceDetail(null);
            }}
          />
          {!edges.length && (
            <p className="management-notice">
              已发现服务片段，但样本中没有可连接的跨服务调用。可选择多个应用查看上下游。
            </p>
          )}
        </>
      )}
      {!!traces.length && (
        <section className="card trace-result-panel">
          <header className="trace-result-heading">
            <h2>{mode === "topology" ? "请求样本" : "链路记录"}</h2>
            <span>{filtered.length} 条匹配样本</span>
          </header>
          <div className="management-table-wrap">
            <table className="management-table ui-data-table">
              <thead>
                <tr>
                  <th>请求 / Trace ID</th>
                  <th>服务</th>
                  <th>开始时间</th>
                  <th>可见片段跨度</th>
                  <th>Span 数</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {pages.items.map((t) => (
                  <tr key={t.id}>
                    <td>
                      <button
                        className="trace-request-link"
                        onClick={() => setSelected(t)}
                      >
                        <span>{t.spans[0]?.name || "请求片段"}</span>
                        <code title={t.id}>{t.id.slice(0, 16)}…</code>
                      </button>
                    </td>
                    <td>
                      <div className="trace-table-services">
                        {t.services.map((s) => (
                          <span key={s}>{s}</span>
                        ))}
                      </div>
                    </td>
                    <td>{timeLabel(t.start)}</td>
                    <td className="trace-duration-cell">
                      {durationLabel(t.duration)}
                    </td>
                    <td>{t.spans.length}</td>
                    <td>
                      <span
                        className={`trace-status ${t.errors ? "trace-status--error" : ""}`}
                      >
                        {t.errors ? `${t.errors} 个错误片段` : "未标记错误"}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!filtered.length && (
            <div className="observation-empty">
              <h3>当前样本没有匹配项</h3>
              <p>
                调整搜索、状态或最小耗时筛选。输入完整 Trace ID
                并回车可向来源查询。
              </p>
            </div>
          )}
          <Pagination
            page={pages.page}
            pageSize={pages.pageSize}
            total={pages.total}
            onPageChange={pages.setPage}
            onPageSizeChange={pages.setPageSize}
          />
        </section>
      )}
      <Sheet
        open={!!selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
        title="请求链路"
        description="按调用层级展示当前授权范围内的片段，缺失上游不推断为根调用。"
        size="wide"
      >
        {selected && (
          <div className="trace-request-detail">
            <code className="trace-full-id">{selected.id}</code>
            <div className="trace-detail-stats">
              <span>{durationLabel(selected.duration)} 可见跨度</span>
              <span>{selected.services.length} 个服务</span>
              <span>{selected.spans.length} 个 Span</span>
              <span>{timeLabel(selected.start)}</span>
            </div>
            <SpanWaterfall
              key={selected.id}
              trace={selected}
              link={scopedLink}
            />
          </div>
        )}
      </Sheet>
      <Sheet
        open={!!serviceDetail || !!edgeDetail}
        onOpenChange={(open) => {
          if (!open) {
            setServiceDetail(null);
            setEdgeDetail(null);
          }
        }}
        title={
          serviceDetail ||
          (edgeDetail ? `${edgeDetail.from} → ${edgeDetail.to}` : "调用详情")
        }
        description="以下计数仅来自当前查询返回的链路样本。"
        size="detail"
      >
        {serviceDetail && (
          <div className="trace-service-detail">
            <div className="trace-detail-stats">
              <span>
                {services.find((s) => s.name === serviceDetail)?.traces ?? 0}{" "}
                条请求样本
              </span>
              <span>
                {services.find((s) => s.name === serviceDetail)?.errors ?? 0}{" "}
                个错误片段
              </span>
            </div>
            <div className="trace-detail-links">
              <Link className="btn" to={scopedLink("metrics", serviceDetail)}>
                指标
                <ArrowUpRight size={14} />
              </Link>
              <Link className="btn" to={scopedLink("logs", serviceDetail)}>
                日志
                <ArrowUpRight size={14} />
              </Link>
              <Link className="btn" to={scopedLink("traces", serviceDetail)}>
                链路
                <ArrowUpRight size={14} />
              </Link>
              <button
                className="btn"
                onClick={() => {
                  setCenter(serviceDetail);
                  setServiceDetail(null);
                }}
              >
                设为中心服务
              </button>
            </div>
            <h3>关联请求</h3>
            {traces
              .filter((t) => t.services.includes(serviceDetail))
              .map((t) => (
                <button
                  className="trace-related-request"
                  key={t.id}
                  onClick={() => {
                    setServiceDetail(null);
                    setSelected(t);
                  }}
                >
                  <span>
                    {t.spans[0]?.name}
                    <small>{t.id.slice(0, 16)}…</small>
                  </span>
                  <span>
                    {durationLabel(t.duration)}
                    <ChevronRight size={14} />
                  </span>
                </button>
              ))}
          </div>
        )}
        {edgeDetail && (
          <div className="trace-service-detail">
            <div className="trace-detail-stats">
              <span>{edgeDetail.count} 个调用片段</span>
              <span>{edgeDetail.traces.size} 条请求样本</span>
              <span>{edgeDetail.errors} 个错误片段</span>
            </div>
            <h3>关联请求</h3>
            {traces
              .filter((t) => edgeDetail.traces.has(t.id))
              .map((t) => (
                <button
                  className="trace-related-request"
                  key={t.id}
                  onClick={() => {
                    setEdgeDetail(null);
                    setSelected(t);
                  }}
                >
                  <span>
                    {t.spans[0]?.name}
                    <small>{t.id.slice(0, 16)}…</small>
                  </span>
                  <span>
                    {durationLabel(t.duration)}
                    <ChevronRight size={14} />
                  </span>
                </button>
              ))}
          </div>
        )}
      </Sheet>
    </div>
  );
}
