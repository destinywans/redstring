import { IncidentEvidence } from "./AutomationEvidence";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AlertTriangle, Search, RefreshCw } from "lucide-react";
import { request, post } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { ErrorNotice } from "../../components/ui";
import {
  type Incident,
  type Notification,
  statusName,
  timeLabel,
} from "./operations";
import "./workspace.css";
export default function IncidentsPage() {
  const { preview, user, applications, notify, setInvestigationId } =
      useWorkspace(),
    qc = useQueryClient(),
    navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const selectedIncident = searchParams.get("incident");
  const [app, setApp] = useState(""),
    [state, setState] = useState(selectedIncident ? "all" : "active"),
    [busy, setBusy] = useState("");
  const q = useQuery({
    queryKey: ["incidents", app, preview],
    queryFn: () =>
      request<{ items: Incident[] }>(
        `/incidents${app ? `?application_id=${encodeURIComponent(app)}` : ""}`,
      ),
    enabled: !preview,
    refetchInterval: 15000,
  });
  const n = useQuery({
    queryKey: ["notifications", preview],
    queryFn: () => request<{ items: Notification[] }>("/notifications"),
    enabled: !preview,
  });
  async function act(i: Incident, action: "claim" | "investigation") {
    setBusy(i.id);
    try {
      const result = await post<{ investigation_id: string }>(
        `/incidents/${i.id}/${action}`,
        action === "claim"
          ? { assignee_id: i.assignee_id === user.id ? "" : user.id }
          : {},
      );
      await qc.invalidateQueries({ queryKey: ["incidents"] });
      await qc.invalidateQueries({ queryKey: ["dashboard"] });
      if (action === "investigation") {
        setInvestigationId(result.investigation_id);
        navigate(`/investigations/${result.investigation_id}`);
      }
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy("");
    }
  }
  const items = (q.data?.items ?? []).filter(
    (i) =>
      state === "all" ||
      (state === "mine"
        ? i.assignee_id === user.id
        : i.alert_status !== "resolved"),
  );
  return (
    <div className="management-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">INCIDENT INBOX</span>
          <h1>异常收件箱</h1>
          <p>从可信告警和有证据的巡检发现开始调查。</p>
        </div>
        <button
          className="btn"
          onClick={() => void q.refetch()}
          disabled={preview}
        >
          <RefreshCw size={16} />
          刷新
        </button>
      </header>
      <div className="management-actions">
        <select
          className="field"
          aria-label="异常应用范围"
          value={app}
          onChange={(e) => setApp(e.target.value)}
        >
          <option value="">所有授权应用</option>
          {applications.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        <select
          className="field"
          aria-label="异常状态"
          value={state}
          onChange={(e) => setState(e.target.value)}
        >
          <option value="active">触发中</option>
          <option value="mine">分配给我</option>
          <option value="all">全部记录</option>
        </select>
      </div>
      {q.error && <ErrorNotice error={q.error} />}
      <div className="management-notice">
        告警恢复只表示触发条件解除。调查结论与业务恢复验证需要分别确认。
      </div>
      <div className="card">
        {items.map((i) => (
          <article
            className={`management-row ${selectedIncident === i.id ? "incident-highlight" : ""}`}
            key={i.id}
          >
            <AlertTriangle size={20} />
            <div className="grow">
              <strong>{i.title}</strong>
              <small>
                {applications.find((a) => a.id === i.application_id)?.name ??
                  "待管理员分配"}{" "}
                / {i.environment_id || "未分配环境"} · {timeLabel(i.starts_at)}
              </small>
              <p>{i.summary}</p>
              <div className="management-meta">
                <span className="badge">{statusName(i.alert_status)}</span>
                <span className="badge">{statusName(i.status)}</span>
                {i.suspected && <span className="badge">AI 疑似 · 需确认</span>}
                {i.assignee_id === user.id && (
                  <span className="badge">由我处理</span>
                )}
              </div>
            </div>
            <div className="management-actions">
              {user.role !== "viewer" && (
                <button
                  className="btn"
                  disabled={busy === i.id || !i.application_id}
                  onClick={() => void act(i, "claim")}
                >
                  {i.assignee_id === user.id ? "取消认领" : "认领"}
                </button>
              )}
              {i.suspected && i.environment_id && !preview && (
                <IncidentEvidence
                  incidentId={i.id}
                  environmentId={i.environment_id}
                />
              )}
              {i.investigation_id ? (
                <Link
                  className="btn btn-primary"
                  to={`/investigations/${i.investigation_id}`}
                >
                  继续调查
                </Link>
              ) : (
                <button
                  className="btn btn-primary"
                  disabled={
                    user.role === "viewer" || busy === i.id || !i.application_id
                  }
                  onClick={() => void act(i, "investigation")}
                >
                  <Search size={15} />
                  创建调查
                </button>
              )}
            </div>
          </article>
        ))}
        {!items.length && (
          <div className="management-empty">
            {q.isPending && !preview ? "正在加载…" : "当前筛选下没有异常记录"}
          </div>
        )}
      </div>
      <section className="management-section">
        <h2>通知投递</h2>
        {n.error && <ErrorNotice error={n.error} />}
        <div className="card management-table-wrap">
          <table className="management-table">
            <thead>
              <tr>
                <th>渠道</th>
                <th>状态</th>
                <th>投递次数</th>
                <th>最近信息</th>
              </tr>
            </thead>
            <tbody>
              {(n.data?.items ?? []).map((x) => (
                <tr key={x.id}>
                  <td>{x.channel === "feishu" ? "飞书" : x.channel}</td>
                  <td>{statusName(x.status)}</td>
                  <td>{x.attempts}</td>
                  <td>{x.last_error || timeLabel(x.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {!n.data?.items?.length && (
            <p className="management-empty">
              暂无通知记录。飞书群映射由管理员在服务端配置。
            </p>
          )}
        </div>
      </section>
    </div>
  );
}
