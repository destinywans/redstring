import Pagination, { useListPagination } from "../../../components/Pagination";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Plus, Upload } from "../../../lib/icons";
import { post, patch } from "../../../lib/api";
import { useWorkspace } from "../../../lib/workspace";
import { ErrorNotice } from "../../../components/ui";
import SelectField from "../../../components/SelectField";
import { FilterBar, FilterSearch } from "../../../components/filters/FilterBar";
import MemberImport from "../../members/MemberImport";
import { AdminOnlyNotice } from "../../members/GovernancePage";
import { memberError } from "../../members/errors";
import {
  Avatar,
  DirectoryDrawer,
  DirectoryFilter,
  DirectoryHeader,
  DirectoryNotice,
  DirectoryState,
  DrawerFooter,
  RoleBadge,
  dateLabel,
  roleLabels,
  useDirectory,
  useRefreshDirectory,
  type DirectoryMember,
} from "./shared";

export function MembersSettingsPage() {
  const { user } = useWorkspace();
  return user.role === "admin" ? <MembersDirectory /> : <AdminOnlyNotice />;
}
function MembersDirectory() {
  const { user, preview, notify } = useWorkspace();
  const directory = useDirectory(),
    refresh = useRefreshDirectory();
  const [search, setSearch] = useState(""),
    [role, setRole] = useState(""),
    [department, setDepartment] = useState(""),
    [status, setStatus] = useState("");
  const [editing, setEditing] = useState<DirectoryMember | "new" | null>(null),
    [detail, setDetail] = useState<DirectoryMember | null>(null);
  const [importOpen, setImportOpen] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null);
  const [confirm, setConfirm] = useState<"disable" | "enable" | null>(null);
  const data = directory.data,
    members = data?.members ?? [],
    teams = data?.teams ?? [];
  const rows = members.filter(
    (m) =>
      (!role || m.role === role) &&
      (!department || m.team_ids?.includes(department)) &&
      (status === "deleted"
        ? !!m.deleted_at
        : !m.deleted_at &&
          (!status || (status === "disabled" ? m.disabled : !m.disabled))) &&
      `${m.name} ${m.email} ${(m.team_ids ?? []).map((id) => teams.find((t) => t.id === id)?.name ?? "").join(" ")}`
        .toLowerCase()
        .includes(search.trim().toLowerCase()),
  );
  const pagination = useListPagination(
    rows,
    [search, role, department, status].join("|"),
  );
  const lastAdmin = (m: DirectoryMember) =>
    m.role === "admin" &&
    !m.disabled &&
    !m.deleted_at &&
    members.filter((u) => u.role === "admin" && !u.disabled && !u.deleted_at)
      .length <= 1;
  const close = () => {
    if (!busy) {
      setEditing(null);
      setDetail(null);
      setConfirm(null);
      setError(null);
    }
  };
  const openEdit = (m: DirectoryMember | "new") => {
    setEditing(m);
    setDetail(null);
    setConfirm(null);
    setError(null);
  };
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing || preview || busy) return;
    const f = new FormData(event.currentTarget);
    const body = {
      name: String(f.get("name") || "").trim(),
      email: String(f.get("email") || "").trim(),
      job_title: String(f.get("job_title") || "").trim(),
      role: f.get("role"),
      ...(f.get("password") ? { password: f.get("password") } : {}),
    };
    setBusy(true);
    setError(null);
    try {
      let member = editing === "new" ? null : editing;
      if (!member) {
        const created = await post<{ user: DirectoryMember }>("/users", body);
        member = created.user;
        // Retain the new identity if the subsequent department update fails.
        setEditing(member);
      } else await patch(`/users/${member.id}`, body);
      const nextDepartment = String(f.get("department_id") || "");
      if (nextDepartment !== (member.team_ids?.[0] || "")) {
        try {
          await patch(
            `/rbac/users/${encodeURIComponent(member.id)}/department`,
            { department_id: nextDepartment || null },
          );
        } catch (departmentError) {
          await refresh();
          throw new Error(
            `成员基本资料已保存，部门归属尚未更新：${departmentError instanceof Error ? departmentError.message : String(departmentError)}`,
          );
        }
      }
      await refresh();
      setEditing(null);
      notify("成员信息已保存", "success");
    } catch (e) {
      setError(memberError(e));
    } finally {
      setBusy(false);
    }
  }
  async function changeStatus() {
    if (!detail || !confirm || busy || preview) return;
    setBusy(true);
    setError(null);
    try {
      await patch(`/users/${detail.id}`, { disabled: confirm === "disable" });
      await refresh();
      setDetail(null);
      setConfirm(null);
      notify("成员状态已更新", "success");
    } catch (e) {
      setError(memberError(e));
    } finally {
      setBusy(false);
    }
  }
  const statusLabel = (m: DirectoryMember) =>
    m.deleted_at ? "已删除" : m.disabled ? "已停用" : "活跃";
  const departmentNames = (member: DirectoryMember) =>
    (member.team_ids ?? [])
      .map((id) => teams.find((department) => department.id === id)?.name)
      .filter(Boolean)
      .join("、") || "未分配部门";
  return (
    <div className="directory-page">
      <DirectoryHeader
        title="成员管理"
        description="管理成员资料、部门归属与访问权限。"
        actions={
          <>
            <button
              className="btn"
              disabled={preview || !data}
              onClick={() => setImportOpen(true)}
            >
              <Upload size={16} />
              导入目录
            </button>
            <button
              className="btn btn-primary"
              disabled={preview || !data}
              onClick={() => openEdit("new")}
            >
              <Plus size={16} />
              添加成员
            </button>
          </>
        }
      />
      <section className="directory-card">
        <div className="directory-card-toolbar">
          <FilterBar label="成员搜索与筛选">
            <FilterSearch
              value={search}
              onChange={setSearch}
              onSubmit={() => setSearch(search.trim())}
              showSubmit={false}
              placeholder="搜索成员姓名、邮箱或部门"
              label="搜索成员"
            />
            <DirectoryFilter
              label="角色"
              value={role}
              onChange={setRole}
              options={(data?.roles ?? []).map((r) => ({
                value: r.id,
                label: r.name,
              }))}
            />
            <DirectoryFilter
              label="部门"
              value={department}
              onChange={setDepartment}
              options={teams.map((t) => ({ value: t.id, label: t.name }))}
            />
            <DirectoryFilter
              label="状态"
              value={status}
              onChange={setStatus}
              options={[
                { value: "enabled", label: "活跃" },
                { value: "disabled", label: "已停用" },
                { value: "deleted", label: "已删除" },
              ]}
            />
          </FilterBar>
        </div>
        <DirectoryState
          loading={directory.isPending && !preview}
          error={directory.error}
          retry={directory.refetch}
        />
        {!directory.isPending && !directory.error && (
          <>
            <div className="directory-table-wrap">
              <table className="directory-table">
                <thead>
                  <tr>
                    <th>成员</th>
                    <th>角色</th>
                    <th>部门</th>
                    <th>状态</th>
                    <th>加入时间</th>
                  </tr>
                </thead>
                <tbody>
                  {pagination.items.map((m) => (
                    <tr key={m.id}>
                      <td>
                        <div className="directory-person">
                          <Avatar name={m.name} />
                          <div>
                            <button
                              className="directory-name"
                              onClick={() => {
                                setDetail(m);
                                setError(null);
                                setConfirm(null);
                              }}
                            >
                              {m.name}
                              {m.id === user.id && (
                                <span className="directory-inline-label">
                                  你
                                </span>
                              )}
                            </button>
                            <small>{m.email}</small>
                          </div>
                        </div>
                      </td>
                      <td>
                        <RoleBadge
                          role={m.role}
                          label={data?.roles.find((r) => r.id === m.role)?.name}
                        />
                      </td>
                      <td>{departmentNames(m)}</td>
                      <td>
                        <span
                          className={`directory-status ${m.deleted_at ? "is-deleted" : m.disabled ? "is-disabled" : ""}`}
                        >
                          {statusLabel(m)}
                        </span>
                      </td>
                      <td>{dateLabel(m.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <DirectoryState
              loading={false}
              error={null}
              empty={!rows.length}
              retry={directory.refetch}
            />
            <Pagination
              total={pagination.total}
              page={pagination.page}
              pageSize={pagination.pageSize}
              loading={directory.isFetching}
              onPageSizeChange={pagination.setPageSize}
              onPageChange={pagination.setPage}
            />
          </>
        )}
        {preview && (
          <DirectoryNotice>
            设置目录需要连接真实服务后读取，预览模式不提供账号写入。
          </DirectoryNotice>
        )}
      </section>
      {editing && (
        <DirectoryDrawer
          title={editing === "new" ? "添加成员" : "编辑成员"}
          onClose={close}
        >
          <form
            className="directory-form"
            onSubmit={save}
            key={editing === "new" ? "new" : editing.id}
          >
            {!!error && <ErrorNotice error={error} />}
            <label>
              姓名
              <input
                className="field"
                name="name"
                required
                maxLength={200}
                defaultValue={editing === "new" ? "" : editing.name}
              />
            </label>
            <label>
              邮箱
              <input
                className="field"
                name="email"
                type="email"
                required
                readOnly={
                  editing !== "new" &&
                  !!editing.source &&
                  editing.source !== "local"
                }
                defaultValue={editing === "new" ? "" : editing.email}
              />
            </label>
            <label>
              账户基础角色
              <SelectField
                className="field"
                name="role"
                defaultValue={editing === "new" ? "viewer" : editing.role}
              >
                {(data?.roles ?? []).map((r) => (
                  <option
                    key={r.id}
                    value={r.id}
                    disabled={
                      editing !== "new" &&
                      lastAdmin(editing) &&
                      r.id !== "admin"
                    }
                  >
                    {r.name}
                  </option>
                ))}
              </SelectField>
            </label>
            <div className="directory-form-row">
              <label>
                部门
                <SelectField
                  className="field"
                  name="department_id"
                  defaultValue={
                    editing === "new" ? "" : editing.team_ids?.[0] || ""
                  }
                >
                  <option value="">未分配部门</option>
                  {teams.map((team) => (
                    <option key={team.id} value={team.id}>
                      {team.name}
                    </option>
                  ))}
                </SelectField>
                <small className="muted">
                  每位成员最多归属一个部门，更换后继承新部门的角色。
                </small>
              </label>
              <label>
                职位
                <input
                  className="field"
                  name="job_title"
                  maxLength={200}
                  defaultValue={editing === "new" ? "" : editing.job_title}
                />
              </label>
            </div>
            {(editing === "new" || !editing.is_mock) && (
              <label>
                {editing === "new" ? "初始密码" : "新密码（留空保留原密码）"}
                <input
                  className="field"
                  name="password"
                  type="password"
                  minLength={12}
                  autoComplete="new-password"
                  required={editing === "new"}
                />
              </label>
            )}
            <DirectoryNotice>
              账户基础角色保留原有兼容授权。自定义角色可直接关联成员，或通过唯一所属部门继承。
            </DirectoryNotice>
            {editing !== "new" && lastAdmin(editing) && (
              <p className="directory-source">
                至少保留一位启用的管理员，当前成员不能降级或停用。
              </p>
            )}
            <DrawerFooter busy={busy} onClose={close} />
          </form>
        </DirectoryDrawer>
      )}
      {detail && (
        <DirectoryDrawer
          title={
            confirm
              ? `${confirm === "disable" ? "停用" : "恢复"}成员`
              : detail.name
          }
          subtitle={detail.email}
          onClose={close}
        >
          {!!error && <ErrorNotice error={error} />}
          <DirectoryNotice>
            在角色管理的“用户”页签维护直接角色；“部门”页签维护继承角色。
            <Link to="/platform/roles">前往角色管理</Link>
          </DirectoryNotice>
          <dl className="directory-details">
            {[
              ["姓名", detail.name],
              ["平台角色", roleLabels[detail.role]],
              ["部门", departmentNames(detail)],
              ["职位", detail.job_title || "未填写"],
              ["状态", statusLabel(detail)],
              [
                "账号来源",
                !detail.source || detail.source === "local"
                  ? "本地账号"
                  : "目录导入",
              ],
              ["加入时间", dateLabel(detail.created_at)],
            ].map(([k, v]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </div>
            ))}
          </dl>
          {confirm ? (
            <>
              <DirectoryNotice>
                {confirm === "disable"
                  ? "停用后无法访问平台，账号资料与历史记录保留。"
                  : "恢复后成员可用已有密码登录。目录导入账号需先设置密码。"}
              </DirectoryNotice>
              <div className="directory-form-footer">
                <button
                  className="btn"
                  disabled={busy}
                  onClick={() => setConfirm(null)}
                >
                  取消
                </button>
                <button
                  className="btn btn-primary"
                  disabled={busy}
                  onClick={() => void changeStatus()}
                >
                  {busy ? "处理中…" : "确认"}
                </button>
              </div>
            </>
          ) : (
            !detail.deleted_at && (
              <>
                <div className="directory-actions">
                  <button
                    className="btn btn-primary"
                    onClick={() => openEdit(detail)}
                  >
                    编辑成员
                  </button>
                  {!detail.is_mock && (
                    <button
                      className="btn"
                      disabled={lastAdmin(detail)}
                      onClick={() =>
                        setConfirm(detail.disabled ? "enable" : "disable")
                      }
                    >
                      {detail.disabled ? "恢复使用" : "停用"}
                    </button>
                  )}
                </div>
                {lastAdmin(detail) && (
                  <p className="directory-source">
                    此成员是最后一位启用的管理员，不能停用。
                  </p>
                )}
              </>
            )
          )}
        </DirectoryDrawer>
      )}
      <MemberImport
        open={importOpen}
        onClose={() => setImportOpen(false)}
        onImported={refresh}
      />
    </div>
  );
}
