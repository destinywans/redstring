import SelectField from "../../components/SelectField";
import { useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  Clock,
  FileText,
  ListChecks,
  Info,
  MessageSquare,
  Search,
} from "../../lib/icons";
import { ApiError, post, request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { usePageScope } from "../../components/ScopeFilter";
import { ErrorNotice, Modal } from "../../components/ui";
import type { Investigation, Items } from "../../lib/types";
import { type Dashboard, type Todo, statusName, timeLabel } from "./operations";
import HomeDrawer, { type HomeItem } from "./homeDrawer";
import HomeComposer, { type HomeComposerHandle } from "./HomeComposer";
import "./home.css";

const empty: Dashboard = {
  incidents: [],
  pending_approvals: [],
  executions: [],
  my_items: [],
  automation_runs: [],
  notification_failures: [],
  data_sources: [],
};
const todoPath = (item: Todo) =>
  item.kind === "execution"
    ? `/recovery?execution=${encodeURIComponent(item.id)}`
    : item.kind === "approval"
      ? `/actions?plan=${encodeURIComponent(item.id)}`
      : item.investigation_id
        ? `/investigations/${item.investigation_id}`
        : `/incidents?incident=${encodeURIComponent(item.id)}`;
const messagePreview = (text?: string) =>
  text
    ?.split("\n")
    .find((line) => line.trim())
    ?.replace(/^#{1,6}\s+/, "")
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/`([^`]+)`/g, "$1");
const sessionStatus = (status: string) =>
  ({ open: "进行中", closed: "已结束", active: "进行中" })[status] ||
  statusName(status);
const errorMessage = (error: unknown) =>
  error instanceof Error ? error.message : "请求未完成，请稍后再试。";
type DrawerKind = "attention" | "todos" | "sessions";

export default function DashboardPage() {
  const workspace = useWorkspace();
  const { preview, user, applications, models } = workspace;
  const scope = usePageScope();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState("");
  const [modelId, setModelId] = useState("");
  const [applicationId, setApplicationId] = useState("");
  const [environmentId, setEnvironmentId] = useState("");
  const [confirmScope, setConfirmScope] = useState(false);
  const [busy, setBusy] = useState(false);
  const [sendError, setSendError] = useState("");
  const [creationUncertain, setCreationUncertain] = useState(false);
  const [created, setCreated] = useState<Investigation | null>(null);
  const [drawer, setDrawer] = useState<DrawerKind | null>(null);
  const [pinned, setPinned] = useState(false);
  const [drawerKind, setDrawerKind] = useState<DrawerKind>("sessions");
  const composer = useRef<HomeComposerHandle>(null);
  const sending = useRef(false);
  const pendingMessage = useRef<{
    content: string;
    modelId: string;
    id: string;
    title: string;
    applicationId: string;
    environmentId: string;
  } | null>(null);
  const q = useQuery({
    queryKey: ["dashboard", preview, scope.query],
    queryFn: () => request<Dashboard>(`/dashboard?${scope.query}`),
    enabled: !preview && !scope.empty,
    refetchInterval: 30000,
  });
  const sessionsQuery = useQuery({
    queryKey: ["investigations", "home"],
    queryFn: () => request<Items<Investigation>>("/investigations"),
    enabled: !preview,
    refetchInterval: 30000,
  });
  const data = scope.empty ? empty : (q.data ?? empty);
  const targetIds = new Set(scope.targets.map((target) => target.env.id));
  const appIds = new Set(scope.apps.map((app) => app.id));
  const sessions = (scope.empty ? [] : sessionsQuery.data?.items || [])
    .filter(
      (session) =>
        appIds.has(session.application_id) &&
        (session.environment_id
          ? targetIds.has(session.environment_id)
          : !scope.environmentIds.length),
    )
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
  const details = useQueries({
    queries: sessions.slice(0, 5).map((session) => ({
      queryKey: ["investigation", session.id],
      queryFn: () => request<Investigation>(`/investigations/${session.id}`),
      enabled: !preview,
      staleTime: 30000,
    })),
  });
  const appName = (id: string) =>
    applications.find((app) => app.id === id)?.name || "未关联应用";
  const sessionItems: HomeItem[] = sessions.map((session, index) => {
    const detail = index < 5 ? details[index]?.data : undefined;
    const newestMessage = detail?.branches
      ?.flatMap((branch) => branch.messages || [])
      .filter((message) => message.role === "assistant")
      .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))[0];
    const latestRun = detail?.runs
      ?.slice()
      .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))[0];
    const app = applications.find((app) => app.id === session.application_id);
    const env = app?.environments?.find(
      (environment) => environment.id === session.environment_id,
    );
    const resources = [
      ...new Set(
        env?.bindings?.map((binding) => binding.resource).filter(Boolean) || [],
      ),
    ];
    return {
      id: session.id,
      title: session.title,
      resource: `${appName(session.application_id)}${resources.length ? ` · ${resources.slice(0, 2).join("、")}` : ""}`,
      progress:
        messagePreview(newestMessage?.content) ||
        (latestRun
          ? `最近运行：${statusName(latestRun.status)}`
          : detail
            ? "尚无 Agent 回复"
            : "打开会话查看最新进展"),
      status: session.status,
      statusLabel: sessionStatus(session.status),
      updatedAt: session.updated_at,
      to: `/investigations/${session.id}`,
      action: session.status === "closed" ? "查看会话" : "继续会话",
      tone: session.status === "closed" ? "success" : undefined,
    };
  });
  const attention: HomeItem[] = data.incidents
    .filter(
      (incident) =>
        incident.alert_status !== "resolved" || incident.status !== "closed",
    )
    .map((incident) => ({
      id: incident.id,
      title: incident.title,
      resource: appName(incident.application_id),
      progress:
        incident.summary ||
        `${statusName(incident.alert_status)} · ${statusName(incident.status)}`,
      status: incident.alert_status,
      statusLabel: statusName(incident.alert_status),
      updatedAt: incident.updated_at,
      to: incident.investigation_id
        ? `/investigations/${incident.investigation_id}`
        : `/incidents?incident=${encodeURIComponent(incident.id)}`,
      action: incident.investigation_id ? "继续调查" : "查看异常",
      tone: incident.alert_status === "resolved" ? "success" : "warning",
    }));
  const todoItems = Array.from(
    new Map(
      [...data.pending_approvals, ...data.my_items, ...data.executions]
        .filter(
          (item) =>
            !["closed", "completed", "succeeded", "cancelled"].includes(
              item.status,
            ),
        )
        .map((item) => [`${item.kind}:${item.id}`, item]),
    ).values(),
  );
  const todos: HomeItem[] = todoItems.map((item) => ({
    id: `${item.kind}:${item.id}`,
    title: item.kind === "approval" ? `待审批 · ${item.title}` : item.title,
    resource: appName(item.application_id),
    progress:
      item.kind === "approval"
        ? "需要有权限的操作者确认提案"
        : item.kind === "execution"
          ? "恢复执行与业务健康验证"
          : "由我认领的事件",
    status: `${item.kind}:${item.status}`,
    statusLabel: item.kind === "approval" ? "待审批" : statusName(item.status),
    updatedAt: item.updated_at,
    to: todoPath(item),
    action: item.kind === "approval" ? "查看提案" : "查看任务",
    tone: "warning",
  }));
  const eligibleApps = scope.apps.filter((app) =>
    scope.targets.some((target) => target.app.id === app.id),
  );
  const selectedApp = eligibleApps.find((app) => app.id === applicationId);
  const possibleTargets = scope.targets.filter(
    (target) => !applicationId || target.app.id === applicationId,
  );
  const selectedTarget =
    possibleTargets.find((target) => target.env.id === environmentId) ||
    (possibleTargets.length === 1 ? possibleTargets[0] : undefined);
  const availableModels = models.filter((model) => model.available);
  const effectiveModel = modelId || availableModels[0]?.id || "";
  const readOnly = user.role === "viewer" || preview;
  const openDrawer = (kind: DrawerKind) => {
    setDrawerKind(kind);
    setDrawer(kind);
    setPinned(false);
  };
  const fill = (text: string) => {
    if (busy || created || creationUncertain) return;
    setDraft(text);
    setSendError("");
    composer.current?.focus();
  };
  const begin = async () => {
    if (
      !draft.trim() ||
      busy ||
      sending.current ||
      readOnly ||
      creationUncertain
    )
      return;
    if (
      !created &&
      !availableModels.some((model) => model.id === effectiveModel)
    ) {
      setSendError("请先在集成中心配置可用的模型服务。");
      return;
    }
    if (!selectedTarget && !created) {
      setConfirmScope(true);
      return;
    }
    sending.current = true;
    setBusy(true);
    setSendError("");
    let investigation = created;
    try {
      // Freeze the entire first-message payload before creating the session.
      // An uncertain send retries the identical content, model and request ID.
      if (!pendingMessage.current) {
        if (!selectedTarget || !composer.current)
          throw new Error("请确认本次工作的应用与环境。");
        const content = await composer.current.prepareContent();
        pendingMessage.current = {
          content,
          modelId: effectiveModel,
          id: crypto.randomUUID(),
          title: draft.trim().slice(0, 100),
          applicationId: selectedTarget.app.id,
          environmentId: selectedTarget.env.id,
        };
      }
      const payload = pendingMessage.current;
      if (!investigation && selectedTarget) {
        try {
          investigation = await post<Investigation>("/investigations", {
            application_id: payload.applicationId,
            environment_id: payload.environmentId,
            title: payload.title,
          });
          setCreated(investigation);
        } catch (error) {
          if (!(error instanceof ApiError) || error.status >= 500)
            setCreationUncertain(true);
          else pendingMessage.current = null;
          throw error;
        }
      }
      if (!investigation) return;
      const root = investigation.branches?.find((branch) => !branch.parent_id);
      if (!root)
        throw new Error(
          "会话已建立，但未返回主分支。请打开该会话继续，输入内容仍为你保留。",
        );
      await post(`/branches/${root.id}/messages`, {
        content: payload.content,
        model_id: payload.modelId,
        client_request_id: payload.id,
      });
      workspace.setScope(
        investigation.application_id,
        investigation.environment_id || "",
      );
      workspace.setInvestigationId(investigation.id);
      await queryClient.invalidateQueries({ queryKey: ["investigations"] });
      setConfirmScope(false);
      navigate(`/investigations/${investigation.id}`);
    } catch (error) {
      setSendError(errorMessage(error));
    } finally {
      sending.current = false;
      setBusy(false);
    }
  };
  const drawerItems =
    drawerKind === "attention"
      ? attention
      : drawerKind === "todos"
        ? todos
        : sessionItems;
  return (
    <div className={`home-page${drawer && pinned ? " home-with-pinned" : ""}`}>
      <section className="home-start" aria-labelledby="home-title">
        <header className="home-hero">
          <span className="home-eyebrow">AI OPERATIONS</span>
          <h1 id="home-title">今天想完成什么？</h1>
          <p>从一个问题开始，让 Agent 连接数据、整理证据、推进工作。</p>
        </header>
        <HomeComposer
          ref={composer}
          draft={draft}
          onDraft={setDraft}
          applicationId={applicationId}
          onApplication={(id) => {
            setApplicationId(id);
            setEnvironmentId("");
          }}
          targetApplicationId={selectedTarget?.app.id || selectedApp?.id || ""}
          environmentId={selectedTarget?.env.id || ""}
          applications={eligibleApps}
          models={availableModels}
          modelId={modelId}
          onModel={setModelId}
          disabled={busy || readOnly || creationUncertain || !!created}
          sendDisabled={
            busy ||
            readOnly ||
            scope.empty ||
            !draft.trim() ||
            creationUncertain
          }
          busy={busy}
          retry={!!created}
          preview={preview}
          onSend={() => void begin()}
        />
        <p className="home-composer-hint">
          {readOnly
            ? preview
              ? "设计预览 · 未连接真实数据与模型。"
              : "当前为只读权限，可查看已有会话与资料。"
            : scope.empty
              ? "当前环境没有可用应用，请先调整右上角的环境范围。"
              : applicationId && !selectedApp
                ? "已选应用在当前环境中不可用，请重新选择应用或调整环境。"
                : "目标不明确时，会先请你确认应用与环境；变更操作仍需明确审批。"}
        </p>
        {sendError && (
          <div className="home-send-error" role="alert">
            {sendError}
            {created && (
              <Link to={`/investigations/${created.id}`}>
                打开已创建的会话 <ArrowRight size={15} />
              </Link>
            )}
            {creationUncertain && (
              <span>
                创建结果暂不确定。请先查看最近会话，避免重复创建；输入内容已保留。
              </span>
            )}
          </div>
        )}
        <div className="home-shortcuts">
          <button
            disabled={busy || !!created || creationUncertain}
            onClick={() =>
              fill("检查当前环境的健康状态，汇总异常和需要关注的资源。")
            }
          >
            <Activity size={21} />
            检查环境健康
          </button>
          <button
            onClick={() => {
              if (attention.length) openDrawer("attention");
              else fill("分析一条告警，帮我确认影响范围和可能原因。");
            }}
          >
            <FileText size={21} />
            分析这条告警
          </button>
          <button
            disabled={busy || !!created || creationUncertain}
            onClick={() => fill("查找最近的错误日志，归纳异常模式及关联资源。")}
          >
            <Search size={21} />
            查找错误日志
          </button>
        </div>
      </section>
      {q.error && <ErrorNotice error={q.error} />}
      <div className="home-summary-grid">
        <section className="home-card">
          <header className="home-card-heading">
            <h2>
              <AlertTriangle size={24} />
              需要关注{" "}
              <span className="home-count">
                {q.isFetching && !preview ? "—" : attention.length}
              </span>
            </h2>
            <button
              className="home-view-all"
              onClick={() => openDrawer("attention")}
            >
              查看全部 <ArrowRight size={16} />
            </button>
          </header>
          <div className="home-card-rows">
            {attention.slice(0, 2).map((item) => (
              <div className="home-summary-row" key={item.id}>
                <span className="home-alert-mark">
                  <Info size={22} weight="fill" />
                </span>
                <div className="home-row-copy">
                  <strong>{item.title}</strong>
                  <small>
                    {item.resource} · {timeLabel(item.updatedAt)}
                  </small>
                </div>
                <Link className="btn btn-small" to={item.to}>
                  {item.action}
                </Link>
              </div>
            ))}
            {!attention.length && (
              <p className="home-empty">
                {q.isFetching && !preview
                  ? "正在读取异常…"
                  : "当前没有需要关注的异常"}
                <small>
                  没有告警不代表系统健康，请结合数据源和观测结果判断。
                </small>
              </p>
            )}
          </div>
        </section>
        <section className="home-card">
          <header className="home-card-heading">
            <h2>
              <ListChecks size={24} />
              待我处理{" "}
              <span className="home-count">
                {q.isFetching && !preview ? "—" : todos.length}
              </span>
            </h2>
            <button
              className="home-view-all"
              onClick={() => openDrawer("todos")}
            >
              查看全部 <ArrowRight size={16} />
            </button>
          </header>
          <div className="home-card-rows">
            {todos.slice(0, 2).map((item) => (
              <Link className="home-summary-row" key={item.id} to={item.to}>
                <Clock size={22} className="home-warning-icon" />
                <div className="home-row-copy">
                  <strong>{item.title}</strong>
                  <small>
                    {item.resource} · {item.progress}
                  </small>
                </div>
                <span className="home-status warning">{item.statusLabel}</span>
              </Link>
            ))}
            {!todos.length && (
              <p className="home-empty">
                {q.isFetching && !preview
                  ? "正在读取待办…"
                  : "当前没有待处理任务"}
                <small>待审批提案、认领事件和恢复执行会显示在这里。</small>
              </p>
            )}
          </div>
        </section>
      </div>
      <section className="home-card home-recent">
        <header className="home-card-heading">
          <h2>
            <MessageSquare size={24} />
            最近会话
          </h2>
          <button
            className="home-view-all"
            onClick={() => openDrawer("sessions")}
          >
            查看全部 <ArrowRight size={16} />
          </button>
        </header>
        {sessionsQuery.error && <ErrorNotice error={sessionsQuery.error} />}
        <div className="home-session-scroll">
          <table className="home-session-table ui-data-table">
            <thead>
              <tr>
                <th>会话</th>
                <th>关联资源</th>
                <th>最近进展</th>
                <th>更新时间</th>
                <th>状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {sessionItems.slice(0, 2).map((item) => (
                <tr key={item.id}>
                  <td>
                    <Link className="home-session-title" to={item.to}>
                      <MessageSquare size={20} />
                      <span>{item.title}</span>
                    </Link>
                  </td>
                  <td>
                    <span className="home-clamp" title={item.resource}>
                      {item.resource}
                    </span>
                  </td>
                  <td>
                    <span className="home-clamp" title={item.progress}>
                      {item.progress}
                    </span>
                  </td>
                  <td>
                    <time>{timeLabel(item.updatedAt)}</time>
                  </td>
                  <td>
                    <span className={`home-status ${item.tone || ""}`}>
                      {item.statusLabel}
                    </span>
                  </td>
                  <td>
                    <Link className="home-session-link" to={item.to}>
                      {item.status === "closed" ? "查看" : "继续"}
                      <ArrowRight size={16} />
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!sessionItems.length && (
          <p className="home-empty">
            {sessionsQuery.isFetching && !preview
              ? "正在读取会话…"
              : "还没有会话，从上方描述一个问题开始。"}
          </p>
        )}
      </section>
      <p className="home-data-note">
        卡片数量仅统计当前范围内已加载记录。异常最多 200 条，审批与执行各最多
        100 条。
      </p>
      <Modal
        open={confirmScope}
        onOpenChange={(open) => {
          if (!busy) setConfirmScope(open);
        }}
        title="确认本次工作的目标"
        description="你的问题已保留。选择具体应用与环境后，再开始 Agent 会话。"
      >
        <div className="home-target-fields">
          <label>
            应用
            <SelectField
              value={applicationId}
              disabled={busy || !!created}
              onChange={(event) => {
                setApplicationId(event.target.value);
                setEnvironmentId("");
              }}
            >
              <option value="">选择应用</option>
              {applicationId && !selectedApp && (
                <option value={applicationId} disabled>
                  {appName(applicationId)} · 当前范围不可用
                </option>
              )}
              {eligibleApps.map((app) => (
                <option key={app.id} value={app.id}>
                  {app.name}
                </option>
              ))}
            </SelectField>
          </label>
          <label>
            环境
            <SelectField
              value={selectedTarget?.env.id || ""}
              disabled={busy || !!created || !selectedApp}
              onChange={(event) => setEnvironmentId(event.target.value)}
            >
              <option value="">选择环境</option>
              {possibleTargets
                .filter((target) => target.app.id === applicationId)
                .map((target) => (
                  <option key={target.env.id} value={target.env.id}>
                    {target.env.name}
                  </option>
                ))}
            </SelectField>
          </label>
          {sendError && (
            <p role="alert" className="home-send-error">
              {sendError}
            </p>
          )}
          <button
            className="btn btn-primary"
            disabled={busy || !selectedTarget || creationUncertain}
            onClick={() => void begin()}
          >
            {busy ? "正在开启…" : "确认并发送"}
          </button>
        </div>
      </Modal>
      <HomeDrawer
        open={!!drawer}
        title={
          drawerKind === "attention"
            ? "需要关注"
            : drawerKind === "todos"
              ? "待我处理"
              : "全部会话"
        }
        description={
          drawerKind === "attention"
            ? "查看当前范围的异常，以及自动化与数据源状态。"
            : drawerKind === "todos"
              ? "继续推进待审批提案、认领事件与恢复任务。"
              : "继续处理中的工作，或查看历史记录。"
        }
        items={drawerItems}
        pinned={pinned}
        onPin={() => setPinned((value) => !value)}
        onClose={() => {
          setDrawer(null);
          setPinned(false);
        }}
      >
        {drawerKind === "attention" && (
          <section className="home-source-status">
            <h3>自动化与连接状态</h3>
            {data.automation_runs.slice(0, 20).map((run) => (
              <div key={run.id}>
                <strong>
                  {run.kind === "patrol" ? "主动巡检" : "告警初查"}
                </strong>
                <small>
                  {statusName(run.status)} · {timeLabel(run.created_at)}
                  {run.error && ` · ${run.error}`}
                </small>
                {run.investigation_id && (
                  <Link to={`/investigations/${run.investigation_id}`}>
                    查看会话
                  </Link>
                )}
              </div>
            ))}
            {data.notification_failures.map((notification) => (
              <div key={notification.id}>
                <strong>{notification.channel} 通知失败</strong>
                <small>{notification.last_error || "请检查通知连接配置"}</small>
              </div>
            ))}
            {data.data_sources.map((source, index) => (
              <div key={source.id || index}>
                <strong>{source.name || source.id}</strong>
                <small>
                  {statusName(source.status || "unknown")} {source.error}
                </small>
              </div>
            ))}
            {!data.automation_runs.length &&
              !data.notification_failures.length &&
              !data.data_sources.length && (
                <p className="home-empty">暂无自动化与连接状态记录</p>
              )}
          </section>
        )}
      </HomeDrawer>
    </div>
  );
}
