import { useSearchParams } from "react-router-dom";
import { useWorkspace } from "../lib/workspace";
import { useEnvironmentScope } from "../lib/environmentScope";
import { FilterBar, FilterSelect } from "./filters/FilterBar";
import "./scope.css";

const ids = (p: URLSearchParams, many: string, one: string) =>
  [
    ...new Set((p.get(many) ?? p.get(one) ?? "").split(",").filter(Boolean)),
  ].sort();
export function usePageScope({ globalEnvironmentOnly = true } = {}) {
  const { applications } = useWorkspace();
  const globalEnvironment = useEnvironmentScope();
  const [params, setParams] = useSearchParams();
  const applicationIds =
    params.get("scope") === "all"
      ? []
      : ids(params, "applications", "application");
  // List filters have one environment authority: the header. Legacy object
  // targets are resolved separately by useWorkspaceSelection / fixed sessions.
  const allPageEnvironments = params.get("scope") === "all" || params.get("environment_filter") === "all";
  const explicitEnvironment = !allPageEnvironments && (params.has("environments") || params.has("environment"));
  const legacyEnvironmentIds = ids(params, "environments", "environment");
  const requestedEnvironmentIds = globalEnvironmentOnly
    ? globalEnvironment.environmentIds
    : explicitEnvironment ? legacyEnvironmentIds : allPageEnvironments ? [] : globalEnvironment.environmentIds;
  const environmentRestricted = globalEnvironmentOnly
    ? !!globalEnvironment.key
    : explicitEnvironment || (!allPageEnvironments && !!globalEnvironment.key);
  const environmentIds =
    environmentRestricted && !requestedEnvironmentIds.length
      ? ["__no_authorized_environment__"]
      : requestedEnvironmentIds;
  const apps = applications.filter(
    (a) =>
      !a.archived && (!applicationIds.length || applicationIds.includes(a.id)),
  );
  const targets = apps.flatMap((app) =>
    (app.environments ?? [])
      .filter(
        (env) =>
          !env.archived &&
          (!environmentRestricted || environmentIds.includes(env.id)),
      )
      .map((env) => ({ app, env })),
  );
  const empty = targets.length === 0;
  const query = new URLSearchParams();
  if (applicationIds.length)
    query.set("applications", applicationIds.join(","));
  // Never serialize a restricted empty scope as no filter: existing list APIs
  // interpret an absent environment filter as all authorized environments.
  if (environmentRestricted)
    query.set(
      "environments",
      environmentIds.length
        ? environmentIds.join(",")
        : "__no_authorized_environment__",
    );
  const update = (apps: string[], envs: string[], allEnvironments = false) => {
    const next = new URLSearchParams(params);
    for (const k of [
      "application",
      "environment",
      "applications",
      "environments",
      "scope",
      "incident",
      "environment_filter",
    ])
      next.delete(k);
    const put = (values: string[], one: string, many: string) => {
      const sorted = [...new Set(values)].sort();
      if (sorted.length)
        next.set(sorted.length === 1 ? one : many, sorted.join(","));
    };
    put(apps, "application", "applications");
    put(envs, "environment", "environments");
    if (allEnvironments) next.set("environment_filter", "all");
    setParams(next);
  };
  return {
    applicationIds,
    environmentIds,
    empty,
    environmentRestricted,
    globalEnvironmentLabel: globalEnvironment.label,
    apps,
    targets,
    query: query.toString(),
    update,
  };
}

export default function ScopeFilter({ single = false }: { single?: boolean }) {
  const { applications } = useWorkspace();
  const scope = usePageScope();
  const available = applications.filter((a) => !a.archived);
  if (single) {
    const app = available.find((a) => a.id === scope.applicationIds[0]);
    return (
      <div className="page-scope">
        <FilterBar label="操作目标">
          <FilterSelect
            id="target-application"
            label="应用"
            emptyLabel="选择应用"
            searchable
            options={available.map((a) => ({ value: a.id, label: a.name }))}
            values={app ? [app.id] : []}
            onChange={(values) => scope.update(values, [])}
          />
          {app && !scope.targets.some(target => target.app.id === app.id) && (
            <span className="scope-summary">当前全局环境下没有此应用的资源</span>
          )}
        </FilterBar>
      </div>
    );
  }
  return (
    <div className="page-scope">
      <FilterBar label="当前页面应用范围">
        <FilterSelect
          id="scope-applications"
          label="应用"
          multiple
          searchable
          emptyLabel="全部授权应用"
          options={available.map((a) => ({ value: a.id, label: a.name }))}
          values={scope.applicationIds}
          onChange={(values) => scope.update(values, [])}
        />
        <span className="scope-summary" role="status">
          {scope.empty && scope.environmentRestricted
            ? "当前范围无可用环境"
            : `${scope.apps.length} 个应用 · ${scope.targets.length} 个环境`}
        </span>
        {scope.applicationIds.length > 0 && (
          <button
            className="filter-text-button"
            type="button"
            onClick={() => scope.update([], [])}
          >
            清除应用筛选
          </button>
        )}
      </FilterBar>
    </div>
  );
}
