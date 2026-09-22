import { useEffect, useMemo, useRef, useState } from "react";
import { PageHeader } from "../../components/patterns/ListPage";
import ScopeFilter, { usePageScope } from "../../components/ScopeFilter";
import { FilterBar, FilterSelect } from "../../components/filters/FilterBar";
import Pagination, { useListPagination } from "../../components/Pagination";
import { Sheet } from "../../components/ui/sheet";
import { ErrorNotice } from "../../components/ui";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { timeLabel } from "./types";
import "./observability.css";

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

export default function TraceObservabilityPage({
  mode,
}: {
  mode: "traces" | "topology";
}) {
  const scope = usePageScope();
  const { preview } = useWorkspace();
  const [range, setRange] = useState("15");
  const [items, setItems] = useState<Item[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [queried, setQueried] = useState(false);
  const [selected, setSelected] = useState<Trace | null>(null);
  const controller = useRef<AbortController | null>(null);
  const sequence = useRef(0);
  const targets = scope.targets.filter((t) =>
    t.env.bindings.some((b) => b.kind === "tempo"),
  );
  const signature = JSON.stringify([
    scope.query,
    targets.map((t) => [t.env.id, t.env.binding_revision]),
    range,
    preview,
  ]);
  useEffect(() => {
    sequence.current++;
    controller.current?.abort();
    setItems([]);
    setBusy(false);
    setError(null);
    setQueried(false);
    setSelected(null);
    return () => {
      sequence.current++;
      controller.current?.abort();
    };
  }, [signature]);
  async function query() {
    const seq = ++sequence.current;
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    setBusy(true);
    setError(null);
    setSelected(null);
    setItems([]);
    setQueried(false);
    try {
      const end = new Date();
      const start = new Date(end.getTime() - Number(range) * 60000);
      const data = await request<{ items: Item[] }>(
        "/observability/traces/batch",
        {
          method: "POST",
          signal: abort.signal,
          body: JSON.stringify({
            targets: targets.map((t) => ({
              application_id: t.app.id,
              environment_id: t.env.id,
            })),
            start: start.toISOString(),
            end: end.toISOString(),
            limit: 10,
          }),
        },
      );
      if (seq === sequence.current && !abort.signal.aborted) {
        setItems(data.items);
        setQueried(true);
      }
    } catch (e) {
      if (seq === sequence.current && !abort.signal.aborted) setError(e);
    } finally {
      if (seq === sequence.current) setBusy(false);
    }
  }
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
          duration:
            Math.max(
              ...spans.map((s) => new Date(s.start).getTime() + s.duration_ms),
            ) - new Date(start).getTime(),
          services: [...new Set(spans.map((s) => s.service))],
          errors: spans.filter((s) => s.error).length,
        };
      })
      .sort((a, b) => b.start.localeCompare(a.start));
    const pairs = new Map<
      string,
      {
        from: string;
        to: string;
        count: number;
        errors: number;
        traces: Set<string>;
      }
    >();
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
    return {
      traces,
      edges: [...pairs.values()],
      services: [...new Set([...unique.values()].map((s) => s.service))].sort(),
    };
  }, [items]);
  const pages = useListPagination(
    traces,
    signature + items.map((i) => i.result?.source.captured_at).join(),
  );
  const source = items.find((i) => i.result)?.result?.source;
  const partial = items.some((i) => i.error || i.result?.limit_reached);
  return (
    <div className="management-page observability-page">
      <PageHeader
        title={mode === "traces" ? "链路追踪" : "调用拓扑"}
        description={
          mode === "traces"
            ? "查看真实请求的服务片段与耗时。"
            : "通过已采集 span 的父子关系查看服务之间的调用。"
        }
      />
      <ScopeFilter />
      <section className="card management-card">
        <FilterBar label="链路查询范围">
          <FilterSelect
            id="trace-window"
            label="时间范围"
            emptyLabel="最近 15 分钟"
            values={range === "15" ? [] : [range]}
            onChange={(v) => setRange(v[0] ?? "15")}
            options={[
              { value: "30", label: "最近 30 分钟" },
              { value: "60", label: "最近 1 小时" },
            ]}
          />
          <button
            className="btn btn-primary"
            disabled={busy || preview || !targets.length || targets.length > 6}
            onClick={() => void query()}
          >
            {busy ? "正在查询…" : "查询链路"}
          </button>
          {busy && (
            <button
              className="btn"
              onClick={() => {
                sequence.current++;
                controller.current?.abort();
                setBusy(false);
              }}
            >
              取消查询
            </button>
          )}
        </FilterBar>
        <p className="muted">
          当前 {targets.length} 个环境登记了链路来源，每服务搜索 10 条候选，
          整批最多展示 20 条共享 Trace；仅展示授权范围内的片段。
        </p>
        {targets.length > 6 && (
          <p className="management-notice">请缩小应用范围至 6 个环境以内。</p>
        )}
        {!targets.length && (
          <p className="management-notice">
            当前范围尚未登记 Tempo 来源，可切换应用或环境。
          </p>
        )}
        {preview && (
          <p className="management-notice">设计预览没有连接实时数据。</p>
        )}
      </section>
      {!!error && <ErrorNotice error={error} />}
      {items
        .filter((i) => i.error)
        .map((i) => (
          <p className="management-notice" key={i.environment_id}>
            {scope.targets.find((t) => t.env.id === i.environment_id)?.app
              .name ?? i.application_id}
            ：{i.error?.message}
          </p>
        ))}
      {source && (
        <dl className="observation-provenance">
          <div>
            <dt>来源与采集时间</dt>
            <dd>Tempo · {timeLabel(source.captured_at)}</dd>
          </div>
          <div>
            <dt>查询窗口</dt>
            <dd>
              {timeLabel(source.window.start)} — {timeLabel(source.window.end)}
            </dd>
          </div>
        </dl>
      )}
      {queried && (
        <p className="muted">
          {traces.length} 条 Trace · {services.length} 个服务 · {edges.length}{" "}
          组调用关系{partial ? " · 部分结果达到上限或查询失败" : ""}
          。搜索返回有界样本，缺失父 span
          不推断连线，不能视为完整服务依赖图或全量性能统计。
        </p>
      )}
      {queried && !traces.length && (
        <div className="card observation-empty">
          <h2>这个时间窗口没有可展示的链路</h2>
          <p>
            来源可能尚未收到采样，或所选应用没有匹配记录。可扩大到最近一小时；无记录不能说明服务健康。
          </p>
        </div>
      )}
      {!queried && !busy && !error && (
        <div className="card observation-empty">
          <h2>选择范围后查询真实链路</h2>
          <p>同一 Trace 中，只有都处于当前授权范围的服务才能显示跨服务关系。</p>
        </div>
      )}
      {mode === "topology" && !!traces.length && (
        <section className="card management-card">
          <div className="trace-service-list" aria-label="样本中的服务">
            {services.map((s) => (
              <span className="trace-service" key={s}>
                {s}
              </span>
            ))}
          </div>
          {!edges.length ? (
            <p className="management-notice">
              样本中尚未发现可连接的跨服务父子 span。单服务片段仍可在下方查看。
            </p>
          ) : (
            <div className="trace-edge-list">
              {edges.map((e) => (
                <button
                  className="trace-edge"
                  key={`${e.from}:${e.to}`}
                  onClick={() =>
                    setSelected(traces.find((t) => e.traces.has(t.id)) ?? null)
                  }
                >
                  <strong>{e.from}</strong>
                  <span aria-hidden="true">→</span>
                  <strong>{e.to}</strong>
                  <span>
                    {e.count} 个调用片段 · {e.traces.size} 条 Trace
                    {e.errors ? ` · ${e.errors} 个错误片段` : ""}
                  </span>
                </button>
              ))}
            </div>
          )}
        </section>
      )}
      {!!traces.length && (
        <section className="card management-card">
          <div className="management-table-wrap">
            <table className="management-table ui-data-table">
              <thead>
                <tr>
                  <th>Trace ID</th>
                  <th>服务</th>
                  <th>开始时间</th>
                  <th>可见片段跨度</th>
                  <th>Span</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {pages.items.map((t) => (
                  <tr key={t.id}>
                    <td>
                      <button
                        className="btn btn-ghost"
                        onClick={() => setSelected(t)}
                      >
                        {t.id.slice(0, 16)}…
                      </button>
                    </td>
                    <td>{t.services.join(" · ")}</td>
                    <td>{timeLabel(t.start)}</td>
                    <td>{t.duration.toFixed(1)} ms</td>
                    <td>{t.spans.length}</td>
                    <td>
                      {t.errors ? `${t.errors} 个错误片段` : "未标记错误"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
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
        title="链路片段"
        description="仅显示当前应用授权范围的 span；空缺父节点不代表根调用。"
        size="wide"
      >
        {selected && (
          <div className="observation-data">
            <code className="trace-id">{selected.id}</code>
            <div className="trace-spans">
              {selected.spans.map((s) => (
                <article className="trace-span" key={s.span_id}>
                  <header>
                    <strong>{s.service}</strong>
                    <span>
                      {s.duration_ms.toFixed(2)} ms{s.error ? " · 错误" : ""}
                    </span>
                  </header>
                  <p>{s.name}</p>
                  <div
                    className="trace-timing-track"
                    aria-label={`相对开始 ${Math.max(0, new Date(s.start).getTime() - new Date(selected.start).getTime()).toFixed(1)} 毫秒，持续 ${s.duration_ms.toFixed(2)} 毫秒`}
                  >
                    <span
                      style={{
                        marginLeft: `${Math.min(99, Math.max(0, ((new Date(s.start).getTime() - new Date(selected.start).getTime()) / Math.max(1, selected.duration)) * 100))}%`,
                        width: `${Math.max(0.5, Math.min(100, (s.duration_ms / Math.max(1, selected.duration)) * 100))}%`,
                      }}
                    />
                  </div>
                  <small>
                    Span {s.span_id} · Parent {s.parent_span_id || "未记录"} ·{" "}
                    {timeLabel(s.start)}
                  </small>
                </article>
              ))}
            </div>
          </div>
        )}
      </Sheet>
    </div>
  );
}
