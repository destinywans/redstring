import { useEffect, useMemo, useRef, useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, useGet } from "../../api/client";
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
import { Unavailable, UnknownValue } from "../../ui/unknown";
import { CollectionBanner, CollectionEmpty, useStaleCollectors } from "./CollectionNotice";

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

type RawTopology = Omit<Topology, "nodes" | "edges" | "groups" | "tree" | "sankey" | "blast" | "detail"> & {
  nodes: (Omit<TopologyNode, "x" | "y" | "sub" | "status"> & Partial<Pick<TopologyNode, "x" | "y" | "sub" | "status">>)[];
  edges: (Omit<Topology["edges"][number], "tone" | "width"> & Partial<Pick<Topology["edges"][number], "tone" | "width">>)[];
  groups?: Topology["groups"];
  tree?: Topology["tree"];
  sankey?: Topology["sankey"];
  blast?: Topology["blast"];
  detail?: Topology["detail"];
};

// Columns for the client-side layout of the real CMDB graph (it carries no coordinates).
const KIND_ORDER = ["cluster", "node", "namespace", "network", "deployment"];

/**
 * The real /topology returns nodes and relations only; layout, health, groups, the resource tree,
 * the traffic Sankey, blast radius and the detail card are unavailable (unavailable_fields).
 * Lay nodes out in columns by kind and derive the tree from `belongs_to` relations; never invent
 * health or impact.
 */
function normalizeTopology(raw: RawTopology, kindLabel: (k?: string) => string): Topology {
  const positioned = raw.nodes.every((n) => typeof n.x === "number" && typeof n.y === "number");
  let nodes: TopologyNode[];
  if (positioned) nodes = raw.nodes as TopologyNode[];
  else {
    const col = (k?: string) => (KIND_ORDER.indexOf(k ?? "") >= 0 ? KIND_ORDER.indexOf(k ?? "") : KIND_ORDER.length);
    const byCol = new Map<number, RawTopology["nodes"]>();
    for (const n of raw.nodes) byCol.set(col(n.kind), [...(byCol.get(col(n.kind)) ?? []), n]);
    const cols = [...byCol.keys()].sort((a, b) => a - b);
    nodes = cols.flatMap((c, ci) =>
      (byCol.get(c) ?? [])
        .slice()
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((n, ri) => ({ ...n, x: 40 + ci * (NODE_W + 96), y: 40 + ri * (NODE_H + 18), sub: n.sub ?? kindLabel(n.kind), status: n.status ?? null })),
    );
  }
  const edges = raw.edges.map((e) => ({ ...e, tone: e.tone ?? null, width: e.width ?? 1.4 }));
  let tree = raw.tree;
  if (!tree) {
    const parentOf = new Map<string, string>();
    for (const e of raw.edges) if (e.kind === "belongs_to" && !parentOf.has(e.from)) parentOf.set(e.from, e.to);
    const children = new Map<string, TopologyNode[]>();
    for (const n of nodes) {
      const p = parentOf.get(n.id) ?? "";
      children.set(p, [...(children.get(p) ?? []), n]);
    }
    const rows: Topology["tree"] = [];
    const seen = new Set<string>();
    const walk = (parent: string, depth: number) => {
      for (const n of (children.get(parent) ?? []).slice().sort((a, b) => col2(a) - col2(b) || a.name.localeCompare(b.name))) {
        if (seen.has(n.id)) continue;
        seen.add(n.id);
        const kids = children.get(n.id)?.length ?? 0;
        rows.push({ depth, kind: kindLabel(n.kind), name: n.name, status: null, replicas: "", ...(kids ? { open: true } : {}) });
        walk(n.id, depth + 1);
      }
    };
    const col2 = (n: TopologyNode) => KIND_ORDER.indexOf(n.kind ?? "");
    walk("", 0);
    tree = rows;
  }
  return { ...raw, nodes, edges, groups: raw.groups ?? [], tree, sankey: raw.sankey ?? null, blast: raw.blast ?? null, detail: raw.detail ?? null };
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
  // While a collector is behind, check again every minute so the notice clears (and the minutes tick) on its own.
  const { data: rawTopology, refetch: topoRefetch, error: topoError } = useGet<RawTopology>("/topology", undefined, {
    refetchInterval: (q) => ((q.state.data?.collection ?? []).some((c) => c.state !== "fresh") ? 60_000 : false),
  });
  const stale = useStaleCollectors(rawTopology?.collection);
  const empty = !!rawTopology && rawTopology.nodes.length === 0;
  const kindLabel = (k?: string) =>
    ({ cluster: tx("集群", "Cluster"), node: tx("节点", "Node"), namespace: tx("命名空间", "Namespace"), network: tx("服务网络", "Service"), deployment: tx("工作负载", "Workload") })[k ?? ""] ?? (k || tx("资源", "Resource"));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const t = useMemo(() => (rawTopology ? normalizeTopology(rawTopology, kindLabel) : undefined), [rawTopology, tx]);
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
  const node = t && sel ? byId[sel] : undefined;
  const d = t?.detail ?? null;
  const detail = t && sel ? (
    <DetailPanel
      pinned={pinned}
      onPin={setPinned}
      onClose={() => setSel(null)}
      label={tx("资源详情", "Resource details")}
      header={
        d ? (
            <>
              <Brand name={d!.brand} size={30} />
              <Col gap={0} grow>
                <Strong size={14}>{sel === "orders-db" ? d!.name : sel}</Strong>
                <Muted size={11.5}>{sel === "orders-db" ? d!.meta : t.nodes.find((n) => n.id === sel)?.sub}</Muted>
              </Col>
            </>
        ) : (
          <>
            <Tile icon="box" size={30} />
            <Col gap={0} grow>
              <Strong size={14}>{node?.name ?? sel}</Strong>
              <Muted size={11.5}>{node?.sub}</Muted>
            </Col>
          </>
        )
      }
    >
      {d ? (
            <Col gap={16} style={{ padding: 16 }} className="anim-fade" key={sel}>
              <Row gap={12}>
                <Gauge pct={d!.conns.used / d!.conns.max} size={138} tone="ok" label={`${Math.round((100 * d!.conns.used) / d!.conns.max)}%`} sub={tx("连接 {a} / {b}", "conns {a} / {b}", { a: d!.conns.used, b: d!.conns.max })} />
                <Gauge pct={d!.from_service.used / d!.from_service.max} size={138} tone={d!.from_service.used >= d!.from_service.max * 0.9 ? "danger" : "ok"} label={`${Math.round((100 * d!.from_service.used) / d!.from_service.max)}%`} sub={`${d!.from_service.service} ${d!.from_service.used} / ${d!.from_service.max}`} />
              </Row>
              <Section title={tx("连接等待 P95", "Connection wait P95")} right={<span className="mono" style={{ fontSize: "var(--fs-sub)" }}>{Math.round(d!.wait[d!.wait.length - 1])} ms</span>}>
                <Lines series={[{ name: "P95", values: d!.wait, color: "var(--c3)", fill: "var(--c3s)" }]} height={52} max={Math.max(...d!.wait) * 1.25} grid={2} markers={[{ at: 16, label: tx("发布", "release"), tone: "warn" }]} format={(v) => `${Math.round(v)} ms`} />
              </Section>
              <Section title={tx("健康", "Health")}>
                {d!.health.map((h) => (
                  <KV key={h.k} k={h.k} kw={76}>
                    <span className="tnum">{h.v}</span>
                  </KV>
                ))}
              </Section>
              <Section title={tx("归属", "Ownership")}>
                <KV k={tx("负责人", "Owner")} kw={76}>
                  <Row gap={6}>
                    <Avatar name={d!.owner} size={20} />
                    {d!.owner}
                  </Row>
                </KV>
                <KV k={tx("被依赖", "Dependents")} kw={76}>{d!.dependents}</KV>
                <KV k={tx("来源", "Source")} kw={76}>
                  <Row gap={6}>
                    <Brand name="kubernetes" size={18} />
                    <Muted size={11.5}>{d!.source}</Muted>
                  </Row>
                </KV>
              </Section>
              <div className="v3-grid2">
                <Btn icon="activity" to="/metrics">{tx("查看指标", "Metrics")}</Btn>
                <Btn kind="soft" icon="sparkles" onClick={() => askAi(tx("{n} 现在健康吗？", "Is {n} healthy right now?", { n: sel ?? "" }))}>{tx("问 AI", "Ask AI")}</Btn>
              </div>
            </Col>
      ) : (
        <Col gap={16} style={{ padding: 16 }} className="anim-fade" key={sel}>
          <Section title={tx("资源", "Resource")}>
            <KV k={tx("类型", "Kind")} kw={76}>{node?.sub ?? "—"}</KV>
            <KV k={tx("集群", "Cluster")} kw={76}>{node?.cluster ?? <UnknownValue />}</KV>
            <KV k={tx("环境", "Environment")} kw={76}>{node?.environment_ids?.join(", ") || <UnknownValue />}</KV>
            <KV k={tx("来源", "Source")} kw={76}>
              <Row gap={6}>
                <Brand name="kubernetes" size={18} />
                <Muted size={11.5}>{tx("Kubernetes 资源同步", "Kubernetes inventory")}</Muted>
              </Row>
            </KV>
          </Section>
          <Unavailable reason="not_connected" icon="activity" title={tx("还没有这个资源的健康和容量数据", "No health or capacity data for this resource yet")} desc={tx("资源目录只同步了资源和关系。把服务映射到指标来源后，可以在指标页查看服务的健康情况。", "The inventory syncs resources and relations only. Map services to a metrics source to see their health on the Metrics page.")} to="/integrations/mapping" />
          <div className="v3-grid2">
            <Btn icon="activity" to="/metrics">{tx("查看指标", "Metrics")}</Btn>
            <Btn kind="soft" icon="sparkles" onClick={() => askAi(tx("{n} 现在健康吗？", "Is {n} healthy right now?", { n: node?.name ?? sel ?? "" }))}>{tx("问 AI", "Ask AI")}</Btn>
          </div>
        </Col>
      )}
    </DetailPanel>
  ) : null;
  return (
    <Page
      flush
      // A failed read (503, 422 scope too broad) says why instead of an endless skeleton (PA-221).
      error={!rawTopology ? topoError : undefined}
      errorSource={tx("资源拓扑", "topology")}
      crumbs={[tx("资源与观测", "Resources"), tx("资源与拓扑", "Topology")]}
      actions={
        <>
          {API_MODE === "mock" && <ChipSelect icon="clock" label="" showLabel={false} defaultValue="1431" options={[{ value: "1431", label: tx("快照 14:31 · 最新", "Snapshot 14:31 · latest") }, { value: "1400", label: tx("快照 14:00 · 发布前", "Snapshot 14:00 · before release") }, { value: "y", label: tx("快照 昨天 14:30", "Snapshot yesterday 14:30") }]} />}
          <Btn icon="rotate" onClick={() => (API_MODE === "mock" ? toast(tx("已刷新拓扑快照", "Topology snapshot refreshed")) : void topoRefetch())}>
            {tx("刷新快照", "Refresh")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main" style={{ display: "flex", flexDirection: "column", padding: "14px 16px", gap: 10, overflow: "hidden" }}>
          <Row gap={8}>
            <Segmented label={tx("视图", "View")} value={view_} onChange={setViewMode} options={[{ value: "topo", label: tx("拓扑", "Topology") }, { value: "tree", label: tx("层级", "Hierarchy") }, { value: "sankey", label: tx("桑基", "Sankey") }]} />
            {/* Cluster / around / depth don't filter the real topology yet; only the sample shows them. */}
            {API_MODE === "mock" && (
              <>
                <ChipSelect icon="grid" label={tx("集群", "Cluster")} defaultValue="prod-sh-1" options={["prod-sh-1", "prod-bj-1"]} />
                <ChipSelect icon="box" label={tx("围绕", "Around")} defaultValue="checkout-api" options={lists.services.length ? lists.services : ["checkout-api"]} />
                <ChipSelect label={tx("深度", "Depth")} defaultValue="2" options={["1", "2", "3"]} />
              </>
            )}
            <ChipSelect icon="layers" label="" showLabel={false} defaultValue="zone" options={[{ value: "zone", label: tx("按可用区分组", "Group by zone") }, { value: "ns", label: tx("按命名空间分组", "Group by namespace") }, { value: "team", label: tx("按团队分组", "Group by team") }]} />
            <span style={{ flexGrow: 1 }} />
            <Chip icon="alert" active={blast} onClick={() => setBlast((b) => !b)}>
              {tx("影响范围", "Blast radius")}
            </Chip>
          </Row>
          {!empty && <CollectionBanner stale={stale} />}
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
            {/* An empty graph says why instead of drawing a blank canvas (PA-61). */}
            {t && empty && view_ !== "sankey" && <CollectionEmpty stale={stale} />}
            {t && !empty && view_ === "topo" && (
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
                      style={{ fill: "none", stroke: e.tone ? TONE_COLOR[e.tone] : "var(--border-strong)", strokeWidth: e.width, strokeLinecap: "round", opacity: hover && e.from !== hover && e.to !== hover ? 0.25 : 1, transition: "opacity 150ms" }}
                    />
                  ))}
                </svg>
                {t.edges.map((e) =>
                  e.label ? (
                    <span key={e.id} className="topo-elabel tnum" style={{ left: byId[e.from] && byId[e.to] ? route(byId[e.from], byId[e.to]).lx : e.lx, top: byId[e.from] && byId[e.to] ? route(byId[e.from], byId[e.to]).ly : e.ly, color: e.tone == null ? "var(--muted)" : e.tone === "ok" ? "var(--muted)" : TONE_COLOR[e.tone] }}>
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
                    {n.status && <Dot tone={n.status} size={8} pulse={n.status === "danger"} />}
                  </button>
                ))}
              </div>
            )}
            {t && !empty && view_ === "tree" && (
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
                          {r.status ? <State tone={r.status}>{r.status === "danger" ? tx("异常", "Degraded") : r.status === "warn" ? tx("警告", "Warning") : tx("正常", "Healthy")}</State> : <UnknownValue reason="not_connected" source={tx("健康状态来源", "Health source")} />}
                        </span>
                        <span className="tnum">{r.replicas}</span>
                        <span className="v3-row" style={{ gap: 4 }}>{r.cpu !== undefined && <><Bar pct={r.cpu} tone={r.cpu > 80 ? "danger" : r.cpu > 65 ? "warn" : "ok"} width={70} /><Muted size={11.5}>{r.cpu}%</Muted></>}</span>
                        <span className="v3-row" style={{ gap: 4 }}>{r.mem !== undefined && <><Bar pct={r.mem} tone={r.mem > 75 ? "warn" : "ok"} width={70} /><Muted size={11.5}>{r.mem}%</Muted></>}</span>
                        <span className="v3-row" style={{ gap: 4 }}>{r.alerts !== undefined && <span className={`v3-badge mono t-${r.alerts ? "danger" : "neutral"}`}>{r.alerts}</span>}<span className="hact" onClick={(ev) => ev.stopPropagation()}><MenuButton trigger={({ onClick, ref }) => <span ref={ref}><IconBtn icon="more" label={tx("更多操作", "More")} size={22} onClick={onClick} /></span>} items={[{ label: tx("查看指标", "Metrics"), icon: "activity", onSelect: () => nav(`/metrics?service=${r.name}`) }, { label: tx("问 AI", "Ask AI"), icon: "sparkles", onSelect: () => askAi(tx("{n} 现在健康吗？", "Is {n} healthy right now?", { n: r.name })) }]} /></span></span>
                      </div>
                    );
                  })}
              </div>
            )}
            {t && view_ === "sankey" && (
              <div className="anim-fade" style={{ padding: "24px 48px", display: "flex", flexDirection: "column", gap: 14, background: "var(--panel)", height: "100%" }}>
                {t.sankey ? (
                  <>
                    <Sankey cols={t.sankey.cols} links={t.sankey.links} height={520} />
                    <Muted size={11.5}>{tx("线宽 = 请求量；红色为异常流量。", "Width = volume; red is unhealthy.")}</Muted>
                  </>
                ) : (
                  <Unavailable reason="not_connected" icon="trend" title={tx("还没有服务间流量数据", "No service-to-service traffic yet")} desc={tx("流量图需要按服务统计的调用量。接入链路（Tempo）并绑定到服务后，这里显示服务之间的请求流向。", "This view needs per-service call volumes. Connect traces (Tempo) and map them to services to see request flows here.")} />
                )}
              </div>
            )}
          </div>
          {t && !empty && view_ === "topo" && (
            <>
              <ZoomControl k={view.k} onZoom={(f) => zoom(f)} onReset={() => zoom(1 / view.k)} onFit={fit} />
              {t.blast && (
                <div className={cx("blast-chip", blast && "show")}>
                  <Icon name="alert" size={14} color="var(--danger)" />
                  <b>{tx("影响范围", "Blast radius")}</b>
                  {t.blast}
                </div>
              )}
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
