import SelectField from "../../components/SelectField";
import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Upload, Users } from "../../lib/icons";
import { request, post, patch } from "../../lib/api";
import type { User } from "../../lib/types";
import { useWorkspace } from "../../lib/workspace";
import { Modal, ErrorNotice } from "../../components/ui";
import { AdminOnlyNotice } from "./GovernancePage";
import MemberImport from "./MemberImport";
import { memberError } from "./errors";
import "../workspace/workspace.css";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import "./members.css";

export type Member = User & {
  department?: string;
  job_title?: string;
  source?: string;
  external_id?: string;
  deleted_at?: string | null;
  created_at?: string;
};
const roles = { admin: "管理员", operator: "操作员", viewer: "只读" };
const statusLabel = (m: Member) =>
  m.deleted_at ? "已删除" : m.disabled ? "已停用" : "启用";

export default function MembersPage() {
  const { user } = useWorkspace();
  return user.role === "admin" ? <MemberDirectory /> : <AdminOnlyNotice />;
}

function MemberDirectory() {
  const { preview, user, notify } = useWorkspace();
  const qc = useQueryClient();
  const [search, setSearch] = useState("");
  const [role, setRole] = useState("");
  const [status, setStatus] = useState("");
  const [department, setDepartment] = useState("");
  const [editing, setEditing] = useState<Member | "new" | null>(null);
  const [detail, setDetail] = useState<Member | null>(null);
  const [confirmation, setConfirmation] = useState<{
    member: Member;
    action: "disable" | "enable" | "delete";
  } | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const members = useQuery({
    queryKey: ["users", "directory", status === "deleted"],
    queryFn: () =>
      request<{ items: Member[] | null }>(
        `/users${status === "deleted" ? "?include_deleted=true" : ""}`,
      ),
    enabled: !preview,
  });
  const rows: Member[] = preview ? [user] : (members.data?.items ?? []);
  const departments = [
    ...new Set(rows.map((m) => m.department).filter((d): d is string => !!d)),
  ].sort();
  const visible = rows.filter(
    (m) =>
      (!role || m.role === role) &&
      (!department ||
        (department === "__none"
          ? !m.department
          : m.department === department)) &&
      (status === "deleted"
        ? !!m.deleted_at
        : !m.deleted_at &&
          (status === "enabled"
            ? !m.disabled
            : status === "disabled"
              ? !!m.disabled
              : true)) &&
      `${m.name} ${m.email} ${m.department ?? ""} ${m.external_id ?? ""}`
        .toLowerCase()
        .includes(search.trim().toLowerCase()),
  );
  function openEdit(m: Member | "new") {
    setError(null);
    setEditing(m);
  }
  function openConfirmation(
    member: Member,
    action: "disable" | "enable" | "delete",
  ) {
    setError(null);
    setConfirmation({ member, action });
  }
  async function refresh() {
    await Promise.all([
      qc.invalidateQueries({ queryKey: ["users"] }),
      qc.invalidateQueries({ queryKey: ["audit"] }),
    ]);
  }
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (preview || !editing) return;
    const f = new FormData(e.currentTarget);
    const body = {
      name: String(f.get("name") ?? "").trim(),
      email: String(f.get("email") ?? "").trim(),
      role: f.get("role"),
      department: String(f.get("department") ?? "").trim(),
      job_title: String(f.get("job_title") ?? "").trim(),
      ...(f.get("password") ? { password: f.get("password") } : {}),
    };
    setBusy(true);
    setError(null);
    try {
      if (editing === "new") await post("/users", body);
      else await patch(`/users/${editing.id}`, body);
      await refresh();
      setEditing(null);
      setDetail(null);
      notify("成员信息已保存", "success");
    } catch (e) {
      setError(memberError(e));
    } finally {
      setBusy(false);
    }
  }
  async function changeStatus() {
    if (!confirmation || preview) return;
    setBusy(true);
    setError(null);
    try {
      const { member, action } = confirmation;
      if (action === "delete")
        await request(`/users/${member.id}`, { method: "DELETE" });
      else
        await patch(`/users/${member.id}`, { disabled: action === "disable" });
      await refresh();
      setConfirmation(null);
      setDetail(null);
      notify(
        action === "delete"
          ? "成员已删除，历史记录保留"
          : action === "disable"
            ? "成员已停用"
            : "成员已恢复使用",
        "success",
      );
    } catch (e) {
      setError(memberError(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="management-page members-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">WORKSPACE DIRECTORY</span>
          <h1>成员管理</h1>
          <p>维护平台账号、部门和角色，或从企业 IAM 导入成员目录。</p>
        </div>
        <div className="members-actions">
          <button
            className="btn"
            disabled={preview}
            onClick={() => setImportOpen(true)}
          >
            <Upload size={16} />
            导入 IAM 成员
          </button>
          <button
            className="btn btn-primary"
            disabled={preview}
            onClick={() => openEdit("new")}
          >
            <Plus size={16} />
            添加成员
          </button>
        </div>
      </header>
      <section className="card management-card">
        <FilterBar label="成员搜索与筛选">
          <FilterSearch
            value={search}
            onChange={setSearch}
            onSubmit={() => setSearch(search.trim())}
            label="搜索成员"
            placeholder="姓名、邮箱、部门或外部标识"
          />
          <FilterSelect
            id="member-role"
            label="角色"
            values={role ? [role] : []}
            options={Object.entries(roles).map(([value, label]) => ({
              value,
              label,
            }))}
            onChange={(v) => setRole(v[0] ?? "")}
          />
          <FilterSelect
            id="member-status"
            label="状态"
            values={status ? [status] : []}
            options={[
              { value: "enabled", label: "启用" },
              { value: "disabled", label: "已停用" },
              { value: "deleted", label: "已删除" },
            ]}
            emptyLabel="在册成员"
            onChange={(v) => setStatus(v[0] ?? "")}
          />
          <FilterSelect
            id="member-department"
            label="部门"
            values={department ? [department] : []}
            options={[
              { value: "__none", label: "未填写部门" },
              ...departments.map((value) => ({ value, label: value })),
            ]}
            searchable
            onChange={(v) => setDepartment(v[0] ?? "")}
          />
          {(search || role || status || department) && (
            <button
              className="filter-button"
              onClick={() => {
                setSearch("");
                setRole("");
                setStatus("");
                setDepartment("");
              }}
            >
              清除筛选
            </button>
          )}
        </FilterBar>
        <p className="members-count">
          {visible.length} 位成员 ·
          角色定义操作上限，具体应用访问由应用权限授予。
        </p>
        {members.error && <ErrorNotice error={members.error} />}
        {members.isPending && !preview ? (
          <p className="management-empty">正在读取成员目录…</p>
        ) : (
          <div className="management-table-wrap">
            <table className="management-table">
              <thead>
                <tr>
                  <th>成员</th>
                  <th>部门 / 职位</th>
                  <th>角色</th>
                  <th>状态</th>
                  <th>来源</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {visible.map((m) => (
                  <tr key={m.id}>
                    <td>
                      <button
                        className="member-name"
                        onClick={() => setDetail(m)}
                      >
                        {m.name}
                        {m.id === user.id && <span className="badge">你</span>}
                      </button>
                      <small>{m.email}</small>
                    </td>
                    <td>
                      {m.department || "未填写"}
                      <small>{m.job_title || "—"}</small>
                    </td>
                    <td>{roles[m.role]}</td>
                    <td>
                      <span
                        className={`member-status ${m.disabled || m.deleted_at ? "inactive" : ""}`}
                      >
                        {statusLabel(m)}
                      </span>
                    </td>
                    <td>
                      {!m.source || m.source === "local"
                        ? "本地账号"
                        : m.source}
                      <small>{m.external_id}</small>
                    </td>
                    <td>
                      <div className="members-row-actions">
                        <button className="btn" onClick={() => setDetail(m)}>
                          详情
                        </button>
                        {!m.deleted_at && (
                          <>
                            <button
                              className="btn"
                              disabled={preview}
                              onClick={() => openEdit(m)}
                            >
                              编辑
                            </button>
                            <button
                              className="btn"
                              disabled={preview}
                              onClick={() =>
                                openConfirmation(
                                  m,
                                  m.disabled ? "enable" : "disable",
                                )
                              }
                            >
                              {m.disabled ? "恢复" : "停用"}
                            </button>
                            <button
                              className="btn member-danger"
                              disabled={preview}
                              onClick={() => openConfirmation(m, "delete")}
                            >
                              删除
                            </button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!visible.length && !members.error && (
              <div className="management-empty">
                <Users size={24} />
                <p>没有符合条件的成员，请调整筛选或添加成员。</p>
              </div>
            )}
          </div>
        )}
      </section>
      <Modal
        open={!!editing}
        onOpenChange={(v) => !v && !busy && setEditing(null)}
        title={editing === "new" ? "添加成员" : "编辑成员"}
      >
        {editing && (
          <form
            key={editing === "new" ? "new" : editing.id}
            className="management-form member-form"
            onSubmit={save}
          >
            {!!error && <ErrorNotice error={error} />}
            <div className="member-form-grid">
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
                  required
                  type="email"
                  readOnly={
                    editing !== "new" &&
                    !!editing.source &&
                    editing.source !== "local"
                  }
                  defaultValue={editing === "new" ? "" : editing.email}
                />
              </label>
              <label>
                部门
                <input
                  className="field"
                  name="department"
                  maxLength={200}
                  defaultValue={editing === "new" ? "" : editing.department}
                />
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
            <label>
              平台角色
              <SelectField
                className="field"
                name="role"
                defaultValue={editing === "new" ? "viewer" : editing.role}
              >
                {Object.entries(roles).map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </SelectField>
            </label>
            <p className="muted">
              管理员维护工作区；操作员可执行已授权应用的调查与审批；只读成员查看已授权资料。
            </p>
            <label>
              {editing === "new" ? "初始密码" : "新密码（留空保留原密码）"}
              <input
                className="field"
                name="password"
                type="password"
                autoComplete="new-password"
                minLength={12}
                required={editing === "new"}
              />
            </label>
            {editing !== "new" &&
              editing.source &&
              editing.source !== "local" && (
                <p className="muted">
                  此成员来自 {editing.source}
                  。邮箱由目录导入维护；姓名、部门和职位会在下次导入时按目录更新。新导入成员需先设置本地登录密码，再恢复使用。
                </p>
              )}
            <footer>
              <button
                className="btn"
                type="button"
                disabled={busy}
                onClick={() => setEditing(null)}
              >
                取消
              </button>
              <button className="btn btn-primary" disabled={busy}>
                {busy ? "保存中…" : "保存成员"}
              </button>
            </footer>
          </form>
        )}
      </Modal>
      <Modal
        open={!!detail}
        onOpenChange={(v) => !v && setDetail(null)}
        title="成员详情"
      >
        {detail && (
          <>
            <dl className="member-details">
              {[
                ["姓名", detail.name],
                ["邮箱", detail.email],
                ["部门", detail.department],
                ["职位", detail.job_title],
                ["角色", roles[detail.role]],
                ["状态", statusLabel(detail)],
                ["账号来源", detail.source || "local"],
                ["外部成员标识", detail.external_id],
              ].map(([k, v]) => (
                <div key={k}>
                  <dt>{k}</dt>
                  <dd>{v || "未填写"}</dd>
                </div>
              ))}
            </dl>
            {!detail.deleted_at && (
              <button
                className="btn btn-primary"
                disabled={preview}
                onClick={() => {
                  openEdit(detail);
                  setDetail(null);
                }}
              >
                编辑成员
              </button>
            )}
          </>
        )}
      </Modal>
      <Modal
        open={!!confirmation}
        onOpenChange={(v) => !v && !busy && setConfirmation(null)}
        title={
          confirmation?.action === "delete"
            ? "删除成员"
            : confirmation?.action === "disable"
              ? "停用成员"
              : "恢复成员"
        }
      >
        {confirmation && (
          <div className="management-form">
            {!!error && <ErrorNotice error={error} />}
            <p>
              确认
              {confirmation.action === "delete"
                ? "删除"
                : confirmation.action === "disable"
                  ? "停用"
                  : "恢复"}{" "}
              <strong>{confirmation.member.name}</strong>（
              {confirmation.member.email}）？
            </p>
            <p className="muted">
              {confirmation.action === "delete"
                ? "删除后成员无法登录，已产生的调查和审计记录保留。此页面不支持撤销删除。"
                : confirmation.action === "disable"
                  ? "停用后成员无法继续访问平台，账号资料和历史记录保留。"
                  : "恢复后成员可用已有本地密码登录。IAM 导入成员请先通过编辑设置密码。"}
            </p>
            <p className="muted">平台必须保留至少一位启用的管理员。</p>
            <footer>
              <button
                className="btn"
                disabled={busy}
                onClick={() => setConfirmation(null)}
              >
                取消
              </button>
              <button
                className={`btn ${confirmation.action === "delete" ? "member-danger" : "btn-primary"}`}
                disabled={busy || preview}
                onClick={() => void changeStatus()}
              >
                {busy ? "处理中…" : "确认操作"}
              </button>
            </footer>
          </div>
        )}
      </Modal>
      <MemberImport
        open={importOpen}
        onClose={() => setImportOpen(false)}
        onImported={refresh}
      />
    </div>
  );
}
