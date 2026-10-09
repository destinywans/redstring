import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Btn, Field, Input } from "../../ui/core";
import { BrandMark } from "../../ui/BrandMark";
import { useI18n } from "../../i18n";
import { ApiError, api } from "../../api/client";
import type { InstanceInfo } from "../../api/types";
import "./auth.css";

/**
 * /invite — public, outside the shell like /login. The invitation mail carries only the product entry and a one-time
 * code (never in the link), so the person pastes the code here and sets a name and password
 * (POST /auth/invitations/accept). The new account is a read-only member without environment access.
 */
export default function InvitePage() {
  const { tx } = useI18n();
  const [instance, setInstance] = useState<InstanceInfo | null>(null);
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<{ field: "code" | "password" | "form"; text: string; existing?: boolean } | null>(null);
  const [lockedUntil, setLockedUntil] = useState(0);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [, tick] = useState(0);

  useEffect(() => {
    document.title = tx("加入工作区", "Join the workspace") + " · Redstring";
    fetch("/api/v1/instance", { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then(setInstance)
      .catch(() => undefined);
  }, [tx]);
  useEffect(() => {
    if (lockedUntil <= Date.now()) return;
    const t = setInterval(() => (Date.now() >= lockedUntil ? (clearInterval(t), tick((n) => n + 1)) : tick((n) => n + 1)), 1000);
    return () => clearInterval(t);
  }, [lockedUntil]);
  const locked = lockedUntil > Date.now();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy || locked) return;
    if (password.length < 12) return setError({ field: "password", text: tx("密码至少 12 位。", "Use at least 12 characters.") });
    if (password !== confirm) return setError({ field: "password", text: tx("两次输入的密码不一致。", "The passwords don't match.") });
    setBusy(true);
    setError(null);
    try {
      await api("POST", "/auth/invitations/accept", { body: { code: code.trim(), name: name.trim(), password } });
      setDone(true);
    } catch (err) {
      if (err instanceof ApiError && err.code === "invitation_conflict") setError({ field: "form", text: tx("这个邮箱已经有账号，直接登录即可。", "This email already has an account; just sign in."), existing: true });
      else if (err instanceof ApiError && (err.code === "invitation_invalid" || (err.status === 422 && !name.trim())))
        setError({ field: "code", text: tx("邀请码无效或已过期。邀请码 24 小时内有效、只能用一次，请联系管理员重新邀请。", "The invitation code is invalid or expired. Codes last 24 hours and work once; ask an admin to invite you again.") });
      else if (err instanceof ApiError && err.status === 422) setError({ field: "code", text: tx("请检查邀请码和姓名：邀请码要完整复制，姓名不超过 120 个字。", "Check the code and name: copy the whole code; names are up to 120 characters.") });
      else if (err instanceof ApiError && err.status === 429) {
        setLockedUntil(Date.now() + 60_000);
        setError({ field: "form", text: tx("尝试次数过多，请 1 分钟后再试。", "Too many attempts. Try again in a minute.") });
      } else if (err instanceof ApiError) setError({ field: "form", text: tx("暂时无法创建账号，请稍后再试。", "Can't create the account right now. Try again shortly.") });
      else setError({ field: "form", text: tx("连不上服务器，请检查网络后重试。", "Can't reach the server. Check your connection and try again.") });
    } finally {
      setBusy(false);
    }
  };

  const prod = instance?.tier === "production";
  const brand = (
    <div className="v3-login-brand">
      <BrandMark size={22} />
      Redstring
    </div>
  );
  if (done)
    return (
      <main className="v3-login">
        <div className="v3-login-card">
          {brand}
          <div className="v3-login-ok" role="status">
            {tx("账号已创建。", "Your account is ready.")}
          </div>
          <p className="v3-login-lead" style={{ margin: 0 }}>
            {tx("新账号默认是只读成员，还没有任何环境的权限。登录后如果看不到数据，请管理员在“成员与权限”里给你授予环境。", "New accounts start as read-only members without access to any environment. If you see no data after signing in, ask an admin to grant you an environment under Members.")}
          </p>
          <Btn kind="primary" h={34} to="/login" className="v3-login-submit">
            {tx("去登录", "Sign in")}
          </Btn>
        </div>
      </main>
    );
  return (
    <main className="v3-login">
      <form className="v3-login-card" onSubmit={submit} noValidate>
        {brand}
        <h1>{tx("加入工作区", "Join the workspace")}</h1>
        <p className="v3-login-lead">{tx("输入邀请邮件里的邀请码，设置你的姓名和密码。", "Enter the code from your invitation email, then set your name and password.")}</p>
        <Field label={tx("邀请码", "Invitation code")}>
          <Input id="invite-code" mono autoComplete="one-time-code" autoFocus required value={code} onChange={(e) => setCode(e.target.value)} className={error?.field === "code" ? "invalid" : undefined} />
        </Field>
        {error?.field === "code" && (
          <p className="v3-login-error" role="alert">
            {error.text}
          </p>
        )}
        <Field label={tx("姓名", "Name")}>
          <Input id="invite-name" autoComplete="name" required value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={tx("密码", "Password")} hint={tx("至少 12 位", "At least 12 characters")}>
          <Input id="invite-password" type="password" autoComplete="new-password" required value={password} onChange={(e) => setPassword(e.target.value)} className={error?.field === "password" ? "invalid" : undefined} />
        </Field>
        <Field label={tx("确认密码", "Confirm password")}>
          <Input id="invite-confirm" type="password" autoComplete="new-password" required value={confirm} onChange={(e) => setConfirm(e.target.value)} className={error?.field === "password" ? "invalid" : undefined} />
        </Field>
        {error && error.field !== "code" && (
          <p className={error.existing ? "v3-login-note" : "v3-login-error"} role="alert">
            {error.text} {error.existing && <Link to="/login">{tx("去登录", "Sign in")}</Link>}
          </p>
        )}
        <Btn kind="primary" type="submit" h={34} loading={busy} disabled={locked || !code.trim() || !name.trim() || !password || !confirm} className="v3-login-submit">
          {locked ? tx("请稍后再试", "Try again shortly") : tx("创建账号", "Create account")}
        </Btn>
        <p className="v3-login-alt">
          {tx("已经有账号？", "Already have an account?")} <Link to="/login">{tx("登录", "Sign in")}</Link>
        </p>
        {instance && (
          <div className="v3-login-instance">
            <span className={prod ? "tag prod" : "tag"}>● {prod ? tx("生产", "Production") : tx("非生产", "Non-production")}</span>
            {instance.domain}
          </div>
        )}
      </form>
    </main>
  );
}
