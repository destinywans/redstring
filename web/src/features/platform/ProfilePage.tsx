import { PageHeader } from "../../components/patterns/ListPage";
import { useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, patch, post, request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import Pagination, { useListPagination } from "../../components/Pagination";
import { LoaderCircle } from "../../lib/icons";
import { prepareAvatar } from "./avatar";
import { ErrorNotice, Modal } from "../../components/ui";
import "./profile.css";

type Profile = {
  user: {
    id: string;
    name: string;
    email: string;
    phone?: string | null;
    avatar_url?: string;
    role: string;
  };
  departments: { id: string; name: string }[];
  roles?: {
    id: string;
    name: string;
    source: "direct" | "department";
    department_id?: string;
  }[];
  permissions: {
    view: boolean;
    update: boolean;
    approve: boolean;
    delete: boolean;
  };
  is_platform_admin: boolean;
  applications: { id: string; key: string; name: string }[];
  can_change_password: boolean;
};
const roles: Record<string, string> = {
  admin: "管理员",
  operator: "操作员",
  viewer: "只读成员",
};
const permissionLabels: Record<keyof Profile["permissions"], string> = {
  view: "查看资源",
  update: "变更资源",
  approve: "审批操作",
  delete: "删除资源",
};

function profileError(error: unknown, avatar = false) {
  if (!(error instanceof ApiError)) return error;
  const messages: Record<string, string> = {
    incorrect_password: "旧密码不正确，请重新输入。",
    invalid_password:
      "新密码需为 8–128 位，包含大写字母、小写字母、数字和符号。",
    no_local_password: "此账户尚未设置本地密码，请联系管理员。",
    invalid_profile: avatar
      ? "这张图片未能保存，请换一张图片重试。"
      : "新密码不能与旧密码相同，请重新设置。",
    profile_unavailable: "资料暂时无法保存，请稍后重试。",
  };
  return messages[error.code] ? new Error(messages[error.code]) : error;
}
export function ProfilePage() {
  const { user, preview, notify } = useWorkspace();
  const client = useQueryClient();
  const profile = useQuery({
    queryKey: ["profile", user.id],
    queryFn: () => request<Profile>("/profile"),
    enabled: !preview,
  });
  const fileInput = useRef<HTMLInputElement>(null);
  const avatarLock = useRef(false);
  const [avatarError, setAvatarError] = useState<unknown>(null);
  const [avatarBusy, setAvatarBusy] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [passwordBusy, setPasswordBusy] = useState(false);
  const [passwordError, setPasswordError] = useState<unknown>(null);
  const data = profile.data;
  const applicationPages = useListPagination(data?.applications ?? [], user.id);

  async function chooseAvatar(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || avatarLock.current) return;
    avatarLock.current = true;
    setAvatarBusy(true);
    setAvatarError(null);
    try {
      const avatarData = await prepareAvatar(file);
      const updated = await patch<Profile>("/profile", {
        avatar_data_url: avatarData,
      });
      client.setQueryData(["profile", user.id], updated);
      await Promise.all([
        client.invalidateQueries({ queryKey: ["profile"] }),
        client.invalidateQueries({ queryKey: ["auth"] }),
      ]);
      notify("头像已更新", "success");
    } catch (error) {
      setAvatarError(profileError(error, true));
    } finally {
      avatarLock.current = false;
      setAvatarBusy(false);
    }
  }

  async function changePassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (passwordBusy) return;
    const form = event.currentTarget;
    const values = new FormData(form);
    const currentPassword = String(values.get("current_password") || "");
    const newPassword = String(values.get("new_password") || "");
    const confirmation = String(values.get("confirm_password") || "");
    setPasswordError(null);
    const size = Array.from(newPassword).length;
    if (
      size < 8 ||
      size > 128 ||
      !/[A-Z]/.test(newPassword) ||
      !/[a-z]/.test(newPassword) ||
      !/[0-9]/.test(newPassword) ||
      !/[\p{P}\p{S}]/u.test(newPassword)
    ) {
      setPasswordError(
        new Error("新密码需为 8–128 位，包含大写字母、小写字母、数字和符号。"),
      );
      return;
    }
    if (newPassword !== confirmation) {
      setPasswordError(new Error("两次输入的新密码不一致。"));
      return;
    }
    if (newPassword === currentPassword) {
      setPasswordError(new Error("新密码不能与旧密码相同，请重新设置。"));
      return;
    }
    setPasswordBusy(true);
    try {
      await post("/profile/password", {
        current_password: currentPassword,
        new_password: newPassword,
      });
      form.reset();
      window.location.assign("/");
    } catch (error) {
      setPasswordError(profileError(error));
      setPasswordBusy(false);
    }
  }

  if (preview)
    return (
      <div className="profile-page">
        <PageHeader title="个人资料" description="登录后查看和管理个人资料。" />
      </div>
    );
  if (profile.isPending)
    return (
      <div className="profile-page" role="status">
        正在加载个人资料…
      </div>
    );
  if (profile.error || !data)
    return (
      <div className="profile-page">
        <ErrorNotice
          error={profile.error || new Error("暂时无法获取个人资料。")}
        />
        <button className="btn" onClick={() => profile.refetch()}>
          重试
        </button>
      </div>
    );
  const avatar = data.user.avatar_url;
  return (
    <div className="profile-page">
      <PageHeader
        title="个人资料"
        description="管理头像与账户安全，查看你的部门和访问权限。"
      />
      <section className="profile-card" aria-labelledby="profile-basic-heading">
        <h2 id="profile-basic-heading">基本资料</h2>
        <div className="profile-avatar-row">
          <button
            type="button"
            className="profile-avatar"
            aria-label={avatarBusy ? "正在更新头像" : "更换头像"}
            aria-busy={avatarBusy}
            disabled={avatarBusy}
            onClick={() => fileInput.current?.click()}
          >
            {avatar ? (
              <img src={avatar} alt="" />
            ) : (
              <span aria-hidden="true">
                {(data.user.name || data.user.email || "?")
                  .slice(0, 1)
                  .toUpperCase()}
              </span>
            )}
            <span className="profile-avatar-overlay" aria-hidden="true">
              {avatarBusy ? (
                <LoaderCircle size={22} className="profile-avatar-spinner" />
              ) : (
                "更换头像"
              )}
            </span>
          </button>
          <input
            ref={fileInput}
            className="profile-file-input"
            type="file"
            accept="image/png,image/jpeg,image/webp,image/gif"
            aria-label="选择头像图片"
            onChange={chooseAvatar}
            disabled={avatarBusy}
          />
          <span className="sr-only" role="status">
            {avatarBusy ? "正在处理并保存头像…" : ""}
          </span>
        </div>
        {!!avatarError && <ErrorNotice error={avatarError} />}
        <dl className="profile-info">
          <div>
            <dt>姓名</dt>
            <dd>{data.user.name || "未设置"}</dd>
          </div>
          <div>
            <dt>邮箱</dt>
            <dd>{data.user.email || "未设置"}</dd>
          </div>
          <div>
            <dt>手机号</dt>
            <dd>{data.user.phone || "未设置"}</dd>
          </div>
          <div>
            <dt>部门</dt>
            <dd>
              {data.departments
                ?.map((department) => department.name)
                .join("、") || "未分配"}
            </dd>
          </div>
          <div>
            <dt>平台角色</dt>
            <dd>{roles[data.user.role] || data.user.role}</dd>
          </div>
        </dl>
        <div className="profile-password-row">
          <div>
            <h3>密码</h3>
            <p className="profile-hint">
              {data.can_change_password
                ? "更改用于登录的密码"
                : "此账户尚未设置本地密码，请联系管理员。"}
            </p>
          </div>
          <button
            className="btn"
            disabled={!data.can_change_password}
            onClick={() => {
              setPasswordError(null);
              setPasswordOpen(true);
            }}
          >
            更改密码
          </button>
        </div>
      </section>
      <section
        className="profile-card"
        aria-labelledby="profile-access-heading"
      >
        <h2 id="profile-access-heading">访问权限</h2>
        <h3>角色关联</h3>
        {data.roles?.length ? (
          <div className="profile-permissions">
            {data.roles.map((role) => (
              <span
                key={`${role.id}-${role.source}-${role.department_id || ""}`}
                data-enabled
              >
                {role.name}
                <span>
                  {role.source === "direct"
                    ? "直接关联"
                    : `部门继承 · ${data.departments.find((department) => department.id === role.department_id)?.name || "所属部门"}`}
                </span>
              </span>
            ))}
          </div>
        ) : (
          <p className="profile-hint">暂无角色关联。</p>
        )}
        <h3>资源操作权限</h3>
        <div className="profile-permissions">
          {Object.entries(permissionLabels).map(([key, label]) => (
            <span
              key={key}
              data-enabled={
                !!data.permissions[key as keyof Profile["permissions"]]
              }
            >
              {label}
              <span>
                {data.permissions[key as keyof Profile["permissions"]]
                  ? "已授权"
                  : "未授权"}
              </span>
            </span>
          ))}
        </div>
        <h3>平台管理</h3>
        <p className="profile-hint">
          {data.is_platform_admin
            ? "可管理平台设置、成员、部门及角色权限。"
            : "未获得平台管理权限。"}
        </p>
        <h3>关联应用</h3>
        <p className="profile-hint">展示有权查看的资源所关联的应用。</p>
        {data.applications?.length ? (
          <div className="profile-table-wrap">
            <table className="profile-table ui-data-table">
              <thead>
                <tr>
                  <th>应用</th>
                </tr>
              </thead>
              <tbody>
                {applicationPages.items.map((application) => (
                  <tr key={application.id}>
                    <td>{application.key || application.name}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination
              page={applicationPages.page}
              pageSize={applicationPages.pageSize}
              total={applicationPages.total}
              onPageChange={applicationPages.setPage}
              onPageSizeChange={applicationPages.setPageSize}
              ariaLabel="个人关联应用分页"
            />
          </div>
        ) : (
          <p className="profile-hint">暂无关联应用</p>
        )}
      </section>
      <Modal
        open={passwordOpen}
        onOpenChange={(open) => {
          if (passwordBusy) return;
          setPasswordOpen(open);
          setPasswordError(null);
        }}
        title="更改密码"
        description="设置新密码后，所有设备上的当前账户都将退出登录。"
      >
        <form
          className="profile-password-form"
          onSubmit={changePassword}
          aria-busy={passwordBusy}
        >
          <label>
            旧密码
            <input
              className="field"
              type="password"
              name="current_password"
              autoComplete="current-password"
              required
              disabled={passwordBusy}
            />
          </label>
          <label>
            新密码
            <input
              className="field"
              type="password"
              name="new_password"
              autoComplete="new-password"
              aria-describedby="profile-password-requirements"
              required
              disabled={passwordBusy}
            />
            <span className="profile-hint" id="profile-password-requirements">
              8–128 位，需包含大写字母、小写字母、数字和符号。
            </span>
          </label>
          <label>
            确认新密码
            <input
              className="field"
              type="password"
              name="confirm_password"
              autoComplete="new-password"
              required
              disabled={passwordBusy}
            />
          </label>
          {!!passwordError && <ErrorNotice error={passwordError} />}
          <footer className="profile-password-actions">
            <button
              className="btn"
              type="button"
              disabled={passwordBusy}
              onClick={() => {
                setPasswordOpen(false);
                setPasswordError(null);
              }}
            >
              取消
            </button>
            <button
              className="btn btn-primary"
              type="submit"
              disabled={passwordBusy}
            >
              {passwordBusy ? "保存中…" : "保存新密码"}
            </button>
          </footer>
        </form>
      </Modal>
    </div>
  );
}
