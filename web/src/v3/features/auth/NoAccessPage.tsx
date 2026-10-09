import { useEffect } from "react";
import { BrandMark } from "../../ui/BrandMark";
import { Btn, Tile } from "../../ui/core";
import { useI18n } from "../../i18n";
import { SIGNED_IN_KEY } from "../../api/client";
import { useEnv } from "../../app/environment";
import "./auth.css";

/** Signs out and returns to /login (shared by the account menu and this page). */
export function signOutToLogin() {
  void fetch("/api/v1/auth/logout", { method: "POST", credentials: "same-origin" }).finally(() => {
    try {
      localStorage.removeItem(SIGNED_IN_KEY);
    } catch {
      /* convenience only */
    }
    location.assign("/login");
  });
}

/**
 * Signed in, but the account has no environment yet (approved design, section 8): new invitees start without one,
 * so /me and every page answer 403. Instead of endless skeletons, say who can fix it and offer re-check / sign out.
 */
export default function NoAccessPage() {
  const { tx } = useI18n();
  const instance = useEnv((s) => s.instance);
  useEffect(() => {
    document.title = tx("还没有环境权限", "No environment access") + " · Redstring";
  }, [tx]);
  const prod = instance?.tier === "production";
  return (
    <main className="v3-login">
      <div className="v3-login-card" style={{ maxWidth: 360 }}>
        <div className="v3-login-brand">
          <BrandMark size={22} />
          Redstring
        </div>
        <div className="v3-col" style={{ gap: 8, alignItems: "flex-start" }}>
          <Tile icon="lock" size={30} />
          <h1 style={{ margin: 0, fontSize: 18 }}>{tx("你还没有任何环境的访问权限", "You don't have access to any environment yet")}</h1>
          <p className="v3-login-lead" style={{ margin: 0 }}>
            {tx("账号已经创建，但管理员还没有给你授予环境。请管理员在「成员与权限」中授予后，点“重新检查”。", "Your account exists, but an admin hasn't granted you an environment yet. Ask an admin to grant one under Members, then choose Check again.")}
          </p>
        </div>
        <div className="v3-row" style={{ gap: 8 }}>
          <Btn kind="primary" h={32} onClick={() => location.reload()}>
            {tx("重新检查", "Check again")}
          </Btn>
          <Btn h={32} onClick={signOutToLogin}>
            {tx("退出登录", "Sign out")}
          </Btn>
        </div>
        {instance && (
          <div className="v3-login-instance">
            <span className={prod ? "tag prod" : "tag"}>● {prod ? tx("生产", "Production") : tx("非生产", "Non-production")}</span>
            {instance.domain}
          </div>
        )}
      </div>
    </main>
  );
}
