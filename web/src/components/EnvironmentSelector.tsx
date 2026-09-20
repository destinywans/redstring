import SelectField from "./SelectField";
import { useLocation } from "react-router-dom";
import { ChevronDown } from "../lib/icons";
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
    <div className="header-environment">
      <div className="header-environment-field">
        <SelectField
          id="global-environment"
          aria-label="选择全局环境"
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
        </SelectField>
        <ChevronDown size={12} aria-hidden="true" />
      </div>
      <span className="header-environment-help" id="global-environment-help">
        {scope.loading
          ? "正在加载环境…"
          : fixed
            ? "当前会话与操作保留原目标"
            : explicit
              ? "当前页面优先使用指定范围"
              : "当前查看范围"}
      </span>
    </div>
  );
}
