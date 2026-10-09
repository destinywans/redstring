import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as RPE } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, api, useGet } from "../../api/client";
import type { CanvasBlock, CanvasDoc, CanvasDocNode, CanvasTurn, Me, ModelChoice } from "../../api/types";
import { AiAvatar, Avatar, AvatarGroup, Badge, Brand, Btn, Dot, Env, Icon, IconBtn, Muted, Row, Segmented, Skeleton, Spacer, Strong, Tabs, Tile, cx, type IconName, type Tone } from "../../ui/core";
import { BrandMark } from "../../ui/BrandMark";
import { toast } from "../../ui/overlays";
import { Composer } from "../../ui/Composer";
import { describeChoice, useModelCatalog } from "../../ui/ModelPicker";
import { VBars } from "../../charts";
import { useI18n } from "../../i18n";
import { CanvasState, KindTag } from "./CanvasesPage";
import { LITE_ZOOM } from "./CanvasPage";
import { DetailPanel, useFloatCover, usePinned } from "../../ui/DetailPanel";
import "./canvas.css";
import "./canvases.css";
import { downloadFile } from "../../ui/demo";

const ANCHOR = 22;
const clampK = (k: number) => Math.min(1.8, Math.max(0.4, k));

function Block({ b }: { b: CanvasBlock }) {
  switch (b.type) {
    case "kv":
      return (
        <div className="dc-kv">
          {b.items.map(([k, v]) => (
            <div key={k}>
              <span className="muted">{k}</span>
              <span>{v}</span>
            </div>
          ))}
        </div>
      );
    case "tags":
      return (
        <Row gap={4} wrap>
          {b.items.map((t) => (
            <Badge key={t}>{t}</Badge>
          ))}
        </Row>
      );
    case "text":
      return <Muted size={11.5}>{b.text}</Muted>;
    case "bars":
      return <VBars values={b.values} height={52} color={b.color ?? "var(--c5)"} highlight={b.highlight !== undefined ? new Set([b.highlight]) : undefined} hiColor="var(--c3)" />;
    case "rows":
      return (
        <div className="v3-col" style={{ gap: 7 }}>
          {b.items.map((r, i) => (
            <Row key={i} gap={6} style={{ fontSize: "var(--fs-sub)" }}>
              {r.dot && <Dot tone={r.dot} size={7} />}
              {r.badge && <Badge tone={r.badge_tone ?? "neutral"}>{r.badge}</Badge>}
              <span className="grow" style={{ minWidth: 0 }}>
                {r.text}
              </span>
              {r.value && <b className="tnum">{r.value}</b>}
              {r.state && <Badge tone={r.state_tone ?? "neutral"}>{r.state}</Badge>}
            </Row>
          ))}
        </div>
      );
    case "steps":
      return (
        <div className="v3-col" style={{ gap: 9 }}>
          {b.items.map((s, i) => (
            <Row key={i} gap={8} align="flex-start">
              <span className={cx("dc-step", s.state)}>{s.state === "done" ? <Icon name="check" size={11} /> : s.state === "gate" ? <Icon name="shield" size={11} /> : i + 1}</span>
              <span className="v3-col" style={{ gap: 0, minWidth: 0 }}>
                <span style={{ fontSize: "var(--fs-sub)", fontWeight: 500 }}>{s.text}</span>
                <Muted size={10.5}>{s.sub}</Muted>
              </span>
            </Row>
          ))}
        </div>
      );
    case "callout":
      return (
        <div className={cx("dc-callout", b.tone)}>
          {b.icon && <Icon name={b.icon as IconName} size={13} />}
          <span>{b.text}</span>
        </div>
      );
  }
}

/** One line that stands for the node when zoomed out. */
function liteLine(n: CanvasDocNode) {
  for (const b of n.blocks) {
    if (b.type === "text" || b.type === "callout") return b.text;
    if (b.type === "kv") return b.items.map(([k, v]) => `${k} ${v}`).join(" · ");
    if (b.type === "steps") return b.items.map((x) => x.text).join(" → ");
    if (b.type === "rows") return b.items.map((x) => x.text).join(" · ");
  }
  return n.sub;
}

function Turn({ t }: { t: CanvasTurn }) {
  return (
    <Row gap={8} align="flex-start">
      {t.role === "assistant" ? <AiAvatar size={22} /> : <Avatar name={t.author} size={22} accent />}
      <div className="v3-col" style={{ gap: 4, minWidth: 0 }}>
        {t.model && (
          <Row gap={5}>
            {t.brand && <Brand name={t.brand} size={16} />}
            <Muted size={11}>{t.reasoning_label ? `${t.model} · ${t.reasoning_label}` : t.model}</Muted>
          </Row>
        )}
        <p style={{ fontSize: "var(--fs-sm)", lineHeight: 1.6 }}>{t.text}</p>
      </div>
    </Row>
  );
}

/**
 * Canvases that are not investigations (plans, reviews, analyses, blank). Nodes are made of generic blocks;
 * each node keeps its own conversation and model choice. Writes still go through proposals.
 */
export default function DocCanvasPage() {
  const { id = "" } = useParams();
  const { tx } = useI18n();
  const { data: me } = useGet<Me>("/me", undefined, { staleTime: 5 * 60_000 });
  const nav = useNavigate();
  const { data, isLoading } = useGet<CanvasDoc>(`/canvases/${id}`);
  const { data: catalog } = useModelCatalog();
  const [nodes, setNodes] = useState<CanvasDocNode[]>([]);
  const [heights, setHeights] = useState<Record<string, number>>({});
  const [sel, setSel] = useState<string | null>(null);
  const [pinned, setPinned] = usePinned("doc-canvas");
  const panned = useRef(false);
  const [tab, setTab] = useState<"chat" | "context" | "artifacts">("chat");
  const [view, setView] = useState({ x: 0, y: 0, k: 1 });
  const [threads, setThreads] = useState<CanvasDoc["threads"]>({});
  const [choice, setChoice] = useState<ModelChoice | null>(null);
  const [busy, setBusy] = useState<ModelChoice | null>(null);
  const area = useRef<HTMLDivElement>(null);
  const layer = useRef<HTMLDivElement>(null);
  const drag = useRef<{ kind: "pan" | "node"; id?: string; sx: number; sy: number; ox: number; oy: number; moved?: boolean } | null>(null);

  useEffect(() => {
    if (!data) return;
    setNodes(data.nodes);
    setThreads(data.threads);
  }, [data]);
  useLayoutEffect(() => {
    const h: Record<string, number> = {};
    layer.current?.querySelectorAll<HTMLElement>("[data-node]").forEach((el) => (h[el.dataset.node!] = el.offsetHeight));
    setHeights((old) => (JSON.stringify(old) === JSON.stringify(h) ? old : h));
  }, [nodes, view.k < LITE_ZOOM]); // eslint-disable-line react-hooks/exhaustive-deps

  const byId = useMemo(() => Object.fromEntries(nodes.map((n) => [n.id, n])), [nodes]);
  const selected = sel ? byId[sel] : undefined;
  const thread = sel ? threads[sel] : undefined;

  const zoom = useCallback((f: number) => setView((v) => ({ ...v, k: clampK(v.k * f) })), []);
  const fit = useCallback(() => {
    if (!nodes.length || !area.current || !data) return;
    const r = area.current.getBoundingClientRect();
    const xs = [...nodes.map((n) => n.x + n.w), ...data.artifacts.map((a) => a.x + 148)];
    const ys = [...nodes.map((n) => n.y + (heights[n.id] ?? 200)), ...data.artifacts.map((a) => a.y + 52)];
    const minX = Math.min(...nodes.map((n) => n.x));
    const minY = Math.min(...nodes.map((n) => n.y));
    // Fit (never above 100%) and centre; leave room for the note on top and the toolbar below.
    const bw = Math.max(...xs) - minX;
    const bh = Math.max(...ys) - minY;
    const top = 36;
    const bottom = 74;
    const k = clampK(Math.min((r.width - 60) / bw, (r.height - top - bottom) / bh, 1));
    setView({ k, x: (r.width - bw * k) / 2 - minX * k, y: top + (r.height - top - bottom - bh * k) / 2 - minY * k });
  }, [nodes, heights, data]);
  const fitted = useRef(false);
  useEffect(() => {
    if (nodes.length && Object.keys(heights).length && !fitted.current) {
      fitted.current = true;
      requestAnimationFrame(fit);
    }
  }, [nodes, heights, fit]);
  useEffect(() => {
    const el = area.current;
    if (!el) return;
    const h = (e: WheelEvent) => {
      e.preventDefault();
      if (e.ctrlKey || e.metaKey) zoom(Math.exp(-e.deltaY * 0.01));
      else setView((v) => ({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY }));
    };
    el.addEventListener("wheel", h, { passive: false });
    return () => el.removeEventListener("wheel", h);
  }, [zoom]);

  const onMove = (e: RPE) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.sx;
    const dy = e.clientY - d.sy;
    if (Math.abs(dx) + Math.abs(dy) > 2) d.moved = true;
    if (d.kind === "pan") setView((v) => ({ ...v, x: d.ox + dx, y: d.oy + dy }));
    else setNodes((ns) => ns.map((n) => (n.id === d.id ? { ...n, x: d.ox + dx / view.k, y: d.oy + dy / view.k } : n)));
  };

  const edgePath = (a: CanvasDocNode, b: CanvasDocNode) => {
    if (Math.abs(a.x - b.x) < 20) {
      const x = a.x + a.w / 2;
      return `M${x} ${a.y + (heights[a.id] ?? 150)} L ${x} ${b.y}`;
    }
    const x1 = a.x + a.w;
    const y1 = a.y + (heights[a.id] ?? 120) / 2;
    const x2 = b.x;
    const y2 = b.y + ANCHOR;
    return `M${x1} ${y1} C ${x1 + 16} ${y1}, ${x2 - 16} ${y2}, ${x2} ${y2}`;
  };

  const ask = async (text: string, pick: ModelChoice | null) => {
    if (!sel || !text) return;
    const c = pick ?? catalog?.default ?? null;
    const d = describeChoice(catalog, c);
    const nodeId = sel;
    const push = (t: CanvasTurn) => setThreads((th) => ({ ...th, [nodeId]: { context: th[nodeId]?.context ?? "", turns: [...(th[nodeId]?.turns ?? []), t] } }));
    push({ id: `u${Date.now()}`, role: "user", author: API_MODE === "mock" ? "王晨" : me?.name ?? tx("我", "Me"), text });
    setBusy(c);
    const r = await api<{ reply: string }>("POST", `/canvases/${id}/nodes/${nodeId}/messages`, { body: { text, model: c?.model, reasoning: c?.reasoning } });
    setTimeout(() => {
      push({ id: `a${Date.now()}`, role: "assistant", author: "AI", text: r.reply, model: d?.name, reasoning_label: d?.effort, brand: d?.brand });
      setBusy(null);
    }, 900);
  };

  const doc = data;
  const inspector = selected ? (
    <DetailPanel
      pinned={pinned}
      onPin={setPinned}
      onClose={() => setSel(null)}
      label={tx("节点详情", "Node details")}
      header={
        <>
          <Tile icon={selected.icon as IconName} tone={selected.tone as Tone} />
          <div className="v3-col grow" style={{ gap: 0, minWidth: 0 }}>
            <Strong size={13.5}>{selected.title}</Strong>
            <Muted size={11.5}>{thread?.context ?? selected.sub}</Muted>
          </div>
        </>
      }
      footer={
            <div style={{ padding: "0 12px 12px", flex: "none" }}>
              <Composer minRows={1} maxRows={6} placeholder={tx("在这个节点继续…", "Continue in this node…")} sending={!!busy} turn={busy} choice={choice} onChoiceChange={setChoice} onSend={(t, _a, x) => void ask(t, x.choice)} />
            </div>
      }
    >
            <Tabs
              value={tab}
              onChange={setTab}
              pad="6px 16px 0"
              options={[
                { value: "chat", label: tx("对话", "Chat") },
                { value: "context", label: tx("上下文", "Context") },
                { value: "artifacts", label: tx("产物", "Artifacts"), count: doc?.artifacts.length },
              ]}
            />
            <div className="cv-inspector-body">
              {tab === "chat" && (
                <>
                  {(thread?.turns ?? []).length === 0 && <Muted>{tx("在这个节点提问：AI 只看到这个节点和它继承的上下文。", "Ask in this node: the AI only sees this node and the context it inherits.")}</Muted>}
                  {thread?.turns.map((t) => <Turn key={t.id} t={t} />)}
                  {busy && (
                    <Row gap={8}>
                      <AiAvatar size={22} />
                      <BrandMark size={16} state="thinking" label={tx("生成中", "Generating")} />
                      <Muted size={11.5}>{describeChoice(catalog, busy)?.label}</Muted>
                    </Row>
                  )}
                </>
              )}
              {tab === "context" && (
                <div className="v3-col" style={{ gap: 8, fontSize: "var(--fs-sm)" }}>
                  <Muted size={11.5}>{tx("本节点可见的上下文", "Context visible to this node")}</Muted>
                  {/* The list of context sources is sample content; real nodes show their thread context only. */}
                  {API_MODE === "mock" ? (
                    <>
                      <Row gap={6}><Icon name="layers" size={13} />{tx("主线：目标与范围 v4", "Main: Goal & scope v4")}</Row>
                      <Row gap={6}><Icon name="book" size={13} />{tx("Runbook「下游超时与重试风暴」", "Runbook “Retry storms”")}</Row>
                      <Row gap={6}><Icon name="file" size={13} />{tx("INC-1042 复盘", "INC-1042 postmortem")}</Row>
                    </>
                  ) : (
                    <span>{(sel && threads[sel]?.context) || tx("这个节点还没有记录上下文来源。", "This node has no recorded context sources yet.")}</span>
                  )}
                  <Muted size={11.5}>{tx("兄弟分支的对话不会进入本节点上下文。", "Sibling branches' conversations are not included.")}</Muted>
                </div>
              )}
              {tab === "artifacts" &&
                doc?.artifacts.map((a) => (
                  <Row key={a.id} gap={8} style={{ fontSize: "var(--fs-sm)" }}>
                    <Icon name="file" size={13} color="var(--muted)" />
                    <span className="grow">{a.title}</span>
                    <Muted size={11}>{a.sub}</Muted>
                  </Row>
                ))}
              <Spacer />
            </div>
    </DetailPanel>
  ) : null;
  const floatCover = useFloatCover();
  // Keep the selected node clear of the floating card by panning the canvas.
  useEffect(() => {
    if (!selected || pinned || !area.current) return;
    const right = view.x + (selected.x + selected.w) * view.k;
    const overlap = right - (area.current.clientWidth - floatCover) + 16;
    if (overlap > 0) setView((v) => ({ ...v, x: v.x - overlap }));
  }, [sel, pinned, floatCover]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Page
      flush
      crumbs={[tx("对话与画布", "Conversations & canvases"), tx("画布", "Canvases"), doc?.title ?? "…"]}
      actions={
        doc && (
          <>
            <KindTag kind={doc.kind} />
            <CanvasState status={doc.status} />
            {doc.env && <Env env={doc.env} />}
            <Segmented
              label={tx("视图", "View")}
              value="canvas"
              onChange={(v) => v === "chat" && toast(tx("该画布还没有关联的对话", "This canvas has no linked conversation yet"))}
              options={[
                { value: "chat", label: tx("对话", "Chat"), icon: "message" },
                { value: "canvas", label: tx("画布", "Canvas"), icon: "branch" },
              ]}
            />
            <AvatarGroup names={doc.participants} />
            <Btn kind="ghost" icon="file" onClick={() => {
                const blockText = (b: CanvasBlock) => (b.type === "kv" ? b.items.map(([k, v]) => `- ${k}: ${v}`).join("\n") : b.type === "tags" ? b.items.map((t) => `\`${t}\``).join(" ") : "");
                const md = [`# ${doc.title}`, "", doc.note, "", ...doc.nodes.flatMap((n) => [`## ${n.title}`, n.sub, "", ...n.blocks.map(blockText).filter(Boolean), ""])].join("\n");
                downloadFile(`${doc.id}.md`, md, "text/markdown");
                toast(tx("已导出为 {f}", "Exported as {f}", { f: `${doc.id}.md` }));
              }}>
              {tx("导出为文档", "Export as doc")}
            </Btn>
            {doc.proposal && (
              <Btn kind="primary" icon="shield" onClick={() => nav(doc.proposal!.href)}>
                {tx("提交审批", "Submit for approval")}
              </Btn>
            )}
          </>
        )
      }
    >
      <div className="cv-layout">
        <div
          ref={area}
          className="cv-area"
          style={{ backgroundPosition: `${view.x}px ${view.y}px`, backgroundSize: `${20 * view.k}px ${20 * view.k}px` }}
          onPointerDown={(e) => {
            if (e.button !== 0) return;
            drag.current = { kind: "pan", sx: e.clientX, sy: e.clientY, ox: view.x, oy: view.y };
            e.currentTarget.setPointerCapture(e.pointerId);
          }}
          onPointerMove={onMove}
          onPointerUp={() => {
            panned.current = !!drag.current?.moved;
            drag.current = null;
          }}
          onClick={(e) => {
            if (!panned.current && !(e.target as HTMLElement).closest(".cv-node, .dc-artifact, .cv-toolbar, .cv-zoom, .dp")) setSel(null);
          }}
        >
          {isLoading && (
            <div style={{ padding: 40 }}>
              <Skeleton w={260} h={160} />
            </div>
          )}
          <div ref={layer} className="cv-layer" style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})` }}>
            <svg className="cv-edges" width="4000" height="3000" aria-hidden="true">
              {doc?.edges.map((e) =>
                byId[e.from] && byId[e.to] ? (
                  <path
                    key={e.from + e.to}
                    d={edgePath(byId[e.from], byId[e.to])}
                    className={cx("cv-edge", e.live && "flow", e.dash && "evidence")}
                    style={{ stroke: e.tone ? (e.tone === "accent" ? "var(--accent)" : `var(--${e.tone})`) : "var(--faint)" }}
                  />
                ) : null,
              )}
            </svg>
            {nodes.map((n) => (
              <article
                key={n.id}
                data-node={n.id}
                className={cx("cv-node dc-node anim-pop", view.k < LITE_ZOOM && "lite", sel === n.id && "selected")}
                style={{ left: n.x, top: n.y, width: n.w }}
                onPointerDown={(e) => {
                  e.stopPropagation();
                  setSel(n.id);
                  if ((e.target as HTMLElement).closest("button,input,a")) return;
                  drag.current = { kind: "node", id: n.id, sx: e.clientX, sy: e.clientY, ox: n.x, oy: n.y };
                  area.current?.setPointerCapture(e.pointerId);
                }}
              >
                {view.k < LITE_ZOOM ? (
                  <div className="cv-lite" style={{ fontSize: `calc(var(--fs-sm) / ${view.k})` }}>
                    <div className="cv-lite-head">
                      <span className="cv-lite-dot" style={{ background: `var(--${n.tone === "neutral" ? "faint" : n.tone})` }} />
                      <b className="ellipsis">{n.title}</b>
                      {sel === n.id && <span className="cv-lite-tag accent">{tx("已选中", "Selected")}</span>}
                    </div>
                    <div className="cv-lite-line">{liteLine(n)}</div>
                  </div>
                ) : (
                  <>
                <div className="cv-head">
                    <Tile icon={n.icon as IconName} tone={n.tone as Tone} size={26} />
                    <div className="v3-col grow" style={{ gap: 0, lineHeight: 1.35, minWidth: 0 }}>
                      <span className="ellipsis" style={{ fontWeight: 600, fontSize: "var(--fs-body)" }}>
                        {n.title}
                      </span>
                      <span className="muted ellipsis" style={{ fontSize: "var(--fs-cap)" }}>
                        {n.sub}
                      </span>
                    </div>
                    {sel === n.id && <Badge tone="accent">{tx("已选中", "Selected")}</Badge>}
                  </div>
                  <div className="v3-col dc-body" style={{ gap: 8 }}>
                    {n.blocks.map((b, i) => (
                      <Block key={i} b={b} />
                    ))}
                  </div>
                    </>
                )}
              </article>
            ))}
            {doc?.artifacts.map((a) => (
              <a
                key={a.id}
                href={a.href ?? "#"}
                className="dc-artifact"
                style={{ left: a.x, top: a.y }}
                onPointerDown={(e) => e.stopPropagation()}
                onClick={(e) => {
                  e.preventDefault();
                  if (a.href) nav(a.href);
                  else toast(tx("产物预览：{t}", "Artifact preview: {t}", { t: a.title }));
                }}
              >
                {a.brand ? <Brand name={a.brand} size={26} /> : a.ext ? <span className={cx("att-ext", a.tone === "ok" ? "" : "link")} style={{ width: 26, height: 26 }}>{a.ext}</span> : <Tile icon={(a.icon ?? "file") as IconName} tone={a.tone ?? "neutral"} size={26} />}
                <span className="v3-col" style={{ gap: 0, minWidth: 0 }}>
                  <span className="ellipsis" style={{ fontSize: "var(--fs-sub)", fontWeight: 500 }}>
                    {a.title}
                  </span>
                  <Muted size={10.5}>{a.sub}</Muted>
                </span>
              </a>
            ))}
          </div>
          {doc && <span className="cv-status">{doc.note}</span>}
          <div className="cv-zoom" onPointerDown={(e) => e.stopPropagation()}>
            <IconBtn icon="zoomout" label={tx("缩小", "Zoom out")} size={26} onClick={() => zoom(0.85)} />
            <button type="button" className="tnum" onClick={() => setView({ x: 0, y: 0, k: 1 })} title={tx("重置", "Reset")}>
              {Math.round(view.k * 100)}%
            </button>
            <IconBtn icon="zoomin" label={tx("放大", "Zoom in")} size={26} onClick={() => zoom(1.18)} />
            <IconBtn icon="fit" label={tx("适应画布", "Fit")} size={26} onClick={fit} />
          </div>
          <div className="cv-toolbar" role="toolbar" aria-label={tx("画布工具", "Canvas tools")} onPointerDown={(e) => e.stopPropagation()}>
            <Btn kind="ghost" icon="fork" onClick={() => toast(tx("已从「{p}」派生新分支，继承其上下文", "Forked a branch from “{p}” with its context", { p: selected?.title ?? "" }))}>
              {tx("派生", "Fork")}
            </Btn>
            <Btn kind="ghost" icon="merge" onClick={() => toast(tx("请选择两个同级分支进行合并", "Select two sibling branches to merge"))}>
              {tx("合并", "Merge")}
            </Btn>
            <Btn kind="ghost" icon="writeback" onClick={() => toast(tx("回写会生成主线的新版本，可撤销", "Write-back creates a new main-line version you can revert"))}>
              {tx("回写", "Write back")}
            </Btn>
            <span className="v3-vdivider" />
            <Btn kind="ghost" icon="file" onClick={() => setTab("artifacts")}>
              {tx("产物", "Artifacts")}
            </Btn>
            <Btn kind="ghost" icon="grid" onClick={() => { if (data) setNodes(data.nodes); fit(); }}>
              {tx("自动布局", "Auto layout")}
            </Btn>
          </div>
          {selected && !pinned && inspector}
        </div>
        {selected && pinned && inspector}
      </div>
    </Page>
  );
}
