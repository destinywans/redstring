import { useState } from "react";
import { ApiError, api, useGet } from "../../api/client";
import { Badge, Btn, Col, Muted, Panel, Strong, Tile } from "../../ui/core";
import { confirmAction, openForm } from "../../ui/demo";
import { toast } from "../../ui/overlays";
import { createCredential, getAssertion, passkeysSupported } from "../../ui/passkey";
import { useI18n } from "../../i18n";

type Credential = { id: string; label?: string; created_at: string; last_used_at?: string; backup_eligible: boolean; backup_state: boolean };
type Ceremony = { id: string; options: unknown; expires_at: string };

/**
 * Your own passkeys (WebAuthn). They are what proposal approvals verify, so this works on its own
 * even when the rest of the profile has no identity source. Adding and removing need a recent sign-in.
 */
export function PasskeysPanel() {
  const { tx, fmt } = useI18n();
  const { data, error, refetch } = useGet<{ credentials: Credential[] }>("/me/passkeys");
  const [busy, setBusy] = useState<string | null>(null);

  const fail = (e: unknown) => {
    if (e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError")) return toast(tx("验证没有完成，未做任何更改", "Verification didn't finish; nothing changed"), { tone: "danger" });
    if (e instanceof DOMException && e.name === "InvalidStateError") return toast(tx("这台设备上已经有你的通行密钥", "This device already has one of your passkeys"), { tone: "danger" });
    if (e instanceof ApiError && (e.status === 401 || e.code.includes("recent"))) return toast(tx("需要最近登录过才能更改通行密钥，请重新登录后再试", "Changing passkeys needs a recent sign-in; sign in again and retry"), { tone: "danger" });
    toast(e instanceof ApiError ? e.message : tx("操作失败，请稍后重试", "That didn't work; try again later"), { tone: "danger" });
  };

  const add = async () => {
    if (!passkeysSupported()) return toast(tx("这个浏览器不支持通行密钥", "This browser doesn't support passkeys"), { tone: "danger" });
    const v = await openForm({
      title: tx("添加通行密钥", "Add a passkey"),
      desc: tx("浏览器会弹出系统对话框，用指纹、面容或安全密钥确认。批准生产变更时会用到它。", "Your browser shows a system prompt to confirm with fingerprint, face or a security key. Approving production changes uses it."),
      fields: [{ key: "label", label: tx("名称（可选）", "Name (optional)"), initial: "" }],
      submit: tx("继续", "Continue"),
    });
    if (!v) return;
    setBusy("add");
    try {
      const o = await api<Ceremony>("POST", "/me/passkeys/registration/options", { body: {} });
      const credential = await createCredential(o.options as Parameters<typeof createCredential>[0]);
      await api("POST", "/me/passkeys/registration/finish", { body: { challenge_id: o.id, credential, ...(v.label.trim() ? { label: v.label.trim().slice(0, 64) } : {}) } });
      toast(tx("通行密钥已添加", "Passkey added"));
      void refetch();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(null);
    }
  };

  const remove = async (c: Credential) => {
    const ok = await confirmAction({
      title: tx("删除这个通行密钥？", "Remove this passkey?"),
      desc: tx("需要用这个通行密钥确认一次。删除后不能再用它批准提案。", "Confirm once with this passkey. After removal it can't approve proposals."),
      confirm: tx("删除", "Remove"),
      danger: true,
    });
    if (!ok) return;
    setBusy(c.id);
    try {
      const o = await api<Ceremony>("POST", "/me/passkeys/deletion/options", { body: { credential_id: c.id } });
      const credential = await getAssertion(o.options as Parameters<typeof getAssertion>[0]);
      await api("POST", "/me/passkeys/deletion/finish", { body: { challenge_id: o.id, credential } });
      toast(tx("通行密钥已删除", "Passkey removed"));
      void refetch();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <Panel
      title={tx("通行密钥", "Passkeys")}
      right={
        <Btn h={26} kind="secondary" icon="plus" loading={busy === "add"} disabled={!!error} onClick={() => void add()}>
          {tx("添加", "Add")}
        </Btn>
      }
    >
      {error ? (
        <Muted>{error instanceof ApiError && error.status === 404 ? tx("这个实例没有启用通行密钥。", "Passkeys are not enabled on this instance.") : tx("暂时读不到你的通行密钥。", "Your passkeys can't be read right now.")}</Muted>
      ) : !data ? (
        <Muted>…</Muted>
      ) : data.credentials.length === 0 ? (
        <Muted>{tx("还没有通行密钥。批准生产变更前需要先添加一个。", "No passkeys yet. Add one before approving production changes.")}</Muted>
      ) : (
        <ul className="set-list">
          {data.credentials.map((c) => (
            <li key={c.id}>
              <Tile icon="key" tone="accent" size={30} />
              <Col gap={0} grow>
                <Strong>{c.label || tx("通行密钥", "Passkey")}</Strong>
                <Muted size={11.5}>
                  {tx("添加于 {a}", "Added {a}", { a: fmt.dateTime(c.created_at) })}
                  {c.last_used_at ? ` · ${tx("最近使用 {u}", "last used {u}", { u: fmt.ago(c.last_used_at) })}` : ""}
                </Muted>
              </Col>
              {c.backup_state && <Badge>{tx("已同步", "Synced")}</Badge>}
              <Btn h={26} kind="ghost" loading={busy === c.id} onClick={() => void remove(c)}>
                {tx("删除", "Remove")}
              </Btn>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}
