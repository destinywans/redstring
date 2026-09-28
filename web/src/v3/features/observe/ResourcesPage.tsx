import { useEffect, useRef, useState } from "react";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { Topology } from "../../api/types";
import { Avatar, Bar, Brand, Btn, Chip, Col, Dot, Icon, IconBtn, KV, Muted, Row, Section, Segmented, Skeleton, State, Strong, TextLink, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Gauge, Lines, Sankey } from "../../charts";
import { useI18n } from "../../i18n";
import { DetailPanel, FLOAT_COVER, usePinned } from "../../ui/DetailPanel";
import "./observe.css";

const TONE_COLOR = { ok: "var(--c2)", warn: "var(--warn)", danger: "var(--danger)" };

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
  const [view, setView] = useState<"topo" | "tree" | "sankey">("topo");
  const [blast, setBlast] = useState(true);
  const [sel, setSel] = useState<string | null>(null);
  const [pinned, setPinned] = usePinned("topology");
  const box = useRef<HTMLDivElement>(null);
  const floating = !!sel && !pinned;
  // Shift the graph left (not the legend or the page) just enough to keep the selected node clear of the card.
  const [shift, setShift] = useState(0);
  useEffect(() => {
    if (!floating || !box.current) {
      setShift(0);
      return;
    }
    const el = box.current.querySelector<HTMLElement>(`[data-node="${CSS.escape(sel!)}"]`);
    if (!el) return;
    const b = box.current.getBoundingClientRect();
    const n = el.getBoundingClientRect();
    const overlap = n.right - (b.right - FLOAT_COVER) + 16;
    if (overlap > 0) setShift((x) => x - overlap);
  }, [sel, floating]);
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
                <Btn icon="external">Grafana</Btn>
                <Btn kind="soft" icon="sparkles" to="/conversations/new?q=orders-db%20%E7%8E%B0%E5%9C%A8%E5%81%A5%E5%BA%B7%E5%90%97%EF%BC%9F">{tx("问 AI", "Ask AI")}</Btn>
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
          <Chip icon="clock" caret>
            {tx("快照 14:31", "Snapshot 14:31")}
          </Chip>
          <Btn icon="rotate" onClick={() => toast(tx("已刷新拓扑快照", "Topology snapshot refreshed"))}>
            {tx("刷新快照", "Refresh")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main" style={{ display: "flex", flexDirection: "column", padding: "14px 16px", gap: 10, overflow: "hidden" }}>
          <Row gap={8}>
            <Segmented label={tx("视图", "View")} value={view} onChange={setView} options={[{ value: "topo", label: tx("拓扑", "Topology") }, { value: "tree", label: tx("层级", "Hierarchy") }, { value: "sankey", label: tx("桑基", "Sankey") }]} />
            <Chip icon="grid" caret>
              {tx("集群 prod-sh-1", "Cluster prod-sh-1")}
            </Chip>
            <Chip icon="box" caret>
              {tx("围绕：checkout-api", "Around: checkout-api")}
            </Chip>
            <Chip caret>{tx("深度 2", "Depth 2")}</Chip>
            <Chip icon="layers" caret>
              {tx("按可用区分组", "Group by zone")}
            </Chip>
            <span style={{ flexGrow: 1 }} />
            <Chip icon="alert" active={blast} onClick={() => setBlast((b) => !b)}>
              {tx("影响范围", "Blast radius")}
            </Chip>
          </Row>
          <div className="topo-stage">
          <div
            className="topo-box"
            ref={box}
            onClick={(e) => {
              if (!(e.target as HTMLElement).closest(".topo-node, .blast-chip, .topo-legend")) setSel(null);
            }}
          >
            {!t && <Skeleton h={600} style={{ margin: 20 }} />}
            {t && view === "topo" && (
              <div className="topo-canvas anim-fade" style={{ transform: shift ? `translateX(${shift}px)` : undefined }}>
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
                      d={e.path}
                      className={e.fast ? "flow-fast" : "flow"}
                      style={{ fill: "none", stroke: TONE_COLOR[e.tone], strokeWidth: e.width, strokeLinecap: "round", opacity: hover && e.from !== hover && e.to !== hover ? 0.25 : 1, transition: "opacity 150ms" }}
                    />
                  ))}
                </svg>
                {t.edges.map((e) =>
                  e.label ? (
                    <span key={e.id} className="topo-elabel tnum" style={{ left: e.lx, top: e.ly, color: e.tone === "ok" ? "var(--muted)" : TONE_COLOR[e.tone] }}>
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
            {t && view === "tree" && (
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
                        <span className="v3-row" style={{ gap: 4 }}>{r.alerts !== undefined && <span className={`v3-badge mono t-${r.alerts ? "danger" : "neutral"}`}>{r.alerts}</span>}<span className="hact"><IconBtn icon="more" label="more" size={22} /></span></span>
                      </div>
                    );
                  })}
              </div>
            )}
            {t && view === "sankey" && (
              <div className="anim-fade" style={{ padding: "24px 48px", display: "flex", flexDirection: "column", gap: 14, background: "var(--panel)", height: "100%" }}>
                <Sankey cols={t.sankey.cols} links={t.sankey.links} height={520} />
                <Muted size={11.5}>{tx("线宽 = 请求量；红色为异常流量。checkout-api 流向 orders-db 的 500 qps 中 8.4% 以超时结束。", "Width = volume; red is unhealthy. 8.4% of the 500 qps from checkout-api to orders-db end in timeouts.")}</Muted>
              </div>
            )}
          </div>
          {t && view === "topo" && (
            <>
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
