import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Btn, Field, Input } from "../../ui/core";
import { BrandMark } from "../../ui/BrandMark";
import { useI18n } from "../../i18n";
import { RETURN_TITLE_KEY, SIGNED_IN_KEY } from "../../api/client";
import type { InstanceInfo } from "../../api/types";
import "./auth.css";

/** Only same-origin app paths may be a post-login destination (no open redirects). */
function safeNext(raw: string | null) {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/login")) return "/";
  return raw;
}

export default function LoginPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const next = safeNext(params.get("next"));
  const expired = params.get("expired") === "1";
  // Passkey / authenticator changes need a fresh sign-in; the page that sent us here is named.
  const recent = params.get("reason") === "recent";
  const [instance, setInstance] = useState<InstanceInfo | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [lockedUntil, setLockedUntil] = useState(0);
  const [busy, setBusy] = useState(false);
  const [, tick] = useState(0);
  const returnTitle = (() => {
    try {
      return sessionStorage.getItem(RETURN_TITLE_KEY) ?? "";
    } catch {
      return "";
    }
  })();

  useEffect(() => {
    document.title = tx("登录", "Sign in") + " · Redstring";
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
    setBusy(true);
    setError(null);
    try {
      const r = await fetch("/api/v1/auth/login", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: email.trim(), password }),
      });
      if (r.ok) {
        try {
          sessionStorage.removeItem(RETURN_TITLE_KEY);
          localStorage.setItem(SIGNED_IN_KEY, "1");
        } catch {
          /* convenience only */
        }
        nav(next, { replace: true });
        return;
      }
      if (r.status === 429) {
        setLockedUntil(Date.now() + 60_000);
        setError(tx("尝试次数过多，请 1 分钟后再试。", "Too many attempts. Try again in a minute."));
      } else if (r.status === 401 || r.status === 422) {
        setError(tx("邮箱或密码不正确。", "Incorrect email or password."));
      } else {
        setError(tx("暂时无法登录，请稍后再试。", "Sign-in is unavailable right now. Try again shortly."));
      }
    } catch {
      setError(tx("连不上服务器，请检查网络后重试。", "Can't reach the server. Check your connection and try again."));
    } finally {
      setBusy(false);
    }
  };

  const prod = instance?.tier === "production";
  return (
    <main className="v3-login">
      <form className="v3-login-card" onSubmit={submit} noValidate>
        <div className="v3-login-brand">
          <BrandMark size={22} />
          Redstring
        </div>
        <h1>{tx("登录", "Sign in")}</h1>
        {recent && !expired && (
          <div className="v3-login-note" role="status">
            {returnTitle
              ? tx("为了安全，请重新登录。登录后回到「{t}」。", "For security, sign in again. You'll return to “{t}”.", { t: returnTitle })
              : tx("为了安全，请重新登录。", "For security, please sign in again.")}
          </div>
        )}
        {expired && (
          <div className="v3-login-note" role="status">
            {returnTitle
              ? tx("登录已过期，请重新登录。登录后回到「{t}」。", "Your session expired. Sign in to return to “{t}”.", { t: returnTitle })
              : tx("登录已过期，请重新登录。", "Your session expired. Please sign in again.")}
          </div>
        )}
        <Field label={tx("工作邮箱", "Work email")}>
          <Input id="login-email" type="email" autoComplete="username" autoFocus required value={email} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <Field label={tx("密码", "Password")}>
          <Input id="login-password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} className={error && !locked ? "invalid" : undefined} />
        </Field>
        {error && (
          <p className="v3-login-error" role="alert">
            {error}
          </p>
        )}
        <Btn kind="primary" type="submit" h={34} loading={busy} disabled={locked || !email.trim() || !password} className="v3-login-submit">
          {locked ? tx("请稍后再试", "Try again shortly") : tx("登录", "Sign in")}
        </Btn>
        <p className="v3-login-alt">
          {tx("收到邀请？", "Got an invitation?")} <Link to="/invite">{tx("用邀请码加入", "Join with your code")}</Link>
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
