import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import SelectField from "../../components/SelectField";
import { post, request } from "../../lib/api";
import { useEnvironmentScope } from "../../lib/environmentScope";
import { useWorkspace } from "../../lib/workspace";
import { ChevronLeft, ChevronRight, Plus, X } from "../../lib/icons";
import ResourceDrawer, {
  ResourceIcon,
  ResourceOverlay,
} from "./ResourceDrawer";
import {
  resourceKindLabel,
  resourceKinds,
  resourceTime,
  sourceLabel,
  type Resource,
  type ResourceList,
} from "./types";
import "./resources.css";

export default function ResourceCatalogPage() {
  const scope = useEnvironmentScope();
  const { user } = useWorkspace();
  const [params, setParams] = useSearchParams();
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const filterFields = [
    "q",
    "kind",
    "applications",
    "source_kind",
    "owner_id",
    "team_id",
    "page",
    "page_size",
    "sort",
  ];
  const query = new URLSearchParams();
  for (const field of filterFields)
    for (const value of params.getAll(field)) query.append(field, value);
  query.set("environment_scope", scope.key || "all");
  const applicationAlias =
    params.get("application_id") || params.get("application") || "";
  if (!query.has("applications") && applicationAlias)
    query.set("applications", applicationAlias);
  const resources = useQuery({
    queryKey: ["resources", query.toString()],
    queryFn: ({ signal }) =>
      request<ResourceList>(`/resources?${query}`, { signal }),
  });
  useEffect(() => setSelected(null), [scope.key]);
  const change = (key: string, values: string[]) => {
    const next = new URLSearchParams(params);
    next.delete(key);
    if (key === "applications") {
      next.delete("application");
      next.delete("application_id");
    }
    for (const value of values) if (value) next.append(key, value);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: key === "q" });
  };
  const facets = resources.data?.facets;
  const activeFilters =
    ["q", "kind", "applications", "source_kind", "owner_id", "team_id"].some(
      (key) => params.has(key),
    ) || !!applicationAlias;
  const page = resources.data?.page ?? 1;
  const pageSize = resources.data?.page_size ?? 10;
  const total = resources.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / pageSize));
  return (
    <section className="resource-page">
      <header className="resource-page-heading">
        <div>
          <h1>资源目录</h1>
          <p>管理应用与基础设施资源，查看归属、来源和关联信息。</p>
        </div>
        {user.role === "admin" && (
          <button className="btn primary" onClick={() => setCreating(true)}>
            <Plus size={16} /> 添加资源
          </button>
        )}
      </header>
      <FilterBar label="资源搜索与筛选">
        <FilterSearch
          value={params.get("q") ?? ""}
          onChange={(value) => change("q", value ? [value] : [])}
          onSubmit={() => {}}
          label="搜索资源"
          placeholder="搜索资源名称、IP、ID 或描述"
          showSubmit={false}
        />
        {[
          {
            key: "kind",
            label: "类型",
            options: facets?.kinds.map((option) => ({
              ...option,
              label: resourceKindLabel(option.value),
            })),
          },
          {
            key: "applications",
            label: "关联应用",
            options: facets?.applications,
          },
          {
            key: "source_kind",
            label: "来源",
            options: facets?.sources.map((option) => ({
              ...option,
              label: sourceLabel(option.value),
            })),
          },
          { key: "owner_id", label: "负责人", options: facets?.owners },
          { key: "team_id", label: "部门", options: facets?.teams },
        ].map((field) => (
          <FilterSelect
            key={field.key}
            id={`resource-${field.key}`}
            label={field.label}
            emptyLabel={field.label}
            options={field.options ?? []}
            values={
              field.key === "applications" &&
              !params.has("applications") &&
              applicationAlias
                ? [applicationAlias]
                : params.getAll(field.key)
            }
            onChange={(values) => change(field.key, values)}
            multiple
            searchable
            showLabel={false}
            popoverWidth={232}
          />
        ))}
        {activeFilters && (
          <button
            className="resource-filter-clear icon-button"
            aria-label="清除资源筛选"
            onClick={() => {
              const next = new URLSearchParams(params);
              for (const key of [
                ...filterFields,
                "application",
                "application_id",
              ])
                next.delete(key);
              setParams(next);
            }}
          >
            <X size={16} />
          </button>
        )}
      </FilterBar>
      <div className="resource-table-panel">
        {resources.isPending ? (
          <div className="resource-empty">正在读取资源目录…</div>
        ) : resources.isError ? (
          <div className="resource-error" role="alert">
            {resources.error.message}
            <button className="btn" onClick={() => void resources.refetch()}>
              重新加载
            </button>
          </div>
        ) : (
          <>
            <div className="resource-table-scroll">
              <table className="resource-table">
                <thead>
                  <tr>
                    <th>资源名称 / 类型</th>
                    <th>关联应用</th>
                    <th>负责人 / 部门</th>
                    <th>数据来源</th>
                    <th>
                      <button
                        className="resource-sort"
                        onClick={() =>
                          change(
                            "sort",
                            params.get("sort") === "updated_desc"
                              ? []
                              : ["updated_desc"],
                          )
                        }
                      >
                        更新时间
                        {params.get("sort") === "updated_desc" ? " ↓" : ""}
                      </button>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {resources.data.items.map((resource) => (
                    <tr
                      key={resource.id}
                      className={selected === resource.id ? "selected" : ""}
                    >
                      <td>
                        <button
                          className="resource-name"
                          onClick={() => setSelected(resource.id)}
                        >
                          <span className="resource-icon">
                            <ResourceIcon kind={resource.kind} />
                          </span>
                          <span>
                            <strong>{resource.name}</strong>
                            <small>{resourceKindLabel(resource.kind)}</small>
                          </span>
                        </button>
                      </td>
                      <td>
                        <div className="resource-app-list">
                          {[
                            ...new Map(
                              resource.applications.map((app) => [app.id, app]),
                            ).values(),
                          ].map((app) => (
                            <span key={app.id}>{app.key || app.name}</span>
                          ))}
                          {!resource.applications.length && (
                            <span className="resource-muted">未关联</span>
                          )}
                        </div>
                      </td>
                      <td>
                        <span>{resource.owner_name || "待指定"}</span>
                        <small>{resource.team_name || "部门待指定"}</small>
                      </td>
                      <td>
                        <span
                          className={`resource-source${resource.is_mock ? " is-mock" : ""}`}
                        >
                          {sourceLabel(resource.source_kind)}
                        </span>
                      </td>
                      <td className="resource-time">
                        {resourceTime(resource.updated_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!total && (
                <div className="resource-empty">
                  没有匹配的资源，请调整筛选条件。
                </div>
              )}
            </div>
            <footer className="resource-pagination">
              <span>共 {total} 条</span>
              <FilterSelect
                id="resource-page-size"
                label="每页条数"
                emptyLabel="每页 10 条"
                values={[String(pageSize)]}
                options={[10, 20, 50].map((value) => ({
                  value: String(value),
                  label: `每页 ${value} 条`,
                }))}
                onChange={(values) => change("page_size", values)}
                includeEmptyOption={false}
                showLabel={false}
                popoverWidth={152}
              />
              <button
                className="icon-button"
                aria-label="上一页资源"
                disabled={page <= 1}
                onClick={() => change("page", [String(page - 1)])}
              >
                <ChevronLeft size={16} />
              </button>
              <span>
                第 {page} / {pages} 页
              </span>
              <button
                className="icon-button"
                aria-label="下一页资源"
                disabled={page >= pages}
                onClick={() => change("page", [String(page + 1)])}
              >
                <ChevronRight size={16} />
              </button>
            </footer>
          </>
        )}
      </div>
      {selected && (
        <ResourceDrawer
          resourceId={selected}
          onClose={() => setSelected(null)}
          onSelect={setSelected}
        />
      )}
      {creating && (
        <CreateResource
          onClose={() => setCreating(false)}
          onCreated={(id) => {
            setCreating(false);
            setSelected(id);
          }}
        />
      )}
    </section>
  );
}
function CreateResource({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const scope = useEnvironmentScope();
  const { applications } = useWorkspace();
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [kind, setKind] = useState("deployment");
  const [description, setDescription] = useState("");
  const [externalId, setExternalId] = useState("");
  const [appId, setAppId] = useState("");
  const [envId, setEnvId] = useState("");
  const eligible = applications.filter(
    (app) =>
      !app.archived &&
      app.environments?.some(
        (env) => !env.archived && (!scope.key || env.key === scope.key),
      ),
  );
  const environments =
    eligible
      .find((app) => app.id === appId)
      ?.environments?.filter(
        (env) => !env.archived && (!scope.key || env.key === scope.key),
      ) ?? [];
  const create = useMutation({
    mutationFn: () =>
      post<Resource>("/resources", {
        name: name.trim(),
        kind,
        description: description.trim(),
        external_id: externalId.trim(),
        applications: [{ id: appId, environment_id: envId }],
      }),
    onSuccess: (resource) => {
      void qc.invalidateQueries({ queryKey: ["resources"] });
      void qc.invalidateQueries({ queryKey: ["resource-relations"] });
      onCreated(resource.id);
    },
  });
  return (
    <ResourceOverlay
      title="添加资源"
      subtitle="保存资源资料与应用归属"
      onClose={onClose}
    >
      <form
        className="resource-create-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (name.trim() && appId && envId) create.mutate();
        }}
      >
        <label>
          资源名称
          <input
            value={name}
            onChange={(event) => setName(event.target.value)}
            required
            maxLength={200}
            placeholder="例如 redis-checkout"
          />
        </label>
        <label>
          资源类型
          <SelectField
            aria-label="资源类型"
            value={kind}
            onChange={(event) => setKind(event.target.value)}
          >
            {Object.entries(resourceKinds).map(([value, label]) => (
              <option value={value} key={value}>
                {label}
              </option>
            ))}
          </SelectField>
        </label>
        <label>
          关联应用
          <SelectField
            aria-label="关联应用"
            value={appId}
            onChange={(event) => {
              const id = event.target.value;
              setAppId(id);
              const envs =
                eligible
                  .find((app) => app.id === id)
                  ?.environments?.filter(
                    (env) =>
                      !env.archived && (!scope.key || env.key === scope.key),
                  ) ?? [];
              setEnvId(envs.length === 1 ? envs[0].id : "");
            }}
            required
          >
            <option value="">选择应用</option>
            {eligible.map((app) => (
              <option key={app.id} value={app.id}>
                {app.key}
              </option>
            ))}
          </SelectField>
        </label>
        <label>
          资源归属环境
          <SelectField
            aria-label="资源归属环境"
            value={envId}
            onChange={(event) => setEnvId(event.target.value)}
            required
            disabled={!appId}
          >
            <option value="">选择归属环境</option>
            {environments.map((env) => (
              <option value={env.id} key={env.id}>
                {env.name}
              </option>
            ))}
          </SelectField>
        </label>
        <label>
          外部标识
          <input
            value={externalId}
            onChange={(event) => setExternalId(event.target.value)}
            maxLength={500}
            placeholder="资源在来源系统中的 ID（可选）"
          />
        </label>
        <label>
          描述
          <textarea
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            maxLength={2000}
            rows={4}
            placeholder="资源的用途与维护说明"
          />
        </label>
        <p className="resource-muted">
          此操作只保存资源资料，不会创建基础设施或更改运行状态。
        </p>
        {create.isError && (
          <p className="resource-error" role="alert">
            {create.error.message}
          </p>
        )}
        <footer>
          <button className="btn" type="button" onClick={onClose}>
            取消
          </button>
          <button
            className="btn primary"
            disabled={create.isPending || !name.trim() || !appId || !envId}
          >
            {create.isPending ? "保存中…" : "保存资源"}
          </button>
        </footer>
      </form>
    </ResourceOverlay>
  );
}
