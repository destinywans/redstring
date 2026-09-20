import { Link } from "react-router-dom";
import { GitBranch, ArrowRight } from "../../lib/icons";
import { useEnvironmentScope } from "../../lib/environmentScope";
import "./paused.css";

/** Keep a discoverable destination while delivery product design is paused. */
export default function DeliveryPausedPage() {
  const scope = useEnvironmentScope();
  return (
    <section
      className="delivery-paused"
      aria-labelledby="delivery-paused-title"
    >
      <div className="delivery-paused-icon" aria-hidden="true">
        <GitBranch size={26} />
      </div>
      <span className="delivery-paused-status">暂缓建设</span>
      <h1 id="delivery-paused-title">交付中心</h1>
      <p>CI/CD 功能暂缓建设，待流程与使用方式确定后继续。</p>
      <Link className="btn" to={scope.withScope("/")}>
        返回工作台 <ArrowRight size={15} />
      </Link>
    </section>
  );
}
