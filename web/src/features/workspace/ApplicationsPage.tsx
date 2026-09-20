import { memberLabel } from "../../lib/members";
import {
  ProfileFields,
  ConfigurationFields,
  readProfile,
  readConfiguration,
  ApplicationDetails,
} from "./ApplicationProfile";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Box, Plus, Settings, ArrowRight, Archive, Layers } from "../../lib/icons";
import { request, post, patch, put } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type {
  Application,
  Environment,
  Team,
  User,
  Grant,
} from "../../lib/types";
import { Modal, ErrorNotice } from "../../components/ui";
import AutomationPanel from "./AutomationPanel";
import "./workspace.css";
export default function ApplicationsPage() {
  const {
      preview,
      user,
      applications,
      setApplication,
      setEnvironment,
      notify,
    } = useWorkspace(),
    qc = useQueryClient(),
    navigate = useNavigate();
  const [editing, setEditing] = useState<Application | null | undefined>(),
    [managing, setManaging] = useState<Application | null>(null),
    [filter, setFilter] = useState(""),
    [teamFilter, setTeamFilter] = useState(""),
    [ownerFilter, setOwnerFilter] = useState(""),
    [environmentFilter, setEnvironmentFilter] = useState(""),
    [languageFilter, setLanguageFilter] = useState(""),
    [archiveFilter, setArchiveFilter] = useState("active"),
    [sort, setSort] = useState("name"),
    [busy, setBusy] = useState(false);
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
  const filtered = applications
    .filter(
      (a) =>
        `${a.name} ${a.key ?? ""} ${a.description} ${a.maintainer_name ?? ""} ${a.tags?.join(" ") ?? ""}`
          .toLowerCase()
          .includes(filter.trim().toLowerCase()) &&
        (!teamFilter || a.owner_team_id === teamFilter) &&
        (!ownerFilter || a.maintainer_id === ownerFilter) &&
        (!environmentFilter ||
          a.environments?.some(
            (e) => e.key === environmentFilter && !e.archived,
          )) &&
        (!languageFilter || a.profile?.language === languageFilter) &&
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
  return (
    <div className="management-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">APPLICATION CATALOG</span>
          <h1>应用</h1>
          <p>按应用组织环境、负责人、信号与调查权限。</p>
        </div>
        {user.role === "admin" && (
          <button className="btn btn-primary" onClick={() => setEditing(null)}>
            <Plus size={16} />
            登记应用
          </button>
        )}
      </header>
      <div className="catalog-toolbar">
        <input
          className="field catalog-search"
          aria-label="搜索应用"
          placeholder="搜索应用名称、标识、负责人或标签…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        <select
          className="field"
          aria-label="团队筛选"
          value={teamFilter}
          onChange={(e) => setTeamFilter(e.target.value)}
        >
          <option value="">全部团队</option>
          {Array.from(
            new Map(
              applications
                .filter((a) => a.owner_team_id)
                .map(
                  (a) =>
                    [
                      a.owner_team_id!,
                      a.owner_team_name || "团队不可用",
                    ] as const,
                ),
            ),
          ).map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </select>
        <select
          className="field"
          aria-label="负责人筛选"
          value={ownerFilter}
          onChange={(e) => setOwnerFilter(e.target.value)}
        >
          <option value="">全部负责人</option>
          {Array.from(
            new Map(
              applications
                .filter((a) => a.maintainer_id)
                .map(
                  (a) =>
                    [
                      a.maintainer_id!,
                      a.maintainer_name || "成员不可用",
                    ] as const,
                ),
            ),
          ).map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </select>
        <select
          className="field"
          aria-label="环境筛选"
          value={environmentFilter}
          onChange={(e) => setEnvironmentFilter(e.target.value)}
        >
          <option value="">全部环境</option>
          {Array.from(
            new Set(
              applications.flatMap(
                (a) =>
                  a.environments
                    ?.filter((e) => !e.archived)
                    .map((e) => e.key) ?? [],
              ),
            ),
          )
            .sort()
            .map((key) => (
              <option key={key}>{key}</option>
            ))}
        </select>
        <select
          className="field"
          aria-label="开发语言筛选"
          value={languageFilter}
          onChange={(e) => setLanguageFilter(e.target.value)}
        >
          <option value="">全部语言</option>
          {Array.from(
            new Set(
              applications
                .map((a) => a.profile?.language)
                .filter((v): v is string => !!v),
            ),
          )
            .sort()
            .map((value) => (
              <option key={value}>{value}</option>
            ))}
        </select>
        <select
          className="field"
          aria-label="归档筛选"
          value={archiveFilter}
          onChange={(e) => setArchiveFilter(e.target.value)}
        >
          <option value="active">使用中</option>
          <option value="archived">已归档</option>
          <option value="all">全部应用</option>
        </select>
        <select
          className="field"
          aria-label="应用排序"
          value={sort}
          onChange={(e) => setSort(e.target.value)}
        >
          <option value="name">按名称排序</option>
          <option value="key">按标识排序</option>
          <option value="owner">按负责人排序</option>
        </select>
        <button
          className="btn"
          onClick={() => {
            setFilter("");
            setTeamFilter("");
            setOwnerFilter("");
            setEnvironmentFilter("");
            setLanguageFilter("");
            setArchiveFilter("active");
            setSort("name");
          }}
        >
          重置
        </button>
      </div>
      <div className="catalog-table-wrap card management-section">
        <table className="catalog-table">
          <thead>
            <tr>
              <th>应用</th>
              <th>负责人 / 团队</th>
              <th>开发信息</th>
              <th>环境</th>
              <th>生命周期</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((a) => (
              <tr key={a.id}>
                <td>
                  <strong className="catalog-app-name">
                    <Box size={17} />
                    {a.name}
                  </strong>
                  <small>{a.key || a.id}</small>
                  <p>{a.description || "尚未填写应用说明"}</p>
                  <div className="management-meta">
                    {a.tags?.map((t) => (
                      <span className="badge" key={t}>
                        {t}
                      </span>
                    ))}
                  </div>
                </td>
                <td>
                  {a.maintainer_name ||
                    memberLabel(a.maintainer_id, user, users.data?.items ?? [])}
                  <small>{a.owner_team_name || "团队待指定"}</small>
                </td>
                <td>
                  {a.profile?.language || "语言未登记"}
                  <small>{a.profile?.framework || "框架未登记"}</small>
                </td>
                <td>
                  <div className="management-meta">
                    {a.environments
                      ?.filter((e) => !e.archived)
                      .map((e) => (
                        <span className="badge" key={e.id}>
                          <Layers size={12} />
                          {e.name}
                        </span>
                      ))}
                  </div>
                  {!a.environments?.some((e) => !e.archived) && (
                    <span className="muted">暂无环境</span>
                  )}
                </td>
                <td>
                  <span className="badge">
                    {a.archived ? "已归档" : a.profile?.lifecycle || "未登记"}
                  </span>
                  <small>{a.profile?.criticality || "等级未登记"}</small>
                </td>
                <td>
                  <div className="catalog-row-actions">
                    <button
                      className="btn btn-primary"
                      onClick={() => {
                        setApplication(a.id);
                        setEnvironment("");
                        navigate("/applications/detail");
                      }}
                    >
                      详情
                      <ArrowRight size={14} />
                    </button>
                    <button className="btn" onClick={() => setManaging(a)}>
                      <Settings size={14} />
                      环境与权限
                    </button>
                    {user.role === "admin" && (
                      <button className="btn" onClick={() => setEditing(a)}>
                        编辑
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!filtered.length && (
          <p className="management-empty">
            {applications.length
              ? "没有匹配的应用，请调整筛选条件。"
              : "还没有可访问的应用。管理员可以登记应用并授予访问权限。"}
          </p>
        )}
        <footer className="catalog-table-footer">
          显示 {filtered.length} / {applications.length} 个可访问应用 ·
          目录信息不代表实时健康状态
        </footer>
      </div>
      <Modal
        open={editing !== undefined}
        onOpenChange={(v) => !v && setEditing(undefined)}
        title={editing ? "编辑应用" : "登记应用"}
      >
        <form className="management-form" onSubmit={save}>
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
              <select
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
              </select>
            </label>
            <label>
              主负责人
              <select
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
              </select>
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
            <button className="btn btn-primary" disabled={busy}>
              {busy ? "保存中…" : "保存应用"}
            </button>
          </footer>
        </form>
      </Modal>
      {managing && (
        <Modal
          open
          onOpenChange={(v) => !v && setManaging(null)}
          title={`${managing.name} · 环境与权限`}
        >
          <ApplicationManagement
            application={
              applications.find((a) => a.id === managing.id) ?? managing
            }
            teams={teams.data?.items ?? []}
            users={users.data?.items ?? []}
            onClose={() => setManaging(null)}
          />
        </Modal>
      )}
    </div>
  );
}
function ApplicationManagement({
  application,
  teams,
  users,
  onClose,
}: {
  application: Application;
  teams: Team[];
  users: User[];
  onClose: () => void;
}) {
  const { user, preview, notify } = useWorkspace(),
    qc = useQueryClient();
  const [tab, setTab] = useState("environments"),
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
      <nav className="management-tabs" role="tablist">
        {[
          ["environments", "环境"],
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
              <select name="subject" className="field" required>
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
              </select>
            </label>
            <label>
              权限
              <select name="permission" className="field">
                <option value="view">只读</option>
                <option value="operate">调查与操作</option>
              </select>
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
            <select
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
            </select>
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
