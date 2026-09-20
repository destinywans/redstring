import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  CheckCheck,
  Clock,
  ShieldCheck,
} from "lucide-react";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { ErrorNotice } from "../../components/ui";
import { type Dashboard, type Todo, statusName, timeLabel } from "./operations";
import "./workspace.css";
const empty: Dashboard = {
  incidents: [],
  pending_approvals: [],
  executions: [],
  my_items: [],
  automation_runs: [],
  notification_failures: [],
  data_sources: [],
};
export default function DashboardPage() {
  const { preview, user, applications } = useWorkspace();
  const q = useQuery({
    queryKey: ["dashboard", preview],
    queryFn: () => request<Dashboard>("/dashboard"),
    enabled: !preview,
    refetchInterval: 30000,
  });
  const data = q.data ?? empty;
  const tasks = (title: string, items: Todo[], icon: React.ReactNode) => (
    <section className="card management-card">
      <h2>
        {icon}
        {title}
      </h2>
      {items.length ? (
        items.map((t) => (
          <Link
            className="management-row"
            key={t.id}
            to={
              t.kind === "execution"
                ? `/recovery?execution=${encodeURIComponent(t.id)}`
                : t.kind === "approval"
                  ? `/actions?plan=${encodeURIComponent(t.id)}`
                  : t.investigation_id
                    ? `/investigations/${t.investigation_id}`
                    : "/incidents"
            }
          >
            <div className="grow">
              <strong>{t.title}</strong>
              <small>
                {statusName(t.status)} · {timeLabel(t.updated_at)}
              </small>
            </div>
            <ArrowRight size={16} />
          </Link>
        ))
      ) : (
        <p className="management-empty">暂无{title}</p>
      )}
    </section>
  );
  return (
    <div className="management-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">WORKSPACE</span>
          <h1>工作台</h1>
          <p>{user.name}，先处理需要关注的变化。</p>
        </div>
        <Link className="btn btn-primary" to="/applications">
          查看应用 <ArrowRight size={16} />
        </Link>
      </header>
      {preview && (
        <div className="management-notice">
          设计预览 · 此页展示真实工作台的空状态，未连接数据源。
        </div>
      )}
      {q.error && <ErrorNotice error={q.error} />}
      <div className="management-grid">
        {[
          { label: "可访问应用", value: applications.length, icon: Activity },
          {
            label: "待关注异常",
            value: data.incidents.filter((i) => i.alert_status !== "resolved")
              .length,
            icon: AlertTriangle,
          },
          {
            label: "待审批",
            value: data.pending_approvals.length,
            icon: ShieldCheck,
          },
        ].map((s) => (
          <div className="card management-stat" key={s.label}>
            <span>
              <s.icon size={18} /> {s.label}
            </span>
            <strong>{q.isPending && !preview ? "—" : s.value}</strong>
          </div>
        ))}
      </div>
      <section className="management-section">
        <div className="management-heading">
          <h2>需要关注</h2>
          <Link to="/incidents">所有异常 →</Link>
        </div>
        <div className="card">
          {data.incidents.length ? (
            data.incidents.map((i) => (
              <Link
                className="management-row"
                to={`/incidents?incident=${encodeURIComponent(i.id)}`}
                key={i.id}
              >
                <span
                  className={`management-dot ${i.severity === "critical" ? "critical" : ""}`}
                />
                <div className="grow">
                  <strong>{i.title}</strong>
                  <small>
                    {applications.find((a) => a.id === i.application_id)
                      ?.name ?? "待分配应用"}{" "}
                    · {statusName(i.alert_status)} · {timeLabel(i.updated_at)}
                  </small>
                </div>
                {i.suspected && <span className="badge">AI 疑似异常</span>}
                <ArrowRight size={16} />
              </Link>
            ))
          ) : (
            <div className="management-empty">
              <CheckCheck size={24} />
              <p>
                {q.isPending && !preview
                  ? "正在读取异常…"
                  : "当前没有已记录的异常"}
              </p>
              <small>
                没有告警不代表系统健康。请结合数据源连接与最近观测判断。
              </small>
            </div>
          )}
        </div>
      </section>
      <div className="management-grid management-section">
        {tasks("我的待办", data.my_items, <Clock size={18} />)}
        {tasks("待审批提案", data.pending_approvals, <ShieldCheck size={18} />)}
        {tasks("恢复执行", data.executions, <Activity size={18} />)}
      </div>
      <section className="management-section">
        <h2>自动化与连接状态</h2>
        <div className="card">
          {data.automation_runs.map((r) => (
            <div className="management-row" key={r.id}>
              <div className="grow">
                <strong>{r.kind === "patrol" ? "主动巡检" : "告警初查"}</strong>
                <small>
                  {r.environment_id} · {timeLabel(r.created_at)}
                  {r.error && ` · ${r.error}`}
                </small>
              </div>
              <span className="badge">{statusName(r.status)}</span>
              {r.investigation_id && (
                <Link to={`/investigations/${r.investigation_id}`}>
                  查看调查
                </Link>
              )}
            </div>
          ))}
          {data.notification_failures.map((n) => (
            <div className="management-row" key={n.id}>
              <AlertTriangle size={18} />
              <div>
                <strong>{n.channel} 通知失败</strong>
                <small>{n.last_error || "请检查通知连接配置"}</small>
              </div>
            </div>
          ))}
          {data.data_sources.map((s, i) => (
            <div className="management-row" key={s.id ?? i}>
              <Activity size={18} />
              <div>
                <strong>{s.name ?? s.id}</strong>
                <small>
                  {statusName(s.status ?? "unknown")} {s.error}
                </small>
              </div>
            </div>
          ))}
          {!data.automation_runs.length &&
            !data.notification_failures.length &&
            !data.data_sources.length && (
              <p className="management-empty">
                暂无自动化运行记录。管理员可在应用环境中配置告警初查与巡检。
              </p>
            )}
        </div>
      </section>
    </div>
  );
}
