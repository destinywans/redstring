import { useEffect, useState, type ReactNode } from "react";
import { ApiError, RETURN_TITLE_KEY, useGet, useSend } from "../../api/client";
import type { ProfileView } from "../../api/types";
import { Btn, Icon, Muted } from "../../ui/core";
import { useI18n } from "../../i18n";

/** Title block at the top of every settings page. */
export function SettingsHead({ title, desc, right }: { title: string; desc?: ReactNode; right?: ReactNode }) {
  return (
    <div className="set-title">
      <div className="v3-row" style={{ gap: 10 }}>
        <h1 className="grow">{title}</h1>
        {right}
      </div>
      {desc && <Muted size={12.5}>{desc}</Muted>}
    </div>
  );
}

/** Profile, notifications and account security share `/me/profile`; each page edits its own slice. */
export function useProfileForm() {
  const { data, error } = useGet<ProfileView>("/me/profile");
  const [form, setForm] = useState<ProfileView | null>(null);
  const save = useSend<ProfileView>("PUT", "/me/profile", ["/me"]);
  useEffect(() => {
    if (data) setForm(structuredClone(data));
  }, [data]);
  const dirty = !!data && !!form && JSON.stringify(data) !== JSON.stringify(form);
  return { form, setForm, save, dirty, error };
}

/** The server wants a sign-in from the last 10 minutes before passkeys or the authenticator change. */
export const isRecentLoginError = (e: unknown) => e instanceof ApiError && (e.code === "passkey_recent_login_required" || e.code === "mfa_recent_login_required");

/** Sends the person to /login and back to account security; the login page names where they return to. */
export function reloginForAccountSecurity(title: string) {
  try {
    sessionStorage.setItem(RETURN_TITLE_KEY, title);
  } catch {
    /* convenience only */
  }
  location.assign(`/login?reason=recent&next=${encodeURIComponent("/settings/account-security")}`);
}

/** Inline notice with the way forward, instead of a toast that disappears (approved design). */
export function RecentLoginBanner({ what }: { what: string }) {
  const { tx } = useI18n();
  return (
    <div className="unk-banner warn" role="status">
      <Icon name="lock" size={14} />
      <span className="grow">{tx("{w}需要最近 10 分钟内登录过。重新登录后会回到这里。", "{w} needs a sign-in from the last 10 minutes. You'll come back here after signing in.", { w: what })}</span>
      <Btn h={26} onClick={() => reloginForAccountSecurity(tx("账号安全", "Account security"))}>
        {tx("重新登录后继续", "Sign in again to continue")}
      </Btn>
    </div>
  );
}
