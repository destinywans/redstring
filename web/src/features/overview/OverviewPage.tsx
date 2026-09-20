import ScopeFilter from "../../components/ScopeFilter";
import { useState, useEffect, useRef } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Sparkles,
  Play,
  Send,
  Box,
  Network,
  FileText,
  ArrowRight,
  BookOpen,
  LineChart,
  ScrollText,
  ChevronDown,
  Search,
  Database,
  GitBranch,
  Plus,
  Pin,
  Minus,
  Layers,
  Activity,
} from "../../lib/icons";
import { useWorkspace } from "../../lib/workspace";
import { post, request } from "../../lib/api";
import type { Investigation, Items } from "../../lib/types";
import {
  Modal,
  CardHeading,
  CardTools,
  ErrorNotice,
} from "../../components/ui";
import { MetricChart, incidentPoints } from "../../components/MetricChart";
import { useDraftStore } from "../../lib/drafts";
import "./overview.css";
import { ApplicationDetails } from "../workspace/ApplicationProfile";
import "../workspace/workspace.css";
import TelemetryResult, { type TelemetryResultData } from "./TelemetryResult";
type Deployment = {
  deployment: string;
  namespace: string;
  images: { name: string; image: string }[];
  ready_replicas: number;
  replicas: number;
  captured_at: string;
  revisions: {
    revision: string;
    digest: string;
    images: { name: string; image: string }[];
  }[];
};
type Knowledge = { id: string; title: string; body: string; status: string };
export default function OverviewPage() {
  const {
    preview,
    user,
    application,
    environment,
    models,
    investigationId,
    setInvestigationId,
    notify,
  } = useWorkspace();
  const navigate = useNavigate(),
    client = useQueryClient();
  const [prompt, setPrompt] = useState(""),
    [busy, setBusy] = useState(false),
    [folded, setFolded] = useState<string[]>([]),
    [pinned, setPinned] = useState<string[]>([]),
    [contextOpen, setContextOpen] = useState(false),
    [tool, setTool] = useState<"metrics" | "logs" | null>(null),
    [queryID, setQueryID] = useState(""),
    [result, setResult] = useState<TelemetryResultData | null>(null),
    [windowMinutes, setWindowMinutes] = useState(30),
    [pinInvestigation, setPinInvestigation] = useState(""),
    [pinBranch, setPinBranch] = useState(""),
    [pinBusy, setPinBusy] = useState(false),
    [queryError, setQueryError] = useState<unknown>(null),
    [queryBusy, setQueryBusy] = useState(false);
  const queryGeneration = useRef(0);
  const investigations = useQuery({
    queryKey: ["investigations", preview],
    queryFn: () => request<Items<Investigation>>("/investigations"),
    enabled: !preview,
  });
  const deployment = useQuery({
    queryKey: ["deployment", application?.id, environment?.id, preview],
    queryFn: () =>
      request<Deployment>(
        `/applications/${application!.id}/deployment?environment_id=${encodeURIComponent(environment?.id ?? "")}`,
      ),
    enabled: !!application && !preview,
  });
  const knowledge = useQuery({
    queryKey: ["knowledge", application?.id, environment?.id, preview],
    queryFn: () =>
      request<Items<Knowledge>>(
        `/knowledge?application_id=${encodeURIComponent(application!.id)}`,
      ),
    enabled: !!application && !preview,
  });
  const telemetry = useQuery({
    queryKey: ["telemetry", application?.id, environment?.id, preview],
    queryFn: () =>
      request<{ metric_query_ids: string[]; logs_configured: boolean }>(
        `/applications/${application!.id}/telemetry?environment_id=${encodeURIComponent(environment?.id ?? "")}`,
      ),
    enabled: !!application && !preview && !!tool,
  });
  useEffect(() => {
    queryGeneration.current++;
    setQueryBusy(false);
    setResult(null);
    setQueryError(null);
    setQueryID("");
    setPinInvestigation("");
    setPinBranch("");
  }, [application?.id, environment?.id]);
  const pinDetail = useQuery({
    queryKey: ["pin-investigation", pinInvestigation],
    queryFn: () =>
      request<Investigation>(`/investigations/${pinInvestigation}`),
    enabled: !!pinInvestigation && !preview,
  });
  const active = (investigations.data?.items ?? []).filter(
    (x) =>
      x.application_id === application?.id &&
      (!environment || x.environment_id === environment.id),
  );
  const latest = active[0];
  function toggle(
    set: React.Dispatch<React.SetStateAction<string[]>>,
    id: string,
  ) {
    set((old) =>
      old.includes(id) ? old.filter((x) => x !== id) : [...old, id],
    );
  }
  function tools(id: string) {
    return (
      <CardTools
        pinned={pinned.includes(id)}
        onCollapse={() => toggle(setFolded, id)}
        onPin={() => toggle(setPinned, id)}
      />
    );
  }
  async function start(input = prompt) {
    if (preview) {
      setInvestigationId("preview-investigation");
      navigate("/investigations/preview-investigation");
      return;
    }
    if (!application) return;
    setBusy(true);
    try {
      const content = input.trim();
      const inv = await post<Investigation>("/investigations", {
        application_id: application.id,
        environment_id: environment?.id,
        title: content.slice(0, 100) || `${application.name} · 运行状态调查`,
      });
      setInvestigationId(inv.id);
      const branch = inv.branches?.[0];
      const model = models.find((m) => m.available);
      const clientRequestId = crypto.randomUUID();
      if (content && branch) {
        useDraftStore.getState().setPrompt({
          investigationId: inv.id,
          branchId: branch.id,
          content,
          clientRequestId,
        });
        if (model) {
          try {
            await post(`/branches/${branch.id}/messages`, {
              content,
              model_id: model.id,
              client_request_id: clientRequestId,
            });
            useDraftStore.getState().setPrompt(null);
          } catch (e) {
            notify(
              `调查已创建，问题保留在草稿：${e instanceof Error ? e.message : String(e)}`,
              "error",
            );
          }
        } else
          notify("调查已创建，问题已带入草稿。配置模型后即可发送。", "info");
      }
      await client.invalidateQueries({ queryKey: ["investigations"] });
      navigate(`/investigations/${inv.id}`);
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  async function query() {
    if (preview) {
      setResult(
        tool === "metrics"
          ? {
              source: {
                provider: "设计示例",
                query: "http_5xx_rate",
                captured_at: new Date().toISOString(),
              },
              series: [
                {
                  labels: { service: "checkout-api", data: "示例" },
                  samples: incidentPoints.slice(-8).map((p, i) => ({
                    timestamp: Date.now() / 1000 - (8 - i) * 60,
                    value: String(p.value),
                  })),
                },
              ],
            }
          : {
              source: {
                provider: "设计示例",
                captured_at: new Date().toISOString(),
              },
              entries: [
                {
                  timestamp: new Date().toISOString(),
                  line: "ERROR connection timeout after 30000ms",
                  labels: { level: "error" },
                },
                {
                  timestamp: new Date().toISOString(),
                  line: "WARN upstream service unavailable",
                  labels: { level: "warn" },
                },
              ],
            },
      );
      return;
    }
    if (!application) return;
    const generation = ++queryGeneration.current;
    setQueryBusy(true);
    setQueryError(null);
    try {
      const body =
        tool === "metrics"
          ? { query_id: queryID || telemetry.data?.metric_query_ids[0] }
          : {};
      const response = await post<TelemetryResultData>(
        `/applications/${application.id}/${tool}`,
        {
          ...body,
          environment_id: environment?.id,
          start: new Date(Date.now() - windowMinutes * 60000).toISOString(),
          end: new Date().toISOString(),
        },
      );
      if (generation === queryGeneration.current) setResult(response);
    } catch (e) {
      if (generation === queryGeneration.current) setQueryError(e);
    } finally {
      if (generation === queryGeneration.current) setQueryBusy(false);
    }
  }
  if (!application)
    return (
      <div className="empty-state">
        <Box size={30} />
        <h2>选择应用与环境</h2>
        <ScopeFilter single />
        <p>请由管理员在应用目录登记应用与环境，并绑定连接器。</p>
      </div>
    );
  const docs = preview
    ? [
        {
          id: "1",
          title: "发布后 5xx 排查",
          body: "定位发布后错误率上升的常见原因和步骤",
          status: "ready",
        },
        {
          id: "2",
          title: "数据库连接异常排查",
          body: "MySQL 连接池、连接超时相关问题",
          status: "ready",
        },
        {
          id: "3",
          title: "服务性能诊断",
          body: "CPU、内存、依赖慢查询分析",
          status: "ready",
        },
      ]
    : (knowledge.data?.items ?? []).slice(0, 3);
  return (
    <div className="page overview-page">
      <ScopeFilter single />
      <div className="page-heading overview-heading">
        <div>
          <div className="overview-title">
            <h1>{application.name}</h1>
            <span className="service-id">
              {preview ? "checkout-api" : application.key || application.id}
            </span>
            <span className={`badge ${preview ? "badge-warning" : ""}`}>
              {preview ? "需要关注" : "运行状态待确认"}
            </span>
          </div>
          <p>{application.description}</p>
        </div>
        <div className="app-facts">
          <div>
            <span>工作空间</span>
            <strong>AIOps 工作区</strong>
          </div>
          <div>
            <span>环境</span>
            <strong>{environment?.name || application.environment}</strong>
          </div>
          <div>
            <span>当前角色</span>
            <strong>
              {
                { admin: "管理员", operator: "操作员", viewer: "只读用户" }[
                  user.role
                ]
              }
            </strong>
          </div>
        </div>
      </div>
      <ApplicationDetails
        application={application}
        environment={environment ?? undefined}
      />
      <div className="overview-top">
        <section
          className={`card incident-card ${pinned.includes("incident") ? "card-is-pinned" : ""}`}
        >
          <div className="incident-header">
            <span className="incident-alert">
              <AlertTriangle size={23} />
            </span>
            <div>
              <h2>
                {preview
                  ? "发布后 5xx 上升"
                  : (latest?.title ?? "从应用信号开始调查")}{" "}
                <span className={`badge ${preview ? "badge-danger" : ""}`}>
                  {preview
                    ? "服务异常"
                    : latest?.status === "closed"
                      ? "已结案"
                      : "调查工作区"}
                </span>
              </h2>
              <p>
                {preview
                  ? "在 14:03 发布 v2.8.1 后，错误率从 0.2% 上升至 8.4%"
                  : "围绕指标、日志与部署变更，逐步建立可追溯的证据。"}
              </p>
            </div>
            {tools("incident")}
          </div>
          {folded.includes("incident") ? (
            <button
              className="btn btn-ghost"
              onClick={() => toggle(setFolded, "incident")}
            >
              展开应用信号
              <ChevronDown size={14} />
            </button>
          ) : (
            <div className="incident-body">
              <div className="incident-chart">
                <h3>{preview ? "5xx 错误率" : "最近的调查"}</h3>
                {preview ? (
                  <MetricChart
                    points={[
                      ...incidentPoints.slice(0, 17),
                      { time: "14:03", value: 0.8 },
                      ...incidentPoints.slice(17),
                    ]}
                    height={208}
                  />
                ) : (
                  <div className="recent-investigations">
                    {active.length ? (
                      active.slice(0, 4).map((inv) => (
                        <button
                          key={inv.id}
                          onClick={() => {
                            setInvestigationId(inv.id);
                            navigate(`/investigations/${inv.id}`);
                          }}
                        >
                          <Search size={16} />
                          <span>
                            <strong>{inv.title}</strong>
                            <small>
                              {new Date(inv.updated_at).toLocaleString("zh-CN")}
                            </small>
                          </span>
                          <span className="badge">
                            {inv.status === "closed" ? "已结案" : "调查中"}
                          </span>
                          <ArrowRight size={15} />
                        </button>
                      ))
                    ) : (
                      <div className="empty-state">
                        <Activity size={25} />
                        <p>尚未开始调查</p>
                        <small>创建后，模型只读取已登记的应用证据。</small>
                      </div>
                    )}
                  </div>
                )}
              </div>
              <div className="incident-stats">
                {preview ? (
                  <>
                    <div className="rate-comparison">
                      <strong>0.2%</strong>
                      <ArrowRight size={20} />
                      <strong>8.4%</strong>
                    </div>
                    <div className="rate-captions">
                      <span>发布前 5xx</span>
                      <span>当前 5xx</span>
                    </div>
                    <span className="muted">发布版本</span>
                    <h2>v2.8.1</h2>
                    <small className="muted">（上一个版本 v2.8.0）</small>
                  </>
                ) : (
                  <>
                    <span className="muted">当前应用调查</span>
                    <strong className="investigation-count">
                      {active.length}
                    </strong>
                    <span className="muted">已配置 Deployment</span>
                    <strong>{deployment.data?.deployment ?? "尚未读取"}</strong>
                  </>
                )}
                <button
                  className="btn btn-primary"
                  disabled={busy || user.role === "viewer"}
                  onClick={() => void start(preview ? "发布后 5xx 上升" : "")}
                >
                  <Play size={15} fill="currentColor" />
                  {busy ? "正在创建…" : "开始调查"}
                </button>
              </div>
            </div>
          )}
        </section>
        <section className="card assistant-card">
          <CardHeading
            title="AI 助手"
            icon={<Sparkles className="accent-icon" size={29} />}
          >
            {tools("assistant")}
          </CardHeading>
          {!folded.includes("assistant") && (
            <>
              <p className="muted">
                基于可观测性数据和团队知识，协助你快速定位和解决问题
              </p>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void start();
                }}
                className="overview-composer"
              >
                <textarea
                  aria-label="向 AI 发起调查"
                  placeholder="描述你的问题，开始一段调查…"
                  value={prompt}
                  onChange={(e) => setPrompt(e.target.value)}
                />
                <button
                  className="btn btn-primary"
                  aria-label="创建调查"
                  disabled={busy || user.role === "viewer"}
                >
                  <Send size={17} />
                </button>
              </form>
              <div className="suggestion-list">
                {[
                  "调查这次发布后的错误率变化",
                  "检查应用的 Deployment 状态",
                  "分析 5xx 的主要错误类型",
                  "查看相关 Runbook",
                ].map((x, i) => (
                  <button
                    key={x}
                    className={i === 0 ? "selected" : ""}
                    onClick={() =>
                      i === 3 ? navigate("/knowledge") : setPrompt(x)
                    }
                  >
                    {x}
                  </button>
                ))}
              </div>
            </>
          )}
        </section>
      </div>
      <div className="overview-bottom">
        <section className="card releases-card">
          <CardHeading title="最近发布" icon={<Box size={24} />}>
            {tools("releases")}
          </CardHeading>
          {!folded.includes("releases") && (
            <>
              <div className="section-link">
                <button
                  onClick={() => {
                    setContextOpen(true);
                  }}
                  className="text-link"
                >
                  查看详情 <ArrowRight size={14} />
                </button>
              </div>
              <div className="table-scroll">
                <table className="release-table">
                  <thead>
                    <tr>
                      <th>版本</th>
                      <th>{preview ? "发布时间" : "镜像"}</th>
                      {preview && <th>发布说明</th>}
                      <th>状态</th>
                    </tr>
                  </thead>
                  <tbody>
                    {preview ? (
                      <>
                        <tr>
                          <td>
                            <strong>v2.8.1</strong>
                          </td>
                          <td>今天 14:03</td>
                          <td>优化结算流程，调整连接池…</td>
                          <td>
                            <i className="status-dot" />
                            已发布
                          </td>
                        </tr>
                        <tr>
                          <td>
                            <strong>v2.8.0</strong>
                          </td>
                          <td>11-02 10:21</td>
                          <td>修复支付回调异常</td>
                          <td>
                            <i className="status-dot green" />
                            稳定运行
                          </td>
                        </tr>
                      </>
                    ) : (
                      deployment.data?.revisions.map((r) => (
                        <tr key={r.revision}>
                          <td>{r.revision}</td>
                          <td className="image-cell">
                            {r.images.map((x) => x.image).join(", ")}
                          </td>
                          <td>已记录</td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              </div>
              {!preview && !deployment.data && (
                <div className="inline-empty">
                  {deployment.isPending
                    ? "正在读取…"
                    : "未接入或暂时无法读取 Deployment；不推断发布状态。"}
                </div>
              )}
            </>
          )}
        </section>
        <section className="card dependencies-card">
          <CardHeading title="依赖关系" icon={<Network size={25} />}>
            {tools("dependencies")}
          </CardHeading>
          {!folded.includes("dependencies") &&
            (preview ? (
              <div className="dependency-graph">
                <div className="dependency-node selected">
                  <Box size={25} />
                  <span>
                    <strong>结算服务</strong>
                    <small>checkout-api</small>
                  </span>
                </div>
                <div className="dependency-join">
                  <span>→</span>
                  <span>→</span>
                </div>
                <div className="dependency-targets">
                  <div className="dependency-node">
                    <Box size={22} />
                    <span>
                      <strong>订单服务</strong>
                      <small>order-api</small>
                    </span>
                  </div>
                  <div className="dependency-node">
                    <Database size={22} />
                    <span>
                      <strong>MySQL</strong>
                      <small>mysql-prod</small>
                    </span>
                  </div>
                </div>
              </div>
            ) : (
              <div className="empty-state">
                <Network size={26} />
                <p>暂无依赖证据</p>
                <small>调查取证后，在画布查看证据关系。</small>
              </div>
            ))}
        </section>
        <section className="card runbooks-card">
          <CardHeading title="相关 Runbook" icon={<BookOpen size={25} />}>
            {tools("runbooks")}
          </CardHeading>
          {!folded.includes("runbooks") && (
            <>
              <div className="section-link">
                <button
                  className="text-link"
                  onClick={() => navigate("/knowledge")}
                >
                  查看全部 <ArrowRight size={14} />
                </button>
              </div>
              {docs.map((d) => (
                <button
                  className="runbook-row"
                  key={d.id}
                  onClick={() => navigate("/knowledge")}
                >
                  <span>
                    <FileText size={21} />
                  </span>
                  <div>
                    <strong>{d.title}</strong>
                    <p>{d.body.replace(/[#*`]/g, "").slice(0, 45)}</p>
                  </div>
                </button>
              ))}
              {!docs.length && (
                <div className="inline-empty">
                  还没有 Runbook。可以导入应用排查文档。
                </div>
              )}
            </>
          )}
        </section>
      </div>
      <section className="card app-context">
        <button onClick={() => setContextOpen((v) => !v)}>
          <FileText size={20} />
          <strong>应用上下文 · {preview ? 4 : 2} 项</strong>
          <ChevronDown size={18} />
        </button>
        {contextOpen && (
          <div className="app-context-body">
            <dl>
              <dt>应用范围</dt>
              <dd>
                {application.key || application.id} ·{" "}
                {environment?.name || application.environment}
              </dd>
              <dt>Deployment</dt>
              <dd>
                {preview
                  ? "checkout-api / production"
                  : deployment.data
                    ? `${deployment.data.deployment} / ${deployment.data.namespace}`
                    : "尚未接入"}
              </dd>
              <dt>副本</dt>
              <dd>
                {preview
                  ? "3 / 3（示例）"
                  : deployment.data
                    ? `${deployment.data.ready_replicas} / ${deployment.data.replicas}`
                    : "暂无数据"}
              </dd>
              <dt>模型调查</dt>
              <dd>只读取证；变更需明确提案与人工批准。</dd>
            </dl>
          </div>
        )}
      </section>
      <nav className="floating-toolbar" aria-label="应用工具">
        <button
          onClick={() => {
            setTool("metrics");
            setResult(null);
            setQueryError(null);
          }}
        >
          <LineChart size={21} />
          指标
        </button>
        <span className="separator" />
        <button
          onClick={() => {
            setTool("logs");
            setResult(null);
            setQueryError(null);
          }}
        >
          <ScrollText size={21} />
          日志
        </button>
        <span className="separator" />
        <button onClick={() => navigate("/investigations")}>
          <Search size={21} />
          调查
        </button>
        <span className="separator" />
        <button onClick={() => navigate("/knowledge")}>
          <BookOpen size={21} />
          Runbook
        </button>
      </nav>
      <Modal
        open={!!tool}
        onOpenChange={(v) => {
          if (!v) setTool(null);
        }}
        title={tool === "metrics" ? "查询应用指标" : "查询应用日志"}
        description="只读取当前应用已登记的数据源。"
      >
        {preview ? (
          <p className="badge">设计示例 · 不连接外部系统</p>
        ) : telemetry.error ? (
          <ErrorNotice error={telemetry.error} />
        ) : (
          <p className="muted">数据范围由管理员登记，空数据不代表健康。</p>
        )}
        <label>
          时间窗口
          <select
            className="field"
            value={windowMinutes}
            onChange={(e) => setWindowMinutes(Number(e.target.value))}
          >
            <option value={15}>最近 15 分钟</option>
            <option value={30}>最近 30 分钟</option>
            <option value={60}>最近 1 小时</option>
            <option value={180}>最近 3 小时</option>
          </select>
        </label>
        {tool === "metrics" && (
          <label>
            已登记查询
            <select
              className="field"
              value={queryID || telemetry.data?.metric_query_ids[0] || ""}
              onChange={(e) => setQueryID(e.target.value)}
            >
              {preview ? (
                <option value="http_5xx_rate">http_5xx_rate</option>
              ) : (
                telemetry.data?.metric_query_ids.map((x) => (
                  <option key={x}>{x}</option>
                ))
              )}
            </select>
          </label>
        )}
        <button
          className="btn btn-primary"
          disabled={
            queryBusy ||
            user.role === "viewer" ||
            (!preview &&
              (!telemetry.data ||
                (tool === "logs" && !telemetry.data.logs_configured)))
          }
          onClick={() => void query()}
        >
          {queryBusy ? "正在读取…" : "查询最近数据"}
        </button>
        {!!queryError && <ErrorNotice error={queryError} />}
        <TelemetryResult result={result} />
        {result && !preview && user.role !== "viewer" && (
          <section className="management-section">
            <h3>固定为调查证据</h3>
            <p className="muted">
              保存这次查询的快照与采集时间，标记为人工固定；不会重新查询数据源。
            </p>
            <div className="management-form">
              <label>
                当前应用与环境的调查
                <select
                  className="field"
                  value={pinInvestigation}
                  onChange={(e) => {
                    setPinInvestigation(e.target.value);
                    setPinBranch("");
                  }}
                >
                  <option value="">选择调查</option>
                  {active
                    .filter((i) => i.status !== "closed")
                    .map((i) => (
                      <option key={i.id} value={i.id}>
                        {i.title}
                      </option>
                    ))}
                </select>
              </label>
              <label>
                证据所属分支
                <select
                  className="field"
                  value={pinBranch}
                  onChange={(e) => setPinBranch(e.target.value)}
                >
                  <option value="">选择分支</option>
                  {pinDetail.data?.branches?.map((b) => (
                    <option key={b.id} value={b.id}>
                      {b.title}
                    </option>
                  ))}
                </select>
              </label>
              {pinDetail.error && <ErrorNotice error={pinDetail.error} />}
              <button
                className="btn"
                disabled={!pinBranch || pinBusy}
                onClick={async () => {
                  setPinBusy(true);
                  try {
                    await post(`/investigations/${pinInvestigation}/evidence`, {
                      branch_id: pinBranch,
                      environment_id: environment?.id,
                      kind: tool,
                      title:
                        tool === "metrics"
                          ? `指标快照 · ${result.source?.query_id || queryID}`
                          : "日志快照",
                      source: result.source?.provider || "telemetry",
                      content: JSON.stringify(result),
                    });
                    await client.invalidateQueries({
                      queryKey: ["investigation", pinInvestigation],
                    });
                    notify("查询快照已固定为证据", "success");
                  } catch (e) {
                    notify(String(e), "error");
                  } finally {
                    setPinBusy(false);
                  }
                }}
              >
                {pinBusy ? "正在保存…" : "固定快照"}
              </button>
            </div>
          </section>
        )}
      </Modal>
    </div>
  );
}
