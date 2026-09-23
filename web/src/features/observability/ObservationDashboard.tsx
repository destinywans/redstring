import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ResponsiveContainer, LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, BarChart, Bar, Cell } from "recharts";
import Pagination, { useListPagination } from "../../components/Pagination";
import { Sheet } from "../../components/ui/sheet";
import { type QuerySnapshot, type Transfer, timeLabel } from "./types";

const palette = ["var(--accent)", "#2c9c96", "#bd873d", "#507db7", "#b56583", "#699850", "#976db3", "#548b9c"];
const clock = (value: number) => new Date(value * 1000).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
const compact = (value: number) => new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2, notation: Math.abs(value) >= 10000 ? "compact" : "standard" }).format(value);
function metricUnit(id: string) {
  if (/使用率|错误率|命中率|CPU/.test(id)) return { unit: "%", scale: 1 };
  if (/P95耗时/.test(id)) return { unit: "ms", scale: 1000 };
  if (/网络.*速率/.test(id)) return { unit: "KiB/s", scale: 1 / 1024 };
  if (/内存使用量|内存用量/.test(id)) return { unit: "MiB", scale: 1 / 1048576 };
  if (/速率|QPS/.test(id)) return { unit: "次/s", scale: 1 };
  return { unit: "", scale: 1 };
}
function latest(s: NonNullable<QuerySnapshot["data"]["series"]>[number]) {
  const sample = s.samples.at(-1);
  if (!sample || sample.value.trim() === "") return null;
  const value = Number(sample.value);
  return Number.isFinite(value) ? value : null;
}
function seriesName(labels: Record<string, string>, fallback: string) {
  return labels.resource || labels.node || labels.container || labels.pod || labels.instance || labels.service || labels.application || fallback;
}
const tipStyle = { background: "var(--panel)", border: "1px solid var(--border)", borderRadius: 8, color: "var(--text)", fontSize: 12 };
function MetricPanel({ id, snapshots, onSelect }: { id: string; snapshots: QuerySnapshot[]; onSelect: (s: QuerySnapshot) => void }) {
  const { unit, scale } = metricUnit(id);
  const lines = snapshots.flatMap(s => (s.data.series ?? []).map(series => ({ series, snapshot: s, name: snapshots.length > 1 ? `${s.applicationName}${Object.keys(series.labels).length > 1 ? ` · ${seriesName(series.labels, "")}` : ""}` : seriesName(series.labels, s.applicationName) })));
  const rows = new Map<number, Record<string, number | null>>();
  lines.forEach(({ series }, i) => series.samples.forEach(point => {
    const row = rows.get(point.timestamp) || { timestamp: point.timestamp };
    const n = point.value.trim() ? Number(point.value) : NaN;
    row[`s${i}`] = Number.isFinite(n) ? n * scale : null;
    rows.set(point.timestamp, row);
  }));
  const data = [...rows.values()].sort((a, b) => Number(a.timestamp) - Number(b.timestamp));
  const hasValues = data.some(row => Object.entries(row).some(([key, value]) => key !== "timestamp" && value !== null));
  return <section className="obs-chart-card"><header><h2>{id}</h2><span>{unit || "采样值"}</span></header>
    {hasValues ? <div className="obs-chart" role="img" aria-label={`${id}，${lines.length} 条真实指标趋势`}><ResponsiveContainer width="100%" height="100%"><LineChart data={data} margin={{ top: 12, right: 12, bottom: 0, left: 0 }}><CartesianGrid stroke="var(--border)" vertical={false} strokeDasharray="3 3" /><XAxis dataKey="timestamp" tickFormatter={clock} minTickGap={48} axisLine={false} tickLine={false} tick={{ fill: "var(--muted)", fontSize: 11 }} /><YAxis width={48} tickFormatter={compact} axisLine={false} tickLine={false} tick={{ fill: "var(--muted)", fontSize: 11 }} /><Tooltip contentStyle={tipStyle} labelFormatter={v => clock(Number(v))} formatter={(v, name) => [typeof v === "number" ? `${compact(v)} ${unit}` : "无数据", name]} />{lines.map((line, i) => <Line key={i} dataKey={`s${i}`} name={line.name} stroke={palette[i % palette.length]} strokeWidth={1.7} dot={false} connectNulls={false} isAnimationActive={false} />)}</LineChart></ResponsiveContainer></div> : <div className="obs-chart-empty">这个时间段暂无有效采样</div>}
    <div className="obs-chart-legend">{lines.map((line, i) => <button key={i} title={`${line.name} · 查看详情`} onClick={() => onSelect(line.snapshot)}><i style={{ background: palette[i % palette.length] }} /><span>{line.name}</span><em>{latest(line.series) === null ? "—" : compact(latest(line.series)! * scale)} {unit}</em></button>)}</div>
    {!lines.length && <button className="obs-chart-inspect" onClick={() => onSelect(snapshots[0])}>查看数据来源</button>}
  </section>;
}
export default function ObservationDashboard({ results, view, onSelect }: { results: QuerySnapshot[]; view: string; onSelect: (s: QuerySnapshot) => void }) {
  const grouped = new Map<string, QuerySnapshot[]>();
  results.forEach(s => grouped.set(s.queryId, [...(grouped.get(s.queryId) || []), s]));
  const collect = (pattern: RegExp) => results.filter(s => pattern.test(s.queryId)).flatMap(s => (s.data.series || []).map(latest).filter((v): v is number => v !== null));
  const sum = (values: number[]) => values.length ? values.reduce((a, b) => a + b, 0) : null;
  const mean = (values: number[]) => values.length ? values.reduce((a, b) => a + b, 0) / values.length : null;
  const isInfra = view === "infrastructure";
  const rps = collect(/^请求速率$/), p95 = collect(/请求P95耗时/);
  const rates = results.filter(s => s.queryId === "请求速率");
  let weighted = 0, rateTotal = 0, rateComplete = rates.length > 0;
  for (const s of rates) {
    const r = sum((s.data.series || []).map(latest).filter((v): v is number => v !== null));
    const err = results.find(e => e.environmentId === s.environmentId && e.queryId === "请求错误率");
    const e = err ? mean((err.data.series || []).map(latest).filter((v): v is number => v !== null)) : null;
    if (r === null || (r > 0 && e === null)) rateComplete = false;
    if (r !== null && r > 0 && e !== null) { weighted += r * e; rateTotal += r; }
  }
  const kpis = isInfra ? [
    { label: "VM 节点平均 CPU", value: mean(collect(/^节点CPU使用率$/)), unit: "%", note: "当前有采样的 VM 节点" },
    { label: "VM 节点平均内存", value: mean(collect(/^节点内存使用率$/)), unit: "%", note: "当前有采样的 VM 节点" },
    { label: "最高磁盘使用率", value: collect(/磁盘使用率/).length ? Math.max(...collect(/磁盘使用率/)) : null, unit: "%", note: "已接入节点与主机" },
    { label: "指标覆盖", value: results.filter(s => s.data.series?.some(series => latest(series) !== null)).length, unit: `/ ${results.length}`, note: "有数据的查询 / 本次查询" },
  ] : [
    { label: "请求速率", value: sum(rps), unit: "次/s", note: "当前范围内服务合计" },
    { label: "请求错误率", value: rateComplete && rateTotal > 0 ? weighted / rateTotal : null, unit: "%", note: "按请求速率加权 · HTTP 5xx" },
    { label: "最慢服务 P95", value: p95.length ? Math.max(...p95) * 1000 : null, unit: "ms", note: "各服务 P95 的最大值" },
    { label: "在线实例", value: sum(collect(/^在线实例数$/)), unit: "个", note: "观测采集目标在线实例" },
  ];
  const rankingQuery = isInfra ? /CPU使用率/ : /^请求速率$/;
  const rankings = results.filter(s => rankingQuery.test(s.queryId)).flatMap(s => (s.data.series || []).map(series => ({ name: seriesName(series.labels, s.applicationName), value: latest(series), snapshot: s }))).filter((r): r is typeof r & { value: number } => r.value !== null).sort((a, b) => b.value - a.value).slice(0, 8);
  return <>
    <div className="obs-kpis">{kpis.map(k => <article key={k.label}><span>{k.label}</span><div>{k.value === null ? "—" : compact(k.value)}<small>{k.unit}</small></div><p>{k.value === null ? "当前没有可用采样" : k.note}</p></article>)}</div>
    <div className="obs-chart-grid">{[...grouped].map(([id, snapshots]) => <MetricPanel key={id} id={id} snapshots={snapshots} onSelect={onSelect} />)}</div>
    {!!rankings.length && <section className="obs-chart-card obs-ranking"><header><h2>{isInfra ? "CPU 用量对比" : "服务请求速率对比"}</h2><span>最近采样 · 前 {rankings.length} 项</span></header><div style={{ height: Math.max(180, rankings.length * 34 + 35) }} role="img" aria-label={isInfra ? "资源 CPU 用量横向对比" : "各服务请求速率横向对比"}><ResponsiveContainer width="100%" height="100%"><BarChart data={rankings} layout="vertical" margin={{ left: 10, right: 35, top: 10, bottom: 0 }}><XAxis type="number" tickFormatter={compact} axisLine={false} tickLine={false} tick={{ fill: "var(--muted)", fontSize: 11 }} /><YAxis dataKey="name" type="category" width={170} axisLine={false} tickLine={false} tick={{ fill: "var(--text)", fontSize: 11 }} /><Tooltip contentStyle={tipStyle} formatter={v => typeof v === "number" ? `${compact(v)} ${isInfra ? "%" : "次/s"}` : "—"} /><Bar dataKey="value" name={isInfra ? "CPU" : "请求速率"} barSize={12} radius={[0, 3, 3, 0]} isAnimationActive={false} onClick={(_, index) => onSelect(rankings[index].snapshot)}>{rankings.map((_, i) => <Cell key={i} fill={palette[i % palette.length]} />)}</Bar></BarChart></ResponsiveContainer></div></section>}
  </>;
}

type LogRow = { entry: NonNullable<QuerySnapshot["data"]["entries"]>[number]; index: number; snapshot: QuerySnapshot };
function logInfo(entry: LogRow["entry"]) {
  let parsed: Record<string, unknown> = {};
  try { const v = JSON.parse(entry.line); if (v && typeof v === "object") parsed = v; } catch { /* Unstructured logs remain plain text. */ }
  const severity = String(entry.level || entry.labels.level || parsed.level || "unknown").toLowerCase();
  const trace = String(entry.trace_id || entry.labels.trace_id || parsed.trace_id || "");
  return { severity, trace: /^[a-f\d]{32}$/i.test(trace) && !/^0+$/.test(trace) ? trace : "", message: typeof parsed.msg === "string" ? parsed.msg : typeof parsed.message === "string" ? parsed.message : entry.line };
}
export function ObservationLogs({ results, canTransfer, onTransfer, link }: { results: QuerySnapshot[]; canTransfer: boolean; onTransfer: (transfer: Transfer) => void; link: (destination: string, snapshot?: QuerySnapshot) => string }) {
  const [selected, setSelected] = useState<LogRow | null>(null);
  const rows = useMemo(() => results.flatMap(snapshot => (snapshot.data.entries || []).map((entry, index) => ({ entry, index, snapshot }))).sort((a, b) => b.entry.timestamp.localeCompare(a.entry.timestamp)), [results]);
  const pages = useListPagination(rows, results.map(s => s.id).join());
  const summaries = results.map(s => s.data.summary);
  const complete = summaries.length > 0 && summaries.every(Boolean);
  const total = complete ? summaries.reduce((n, s) => n + s!.total, 0) : null;
  const errors = complete && summaries.every(s => s?.error_count !== undefined) ? summaries.reduce((n, s) => n + s!.error_count!, 0) : null;
  const bucketMap = new Map<number, number>();
  for (const summary of summaries) for (const b of summary?.buckets || []) {
    const t = new Date(b.timestamp).getTime() / 1000;
    if (Number.isFinite(t)) bucketMap.set(t, (bucketMap.get(t) || 0) + b.count);
  }
  const buckets = [...bucketMap].sort(([a], [b]) => a - b).map(([timestamp, count]) => ({ timestamp, count }));
  const info = selected ? logInfo(selected.entry) : null;
  const nearby = selected ? rows.filter(r => r.snapshot.applicationId === selected.snapshot.applicationId && r !== selected).sort((a, b) => Math.abs(new Date(a.entry.timestamp).getTime() - new Date(selected.entry.timestamp).getTime()) - Math.abs(new Date(b.entry.timestamp).getTime() - new Date(selected.entry.timestamp).getTime())).slice(0, 6).sort((a, b) => a.entry.timestamp.localeCompare(b.entry.timestamp)) : [];
  return <>
    <section className="obs-chart-card obs-log-overview"><header><div><h2>日志时间分布</h2><p>{complete ? "所选时间窗口内的匹配日志" : "部分数据源暂未返回窗口统计"}</p></div><div className="obs-log-count"><span>日志总量 <strong>{total === null ? "—" : compact(total)}</strong></span><span>错误日志 <strong className={errors ? "obs-error-text" : ""}>{errors === null ? "—" : compact(errors)}</strong></span></div></header>{complete && buckets.length ? <div style={{ height: 145 }} role="img" aria-label="日志数量随时间分布"><ResponsiveContainer width="100%" height="100%"><BarChart data={buckets} margin={{ top: 8, right: 10, bottom: 0, left: 0 }}><CartesianGrid vertical={false} stroke="var(--border)" strokeDasharray="3 3" /><XAxis dataKey="timestamp" tickFormatter={clock} minTickGap={55} axisLine={false} tickLine={false} tick={{ fill: "var(--muted)", fontSize: 11 }} /><YAxis width={42} tickFormatter={compact} axisLine={false} tickLine={false} tick={{ fill: "var(--muted)", fontSize: 11 }} /><Tooltip contentStyle={tipStyle} labelFormatter={v => clock(Number(v))} /><Bar dataKey="count" name="日志条数" fill="var(--accent)" radius={[2, 2, 0, 0]} isAnimationActive={false} /></BarChart></ResponsiveContainer></div> : <div className="obs-chart-empty">{complete ? "这个时间段没有匹配日志" : "日志记录仍可查看，统计暂时不可用"}</div>}</section>
    <section className="obs-log-table-card"><header><h2>日志记录</h2><span>最新优先 · 每个环境最多载入 100 条{total !== null && total > rows.length ? ` · 窗口内共 ${total} 条` : ""}</span></header><div className="management-table-wrap"><table className="management-table ui-data-table obs-log-table"><thead><tr><th>时间</th><th>级别</th><th>服务</th><th>内容</th></tr></thead><tbody>{pages.items.map(row => { const info = logInfo(row.entry); return <tr key={`${row.snapshot.id}-${row.index}`}><td>{new Date(row.entry.timestamp).toLocaleTimeString("zh-CN", { hour12: false })}</td><td><span className={`obs-level obs-level-${info.severity}`}>{info.severity === "unknown" ? "—" : info.severity.toUpperCase()}</span></td><td>{row.snapshot.applicationName}</td><td><button className="obs-log-message" onClick={() => setSelected(row)} title="打开日志详情">{info.message}</button>{info.trace && <small className="obs-trace-hint">Trace · {info.trace.slice(0, 12)}…</small>}</td></tr>; })}</tbody></table></div>{!rows.length && <p className="management-empty">这段时间没有匹配的日志。</p>}<Pagination page={pages.page} pageSize={pages.pageSize} total={pages.total} totalLabel={`已加载 ${pages.total} 条`} onPageChange={pages.setPage} onPageSizeChange={pages.setPageSize} /></section>
    <Sheet open={!!selected} onOpenChange={open => { if (!open) setSelected(null); }} title="日志详情" description={selected ? `${selected.snapshot.applicationName} · ${timeLabel(selected.entry.timestamp)}` : undefined} size="detail">{selected && info && <div className="obs-log-detail"><div className="obs-detail-links">{info.trace && <Link className="btn" to={`${link("traces", selected.snapshot)}&trace_id=${info.trace}`}>查看链路</Link>}<Link className="btn" to={link("metrics", selected.snapshot)}>查看指标</Link><button className="btn" disabled={!canTransfer} onClick={() => { onTransfer({ snapshot: selected.snapshot, selection: { kind: "log_entry", index: selected.index } }); setSelected(null); }}>带入调查</button></div><h3>日志内容</h3><pre>{selected.entry.line}</pre><h3>来源字段</h3><dl>{Object.entries(selected.entry.labels).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl>{nearby.length > 0 && <><h3>附近已加载的日志</h3><p className="muted">同一服务在当前结果中的相邻记录。</p>{nearby.map(row => <article key={row.index}><time>{timeLabel(row.entry.timestamp)}</time><pre>{row.entry.line}</pre></article>)}</>}</div>}</Sheet>
  </>;
}
