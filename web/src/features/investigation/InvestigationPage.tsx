import { matchesSearch } from "../../lib/search";
import SelectField from "../../components/SelectField";
import ScopeFilter from "../../components/ScopeFilter";
import ContextInspector from "./ContextInspector";
import AgentPanel from "./AgentPanel";
import EvidenceTelemetry from "./EvidenceTelemetry";
import MessageList, { type ListMessage } from "../conversation/MessageList";
import RichMessage from "../conversation/RichMessage";
import { applyStepEvent, type RunStep } from "../conversation/steps";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Controls,
  Handle,
  Position,
  applyNodeChanges,
  type Node,
  type NodeProps,
  type NodeChange,
  type ReactFlowInstance,
  type Edge,
} from "@xyflow/react";
import {
  Activity,
  ArrowLeft,
  ArrowRight,
  BookOpen,
  Check,
  ChevronDown,
  ChevronRight,
  Code2,
  Database,
  Eye,
  FileText,
  GitBranch,
  GitMerge,
  History,
  LayoutPanelLeft,
  Maximize2,
  MessageSquare,
  Minus,
  MoreHorizontal,
  Network,
  PanelRightClose,
  Pin,
  Plus,
  RotateCcw,
  Search,
  Send,
  Square,
  TriangleAlert,
  User,
  X,
} from "../../lib/icons";
import {
  Area,
  AreaChart,
  CartesianGrid,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { Modal } from "../../components/ui";
import { useWorkspace } from "../../lib/workspace";
import { useDraftStore } from "../../lib/drafts";
import { request, post, patch, ApiError, expireSession } from "../../lib/api";
import {
  previewInvestigation,
  previewChart,
  makePreview,
  previewSteps,
  type CanvasInvestigation,
  type CanvasBranch,
  type CanvasEvidence,
  type ChatMessage,
  type MergePreview,
} from "./preview";
import "@xyflow/react/dist/style.css";
import "./investigation.css";
import InvestigationLoop from "./InvestigationLoop";

type Display = "expanded" | "collapsed" | "minimal" | "hidden";
type Placement = "canvas" | "floating" | "dock";
type CardLayout = {
  display?: Display;
  placement?: Placement;
  x?: number;
  y?: number;
};
type Layout = {
  cards?: Record<string, CardLayout>;
  drafts?: Record<string, string>;
  selected?: string;
  viewport?: { x: number; y: number; zoom: number };
};
type Revision = {
  revision: number;
  created_at: string;
  message_count: number;
  provenance: { kind: string; source_branch_id?: string };
  messages?: ChatMessage[];
  evidence?: CanvasEvidence[];
};
type BranchData = Record<string, unknown> & {
  branch: CanvasBranch;
  evidence: CanvasEvidence[];
  active: boolean;
  display: Display;
  busy: boolean;
  draft: string;
  readOnly: boolean;
  onSelect: () => void;
  onLayout: (change: CardLayout) => void;
  onDraft: (v: string) => void;
  onSend: () => void;
  onFork: () => void;
  onMerge: () => void;
  preview: boolean;
  onCite: (evidenceId: string) => void;
};
// Serial writes prevent a slower old layout save from overwriting a newer draft.
const layoutWrites = new Map<string, Promise<unknown>>();
function persistLayout(id: string, layout: Layout) {
  const previous = layoutWrites.get(id) || Promise.resolve();
  const next = previous
    .catch(() => undefined)
    .then(() =>
      request(`/investigations/${id}/layout`, {
        method: "PUT",
        body: JSON.stringify({ layout }),
      }),
    );
  layoutWrites.set(id, next);
  void next
    .finally(() => {
      if (layoutWrites.get(id) === next) layoutWrites.delete(id);
    })
    .catch(() => undefined);
  return next;
}
const activeRun = (status: string) =>
  ["queued", "running", "cancelling"].includes(status);
const shortTime = (time: string) =>
  new Date(time).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
  });
const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));
function BranchIcon({ branch }: { branch: CanvasBranch }) {
  return !branch.parent_id ? (
    <TriangleAlert size={23} />
  ) : branch.title.includes("代码") ? (
    <Code2 size={24} />
  ) : branch.title.includes("上游") ? (
    <Network size={23} />
  ) : (
    <Database size={24} />
  );
}
function CardTools({
  onChange,
  expanded = true,
}: {
  onChange: (change: CardLayout) => void;
  expanded?: boolean;
}) {
  return (
    <div
      className="inv-card-tools nodrag nopan"
      onClick={(event) => event.stopPropagation()}
      onFocus={(event) => event.stopPropagation()}
    >
      <button
        title="浮动并固定卡片"
        aria-label="浮动并固定卡片"
        onClick={() => onChange({ placement: "floating" })}
      >
        <Pin size={13} />
      </button>
      <button
        title={expanded ? "折叠卡片" : "展开卡片"}
        aria-label={expanded ? "折叠卡片" : "展开卡片"}
        onClick={() =>
          onChange({ display: expanded ? "collapsed" : "expanded" })
        }
      >
        {expanded ? <Minus size={15} /> : <Maximize2 size={13} />}
      </button>
      <details className="inv-menu">
        <summary title="卡片选项" aria-label="卡片选项">
          <MoreHorizontal size={16} />
        </summary>
        <div className="inv-menu-content">
          <button onClick={() => onChange({ display: "minimal" })}>
            缩为图标
          </button>
          <button
            onClick={() => onChange({ placement: "dock", display: "expanded" })}
          >
            停靠右侧
          </button>
          <button
            onClick={() =>
              onChange({ placement: "canvas", display: "expanded" })
            }
          >
            返回画布
          </button>
          <button onClick={() => onChange({ display: "hidden" })}>
            隐藏卡片
          </button>
        </div>
      </details>
    </div>
  );
}
// Canvas cards share the chat renderer: markdown, step summary, citations.
function NodeMessage({ data, message }: { data: BranchData; message: ChatMessage }) {
  const citations = (message.evidence_ids ?? []).flatMap((eid) => {
    const e = data.evidence.find((v) => v.id === eid);
    return e ? [{ id: e.id, title: e.title }] : [];
  });
  return (
    <RichMessage
      compact
      role={message.role === "user" ? "user" : "assistant"}
      content={message.content}
      runId={message.run_id}
      steps={data.preview ? previewSteps[message.id] : undefined}
      live={!data.preview}
      citations={citations}
      onCite={data.onCite}
    />
  );
}
function BranchCard({ data }: NodeProps<Node<BranchData>>) {
  const { branch, evidence, active, display, busy } = data;
  const last = branch.messages.filter((m) => m.role === "assistant").at(-1);
  return (
    <article
      className={`inv-node ${!branch.parent_id ? "inv-root" : "inv-child"} ${active ? "is-selected" : ""} ${display === "minimal" ? "is-minimal" : ""}`}
      tabIndex={0}
      onFocus={data.onSelect}
      onClick={data.onSelect}
      aria-label={`${branch.title}调查节点`}
    >
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
      {display === "minimal" ? (
        <button
          className="inv-node-icon"
          title={`展开 ${branch.title}`}
          onClick={() => data.onLayout({ display: "expanded" })}
        >
          <BranchIcon branch={branch} />
        </button>
      ) : (
        <>
          <CardTools
            onChange={data.onLayout}
            expanded={display === "expanded"}
          />
          <div className="inv-node-heading">
            <span
              className={`inv-node-icon ${!branch.parent_id ? "is-alert" : ""}`}
            >
              <BranchIcon branch={branch} />
            </span>
            <div>
              <h3>{branch.title}</h3>
              <span className="muted">
                {busy
                  ? "正在取证…"
                  : branch.parent_id
                    ? `独立上下文 · v${branch.revision}`
                    : "主调查 · 发布异常"}
              </span>
            </div>
          </div>
          {display === "expanded" && (
            <>
              {last || branch.messages.length ? (
                <div className="inv-node-summary nodrag nowheel">
                  <NodeMessage
                    data={data}
                    message={last || branch.messages.at(-1)!}
                  />
                </div>
              ) : (
                <p className="inv-node-summary">
                  开始对话，让 AI 收集证据并建立调查分支。
                </p>
              )}
              <button
                className="inv-evidence-link nodrag"
                onClick={data.onSelect}
              >
                {evidence.length} 条证据 <ChevronRight size={14} />
              </button>
              {active && (
                <>
                  <div className="inv-node-messages">
                    {branch.messages.slice(-2).map((m) => (
                      <div className="inv-node-message" key={m.id}>
                        <span className="inv-avatar">
                          {m.role === "user" ? <User size={17} /> : "A³"}
                        </span>
                        <div>
                          <b>{m.role === "user" ? "你" : "AI"}</b>
                          <small>{shortTime(m.created_at)}</small>
                          <NodeMessage data={data} message={m} />
                        </div>
                      </div>
                    ))}
                  </div>
                  <form
                    className="inv-inline-composer nodrag nopan"
                    onSubmit={(e) => {
                      e.preventDefault();
                      data.onSend();
                    }}
                  >
                    <input
                      aria-label="节点对话"
                      placeholder="继续提问或执行操作…"
                      value={data.draft}
                      onChange={(e) => data.onDraft(e.target.value)}
                      disabled={data.readOnly}
                    />
                    <button
                      title="发送节点消息"
                      disabled={busy || data.readOnly || !data.draft.trim()}
                    >
                      <Send size={17} />
                    </button>
                  </form>
                </>
              )}
            </>
          )}
          {display !== "expanded" && (
            <span className="inv-node-mini-status">
              {busy ? "调查中" : "上下文已保存"} <Check size={14} />
            </span>
          )}
          <div className="inv-node-hover-actions nodrag">
            <button
              onClick={(e) => {
                e.stopPropagation();
                data.onFork();
              }}
              disabled={data.readOnly}
            >
              <GitBranch size={13} />
              派生
            </button>
            <button
              onClick={(e) => {
                e.stopPropagation();
                data.onMerge();
              }}
              disabled={data.readOnly}
            >
              <GitMerge size={13} />
              合并
            </button>
          </div>
        </>
      )}
    </article>
  );
}
type EvidenceData = {
  evidence: CanvasEvidence;
  preview: boolean;
  display: Display;
  onLayout: (change: CardLayout) => void;
};
function EvidenceCard({
  data,
  inFlow = false,
}: {
  data: EvidenceData;
  inFlow?: boolean;
}) {
  return (
    <article
      className={`inv-metric-node ${data.display === "minimal" ? "is-minimal" : ""}`}
    >
      {inFlow && <Handle type="target" position={Position.Left} />}
      {data.display === "minimal" ? (
        <button
          className="inv-node-icon"
          title={`展开 ${data.evidence.title}`}
          onClick={() => data.onLayout({ display: "expanded" })}
        >
          <Activity size={22} />
        </button>
      ) : (
        <>
          <CardTools
            onChange={data.onLayout}
            expanded={data.display === "expanded"}
          />
          <header>
            <span className="inv-small-icon">
              <Activity size={18} />
            </span>
            <h3>{data.evidence.title}</h3>
          </header>
          {data.display === "expanded" && (
            <>
              {data.preview ? (
                <MetricChart kind="wait" />
              ) : (
                <EvidenceTelemetry
                  kind={data.evidence.kind}
                  content={data.evidence.content}
                />
              )}
              <footer>{data.evidence.source}</footer>
            </>
          )}
        </>
      )}
    </article>
  );
}
function EvidenceNode({ data }: NodeProps<Node<EvidenceData>>) {
  return <EvidenceCard data={data} inFlow />;
}
function canvasEvidenceFor(
  inv: CanvasInvestigation | undefined,
  preview: boolean,
) {
  return (inv?.evidence || [])
    .filter((e) => !preview || e.id === "e-metrics")
    .map((e) => ({
      key: preview ? "metric-evidence" : e.id,
      evidence: preview ? { ...e, title: "连接等待时间上升" } : e,
    }));
}

const nodeTypes = { branch: BranchCard, evidence: EvidenceNode };
function MetricChart({ kind }: { kind: "errors" | "wait" }) {
  return (
    <div
      className="inv-chart"
      aria-label={
        kind === "errors"
          ? "示意 HTTP 5xx 错误率曲线"
          : "示意数据库连接等待曲线"
      }
    >
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart
          data={previewChart}
          margin={{ left: 0, right: 12, top: 16, bottom: 0 }}
        >
          <CartesianGrid strokeDasharray="3 3" vertical={false} />
          <XAxis
            dataKey="time"
            minTickGap={44}
            tick={{ fontSize: "var(--type-caption)", fill: "var(--muted)" }}
            axisLine={false}
            tickLine={false}
          />
          <YAxis
            width={52}
            unit={kind === "errors" ? "%" : "ms"}
            tick={{ fontSize: "var(--type-caption)", fill: "var(--muted)" }}
            axisLine={false}
            tickLine={false}
          />
          <Tooltip contentStyle={{ background: "var(--panel)", color: "var(--text)", border: "1px solid var(--border)", fontSize: "var(--type-small)" }} />
          <ReferenceLine
            x="14:02"
            stroke="#7754f5"
            strokeDasharray="5 4"
            label={{
              value: "14:03",
              position: "insideTopRight",
              fontSize: "var(--type-caption)",
              fill: "#7754f5",
            }}
          />
          <Area
            type="monotone"
            dataKey={kind}
            stroke={kind === "errors" ? "#f04f61" : "#7356ee"}
            fill={kind === "errors" ? "#f04f6116" : "#7356ee16"}
            strokeWidth={2}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
export default function InvestigationPage() {
  return (
    <ReactFlowProvider>
      <InvestigationWorkspace />
    </ReactFlowProvider>
  );
}
function InvestigationWorkspace() {
  const workspace = useWorkspace();
  const { preview, user, application, environment, models, notify } = workspace;
  const params = useParams();
  const navigate = useNavigate();
  const [search, setSearch] = useSearchParams();
  const view = search.get("view") || "canvas";
  const id = preview
    ? "preview-investigation"
    : params.id || workspace.investigationId;
  const qc = useQueryClient();
  const citation = useDraftStore((s) => s.pendingCitation);
  const setCitation = useDraftStore((s) => s.setCitation);
  const pendingPrompt = useDraftStore((s) => s.pendingPrompt);
  const setPrompt = useDraftStore((s) => s.setPrompt);
  const pendingRequests = useRef<Record<string, { text: string; id: string }>>(
    {},
  );
  const [demo, setDemo] = useState<CanvasInvestigation>(() =>
    structuredClone(previewInvestigation),
  );
  const [title, setTitle] = useState("");
  const [creating, setCreating] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const query = useQuery({
    queryKey: ["investigation", id],
    queryFn: () => request<CanvasInvestigation>(`/investigations/${id}`),
    enabled: !!id && !preview,
  });
  const list = useQuery({
    queryKey: ["investigations"],
    queryFn: () => request<{ items: CanvasInvestigation[] }>("/investigations"),
    enabled: !preview,
  });
  const inv = preview ? demo : query.data;
  const investigationApplication = workspace.applications.find(
    (a) => a.id === inv?.application_id,
  );
  const investigationEnvironment = investigationApplication?.environments?.find(
    (e) => e.id === inv?.environment_id,
  );
  const actualApplicationName = inv
    ? investigationApplication?.name || inv.application_id
    : application?.name || "选择应用";
  const actualEnvironmentName = inv
    ? preview
      ? "生产（示例）"
      : investigationEnvironment?.name || inv.environment_id || "历史环境未记录"
    : environment?.name || "尚未选择环境";
  const [layout, setLayout] = useState<Layout>({});
  const [layoutReady, setLayoutReady] = useState(false);
  const [layoutDirty, setLayoutDirty] = useState(false);
  const [layoutSaving, setLayoutSaving] = useState(false);
  const [layoutError, setLayoutError] = useState(false);
  const saveVersion = useRef(0);
  const layoutRef = useRef(layout);
  layoutRef.current = layout;
  const dirtyRef = useRef(layoutDirty);
  dirtyRef.current = layoutDirty;
  const currentId = useRef(id);
  currentId.current = id;
  const [selected, setSelected] = useState("");
  const [sideTab, setSideTab] = useState("chat");
  const [sidebar, setSidebar] = useState(true);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [nodes, setNodes] = useState<Node[]>([]);
  const flow = useRef<ReactFlowInstance | null>(null);
  const [stream, setStream] = useState<
    Record<string, { run: string; attempt: string; text: string }>
  >({});
  const [eventStatus, setEventStatus] = useState("连接中");
  const eventSeq = useRef(0);
  const [forkTitle, setForkTitle] = useState<string | null>(null);
  const [forkBranch, setForkBranch] = useState("");
  const [mergeSource, setMergeSource] = useState("");
  const [mergeTarget, setMergeTarget] = useState("");
  const [mergePreview, setMergePreview] = useState<MergePreview | null>(null);
  const [mergeSelected, setMergeSelected] = useState<string[]>([]);
  const [mergeMode, setMergeMode] = useState("messages");
  const [conflictReviewed, setConflictReviewed] = useState(false);
  const [revisions, setRevisions] = useState<Revision[]>([]);
  const [revisionDetail, setRevisionDetail] = useState<Revision | null>(null);
  const [historyError, setHistoryError] = useState("");
  const [evidenceSearch, setEvidenceSearch] = useState("");
  const [evidenceHidden, setEvidenceHidden] = useState<string[]>([]);
  const [focusedEvidence, setFocusedEvidence] = useState("");
  useEffect(() => {
    if (!focusedEvidence || sideTab !== "evidence") return;
    const frame = requestAnimationFrame(() =>
      document
        .getElementById(`evidence-${focusedEvidence}`)
        ?.scrollIntoView({ block: "nearest", behavior: "smooth" }),
    );
    const timer = window.setTimeout(() => setFocusedEvidence(""), 2400);
    return () => {
      cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, [focusedEvidence, sideTab]);
  const [logsVisible, setLogsVisible] = useState(true);
  const [notesCollapsed, setNotesCollapsed] = useState(true);
  const [contextCollapsed, setContextCollapsed] = useState(true);

  const readOnly = user.role === "viewer" || inv?.status === "closed";
  const branch =
    inv?.branches?.find((b) => b.id === selected) ||
    inv?.branches?.find((b) => !b.parent_id) ||
    inv?.branches?.[0];
  const branchBusy = !!inv?.runs?.some(
    (r) => r.branch_id === branch?.id && activeRun(r.status),
  );
  const setView = (next: string) => {
    const nextSearch = new URLSearchParams(search);
    nextSearch.set("view", next);
    setSearch(nextSearch);
  };
  const updateLayout = useCallback((update: (prev: Layout) => Layout) => {
    saveVersion.current++;
    dirtyRef.current = true;
    setLayout((previous) => {
      const next = update(previous);
      layoutRef.current = next;
      return next;
    });
    setLayoutDirty(true);
    setLayoutError(false);
  }, []);
  const cardChange = useCallback(
    (bid: string, change: CardLayout) =>
      updateLayout((prev) => ({
        ...prev,
        cards: { ...prev.cards, [bid]: { ...prev.cards?.[bid], ...change } },
      })),
    [updateLayout],
  );
  const selectBranch = useCallback(
    (bid: string) => {
      setSelected(bid);
      setSidebar(true);
      updateLayout((prev) => ({ ...prev, selected: bid }));
    },
    [updateLayout],
  );
  const draftChange = useCallback(
    (bid: string, value: string) =>
      updateLayout((prev) => ({
        ...prev,
        drafts: { ...prev.drafts, [bid]: value },
      })),
    [updateLayout],
  );
  const refresh = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["investigation", id] });
    void qc.invalidateQueries({ queryKey: ["investigations"] });
  }, [qc, id]);
  useEffect(() => {
    if (!id) return;
    workspace.setInvestigationId(id);
  }, [id, workspace.setInvestigationId]);
  useEffect(() => {
    let cancelled = false;
    setLayoutReady(false);
    setLayoutDirty(false);
    setLayoutError(false);
    setLayout({});
    setSelected("");
    setMergePreview(null);
    setStream({});
    eventSeq.current = 0;
    if (preview) {
      setLayout({
        selected: "database",
        cards: {
          code: { display: "collapsed" },
          upstream: { display: "collapsed" },
        },
      });
      setSelected("database");
      setLayoutReady(true);
      return;
    }
    if (!id) return;
    request<{ layout: Layout }>(`/investigations/${id}/layout`)
      .then((v) => {
        if (cancelled) return;
        setLayout(v.layout || {});
        setSelected(v.layout?.selected || "");
        setLayoutReady(true);
      })
      .catch((e) => {
        if (!cancelled) {
          notify(`布局读取失败：${errorText(e)}`, "error");
          setLayoutError(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [id, preview]);
  useEffect(() => {
    if (!id || !layoutReady || !layoutDirty || preview) return;
    const version = saveVersion.current;
    const timer = window.setTimeout(() => {
      setLayoutSaving(true);
      persistLayout(id, layout)
        .then(() => {
          if (currentId.current !== id) return;
          if (saveVersion.current === version) {
            setLayoutDirty(false);
            dirtyRef.current = false;
          }
          setLayoutError(false);
        })
        .catch(() => {
          if (currentId.current === id) setLayoutError(true);
        })
        .finally(() => {
          if (currentId.current === id) setLayoutSaving(false);
        });
    }, 700);
    return () => clearTimeout(timer);
  }, [id, layout, layoutReady, layoutDirty, preview]);
  useEffect(() => {
    if (!id || preview) return;
    return () => {
      if (dirtyRef.current)
        void persistLayout(id, layoutRef.current).catch(() =>
          notify("离开调查时布局保存失败；请检查连接后恢复草稿。", "error"),
        );
    };
  }, [id, preview]);
  useEffect(() => {
    const handler = (event: BeforeUnloadEvent) => {
      if (!preview && dirtyRef.current) {
        event.preventDefault();
      }
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [preview]);
  useEffect(() => {
    if (layoutReady && layoutRef.current.viewport)
      void flow.current?.setViewport(layoutRef.current.viewport);
  }, [id, layoutReady]);
  useEffect(() => {
    if (!id || preview) return;
    const es = new EventSource(
      `/api/v1/investigations/${id}/events?after=${eventSeq.current}`,
      { withCredentials: true },
    );
    let timer: number | undefined;
    let checkingSession = false;
    let disconnected = false;
    es.onopen = () => setEventStatus("实时连接");
    es.onerror = () => {
      setEventStatus("重连中");
      if (checkingSession || disconnected) return;
      checkingSession = true;
      void request("/auth/me")
        .catch((error) => {
          if (error instanceof ApiError && error.status === 401) {
            disconnected = true;
            es.close();
            setEventStatus("登录已失效");
            expireSession();
          }
        })
        .finally(() => {
          checkingSession = false;
        });
    };
    es.addEventListener("update", (raw) => {
      try {
        const evt = JSON.parse((raw as MessageEvent).data) as {
          seq: number;
          type: string;
          branch_id?: string;
          payload: { run_id?: string; attempt_id?: string; delta?: string };
        };
        if (evt.seq <= eventSeq.current) return;
        eventSeq.current = evt.seq;
        if (evt.type === "attempt.step") {
          applyStepEvent(qc, evt.payload as unknown as RunStep);
          return;
        }
        if (evt.type === "attempt.delta" && evt.branch_id) {
          const bid = evt.branch_id;
          setStream((prev) => ({
            ...prev,
            [bid]: {
              run: evt.payload.run_id || "",
              attempt: evt.payload.attempt_id || "",
              text:
                (prev[bid]?.attempt === evt.payload.attempt_id
                  ? prev[bid].text
                  : "") + (evt.payload.delta || ""),
            },
          }));
        }
        if (!timer)
          timer = window.setTimeout(() => {
            refresh();
            timer = undefined;
          }, 250);
      } catch {
        setEventStatus("事件解析失败");
      }
    });
    return () => {
      disconnected = true;
      es.close();
      window.clearTimeout(timer);
    };
  }, [id, preview, refresh]);
  useEffect(() => {
    if (!inv) return;
    setStream((prev) =>
      Object.fromEntries(
        Object.entries(prev).filter(([, s]) =>
          inv.runs?.some((r) => r.id === s.run && activeRun(r.status)),
        ),
      ),
    );
  }, [inv]);
  useEffect(() => {
    if (
      !citation ||
      !inv ||
      !branch ||
      !layoutReady ||
      citation.investigationId !== inv.id
    )
      return;
    const target =
      inv.branches.find((b) => b.id === citation.branchId) || branch;
    const reference = `\n\n[Runbook 引用：${citation.title} · v${citation.version} · ${citation.documentId}]\n${citation.body}`;
    draftChange(
      target.id,
      (layoutRef.current.drafts?.[target.id] || "") + reference,
    );
    setSelected(target.id);
    setSidebar(true);
    setSideTab("chat");
    setCitation(null);
  }, [citation, inv?.id, branch?.id, layoutReady]);
  useEffect(() => {
    if (
      !pendingPrompt ||
      !inv ||
      !branch ||
      !layoutReady ||
      pendingPrompt.investigationId !== inv.id
    )
      return;
    const target =
      inv.branches.find((b) => b.id === pendingPrompt.branchId) || branch;
    if (pendingPrompt.clientRequestId)
      pendingRequests.current[target.id] = {
        text: pendingPrompt.content.trim(),
        id: pendingPrompt.clientRequestId,
      };
    const existing = layoutRef.current.drafts?.[target.id] || "";
    draftChange(
      target.id,
      existing
        ? `${existing}\n\n${pendingPrompt.content}`
        : pendingPrompt.content,
    );
    setSelected(target.id);
    setSidebar(true);
    setSideTab("chat");
    setPrompt(null);
  }, [pendingPrompt, inv?.id, branch?.id, layoutReady]);
  const perform = async <T,>(
    work: () => Promise<T>,
  ): Promise<T | undefined> => {
    setBusy(true);
    try {
      return await work();
    } catch (e) {
      notify(errorText(e), "error");
      return undefined;
    } finally {
      setBusy(false);
    }
  };
  const send = async (bid: string, text: string) => {
    if (!text.trim() || readOnly || busy) return;
    const b = inv?.branches.find((x) => x.id === bid);
    if (!b) return;
    if (preview) {
      notify("这是交互预览，未调用模型。进入实际工作区后可发起调查。", "info");
      return;
    }
    const model = b.model_id || models.find((m) => m.available)?.id;
    if (!model) {
      notify("请先配置可用模型。", "error");
      return;
    }
    const prior = pendingRequests.current[bid];
    const requestId =
      prior?.text === text.trim() ? prior.id : crypto.randomUUID();
    pendingRequests.current[bid] = { text: text.trim(), id: requestId };
    const result = await perform(() =>
      post(`/branches/${bid}/messages`, {
        content: text.trim(),
        model_id: model,
        client_request_id: requestId,
      }),
    );
    if (result) {
      delete pendingRequests.current[bid];
      draftChange(bid, "");
      refresh();
    }
  };
  const openFork = (bid: string) => {
    setForkBranch(bid);
    setForkTitle("");
  };
  const openMerge = (source: string, target?: string) => {
    setMergeSource(source);
    setMergeTarget(
      target ||
        inv?.branches.find((b) => b.id === source)?.parent_id ||
        inv?.branches.find((b) => b.id !== source)?.id ||
        "",
    );
    setMergePreview(null);
    setConflictReviewed(false);
    setMergeMode("messages");
    setView("merge");
  };
  const generateMerge = async () => {
    const src = inv?.branches.find((b) => b.id === mergeSource),
      target = inv?.branches.find((b) => b.id === mergeTarget);
    if (!src || !target || src.id === target.id) return;
    const result = preview
      ? makePreview(src, target)
      : await perform(() =>
          post<MergePreview>(`/branches/${target.id}/merge-preview`, {
            source_branch_id: src.id,
          }),
        );
    if (result) {
      setMergePreview(result);
      setMergeSelected((result.additions || []).map((m) => m.id));
      setConflictReviewed(false);
    }
  };
  const confirmMerge = async () => {
    if (!mergePreview || readOnly) return;
    if (preview) {
      notify("预览模式不会合并实际上下文。", "info");
      return;
    }
    setBusy(true);
    try {
      await post(`/branches/${mergePreview.target_branch_id}/merge`, {
        preview_id: mergePreview.id,
        mode: mergeMode,
        ...(mergeMode === "messages" ? { message_ids: mergeSelected } : {}),
      });
      notify("已创建目标分支的新上下文版本。", "success");
      setSelected(mergePreview.target_branch_id);
      setMergePreview(null);
      setView("canvas");
      refresh();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        setMergePreview(null);
        notify(
          "目标或来源已更新。请重新生成预览，审查新版本后再合并。",
          "error",
        );
        refresh();
      } else notify(errorText(e), "error");
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    if (view === "merge" && inv && !mergeSource) {
      const src = inv.branches.find((b) => b.parent_id) || inv.branches[0];
      if (src) {
        setMergeSource(src.id);
        setMergeTarget(
          src.parent_id || inv.branches.find((b) => b.id !== src.id)?.id || "",
        );
        if (preview && src.parent_id) {
          const target = inv.branches.find((b) => b.id === src.parent_id)!;
          const p = makePreview(
            src.id === "code"
              ? inv.branches.find((b) => b.id === "database")!
              : src,
            target,
          );
          setMergeSource(p.source_branch_id);
          setMergePreview(p);
          setMergeSelected(p.additions.map((m) => m.id));
        }
      }
    }
  }, [view, inv, mergeSource, preview]);
  useEffect(() => {
    setRevisionDetail(null);
    setRevisions([]);
    setHistoryError("");
    if (sideTab !== "context" || !branch) return;
    let cancelled = false;
    if (preview) {
      setRevisions([
        {
          revision: branch.revision,
          created_at: inv!.updated_at,
          message_count: branch.messages.length,
          provenance: { kind: "preview_snapshot" },
          messages: branch.messages,
        },
      ]);
      return;
    }
    request<{ items: Revision[] }>(`/branches/${branch.id}/revisions`)
      .then((v) => {
        if (!cancelled) setRevisions(v.items || []);
      })
      .catch((e) => {
        if (!cancelled) setHistoryError(errorText(e));
      });
    return () => {
      cancelled = true;
    };
  }, [branch?.id, sideTab, preview, inv?.updated_at]);
  useEffect(() => {
    if (!inv || !layoutReady) return;
    const branches = [...(inv.branches || [])].sort(
      (a, b) => Number(!!a.parent_id) - Number(!!b.parent_id),
    );
    const visible: Node[] = branches
      .filter(
        (b) =>
          (layout.cards?.[b.id]?.display || "expanded") !== "hidden" &&
          (!layout.cards?.[b.id]?.placement ||
            layout.cards[b.id].placement === "canvas"),
      )
      .map((b) => {
        const i = branches.findIndex((candidate) => candidate.id === b.id);
        const card = layout.cards?.[b.id] || {};
        const branchEvidence = (inv.evidence || []).filter(
          (e) => e.branch_id === b.id,
        );
        return {
          id: b.id,
          type: "branch",
          position: {
            x: card.x ?? (i === 0 ? 25 : i === 2 ? 405 : 445),
            y:
              card.y ??
              (i === 0
                ? 340
                : i === 1
                  ? 110
                  : i === 2
                    ? 260
                    : 710 + (i - 3) * 220),
          },
          data: {
            branch: b,
            evidence: branchEvidence,
            active: b.id === branch?.id,
            display: card.display || "expanded",
            busy: (inv.runs || []).some(
              (r) => r.branch_id === b.id && activeRun(r.status),
            ),
            draft: layout.drafts?.[b.id] || "",
            readOnly: !!readOnly,
            onSelect: () => selectBranch(b.id),
            onLayout: (c: CardLayout) => cardChange(b.id, c),
            onDraft: (value: string) => draftChange(b.id, value),
            onSend: () =>
              void send(b.id, layoutRef.current.drafts?.[b.id] || ""),
            onFork: () => openFork(b.id),
            onMerge: () => openMerge(b.id),
            preview,
            onCite: (eid: string) => {
              selectBranch(b.id);
              openEvidence(eid);
            },
          },
        };
      });
    canvasEvidenceFor(inv, preview).forEach(({ key, evidence }, i) => {
      const stored = layout.cards?.[key] || {};
      if (
        stored.display === "hidden" ||
        (stored.placement && stored.placement !== "canvas")
      )
        return;
      visible.push({
        id: key,
        type: "evidence",
        position: {
          x: stored.x ?? (preview ? 805 : 850 + Math.floor(i / 3) * 320),
          y: stored.y ?? (preview ? 330 : 180 + (i % 3) * 290),
        },
        data: {
          evidence,
          preview,
          display: stored.display || "expanded",
          onLayout: (change: CardLayout) => {
            cardChange(key, change);
            if (change.placement === "dock") setSidebar(true);
          },
        },
      });
    });
    setNodes((previous) =>
      visible.map((n) => {
        const existing = previous.find((p) => p.id === n.id);
        return existing?.dragging
          ? { ...n, position: existing.position, dragging: true }
          : n;
      }),
    );
  }, [inv, layout, layoutReady, branch?.id, readOnly, preview, busy]);
  const onNodesChange = useCallback((changes: NodeChange[]) => {
    setNodes((ns) => applyNodeChanges(changes, ns));
  }, []);
  const edges = useMemo<Edge[]>(() => {
    if (!inv) return [];
    const visible = new Set(nodes.map((n) => n.id));
    const list: Edge[] = inv.branches
      .filter(
        (b) => b.parent_id && visible.has(b.id) && visible.has(b.parent_id),
      )
      .map((b) => ({
        id: `${b.parent_id}-${b.id}`,
        source: b.parent_id!,
        target: b.id,
        type: "default",
        style: {
          stroke: b.id === branch?.id ? "#8365ff" : "#a9b4c8",
          strokeWidth: 1.6,
        },
        animated: false,
      }));
    if (!preview)
      for (const evidence of inv.evidence || []) {
        if (visible.has(evidence.branch_id) && visible.has(evidence.id))
          list.push({
            id: `evidence-${evidence.id}`,
            source: evidence.branch_id,
            target: evidence.id,
            type: "default",
            style: { stroke: "#b4a4ee", strokeWidth: 1.4 },
            animated: false,
          });
      }
    if (preview && visible.has("database") && visible.has("metric-evidence"))
      list.push({
        id: "db-evidence",
        source: "database",
        target: "metric-evidence",
        type: "default",
        style: { stroke: "#8365ff", strokeWidth: 1.6 },
        animated: false,
      });
    return list;
  }, [inv, nodes, branch?.id, preview]);
  const create = async () => {
    if (!application || !environment || !title.trim()) return;
    setCreating(true);
    try {
      const v = await post<CanvasInvestigation>("/investigations", {
        application_id: application.id,
        environment_id: environment?.id,
        title: title.trim(),
      });
      workspace.setInvestigationId(v.id);
      qc.setQueryData(["investigation", v.id], v);
      await qc.invalidateQueries({ queryKey: ["investigations"] });
      navigate(`/investigations/${v.id}`);
      setCreateOpen(false);
      setTitle("");
    } catch (e) {
      notify(errorText(e), "error");
    } finally {
      setCreating(false);
    }
  };
  const hidden =
    inv?.branches.filter((b) => layout.cards?.[b.id]?.display === "hidden") ||
    [];
  const floating =
    inv?.branches.filter(
      (b) =>
        layout.cards?.[b.id]?.placement === "floating" &&
        layout.cards[b.id].display !== "hidden",
    ) || [];
  const docked =
    inv?.branches.filter(
      (b) =>
        layout.cards?.[b.id]?.placement === "dock" &&
        layout.cards[b.id].display !== "hidden",
    ) || [];
  const canvasEvidence = canvasEvidenceFor(inv, preview);
  const hiddenEvidence = canvasEvidence.filter(
    (e) => layout.cards?.[e.key]?.display === "hidden",
  );
  const floatingEvidence = canvasEvidence.filter(
    (e) =>
      layout.cards?.[e.key]?.placement === "floating" &&
      layout.cards[e.key].display !== "hidden",
  );
  const dockedEvidence = canvasEvidence.filter(
    (e) =>
      layout.cards?.[e.key]?.placement === "dock" &&
      layout.cards[e.key].display !== "hidden",
  );
  const detachedEvidence = (entry: {
    key: string;
    evidence: CanvasEvidence;
  }) => (
    <EvidenceCard
      data={{
        evidence: entry.evidence,
        preview,
        display: layout.cards?.[entry.key]?.display || "expanded",
        onLayout: (change) => {
          cardChange(entry.key, change);
          if (change.placement === "dock") setSidebar(true);
        },
      }}
    />
  );
  const evidenceById = new Map((inv?.evidence ?? []).map((e) => [e.id, e]));
  const citationsFor = (m: ListMessage) =>
    (m.evidence_ids ?? []).flatMap((eid) => {
      const e = evidenceById.get(eid);
      return e ? [{ id: e.id, title: e.title, kind: e.kind }] : [];
    });
  // Citations jump to the evidence tab and flash the cited item.
  const openEvidence = (eid: string) => {
    setSideTab("evidence");
    setSidebar(true);
    setFocusedEvidence(eid);
  };
  const renderEvidence = (eid: string) => {
    const e = evidenceById.get(eid);
    if (!e) return null;
    return preview && e.kind === "metrics" ? (
      <MetricChart kind="wait" />
    ) : (
      <EvidenceTelemetry kind={e.kind} content={e.content} />
    );
  };
  const displayMessages: ListMessage[] = branch
    ? [
        ...(branch.messages || []),
        ...(stream[branch.id]?.text && branchBusy
          ? [
              {
                id: `stream-${stream[branch.id].attempt}`,
                role: "assistant",
                content: stream[branch.id].text,
                created_at: new Date().toISOString(),
                run_id: stream[branch.id].run,
                streaming: true,
              },
            ]
          : []),
      ]
    : [];
  const selectModel = async (model: string) => {
    if (!branch || readOnly) return;
    if (preview) {
      setDemo((prev) => ({
        ...prev,
        branches: prev.branches.map((b) =>
          b.id === branch.id ? { ...b, model_id: model } : b,
        ),
      }));
      return;
    }
    await perform(() => patch(`/branches/${branch.id}`, { model_id: model }));
    refresh();
  };
  const toolbar = (
    <nav className="inv-view-tabs" aria-label="调查视图">
      <button
        className={view === "canvas" ? "active" : ""}
        onClick={() => setView("canvas")}
      >
        <GitBranch size={15} />
        调查画布
      </button>
      <button
        className={view === "evidence" ? "active" : ""}
        onClick={() => setView("evidence")}
      >
        <LayoutPanelLeft size={15} />
        证据对照
      </button>
      <button
        className={view === "merge" ? "active" : ""}
        onClick={() => {
          if (branch) openMerge(branch.id);
        }}
        disabled={readOnly || !inv || inv.branches.length < 2}
      >
        <GitMerge size={15} />
        上下文合并
      </button>
    </nav>
  );
  return (
    <section
      className={`inv-workspace ${view === "evidence" ? "inv-evidence-workspace" : view === "canvas" ? "inv-canvas-workspace" : view === "merge" ? "inv-merge-view" : ""} ${!sidebar ? "inv-sidebar-hidden" : ""}`}
    >
      <header className="inv-page-header">
        <div>
          <div className="inv-title-row">
            <h1>{inv?.title || "会话与画布"}</h1>
            {inv && (
              <span className="badge">
                {inv.status === "closed" ? "已关闭" : "调查中"}
              </span>
            )}
          </div>
          <p>
            {actualApplicationName} <span>·</span>{" "}
            <span title={inv ? "当前调查固定的应用环境" : undefined}>
              {actualEnvironmentName}
            </span>{" "}
            <span>·</span>{" "}
            {inv
              ? new Date(inv.created_at).toLocaleString("zh-CN")
              : "从问题出发，让证据连接成答案"}{" "}
            {inv && !preview && (
              <>
                <span>·</span>
                <span
                  className={eventStatus === "实时连接" ? "inv-live" : "muted"}
                >
                  {eventStatus}
                </span>
              </>
            )}
          </p>
        </div>
        <div className="inv-header-actions">
          <SelectField
            aria-label="选择会话"
            value={id || ""}
            onChange={(e) => {
              workspace.setInvestigationId(e.target.value);
              navigate(
                `/investigations/${e.target.value}${preview ? "?preview=1" : ""}`,
              );
            }}
            disabled={preview}
          >
            <option value="">选择会话</option>
            {preview ? (
              <option value="preview-investigation">发布异常调查</option>
            ) : (
              list.data?.items
                ?.filter(
                  (i) => !application || i.application_id === application.id,
                )
                .map((i) => (
                  <option key={i.id} value={i.id}>
                    {i.title}
                  </option>
                ))
            )}
          </SelectField>
          <button
            className="btn"
            disabled={readOnly || preview || !application || !environment}
            onClick={() => setCreateOpen(true)}
          >
            <Plus size={15} />
            新建会话
          </button>
          {inv && (
            <Link
              className="btn"
              to={{
                search: `?${new URLSearchParams({ ...Object.fromEntries(search), panel: "approval" })}`,
              }}
            >
              <RotateCcw size={15} />
              恢复提案
            </Link>
          )}
        </div>
      </header>
      {inv && (
        <InvestigationLoop
          inv={inv}
          preview={preview}
          onEvidence={() => setView("evidence")}
        />
      )}
      {!inv && <ScopeFilter single />}
      {toolbar}
      <Modal
        open={createOpen}
        onOpenChange={setCreateOpen}
        title="新建会话"
        description={`应用：${application?.name || "尚未选择"}`}
      >
        <form
          className="inv-modal-form"
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <label>
            问题描述
            <input
              className="field"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="例如：发布后 checkout-api 5xx 上升"
              maxLength={100}
            />
          </label>
          <button
            className="btn btn-primary"
            disabled={creating || !title.trim()}
          >
            {creating ? "创建中…" : "开始会话"}
          </button>
        </form>
      </Modal>
      {!inv ? (
        <div className="inv-no-investigation">
          <GitBranch size={38} />
          <h2>
            {query.isLoading ? "正在加载会话…" : "从一个问题，开始一张画布"}
          </h2>
          <p>
            {query.error
              ? errorText(query.error)
              : "选择应用，开始会话。AI 的回答、证据和分支会保存在同一画布。"}
          </p>
          <button
            className="btn btn-primary"
            disabled={user.role === "viewer" || !application}
            onClick={() => setCreateOpen(true)}
          >
            <Plus size={16} />
            新建会话
          </button>
        </div>
      ) : (
        <>
          {layoutError && (
            <div className="inv-layout-warning">
              个人布局{layoutReady ? "保存" : "读取"}失败，当前更改尚未保存。
              <button
                className="btn btn-ghost"
                onClick={() => {
                  if (layoutReady) {
                    setLayoutDirty(true);
                    setLayout({ ...layout });
                  } else {
                    setLayoutReady(true);
                    setLayoutError(false);
                    notify(
                      "已使用默认布局；移动卡片后将保存为新的个人布局。",
                      "info",
                    );
                  }
                }}
              >
                {layoutReady ? "重试保存" : "使用默认布局"}
              </button>
            </div>
          )}
          {view === "canvas" && (
            <div className="inv-canvas-row">
              <div className="inv-flow-wrap">
                <ReactFlow
                  nodes={nodes}
                  edges={edges}
                  nodeTypes={nodeTypes}
                  onNodesChange={onNodesChange}
                  onInit={(instance) => {
                    flow.current = instance;
                    if (layout.viewport)
                      void instance.setViewport(layout.viewport);
                  }}
                  onNodeDragStop={(_, n) =>
                    cardChange(n.id, { x: n.position.x, y: n.position.y })
                  }
                  onMoveEnd={(_, viewport) => {
                    if (layoutReady)
                      updateLayout((prev) => ({ ...prev, viewport }));
                  }}
                  defaultViewport={layout.viewport || { x: 0, y: 0, zoom: 1 }}
                  minZoom={0.25}
                  maxZoom={1.6}
                  nodesConnectable={false}
                  deleteKeyCode={null}
                >
                  <Background color="#cdd4e3" gap={25} size={1} />
                  <Controls showInteractive={false} />
                </ReactFlow>
                <div className="inv-canvas-bottom">
                  <button onClick={() => setView("evidence")}>
                    <FileText size={21} />
                    日志
                  </button>
                  <button onClick={() => setView("evidence")}>
                    <Activity size={21} />
                    指标
                  </button>
                  <Link to={`/knowledge${preview ? "?preview=1" : ""}`}>
                    <BookOpen size={21} />
                    Runbook
                  </Link>
                  <span />
                  <button onClick={() => setRestoreOpen(!restoreOpen)}>
                    <Eye size={20} />
                    收纳{" "}
                    {hidden.length + hiddenEvidence.length > 0 &&
                      `(${hidden.length + hiddenEvidence.length})`}
                  </button>
                  <button
                    onClick={() => branch && openFork(branch.id)}
                    disabled={readOnly}
                  >
                    <Plus size={20} />
                    分支
                  </button>
                </div>
                {restoreOpen && (
                  <div className="inv-restore-panel">
                    <header>
                      <b>卡片收纳</b>
                      <button
                        className="icon-button"
                        aria-label="关闭收纳"
                        onClick={() => setRestoreOpen(false)}
                      >
                        <X size={16} />
                      </button>
                    </header>
                    {hidden.length + hiddenEvidence.length ? (
                      hidden.map((b) => (
                        <button
                          key={b.id}
                          onClick={() =>
                            cardChange(b.id, {
                              display: "expanded",
                              placement: "canvas",
                            })
                          }
                        >
                          <Database size={16} />
                          {b.title}
                          <Eye size={14} />
                        </button>
                      ))
                    ) : (
                      <p className="muted">没有隐藏的卡片</p>
                    )}
                    {hiddenEvidence.map((e) => (
                      <button
                        key={e.key}
                        onClick={() =>
                          cardChange(e.key, {
                            display: "expanded",
                            placement: "canvas",
                          })
                        }
                      >
                        <Activity size={16} />
                        {e.evidence.title}
                        <Eye size={14} />
                      </button>
                    ))}
                    <button
                      onClick={() => {
                        updateLayout((prev) => ({
                          ...prev,
                          cards: {},
                          viewport: { x: 0, y: 0, zoom: 1 },
                        }));
                        void flow.current?.setViewport({ x: 0, y: 0, zoom: 1 });
                      }}
                    >
                      恢复默认布局
                    </button>
                  </div>
                )}
                {floating.map((b, i) => (
                  <aside
                    key={b.id}
                    className={`inv-floating-card ${layout.cards?.[b.id]?.display === "minimal" ? "is-minimal" : ""}`}
                    style={{ top: 24 + i * 44, right: 24 + i * 24 }}
                  >
                    {layout.cards?.[b.id]?.display === "minimal" ? (
                      <button
                        className="inv-node-icon"
                        title={`展开 ${b.title}`}
                        onClick={() =>
                          cardChange(b.id, { display: "expanded" })
                        }
                      >
                        <BranchIcon branch={b} />
                      </button>
                    ) : (
                      <>
                        <CardTools
                          onChange={(c) => cardChange(b.id, c)}
                          expanded={
                            (layout.cards?.[b.id]?.display || "expanded") ===
                            "expanded"
                          }
                        />
                        <h3>
                          <Database size={18} />
                          {b.title}
                        </h3>
                        {(layout.cards?.[b.id]?.display || "expanded") ===
                          "expanded" && (
                          <p>{b.messages.at(-1)?.content || "暂无消息"}</p>
                        )}
                        <button
                          className="btn btn-ghost"
                          onClick={() => selectBranch(b.id)}
                        >
                          继续调查
                        </button>
                      </>
                    )}
                  </aside>
                ))}
                {floatingEvidence.map((e, i) => (
                  <aside
                    className="inv-evidence-floating"
                    key={e.key}
                    style={{
                      top: 32 + (floating.length + i) * 44,
                      right: 24 + (floating.length + i) * 24,
                    }}
                  >
                    {detachedEvidence(e)}
                  </aside>
                ))}
                {!sidebar && (
                  <button
                    className="inv-open-sidebar btn"
                    onClick={() => setSidebar(true)}
                  >
                    <MessageSquare size={17} />
                    节点对话
                  </button>
                )}
                <div className="inv-layout-status">
                  {preview
                    ? "视觉预览 · 示例数据"
                    : layoutSaving
                      ? "正在保存布局…"
                      : layoutDirty
                        ? "布局待保存"
                        : "个人布局已保存"}
                </div>
              </div>
              {sidebar && branch && (
                <aside className="inv-sidebar">
                  <header>
                    <span className="inv-node-icon">
                      <BranchIcon branch={branch} />
                    </span>
                    <h2>{branch.title}</h2>
                    <button
                      className="icon-button"
                      title="收起对话面板"
                      onClick={() => setSidebar(false)}
                    >
                      <PanelRightClose size={17} />
                    </button>
                  </header>
                  <nav className="inv-side-tabs">
                    <button
                      className={sideTab === "chat" ? "active" : ""}
                      onClick={() => setSideTab("chat")}
                    >
                      对话
                    </button>
                    <button
                      className={sideTab === "evidence" ? "active" : ""}
                      onClick={() => setSideTab("evidence")}
                    >
                      证据 (
                      {
                        inv.evidence.filter((e) => e.branch_id === branch.id)
                          .length
                      }
                      )
                    </button>
                    <button
                      className={sideTab === "context" ? "active" : ""}
                      onClick={() => setSideTab("context")}
                    >
                      上下文
                    </button>
                    <button
                      className={sideTab === "agent" ? "active" : ""}
                      onClick={() => setSideTab("agent")}
                    >
                      Agent
                    </button>
                  </nav>
                  <div className="inv-side-body">
                    {sideTab === "chat" ? (
                      <>
                        <div className="inv-context-summary">
                          <b>调查范围</b>
                          <span>
                            <Network size={14} />
                            {branch.parent_id ? "继承自主调查" : "主调查"}
                          </span>
                          <p>
                            冻结父上下文，当前分支独立调查。
                            {branch.parent_id &&
                              `继承版本 v${branch.inherited_parent_revision ?? 1}`}
                          </p>
                          {branch.parent_has_updates && (
                            <button
                              onClick={() =>
                                openMerge(branch.parent_id!, branch.id)
                              }
                              disabled={readOnly}
                            >
                              父节点有更新，预览同步 <ChevronRight size={14} />
                            </button>
                          )}
                        </div>
                        <MessageList
                          key={branch.id}
                          className="inv-chat-history"
                          messages={displayMessages}
                          live={!preview}
                          stepsFor={(m) =>
                            preview ? previewSteps[m.id] : undefined
                          }
                          citationsFor={citationsFor}
                          onCite={openEvidence}
                          renderEvidence={renderEvidence}
                        />
                        {!displayMessages.length && (
                          <div className="inv-chat-empty">
                            <MessageSquare size={26} />
                            <p>描述现象，或指定需要检查的对象。</p>
                          </div>
                        )}
                      </>
                    ) : sideTab === "agent" ? (
                      <AgentPanel
                        key={branch.id}
                        branch={branch}
                        runs={(inv.runs ?? []).filter(
                          (r) => r.branch_id === branch.id,
                        )}
                        preview={preview}
                        previewSteps={
                          branch.id === "database"
                            ? previewSteps["m-db4"]
                            : undefined
                        }
                        renderEvidence={renderEvidence}
                      />
                    ) : sideTab === "evidence" ? (
                      <div className="inv-evidence-list">
                        {inv.evidence
                          .filter((e) => e.branch_id === branch.id)
                          .map((e) => (
                            <details
                              key={e.id}
                              id={`evidence-${e.id}`}
                              className={
                                focusedEvidence === e.id ? "is-focused" : ""
                              }
                              open
                            >
                              <summary>{e.title}</summary>
                              <small>
                                {e.source} · {shortTime(e.created_at)}
                              </small>
                              <pre>{e.content}</pre>
                            </details>
                          ))}
                        {!inv.evidence.some(
                          (e) => e.branch_id === branch.id,
                        ) && (
                          <p className="muted">当前分支还没有已提交证据。</p>
                        )}
                      </div>
                    ) : (
                      <div className="inv-history">
                        <h3>上下文版本 v{branch.revision}</h3>
                        <p className="muted">
                          原始消息与来源保留，可审查继承与合并历史。
                        </p>
                        {historyError && (
                          <p className="inv-error">{historyError}</p>
                        )}
                        {revisions.map((r) => (
                          <button
                            key={r.revision}
                            onClick={() => {
                              if (preview) setRevisionDetail(r);
                              else
                                void perform(() =>
                                  request<Revision>(
                                    `/branches/${branch.id}/revisions/${r.revision}`,
                                  ),
                                ).then((v) => {
                                  if (v) setRevisionDetail(v);
                                });
                            }}
                          >
                            <History size={16} />
                            <span>
                              v{r.revision} · {r.provenance.kind}
                              <small>
                                {r.message_count} 条消息 ·{" "}
                                {shortTime(r.created_at)}
                              </small>
                            </span>
                            <ChevronRight size={14} />
                          </button>
                        ))}
                        {revisionDetail && (
                          <section>
                            <h4>v{revisionDetail.revision} 快照</h4>
                            {revisionDetail.messages?.map((m) => (
                              <p key={m.id}>
                                <b>{m.role}</b> {m.content}
                              </p>
                            ))}
                          </section>
                        )}
                        <button
                          className="btn"
                          disabled={readOnly}
                          onClick={() => openFork(branch.id)}
                        >
                          <GitBranch size={15} />
                          从当前版本派生
                        </button>
                      </div>
                    )}
                  </div>
                  {sideTab === "chat" && (
                    <div className="inv-sidebar-composer">
                      <form
                        onSubmit={(e) => {
                          e.preventDefault();
                          void send(
                            branch.id,
                            layout.drafts?.[branch.id] || "",
                          );
                        }}
                      >
                        <textarea
                          aria-label="继续调查"
                          placeholder={
                            readOnly ? "当前为只读状态" : "继续提问或执行操作…"
                          }
                          value={layout.drafts?.[branch.id] || ""}
                          onChange={(e) =>
                            draftChange(branch.id, e.target.value)
                          }
                          disabled={readOnly}
                          onKeyDown={(e) => {
                            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                              e.preventDefault();
                              void send(
                                branch.id,
                                layout.drafts?.[branch.id] || "",
                              );
                            }
                          }}
                        />
                        <div>
                          <SelectField
                            aria-label="下一轮使用的模型"
                            value={
                              branch.model_id ||
                              models.find((m) => m.available)?.id ||
                              ""
                            }
                            disabled={readOnly || busy}
                            onChange={(e) => void selectModel(e.target.value)}
                          >
                            {preview && (
                              <option value="preview-model">
                                设计预览模型
                              </option>
                            )}
                            {!models.length && !preview && (
                              <option value="">未配置模型</option>
                            )}
                            {models.map((m) => (
                              <option
                                key={m.id}
                                value={m.id}
                                disabled={!m.available}
                              >
                                {m.name || m.id}
                              </option>
                            ))}
                          </SelectField>
                          <button
                            className="icon-button"
                            type="submit"
                            aria-label="发送调查消息"
                            disabled={
                              busy ||
                              branchBusy ||
                              readOnly ||
                              !(layout.drafts?.[branch.id] || "").trim()
                            }
                          >
                            <Send size={18} />
                          </button>
                        </div>
                      </form>
                      <small>
                        模型切换从下一轮生效 · ⌘ / Ctrl + Enter 发送
                      </small>
                    </div>
                  )}
                  <details className="inv-run-history">
                    <summary>
                      执行记录 (
                      {inv.runs.filter((r) => r.branch_id === branch.id).length}
                      )
                    </summary>
                    {inv.runs
                      .filter((r) => r.branch_id === branch.id)
                      .map((r) => (
                        <div key={r.id}>
                          <span>
                            {r.status} · {r.model_id}
                          </span>
                          <ContextInspector runId={r.id} />
                          {r.error && <p className="inv-error">{r.error}</p>}
                          {activeRun(r.status) && !readOnly && (
                            <button
                              className="btn btn-ghost"
                              disabled={busy}
                              onClick={() =>
                                void perform(() =>
                                  post(`/runs/${r.id}/cancel`, {}),
                                ).then(refresh)
                              }
                            >
                              <Square size={12} />
                              取消任务
                            </button>
                          )}
                        </div>
                      ))}
                  </details>
                  {docked.map((b) => (
                    <section key={b.id} className="inv-docked-card">
                      {layout.cards?.[b.id]?.display === "minimal" ? (
                        <button
                          className="inv-node-icon"
                          title={`展开 ${b.title}`}
                          onClick={() =>
                            cardChange(b.id, { display: "expanded" })
                          }
                        >
                          <BranchIcon branch={b} />
                        </button>
                      ) : (
                        <>
                          <CardTools
                            onChange={(c) => cardChange(b.id, c)}
                            expanded={
                              (layout.cards?.[b.id]?.display || "expanded") ===
                              "expanded"
                            }
                          />
                          <h3>{b.title}</h3>
                          {(layout.cards?.[b.id]?.display || "expanded") ===
                            "expanded" && <p>{b.messages.at(-1)?.content}</p>}
                          <button
                            className="btn btn-ghost"
                            onClick={() =>
                              cardChange(b.id, { placement: "canvas" })
                            }
                          >
                            返回画布
                          </button>
                        </>
                      )}
                    </section>
                  ))}
                  {dockedEvidence.map((e) => (
                    <section className="inv-evidence-docked" key={e.key}>
                      {detachedEvidence(e)}
                    </section>
                  ))}
                </aside>
              )}
              {!sidebar && (docked.length > 0 || dockedEvidence.length > 0) && (
                <aside className="inv-dock-rail" aria-label="停靠卡片">
                  {docked.map((b) => (
                    <section className="inv-docked-card" key={b.id}>
                      {layout.cards?.[b.id]?.display === "minimal" ? (
                        <button
                          className="inv-node-icon"
                          title={`展开 ${b.title}`}
                          onClick={() =>
                            cardChange(b.id, { display: "expanded" })
                          }
                        >
                          <BranchIcon branch={b} />
                        </button>
                      ) : (
                        <>
                          <CardTools
                            onChange={(change) => cardChange(b.id, change)}
                            expanded={
                              (layout.cards?.[b.id]?.display || "expanded") ===
                              "expanded"
                            }
                          />
                          <button
                            className="btn btn-ghost"
                            onClick={() => selectBranch(b.id)}
                          >
                            {b.title}
                          </button>
                          {(layout.cards?.[b.id]?.display || "expanded") ===
                            "expanded" && <p>{b.messages.at(-1)?.content}</p>}
                        </>
                      )}
                    </section>
                  ))}
                  {dockedEvidence.map((e) => (
                    <section className="inv-evidence-docked" key={e.key}>
                      {detachedEvidence(e)}
                    </section>
                  ))}
                </aside>
              )}
            </div>
          )}
          {view === "evidence" && (
            <div className="inv-evidence-stage">
              <div className="inv-evidence-grid">
                <article className="inv-compare-card">
                  <header>
                    <FileText size={18} />
                    <h2>发布配置差异</h2>
                    <Pin size={15} />
                    <button
                      className="icon-button"
                      title="隐藏配置卡片"
                      onClick={() =>
                        setEvidenceHidden((v) => [...v, "configuration"])
                      }
                    >
                      <X size={15} />
                    </button>
                  </header>
                  <p className="muted">· {actualApplicationName}</p>
                  {evidenceHidden.includes("configuration") ? (
                    <button
                      className="btn"
                      onClick={() =>
                        setEvidenceHidden((v) =>
                          v.filter((x) => x !== "configuration"),
                        )
                      }
                    >
                      <Eye size={16} />
                      恢复配置卡片
                    </button>
                  ) : (
                    <>
                      {preview && (
                        <div className="inv-version-compare">
                          <div>
                            <b>v2.8.0</b>
                            <span>
                              发布前<small>13:47</small>
                            </span>
                          </div>
                          <ArrowRight size={20} />
                          <div>
                            <b>v2.8.1</b>
                            <span>
                              发布后<small>14:03</small>
                            </span>
                          </div>
                        </div>
                      )}
                      <div className="inv-search">
                        <Search size={16} />
                        <input
                          placeholder="搜索配置或证据…"
                          aria-label="搜索证据"
                          value={evidenceSearch}
                          onChange={(e) => setEvidenceSearch(e.target.value)}
                        />
                      </div>
                      {preview ? (
                        <>
                          <h3 className="inv-config-heading">
                            <ChevronDown size={16} />
                            数据库配置
                          </h3>
                          <div className="inv-config-table">
                            {[
                              ["connection_pool.max", "200", "50"],
                              ["connection_pool.min", "20", "20"],
                              ["connection_pool.timeout", "30s", "30s"],
                              ["connection_pool.idle", "10s", "10s"],
                            ]
                              .filter((v) =>
                                matchesSearch(v[0], evidenceSearch),
                              )
                              .map((v, i) => (
                                <div key={v[0]}>
                                  <span>{v[0]}</span>
                                  <code>{v[1]}</code>
                                  <code className={i === 0 ? "changed" : ""}>
                                    {v[2]}
                                  </code>
                                </div>
                              ))}
                          </div>
                          {["应用配置", "JVM 配置", "其他变更"].map((x, i) => (
                            <details className="inv-config-other" key={x}>
                              <summary>
                                {x}
                                <span>{[3, 2, 1][i]}</span>
                              </summary>
                              <p className="muted">
                                示意分组；当前场景仅展示连接池配置差异。
                              </p>
                            </details>
                          ))}
                        </>
                      ) : (
                        <div className="inv-evidence-list">
                          {inv.evidence
                            .filter((e) =>
                              matchesSearch(
                                `${e.title} ${e.content}`,
                                evidenceSearch,
                              ),
                            )
                            .map((e) => (
                              <details key={e.id}>
                                <summary>{e.title}</summary>
                                <small>{e.source}</small>
                                <pre>{e.content}</pre>
                              </details>
                            ))}
                          {!inv.evidence.length && (
                            <p className="muted">
                              尚无调查证据。请从节点发起一次取证。
                            </p>
                          )}
                        </div>
                      )}
                    </>
                  )}
                </article>
                <div className="inv-compare-main">
                  <article className="inv-compare-context">
                    <button
                      onClick={() => setContextCollapsed(!contextCollapsed)}
                    >
                      <Network size={16} />
                      <b>调查上下文 · {inv.branches.length} 项</b>
                      {contextCollapsed ? (
                        <Plus size={15} />
                      ) : (
                        <Minus size={15} />
                      )}
                    </button>
                    {!contextCollapsed &&
                      inv.branches.map((b) => (
                        <button
                          key={b.id}
                          onClick={() => {
                            selectBranch(b.id);
                            setView("canvas");
                          }}
                        >
                          {b.title}
                          <ChevronRight size={14} />
                        </button>
                      ))}
                  </article>
                  <article className="inv-compare-card inv-chart-card">
                    <header>
                      <FileText size={18} />
                      <h2>错误率与连接等待</h2>
                      <Pin size={15} />
                      <button
                        className="icon-button"
                        title="折叠指标卡片"
                        onClick={() =>
                          setEvidenceHidden((v) =>
                            v.includes("metrics")
                              ? v.filter((x) => x !== "metrics")
                              : [...v, "metrics"],
                          )
                        }
                      >
                        {evidenceHidden.includes("metrics") ? (
                          <Maximize2 size={14} />
                        ) : (
                          <Minus size={15} />
                        )}
                      </button>
                    </header>
                    {!evidenceHidden.includes("metrics") && (
                      <>
                        <p className="muted">
                          · {actualApplicationName} ·{" "}
                          {preview
                            ? "核心指标对比 · 示例数据"
                            : "已提交的指标证据"}
                        </p>
                        {preview ? (
                          <div className="inv-chart-pair">
                            <section>
                              <h3>HTTP 5xx 错误率</h3>
                              <MetricChart kind="errors" />
                              <p>
                                <span className="inv-legend red" />
                                5xx 错误率
                              </p>
                            </section>
                            <section>
                              <h3>数据库连接等待时间</h3>
                              <MetricChart kind="wait" />
                              <p>
                                <span className="inv-legend" />
                                连接等待时间（p95）
                              </p>
                            </section>
                          </div>
                        ) : (
                          <div className="inv-evidence-list">
                            {inv.evidence
                              .filter((e) => e.kind.includes("metric"))
                              .map((e) => (
                                <details key={e.id} open>
                                  <summary>{e.title}</summary>
                                  <small>{e.source}</small>
                                  <pre>{e.content}</pre>
                                </details>
                              ))}
                            {!inv.evidence.some((e) =>
                              e.kind.includes("metric"),
                            ) && (
                              <p className="muted">
                                暂无指标证据。这里仅显示 Agent
                                已持久化的数据，不使用示意曲线。
                              </p>
                            )}
                          </div>
                        )}
                      </>
                    )}
                  </article>
                  <article className="inv-compare-notes">
                    <button onClick={() => setNotesCollapsed(!notesCollapsed)}>
                      <FileText size={17} />
                      <b>
                        分支笔记 ·{" "}
                        {inv.branches.filter((b) => b.parent_id).length} 条
                      </b>
                      {notesCollapsed ? (
                        <Plus size={15} />
                      ) : (
                        <Minus size={15} />
                      )}
                    </button>
                    {!notesCollapsed &&
                      inv.branches
                        .filter((b) => b.parent_id)
                        .map((b) => (
                          <div key={b.id}>
                            <strong>{b.title}</strong>
                            <p>{b.messages.at(-1)?.content || "暂无笔记"}</p>
                          </div>
                        ))}
                  </article>
                </div>
                <button
                  className="inv-icon-card"
                  title="展开相关日志"
                  onClick={() => setLogsVisible(!logsVisible)}
                >
                  <FileText size={23} />
                  <b>相关日志</b>
                  <span>
                    {inv.evidence.filter((e) => e.kind.includes("log")).length}
                  </span>
                </button>
              </div>
              {logsVisible ? (
                <section className="inv-log-dock">
                  <header>
                    <FileText size={18} />
                    <h3>运行日志</h3>
                    <small>已提交证据 · {preview ? "示例" : "只读"}</small>
                    <button
                      className="icon-button"
                      title="收起日志"
                      onClick={() => setLogsVisible(false)}
                    >
                      <Minus size={16} />
                    </button>
                  </header>
                  <div className="inv-log-table">
                    <div className="inv-log-table-header">
                      <span>时间</span>
                      <span>级别</span>
                      <span>服务</span>
                      <span>内容</span>
                    </div>
                    {inv.evidence
                      .filter((e) => e.kind.includes("log"))
                      .flatMap((e) =>
                        e.content.split("\n").map((line, i) => (
                          <div className="inv-log-row" key={`${e.id}-${i}`}>
                            <span>
                              {preview
                                ? line.slice(0, 8)
                                : shortTime(e.created_at)}
                            </span>
                            <span>
                              <b
                                className={
                                  line.includes("ERROR")
                                    ? "error"
                                    : line.includes("WARN")
                                      ? "warn"
                                      : "info"
                                }
                              >
                                {line.includes("ERROR")
                                  ? "ERROR"
                                  : line.includes("WARN")
                                    ? "WARN"
                                    : "INFO"}
                              </b>
                            </span>
                            <span>{actualApplicationName}</span>
                            <code>{preview ? line.slice(15) : line}</code>
                          </div>
                        )),
                      )}
                    {!inv.evidence.some((e) => e.kind.includes("log")) && (
                      <p className="muted">尚无已提交日志证据。</p>
                    )}
                  </div>
                </section>
              ) : (
                <button
                  className="btn inv-show-logs"
                  onClick={() => setLogsVisible(true)}
                >
                  <FileText size={16} />
                  展开运行日志
                </button>
              )}
            </div>
          )}
          {view === "merge" && (
            <div className="inv-merge-workspace">
              <div className="inv-merge-map">
                <Link
                  to="#"
                  onClick={(e) => {
                    e.preventDefault();
                    setView("canvas");
                  }}
                  className="inv-back"
                >
                  <ArrowLeft size={16} />
                  调查 / {inv.title}
                </Link>
                <div className="inv-merge-branches">
                  {inv.branches
                    .filter((b) => b.parent_id)
                    .map((b) => (
                      <button
                        key={b.id}
                        className={`inv-merge-node ${b.id === mergeSource ? "selected" : ""}`}
                        onClick={() => {
                          setMergeSource(b.id);
                          setMergePreview(null);
                        }}
                      >
                        <span className="inv-node-icon">
                          <BranchIcon branch={b} />
                        </span>
                        <div>
                          <h3>{b.title}</h3>
                          <p>独立调查 · v{b.revision}</p>
                          <small>
                            <History size={13} />
                            {shortTime(inv.created_at)}
                            <span className="badge">
                              {b.id === mergeSource ? "待合并" : "已保存"}
                            </span>
                          </small>
                        </div>
                      </button>
                    ))}
                </div>
                <div className="inv-merge-target-card">
                  <GitMerge size={22} />
                  <b>
                    {inv.branches.find((b) => b.id === mergeTarget)?.title ||
                      "选择目标调查"}
                  </b>
                  <small>合并后创建新版本</small>
                </div>
              </div>
              <section className="inv-merge-panel">
                <header>
                  <span className="inv-node-icon">
                    <GitMerge size={25} />
                  </span>
                  <div>
                    <h2>合并调查结果</h2>
                    <p className="muted">预览后再更新主调查</p>
                  </div>
                  <button
                    className="icon-button"
                    aria-label="返回画布"
                    onClick={() => setView("canvas")}
                  >
                    <X size={18} />
                  </button>
                </header>
                <div className="inv-merge-selectors">
                  <label>
                    来源调查（子调查）
                    <SelectField
                      className="field"
                      value={mergeSource}
                      onChange={(e) => {
                        setMergeSource(e.target.value);
                        setMergePreview(null);
                      }}
                    >
                      {inv.branches.map((b) => (
                        <option key={b.id} value={b.id}>
                          {b.title} · v{b.revision}
                        </option>
                      ))}
                    </SelectField>
                  </label>
                  <ArrowRight size={24} />
                  <label>
                    目标调查
                    <SelectField
                      className="field"
                      value={mergeTarget}
                      onChange={(e) => {
                        setMergeTarget(e.target.value);
                        setMergePreview(null);
                      }}
                    >
                      <option value="">选择目标分支</option>
                      {inv.branches
                        .filter((b) => b.id !== mergeSource)
                        .map((b) => (
                          <option key={b.id} value={b.id}>
                            {b.title} · v{b.revision}
                          </option>
                        ))}
                    </SelectField>
                  </label>
                </div>
                {!mergePreview ? (
                  <div className="inv-merge-before">
                    <GitMerge size={36} />
                    <p>读取两个分支的最新版本，比较新增消息、证据和冲突。</p>
                    <button
                      className="btn btn-primary"
                      disabled={
                        busy ||
                        !mergeTarget ||
                        mergeTarget === mergeSource ||
                        readOnly
                      }
                      onClick={() => void generateMerge()}
                    >
                      生成合并预览
                    </button>
                  </div>
                ) : (
                  <>
                    <div className="inv-merge-additions">
                      <section>
                        <h3>本次新增</h3>
                        <label className="inv-mode">
                          <input
                            type="radio"
                            checked={mergeMode === "messages"}
                            onChange={() => setMergeMode("messages")}
                          />
                          消息与关联证据
                        </label>
                        <label className="inv-mode">
                          <input
                            type="radio"
                            checked={mergeMode === "evidence_only"}
                            onChange={() => setMergeMode("evidence_only")}
                          />
                          仅合并证据
                        </label>
                        {mergeMode === "messages" &&
                          (mergePreview.additions || []).map((m) => (
                            <label key={m.id} className="inv-merge-check">
                              <input
                                type="checkbox"
                                checked={mergeSelected.includes(m.id)}
                                onChange={(e) =>
                                  setMergeSelected((ids) =>
                                    e.target.checked
                                      ? [...ids, m.id]
                                      : ids.filter((x) => x !== m.id),
                                  )
                                }
                              />
                              <span>{m.content}</span>
                            </label>
                          ))}
                        {mergeMode === "evidence_only" &&
                          (mergePreview.evidence || []).map((e) => (
                            <p key={e.id}>
                              <FileText size={15} /> {e.title}
                            </p>
                          ))}
                        {!mergePreview.additions?.length && (
                          <p className="muted">
                            没有新增消息，可切换为仅合并证据。
                          </p>
                        )}
                      </section>
                      <section>
                        <h3>合并后的主调查</h3>
                        <div className="inv-result-summary">
                          <FileText size={23} />
                          <p>
                            保留目标 v{mergePreview.target_revision}{" "}
                            的原始内容，追加本次选中的
                            {mergeMode === "messages"
                              ? `${mergeSelected.length} 条消息`
                              : `${mergePreview.evidence?.length || 0} 条证据`}
                            ，创建新版本。
                          </p>
                        </div>
                        <p className="muted">
                          来源 v{mergePreview.source_revision} ·
                          合并不会删除来源分支，也不会批准任何操作。
                        </p>
                      </section>
                    </div>
                    {!!mergePreview.conflicts?.length && (
                      <div className="inv-conflict">
                        <h3>
                          <TriangleAlert size={24} />
                          需要确认
                        </h3>
                        {mergePreview.conflicts.map((c, i) => (
                          <p key={i}>{c}</p>
                        ))}
                        {mergePreview.conflict_details?.map((c, i) => (
                          <p key={`detail-${i}`}>
                            {c.message} · {c.review}
                          </p>
                        ))}
                        <label>
                          <input
                            type="checkbox"
                            checked={conflictReviewed}
                            onChange={(e) =>
                              setConflictReviewed(e.target.checked)
                            }
                          />
                          已审查差异，保留原始记录与所选新增信息供后续核实
                        </label>
                      </div>
                    )}
                    <div className="inv-context-changes">
                      <h3>上下文变化</h3>
                      <div>
                        <section>
                          <FileText size={19} />
                          <strong>现有上下文</strong>
                          <p>目标版本 v{mergePreview.target_revision}</p>
                          <small>来自目标调查</small>
                        </section>
                        <section>
                          <Plus size={21} />
                          <strong>新增内容</strong>
                          <p>
                            {mergeMode === "messages"
                              ? mergeSelected.length
                              : 0}{" "}
                            条消息 · {mergePreview.evidence?.length || 0} 条证据
                          </p>
                          <small>来自本次合并</small>
                        </section>
                        <section>
                          <History size={20} />
                          <strong>新版本</strong>
                          <p>
                            <span className="badge">
                              v{mergePreview.target_revision + 1}
                            </span>
                          </p>
                          <small>确认后由后端生成</small>
                        </section>
                      </div>
                    </div>
                  </>
                )}
                <footer>
                  <button className="btn" onClick={() => setView("canvas")}>
                    返回调查
                  </button>
                  <button
                    className="btn btn-primary"
                    disabled={
                      !mergePreview ||
                      busy ||
                      readOnly ||
                      (mergeMode === "messages" && !mergeSelected.length) ||
                      (!!mergePreview.conflicts?.length && !conflictReviewed)
                    }
                    onClick={() => void confirmMerge()}
                  >
                    {busy ? "处理中…" : "确认合并"}
                  </button>
                </footer>
              </section>
            </div>
          )}
        </>
      )}
      <Modal
        open={forkTitle !== null}
        onOpenChange={(open) => {
          if (!open) setForkTitle(null);
        }}
        title="派生调查分支"
        description="继承父节点当前已提交版本，随后独立演进。"
      >
        <form
          className="inv-modal-form"
          onSubmit={async (e) => {
            e.preventDefault();
            const source = inv?.branches.find((b) => b.id === forkBranch);
            if (!source || forkTitle === null) return;
            if (preview) {
              const next = {
                ...source,
                id: crypto.randomUUID(),
                parent_id: source.id,
                inherited_parent_revision: source.revision,
                title: forkTitle,
                revision: 1,
                messages: [...source.messages],
              };
              setDemo((prev) => ({
                ...prev,
                branches: [...prev.branches, next],
              }));
              setForkTitle(null);
              selectBranch(next.id);
              notify("已创建本地预览分支，未写入服务器。", "info");
              return;
            }
            const next = await perform(() =>
              post<CanvasBranch>(`/branches/${forkBranch}/fork`, {
                title: forkTitle,
                revision: source.revision,
              }),
            );
            if (next) {
              setForkTitle(null);
              selectBranch(next.id);
              refresh();
            }
          }}
        >
          <label>
            调查方向
            <input
              className="field"
              placeholder="例如：检查数据库连接池"
              value={forkTitle || ""}
              onChange={(e) => setForkTitle(e.target.value)}
              maxLength={80}
            />
          </label>
          <button
            className="btn btn-primary"
            disabled={busy || !forkTitle?.trim()}
          >
            创建分支
          </button>
        </form>
      </Modal>
    </section>
  );
}
