import { useEffect, useState, type ReactNode } from "react";
import { useGet, useSend } from "../../api/client";
import type { ProfileView } from "../../api/types";
import { Muted } from "../../ui/core";

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
  const { data } = useGet<ProfileView>("/me/profile");
  const [form, setForm] = useState<ProfileView | null>(null);
  const save = useSend<ProfileView>("PUT", "/me/profile", ["/me"]);
  useEffect(() => {
    if (data) setForm(structuredClone(data));
  }, [data]);
  const dirty = !!data && !!form && JSON.stringify(data) !== JSON.stringify(form);
  return { form, setForm, save, dirty };
}
