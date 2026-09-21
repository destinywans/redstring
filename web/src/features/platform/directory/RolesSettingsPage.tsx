import Pagination, { useListPagination } from "../../../components/Pagination";
import { useState, type FormEvent } from "react";
import {
  Box,
  Check,
  Minus,
  MoreHorizontal,
  Plus,
  Users,
  User,
} from "../../../lib/icons";
import { put, request } from "../../../lib/api";
import type { Grant } from "../../../lib/types";
import { useWorkspace } from "../../../lib/workspace";
import { ErrorNotice } from "../../../components/ui";
import SelectField from "../../../components/SelectField";
import { FilterBar, FilterSearch } from "../../../components/filters/FilterBar";
import { AdminOnlyNotice } from "../../members/GovernancePage";
import {
  Avatar,
  DirectoryDrawer,
  DirectoryFilter,
  DirectoryHeader,
  DirectoryNotice,
  DirectoryState,
  DrawerFooter,
  RoleBadge,
  useDirectory,
  useRefreshDirectory,
  type DirectoryRole,
} from "./shared";

type ApplicationGrant = Grant & { application_id: string };
const capabilities: {
  id: keyof DirectoryRole["permissions"];
  name: string;
  detail: string;
}[] = [
  { id: "view", name: "查看", detail: "资源、配置与日志" },
  { id: "investigate", name: "调查", detail: "问题排查与分析诊断" },
  { id: "approve", name: "审批", detail: "显式批准恢复操作" },
  { id: "execute", name: "执行", detail: "经批准的操作与任务" },
  { id: "manage", name: "管理", detail: "成员、配置与授权管理" },
];
export function RolesSettingsPage() {
  const { user } = useWorkspace();
  return user.role === "admin" ? <RolesDirectory /> : <AdminOnlyNotice />;
}
function RolesDirectory() {
  const { preview, notify } = useWorkspace();
  const directory = useDirectory(),
    refresh = useRefreshDirectory();
  const [search, setSearch] = useState(""),
    [permission, setPermission] = useState(""),
    [application, setApplication] = useState(""),
    [subjectType, setSubjectType] = useState("");
  const [editing, setEditing] = useState<ApplicationGrant | "new" | null>(null),
    [kind, setKind] = useState<"user" | "team">("user"),
    [selected, setSelected] = useState<ApplicationGrant | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null),
    [appId, setAppId] = useState(""),
    [subjectId, setSubjectId] = useState("");
  const data = directory.data,
    members = (data?.members ?? []).filter((m) => !m.deleted_at),
    teams = data?.teams ?? [],
    apps = data?.applications ?? [];
  const subjectName = (g: ApplicationGrant) =>
    (g.subject_type === "team" ? teams : members).find(
      (m) => m.id === g.subject_id,
    )?.name || "目录中已不存在的主体";
  const appName = (g: ApplicationGrant) =>
    apps.find((a) => a.id === g.application_id)?.name || g.application_id;
  const rows = (data?.grants ?? []).filter(
    (g) =>
      (!permission || g.permission === permission) &&
      (!application || g.application_id === application) &&
      (!subjectType || g.subject_type === subjectType) &&
      `${subjectName(g)} ${appName(g)}`
        .toLowerCase()
        .includes(search.trim().toLowerCase()),
  );
  const pagination = useListPagination(
    rows,
    [search, permission, application, subjectType].join("|"),
  );
  const close = () => {
    if (!busy) {
      setEditing(null);
      setSelected(null);
      setError(null);
    }
  };
  const openEdit = (g: ApplicationGrant | "new") => {
    setEditing(g);
    setSelected(null);
    setError(null);
    setKind(g === "new" ? "user" : g.subject_type);
    setAppId(g === "new" ? "" : g.application_id);
    setSubjectId(g === "new" ? "" : g.subject_id);
  };
  async function writeGrant(grant: ApplicationGrant, remove = false) {
    const endpoint = `/applications/${encodeURIComponent(grant.application_id)}/grants`;
    // Read the current application list before replacing it, not an old directory snapshot.
    const current = await request<{ items: Grant[] | null }>(endpoint);
    const other = (current.items ?? []).filter(
      (g) =>
        g.subject_type !== grant.subject_type ||
        g.subject_id !== grant.subject_id,
    );
    await put(endpoint, {
      items: remove
        ? other
        : [
            ...other,
            {
              subject_type: grant.subject_type,
              subject_id: grant.subject_id,
              permission: grant.permission,
            },
          ],
    });
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || preview || !appId || !subjectId) return;
    const f = new FormData(event.currentTarget);
    const next: ApplicationGrant = {
      application_id: appId,
      subject_type: kind,
      subject_id: subjectId,
      permission: f.get("permission") === "operate" ? "operate" : "view",
    };
    setBusy(true);
    setError(null);
    try {
      await writeGrant(next);
      await refresh();
      setEditing(null);
      notify("应用授权已保存", "success");
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  async function remove() {
    if (!selected || busy || preview) return;
    setBusy(true);
    setError(null);
    try {
      await writeGrant(selected, true);
      await refresh();
      setSelected(null);
      notify("应用授权已移除", "success");
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="directory-page">
      <DirectoryHeader
        title="角色与资源范围"
        description="管理平台角色的权限范围，以及成员或部门在具体应用中的资源授权。"
      />
      <DirectoryState
        loading={directory.isPending && !preview}
        error={directory.error}
        retry={directory.refetch}
      />
      <section className="directory-card">
        <div className="directory-section-heading">
          <div>
            <h2>角色权限矩阵</h2>
            <p>
              平台角色决定能力上限，应用授权决定访问范围。以下能力来自服务端角色定义。
            </p>
          </div>
        </div>
        <div className="directory-matrix">
          <div className="directory-table-wrap">
            <table className="directory-table">
              <thead>
                <tr>
                  <th>权限项</th>
                  {(data?.roles ?? []).map((r) => (
                    <th key={r.id}>{r.name}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {capabilities.map((c) => (
                  <tr key={c.id}>
                    <td>
                      {c.name}
                      <small>（{c.detail}）</small>
                    </td>
                    {(data?.roles ?? []).map((r) => (
                      <td
                        key={r.id}
                        title={r.scope}
                        aria-label={`${r.name}：${r.permissions[c.id] ? "允许，受应用范围与审批约束" : "不允许"}`}
                      >
                        {r.permissions[c.id] ? (
                          <Check size={19} weight="bold" />
                        ) : (
                          <Minus size={15} />
                        )}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="directory-source">
            操作员在可操作的应用内可显式审批；执行仍经过独立执行器。只读成员不会因可操作授权获得写权限。
          </p>
        </div>
      </section>
      <section className="directory-card">
        <div className="directory-section-heading">
          <div>
            <h2>应用资源授权</h2>
            <p>授权仅对所选应用生效，成员可同时继承所在部门的授权。</p>
          </div>
          <button
            className="btn btn-primary"
            disabled={!data || preview}
            onClick={() => openEdit("new")}
          >
            <Plus size={16} />
            添加授权
          </button>
        </div>
        <div className="directory-card-toolbar">
          <FilterBar label="应用资源授权筛选">
            <FilterSearch
              value={search}
              onChange={setSearch}
              onSubmit={() => setSearch(search.trim())}
              showSubmit={false}
              placeholder="搜索成员或部门、应用名称"
              label="搜索授权"
            />
            <DirectoryFilter
              label="授权权限"
              value={permission}
              onChange={setPermission}
              options={[
                { value: "view", label: "只读" },
                { value: "operate", label: "可操作" },
              ]}
            />
            <DirectoryFilter
              label="应用"
              value={application}
              onChange={setApplication}
              options={apps.map((a) => ({ value: a.id, label: a.name }))}
            />
            <DirectoryFilter
              label="主体类型"
              value={subjectType}
              onChange={setSubjectType}
              options={[
                { value: "user", label: "成员" },
                { value: "team", label: "部门" },
              ]}
            />
          </FilterBar>
        </div>
        <div className="directory-table-wrap">
          <table className="directory-table">
            <thead>
              <tr>
                <th>主体</th>
                <th>类型</th>
                <th>应用</th>
                <th>授权权限</th>
                <th>资源范围</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {pagination.items.map((g) => (
                <tr
                  key={`${g.application_id}-${g.subject_type}-${g.subject_id}`}
                >
                  <td>
                    <div className="directory-person">
                      {g.subject_type === "team" ? (
                        <span className="directory-avatar">
                          <Users size={18} />
                        </span>
                      ) : (
                        <Avatar name={subjectName(g)} />
                      )}
                      <button
                        className="directory-name"
                        onClick={() => {
                          setSelected(g);
                          setError(null);
                        }}
                      >
                        {subjectName(g)}
                      </button>
                    </div>
                  </td>
                  <td>{g.subject_type === "team" ? "部门" : "成员"}</td>
                  <td>
                    <div className="directory-application-item">
                      <Box size={18} />
                      <span>{appName(g)}</span>
                    </div>
                  </td>
                  <td>
                    <RoleBadge
                      role={g.permission}
                      label={g.permission === "operate" ? "可操作" : "只读"}
                    />
                  </td>
                  <td>应用内全部资源</td>
                  <td>
                    <button
                      className="icon-button"
                      aria-label={`管理 ${subjectName(g)} 对 ${appName(g)} 的授权`}
                      onClick={() => {
                        setSelected(g);
                        setError(null);
                      }}
                    >
                      <MoreHorizontal size={20} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!directory.isPending && !directory.error && (
          <DirectoryState
            loading={false}
            error={null}
            empty={!rows.length}
            retry={directory.refetch}
          />
        )}
        <Pagination
          total={directory.data ? pagination.total : undefined}
          totalLabel={
            directory.isPending
              ? "正在加载…"
              : directory.error
                ? "记录暂不可用"
                : undefined
          }
          page={pagination.page}
          pageSize={pagination.pageSize}
          loading={directory.isFetching}
          onPageSizeChange={pagination.setPageSize}
          onPageChange={pagination.setPage}
        />
      </section>
      {editing && (
        <DirectoryDrawer
          title={editing === "new" ? "添加授权" : "编辑授权"}
          onClose={close}
        >
          <form className="directory-form" onSubmit={save}>
            {!!error && <ErrorNotice error={error} />}
            <label>授权主体</label>
            <div className="directory-subject-tabs">
              <button
                type="button"
                disabled={editing !== "new"}
                aria-pressed={kind === "user"}
                onClick={() => {
                  setKind("user");
                  setSubjectId("");
                }}
              >
                <User size={16} />
                成员
              </button>
              <button
                type="button"
                disabled={editing !== "new"}
                aria-pressed={kind === "team"}
                onClick={() => {
                  setKind("team");
                  setSubjectId("");
                }}
              >
                <Users size={16} />
                部门
              </button>
            </div>
            <SelectField
              className="field"
              aria-label={kind === "user" ? "选择成员" : "选择部门"}
              required
              disabled={editing !== "new"}
              value={subjectId}
              onChange={(e) => setSubjectId(e.target.value)}
            >
              <option value="">
                {kind === "user" ? "搜索并选择成员" : "搜索并选择部门"}
              </option>
              {(kind === "user" ? members : teams).map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </SelectField>
            <label>
              应用
              <SelectField
                className="field"
                required
                value={appId}
                disabled={editing !== "new"}
                onChange={(e) => setAppId(e.target.value)}
              >
                <option value="">选择应用</option>
                {apps.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </SelectField>
            </label>
            <label>
              授权权限
              <SelectField
                className="field"
                name="permission"
                defaultValue={editing === "new" ? "view" : editing.permission}
              >
                <option value="view">只读 · 查看应用资源</option>
                <option value="operate">可操作 · 受平台角色上限约束</option>
              </SelectField>
            </label>
            <label>
              资源范围
              <SelectField
                className="field"
                name="scope"
                defaultValue="all"
                disabled
              >
                <option value="all">应用内全部资源</option>
              </SelectField>
            </label>
            <p className="directory-source">
              当前支持应用级授权，尚不支持指定服务、集群或命名空间的单独授权。
            </p>
            <DirectoryNotice>
              应用授权不会创建新的平台角色，也不会授予平台管理权。写操作仍需人工批准，并重新核对执行目标。
            </DirectoryNotice>
            <DrawerFooter
              busy={busy}
              onClose={close}
              disabled={!subjectId || !appId}
              label="确认授权"
            />
          </form>
        </DirectoryDrawer>
      )}
      {selected && (
        <DirectoryDrawer
          title="管理应用授权"
          subtitle={`${subjectName(selected)} · ${appName(selected)}`}
          onClose={close}
        >
          {!!error && <ErrorNotice error={error} />}
          <dl className="directory-details">
            <div>
              <dt>授权主体</dt>
              <dd>{subjectName(selected)}</dd>
            </div>
            <div>
              <dt>应用</dt>
              <dd>{appName(selected)}</dd>
            </div>
            <div>
              <dt>授权权限</dt>
              <dd>{selected.permission === "operate" ? "可操作" : "只读"}</dd>
            </div>
            <div>
              <dt>资源范围</dt>
              <dd>应用内全部资源</dd>
            </div>
          </dl>
          <DirectoryNotice>
            移除后该条直接授权失效。成员若仍从其他部门继承授权，或为平台管理员，仍可能访问此应用。
          </DirectoryNotice>
          <div className="directory-form-footer">
            <button
              className="btn"
              disabled={busy}
              onClick={() => openEdit(selected)}
            >
              编辑授权
            </button>
            <button
              className="btn directory-danger"
              disabled={busy}
              onClick={() => void remove()}
            >
              {busy ? "处理中…" : "移除此授权"}
            </button>
          </div>
        </DirectoryDrawer>
      )}
    </div>
  );
}
