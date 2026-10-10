import { useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import type { Artifact, Part } from "../../api/types";
import { Badge, Brand, Btn, Card, Check, Cite, Code, Icon, IconBtn, Legend, Mono, Muted, Radio, Row, Segmented, Spacer, Strong, Sup, Tile, cx, type IconName } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { NextSteps } from "../../ui/Composer";
import { Heatmap, Lines, VBars, C, CS } from "../../charts";
import { useI18n } from "../../i18n";
import "./parts.css";
import { openExternal } from "../../ui/demo";
import { API_MODE } from "../../api/client";
import { RollbackSuggestionCard } from "../events/RollbackSuggestion";

/** Pin / add-to-report / download / edit buttons only have sample behaviour; real mode has no writer for them yet. */
const demo = API_MODE === "mock";
// Tool reads are measured in whole milliseconds, so a 0 is a read shorter than 1 ms, not a missing value.
const fmtMs = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : ms < 1 ? "<1 ms" : `${Math.round(ms)} ms`);

export type PartActions = {
  openArtifact?: (a: Partial<Artifact> & { kind: Artifact["kind"] }) => void;
  onStep?: (action: string, label: string) => void;
  onCite?: (n: number) => void;
  /** Only conversations linked to an investigation pass this; without it rollback suggestions aren't shown (PA-08). */
  openRollback?: (r: { serviceId: string; preselect: string }) => void;
};

function PartActionsBar({ onOpen, extra }: { onOpen?: () => void; extra?: ReactNode }) {
  const { tx } = useI18n();
  return (
    <Row gap={2}>
      {onOpen && <IconBtn icon="panel" label={tx("在右侧产物面板打开", "Open in artifact panel")} size={24} onClick={onOpen} />}
      {demo && <IconBtn icon="pin" label={tx("钉到 Canvas", "Pin to canvas")} size={24} onClick={() => toast(tx("已钉到 Canvas：成为证据节点", "Pinned to the canvas as an evidence node"))} />}
      {demo && <IconBtn icon="file" label={tx("加入报告", "Add to report")} size={24} onClick={() => toast(tx("已加入故障报告草稿", "Added to the report draft"))} />}
      {extra}
    </Row>
  );
}

function ToolCalls({ p }: { p: Extract<Part, { type: "tool_calls" }> }) {
  const { tx } = useI18n();
  const [open, setOpen] = useState(false);
  const running = p.calls.some((c) => c.status === "running");
  const kinds = [...new Set(p.calls.map((c) => c.connector))];
  return (
    <div className="part-tools">
      <button type="button" className="head" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <Icon name="right" size={12} style={{ transform: open ? "rotate(90deg)" : "none", transition: "transform 150ms" }} />
        {running ? <span className="v3-spinner spin" style={{ color: "var(--accent)" }} /> : <Icon name="check" size={13} color="var(--ok)" />}
        {/* The server's summary is "N tool calls" in English; say it in the reader's language. */}
        <b>{/^\d+ tool calls?$/.test(p.summary) ? tx("调用了 {n} 个工具", "Called {n} tools", { n: p.calls.length }) : p.summary}</b>
        {kinds.map((k) => (
          <Brand key={k} name={k} size={18} />
        ))}
        <Spacer />
        {/* duration_s is seconds and call durations are milliseconds; short tool steps read as ms, not "0.0 s". */}
        {!running && <span className="muted tnum">{tx("{t} tokens · {d}", "{t} tokens · {d}", { t: p.tokens >= 1000 ? (p.tokens / 1000).toFixed(1) + "k" : String(p.tokens), d: fmtMs(p.duration_s * 1000) })}</span>}
      </button>
      <div className={cx("body", open && "open")}>
        <ul>
          {p.calls.map((c) => (
            <li key={c.id}>
              <Brand name={c.connector} size={20} />
              <b style={{ fontWeight: 500 }}>{c.tool}</b>
              <Mono size={11.5}>{c.args}</Mono>
              <Spacer />
              <Muted size={11}>{c.status === "running" ? tx("进行中", "running") : fmtMs(c.duration_ms)}</Muted>
              {c.status === "running" ? <span className="v3-spinner spin" style={{ color: "var(--accent)" }} /> : c.status === "error" ? <Icon name="x" size={13} color="var(--danger)" /> : <Icon name="check" size={13} color="var(--ok)" />}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

function ChartPart({ p, actions }: { p: Extract<Part, { type: "chart" }>; actions?: PartActions }) {
  const { tx } = useI18n();
  const [kind, setKind] = useState<"line" | "area" | "bar" | "heat">("line");
  const [range, setRange] = useState<"30m" | "1h" | "6h">("1h");
  const n = p.series[0].values.length;
  const cut = range === "30m" ? Math.floor(n / 2) : 0;
  const s = p.series.map((x) => ({ ...x, values: x.values.slice(cut) }));
  const labels = p.labels.slice(cut);
  const markers = (p.markers ?? []).filter((m) => m.at >= cut).map((m) => ({ ...m, at: m.at - cut }));
  const heat = useMemo(() => {
    const v = s[0].values;
    const max = Math.max(...v) || 1;
    return Array.from({ length: 8 }, (_, r) => v.map((x) => Math.max(0, Math.min(0.99, 1 - Math.abs(r - (7 - (x / max) * 6)) * 0.3))));
  }, [s]);
  return (
    <Card className="part-card">
      <div className="bar">
        <Strong>{p.title}</Strong>
        <Spacer />
        <Segmented label={tx("图表类型", "Chart type")} value={kind} onChange={setKind} options={[{ value: "line", label: tx("折线", "Line") }, { value: "area", label: tx("面积", "Area") }, { value: "bar", label: tx("柱状", "Bar") }, { value: "heat", label: tx("热力", "Heat") }]} />
        <Segmented label={tx("时间范围", "Range")} value={range} onChange={setRange} options={[{ value: "30m", label: tx("30 分", "30m") }, { value: "1h", label: tx("1 小时", "1h") }, { value: "6h", label: tx("6 小时", "6h") }]} />
      </div>
      <div style={{ padding: "12px 14px 6px" }} key={kind + range} className="anim-fade">
        {kind === "line" && <Lines series={s.map((x, i) => ({ name: x.name, values: x.values, color: i ? C[0] : C[2] }))} height={130} threshold={p.threshold} markers={markers} labels={labels} format={(v) => `${Math.round(v)} ${p.unit}`} />}
        {kind === "area" && <Lines series={[{ name: s[0].name, values: s[0].values, color: C[2], fill: CS[2] }]} height={130} threshold={p.threshold} markers={markers} labels={labels} format={(v) => `${Math.round(v)} ${p.unit}`} />}
        {kind === "bar" && <VBars values={s[0].values} height={130} color={C[0]} highlight={new Set(markers.length ? Array.from({ length: s[0].values.length - markers[0].at }, (_, i) => i + markers[0].at) : [])} hiColor={C[2]} />}
        {kind === "heat" && <Heatmap matrix={heat} height={130} />}
      </div>
      <div className="foot">
        <Legend items={[...s.map((x, i): [string, string] => [x.name, i ? C[0] : C[2]]), ...(p.threshold !== undefined ? [[tx("阈值 {v} {u}", "Threshold {v} {u}", { v: p.threshold, u: p.unit }), "var(--danger)"] as [string, string]] : [])]} />
        <Spacer />
        {p.source && <Muted size={11.5}>{p.source}</Muted>}
        <PartActionsBar
          onOpen={() => actions?.openArtifact?.({ kind: "chart", title: p.title })}
          extra={p.deep_link && <IconBtn icon="external" label={tx("在 Grafana 中打开", "Open in Grafana")} size={24} onClick={() => openExternal("Grafana", p.deep_link!)} />}
        />
      </div>
    </Card>
  );
}

function TablePart({ p, actions }: { p: Extract<Part, { type: "table" }>; actions?: PartActions }) {
  const { tx } = useI18n();
  const [sort, setSort] = useState<{ i: number; dir: 1 | -1 } | null>({ i: 1, dir: -1 });
  const num = (v: string | number) => (typeof v === "number" ? v : parseFloat(String(v).replace(/[^\d.]/g, "")) || 0);
  const rows = sort ? [...p.rows].sort((a, b) => (num(a[sort.i]) - num(b[sort.i])) * sort.dir) : p.rows;
  const exportCsv = () => {
    const csv = [p.columns, ...p.rows].map((r) => r.map((c) => `"${String(c).replace(/"/g, '""')}"`).join(",")).join("\n");
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${p.title}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <Card className="part-card">
      <div className="bar">
        <Strong>{p.title}</Strong>
        {p.filter && <Badge tone="accent">{p.filter}</Badge>}
        <Spacer />
        <Btn kind="ghost" icon="download" h={24} onClick={exportCsv}>
          {tx("导出 CSV", "Export CSV")}
        </Btn>
        <IconBtn icon="panel" label={tx("在右侧打开", "Open on the right")} size={24} onClick={() => actions?.openArtifact?.({ kind: "table", title: p.title })} />
      </div>
      <table className="v3-table">
        <thead>
          <tr>
            {p.columns.map((c, i) => (
              <th key={i} className="sortable" onClick={() => setSort((s) => (s?.i === i ? { i, dir: s.dir === 1 ? -1 : 1 } : { i, dir: -1 }))}>
                {c} {sort?.i === i && <Icon name={sort.dir === 1 ? "up" : "down"} size={11} />}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, ri) => (
            <tr key={ri} style={{ height: 34 }}>
              {r.map((c, ci) => (
                <td key={ci} className={ci === 0 ? "mono" : "tnum"} style={{ fontSize: ci === 0 ? 11.5 : undefined, color: ci === 1 && num(c) > 1 && String(c).endsWith("%") ? "var(--danger)" : undefined }}>
                  {c}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}

export function CallGraph({ width = 520 }: { width?: number }) {
  return (
    <svg width="100%" viewBox={`0 0 ${width} 150`} className="call-graph" role="img" aria-hidden="true">
      <rect x="10" y="55" width="90" height="40" rx="8" style={{ fill: "var(--c1s)", stroke: "var(--c1)" }} />
      <rect x="160" y="55" width="110" height="40" rx="8" style={{ fill: "var(--c3s)", stroke: "var(--c3)" }} />
      <rect x="340" y="15" width="130" height="36" rx="8" style={{ fill: "var(--c6s)", stroke: "var(--c6)" }} />
      <rect x="340" y="100" width="130" height="36" rx="8" style={{ fill: "var(--c5s)", stroke: "var(--c5)" }} />
      <path className="flow" d="M100 75 L160 75" style={{ fill: "none", stroke: "var(--c1)", strokeWidth: 1.5 }} />
      <path className="flow" d="M270 70 C 305 70, 305 33, 340 33" style={{ fill: "none", stroke: "var(--c6)", strokeWidth: 1.5 }} />
      <path className="flow-fast" d="M270 80 C 305 80, 305 118, 340 118" style={{ fill: "none", stroke: "var(--c3)", strokeWidth: 1.5 }} />
      <text x="55" y="79" textAnchor="middle">gateway</text>
      <text x="215" y="79" textAnchor="middle">checkout-api</text>
      <text x="405" y="37" textAnchor="middle">payment-gateway</text>
      <text x="405" y="122" textAnchor="middle">inventory-service</text>
    </svg>
  );
}

function ImagePart({ p }: { p: Extract<Part, { type: "image" }> }) {
  const { tx } = useI18n();
  const [zoom, setZoom] = useState(false);
  return (
    <Card className="part-card">
      <div className="img-box">
        <CallGraph />
        <Btn h={26} icon="fit" onClick={() => setZoom(true)} style={{ position: "absolute", right: 10, top: 10 }}>
          {tx("放大", "Zoom")}
        </Btn>
      </div>
      <div className="foot">
        <Icon name="file" size={14} color="var(--muted)" />
        <span style={{ fontSize: "var(--fs-sub)" }}>{p.title}.png</span>
        <Muted size={11.5}>{p.caption}</Muted>
        <Spacer />
        {demo && <IconBtn icon="download" label={tx("下载", "Download")} size={24} onClick={() => toast(tx("已开始下载", "Download started"))} />}
      </div>
      <Dialog open={zoom} onClose={() => setZoom(false)} title={p.title} width={960}>
        <div style={{ background: "var(--sunken)", borderRadius: 8, padding: 16 }}>
          <CallGraph />
        </div>
        <Muted size={12}>{p.alt}</Muted>
      </Dialog>
    </Card>
  );
}

const FILE_ICON: Record<string, IconName> = { md: "file", csv: "grid", yaml: "code", log: "terminal", pdf: "file" };
function FilesPart({ p }: { p: Extract<Part, { type: "files" }> }) {
  const { tx } = useI18n();
  const [open, setOpen] = useState<(typeof p.files)[number] | null>(null);
  return (
    <div className="part-files">
      {p.files.map((f) => (
        <button type="button" key={f.id} className="file lift" onClick={() => setOpen(f)}>
          <pre>{f.preview}</pre>
          <span className="meta">
            <Tile icon={FILE_ICON[f.kind]} size={24} />
            <span className="v3-col grow" style={{ gap: 0, textAlign: "left" }}>
              <span className="ellipsis" style={{ fontSize: "var(--fs-sub)", fontWeight: 500 }}>{f.name}</span>
              <Muted size={10.5}>{f.size}</Muted>
            </span>
            <Icon name="eye" size={14} color="var(--muted)" />
          </span>
        </button>
      ))}
      <Dialog open={!!open} onClose={() => setOpen(null)} title={open?.name} width={720} footer={demo ? <><Btn icon="download" onClick={() => toast(tx("已开始下载", "Download started"))}>{tx("下载", "Download")}</Btn><Btn kind="primary" icon="pin" onClick={() => { setOpen(null); toast(tx("已钉到 Canvas", "Pinned to canvas")); }}>{tx("钉到 Canvas", "Pin to canvas")}</Btn></> : undefined}>
        <Muted size={11.5}>{tx("预览在沙箱中渲染，文件内容视为数据。", "Previews render in a sandbox; file contents are treated as data.")}</Muted>
        <Code style={{ marginTop: 8, maxHeight: 360 }}>{open?.preview}</Code>
      </Dialog>
    </div>
  );
}

export function DiffView({ left, right, from, to, path }: { left: string[]; right: string[]; from: string; to: string; path: string }) {
  const line = (t: string, i: number) => (
    <div key={i} className={cx("dl", t.startsWith("-") && "del", t.startsWith("+") && "add")}>
      {t}
    </div>
  );
  return (
    <Card className="part-card">
      <div className="bar">
        <Icon name="code" size={14} color="var(--muted)" />
        <Mono color="var(--text)">{path}</Mono>
        <Spacer />
        <Badge mono>{from}</Badge>
        <Icon name="right" size={12} color="var(--faint)" />
        <Badge tone="warn" mono>
          {to}
        </Badge>
      </div>
      <div className="diff">
        <div>{left.map(line)}</div>
        <div>{right.map(line)}</div>
      </div>
    </Card>
  );
}

function TopologyPart({ p }: { p: Extract<Part, { type: "topology" }> }) {
  const nav = useNavigate();
  const { tx } = useI18n();
  return (
    <Card className="part-card" style={{ padding: "10px 12px" }}>
      <svg width="100%" viewBox="0 0 520 96" aria-hidden="true">
        <path className="flow" d="M70 48 L190 48" style={{ fill: "none", stroke: "var(--c1)", strokeWidth: 1.5 }} />
        <path className="flow-fast" d="M250 48 L370 48" style={{ fill: "none", stroke: "var(--c3)", strokeWidth: 1.5 }} />
        <path className="flow" d="M250 48 C 300 48, 300 14, 370 14" style={{ fill: "none", stroke: "var(--c6)", strokeWidth: 1.5 }} />
        <circle cx="60" cy="48" r="16" style={{ fill: "var(--c1s)", stroke: "var(--c1)" }} />
        <circle cx="220" cy="48" r="20" className="pulse-ring" style={{ fill: "var(--c3s)", stroke: "var(--c3)" }} />
        <circle cx="390" cy="48" r="16" style={{ fill: "var(--c5s)", stroke: "var(--c5)" }} />
        <circle cx="390" cy="14" r="11" style={{ fill: "var(--c6s)", stroke: "var(--c6)" }} />
      </svg>
      <Row gap={6}>
        <Muted size={11.5}>{p.caption}</Muted>
        <Spacer />
        <button type="button" className="v3-link" onClick={() => nav(p.href)}>
          {tx("在资源拓扑中打开", "Open in topology")}
          <Icon name="right" size={12} />
        </button>
      </Row>
    </Card>
  );
}

function ProposalPart({ p }: { p: Extract<Part, { type: "proposal" }> }) {
  const { tx } = useI18n();
  const risk = { low: ["ok", tx("低风险", "Low risk")], medium: ["warn", tx("中风险", "Medium risk")], high: ["danger", tx("高风险", "High risk")] } as const;
  return (
    <div className="part-proposal">
      <Tile icon="shield" tone="accent" size={30} />
      <div className="v3-col grow" style={{ gap: 8 }}>
        <Row gap={6} wrap>
          <b style={{ fontSize: "var(--fs-body)" }}>{p.title}</b>
          <Badge tone={risk[p.risk][0]}>{risk[p.risk][1]}</Badge>
          {p.reversible && <Badge tone="ok">{tx("可回退", "Reversible")}</Badge>}
        </Row>
        <Muted size={11.5}>
          {p.target} · {p.detail}
        </Muted>
        <Row gap={6}>
          <Btn kind="primary" icon="shield" to={`/review/${p.id}`}>
            {tx("复核并批准", "Review & approve")}
          </Btn>
          <Btn to="/resources">{tx("查看影响范围", "Blast radius")}</Btn>
          {demo && (
            <Btn kind="ghost" onClick={() => toast(tx("已在对话中打开编辑", "Opened for editing"))}>
              {tx("修改提案", "Edit proposal")}
            </Btn>
          )}
        </Row>
      </div>
    </div>
  );
}

function FormPart({ p }: { p: Extract<Part, { type: "form" }> }) {
  const { tx } = useI18n();
  const [vals, setVals] = useState<Record<string, string | boolean | undefined>>(Object.fromEntries(p.fields.map((f) => [f.id, f.value])));
  const [done, setDone] = useState(false);
  return (
    <Card className="part-card" style={{ padding: "12px 14px", display: "flex", flexDirection: "column", gap: 10 }}>
      <Row gap={6}>
        <Icon name="info" size={14} color="var(--info)" />
        <Strong>{p.title}</Strong>
        {done && <Badge tone="ok">{tx("已提交", "Submitted")}</Badge>}
      </Row>
      <Row gap={24} align="flex-start" wrap>
        {p.fields.filter((f) => f.kind !== "check").map((f) => (
          <div key={f.id} className="v3-field">
            <span className="label">{f.label}</span>
            {f.kind === "radio" ? (
              <Row gap={14}>
                {f.options!.map((o) => (
                  <Radio key={o} name={f.id} label={o} checked={vals[f.id] === o} onChange={() => setVals((v) => ({ ...v, [f.id]: o }))} />
                ))}
              </Row>
            ) : (
              <select className="v3-native-select" value={String(vals[f.id])} onChange={(e) => setVals((v) => ({ ...v, [f.id]: e.target.value }))}>
                {f.options!.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
            )}
          </div>
        ))}
      </Row>
      {p.fields.filter((f) => f.kind === "check").map((f) => (
        <Check key={f.id} label={f.label} checked={!!vals[f.id]} onChange={(c) => setVals((v) => ({ ...v, [f.id]: c }))} />
      ))}
      <Row gap={6}>
        <Btn kind="primary" iconRight="right" disabled={done || !demo} onClick={() => { setDone(true); toast(tx("已提交，AI 继续分析", "Submitted — the AI continues")); }}>
          {tx("继续分析", "Continue")}
        </Btn>
        <Btn kind="ghost" disabled={done} onClick={() => setDone(true)}>
          {tx("跳过", "Skip")}
        </Btn>
      </Row>
    </Card>
  );
}

function TasksPart({ p }: { p: Extract<Part, { type: "tasks" }> }) {
  const done = p.items.filter((i) => i.state === "done").length;
  return (
    <Card className="part-card" style={{ padding: "12px 14px", display: "flex", flexDirection: "column", gap: 8 }}>
      <Row gap={8}>
        <Strong>
          {p.title} · {done} / {p.items.length}
        </Strong>
        <Spacer />
        <span className="v3-bar" style={{ width: 120, height: 5 }}>
          <span style={{ width: `${(100 * done) / p.items.length}%`, background: "var(--accent)" }} />
        </span>
      </Row>
      {p.items.map((t) => (
        <Row key={t.label} gap={8}>
          {t.state === "done" ? <Icon name="check" size={14} color="var(--ok)" /> : t.state === "running" ? <span className="v3-spinner spin" style={{ color: "var(--accent)" }} /> : <span className="todo-ring" />}
          <span style={{ fontSize: "var(--fs-sm)", color: t.state === "todo" ? "var(--muted)" : undefined, fontWeight: t.state === "running" ? 500 : undefined }}>{t.label}</span>
          <Spacer />
          {t.meta && <Muted size={11}>{t.meta}</Muted>}
        </Row>
      ))}
    </Card>
  );
}

/** Renders one message part. Shared by conversation view, canvas nodes and the artifact panel. */
/** Status codes the server sends (PA-60/71): failures, and answers that finished under a limit. */
const STATUS_FAILED = new Set(["reply_failed", "reply_interrupted"]);
const STATUS_LIMITED = new Set(["evidence_limit", "output_truncated", "canvas_history_too_large"]);

export function PartView({ part, actions, streamingText, streaming }: { part: Part; actions?: PartActions; streamingText?: boolean; /** the message is still being written */ streaming?: boolean }) {
  const nav = useNavigate();
  switch (part.type) {
    case "text":
      return <Markdown text={part.markdown} onCite={actions?.onCite} caretAtEnd={streamingText} />;
    case "tool_calls":
      return <ToolCalls p={part} />;
    case "chart":
      return <ChartPart p={part} actions={actions} />;
    case "table":
      return <TablePart p={part} actions={actions} />;
    case "image":
      return <ImagePart p={part} />;
    case "files":
      return <FilesPart p={part} />;
    case "diff":
      return <DiffView {...part} />;
    case "topology":
      return <TopologyPart p={part} />;
    case "proposal":
      return <ProposalPart p={part} />;
    case "form":
      return <FormPart p={part} />;
    case "tasks":
      return <TasksPart p={part} />;
    case "citations":
      return (
        <Row gap={6} wrap>
          {part.items.map((c) => (
            <Cite key={c.n} n={c.n} onClick={() => (c.ref?.kind === "change" ? nav("/changes") : c.ref?.kind === "runbook" ? nav("/runbooks") : actions?.onCite?.(c.n))}>
              {c.label}
            </Cite>
          ))}
        </Row>
      );
    case "rollback_suggestion":
      return actions?.openRollback ? <RollbackSuggestionCard s={part} onOpen={actions.openRollback} onCite={actions.onCite} /> : null;
    case "data_gap":
      return (
        <div className="part-gap">
          <Icon name="alert" size={14} />
          <span className="grow">{part.text}</span>
          <button type="button" className="v3-link" onClick={() => nav(part.href)}>
            <GapLink />
          </button>
        </div>
      );
    case "next_steps":
      return <NextSteps items={part.items.map((s) => ({ icon: s.icon as IconName, label: s.label, onClick: () => actions?.onStep?.(s.action, s.label) }))} />;
    case "artifacts":
      return null;
    case "status": {
      // Only a message still being written shows progress; afterwards a status is a finished step (check), a failure
      // or an answer cut short by a limit.
      const failed = !!part.code && STATUS_FAILED.has(part.code);
      const limited = !!part.code && STATUS_LIMITED.has(part.code);
      return (
        <Row gap={8} className={cx("part-status", failed && "danger", limited && "warn")}>
          {failed ? <Icon name="x" size={13} /> : limited ? <Icon name="alert" size={13} /> : streaming ? <span className="v3-spinner spin" /> : <Icon name="check" size={13} color="var(--ok)" />}
          <span>{part.text}</span>
        </Row>
      );
    }
  }
}

function GapLink() {
  const { tx } = useI18n();
  return (
    <>
      {tx("去接入", "Connect data")}
      <Icon name="right" size={12} />
    </>
  );
}
