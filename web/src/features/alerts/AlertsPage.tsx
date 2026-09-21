import { Sheet } from "../../components/ui/sheet";
import { PageHeader } from "../../components/patterns/ListPage";
import Pagination, { useListPagination } from "../../components/Pagination";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  AlertTriangle,
  ArrowRight,
  Bell,
  RefreshCw,
  Search,
} from "../../lib/icons";
import { request, post } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import ScopeFilter, { usePageScope } from "../../components/ScopeFilter";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { ErrorNotice } from "../../components/ui";
import { IncidentEvidence } from "../workspace/AutomationEvidence";
import {
  type Incident,
  type Notification,
  statusName,
  timeLabel,
} from "../workspace/operations";
import "../workspace/workspace.css";
import "./alerts.css";

type Mode = "alerts" | "incidents" | "sources";
type Catalog = {
  sources: {
    id: string;
    type: string;
    state: string;
    mappings: {
      labels: Record<string, string>;
      application_id: string;
      environment_id: string;
    }[];
  }[];
  channels: {
    id: string;
    type: string;
    state: string;
    scope: { application_id: string; environment_id: string };
  }[];
};
type Receipts = {
  items: { id: number; status: string; received_at: string }[];
  limit: number;
  truncated: boolean;
  kind: "alert_receipts";
};
const severityName = (value: string) =>
  ({ critical: "严重", warning: "警告", info: "提示" })[value] ?? value;

export default function AlertsPage({ mode = "incidents" }: { mode?: Mode }) {
  const { preview, user, applications, notify, setInvestigationId, setScope } =
    useWorkspace();
  const qc = useQueryClient(),
    navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const scope = usePageScope();
  const selectedId = params.get("incident") ?? "";
  const [text, setText] = useState(""),
    [searchDraft, setSearchDraft] = useState(""),
    [severity, setSeverity] = useState(""),
    [state, setState] = useState("all"),
    [busy, setBusy] = useState("");
  const query = new URLSearchParams(scope.query);
  const incidents = useQuery({
    queryKey: ["incidents", "workspace", query.toString(), preview],
    queryFn: () => request<{ items: Incident[] }>(`/incidents?${query}`),
    enabled: !preview && mode !== "sources",
  });
  const notifications = useQuery({
    queryKey: ["notifications", preview],
    queryFn: () => request<{ items: Notification[] }>("/notifications"),
    enabled: !preview && mode !== "sources",
  });
  const detail = useQuery({
    queryKey: ["incident", selectedId, preview],
    queryFn: () =>
      request<Incident>(`/incidents/${encodeURIComponent(selectedId)}`),
    enabled: !preview && !!selectedId && mode !== "sources",
  });
  const receipts = useQuery({
    queryKey: ["incident-receipts", selectedId, preview],
    queryFn: () =>
      request<Receipts>(`/incidents/${encodeURIComponent(selectedId)}/events`),
    enabled: !preview && !!selectedId && mode !== "sources",
  });
  const catalog = useQuery({
    queryKey: ["operations-catalog", preview],
    queryFn: () => request<Catalog>("/operations/catalog"),
    enabled: !preview && mode === "sources" && user.role === "admin",
  });
  const rows = (incidents.data?.items ?? []).filter(
    (i) => mode !== "alerts" || !i.suspected,
  );
  const filtered = rows.filter(
    (i) =>
      (!severity || i.severity === severity) &&
      (!text.trim() ||
        `${i.title} ${i.summary} ${i.source_id} ${i.fingerprint}`
          .toLowerCase()
          .includes(text.trim().toLowerCase())) &&
      (state === "all" ||
        (state === "mine"
          ? i.assignee_id === user.id
          : state === "firing" || state === "resolved"
            ? i.alert_status === state
            : i.status === state)),
  );
  const pagination = useListPagination(
    filtered,
    JSON.stringify([mode, severity, text, state, scope.query]),
  );
  const sourcePages = useListPagination(catalog.data?.sources ?? [], mode);
  const channelPages = useListPagination(catalog.data?.channels ?? [], mode);
  const active = rows.filter((i) => i.alert_status === "firing").length;
  function select(id: string) {
    const next = new URLSearchParams(params);
    if (id) next.set("incident", id);
    else next.delete("incident");
    setParams(next);
  }
  function scopeLabel(i: { application_id: string; environment_id: string }) {
    const a = applications.find((v) => v.id === i.application_id);
    return `${a?.name ?? (i.application_id || "待分配应用")} / ${a?.environments?.find((v) => v.id === i.environment_id)?.name ?? (i.environment_id || "待分配环境")}`;
  }
  function observe(i: Incident, kind: "metrics" | "logs") {
    const next = new URLSearchParams({
      application: i.application_id,
      environment: i.environment_id,
      incident: i.id,
    });
    if (params.has("environment_scope"))
      next.set("environment_scope", params.get("environment_scope")!);
    if (preview) next.set("preview", "1");
    return `/observability/${kind}?${next}`;
  }
  async function act(i: Incident, action: "claim" | "investigation") {
    if (preview || user.role === "viewer") return;
    setBusy(i.id);
    try {
      const result = await post<Incident>(
        `/incidents/${i.id}/${action}`,
        action === "claim"
          ? { assignee_id: i.assignee_id === user.id ? "" : user.id }
          : {},
      );
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["incidents"] }),
        qc.invalidateQueries({ queryKey: ["incident", i.id] }),
        qc.invalidateQueries({ queryKey: ["dashboard"] }),
      ]);
      if (action === "investigation") {
        setScope(result.application_id, result.environment_id);
        setInvestigationId(result.investigation_id);
        navigate(`/investigations/${result.investigation_id}`);
      } else
        notify(
          i.assignee_id === user.id ? "已取消认领" : "已认领事件",
          "success",
        );
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy("");
    }
  }
  function actions(i: Incident) {
    return (
      <div className="management-actions">
        <button
          className="btn"
          disabled={
            preview ||
            user.role === "viewer" ||
            !!busy ||
            !i.application_id ||
            (!!i.assignee_id && i.assignee_id !== user.id)
          }
          onClick={() => void act(i, "claim")}
        >
          {i.assignee_id === user.id
            ? "取消认领"
            : i.assignee_id
              ? "已被认领"
              : "认领"}
        </button>
        {i.investigation_id ? (
          <Link
            className="btn btn-primary"
            to={`/investigations/${i.investigation_id}`}
          >
            打开调查 <ArrowRight size={14} />
          </Link>
        ) : (
          <button
            className="btn btn-primary"
            disabled={
              preview || user.role === "viewer" || !!busy || !i.application_id
            }
            onClick={() => void act(i, "investigation")}
          >
            <Search size={14} />
            创建调查
          </button>
        )}
      </div>
    );
  }
  const titles = {
    alerts: "告警列表",
    incidents: "事件中心",
    sources: "告警来源与通知",
  };
  const current = detail.data;
  return (
    <div className="management-page alerts-page">
      <PageHeader
        title={titles[mode]}
        description="先查看告警和观测数据，需要时再把证据带入调查。"
        actions={
          <button
            className="btn"
            disabled={preview || (mode === "sources" && user.role !== "admin")}
            onClick={() => {
              if (mode === "sources") void catalog.refetch();
              else {
                void incidents.refetch();
                void notifications.refetch();
                if (selectedId) {
                  void detail.refetch();
                  void receipts.refetch();
                }
              }
            }}
          >
            <RefreshCw size={15} />
            刷新
          </button>
        }
      />
      {mode !== "sources" && <ScopeFilter />}
      {preview && (
        <p className="management-notice">
          设计预览未连接告警源，不展示虚构告警或投递成功记录。
        </p>
      )}
      {mode === "sources" ? (
        user.role !== "admin" ? (
          <p className="management-notice">告警来源与通知映射由管理员维护。</p>
        ) : (
          <>
            {catalog.error && <ErrorNotice error={catalog.error} />}
            <p className="management-notice">
              这里显示服务器登记的来源和路由。告警规则与静默仍由原监控系统管理；登记不代表已连通或已成功投递。
            </p>
            <section className="card management-card">
              <h2>告警来源</h2>
              {sourcePages.items.map((s) => (
                <article className="alert-source" key={s.id}>
                  <div className="management-heading">
                    <strong>{s.id}</strong>
                    <span className="badge">
                      {s.type} · {statusName(s.state)}
                    </span>
                  </div>
                  {s.mappings.map((m, n) => (
                    <div className="alert-mapping" key={n}>
                      <span>{scopeLabel(m)}</span>
                      <code>
                        {Object.entries(m.labels)
                          .map(([k, v]) => `${k}=${v}`)
                          .join(" · ")}
                      </code>
                    </div>
                  ))}
                  {!s.mappings.length && (
                    <p className="muted">
                      未登记应用路由；收到的异常需管理员处理。
                    </p>
                  )}
                </article>
              ))}
              {!catalog.data?.sources?.length && !catalog.error && (
                <p className="management-empty">
                  {catalog.isPending && !preview
                    ? "正在读取…"
                    : "尚未登记告警来源"}
                </p>
              )}
              <Pagination
                page={sourcePages.page}
                pageSize={sourcePages.pageSize}
                total={sourcePages.total}
                onPageChange={sourcePages.setPage}
                onPageSizeChange={sourcePages.setPageSize}
                totalLabel={`本次来源结果共 ${sourcePages.total} 条`}
              />
            </section>
            <section className="card management-card">
              <h2>通知渠道</h2>
              {channelPages.items.map((c) => (
                <article className="management-row" key={c.id}>
                  <Bell size={17} />
                  <div className="grow">
                    <strong>
                      {c.id} · {c.type === "feishu" ? "飞书" : c.type}
                    </strong>
                    <small>{scopeLabel(c.scope)}</small>
                  </div>
                  <span className="badge">{statusName(c.state)}</span>
                </article>
              ))}
              {(preview || catalog.isSuccess) &&
                !catalog.data?.channels?.length && (
                  <p className="management-empty">尚未登记通知渠道</p>
                )}
              {catalog.isPending && !preview && (
                <p className="management-empty">正在读取通知渠道…</p>
              )}
              <Pagination
                page={channelPages.page}
                pageSize={channelPages.pageSize}
                total={channelPages.total}
                onPageChange={channelPages.setPage}
                onPageSizeChange={channelPages.setPageSize}
                totalLabel={`本次渠道结果共 ${channelPages.total} 条`}
              />
              <p className="muted">渠道凭据留在服务器，不在浏览器展示。</p>
            </section>
            <Link className="btn" to="/integrations">
              查看集成配置
            </Link>
          </>
        )
      ) : (
        <>
          <div className="alert-summary">
            <section className="card">
              <span>当前读取范围</span>
              <strong>{rows.length}</strong>
              <small>最多返回最近 200 条记录</small>
            </section>
            <section className="card">
              <span>触发中</span>
              <strong>{active}</strong>
              <small>仅统计已加载记录</small>
            </section>
            <section className="card">
              <span>由我处理</span>
              <strong>
                {rows.filter((i) => i.assignee_id === user.id).length}
              </strong>
              <small>认领不会自动执行恢复</small>
            </section>
          </div>
          <div className="alert-filters">
            <FilterBar label="告警与事件筛选">
              <FilterSearch
                label="搜索告警或事件"
                value={searchDraft}
                onChange={setSearchDraft}
                onSubmit={() => setText(searchDraft)}
                placeholder="搜索标题、摘要、来源…"
              />
              <FilterSelect
                id="alert-severity"
                label="级别"
                emptyLabel="全部级别"
                values={severity ? [severity] : []}
                onChange={(values) => setSeverity(values[0] ?? "")}
                options={[...new Set(rows.map((i) => i.severity))]
                  .sort()
                  .map((s) => ({ value: s, label: severityName(s) }))}
              />
              <FilterSelect
                id="alert-status"
                label="状态"
                emptyLabel="全部状态"
                values={state === "all" ? [] : [state]}
                onChange={(values) => setState(values[0] ?? "all")}
                options={[
                  { value: "firing", label: "告警触发中" },
                  { value: "resolved", label: "告警已恢复" },
                  { value: "open", label: "事件待处理" },
                  { value: "investigating", label: "调查中" },
                  { value: "mine", label: "由我处理" },
                ]}
              />
            </FilterBar>
          </div>
          {incidents.error && <ErrorNotice error={incidents.error} />}
          <p className="muted">
            {mode === "alerts"
              ? "展示外部告警的接收记录；AI 巡检疑似异常在事件中心查看。"
              : "同一告警与调查沿用同一条事实记录，不重复创建事件。"}{" "}
            告警已恢复不等于调查已结案。
          </p>
          <div className="card management-table-wrap">
            <table className="management-table ui-data-table">
              <thead>
                <tr>
                  <th>告警 / 事件</th>
                  <th>应用 / 环境</th>
                  <th>级别与状态</th>
                  <th>更新时间</th>
                  <th>处理</th>
                </tr>
              </thead>
              <tbody>
                {pagination.items.map((i) => (
                  <tr key={i.id}>
                    <td>
                      <button
                        className="alert-title"
                        onClick={() => select(i.id)}
                      >
                        <AlertTriangle size={15} />
                        {i.title}
                      </button>
                      <small>
                        {i.source_id}
                        {i.suspected ? " · AI 疑似" : ""}
                      </small>
                    </td>
                    <td>{scopeLabel(i)}</td>
                    <td>
                      <span className="badge">{severityName(i.severity)}</span>
                      <small>
                        {statusName(i.alert_status)} · {statusName(i.status)}
                      </small>
                    </td>
                    <td>{timeLabel(i.updated_at)}</td>
                    <td>{actions(i)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!filtered.length && !incidents.error && (
              <p className="management-empty">
                {incidents.isPending && !preview
                  ? "正在读取…"
                  : "没有符合筛选的记录；这不代表所有服务都健康。"}
              </p>
            )}
          </div>
          <Pagination
            page={pagination.page}
            pageSize={pagination.pageSize}
            total={pagination.total}
            onPageChange={pagination.setPage}
            onPageSizeChange={pagination.setPageSize}
            totalLabel={`本次结果共 ${pagination.total} 条`}
            loading={incidents.isFetching}
          />
          <Sheet
            open={!!selectedId}
            onOpenChange={(open) => !open && select("")}
            title="告警与事件详情"
            className="alert-detail-sheet"
            size="detail"
          >
            {detail.error && <ErrorNotice error={detail.error} />}
            {detail.isPending && !preview && <p>正在读取事件…</p>}
            {current && (
              <>
                <h3>{current.title}</h3>
                <p>{current.summary}</p>
                <dl className="alert-details">
                  {[
                    ["范围", scopeLabel(current)],
                    ["来源", current.source_id],
                    ["告警状态", statusName(current.alert_status)],
                    ["处理状态", statusName(current.status)],
                    ["开始时间", timeLabel(current.starts_at)],
                    ["告警恢复", timeLabel(current.ends_at)],
                    [
                      "认领",
                      current.assignee_id === user.id
                        ? "由我处理"
                        : current.assignee_id
                          ? "已由其他成员认领"
                          : "尚未认领",
                    ],
                  ].map(([k, v]) => (
                    <div key={k}>
                      <dt>{k}</dt>
                      <dd>{v}</dd>
                    </div>
                  ))}
                </dl>
                {actions(current)}
                {current.application_id && current.environment_id && (
                  <div className="management-actions management-section">
                    <Link className="btn" to={observe(current, "metrics")}>
                      查看同环境指标
                    </Link>
                    <Link className="btn" to={observe(current, "logs")}>
                      查看同环境日志
                    </Link>
                  </div>
                )}
                {current.suspected && current.environment_id && !preview && (
                  <IncidentEvidence
                    incidentId={current.id}
                    environmentId={current.environment_id}
                  />
                )}
                <h3>告警接收记录</h3>
                <p className="muted">
                  接收时间与来源告警时间可能不同。相同通知会去重；这里不代表全部操作审计。
                </p>
                {receipts.error && <ErrorNotice error={receipts.error} />}
                <ol className="alert-timeline">
                  {(receipts.data?.items ?? []).map((r) => (
                    <li key={r.id}>
                      <time>{timeLabel(r.received_at)}</time>
                      <span>{statusName(r.status)}</span>
                    </li>
                  ))}
                </ol>
                {receipts.data?.truncated && (
                  <p className="muted">
                    仅展示最近 {receipts.data.limit} 条接收记录。
                  </p>
                )}
                {!receipts.isPending &&
                  !receipts.error &&
                  !receipts.data?.items.length && (
                    <p className="muted">暂无外部告警接收记录。</p>
                  )}
                <h3>此事件的通知</h3>
                {notifications.isPending && !preview && (
                  <p className="muted">正在读取通知记录…</p>
                )}
                {notifications.error && (
                  <ErrorNotice error={notifications.error} />
                )}
                {(notifications.data?.items ?? [])
                  .filter((n) => n.incident_id === current.id)
                  .map((n) => (
                    <div className="management-row" key={n.id}>
                      <div className="grow">
                        <strong>
                          {n.channel === "feishu" ? "飞书" : n.channel} ·{" "}
                          {statusName(n.status)}
                        </strong>
                        <small>
                          尝试 {n.attempts} 次 · {timeLabel(n.created_at)}
                        </small>
                        <small>{n.last_error}</small>
                      </div>
                    </div>
                  ))}
                {notifications.isSuccess &&
                  !(notifications.data?.items ?? []).some(
                    (n) => n.incident_id === current.id,
                  ) && (
                    <p className="muted">
                      最近返回的通知中无此事件记录。无记录不代表发送成功。
                    </p>
                  )}
              </>
            )}
          </Sheet>
        </>
      )}
    </div>
  );
}
