import { PageHeader } from "../../components/patterns/ListPage";
import Pagination, { useListPagination } from "../../components/Pagination";
import { useEffect, useRef, useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { Link, useLocation } from "react-router-dom";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import ScopeFilter, { usePageScope } from "../../components/ScopeFilter";
import { FilterBar, FilterSelect } from "../../components/filters/FilterBar";
import { ErrorNotice } from "../../components/ui";
import ObservationResults from "./ObservationResults";
import TransferDialog from "./TransferDialog";
import {
  timeLabel,
  type Mode,
  type QuerySnapshot,
  type TelemetryData,
  type Transfer,
} from "./types";
import "../workspace/workspace.css";
import "./observability.css";

type Registration = {
  metric_query_ids: string[];
  logs_configured: boolean;
  log_providers?: string[];
  traces_configured?: boolean;
};
type BatchItem = {
  application_id: string;
  environment_id: string;
  query_id?: string;
  binding_revision: number;
  result?: TelemetryData;
  error?: { code: string; message: string };
};
function localDate(value: Date) {
  return new Date(value.getTime() - value.getTimezoneOffset() * 60_000)
    .toISOString()
    .slice(0, 16);
}
export default function ObservabilityPage({ mode }: { mode: Mode }) {
  const { user, preview } = useWorkspace(),
    scope = usePageScope(),
    location = useLocation();
  const [logProvider, setLogProvider] = useState(""),
    [logSearch, setLogSearch] = useState("");
  const [queryId, setQueryId] = useState(""),
    [range, setRange] = useState("30"),
    [limit, setLimit] = useState(100);
  const [start, setStart] = useState(() =>
      localDate(new Date(Date.now() - 1800000)),
    ),
    [end, setEnd] = useState(() => localDate(new Date()));
  const [results, setResults] = useState<QuerySnapshot[]>([]),
    [failures, setFailures] = useState<BatchItem[]>([]),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null),
    [transfer, setTransfer] = useState<Transfer | null>(null);
  const [created, setCreated] = useState<
    {
      id: string;
      title: string;
      applicationId: string;
      environmentId: string;
    }[]
  >([]);
  const controller = useRef<AbortController | null>(null),
    generation = useRef(0);
  const targetPages = useListPagination(
    scope.targets,
    JSON.stringify([scope.query, mode]),
  );
  const targets = targetPages.items;
  const registrations = useQueries({
    queries: targets.map(({ app, env }) => ({
      queryKey: ["telemetry", app.id, env.id, preview],
      queryFn: () =>
        request<Registration>(
          `/applications/${app.id}/telemetry?environment_id=${env.id}`,
        ),
      enabled: !preview,
      retry: false,
    })),
  });
  const queryIds = [
    ...new Set(registrations.flatMap((q) => q.data?.metric_query_ids ?? [])),
  ].sort();
  const signature = JSON.stringify([
    scope.query,
    targetPages.page,
    targetPages.pageSize,
    targets.map((t) => [t.env.id, t.env.binding_revision]),
    mode,
    queryId,
    range,
    range === "custom" ? [start, end] : null,
    limit,
    logProvider,
    logSearch,
  ]);
  const signatureRef = useRef(signature);
  signatureRef.current = signature;
  useEffect(() => {
    generation.current++;
    controller.current?.abort();
    setBusy(false);
    setError(null);
    setFailures([]);
  }, [signature]);
  useEffect(
    () => () => {
      generation.current++;
      controller.current?.abort();
    },
    [],
  );
  const current =
    results.length > 0 && results.every((r) => r.signature === signature);
  const canTransfer =
    current && !busy && !error && !preview && user.role !== "viewer";
  async function query() {
    const seq = ++generation.current,
      savedSignature = signature;
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    setBusy(true);
    setError(null);
    try {
      const to = range === "custom" ? new Date(end) : new Date(),
        from =
          range === "custom"
            ? new Date(start)
            : new Date(to.getTime() - Number(range) * 60000);
      if (
        !Number.isFinite(from.getTime()) ||
        !Number.isFinite(to.getTime()) ||
        from >= to ||
        to.getTime() > Date.now() + 5000
      )
        throw new Error("请选择有效的起止时间，结束不能在未来。");
      const window = { start: from.toISOString(), end: to.toISOString() };
      const requested = targets.map(({ app, env }, index) => ({
        application_id: app.id,
        environment_id: env.id,
        ...(mode === "metrics"
          ? {
              query_id:
                queryId || registrations[index].data?.metric_query_ids[0] || "",
            }
          : {}),
      }));
      const valid = requested.filter((t) => mode !== "metrics" || t.query_id);
      const unavailable: BatchItem[] = requested
        .filter((t) => mode === "metrics" && !t.query_id)
        .map((t) => ({
          ...t,
          binding_revision: 0,
          error: {
            code: "unregistered",
            message: "未登记指标或无法读取查询配置，请检查数据源。",
          },
        }));
      const batch = valid.length
        ? await request<{ items: BatchItem[]; partial: boolean }>(
            `/observability/${mode}/batch`,
            {
              method: "POST",
              signal: abort.signal,
              body: JSON.stringify({
                targets: valid,
                ...window,
                ...(mode === "logs"
                  ? { limit, provider: logProvider, search: logSearch }
                  : { step_seconds: 30 }),
              }),
            },
          )
        : { items: [], partial: true };
      batch.items.push(...unavailable);
      if (seq !== generation.current || savedSignature !== signatureRef.current)
        return;
      setFailures(batch.items.filter((i) => i.error));
      setResults(
        batch.items
          .filter((i) => i.result)
          .map((i) => {
            const target = targets.find((t) => t.env.id === i.environment_id)!;
            return {
              id: crypto.randomUUID(),
              signature: savedSignature,
              mode,
              applicationId: i.application_id,
              applicationName: target.app.name,
              environmentId: i.environment_id,
              environmentName: target.env.name,
              queryId:
                i.query_id ??
                (mode === "metrics" ? queryId : "registered-log-scope"),
              requestedWindow: window,
              receivedAt: new Date().toISOString(),
              data: i.result!,
            };
          }),
      );
    } catch (e) {
      if (seq === generation.current && !abort.signal.aborted) setError(e);
    } finally {
      if (seq === generation.current) setBusy(false);
    }
  }
  const mergedLogs = results
    .flatMap((snapshot) =>
      (snapshot.data.entries ?? []).map((entry, index) => ({
        entry,
        index,
        snapshot,
      })),
    )
    .sort((a, b) =>
      String(b.entry.timestamp).localeCompare(String(a.entry.timestamp)),
    );
  const logPages = useListPagination(
    mergedLogs,
    JSON.stringify([signature, results.map((r) => r.id)]),
  );
  return (
    <div className="management-page observability-page">
      <PageHeader
        title={mode === "metrics" ? "指标工作台" : "日志工作台"}
        description="跨应用查看运行数据，将选中的证据带入具体调查。"
        actions={
          <Link className="btn" to="/integrations">
            管理数据源
          </Link>
        }
      />
      <ScopeFilter />
      {new URLSearchParams(location.search).get("incident") && (
        <p className="muted">
          从告警进入 ·{" "}
          <Link
            to={`/incidents?incident=${new URLSearchParams(location.search).get("incident")}`}
          >
            返回告警
          </Link>
        </p>
      )}
      <section className="card management-card observation-query">
        <div className="observation-query-controls">
          <FilterBar label="观测查询范围">
            {mode === "metrics" && (
              <FilterSelect
                id="observation-metric"
                label="指标"
                emptyLabel="每个环境的默认指标"
                searchable
                values={queryId ? [queryId] : []}
                onChange={(values) => setQueryId(values[0] ?? "")}
                options={queryIds.map((id) => ({ value: id, label: id }))}
              />
            )}
            <FilterSelect
              id="observation-time-range"
              label="时间范围"
              emptyLabel="最近 30 分钟"
              values={range === "30" ? [] : [range]}
              onChange={(values) => setRange(values[0] ?? "30")}
              options={[
                { value: "15", label: "最近 15 分钟" },
                { value: "60", label: "最近 1 小时" },
                { value: "custom", label: "自定义起止" },
              ]}
            />
            {mode === "logs" && (
              <>
                <FilterSelect
                  id="log-provider"
                  label="日志来源"
                  emptyLabel="环境默认来源"
                  values={logProvider ? [logProvider] : []}
                  onChange={(v) => setLogProvider(v[0] ?? "")}
                  options={[
                    { value: "elasticsearch", label: "Elasticsearch" },
                    { value: "loki", label: "Loki" },
                  ]}
                />
                <label>
                  服务端搜索
                  <input
                    className="field"
                    value={logSearch}
                    maxLength={128}
                    placeholder="日志文本或短语"
                    onChange={(e) => setLogSearch(e.target.value)}
                  />
                </label>
              </>
            )}
            {mode === "logs" && (
              <label>
                每个环境最多条数
                <input
                  className="field"
                  type="number"
                  min="1"
                  max="500"
                  value={limit}
                  onChange={(e) =>
                    setLimit(
                      Math.max(1, Math.min(500, Number(e.target.value) || 1)),
                    )
                  }
                />
              </label>
            )}
            <button
              className="btn btn-primary"
              disabled={
                busy ||
                preview ||
                !targets.length ||
                (mode === "metrics" && registrations.some((r) => r.isPending))
              }
              onClick={() => void query()}
            >
              {busy ? "正在查询…" : "查询运行数据"}
            </button>
            {busy && (
              <button
                className="btn"
                onClick={() => {
                  generation.current++;
                  controller.current?.abort();
                  setBusy(false);
                }}
              >
                取消查询
              </button>
            )}
          </FilterBar>
        </div>
        {range === "custom" && (
          <div className="observation-custom-range">
            <label>
              开始时间
              <input
                className="field"
                type="datetime-local"
                value={start}
                onChange={(e) => setStart(e.target.value)}
              />
            </label>
            <label>
              结束时间
              <input
                className="field"
                type="datetime-local"
                value={end}
                onChange={(e) => setEnd(e.target.value)}
              />
            </label>
          </div>
        )}
        <p className="muted">
          只查询已登记的数据源。各环境分别应用权限、时间窗口与返回上限；未配置或失败会单独列出。
        </p>
        <Pagination
          page={targetPages.page}
          pageSize={targetPages.pageSize}
          total={targetPages.total}
          onPageChange={targetPages.setPage}
          onPageSizeChange={targetPages.setPageSize}
          totalLabel={`范围内共 ${targetPages.total} 个目标`}
          pageSizeOptions={[10, 20]}
        />
        {preview && (
          <p className="management-notice">设计预览没有连接数据源。</p>
        )}
      </section>
      {!!error && <ErrorNotice error={error} />}
      {!!results.length && !current && (
        <p className="management-notice">
          筛选条件已变化。下方保留原查询快照，请重新查询后再调查。
        </p>
      )}
      {failures.map((f) => (
        <p className="management-notice" key={f.environment_id}>
          {targets.find((t) => t.env.id === f.environment_id)?.app.name ??
            f.application_id}{" "}
          ·{" "}
          {targets.find((t) => t.env.id === f.environment_id)?.env.name ??
            f.environment_id}
          ：{f.error?.message}
        </p>
      ))}
      {mode === "logs" && mergedLogs.length > 0 && (
        <section className="card management-card">
          <h2>跨应用日志时间线</h2>
          <p className="muted">
            仅合并本次返回的日志，最新优先；各数据源可能截断。
          </p>
          <div className="management-table-wrap">
            <table className="management-table ui-data-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>应用 / 环境</th>
                  <th>日志</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {logPages.items.map(({ entry, index, snapshot }) => (
                  <tr key={`${snapshot.id}-${index}`}>
                    <td>{timeLabel(entry.timestamp)}</td>
                    <td>
                      {snapshot.applicationName}
                      <small>
                        {snapshot.environmentName} ·{" "}
                        {snapshot.data.source?.provider}
                      </small>
                    </td>
                    <td
                      style={{
                        whiteSpace: "pre-wrap",
                        overflowWrap: "anywhere",
                      }}
                    >
                      {entry.line}
                    </td>
                    <td>
                      <button
                        className="btn"
                        disabled={!canTransfer}
                        onClick={() =>
                          setTransfer({
                            snapshot,
                            selection: { kind: "log_entry", index },
                          })
                        }
                      >
                        调查此条
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pagination
            page={logPages.page}
            pageSize={logPages.pageSize}
            total={logPages.total}
            onPageChange={logPages.setPage}
            onPageSizeChange={logPages.setPageSize}
            totalLabel={`本次日志结果共 ${logPages.total} 条`}
          />
        </section>
      )}
      {results.map((snapshot) => (
        <section
          className="card management-card observation-results"
          key={snapshot.id}
        >
          <div className="management-heading">
            <div>
              <h2>
                {snapshot.applicationName}{" "}
                <span className="badge">{snapshot.environmentName}</span>
              </h2>
              <p>
                {snapshot.queryId} · {timeLabel(snapshot.receivedAt)}
              </p>
            </div>
            <button
              className="btn"
              disabled={!canTransfer}
              onClick={() =>
                setTransfer({
                  snapshot,
                  selection: { kind: "returned_result" },
                })
              }
            >
              带入调查
            </button>
          </div>
          <ObservationResults
            snapshot={snapshot}
            canTransfer={canTransfer}
            onTransfer={(selection) => setTransfer({ snapshot, selection })}
          />
        </section>
      ))}
      {!results.length && !busy && !failures.length && (
        <section className="card management-card observation-empty">
          <h2>从一个时间窗口开始</h2>
          <p>选择应用范围并查询。没有数据不代表服务健康。</p>
        </section>
      )}
      {!!created.length && (
        <section className="card management-card">
          <h2>本页已创建的调查</h2>
          {created.map((i) => (
            <p key={i.id}>
              <Link to={`/investigations/${i.id}`}>{i.title}</Link>
            </p>
          ))}
        </section>
      )}
      {transfer && (
        <TransferDialog
          isCurrent={canTransfer}
          transfer={transfer}
          onClose={() => setTransfer(null)}
          onCreated={(i) =>
            setCreated((prev) =>
              prev.some((v) => v.id === i.id) ? prev : [...prev, i],
            )
          }
        />
      )}
    </div>
  );
}
