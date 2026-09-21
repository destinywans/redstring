import SelectField from "../../components/SelectField";
import { memberLabel } from "../../lib/members";
import {
  ProfileFields,
  ConfigurationFields,
  readProfile,
  readConfiguration,
  ApplicationDetails,
} from "./ApplicationProfile";
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Box, Plus, ArrowRight, Archive, Layers, X } from "../../lib/icons";
import { request, post, patch, put } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type {
  Application,
  Environment,
  Team,
  User,
  Grant,
} from "../../lib/types";
import { ErrorNotice } from "../../components/ui";
import AutomationPanel from "./AutomationPanel";
import ApplicationDrawer from "./ApplicationDrawer";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { useEnvironmentScope } from "../../lib/environmentScope";
import "./workspace.css";
import "./applications.css";
export default function ApplicationsPage() {
  const { preview, user, applications, notify } = useWorkspace();
  const qc = useQueryClient();
  const environmentScope = useEnvironmentScope();
  const [params, setParams] = useSearchParams();
  const query = params.get("q") ?? "";
  const teamFilter = params.get("team") ?? "";
  const ownerFilter = params.get("owner") ?? "";
  const archiveFilter = params.get("archived") ?? "all";
  const sort = params.get("sort") ?? "key";
  const [editing, setEditing] = useState<Application | null | undefined>();
  const [managing, setManaging] = useState<Application | null>(null);
  const [filter, setFilter] = useState(query);
  const [busy, setBusy] = useState(false);
  useEffect(() => setFilter(query), [query]);
  useEffect(() => {
    if (params.get("view") !== "mine") return;
    const next = new URLSearchParams(params);
    next.delete("view");
    setParams(next, { replace: true });
  }, [params, setParams]);
  function updateFilter(key: string, value: string, replace = false) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace });
  }
  function resetFilters() {
    const next = new URLSearchParams(params);
    for (const key of ["q", "team", "owner", "archived", "sort"])
      next.delete(key);
    setFilter("");
    setParams(next);
  }
  // A global scope with no matching applications must remain empty, never widen.
  const scopedApplications = applications.filter(
    (app) =>
      !environmentScope.key ||
      app.environments?.some(
        (env) =>
          !env.archived && (env.key || `id:${env.id}`) === environmentScope.key,
      ),
  );
  const teamOptions = Array.from(
    new Map(
      scopedApplications
        .filter((a) => a.owner_team_id)
        .map((a) => [a.owner_team_id!, a.owner_team_name || "团队不可用"]),
    ),
  ).map(([value, label]) => ({ value, label }));
  const ownerOptions = Array.from(
    new Map(
      scopedApplications
        .filter((a) => a.maintainer_id)
        .map((a) => [
          a.maintainer_id!,
          a.maintainer_name ||
            (a.maintainer_id === user.id ? user.name : "成员不可用"),
        ]),
    ),
  ).map(([value, label]) => ({ value, label }));
  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => request<{ items: Team[] }>("/teams"),
    enabled: !preview && user.role === "admin",
  });
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => request<{ items: User[] }>("/users"),
    enabled: !preview && user.role === "admin",
  });
  async function save(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (preview) {
      notify("设计预览不保存真实应用");
      return;
    }
    if (busy) return;
    const f = new FormData(e.currentTarget),
      body = {
        profile: readProfile(f),
        name: f.get("name"),
        key: f.get("key"),
        description: f.get("description"),
        owner_team_id: f.get("owner_team_id"),
        maintainer_id: f.get("maintainer_id"),
        repository_url: f.get("repository_url"),
        runbook_url: f.get("runbook_url"),
        tags: String(f.get("tags"))
          .split(/[,，]/)
          .map((s) => s.trim())
          .filter(Boolean),
      };
    setBusy(true);
    try {
      if (editing) await patch(`/applications/${editing.id}`, body);
      else await post("/applications", body);
      await qc.invalidateQueries({ queryKey: ["applications"] });
      setEditing(undefined);
      notify("应用信息已保存", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  const filtered = scopedApplications
    .filter(
      (a) =>
        `${a.name} ${a.key ?? ""} ${a.description} ${a.maintainer_name ?? ""} ${a.tags?.join(" ") ?? ""}`
          .toLowerCase()
          .includes(query.trim().toLowerCase()) &&
        (!teamFilter || a.owner_team_id === teamFilter) &&
        (!ownerFilter || a.maintainer_id === ownerFilter) &&
        (archiveFilter === "all" ||
          (archiveFilter === "archived" ? a.archived : !a.archived)),
    )
    .sort((a, b) =>
      sort === "owner"
        ? (a.maintainer_name ?? "").localeCompare(b.maintainer_name ?? "", "zh")
        : sort === "key"
          ? (a.key ?? "").localeCompare(b.key ?? "")
          : a.name.localeCompare(b.name, "zh"),
    );
  const activeFilters = [
    ...(query ? [{ key: "q", label: `关键词：${query}` }] : []),
    ...(teamFilter
      ? [
          {
            key: "team",
            label: `团队：${teamOptions.find((o) => o.value === teamFilter)?.label ?? "当前团队不可用"}`,
          },
        ]
      : []),
    ...(ownerFilter
      ? [
          {
            key: "owner",
            label: `负责人：${ownerOptions.find((o) => o.value === ownerFilter)?.label ?? "当前成员不可用"}`,
          },
        ]
      : []),
    ...(archiveFilter !== "all"
      ? [
          {
            key: "archived",
            label: archiveFilter === "archived" ? "已归档" : "使用中",
          },
        ]
      : []),
    ...(sort !== "key"
      ? [
          {
            key: "sort",
            label: sort === "owner" ? "按负责人排序" : "按名称排序",
          },
        ]
      : []),
  ];
  return (
    <div className="management-page applications-page">
      <header className="management-heading">
        <div>
          <h1>全部应用</h1>
          <p>应用资料、责任归属与关联资源。</p>
        </div>
        {user.role === "admin" && (
          <button
            className="btn btn-primary"
            onClick={() => {
              setManaging(null);
              setEditing(null);
            }}
          >
            <Plus size={16} />
            新建应用
          </button>
        )}
      </header>
      <FilterBar label="应用搜索与筛选">
        <FilterSearch
          value={filter}
          onChange={(value) => {
            setFilter(value);
            updateFilter("q", value, true);
          }}
          onSubmit={() => updateFilter("q", filter.trim())}
          showSubmit={false}
          label="搜索应用"
          placeholder="搜索应用名称、标识、负责人或标签"
        />
        <FilterSelect
          id="application-team"
          label="团队"
          showLabel={false}
          options={teamOptions}
          values={teamFilter ? [teamFilter] : []}
          onChange={(v) => updateFilter("team", v[0] ?? "")}
          searchable
        />
        <FilterSelect
          id="application-owner"
          label="负责人"
          showLabel={false}
          options={ownerOptions}
          values={ownerFilter ? [ownerFilter] : []}
          onChange={(v) => updateFilter("owner", v[0] ?? "")}
          searchable
        />
        <FilterSelect
          id="application-archive"
          label="状态"
          showLabel={false}
          options={[
            { value: "active", label: "使用中" },
            { value: "archived", label: "已归档" },
          ]}
          values={archiveFilter === "all" ? [] : [archiveFilter]}
          onChange={(v) => updateFilter("archived", v[0] ?? "")}
        />
        <FilterSelect
          id="application-sort"
          label="排序"
          showLabel={false}
          options={[
            { value: "name", label: "按名称" },
            { value: "owner", label: "按负责人" },
          ]}
          values={sort === "key" ? [] : [sort]}
          emptyLabel="按标识"
          onChange={(v) => updateFilter("sort", v[0] ?? "")}
        />
      </FilterBar>
      {activeFilters.length > 0 && (
        <div className="application-filter-summary">
          {activeFilters.map((f) => (
            <button
              key={f.key}
              className="application-filter-chip"
              onClick={() => updateFilter(f.key, "")}
              aria-label={`移除${f.label}`}
            >
              {f.label}
              <X size={12} />
            </button>
          ))}
          {activeFilters.length > 0 && (
            <button className="btn btn-ghost" onClick={resetFilters}>
              重置筛选
            </button>
          )}
        </div>
      )}
      <div className="catalog-table-wrap card">
        <table className="catalog-table application-list-table">
          <thead>
            <tr>
              <th>应用</th>
              <th>负责人</th>
              <th>部门 / 团队</th>
              <th>版本</th>
              <th>最近更新时间</th>
              <th aria-label="操作" />
            </tr>
          </thead>
          <tbody>
            {filtered.map((a) => (
              <tr key={a.id}>
                <td>
                  <button
                    className="application-name-link"
                    onClick={() => setManaging(a)}
                  >
                    <span className="application-symbol">
                      <Box size={21} />
                    </span>
                    <span>
                      <strong>{a.key || a.id}</strong>
                      {a.archived && (
                        <span className="application-archived">已归档</span>
                      )}
                    </span>
                  </button>
                </td>
                <td>
                  {a.maintainer_name ||
                    memberLabel(a.maintainer_id, user, users.data?.items ?? [])}
                </td>
                <td>{a.owner_team_name || "待指定"}</td>
                {/* The catalog does not yet expose release versions or profile
                    timestamps. Never substitute configuration revisions or now. */}
                <td className="muted" title="尚未接入应用版本">
                  —
                </td>
                <td className="muted" title="尚未接入应用资料更新时间">
                  —
                </td>
                <td>
                  <button
                    className="icon-button application-open"
                    aria-label={`查看 ${a.key || a.id}`}
                    onClick={() => setManaging(a)}
                  >
                    <ArrowRight size={17} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!filtered.length && (
          <div className="management-empty">
            <Box size={28} />
            <h2>没有匹配的应用</h2>
            <p>
              {activeFilters.length
                ? "调整筛选条件，或重置后查看当前范围内的应用。"
                : environmentScope.key
                  ? "当前环境下暂无符合条件的应用，可从右上角切换查看范围。"
                  : "管理员可以新建应用并指定负责人。"}
            </p>
            {activeFilters.length > 0 && (
              <button className="btn" onClick={resetFilters}>
                重置筛选
              </button>
            )}
          </div>
        )}
        <footer className="catalog-table-footer" aria-live="polite">
          显示 {filtered.length} / {scopedApplications.length} 个可访问应用
        </footer>
      </div>
      <ApplicationDrawer
        open={editing !== undefined}
        onOpenChange={(v) => !v && !busy && setEditing(undefined)}
        title={editing ? `编辑 ${editing.name}` : "新建应用"}
      >
        <form
          key={editing?.id ?? "new"}
          className="management-form"
          onSubmit={save}
        >
          <div className="management-form-grid">
            <label>
              应用名称
              <input
                className="field"
                name="name"
                defaultValue={editing?.name}
                required
                maxLength={120}
              />
            </label>
            <label>
              稳定标识
              <input
                className="field"
                name="key"
                defaultValue={editing?.key}
                required
                pattern="[a-zA-Z0-9_-]+"
                maxLength={120}
                placeholder="checkout-api"
              />
            </label>
          </div>
          <label>
            说明
            <textarea
              className="field"
              name="description"
              defaultValue={editing?.description}
              rows={3}
              maxLength={8000}
            />
          </label>
          <div className="management-form-grid">
            <label>
              所属团队
              <SelectField
                className="field"
                name="owner_team_id"
                defaultValue={editing?.owner_team_id}
              >
                <option value="">待指定</option>
                {editing?.owner_team_id &&
                  !teams.data?.items.some(
                    (t) => t.id === editing.owner_team_id,
                  ) && (
                    <option value={editing.owner_team_id}>
                      {editing.owner_team_name || "原所属团队"}
                    </option>
                  )}
                {(teams.data?.items ?? []).map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </SelectField>
            </label>
            <label>
              主负责人
              <SelectField
                className="field"
                name="maintainer_id"
                defaultValue={editing?.maintainer_id}
              >
                <option value="">待指定</option>
                {editing?.maintainer_id &&
                  !users.data?.items.some(
                    (t) => t.id === editing.maintainer_id,
                  ) && (
                    <option value={editing.maintainer_id}>
                      {editing.maintainer_name || "原负责人"}
                    </option>
                  )}
                {(users.data?.items ?? []).map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                    {t.disabled ? "（已停用）" : ""}
                  </option>
                ))}
              </SelectField>
            </label>
          </div>
          <label>
            标签（逗号分隔）
            <input
              className="field"
              name="tags"
              defaultValue={editing?.tags?.join(", ")}
            />
          </label>
          <label>
            代码仓库链接
            <input
              className="field"
              name="repository_url"
              type="url"
              defaultValue={editing?.repository_url}
            />
          </label>
          <label>
            运维文档链接
            <input
              className="field"
              name="runbook_url"
              type="url"
              defaultValue={editing?.runbook_url}
            />
          </label>
          <ProfileFields profile={editing?.profile} />
          <footer>
            <button
              type="button"
              className="btn"
              disabled={busy}
              onClick={() => setEditing(undefined)}
            >
              取消
            </button>
            <button className="btn btn-primary" disabled={busy}>
              {busy ? "保存中…" : "保存应用"}
            </button>
          </footer>
        </form>
      </ApplicationDrawer>
      {managing && (
        <ApplicationDrawer
          open
          onOpenChange={(v) => !v && setManaging(null)}
          title={managing.name}
        >
          <ApplicationManagement
            application={
              applications.find((a) => a.id === managing.id) ?? managing
            }
            teams={teams.data?.items ?? []}
            users={users.data?.items ?? []}
            onClose={() => setManaging(null)}
            onEdit={() => {
              setEditing(
                applications.find((a) => a.id === managing.id) ?? managing,
              );
              setManaging(null);
            }}
          />
        </ApplicationDrawer>
      )}
    </div>
  );
}
function ApplicationManagement({
  application,
  teams,
  users,
  onClose,
  onEdit,
}: {
  application: Application;
  teams: Team[];
  users: User[];
  onClose: () => void;
  onEdit: () => void;
}) {
  const { user, preview, notify } = useWorkspace(),
    qc = useQueryClient();
  const [tab, setTab] = useState("overview"),
    [env, setEnv] = useState<Environment | null>(null),
    [editEnv, setEditEnv] = useState<Environment | null | undefined>(),
    [busy, setBusy] = useState(false);
  const grants = useQuery({
    queryKey: ["grants", application.id],
    queryFn: () =>
      request<{ items: Grant[] }>(`/applications/${application.id}/grants`),
    enabled: !preview && user.role === "admin" && tab === "permissions",
  });
  async function execute(action: () => Promise<unknown>) {
    if (preview) {
      notify("设计预览不修改配置");
      return;
    }
    setBusy(true);
    try {
      await action();
      await qc.invalidateQueries({ queryKey: ["applications"] });
      await qc.invalidateQueries({ queryKey: ["grants", application.id] });
      notify("配置已保存", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <nav className="management-tabs" role="tablist" aria-label="应用详情分类">
        {[
          ["overview", "应用详情"],
          ["environments", "环境与配置"],
          ...(user.role === "admin"
            ? [
                ["permissions", "应用权限"],
                ["archive", "归档"],
              ]
            : []),
        ].map(([id, name]) => (
          <button
            role="tab"
            aria-selected={tab === id}
            tabIndex={tab === id ? 0 : -1}
            onKeyDown={(event) => {
              if (
                !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)
              )
                return;
              event.preventDefault();
              const tabs = Array.from(
                event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>(
                  '[role="tab"]',
                ) ?? [],
              );
              const index = tabs.indexOf(event.currentTarget);
              const next =
                event.key === "Home"
                  ? 0
                  : event.key === "End"
                    ? tabs.length - 1
                    : (index +
                        (event.key === "ArrowRight" ? 1 : -1) +
                        tabs.length) %
                      tabs.length;
              tabs[next]?.focus();
              tabs[next]?.click();
            }}
            key={id}
            onClick={() => {
              setTab(id);
              setEnv(null);
            }}
          >
            {name}
          </button>
        ))}
      </nav>
      {tab === "overview" && (
        <>
          <div className="application-detail-summary">
            <p>{application.description || "暂无应用说明"}</p>
            <div className="management-meta">
              {application.tags?.map((tag) => (
                <span key={tag} className="badge">
                  {tag}
                </span>
              ))}
            </div>
            {user.role === "admin" && (
              <button className="btn" onClick={onEdit}>
                编辑应用
              </button>
            )}
          </div>
          <ApplicationDetails application={application} hideEnvironment />
        </>
      )}
      {tab === "environments" && (
        <>
          {env ? (
            <>
              <button className="btn" onClick={() => setEnv(null)}>
                ← 返回环境列表
              </button>
              <ApplicationDetails
                application={application}
                environment={
                  application.environments?.find((e) => e.id === env.id) ?? env
                }
              />
              <h3 className="management-section">{env.name} · 自动化</h3>
              <AutomationPanel environmentId={env.id} />
            </>
          ) : (
            <>
              {application.environments?.map((e) => (
                <div className="management-row" key={e.id}>
                  <Layers size={17} />
                  <div className="grow">
                    <strong>{e.name}</strong>
                    <small>
                      {e.key} ·{" "}
                      {e.archived
                        ? "已归档"
                        : `${e.bindings?.length ?? 0} 个绑定`}
                    </small>
                  </div>
                  {user.role === "admin" && (
                    <button className="btn" onClick={() => setEditEnv(e)}>
                      配置与绑定
                    </button>
                  )}
                  <button className="btn" onClick={() => setEnv(e)}>
                    详情与自动化
                  </button>
                </div>
              ))}
              {!application.environments?.length && (
                <p className="management-empty">先建立环境，再绑定数据源。</p>
              )}
              {user.role === "admin" && (
                <button className="btn" onClick={() => setEditEnv(null)}>
                  <Plus size={15} />
                  添加环境
                </button>
              )}
            </>
          )}
          {editEnv !== undefined && (
            <EnvironmentForm
              key={editEnv?.id ?? "new"}
              appId={application.id}
              environment={editEnv}
              onSave={() => {
                setEditEnv(undefined);
                void qc.invalidateQueries({ queryKey: ["applications"] });
              }}
              onCancel={() => setEditEnv(undefined)}
            />
          )}
        </>
      )}
      {tab === "permissions" && (
        <>
          {grants.error && <ErrorNotice error={grants.error} />}
          <p className="management-notice">
            团队归属不会自动授予访问权限。操作权限受成员的工作区角色限制。
          </p>
          {(grants.data?.items ?? []).map((g, i) => (
            <div
              className="management-row"
              key={`${g.subject_type}-${g.subject_id}`}
            >
              <div className="grow">
                <strong>
                  {(g.subject_type === "team" ? teams : users).find(
                    (x) => x.id === g.subject_id,
                  )?.name ?? g.subject_id}
                </strong>
                <small>
                  {g.subject_type === "team" ? "团队" : "成员"} ·{" "}
                  {g.permission === "operate" ? "可操作" : "只读"}
                </small>
              </div>
              <button
                className="btn"
                disabled={busy}
                onClick={() =>
                  void execute(() =>
                    put(`/applications/${application.id}/grants`, {
                      items: grants.data!.items.filter((_, j) => j !== i),
                    }),
                  )
                }
              >
                移除
              </button>
            </div>
          ))}
          <form
            className="management-form"
            onSubmit={(e) => {
              e.preventDefault();
              const f = new FormData(e.currentTarget),
                [subject_type, subject_id] = String(f.get("subject")).split(
                  ":",
                );
              void execute(() =>
                put(`/applications/${application.id}/grants`, {
                  items: [
                    ...(grants.data?.items ?? []).filter(
                      (g) =>
                        g.subject_type !== subject_type ||
                        g.subject_id !== subject_id,
                    ),
                    {
                      subject_type,
                      subject_id,
                      permission: f.get("permission"),
                    },
                  ],
                }),
              );
            }}
          >
            <label>
              授权对象
              <SelectField name="subject" className="field" required>
                <option value="">选择团队或成员</option>
                {teams.map((t) => (
                  <option key={t.id} value={`team:${t.id}`}>
                    团队 · {t.name}
                  </option>
                ))}
                {users.map((t) => (
                  <option key={t.id} value={`user:${t.id}`}>
                    成员 · {t.name}
                  </option>
                ))}
              </SelectField>
            </label>
            <label>
              权限
              <SelectField name="permission" className="field">
                <option value="view">只读</option>
                <option value="operate">调查与操作</option>
              </SelectField>
            </label>
            <button className="btn btn-primary" disabled={busy || !grants.data}>
              保存授权
            </button>
          </form>
        </>
      )}
      {tab === "archive" && (
        <>
          <p>归档后保留调查与历史记录，可在之后恢复。</p>
          <button
            className="btn management-section"
            disabled={busy}
            onClick={() =>
              void execute(async () => {
                await post(`/applications/${application.id}/archive`, {
                  archived: !application.archived,
                });
                onClose();
              })
            }
          >
            <Archive size={16} />
            {application.archived ? "恢复应用" : "归档应用"}
          </button>
        </>
      )}
    </>
  );
}
function EnvironmentForm({
  appId,
  environment,
  onSave,
  onCancel,
}: {
  appId: string;
  environment: Environment | null;
  onSave: () => void;
  onCancel: () => void;
}) {
  const { preview, notify } = useWorkspace();
  const [bindings, setBindings] = useState(environment?.bindings ?? []),
    [busy, setBusy] = useState(false);
  const registrations = useQuery({
    queryKey: ["connector-registrations"],
    queryFn: () =>
      request<{
        items: { id: string; kind: string; name?: string; resource?: string }[];
      }>("/connector-registrations"),
    enabled: !preview,
  });
  return (
    <form
      className="management-form"
      onSubmit={async (e) => {
        e.preventDefault();
        if (preview) return;
        const f = new FormData(e.currentTarget);
        setBusy(true);
        try {
          const body = {
            application_id: appId,
            key: f.get("key"),
            name: f.get("name"),
            archived: f.get("archived") === "on",
            binding_revision: environment?.binding_revision ?? 0,
            configuration_revision: environment?.configuration_revision ?? 0,
            configuration: readConfiguration(f),
            bindings,
          };
          if (environment) await patch(`/environments/${environment.id}`, body);
          else await post(`/applications/${appId}/environments`, body);
          onSave();
        } catch (e) {
          notify(String(e), "error");
        } finally {
          setBusy(false);
        }
      }}
    >
      <h3>{environment ? "编辑环境" : "添加环境"}</h3>
      <div className="management-form-grid">
        <label>
          环境标识
          <input
            className="field"
            name="key"
            defaultValue={environment?.key}
            required
            placeholder="production"
            pattern="[a-zA-Z0-9_-]+"
            maxLength={120}
          />
        </label>
        <label>
          环境名称
          <input
            className="field"
            name="name"
            defaultValue={environment?.name}
            required
            placeholder="生产"
            maxLength={120}
          />
        </label>
      </div>
      {environment && (
        <label className="checkbox-label">
          <input
            type="checkbox"
            name="archived"
            defaultChecked={environment.archived}
          />
          归档此环境
        </label>
      )}
      <ConfigurationFields configuration={environment?.configuration} />
      <h3>连接器绑定</h3>
      <p className="muted">
        选择服务器登记的连接器。资源范围由服务端校验；修改绑定可能使旧操作提案失效。
      </p>
      {registrations.error && <ErrorNotice error={registrations.error} />}{" "}
      {bindings.map((b, i) => (
        <div className="management-form-grid" key={i}>
          <label>
            连接器
            <SelectField
              className="field"
              required
              value={b.connector_id}
              onChange={(e) => {
                const r = (registrations.data?.items ?? []).find(
                  (r) => r.id === e.target.value,
                );
                setBindings(
                  bindings.map((v, j) =>
                    j === i
                      ? {
                          ...v,
                          connector_id: e.target.value,
                          kind: r?.kind ?? v.kind,
                          resource:
                            r?.resource ??
                            (r?.kind === "kubernetes"
                              ? "deployment"
                              : v.resource),
                        }
                      : v,
                  ),
                );
              }}
            >
              <option value="">选择连接器</option>
              {(registrations.data?.items ?? []).map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name ?? r.id} · {r.kind}
                </option>
              ))}
            </SelectField>
          </label>
          <label>
            资源标识
            <input
              className="field"
              value={b.resource}
              onChange={(e) =>
                setBindings(
                  bindings.map((v, j) =>
                    j === i ? { ...v, resource: e.target.value } : v,
                  ),
                )
              }
              placeholder="deployment 或登记的应用映射标识"
            />
          </label>
          <button
            type="button"
            className="btn"
            onClick={() => setBindings(bindings.filter((_, j) => j !== i))}
          >
            移除绑定
          </button>
        </div>
      ))}
      <button
        type="button"
        className="btn"
        onClick={() =>
          setBindings([
            ...bindings,
            { kind: "", connector_id: "", resource: "" },
          ])
        }
      >
        添加连接器绑定
      </button>
      <footer>
        <button type="button" className="btn" onClick={onCancel}>
          取消
        </button>
        <button className="btn btn-primary" disabled={busy}>
          保存环境
        </button>
      </footer>
    </form>
  );
}
