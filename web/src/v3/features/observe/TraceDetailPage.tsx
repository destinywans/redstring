import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { TraceSpan, TraceView } from "../../api/types";
import { Badge, Brand, Btn, Code, Icon, IconBtn, Legend, Mono, Muted, Num, Row, SearchBox, Segmented, Skeleton, Spacer, Strong, Tabs, Tile, Toggle, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { ScopeSwitch, fmtMs } from "./TracesPage";
import { DetailPanel, usePinned } from "../../ui/DetailPanel";
import "./trace.css";

type View = "wf" | "flame" | "graph";

/** Application trace detail: waterfall / flame / service graph, critical path, span attributes, events, logs and links. */
export default function TraceDetailPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { id = "4bf92f35" } = useParams();
  const { data: t } = useGet<TraceView>(`/traces/${id}`);
  const [view, setView] = useState<View>("wf");
  const [sel, setSel] = useState<string | null>(null);
  const [pinned, setPinned] = usePinned("trace-detail");
  const [crit, setCrit] = useState(true);
  const [q, setQ] = useState("");
  const [tab, setTab] = useState<"attrs" | "events" | "logs" | "links">("attrs");
  const span = t?.spans.find((s) => s.id === sel);
  const total = t?.duration_ms ?? 1;
  const services = useMemo(() => [...new Set(t?.spans.map((s) => s.service) ?? [])], [t]);
  const errors = t?.spans.filter((s) => s.status === "error").length ?? 0;
  const color = (s: TraceSpan) => (s.status === "error" ? "var(--danger)" : t?.colors[s.service] ?? "var(--c1)");
  const match = (s: TraceSpan) => !q || `${s.service} ${s.op}`.toLowerCase().includes(q.toLowerCase());
  const hasChild = (i: number) => !!t && i + 1 < t.spans.length && t.spans[i + 1].depth > t.spans[i].depth;
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => fmtMs(total * f));

  const spanPanel = span && t ? (
    <DetailPanel pinned={pinned} onPin={setPinned} onClose={() => setSel(null)} width={380} label={tx("Span 详情", "Span details")} header={
                <div className="v3-col" style={{ gap: 4, minWidth: 0 }}>
                  <Row gap={8}>
                    <i className="tr-sw" style={{ width: 10, height: 10, background: t.colors[span.service] }} />
                    <Strong size={14}>{span.op}</Strong>
                    {span.status === "error" && <Badge tone="danger">{tx("错误", "Error")}</Badge>}
                  </Row>
                  <Muted size={11.5}>
                    {tx("{s} · {d} · 占总耗时 {p}%", "{s} · {d} · {p}% of total", { s: span.service, d: fmtMs(span.end - span.start), p: Math.round((100 * (span.end - span.start)) / total) })}
                    {span.critical && tx(" · 关键路径", " · critical path")}
                  </Muted>
                </div>
    }>
                <Tabs
                  value={tab}
                  onChange={setTab}
                  pad="6px 16px 0"
                  options={[
                    { value: "attrs", label: tx("属性", "Attributes") },
                    { value: "events", label: tx("事件", "Events"), count: span.events?.length },
                    { value: "logs", label: tx("日志", "Logs"), count: span.logs?.length },
                    { value: "links", label: tx("关联", "Links"), count: span.links?.length },
                  ]}
                />
                <div className="v3-col" style={{ gap: 12, padding: 16 }}>
                  {tab === "attrs" &&
                    (span.attrs?.length ? (
                      <div>
                        {span.attrs.map((a) => (
                          <div key={a.k} className={cx("td-attr", a.hot && "hot")}>
                            <span>{a.k}</span>
                            <span>{a.v}</span>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <Muted>{tx("这个 Span 没有额外属性", "No extra attributes on this span")}</Muted>
                    ))}
                  {tab === "events" && (span.events?.length ? span.events.map((e) => <Code key={e.at}>{`${e.at}  ${e.name}\n${e.text}`}</Code>) : <Muted>{tx("没有事件", "No events")}</Muted>)}
                  {tab === "logs" &&
                    (span.logs?.length ? (
                      span.logs.map((l) => (
                        <div key={l.time + l.message} className="td-log">
                          <span className="muted">{l.time.slice(-12)}</span>
                          <span style={{ color: l.level === "ERROR" ? "var(--danger)" : l.level === "WARN" ? "var(--warn)" : "var(--muted)" }}>{l.level}</span>
                          <span>{l.message}</span>
                        </div>
                      ))
                    ) : (
                      <Muted>{tx("没有关联日志", "No correlated logs")}</Muted>
                    ))}
                  {tab === "links" &&
                    (span.links?.length ? (
                      span.links.map((k) => (
                        <Row key={k.label} gap={6}>
                          {k.brand ? <Brand name={k.brand} size={18} /> : <Tile icon={k.kind === "metric" ? "activity" : "book"} tone={k.kind === "runbook" ? "t1" : "neutral"} size={18} />}
                          <Link className="v3-link" to={k.href}>{k.label}</Link>
                        </Row>
                      ))
                    ) : (
                      <Muted>{tx("没有关联对象", "No links")}</Muted>
                    ))}
                  <Row gap={6}>
                    <Btn h={26} icon="pin" onClick={() => toast(tx("已钉到画布 INV-231", "Pinned to canvas INV-231"))}>{tx("钉到画布", "Pin to canvas")}</Btn>
                    <Btn h={26} kind="soft" icon="branch" to="/canvas/INV-231">{tx("带入调查", "Bring into investigation")}</Btn>
                  </Row>
                </div>
    </DetailPanel>
  ) : null;
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("链路", "Traces"), `${id}…`]}
      actions={
        <>
          <ScopeSwitch scope="app" onChange={(s) => s === "agent" && nav("/traces?scope=agent")} />
          <Btn icon="layers" onClick={() => toast(tx("已与同接口 v2.8.0 基线链路对比：db.pool.acquire +2.97 s", "Compared with the v2.8.0 baseline: db.pool.acquire +2.97 s"))}>{tx("与基线对比", "Compare with baseline")}</Btn>
          <Btn kind="ghost" icon="file" to="/logs">{tx("相关日志", "Related logs")}</Btn>
          <Btn kind="ghost" icon="pin" onClick={() => toast(tx("已钉到画布 INV-231", "Pinned to canvas INV-231"))}>{tx("钉到画布", "Pin to canvas")}</Btn>
          <Btn kind="ghost" icon="external">{tx("在 Jaeger 中打开", "Open in Jaeger")}</Btn>
        </>
      }
    >
      {!t ? (
        <div style={{ padding: 24 }}>
          <Skeleton h={60} />
          <Skeleton h={320} style={{ marginTop: 12 }} />
        </div>
      ) : (
        <>
          <div className="td-head">
            <Row gap={8} wrap>
              <b className="mono" style={{ fontSize: 15 }}>{t.op}</b>
              <Num color="var(--danger)">{fmtMs(t.duration_ms)}</Num>
              <Badge>{tx("{n} 个 Span", "{n} spans", { n: t.spans.length })}</Badge>
              <Badge>{tx("{n} 个服务", "{n} services", { n: services.length })}</Badge>
              {errors > 0 && <Badge tone="danger">{tx("{n} 个错误", "{n} errors", { n: errors })}</Badge>}
              <Badge mono>{t.version}</Badge>
              <Spacer />
              <Mono size={11.5} color="var(--muted)">trace {t.trace_id}</Mono>
            </Row>
            <Row gap={12} wrap>
              {services.map((s) => (
                <Row key={s} gap={5}>
                  <i className="tr-sw" style={{ background: t.colors[s] }} />
                  <Muted size={11.5}>{s}</Muted>
                </Row>
              ))}
              <Spacer />
              <Muted size={11.5}>{tx("开始 {t}", "Started {t}", { t: t.time })}</Muted>
            </Row>
          </div>
          <div className="td-body">
            <div
              className="td-main"
              onClick={(e) => {
                if (!(e.target as HTMLElement).closest(".td-row, .td-flame-row button, .td-gnode, .td-bar")) setSel(null);
              }}
            >
              <div className="td-bar">
                <Segmented label={tx("视图", "View")} value={view} onChange={setView} options={[{ value: "wf", label: tx("瀑布图", "Waterfall") }, { value: "flame", label: tx("火焰图", "Flame graph") }, { value: "graph", label: tx("服务图", "Service graph") }]} />
                <SearchBox placeholder={tx("在链路中搜索", "Search in trace")} value={q} onChange={setQ} width={200} />
                <Row gap={6}>
                  <Toggle on={crit} onChange={setCrit} label={tx("高亮关键路径", "Highlight critical path")} />
                  <Muted size={11.5}>{tx("高亮关键路径", "Critical path")}</Muted>
                </Row>
                <Spacer />
                <Legend items={[[tx("关键路径", "Critical path"), "var(--text)"], [tx("错误", "Error"), "var(--danger)"]]} />
              </div>
              {view === "wf" && (
                <div className="anim-fade">
                  <div className="td-mini" aria-hidden="true">
                    {t.spans.map((s, i) => (
                      <span key={s.id} style={{ left: `${(100 * s.start) / total}%`, width: `${Math.max(0.6, (100 * (s.end - s.start)) / total)}%`, top: 3 + (i % 6) * 3.6, background: color(s) }} />
                    ))}
                  </div>
                  <div className="td-wf">
                    <div className="td-wf-head">
                      <span className="td-name"><Muted size={11}>{tx("服务 · 操作", "Service · operation")}</Muted></span>
                      <div className="td-axis" style={{ flex: 1, minWidth: 200 }}>
                        {ticks.map((x) => <span key={x}>{x}</span>)}
                      </div>
                      <span className="td-dur" />
                    </div>
                    {t.spans.map((s, i) => (
                      <button key={s.id} type="button" className={cx("td-row", sel === s.id && "on", !match(s) && "dim")} onClick={() => setSel(s.id)}>
                        <span className="td-name" style={{ paddingLeft: s.depth * 14 }}>
                          {hasChild(i) ? <Icon name="down" size={11} color="var(--muted)" /> : <span style={{ width: 11 }} />}
                          <i className="tr-sw" style={{ background: t.colors[s.service] }} />
                          <span className="ellipsis" style={{ fontWeight: s.status === "error" ? 600 : 400, color: s.status === "error" ? "var(--danger)" : undefined }}>
                            {s.op}
                          </span>
                          {s.label && <Muted size={11}>{s.label}</Muted>}
                        </span>
                        <span className="td-track">
                          <span className={cx(crit && s.critical && "crit")} style={{ left: `${(100 * s.start) / total}%`, width: `${Math.max(0.4, (100 * (s.end - s.start)) / total)}%`, background: color(s) }} />
                        </span>
                        <span className="td-dur" style={{ color: s.status === "error" ? "var(--danger)" : undefined }}>{fmtMs(s.end - s.start)}</span>
                      </button>
                    ))}
                  </div>
                </div>
              )}
              {view === "flame" && (
                <div className="td-flame anim-fade">
                  {[...new Set(t.spans.map((s) => s.depth))].map((d) => (
                    <div key={d} className="td-flame-row">
                      {t.spans
                        .filter((s) => s.depth === d)
                        .map((s) => (
                          <button
                            key={s.id}
                            type="button"
                            className={cx(s.status === "error" && "err")}
                            title={`${s.service} · ${s.op} · ${fmtMs(s.end - s.start)}`}
                            style={{ left: `${(100 * s.start) / total}%`, width: `${Math.max(0.5, (100 * (s.end - s.start)) / total)}%`, background: s.status === "error" ? "var(--danger)" : `color-mix(in srgb, ${t.colors[s.service]} 28%, var(--panel))`, outline: sel === s.id ? "2px solid var(--text)" : undefined }}
                            onClick={() => setSel(s.id)}
                          >
                            {s.service} · {s.op}
                          </button>
                        ))}
                    </div>
                  ))}
                  <Row gap={6} style={{ marginTop: 10 }}>
                    <Icon name="alert" size={13} color="var(--danger)" />
                    <Muted size={12}>{t.flame_note}</Muted>
                  </Row>
                </div>
              )}
              {view === "graph" && (
                <div className="td-graph anim-fade">
                  <svg width="700" height="330" viewBox="0 0 700 330" aria-hidden="true" style={{ position: "absolute", inset: 0 }}>
                    {t.graph.edges.map((e) => {
                      const a = t.graph.nodes.find((n) => n.id === e.from)!;
                      const b = t.graph.nodes.find((n) => n.id === e.to)!;
                      const x1 = a.x + 150;
                      const y1 = a.y + 25;
                      const x2 = b.x;
                      const y2 = b.y + 25;
                      return (
                        <g key={e.from + e.to}>
                          <path d={`M${x1} ${y1} C ${x1 + 40} ${y1}, ${x2 - 40} ${y2}, ${x2} ${y2}`} fill="none" stroke={e.danger ? "var(--danger)" : "var(--border-strong)"} strokeWidth={e.danger ? 2 : 1.2} className={cx(e.danger && "flow")} />
                          <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 6} textAnchor="middle" fontSize={11} fill={e.danger ? "var(--danger)" : "var(--muted)"}>
                            {e.label}
                          </text>
                        </g>
                      );
                    })}
                  </svg>
                  {t.graph.nodes.map((n) => (
                    <div key={n.id} className={cx("td-gnode", n.danger && "danger")} style={{ left: n.x, top: n.y }}>
                      <i className="tr-sw" style={{ width: 10, height: 10, background: t.colors[n.id] }} />
                      <span className="v3-col" style={{ gap: 0, minWidth: 0 }}>
                        <b className="ellipsis" style={{ fontSize: "var(--fs-sub)" }}>{n.id}</b>
                        <Muted size={10.5}>{n.sub}</Muted>
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
            {/* docked when pinned; otherwise floats over the waterfall (td-body is the positioning context) */}
            {spanPanel}
          </div>
        </>
      )}
    </Page>
  );
}
