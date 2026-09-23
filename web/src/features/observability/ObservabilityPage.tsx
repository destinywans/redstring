import { useEffect, useRef, useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { PageHeader } from "../../components/patterns/ListPage";
import Pagination, { useListPagination } from "../../components/Pagination";
import ScopeFilter, { usePageScope } from "../../components/ScopeFilter";
import { FilterBar, FilterSelect, FilterSearch } from "../../components/filters/FilterBar";
import { Sheet } from "../../components/ui/sheet";
import { ErrorNotice } from "../../components/ui";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import ObservationResults from "./ObservationResults";
import ObservationDashboard, { ObservationLogs } from "./ObservationDashboard";
import TransferDialog from "./TransferDialog";
import { timeLabel, type Mode, type QuerySnapshot, type TelemetryData, type Transfer } from "./types";
import "../workspace/workspace.css";
import "./observability.css";
import "./observability-workspace.css";

type Registration = { metric_query_ids: string[]; logs_configured: boolean; log_providers?: string[] };
type BatchItem = { application_id: string; environment_id: string; query_id?: string; binding_revision: number; result?: TelemetryData; error?: { code: string; message: string } };
const applicationMetric = (id: string) => /^(请求|实例|在线实例|依赖)/.test(id);
const localDate = (date: Date) => new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);

export default function ObservabilityPage({ mode }: { mode: Mode }) {
  const { user, preview } = useWorkspace();
  const scope = usePageScope({ globalEnvironmentOnly: false });
  const [params, setParams] = useSearchParams();
  const view = params.get("view") === "infrastructure" ? "infrastructure" : "applications";
  const range = params.has("start") && params.has("end") ? "custom" : params.get("range") || "30";
  const queryId = params.get("metric") || "";
  const logProvider = params.get("provider") || "";
  const logSearch = params.get("search") || "";
  const level = params.get("level") || "";
  const [draftSearch, setDraftSearch] = useState(logSearch);
  const [customStart, setCustomStart] = useState(() => localDate(new Date(Date.now() - 1800000)));
  const [customEnd, setCustomEnd] = useState(() => localDate(new Date()));
  const [results, setResults] = useState<QuerySnapshot[]>([]);
  const [failures, setFailures] = useState<BatchItem[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [queried, setQueried] = useState(false);
  const [refresh, setRefresh] = useState(mode === "metrics");
  const [transfer, setTransfer] = useState<Transfer | null>(null);
  const [detail, setDetail] = useState<QuerySnapshot | null>(null);
  const [created, setCreated] = useState<{ id: string; title: string }[]>([]);
  const pages = useListPagination(scope.targets, scope.query + mode);
  const targets = pages.items;
  const registrations = useQueries({ queries: targets.map(({ app, env }) => ({
    queryKey: ["telemetry", app.id, env.id, preview],
    queryFn: ({ signal }: { signal: AbortSignal }) => request<Registration>(`/applications/${app.id}/telemetry?environment_id=${env.id}`, { signal }),
    enabled: !preview, retry: false, staleTime: 60_000,
  })) });
  const configured = targets.map((target, index) => ({ ...target, registration: registrations[index]?.data }));
  const matchesView = (id: string) => view === "applications" ? applicationMetric(id) : !applicationMetric(id);
  const queryIds = [...new Set(configured.flatMap(t => t.registration?.metric_query_ids ?? []))].filter(matchesView).sort();
  const available = configured.filter(t => mode === "metrics" ? t.registration?.metric_query_ids.some(id => matchesView(id) && (!queryId || id === queryId)) : t.registration?.logs_configured && (!logProvider || t.registration.log_providers?.includes(logProvider)));
  const ready = !preview && registrations.every(q => !q.isPending);
  const signature = JSON.stringify([mode, scope.query, pages.page, pages.pageSize, targets.map(t => [t.env.id, t.env.binding_revision]), registrations.map(r => r.data), view, range, params.get("start"), params.get("end"), queryId, logProvider, logSearch, level]);
  const signatureRef = useRef(signature); signatureRef.current = signature;
  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const querying = useRef(false);
  const loadedAt = results[0]?.receivedAt;
  const current = results.length > 0 && results.every(r => r.signature === signature);
  const canTransfer = current && !busy && !error && !preview && user.role !== "viewer";
  function update(values: Record<string, string>) {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(values)) value ? next.set(key, value) : next.delete(key);
    setParams(next, { replace: true });
  }
  async function query() {
    if (!ready || !targets.length) return;
    const seq = ++generation.current, saved = signature;
    controller.current?.abort();
    const abort = new AbortController(); controller.current = abort;
    querying.current = true; setBusy(true); setError(null);
    try {
      const to = range === "custom" ? new Date(params.get("end") || customEnd) : new Date();
      const from = range === "custom" ? new Date(params.get("start") || customStart) : new Date(to.getTime() - Number(range) * 60_000);
      if (!Number.isFinite(from.getTime()) || !Number.isFinite(to.getTime()) || from >= to || to.getTime() > Date.now() + 5000 || to.getTime() - from.getTime() > 86400000) throw new Error("请选择最近 24 小时内的有效时间范围。");
      const window = { start: from.toISOString(), end: to.toISOString() };
      const requested = available.flatMap(t => mode === "metrics"
        ? t.registration!.metric_query_ids.filter(id => matchesView(id) && (!queryId || id === queryId)).map(id => ({ application_id: t.app.id, environment_id: t.env.id, query_id: id }))
        : [{ application_id: t.app.id, environment_id: t.env.id }]);
      const chunks: typeof requested[] = [];
      for (let i = 0; i < requested.length; i += 20) chunks.push(requested.slice(i, i + 20));
      const all: BatchItem[] = [];
      // Keep provider fan-out bounded while presenting every configured query.
      for (let i = 0; i < chunks.length; i += 2) {
        const batches = await Promise.all(chunks.slice(i, i + 2).map(async chunk => {
          try {
            return (await request<{ items: BatchItem[] }>(`/observability/${mode}/batch`, { method: "POST", signal: abort.signal, body: JSON.stringify({ targets: chunk, ...window, ...(mode === "metrics" ? { step_seconds: Math.max(30, Math.ceil((to.getTime() - from.getTime()) / 1000 / 180)) } : { limit: 100, provider: logProvider, search: logSearch, level, summary: true }) }) })).items;
          } catch (e) {
            if (abort.signal.aborted) throw e;
            return chunk.map(t => ({ ...t, binding_revision: 0, error: { code: "query_failed", message: e instanceof Error ? e.message : "查询失败，请重试" } }));
          }
        }));
        all.push(...batches.flat());
      }
      if (seq !== generation.current || saved !== signatureRef.current) return;
      const failed = all.filter(i => i.error || i.result?.source?.failure);
      setFailures(failed);
      const succeeded = all.filter(i => i.result && !i.result.source?.failure).map(i => {
        const target = targets.find(t => t.env.id === i.environment_id)!;
        return { id: crypto.randomUUID(), signature: saved, mode, applicationId: i.application_id, applicationName: target.app.key || target.app.name, environmentId: i.environment_id, environmentName: target.env.name, queryId: i.query_id || "日志", requestedWindow: window, receivedAt: new Date().toISOString(), data: i.result! };
      });
      if (failed.length && !succeeded.length) setError(new Error("本次刷新失败，保留上次成功的数据。"));
      else setResults(succeeded);
      setQueried(true);
    } catch (e) { if (seq === generation.current && !abort.signal.aborted) setError(e); }
    finally { if (seq === generation.current) { querying.current = false; setBusy(false); } }
  }
  const queryRef = useRef(query); queryRef.current = query;
  useEffect(() => {
    generation.current++; controller.current?.abort(); querying.current = false;
    setResults([]); setFailures([]); setError(null); setQueried(false); setBusy(false); setDetail(null); setTransfer(null);
    if (!ready) return;
    const timer = window.setTimeout(() => void queryRef.current(), 80);
    return () => { window.clearTimeout(timer); generation.current++; controller.current?.abort(); };
  }, [signature, ready]);
  useEffect(() => {
    if (!refresh || range === "custom" || !ready) return;
    const tick = () => { if (!document.hidden && !querying.current) void queryRef.current(); };
    const timer = window.setInterval(tick, 30_000);
    document.addEventListener("visibilitychange", tick);
    return () => { window.clearInterval(timer); document.removeEventListener("visibilitychange", tick); };
  }, [refresh, range, ready]);
  useEffect(() => { setDraftSearch(logSearch); }, [logSearch]);
  useEffect(() => { setRefresh(mode === "metrics"); }, [mode]);
  const crossLink = (destination: string, snapshot?: QuerySnapshot) => {
    const next = new URLSearchParams(params);
    for (const key of ["metric", "view", "provider", "level", "search", "page", "page_size"]) next.delete(key);
    if (snapshot) { next.set("application", snapshot.applicationId); next.delete("applications"); next.set("environment", snapshot.environmentId); next.delete("environments"); next.set("start", snapshot.requestedWindow.start); next.set("end", snapshot.requestedWindow.end); }
    return `/observability/${destination}?${next}`;
  };
  return <div className="management-page observability-page obs-workspace">
    <PageHeader title={mode === "metrics" ? "指标" : "日志"} description={mode === "metrics" ? "查看服务表现与资源用量，定位运行中的变化。" : "检索运行日志，连接请求上下文与问题线索。"} actions={<Link className="btn" to="/integrations">数据源</Link>} />
    {mode === "metrics" && <div className="obs-view-tabs" role="group" aria-label="指标视角">{[["applications", "应用服务"], ["infrastructure", "基础设施与中间件"]].map(([key, title]) => <button key={key} className={view === key ? "active" : ""} aria-pressed={view === key} onClick={() => update({ view: key, metric: "" })}>{title}</button>)}</div>}
    <section className="obs-toolbar" aria-label="观测范围与时间">
      <ScopeFilter />
      <FilterBar label="观测筛选">
        {mode === "logs" && <FilterSearch value={draftSearch} onChange={setDraftSearch} onSubmit={() => update({ search: draftSearch })} label="检索日志" placeholder="搜索日志内容，按回车检索" />}
        <FilterSelect id="observation-time" label="时间范围" values={[range]} onChange={v => { const value = v[0] || "30"; update({ range: value, start: "", end: "" }); }} includeEmptyOption={false} options={[{ value: "15", label: "最近 15 分钟" }, { value: "30", label: "最近 30 分钟" }, { value: "60", label: "最近 1 小时" }, { value: "360", label: "最近 6 小时" }, { value: "1440", label: "最近 24 小时" }, { value: "custom", label: "自定义时间" }]} />
        {mode === "metrics" ? <FilterSelect id="observation-metric" label="指标" searchable values={queryId ? [queryId] : []} options={queryIds.map(id => ({ value: id, label: id }))} onChange={v => update({ metric: v[0] || "" })} /> : <>
          <FilterSelect id="observation-level" label="日志级别" values={level ? [level] : []} onChange={v => update({ level: v[0] || "" })} options={["error", "warn", "info", "debug", "trace", "fatal"].map(v => ({ value: v, label: v.toUpperCase() }))} />
          <FilterSelect id="observation-provider" label="数据源" emptyLabel="自动选择数据源" values={logProvider ? [logProvider] : []} onChange={v => update({ provider: v[0] || "" })} options={[{ value: "elasticsearch", label: "Elasticsearch" }, { value: "loki", label: "Loki" }]} />
        </>}
        <button className="btn" onClick={() => void query()} disabled={busy || !ready || !available.length}>{busy ? "刷新中…" : "刷新"}</button>
        <button className={`btn ${refresh && range !== "custom" ? "obs-enabled" : ""}`} aria-pressed={refresh && range !== "custom"} disabled={range === "custom" || preview} onClick={() => setRefresh(v => !v)}>{mode === "logs" ? (refresh ? "暂停实时刷新" : "实时刷新") : (refresh ? "自动刷新 · 30 秒" : "自动刷新已暂停")}</button>
      </FilterBar>
      {range === "custom" && <form className="observation-custom-range" onSubmit={e => { e.preventDefault(); update({ start: new Date(customStart).toISOString(), end: new Date(customEnd).toISOString() }); }}><label>开始时间<input className="field" type="datetime-local" required value={params.get("start") ? localDate(new Date(params.get("start")!)) : customStart} onChange={e => { setCustomStart(e.target.value); update({ start: "", end: "" }); }} /></label><label>结束时间<input className="field" type="datetime-local" required value={params.get("end") ? localDate(new Date(params.get("end")!)) : customEnd} onChange={e => { setCustomEnd(e.target.value); update({ start: "", end: "" }); }} /></label><button className="btn" type="submit">应用时间</button></form>}
    </section>
    <div className="obs-status" role="status"><span className={`obs-status-dot ${error ? "is-error" : ""}`} />{busy ? "正在读取运行数据" : loadedAt ? `更新于 ${timeLabel(loadedAt)}` : ready ? "等待数据" : "正在加载观测范围"}<span>{available.length} 个已接入环境</span>{!!failures.length && <span> · {failures.length} 项查询失败</span>}</div>
    {!!error && <ErrorNotice error={error} />}
    {!!failures.length && <details className="obs-failures"><summary>查看未能获取的数据（{failures.length}）</summary>{failures.map((f, i) => <p key={i}>{targets.find(t => t.env.id === f.environment_id)?.app.name} · {f.query_id || "日志"}：{f.error?.message || f.result?.source?.failure?.message}</p>)}</details>}
    {registrations.some(r => r.isError) && <p className="management-notice">部分数据源配置暂时无法读取，请刷新重试。</p>}
    {preview && <p className="management-notice">预览模式未连接数据源。</p>}
    {mode === "metrics" && !!results.length && <ObservationDashboard results={results} view={view} onSelect={setDetail} />}
    {mode === "logs" && !!results.length && <ObservationLogs results={results} canTransfer={canTransfer} onTransfer={setTransfer} link={crossLink} />}
    {!results.length && (busy || !ready) && !preview && <div className="obs-loading-grid" aria-label="正在加载观测数据">{[0, 1, 2, 3].map(i => <div key={i} />)}</div>}
    {!results.length && !busy && ready && !error && <section className="card management-card observation-empty"><h2>{!targets.length ? "当前范围没有可访问的资源" : !available.length ? "当前范围尚未接入这类数据" : queried ? "这段时间没有记录" : "准备读取数据"}</h2><p>{!available.length ? "切换应用范围，或在数据源中配置对应的采集。" : "可以扩大时间范围或调整筛选条件。"}</p><Link className="btn" to="/integrations">查看数据源</Link></section>}
    {scope.targets.length > pages.pageSize && <Pagination page={pages.page} pageSize={pages.pageSize} total={pages.total} pageSizeOptions={[10, 20]} onPageChange={pages.setPage} onPageSizeChange={pages.setPageSize} totalLabel={`共 ${pages.total} 个环境 · 正在展示当前页范围`} />}
    {!!created.length && <div className="obs-created">已创建调查：{created.map(i => <Link key={i.id} to={`/investigations/${i.id}`}>{i.title}</Link>)}</div>}
    <Sheet open={!!detail} onOpenChange={open => { if (!open) setDetail(null); }} title={detail?.queryId || "指标详情"} description={detail ? `${detail.applicationName} · ${detail.environmentName}` : undefined} size="wide">{detail && <><div className="obs-detail-links"><Link className="btn" to={crossLink("logs", detail)}>查看日志</Link><Link className="btn" to={crossLink("traces", detail)}>查看链路</Link><button className="btn" disabled={!canTransfer} onClick={() => { setDetail(null); setTransfer({ snapshot: detail, selection: { kind: "returned_result" } }); }}>带入调查</button></div><ObservationResults snapshot={detail} canTransfer={canTransfer} onTransfer={selection => { setDetail(null); setTransfer({ snapshot: detail, selection }); }} /></>}</Sheet>
    {transfer && <TransferDialog isCurrent={canTransfer} transfer={transfer} onClose={() => setTransfer(null)} onCreated={i => setCreated(prev => prev.some(v => v.id === i.id) ? prev : [...prev, i])} />}
  </div>;
}
