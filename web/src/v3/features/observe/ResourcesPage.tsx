import { useEffect, useMemo, useRef, useState } from "react";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { Topology, TopologyNode } from "../../api/types";
import { Avatar, Bar, Brand, Btn, Chip, Col, Dot, Icon, IconBtn, KV, Muted, Row, Section, Segmented, Skeleton, State, Strong, TextLink, Tile, cx } from "../../ui/core";
import { toast, MenuButton } from "../../ui/overlays";
import { Gauge, Lines, Sankey } from "../../charts";
import { useI18n } from "../../i18n";
import { DetailPanel, useFloatCover, usePinned } from "../../ui/DetailPanel";
import { useAskAi } from "../../app/askState";
import { ZoomControl, useViewport, type Bounds } from "../../ui/viewport";
import "./observe.css";
import { openExternal, ChipSelect, usePickLists } from "../../ui/demo";
import { useNavigate } from "react-router-dom";

const TONE_COLOR = { ok: "var(--c2)", warn: "var(--warn)", danger: "var(--danger)" };
const NODE_W = 172;
const NODE_H = 48;

/**
 * Edge routing from node positions (not precomputed paths): nodes in the same column connect vertically
 * (bottom of the upper one to the top of the lower one); otherwise right side to left side.
 */
function route(a: TopologyNode, b: TopologyNode) {
  if (Math.abs(a.x - b.x) < 20) {
    const [u, d] = a.y < b.y ? [a, b] : [b, a];
    const x = u.x + NODE_W / 2;
    const y1 = u.y + NODE_H;
    const y2 = d.y;
    return { d: `M${x} ${y1} L ${x} ${y2}`, lx: x + 22, ly: (y1 + y2) / 2 };
  }
  const [l, r] = a.x < b.x ? [a, b] : [b, a];
  const x1 = l.x + NODE_W;
  const y1 = l.y + NODE_H / 2;
  const x2 = r.x;
  const y2 = r.y + NODE_H / 2;
  const mx = (x1 + x2) / 2;
  return { d: `M${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`, lx: mx, ly: (y1 + y2) / 2 };
}

function boundsOf(t: Topology): Bounds {
  const xs = [...t.groups.flatMap((g) => [g.x, g.x + g.w]), ...t.nodes.flatMap((n) => [n.x, n.x + NODE_W])];
  const ys = [...t.groups.flatMap((g) => [g.y, g.y + g.h]), ...t.nodes.flatMap((n) => [n.y, n.y + NODE_H])];
  return { minX: Math.min(...xs), minY: Math.min(...ys), maxX: Math.max(...xs), maxY: Math.max(...ys) };
}

/** Rows whose ancestors are all expanded; `toggled` flips each row's default open state. */
function visibleRows(tree: Topology["tree"], toggled: Set<number>) {
  const out: { r: Topology["tree"][number]; idx: number }[] = [];
  let hideBelow: number | null = null;
  tree.forEach((r, idx) => {
    if (hideBelow !== null && r.depth > hideBelow) return;
    hideBelow = null;
    out.push({ r, idx });
    const expanded = r.open === undefined ? true : r.open !== toggled.has(idx);
    if (!expanded) hideBelow = r.depth;
  });
  return out;
}

export default function ResourcesPage() {
  const { tx } = useI18n();
  const { data: t } = useGet<Topology>("/topology");
  const [view_, setViewMode] = useState<"topo" | "tree" | "sankey">("topo");
  const [blast, setBlast] = useState(true);
  const [sel, setSel] = useState<string | null>(null);
  const lists = usePickLists();
  const [pinned, setPinned] = usePinned("topology");
  const box = useRef<HTMLDivElement>(null);
  const floating = !!sel && !pinned;
  const { view, setView, zoom, fitTo } = useViewport(box, view_ === "topo");
  const [animate, setAnimate] = useState(false);
  const drag = useRef<{ sx: number; sy: number; ox: number; oy: number; moved: boolean } | null>(null);
  const panned = useRef(false);
  const bounds = useMemo(() => (t ? boundsOf(t) : null), [t]);
  const byId = useMemo(() => Object.fromEntries((t?.nodes ?? []).map((n) => [n.id, n])), [t]);
  const fit = () => bounds && fitTo(bounds, { x: 40, top: 60, bottom: 60 });
  // Fit and centre once the graph is on screen.
  const fitted = useRef(false);
  useEffect(() => {
    if (!bounds || view_ !== "topo" || fitted.current) return;
    fitted.current = true;
    requestAnimationFrame(() => fit());
  }); // eslint-disable-line react-hooks/exhaustive-deps
  const floatCover = useFloatCover();
  const askAi = useAskAi((s) => s.open);
  const nav = useNavigate();
  // Slide the graph (never the legend or the page) just enough to keep the selected node clear of the card.
  useEffect(() => {
    if (!floating || !box.current) return;
    const el = box.current.querySelector<HTMLElement>(`[data-node="${CSS.escape(sel!)}"]`);
    if (!el) return;
    const b = box.current.getBoundingClientRect();
    const n = el.getBoundingClientRect();
    const overlap = n.right - (b.right - floatCover) + 16;
    if (overlap <= 0) return;
    setAnimate(true);
    setView((v) => ({ ...v, x: v.x - overlap }));
    const id = setTimeout(() => setAnimate(false), 320);
    return () => clearTimeout(id);
  }, [sel, floating, floatCover]); // eslint-disable-line react-hooks/exhaustive-deps
  const [hover, setHover] = useState<string | null>(null);
  const [open, setOpen] = useState<Set<number>>(new Set());
  const detail = t && sel ? (
    <DetailPanel
      pinned={pinned}
      onPin={setPinned}
      onClose={() => setSel(null)}
      label={tx("资源详情", "Resource details")}
      header={
            <>
              <Brand name={t.detail.brand} size={30} />
              <Col gap={0} grow>
                <Strong size={14}>{sel === "orders-db" ? t.detail.name : sel}</Strong>
                <Muted size={11.5}>{sel === "orders-db" ? t.detail.meta : t.nodes.find((n) => n.id === sel)?.sub}</Muted>
              </Col>
            </>
      }
    >
            <Col gap={16} style={{ padding: 16 }} className="anim-fade" key={sel}>
              <Row gap={12}>
                <Gauge pct={t.detail.conns.used / t.detail.conns.max} size={138} tone="ok" label={`${Math.round((100 * t.detail.conns.used) / t.detail.conns.max)}%`} sub={tx("连接 {a} / {b}", "conns {a} / {b}", { a: t.detail.conns.used, b: t.detail.conns.max })} />
                <Gauge pct={1} size={138} tone="danger" label="100%" sub={`${t.detail.from_service.service} ${t.detail.from_service.used} / ${t.detail.from_service.max}`} />
              </Row>
              <Section title={tx("连接等待 P95", "Connection wait P95")} right={<span className="mono" style={{ color: "var(--danger)", fontSize: "var(--fs-sub)" }}>420 ms</span>}>
                <Lines series={[{ name: "P95", values: t.detail.wait, color: "var(--c3)", fill: "var(--c3s)" }]} height={52} max={520} grid={2} markers={[{ at: 16, label: tx("发布", "release"), tone: "warn" }]} format={(v) => `${Math.round(v)} ms`} />
              </Section>
              <Section title={tx("健康", "Health")}>
                {t.detail.health.map((h) => (
                  <KV key={h.k} k={h.k} kw={76}>
                    <span className="tnum">{h.v}</span>
                  </KV>
                ))}
              </Section>
              <Section title={tx("归属", "Ownership")}>
                <KV k={tx("负责人", "Owner")} kw={76}>
                  <Row gap={6}>
                    <Avatar name={t.detail.owner} size={20} />
                    {t.detail.owner}
                  </Row>
                </KV>
                <KV k={tx("被依赖", "Dependents")} kw={76}>{t.detail.dependents}</KV>
                <KV k={tx("来源", "Source")} kw={76}>
                  <Row gap={6}>
                    <Brand name="kubernetes" size={18} />
                    <Muted size={11.5}>{t.detail.source}</Muted>
                  </Row>
                </KV>
              </Section>
              <div className="v3-grid2">
                <Btn icon="activity" to="/metrics">{tx("查看指标", "Metrics")}</Btn>
                <Btn icon="pin" to="/canvas/INV-231">{tx("钉到调查", "Pin to investigation")}</Btn>
                <Btn icon="external" onClick={() => openExternal("Grafana", `https://grafana.example.internal/d/k8s-workload?var-workload=${sel ?? ""}`)}>Grafana</Btn>
                <Btn kind="soft" icon="sparkles" onClick={() => askAi(tx("{n} 现在健康吗？", "Is {n} healthy right now?", { n: sel ?? "" }))}>{tx("问 AI", "Ask AI")}</Btn>
              </div>
            </Col>
    </DetailPanel>
  ) : null;
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("资源与拓扑", "Topology")]}
      actions={
        <>
          <ChipSelect icon="clock" label="" showLabel={false} defaultValue="1431" options={[{ value: "1431", label: tx("快照 14:31 · 最新", "Snapshot 14:31 · latest") }, { value: "1400", label: tx("快照 14:00 · 发布前", "Snapshot 14:00 · before release") }, { value: "y", label: tx("快照 昨天 14:30", "Snapshot yesterday 14:30") }]} />
          <Btn icon="rotate" onClick={() => toast(tx("已刷新拓扑快照", "Topology snapshot refreshed"))}>
            {tx("刷新快照", "Refresh")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main" style={{ display: "flex", flexDirection: "column", padding: "14px 16px", gap: 10, overflow: "hidden" }}>
          <Row gap={8}>
            <Segmented label={tx("视图", "View")} value={view_} onChange={setViewMode} options={[{ value: "topo", label: tx("拓扑", "Topology") }, { value: "tree", label: tx("层级", "Hierarchy") }, { value: "sankey", label: tx("桑基", "Sankey") }]} />
            <ChipSelect icon="grid" label={tx("集群", "Cluster")} defaultValue="prod-sh-1" options={["prod-sh-1", "prod-bj-1"]} />
            <ChipSelect icon="box" label={tx("围绕", "Around")} defaultValue="checkout-api" options={lists.services.length ? lists.services : ["checkout-api"]} />
            <ChipSelect label={tx("深度", "Depth")} defaultValue="2" options={["1", "2", "3"]} />
            <ChipSelect icon="layers" label="" showLabel={false} defaultValue="zone" options={[{ value: "zone", label: tx("按可用区分组", "Group by zone") }, { value: "ns", label: tx("按命名空间分组", "Group by namespace") }, { value: "team", label: tx("按团队分组", "Group by team") }]} />
            <span style={{ flexGrow: 1 }} />
            <Chip icon="alert" active={blast} onClick={() => setBlast((b) => !b)}>
              {tx("影响范围", "Blast radius")}
            </Chip>
          </Row>
          <div className="topo-stage">
          <div
            className={cx("topo-box", view_ === "topo" && "pan")}
            ref={box}
            onPointerDown={(e) => {
              if (e.button !== 0 || view_ !== "topo" || (e.target as HTMLElement).closest(".topo-node")) return;
              drag.current = { sx: e.clientX, sy: e.clientY, ox: view.x, oy: view.y, moved: false };
              e.currentTarget.setPointerCapture(e.pointerId);
            }}
            onPointerMove={(e) => {
              const d = drag.current;
              if (!d) return;
              const dx = e.clientX - d.sx;
              const dy = e.clientY - d.sy;
              if (Math.abs(dx) + Math.abs(dy) > 2) d.moved = true;
              setView((v) => ({ ...v, x: d.ox + dx, y: d.oy + dy }));
            }}
            onPointerUp={() => {
              panned.current = !!drag.current?.moved;
              drag.current = null;
            }}
            onClick={(e) => {
              if (!panned.current && !(e.target as HTMLElement).closest(".topo-node")) setSel(null);
            }}
          >
            {!t && <Skeleton h={600} style={{ margin: 20 }} />}
            {t && view_ === "topo" && (
              <div className="topo-canvas" style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})`, transition: animate ? "transform 280ms var(--ease)" : "none" }}>
                {t.groups.map((g) => (
                  <div key={g.id} className="topo-group" style={{ left: g.x, top: g.y, width: g.w, height: g.h }}>
                    <span>
                      <Icon name="layers" size={12} />
                      <b>{g.label}</b>
                      {g.zone}
                    </span>
                  </div>
                ))}
                <svg className="topo-edges" width="880" height="760" aria-hidden="true">
                  {t.edges.map((e) => (
                    <path
                      key={e.id}
                      d={byId[e.from] && byId[e.to] ? route(byId[e.from], byId[e.to]).d : e.path}
                      className={e.fast ? "flow-fast" : "flow"}
                      style={{ fill: "none", stroke: TONE_COLOR[e.tone], strokeWidth: e.width, strokeLinecap: "round", opacity: hover && e.from !== hover && e.to !== hover ? 0.25 : 1, transition: "opacity 150ms" }}
                    />
                  ))}
                </svg>
                {t.edges.map((e) =>
                  e.label ? (
                    <span key={e.id} className="topo-elabel tnum" style={{ left: byId[e.from] && byId[e.to] ? route(byId[e.from], byId[e.to]).lx : e.lx, top: byId[e.from] && byId[e.to] ? route(byId[e.from], byId[e.to]).ly : e.ly, color: e.tone === "ok" ? "var(--muted)" : TONE_COLOR[e.tone] }}>
                      {e.label}
                    </span>
                  ) : null,
                )}
                {t.nodes.map((n) => (
                  <button
                    key={n.id}
                    type="button"
                    className={cx("topo-node lift", sel === n.id && "sel", n.status === "danger" && "bad", blast && n.impacted && "hit", blast && !n.impacted && "dim")}
                    style={{ left: n.x, top: n.y }}
                    data-node={n.id}
                    onClick={() => setSel(n.id)}
                    onMouseEnter={() => setHover(n.id)}
                    onMouseLeave={() => setHover(null)}
                  >
                    {n.brand ? <Brand name={n.brand} size={26} /> : <Tile icon="box" size={26} />}
                    <Col gap={0} grow>
                      <span className="ellipsis" style={{ fontSize: "var(--fs-sm)", fontWeight: 600 }}>{n.name}</span>
                      <span className="ellipsis muted" style={{ fontSize: "var(--fs-cap)" }}>{n.sub}</span>
                    </Col>
                    <Dot tone={n.status} size={8} pulse={n.status === "danger"} />
                  </button>
                ))}
              </div>
            )}
            {t && view_ === "tree" && (
              <div className="topo-tree anim-fade">
                <div className="tree-row head">
                  <span>{tx("名称", "Name")}</span>
                  <span>{tx("状态", "Status")}</span>
                  <span>{tx("副本", "Replicas")}</span>
                  <span>CPU</span>
                  <span>{tx("内存", "Memory")}</span>
                  <span>{tx("告警", "Alerts")}</span>
                </div>
                {visibleRows(t.tree, open)
                  .map(({ r, idx }) => {
                    const expanded = r.open !== undefined ? (r.open ? !open.has(idx) : open.has(idx)) : undefined;
                    return (
                      <div key={r.name} className={cx("tree-row hrow", r.hit && "hit")}>
                        <span className="v3-row" style={{ gap: 6, paddingLeft: r.depth * 18, minWidth: 0 }}>
                          {expanded !== undefined ? (
                            <IconBtn icon={expanded ? "down" : "right"} label="toggle" size={20} onClick={() => setOpen((s) => { const n = new Set(s); if (n.has(idx)) n.delete(idx); else n.add(idx); return n; })} />
                          ) : (
                            <span style={{ width: 20 }} />
                          )}
                          {r.brand ? <Brand name={r.brand} size={20} /> : <Tile icon={r.depth === 1 ? "layers" : "box"} size={20} />}
                          <span className="ellipsis" style={{ fontWeight: r.depth < 2 ? 600 : 400 }}>{r.name}</span>
                          <Muted size={11}>{r.kind}</Muted>
                        </span>
                        <span>
                          <State tone={r.status}>{r.status === "danger" ? tx("异常", "Degraded") : r.status === "warn" ? tx("待定", "Warning") : tx("正常", "Healthy")}</State>
                        </span>
                        <span className="tnum">{r.replicas}</span>
                        <span className="v3-row" style={{ gap: 4 }}>{r.cpu !== undefined && <><Bar pct={r.cpu} tone={r.cpu > 80 ? "danger" : r.cpu > 65 ? "warn" : "ok"} width={70} /><Muted size={11.5}>{r.cpu}%</Muted></>}</span>
                        <span className="v3-row" style={{ gap: 4 }}>{r.mem !== undefined && <><Bar pct={r.mem} tone={r.mem > 75 ? "warn" : "ok"} width={70} /><Muted size={11.5}>{r.mem}%</Muted></>}</span>
                        <span className="v3-row" style={{ gap: 4 }}>{r.alerts !== undefined && <span className={`v3-badge mono t-${r.alerts ? "danger" : "neutral"}`}>{r.alerts}</span>}<span className="hact" onClick={(ev) => ev.stopPropagation()}><MenuButton trigger={({ onClick, ref }) => <span ref={ref}><IconBtn icon="more" label={tx("更多操作", "More")} size={22} onClick={onClick} /></span>} items={[{ label: tx("查看指标", "Metrics"), icon: "activity", onSelect: () => nav(`/metrics?service=${r.name}`) }, { label: tx("问 AI", "Ask AI"), icon: "sparkles", onSelect: () => askAi(tx("{n} 现在健康吗？", "Is {n} healthy right now?", { n: r.name })) }, { label: tx("在 Grafana 中打开", "Open in Grafana"), icon: "external", onSelect: () => openExternal("Grafana", `https://grafana.example.internal/d/k8s-workload?var-workload=${r.name}`) }]} /></span></span>
                      </div>
                    );
                  })}
              </div>
            )}
            {t && view_ === "sankey" && (
              <div className="anim-fade" style={{ padding: "24px 48px", display: "flex", flexDirection: "column", gap: 14, background: "var(--panel)", height: "100%" }}>
                <Sankey cols={t.sankey.cols} links={t.sankey.links} height={520} />
                <Muted size={11.5}>{tx("线宽 = 请求量；红色为异常流量。checkout-api 流向 orders-db 的 500 qps 中 8.4% 以超时结束。", "Width = volume; red is unhealthy. 8.4% of the 500 qps from checkout-api to orders-db end in timeouts.")}</Muted>
              </div>
            )}
          </div>
          {t && view_ === "topo" && (
            <>
              <ZoomControl k={view.k} onZoom={(f) => zoom(f)} onReset={() => zoom(1 / view.k)} onFit={fit} />
              <div className={cx("blast-chip", blast && "show")}>
                <Icon name="alert" size={14} color="var(--danger)" />
                <b>{tx("影响范围", "Blast radius")}</b>
                {t.blast}
                <TextLink to="/canvas/INV-231">{tx("加入调查", "Add to investigation")}</TextLink>
              </div>
              <div className="topo-legend">
                <Row gap={6}>
                  <svg width="28" height="6" aria-hidden="true">
                    <path className="flow" d="M0 3 L28 3" style={{ stroke: "var(--c2)", strokeWidth: 2 }} />
                  </svg>
                  {tx("正常流量（速度 ≈ 请求量）", "Normal traffic (speed ≈ volume)")}
                </Row>
                <Row gap={6}>
                  <svg width="28" height="6" aria-hidden="true">
                    <path className="flow-fast" d="M0 3 L28 3" style={{ stroke: "var(--danger)", strokeWidth: 2 }} />
                  </svg>
                  {tx("异常链路", "Unhealthy path")}
                </Row>
                <Row gap={8}>
                  <span style={{ width: 26, height: 12, borderRadius: 4, border: "1px dashed var(--border-strong)" }} />
                  {tx("命名空间 / 可用区", "Namespace / zone")}
                </Row>
              </div>
            </>
          )}
          {t && floating && detail}
          </div>
        </div>
        {t && sel && pinned && detail}
      </div>
    </Page>
  );
}
