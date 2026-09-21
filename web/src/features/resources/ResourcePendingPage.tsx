import { Link } from "react-router-dom";
import { useEnvironmentScope } from "../../lib/environmentScope";
import { useWorkspace } from "../../lib/workspace";
import "./resources.css";

export default function ResourcePendingPage({
  kind,
}: {
  kind: "catalog" | "relations";
}) {
  const scope = useEnvironmentScope();
  const { preview } = useWorkspace();
  const catalog = kind === "catalog";
  const title = catalog ? "资源目录" : "资源关系";
  const linkTo = (to: string) => {
    const url = new URL(scope.withScope(to), window.location.origin);
    if (preview) url.searchParams.set("preview", "1");
    return `${url.pathname}${url.search}`;
  };

  return (
    <section className="resource-pending" aria-labelledby="resource-page-title">
      <header className="resource-pending-heading">
        <h1 id="resource-page-title">{title}</h1>
        <span className="resource-pending-status">待接入</span>
      </header>
      <p className="resource-pending-purpose">
        {catalog
          ? "按类型、环境和归属查找资源，查看资源信息与关联应用。"
          : "查看应用与资源之间的部署、归属和依赖关系。"}
      </p>
      <div className="resource-pending-notice">
        <h2>{catalog ? "资源清单尚未接入" : "资源关联数据尚未接入"}</h2>
        <p>
          {catalog
            ? "当前可前往全部应用查看已有应用，或进入基础设施查看已接入的资源。"
            : "资源关系用于理解资源如何组织与依赖；运行期间的服务请求流量请查看可观测性中的调用拓扑。"}
        </p>
        <nav className="resource-pending-links" aria-label={`${title}相关入口`}>
          <Link className="btn" to={linkTo("/applications")}>
            查看全部应用
          </Link>
          <Link className="btn" to={linkTo("/infrastructure?view=summary")}>
            查看基础设施
          </Link>
        </nav>
      </div>
    </section>
  );
}
