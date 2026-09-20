import { useSearchParams } from "react-router-dom";
import { useWorkspace } from "../lib/workspace";
import "./scope.css";

const ids = (p: URLSearchParams, many: string, one: string) =>
  [
    ...new Set((p.get(many) ?? p.get(one) ?? "").split(",").filter(Boolean)),
  ].sort();
export function usePageScope() {
  const { applications } = useWorkspace();
  const [params, setParams] = useSearchParams();
  const applicationIds =
    params.get("scope") === "all"
      ? []
      : ids(params, "applications", "application");
  const environmentIds =
    params.get("scope") === "all"
      ? []
      : ids(params, "environments", "environment");
  const apps = applications.filter(
    (a) =>
      !a.archived && (!applicationIds.length || applicationIds.includes(a.id)),
  );
  const targets = apps.flatMap((app) =>
    (app.environments ?? [])
      .filter(
        (env) =>
          !env.archived &&
          (!environmentIds.length || environmentIds.includes(env.id)),
      )
      .map((env) => ({ app, env })),
  );
  const query = new URLSearchParams();
  if (applicationIds.length)
    query.set("applications", applicationIds.join(","));
  if (environmentIds.length)
    query.set("environments", environmentIds.join(","));
  const update = (apps: string[], envs: string[]) => {
    const next = new URLSearchParams(params);
    for (const k of [
      "application",
      "environment",
      "applications",
      "environments",
      "scope",
      "incident",
    ])
      next.delete(k);
    const put = (values: string[], one: string, many: string) => {
      const sorted = [...new Set(values)].sort();
      if (sorted.length)
        next.set(sorted.length === 1 ? one : many, sorted.join(","));
    };
    put(apps, "application", "applications");
    put(envs, "environment", "environments");
    setParams(next);
  };
  return {
    applicationIds,
    environmentIds,
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
        <label>
          应用
          <select
            className="field"
            value={app?.id ?? ""}
            onChange={(e) =>
              scope.update(e.target.value ? [e.target.value] : [], [])
            }
          >
            <option value="">选择应用</option>
            {available.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          环境
          <select
            className="field"
            disabled={!app}
            value={scope.environmentIds[0] ?? ""}
            onChange={(e) =>
              scope.update(
                app ? [app.id] : [],
                e.target.value ? [e.target.value] : [],
              )
            }
          >
            <option value="">选择环境</option>
            {app?.environments
              ?.filter((e) => !e.archived)
              .map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
          </select>
        </label>
      </div>
    );
  }
  return (
    <div className="page-scope" aria-label="当前页面范围">
      <details className="scope-menu">
        <summary>
          {scope.applicationIds.length
            ? `已选 ${scope.applicationIds.length} 个应用`
            : "全部授权应用"}
        </summary>
        <div className="scope-options">
          <button className="btn" onClick={() => scope.update([], [])}>
            查看全部
          </button>
          {available.map((a) => (
            <label key={a.id}>
              <input
                type="checkbox"
                checked={scope.applicationIds.includes(a.id)}
                onChange={(e) => {
                  const next = e.target.checked
                    ? [...scope.applicationIds, a.id]
                    : scope.applicationIds.filter((id) => id !== a.id);
                  scope.update(next, []);
                }}
              />
              {a.name}
            </label>
          ))}
        </div>
      </details>
      <details className="scope-menu">
        <summary>
          {scope.environmentIds.length
            ? `已选 ${scope.environmentIds.length} 个环境`
            : "全部环境"}
        </summary>
        <div className="scope-options">
          <button
            className="btn"
            onClick={() => scope.update(scope.applicationIds, [])}
          >
            查看全部环境
          </button>
          {scope.apps.flatMap((app) =>
            (app.environments ?? [])
              .filter((e) => !e.archived)
              .map((env) => (
                <label key={env.id}>
                  <input
                    type="checkbox"
                    checked={scope.environmentIds.includes(env.id)}
                    onChange={(e) =>
                      scope.update(
                        scope.applicationIds,
                        e.target.checked
                          ? [...scope.environmentIds, env.id]
                          : scope.environmentIds.filter((id) => id !== env.id),
                      )
                    }
                  />
                  {app.name} · {env.name}
                </label>
              )),
          )}
        </div>
      </details>
      <span className="muted">
        {scope.apps.length} 个应用 · {scope.targets.length} 个环境
      </span>
      {(scope.applicationIds.length > 0 || scope.environmentIds.length > 0) && (
        <button className="btn btn-ghost" onClick={() => scope.update([], [])}>
          清除范围
        </button>
      )}
    </div>
  );
}
