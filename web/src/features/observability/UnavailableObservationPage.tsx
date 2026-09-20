import ScopeFilter from "../../components/ScopeFilter";
import { Link, useLocation } from "react-router-dom";
import { Network, Workflow } from "../../lib/icons";
import { useWorkspace } from "../../lib/workspace";
import "../workspace/workspace.css";
import "./observability.css";
export default function UnavailableObservationPage({
  kind,
}: {
  kind: "traces" | "topology";
}) {
  const { application, environment } = useWorkspace();
  const traces = kind === "traces";
  const location = useLocation();
  return (
    <div className="management-page observability-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">OBSERVABILITY</span>
          <h1>{traces ? "调用链" : "服务拓扑"}</h1>
          <p>
            {application?.name ?? "全部授权应用"} /{" "}
            {environment?.name ?? "全部环境"}
          </p>
        </div>
      </header>
      <ScopeFilter />
      <section className="card management-card observation-empty">
        {traces ? <Workflow size={30} /> : <Network size={30} />}
        <span className="badge">尚未接入</span>
        <h2>{traces ? "调用链查询尚未开放" : "服务依赖图尚未开放"}</h2>
        <p>
          {traces
            ? "此功能需要接入真实 Trace 数据源及应用关联。当前工作区不会把日志或示例数据展示为调用链。"
            : "此功能需要可追溯的服务调用关系或基础设施关系数据。当前尚无此数据，不推断或展示虚构拓扑。"}
        </p>
        <div className="management-actions">
          <Link
            className="btn"
            to={{ pathname: "/observability/metrics", search: location.search }}
          >
            查看指标
          </Link>
          <Link
            className="btn"
            to={{ pathname: "/observability/logs", search: location.search }}
          >
            查看日志
          </Link>
        </div>
      </section>
    </div>
  );
}
