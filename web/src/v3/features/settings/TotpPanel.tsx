import { useEffect, useState } from "react";
import { ApiError, api, useGet } from "../../api/client";
import { Badge, Btn, Col, Input, Muted, Panel, Row, Spacer, Strong } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { copyText, downloadFile } from "../../ui/demo";
import { QrCode } from "../../ui/QrCode";
import { useI18n } from "../../i18n";
import { RecentLoginBanner, isRecentLoginError } from "./common";

type Status = { state: "none" | "pending" | "expired" | "active"; recovery_remaining: number };
type Enrollment = { secret: string; otpauth_url: string; expires_at: string };

/**
 * Authenticator app (TOTP), approved design: status → recent sign-in → scan → code → recovery codes (shown once).
 * The secret and recovery codes come back exactly once; nothing here stores them.
 */
export function TotpPanel() {
  const { tx } = useI18n();
  const { data, error, refetch } = useGet<Status>("/me/mfa/totp", undefined, { retry: false });
  const [open, setOpen] = useState(false);
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [busy, setBusy] = useState(false);

  const fail = (e: unknown) => {
    if (isRecentLoginError(e)) {
      setNeedsLogin(true);
      return;
    }
    const msg =
      e instanceof ApiError
        ? ({ mfa_invalid_code: tx("动态码不对或已经用过，请等下一个码再试。", "That code is wrong or already used; wait for the next one."), mfa_rate_limited: tx("尝试太频繁，请稍后再试。", "Too many tries; wait a moment."), mfa_conflict: tx("验证器状态刚刚变化，已重新读取。", "The authenticator state just changed; reloaded.") } as Record<string, string>)[e.code] ?? e.message
        : tx("操作失败，请稍后重试", "That didn't work; try again later");
    toast(msg, { tone: "danger" });
    if (e instanceof ApiError && e.code === "mfa_conflict") void refetch();
  };

  const start = async () => {
    setBusy(true);
    try {
      // Empty body; the pending seed is bound to this session and expires in 10 minutes.
      const r = await api<Enrollment>("POST", "/me/mfa/totp/enrollment");
      setEnrollment(r);
      setCodes(null);
      setOpen(true);
      void refetch();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };
  const abandon = async () => {
    try {
      await api("DELETE", "/me/mfa/totp/enrollment");
      setEnrollment(null);
      setOpen(false);
      void refetch();
    } catch (e) {
      fail(e);
    }
  };

  if (error && !data)
    return (
      <Panel title={tx("身份验证器", "Authenticator app")}>
        <Muted>{error instanceof ApiError && error.status === 404 ? tx("这个实例没有启用身份验证器。", "Authenticator apps aren't enabled on this instance.") : tx("暂时读不到身份验证器状态。", "The authenticator status can't be read right now.")}</Muted>
      </Panel>
    );
  const state = data?.state;
  return (
    <Panel
      title={tx("身份验证器", "Authenticator app")}
      right={
        !data ? null : state === "active" ? (
          <Btn h={26} kind="ghost" disabled={needsLogin} onClick={() => setRemoving(true)}>
            {tx("解除绑定", "Remove")}
          </Btn>
        ) : state === "pending" ? (
          <Row gap={6}>
            <Btn h={26} kind="ghost" onClick={() => void abandon()}>{tx("放弃绑定", "Discard")}</Btn>
            <Btn h={26} kind="primary" onClick={() => setOpen(true)}>{tx("继续绑定", "Continue")}</Btn>
          </Row>
        ) : (
          <Btn h={26} kind="primary" loading={busy} disabled={needsLogin} onClick={() => void start()}>
            {state === "expired" ? tx("重新开始", "Start again") : tx("绑定", "Set up")}
          </Btn>
        )
      }
    >
      <Col gap={10}>
        {needsLogin && <RecentLoginBanner what={tx("更改身份验证器", "Changing the authenticator")} />}
        <Row gap={8}>
          <Strong>{tx("动态码", "One-time codes")}</Strong>
          {!data ? (
            <Muted>…</Muted>
          ) : state === "active" ? (
            <Badge tone="ok">{tx("已绑定", "On")}</Badge>
          ) : state === "pending" ? (
            <Badge tone="warn">{tx("待完成", "Pending")}</Badge>
          ) : state === "expired" ? (
            <Badge tone="warn">{tx("已过期", "Expired")}</Badge>
          ) : (
            <Badge>{tx("未绑定", "Off")}</Badge>
          )}
        </Row>
        <Muted size={11.5}>
          {state === "active"
            ? tx("恢复码剩余 {n} 个。解除绑定需要输入一次动态码或恢复码。", "{n} recovery codes left. Removing needs one code or recovery code.", { n: data?.recovery_remaining ?? 0 })
            : state === "pending"
              ? tx("上次的绑定还没完成。已经扫过码就继续输入动态码；没扫过就放弃后重新开始。", "Last setup didn't finish. If you already scanned the code, continue; otherwise discard and start again.")
              : tx("用验证器 App 生成 6 位动态码，用于敏感操作的二次确认。", "Use an authenticator app's 6-digit codes to confirm sensitive actions.")}
        </Muted>
      </Col>
      <EnrollDialog
        open={open}
        enrollment={enrollment}
        codes={codes}
        onCodes={(c) => {
          setCodes(c);
          void refetch();
        }}
        onFail={fail}
        onAbandon={() => void abandon()}
        onClose={() => {
          setOpen(false);
          if (codes) {
            setEnrollment(null);
            setCodes(null);
          }
        }}
      />
      <RemoveDialog open={removing} onClose={() => setRemoving(false)} onDone={() => { setRemoving(false); void refetch(); toast(tx("身份验证器已解除绑定", "Authenticator removed")); }} onFail={fail} />
    </Panel>
  );
}

function useCountdown(until: string | undefined) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!until) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [until]);
  return until ? Math.max(0, Date.parse(until) - now) : 0;
}

function EnrollDialog({ open, enrollment, codes, onCodes, onFail, onAbandon, onClose }: { open: boolean; enrollment: Enrollment | null; codes: string[] | null; onCodes: (c: string[]) => void; onFail: (e: unknown) => void; onAbandon: () => void; onClose: () => void }) {
  const { tx } = useI18n();
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const left = useCountdown(enrollment?.expires_at);
  useEffect(() => {
    if (open) {
      setCode("");
      setSaved(false);
    }
  }, [open]);
  const confirm = async () => {
    setBusy(true);
    try {
      const r = await api<{ enabled: boolean; recovery_codes: string[] }>("POST", "/me/mfa/totp/confirm", { body: { code: code.trim() } });
      onCodes(r.recovery_codes);
    } catch (e) {
      onFail(e);
    } finally {
      setBusy(false);
    }
  };
  const step = codes ? 4 : 2;
  const mm = Math.floor(left / 60000);
  const ss = Math.floor((left % 60000) / 1000);
  return (
    <Dialog
      open={open}
      onClose={codes && !saved ? () => undefined : onClose}
      width={540}
      title={codes ? tx("已绑定。请保存恢复码", "Done. Save your recovery codes") : tx("用验证器 App 扫描二维码", "Scan with your authenticator app")}
      footer={
        codes ? (
          <>
            <Btn onClick={() => copyText(codes.join("\n"), tx("恢复码已复制", "Recovery codes copied"))}>{tx("复制", "Copy")}</Btn>
            <Btn onClick={() => { downloadFile("redstring-recovery-codes.txt", codes.join("\n") + "\n", "text/plain"); setSaved(true); }}>{tx("下载 .txt", "Download .txt")}</Btn>
            <Spacer />
            <Btn kind="primary" onClick={() => { setSaved(true); onClose(); }}>{tx("我已保存", "I've saved them")}</Btn>
          </>
        ) : (
          <>
            <Btn kind="ghost" onClick={onAbandon}>{tx("放弃绑定", "Discard")}</Btn>
            <Spacer />
            <Btn kind="primary" loading={busy} disabled={!/^\d{6}$/.test(code.trim())} onClick={() => void confirm()}>{tx("验证并绑定", "Verify and turn on")}</Btn>
          </>
        )
      }
    >
      <Col gap={14}>
        <Row gap={6} wrap>
          {[tx("确认身份", "Confirm identity"), tx("扫码", "Scan"), tx("输入动态码", "Enter code"), tx("保存恢复码", "Save recovery codes")].map((l, i) => (
            <Badge key={l} tone={i + 1 < step || (step === 4 && i === 2) ? "ok" : i + 1 === step || (step === 2 && i === 2) ? "accent" : "neutral"}>
              {i + 1} · {l}
            </Badge>
          ))}
        </Row>
        {codes ? (
          <>
            <Muted>{tx("手机丢失时用恢复码代替动态码，每个只能用一次。关闭后不会再显示。", "Use a recovery code instead of a one-time code if you lose your phone. Each works once and won't be shown again.")}</Muted>
            <div className="totp-codes">
              {codes.map((c) => (
                <span key={c} className="mono">{c}</span>
              ))}
            </div>
          </>
        ) : enrollment ? (
          <>
            <Row gap={16} align="flex-start" wrap>
              <QrCode value={enrollment.otpauth_url} label={tx("身份验证器二维码", "Authenticator QR code")} />
              <Col gap={6} style={{ flex: "1 1 200px", minWidth: 0 }}>
                <Muted size={11.5}>{tx("扫不了码时手动输入密钥：", "Can't scan? Enter this key instead:")}</Muted>
                <code className="totp-secret">{enrollment.secret.replace(/(.{4})/g, "$1 ").trim()}</code>
                <Btn h={26} style={{ alignSelf: "flex-start" }} onClick={() => copyText(enrollment.secret, tx("密钥已复制", "Key copied"))}>{tx("复制密钥", "Copy key")}</Btn>
                <Muted size={11.5}>
                  {left > 0
                    ? tx("{m} 分 {s} 秒内完成，超时需重新开始。关掉窗口会保留待完成状态。", "Finish within {m}:{s}; after that, start again. Closing keeps it pending.", { m: mm, s: String(ss).padStart(2, "0") })
                    : tx("已超时，请放弃后重新开始。", "Timed out; discard and start again.")}
                </Muted>
              </Col>
            </Row>
          </>
        ) : (
          <Muted>{tx("如果已经扫过码，直接输入 App 上的 6 位动态码；没扫过请放弃后重新开始。", "If you already scanned the code, enter the 6-digit code; otherwise discard and start again.")}</Muted>
        )}
        {!codes && (
          <label className="v3-col" style={{ gap: 6, fontSize: "var(--fs-sub)", color: "var(--muted)" }}>
            {tx("输入 App 上的 6 位动态码", "Enter the 6-digit code from the app")}
            <Input id="totp-code" mono inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} width={160} />
          </label>
        )}
      </Col>
    </Dialog>
  );
}

function RemoveDialog({ open, onClose, onDone, onFail }: { open: boolean; onClose: () => void; onDone: () => void; onFail: (e: unknown) => void }) {
  const { tx } = useI18n();
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) setCode("");
  }, [open]);
  const remove = async () => {
    setBusy(true);
    try {
      await api("DELETE", "/me/mfa/totp", { body: { code: code.trim() } });
      onDone();
    } catch (e) {
      onFail(e);
      if (isRecentLoginError(e)) onClose();
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={tx("解除身份验证器绑定？", "Remove the authenticator?")}
      footer={
        <>
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="danger" loading={busy} disabled={!code.trim()} onClick={() => void remove()}>{tx("解除绑定", "Remove")}</Btn>
        </>
      }
    >
      <Col gap={10}>
        <Muted>{tx("输入一次 App 上的动态码或一个恢复码确认。解除后未使用的恢复码全部作废。", "Confirm with a one-time code or a recovery code. Unused recovery codes stop working.")}</Muted>
        <Input id="totp-remove-code" mono autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} />
      </Col>
    </Dialog>
  );
}

