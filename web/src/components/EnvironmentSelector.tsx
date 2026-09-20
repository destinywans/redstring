import { useLocation } from "react-router-dom";
import { useEnvironmentScope } from "../lib/environmentScope";
import "./environment.css";

export default function EnvironmentSelector() {
  const scope = useEnvironmentScope();
  const location = useLocation();
  const params = new URLSearchParams(location.search);
  const explicit =
    params.has("environment") ||
    params.has("environments") ||
    params.get("scope") === "all" ||
    params.get("environment_filter") === "all";
  const fixed =
    /^\/investigations\/[^/]+/.test(location.pathname) ||
    location.pathname === "/actions" ||
    location.pathname === "/recovery";
  return (
    <div className="sidebar-environment">
      <label htmlFor="global-environment">环境</label>
      <div className="sidebar-environment-field">
        <span className="environment-scope-dot" aria-hidden="true" />
        <select
          id="global-environment"
          aria-describedby="global-environment-help"
          value={scope.key}
          disabled={scope.loading}
          onChange={(event) => scope.setEnvironmentScope(event.target.value)}
        >
          <option value="">全部环境</option>
          {scope.key && !scope.hasMatches && (
            <option value={scope.key}>当前环境不可用</option>
          )}
          {scope.options.map((option) => (
            <option key={option.key} value={option.key}>
              {option.label}
            </option>
          ))}
        </select>
      </div>
      <small id="global-environment-help">
        {scope.loading
          ? "正在加载环境…"
          : fixed
            ? "当前会话与操作保留原目标"
            : explicit
              ? "当前页面优先使用指定范围"
              : "当前查看范围"}
      </small>
    </div>
  );
}
