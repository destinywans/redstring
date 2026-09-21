import { PageHeader } from "../../components/patterns/ListPage";
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
      <PageHeader
        title={traces ? "调用链" : "调用拓扑"}
        description={`${application?.name ?? "全部授权应用"} / ${environment?.name ?? "全部环境"}`}
      />
      <ScopeFilter />
      <section className="card management-card observation-empty">
        {traces ? <Workflow size={30} /> : <Network size={30} />}
        <span className="badge">尚未接入</span>
        <h2>{traces ? "调用链查询尚未开放" : "调用拓扑尚未接入"}</h2>
        <p>
          {traces
            ? "此功能需要接入真实 Trace 数据源及应用关联。当前工作区不会把日志或示例数据展示为调用链。"
            : "查看指定时间段内实际观测到的服务调用、请求量、错误率与延迟。需要先接入真实调用数据，并关联应用与资源。"}
        </p>
        {!traces && (
          <p>
            资源关系描述部署、归属与依赖；调用拓扑用于分析运行期间的请求流量。
          </p>
        )}
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
