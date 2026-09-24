import { useCallback, useEffect, useMemo, useRef, useState, type PointerEvent as RPE } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { CanvasGraph, CanvasNode, EvidenceTrayItem } from "../../api/types";
import { AvatarGroup, Badge, Btn, Col, Dot, Env, Icon, IconBtn, Muted, Row, Section, Segmented, Skeleton, Spacer, Strong, Tabs, Tile, KV, Mono, cx, type IconName } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Donut, Lines, Sparkline } from "../../charts";
import { useI18n } from "../../i18n";
import { LoopBar } from "./LoopBar";
import { WritebackDialog } from "./WritebackDialog";
import { CompareDialog } from "./CompareDialog";
import "./canvas.css";

type View = { x: number; y: number; k: number };
const ANCHOR = 30;
const clampK = (k: number) => Math.max(0.4, Math.min(1.8, k));

function Edge({ from, to, kind, live }: { from: CanvasNode; to: CanvasNode; kind: "fork" | "evidence"; live?: boolean }) {
  const x1 = from.x + from.w;
  const y1 = from.y + ANCHOR;
  const x2 = to.x;
  const y2 = to.y + ANCHOR;
  const back = x2 < x1;
  const d = back
    ? `M${from.x + from.w / 2} ${from.y + 150} L ${to.x + to.w / 2} ${to.y}`
    : `M${x1} ${y1} C ${x1 + 40} ${y1}, ${x2 - 40} ${y2}, ${x2} ${y2}`;
  const color = kind === "evidence" ? to.evidence?.color ?? "var(--muted)" : to.status === "ruled_out" ? "var(--ok)" : "var(--accent)";
  return <path d={d} className={cx("cv-edge", kind === "evidence" && "evidence", live && "flow")} style={{ stroke: color }} />;
}

function NodeCard({
  n,
  selected,
  onSelect,
  onDragStart,
  onAsk,
}: {
  n: CanvasNode;
  selected: boolean;
  onSelect: () => void;
  onDragStart: (e: RPE) => void;
  onAsk: (text: string) => void;
}) {
  const { tx } = useI18n();
  const [q, setQ] = useState("");
  const icon: IconName = n.kind === "root" ? "alert" : n.kind === "evidence" ? (n.evidence?.type === "log" ? "terminal" : n.evidence?.type === "diff" ? "code" : "activity") : n.status === "ruled_out" ? "branch" : "database";
  const tone = n.kind === "root" ? "danger" : n.status === "ruled_out" ? "ok" : "accent";
  const collapsed = n.kind === "branch" && n.status === "ruled_out";
  return (
    <article
      className={cx("cv-node anim-pop", n.kind, collapsed && "collapsed", selected && "selected", n.status === "running" && "running")}
      style={{ left: n.x, top: n.y, width: n.w, borderTopColor: n.kind === "evidence" ? n.evidence?.color : undefined }}
      onPointerDown={(e) => {
        e.stopPropagation();
        onSelect();
      }}
    >
      <div className="cv-head" onPointerDown={onDragStart}>
        {n.kind === "evidence" ? (
          <span className="ev-tile" style={{ color: n.evidence?.color }}>
            <Icon name={icon} size={13} />
          </span>
        ) : (
          <Tile icon={icon} tone={tone} size={26} />
        )}
        <div className="v3-col grow" style={{ gap: 0, lineHeight: 1.35 }}>
          <span className="ellipsis" style={{ fontWeight: 600, fontSize: n.kind === "evidence" ? 12 : 13 }}>
            {n.title}
          </span>
          <span className="muted ellipsis" style={{ fontSize: 11 }}>
            {n.subtitle}
          </span>
        </div>
        {collapsed && (
          <Badge tone="ok">
            <Icon name="check" size={11} />
            {tx("已排除", "Ruled out")}
          </Badge>
        )}
        {n.status === "running" && <span className="v3-spinner spin" style={{ color: "var(--accent)" }} />}
      </div>
      {!collapsed && n.kind !== "evidence" && (
        <div className="cv-body">
          {n.tools && (
            <Row gap={6} style={{ fontSize: 11.5, color: "var(--muted)" }}>
              <Icon name="check" size={12} color="var(--ok)" />
              {n.tools}
              <Spacer />
              {n.confidence && <Badge tone={n.confidence === "high" ? "ok" : "warn"}>{tx("置信度 {c}", "Confidence {c}", { c: n.confidence === "high" ? tx("高", "high") : tx("中", "medium") })}</Badge>}
            </Row>
          )}
          {n.summary ? <Markdown text={n.summary} /> : n.status === "running" && <Muted>{tx("AI 正在分析这个分支…", "The AI is working on this branch…")}</Muted>}
          {n.series && <Lines series={[{ values: n.series, color: "var(--c3)", fill: "var(--c3s)" }]} height={50} grid={2} hover={false} />}
          {n.evidence_refs && (
            <Row gap={4} wrap>
              {n.kind === "root" ? (
                <>
                  <Badge>
                    <Icon name="file" size={11} />
                    {tx("{n} 条证据", "{n} evidence", { n: n.evidence_refs[0] })}
                  </Badge>
                  <Badge tone="accent">
                    <Icon name="writeback" size={11} />
                    {tx("已回写 3", "3 written back")}
                  </Badge>
                </>
              ) : (
                n.evidence_refs.map((r) => <Badge key={r}>{r}</Badge>)
              )}
            </Row>
          )}
          {n.writeback && (
            <Badge tone="ok">
              <Icon name="check" size={11} />
              {tx("已回写主线 {v}", "Written back to main {v}", { v: n.writeback.version })}
            </Badge>
          )}
          {selected && n.kind === "branch" && (
            <form
              className="cv-ask"
              onSubmit={(e) => {
                e.preventDefault();
                if (!q.trim()) return;
                onAsk(q.trim());
                setQ("");
              }}
              onPointerDown={(e) => e.stopPropagation()}
            >
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={tx("在这个分支继续追问…", "Ask a follow-up in this branch…")} aria-label={tx("在这个分支继续追问", "Follow-up in this branch")} />
              <IconBtn icon="up" label={tx("发送", "Send")} size={24} color="var(--accent-text)" submit />
            </form>
          )}
        </div>
      )}
      {n.kind === "evidence" && n.evidence && (
        <div className="cv-body">
          {n.evidence.type === "chart" && <Lines series={[{ values: n.evidence.values, color: n.evidence.color, fill: "var(--c3s)" }]} height={64} grid={2} hover={false} markers={n.evidence.marker ? [{ at: n.evidence.marker, label: tx("发布", "release"), tone: "warn" }] : []} />}
          {n.evidence.type === "log" && <pre className="cv-log">{n.evidence.lines.join("\n")}</pre>}
          {n.evidence.type === "diff" && (
            <div className="cv-diff mono">
              <div className="del">{n.evidence.minus}</div>
              <div className="add">{n.evidence.plus}</div>
            </div>
          )}
          {n.evidence.type === "table" && (
            <div className="cv-log">
              {n.evidence.rows.map((r) => (
                <div key={r[0]} className="v3-row" style={{ gap: 8 }}>
                  <span>{r[0]}</span>
                  <Spacer />
                  <span style={{ color: "var(--danger)" }}>{r[1]}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </article>
  );
}

function Inspector({ n, nodes, onClose, onWriteback }: { n: CanvasNode; nodes: CanvasNode[]; onClose: () => void; onWriteback: () => void }) {
  const { tx, fmt } = useI18n();
  const [tab, setTab] = useState<"chat" | "evidence" | "context" | "agent">("context");
  const kids = nodes.filter((x) => x.parent === n.id);
  const used = n.context.tokens;
  const budget = 28000;
  const parts: [string, number, string][] = [
    [tx("系统", "System"), 0.07, "var(--faint)"],
    ["Skills", 0.11, "var(--c4)"],
    [tx("证据", "Evidence"), 0.39, "var(--c1)"],
    [tx("历史", "History"), 0.24, "var(--c2)"],
    [tx("工具", "Tools"), 0.19, "var(--c6)"],
  ];
  return (
    <aside className="cv-inspector anim-fade" aria-label={tx("节点详情", "Node details")}>
      <Row gap={10} style={{ padding: "14px 16px 0" }}>
        <Tile icon={n.kind === "root" ? "alert" : n.kind === "evidence" ? "file" : "database"} tone={n.kind === "root" ? "danger" : "accent"} />
        <Col gap={0} grow>
          <Strong size={13.5}>{n.title}</Strong>
          <Muted size={11.5}>
            {n.kind === "root" ? tx("主线", "Main line") : n.kind === "evidence" ? tx("证据节点", "Evidence node") : tx("分支", "Branch")} · {tx("上下文", "context")} {n.context.version}
          </Muted>
        </Col>
        <IconBtn icon="x" label={tx("关闭详情", "Close")} onClick={onClose} />
      </Row>
      <Tabs
        value={tab}
        onChange={setTab}
        pad="6px 16px 0"
        options={[
          { value: "chat", label: tx("对话", "Chat"), count: n.context.messages || undefined },
          { value: "evidence", label: tx("证据", "Evidence"), count: n.evidence_refs?.length },
          { value: "context", label: tx("上下文", "Context") },
          { value: "agent", label: "Agent" },
        ]}
      />
      <div className="cv-inspector-body" key={tab}>
        {tab === "context" && (
          <>
            <Section title={tx("上下文隔离", "Context isolation")}>
              <Col gap={8}>
                {n.context.inherits && (
                  <KV k={tx("继承", "Inherits")} kw={56}>
                    <Badge mono>
                      {tx("主线", "main")} {n.context.inherits}
                    </Badge>{" "}
                    <Muted>{tx("派生时冻结", "frozen at fork")}</Muted>
                  </KV>
                )}
                <KV k={tx("本分支", "This branch")} kw={56}>
                  {tx("+{m} 条消息 · {e} 条证据", "+{m} messages · {e} evidence", { m: n.context.messages, e: n.evidence_refs?.length ?? 0 })}
                </KV>
                {n.context.excludes.length > 0 && (
                  <KV k={tx("不包含", "Excludes")} kw={56}>
                    <Row gap={4} wrap>
                      {n.context.excludes.map((x) => (
                        <Badge key={x}>{x}</Badge>
                      ))}
                    </Row>
                  </KV>
                )}
              </Col>
            </Section>
            <Section title={tx("输入预算", "Input budget")} right={<Mono>{(used / 1000).toFixed(1)}k</Mono>}>
              <div className="ctx-bar">
                {parts.map(([l, p, c]) => (
                  <span key={l} style={{ width: `${(p * used * 100) / budget}%`, background: c }} title={l} />
                ))}
              </div>
              <Row gap={10} wrap style={{ fontSize: 11 }}>
                {parts.map(([l, , c]) => (
                  <span key={l} className="v3-row muted" style={{ gap: 4 }}>
                    <i style={{ width: 7, height: 7, borderRadius: 2, background: c }} />
                    {l}
                  </span>
                ))}
              </Row>
              <Row gap={8}>
                <Muted size={11.5}>
                  {(used / 1000).toFixed(1)}k / {budget / 1000}k tokens
                </Muted>
                <Spacer />
                <Donut parts={[{ value: used, color: "var(--c1)" }, { value: budget - used, color: "var(--sunken)" }]} size={54} thick={8} center={`${Math.round((100 * used) / budget)}%`} />
              </Row>
            </Section>
            {kids.some((k) => k.writeback) && (
              <Section title={tx("子分支与回写", "Children & writeback")}>
                <div className="cv-writeback">
                  <Row gap={6}>
                    <Icon name="check" size={13} color="var(--ok)" />
                    <span style={{ color: "var(--accent-text)" }}>{tx("「连接池参数对比」的 2 条结论已回写到主线 v7", "2 conclusions from “Pool comparison” were written back to main v7")}</span>
                  </Row>
                  <Btn h={24} onClick={onWriteback}>
                    {tx("查看回写记录", "View writeback")}
                  </Btn>
                </div>
              </Section>
            )}
            {n.versions.length > 0 && (
              <Section title={tx("版本", "Versions")}>
                <ol className="cv-versions">
                  {n.versions.map((v, i) => (
                    <li key={v.v}>
                      <Mono color={i === 0 ? "var(--text)" : undefined}>{v.v}</Mono>
                      <span className="grow">{v.text}</span>
                      <Muted size={11.5}>{fmt.time(v.at)}</Muted>
                    </li>
                  ))}
                </ol>
              </Section>
            )}
          </>
        )}
        {tab === "chat" && (
          <Col gap={12}>
            <Muted size={11.5}>{tx("这个节点自己的对话，与其他分支相互隔离。", "This node's own thread, isolated from other branches.")}</Muted>
            <div className="cv-msg user">{tx("连接等待是从什么时候开始升高的？", "When did connection wait start rising?")}</div>
            <div className="cv-msg">{n.summary ? <Markdown text={n.summary} /> : <Muted>—</Muted>}</div>
            <Btn icon="message" to="/conversations/C-320">
              {tx("在对话模式中打开", "Open in chat mode")}
            </Btn>
          </Col>
        )}
        {tab === "evidence" && (
          <Col gap={8}>
            {(n.evidence_refs ?? []).map((r, i) => (
              <Row key={r} gap={10} className="cv-ev-row">
                <Tile icon={(["code", "activity", "terminal"] as IconName[])[i % 3]} size={24} />
                <Col gap={0} grow>
                  <span style={{ fontSize: 12.5 }}>{r}</span>
                  <Muted size={11}>{["GitLab #8812", "Prometheus", "Elasticsearch"][i % 3]}</Muted>
                </Col>
                <IconBtn icon="external" label={tx("打开来源", "Open source")} size={24} />
              </Row>
            ))}
            {!n.evidence_refs?.length && <Muted>{tx("还没有证据，可以从左侧证据托盘拖入。", "No evidence yet — drag some in from the tray.")}</Muted>}
          </Col>
        )}
        {tab === "agent" && (
          <Col gap={8}>
            <KV k={tx("Agent", "Agent")}>{tx("调查助手 v12", "Investigator v12")}</KV>
            <KV k={tx("模型", "Model")}>{tx("默认模型", "Default model")}</KV>
            <KV k={tx("运行", "Run")}>
              <Mono color="var(--accent-text)">run_7f3a</Mono>
            </KV>
            <KV k="Trace">
              <Row gap={4}>
                <Badge>Langfuse</Badge>
                <Badge>Phoenix</Badge>
              </Row>
            </KV>
            <Btn icon="cpu" to="/agents/runs/run_7f3a">
              {tx("打开运行详情", "Open run details")}
            </Btn>
          </Col>
        )}
      </div>
    </aside>
  );
}

export default function CanvasPage() {
  const { id = "INV-231" } = useParams();
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const { data, isLoading } = useGet<CanvasGraph>(`/investigations/${id}/canvas`);
  const [nodes, setNodes] = useState<CanvasNode[]>([]);
  const [sel, setSel] = useState<string | null>("b-pool");
  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 });
  const [tray, setTray] = useState(false);
  const [compare, setCompare] = useState(false);
  const [dropHint, setDropHint] = useState(false);
  const area = useRef<HTMLDivElement>(null);
  const drag = useRef<{ kind: "pan" | "node"; id?: string; sx: number; sy: number; ox: number; oy: number; moved?: boolean } | null>(null);
  const writeback = params.get("writeback");

  useEffect(() => {
    if (data) setNodes(data.nodes);
  }, [data]);

  const byId = useMemo(() => Object.fromEntries(nodes.map((n) => [n.id, n])), [nodes]);
  const selected = sel ? byId[sel] : undefined;

  const toCanvas = (cx: number, cy: number) => {
    const r = area.current!.getBoundingClientRect();
    return { x: (cx - r.left - view.x) / view.k, y: (cy - r.top - view.y) / view.k };
  };

  const onPointerMove = (e: RPE) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.sx;
    const dy = e.clientY - d.sy;
    if (Math.abs(dx) + Math.abs(dy) > 2) d.moved = true;
    if (d.kind === "pan") setView((v) => ({ ...v, x: d.ox + dx, y: d.oy + dy }));
    else setNodes((ns) => ns.map((n) => (n.id === d.id ? { ...n, x: d.ox + dx / view.k, y: d.oy + dy / view.k } : n)));
  };
  const onPointerUp = () => {
    const d = drag.current;
    drag.current = null;
    if (d?.kind === "node" && d.moved) {
      const n = nodes.find((x) => x.id === d.id);
      if (n) void api("PATCH", `/investigations/${id}/nodes/${n.id}`, { body: { x: Math.round(n.x), y: Math.round(n.y) } });
    }
  };
  const zoom = useCallback((factor: number, cx?: number, cy?: number) => {
    setView((v) => {
      const k = clampK(v.k * factor);
      const r = area.current?.getBoundingClientRect();
      const px = cx !== undefined && r ? cx - r.left : (r?.width ?? 800) / 2;
      const py = cy !== undefined && r ? cy - r.top : (r?.height ?? 600) / 2;
      return { k, x: px - ((px - v.x) * k) / v.k, y: py - ((py - v.y) * k) / v.k };
    });
  }, []);
  const fit = useCallback(() => {
    if (!nodes.length || !area.current) return;
    const r = area.current.getBoundingClientRect();
    const minX = Math.min(...nodes.map((n) => n.x));
    const minY = Math.min(...nodes.map((n) => n.y));
    const maxX = Math.max(...nodes.map((n) => n.x + n.w));
    const maxY = Math.max(...nodes.map((n) => n.y + 200));
    const k = clampK(Math.min((r.width - 60) / (maxX - minX), (r.height - 120) / (maxY - minY), 1.1));
    setView({ k, x: 30 - minX * k, y: 40 - minY * k });
  }, [nodes]);
  const fitted = useRef(false);
  useEffect(() => {
    if (nodes.length && !fitted.current) {
      fitted.current = true;
      requestAnimationFrame(() => fit());
    }
  }, [nodes, fit]);

  useEffect(() => {
    const el = area.current;
    if (!el) return;
    const h = (e: WheelEvent) => {
      e.preventDefault();
      if (e.ctrlKey || e.metaKey) zoom(Math.exp(-e.deltaY * 0.01), e.clientX, e.clientY);
      else setView((v) => ({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY }));
    };
    el.addEventListener("wheel", h, { passive: false });
    return () => el.removeEventListener("wheel", h);
  }, [zoom]);

  const fork = async () => {
    const parent = selected && selected.kind !== "evidence" ? selected : byId.root;
    if (!parent) return;
    const siblings = nodes.filter((n) => n.parent === parent.id).length;
    const title = tx("新分支 {n}", "New branch {n}", { n: siblings + 1 });
    const node = await api<CanvasNode>("POST", `/investigations/${id}/branches`, { body: { parent_id: parent.id, title, x: parent.x + parent.w + 70, y: parent.y + siblings * 30 + 40 } });
    setNodes((ns) => [...ns, node]);
    setSel(node.id);
    toast(tx("已从「{p}」派生：继承其上下文 {v}，之后互不影响", "Forked from “{p}”: inherits context {v}, isolated from now on", { p: parent.title, v: parent.context.version }));
    setTimeout(() => setNodes((ns) => ns.map((n) => (n.id === node.id ? { ...n, status: "active", summary: tx("等待你的第一个问题。", "Waiting for your first question.") } : n))), 1200);
  };
  const ask = async (nodeId: string, text: string) => {
    setNodes((ns) => ns.map((n) => (n.id === nodeId ? { ...n, status: "running" } : n)));
    const res = await api<{ summary: string }>("POST", `/investigations/${id}/nodes/${nodeId}/messages`, { body: { text } });
    setTimeout(() => setNodes((ns) => ns.map((n) => (n.id === nodeId ? { ...n, status: "confirmed", summary: res.summary, context: { ...n.context, messages: n.context.messages + 2 } } : n))), 900);
  };
  const addEvidence = (item: EvidenceTrayItem, clientX: number, clientY: number) => {
    const p = toCanvas(clientX, clientY);
    const parent = selected && selected.kind !== "evidence" ? selected : byId["b-pool"] ?? byId.root;
    const colors = { chart: "var(--c1)", table: "var(--c2)", log: "var(--c4)", diff: "var(--c6)" };
    const node: CanvasNode = {
      id: `e-${Date.now()}`,
      kind: "evidence",
      parent: parent.id,
      title: tx("证据 · {t}", "Evidence · {t}", { t: item.title }),
      subtitle: tx("{s} · 从对话拖入", "{s} · dragged from chat", { s: item.source }),
      status: "active",
      x: p.x - 120,
      y: p.y - 30,
      w: 240,
      evidence: { ...item.body, source: item.source, color: colors[item.kind] } as CanvasNode["evidence"],
      context: { version: "—", tokens: 300, messages: 0, excludes: [] },
      versions: [],
    };
    setNodes((ns) => [...ns, node]);
    toast(tx("已成为「{p}」的证据节点", "Added as evidence of “{p}”", { p: parent.title }), { undo: () => setNodes((ns) => ns.filter((n) => n.id !== node.id)) });
  };

  const setWriteback = (v: string | null) => {
    const next = new URLSearchParams(params);
    if (v) next.set("writeback", v);
    else next.delete("writeback");
    setParams(next, { replace: true });
  };

  const inv = data?.investigation;
  const bounds = nodes.length ? { minX: Math.min(...nodes.map((n) => n.x)) - 40, minY: Math.min(...nodes.map((n) => n.y)) - 40, maxX: Math.max(...nodes.map((n) => n.x + n.w)) + 40, maxY: Math.max(...nodes.map((n) => n.y + 180)) + 40 } : null;

  return (
    <Page
      flush
      crumbs={[tx("对话与调查", "Conversations"), inv?.title ?? "…"]}
      actions={
        inv && (
          <>
            <Badge mono>{inv.id}</Badge>
            <Badge tone="warn">
              <Dot tone="warn" size={6} />
              {tx("等待审批", "Awaiting approval")}
            </Badge>
            <Env env={inv.env} />
            <Segmented
              label={tx("视图", "View")}
              value="canvas"
              onChange={(v) => v === "chat" && nav(`/conversations/${inv.conversation_id ?? "C-320"}`)}
              options={[
                { value: "chat", label: tx("对话", "Chat"), icon: "message" },
                { value: "canvas", label: "Canvas", icon: "branch" },
              ]}
            />
            <AvatarGroup names={inv.participants} />
            <Btn icon="file" to="/executions/EXE-51">
              {tx("复盘报告", "Report")}
            </Btn>
          </>
        )
      }
    >
      {data && <LoopBar steps={data.loop} />}
      <div className="cv-layout">
        {tray && data && (
          <aside className="cv-tray anim-fade" aria-label={tx("证据托盘", "Evidence tray")}>
            <Row gap={6}>
              <Strong>{tx("证据托盘", "Evidence tray")}</Strong>
              <Spacer />
              <IconBtn icon="x" label={tx("收起", "Hide")} size={24} onClick={() => setTray(false)} />
            </Row>
            <Muted size={11.5}>{tx("来自对话 C-320 的部件，拖到画布上成为证据节点。", "Parts from chat C-320 — drag onto the canvas to add evidence.")}</Muted>
            {data.tray.map((t) => (
              <div
                key={t.id}
                className="cv-tray-item lift"
                draggable
                onDragStart={(e) => {
                  e.dataTransfer.setData("text/x-aiops-evidence", t.id);
                  e.dataTransfer.effectAllowed = "copy";
                }}
              >
                <Row gap={8}>
                  <Tile icon={t.kind === "chart" ? "activity" : t.kind === "table" ? "grid" : t.kind === "log" ? "terminal" : "code"} size={22} />
                  <Col gap={0} grow>
                    <span className="ellipsis" style={{ fontSize: 12, fontWeight: 500 }}>
                      {t.title}
                    </span>
                    <Muted size={10.5}>{t.source}</Muted>
                  </Col>
                  <Icon name="grid" size={13} color="var(--faint)" />
                </Row>
                {t.body.type === "chart" && <Sparkline values={t.body.values} width={190} height={26} color="var(--c1)" />}
              </div>
            ))}
          </aside>
        )}
        <div
          ref={area}
          className={cx("cv-area", dropHint && "drop")}
          style={{ backgroundPosition: `${view.x}px ${view.y}px`, backgroundSize: `${20 * view.k}px ${20 * view.k}px` }}
          onPointerDown={(e) => {
            if (e.button !== 0) return;
            drag.current = { kind: "pan", sx: e.clientX, sy: e.clientY, ox: view.x, oy: view.y };
            (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
          }}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onClick={(e) => {
            if (e.target === area.current && !drag.current) setSel(null);
          }}
          onDragOver={(e) => {
            if (e.dataTransfer.types.includes("text/x-aiops-evidence")) {
              e.preventDefault();
              setDropHint(true);
            }
          }}
          onDragLeave={() => setDropHint(false)}
          onDrop={(e) => {
            setDropHint(false);
            const tid = e.dataTransfer.getData("text/x-aiops-evidence");
            const item = data?.tray.find((t) => t.id === tid);
            if (item) addEvidence(item, e.clientX, e.clientY);
          }}
        >
          {isLoading && (
            <div style={{ padding: 40 }}>
              <Skeleton w={260} h={160} />
            </div>
          )}
          <div className="cv-layer" style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})` }}>
            <svg className="cv-edges" width="4000" height="3000" aria-hidden="true">
              {nodes
                .filter((n) => n.parent && byId[n.parent])
                .map((n) => (
                  <Edge key={n.id} from={byId[n.parent!]} to={n} kind={n.kind === "evidence" ? "evidence" : "fork"} live={n.status === "running" || (n.status === "confirmed" && n.id === sel)} />
                ))}
            </svg>
            {nodes.map((n) => (
              <NodeCard
                key={n.id}
                n={n}
                selected={sel === n.id}
                onSelect={() => setSel(n.id)}
                onAsk={(t) => ask(n.id, t)}
                onDragStart={(e) => {
                  if ((e.target as HTMLElement).closest("button,input")) return;
                  e.stopPropagation();
                  setSel(n.id);
                  drag.current = { kind: "node", id: n.id, sx: e.clientX, sy: e.clientY, ox: n.x, oy: n.y };
                  area.current?.setPointerCapture(e.pointerId);
                }}
              />
            ))}
          </div>
          {dropHint && <div className="cv-drop-hint">{tx("松开：成为「{p}」的证据节点", "Release to add as evidence of “{p}”", { p: selected?.title ?? "" })}</div>}
          <span className="cv-status">{tx("{b} 个分支 · {e} 个证据节点 · 拖入对话部件即可加入画布", "{b} branches · {e} evidence nodes · drag chat parts in", { b: nodes.filter((n) => n.kind === "branch").length, e: nodes.filter((n) => n.kind === "evidence").length })}</span>
          {bounds && (
            <div className="cv-minimap" aria-label={tx("缩略图", "Minimap")}>
              <svg width="150" height="80" viewBox={`${bounds.minX} ${bounds.minY} ${bounds.maxX - bounds.minX} ${bounds.maxY - bounds.minY}`} preserveAspectRatio="xMidYMid meet">
                {nodes.map((n) => (
                  <rect key={n.id} x={n.x} y={n.y} width={n.w} height={n.kind === "branch" && n.status === "ruled_out" ? 60 : 140} rx={10} style={{ fill: n.kind === "root" ? "var(--danger-soft)" : n.kind === "evidence" ? "var(--c3s)" : n.status === "ruled_out" ? "var(--ok-soft)" : "var(--accent-soft)" }} />
                ))}
              </svg>
            </div>
          )}
          <div className="cv-zoom" onPointerDown={(e) => e.stopPropagation()}>
            <IconBtn icon="zoomout" label={tx("缩小", "Zoom out")} size={26} onClick={() => zoom(0.85)} />
            <button type="button" className="tnum" onClick={() => setView({ x: 0, y: 0, k: 1 })} title={tx("重置", "Reset")}>
              {Math.round(view.k * 100)}%
            </button>
            <IconBtn icon="zoomin" label={tx("放大", "Zoom in")} size={26} onClick={() => zoom(1.18)} />
            <IconBtn icon="fit" label={tx("适应画布", "Fit")} size={26} onClick={fit} />
          </div>
          <div className="cv-toolbar" role="toolbar" aria-label={tx("画布工具", "Canvas tools")} onPointerDown={(e) => e.stopPropagation()}>
            <Btn kind="ghost" icon="fork" onClick={fork}>
              {tx("派生", "Fork")}
            </Btn>
            <Btn kind="ghost" icon="merge" onClick={() => toast(tx("请选择两个同级分支进行合并", "Select two sibling branches to merge"))}>
              {tx("合并", "Merge")}
            </Btn>
            <Btn kind="ghost" icon="writeback" onClick={() => setWriteback(sel ?? "b-cmp")}>
              {tx("回写", "Write back")}
            </Btn>
            <Btn kind={compare ? "soft" : "ghost"} icon="layers" onClick={() => setCompare((c) => !c)}>
              {tx("对比", "Compare")}
            </Btn>
            <span className="v3-vdivider" />
            <Btn kind={tray ? "soft" : "ghost"} icon="file" onClick={() => setTray((t) => !t)}>
              {tx("证据", "Evidence")}
            </Btn>
            <Btn kind="ghost" icon="grid" onClick={() => { if (data) setNodes(data.nodes.concat(nodes.filter((n) => !data.nodes.some((d) => d.id === n.id)))); fit(); }}>
              {tx("自动布局", "Auto layout")}
            </Btn>
          </div>
          {compare && <CompareDialog a={byId["b-pool"]} b={byId["b-cmp"]} onClose={() => setCompare(false)} onMerge={() => { setCompare(false); setWriteback("b-cmp"); }} />}
        </div>
        {selected && <Inspector n={selected} nodes={nodes} onClose={() => setSel(null)} onWriteback={() => setWriteback("b-cmp")} />}
      </div>
      <WritebackDialog
        open={!!writeback}
        investigation={id}
        from={writeback ?? ""}
        onClose={() => setWriteback(null)}
        onDone={(v) => {
          setWriteback(null);
          setNodes((ns) => ns.map((n) => (n.id === "root" ? { ...n, subtitle: tx("主线 · 上下文 {v}", "Main line · context {v}", { v }) } : n)));
          toast(tx("已生成主线 {v}，可在“版本”中查看或撤销", "Main line {v} created — view or revert under Versions", { v }), { undo: () => toast(tx("已撤销回写", "Writeback reverted")) });
        }}
      />
    </Page>
  );
}
