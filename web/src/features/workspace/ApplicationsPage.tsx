import { memberLabel } from "../../lib/members";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Box, Plus, Settings, ArrowRight, Archive, Layers } from "lucide-react";
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
      <input
        className="field"
        aria-label="搜索应用"
        placeholder="搜索名称、标签或说明…"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      <div className="management-grid management-section">
        {applications
          .filter((a) =>
            `${a.name} ${a.description} ${a.tags?.join(" ")}`
              .toLowerCase()
              .includes(filter.toLowerCase()),
          )
          .map((a) => (
            <article className="card management-card" key={a.id}>
              <h2>
                <Box size={19} />
                {a.name}
                {a.archived && <span className="badge">已归档</span>}
              </h2>
              <p>{a.description || "尚未填写应用说明"}</p>
              <div className="management-meta">
                <span className="badge">
                  <Layers size={12} />{" "}
                  {a.environments?.filter((e) => !e.archived).length ?? 0}{" "}
                  个环境
                </span>
                {a.tags?.map((t) => (
                  <span className="badge" key={t}>
                    {t}
                  </span>
                ))}
              </div>
              <small className="muted">
                维护人：
                {memberLabel(
                  a.maintainer_id,
                  user,
                  user.role === "admin" ? (users.data?.items ?? []) : [],
                )}
              </small>
              <div className="management-actions management-section">
                <button
                  className="btn btn-primary"
                  onClick={() => {
                    setApplication(a.id);
                    setEnvironment("");
                    navigate("/applications/detail");
                  }}
                >
                  打开应用 <ArrowRight size={14} />
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
            </article>
          ))}
      </div>
      {!applications.length && (
        <p className="management-empty">
          还没有可访问的应用。管理员可以登记应用并授予访问权限。
        </p>
      )}
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
                {(teams.data?.items ?? []).map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              维护人
              <select
                className="field"
                name="maintainer_id"
                defaultValue={editing?.maintainer_id}
              >
                <option value="">待指定</option>
                {(users.data?.items ?? []).map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
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
                      编辑绑定
                    </button>
                  )}
                  <button className="btn" onClick={() => setEnv(e)}>
                    自动化
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
