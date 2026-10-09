import { useEffect, useRef, useState } from "react";
import { ApiError, api } from "../../api/client";
import { Btn, Col, Icon, Muted, Row, Tile } from "../../ui/core";
import { Dialog } from "../../ui/overlays";
import { getAssertion, passkeysSupported } from "../../ui/passkey";
import { useI18n } from "../../i18n";

type Options = { challenge_id: string; options: Parameters<typeof getAssertion>[0]; expires_at: string; action_digest: string };
type Result = { status: "pending_review" | "approved"; received_approvals: number; execution_id?: string };
type Phase =
  | { kind: "waiting"; digest: string }
  | { kind: "needs_passkey" }
  | { kind: "pending_second" }
  | { kind: "approved"; execution: string }
  | { kind: "failed"; title: string; desc: string; retry: boolean };

/**
 * Passkey approval (design approved 2026-10-09): the challenge is bound to this proposal's
 * action digest. Approval takes two different approvers: the first vote is recorded
 * (202 pending_review), the second yields the execution id. The creator cannot approve.
 */
export function PasskeyApproval({ proposalId, comment, open, onClose, onDone }: { proposalId: string; comment: string; open: boolean; onClose: () => void; onDone: (r: Result) => void }) {
  const { tx } = useI18n();
  const [phase, setPhase] = useState<Phase>({ kind: "waiting", digest: "" });
  const abort = useRef<AbortController | null>(null);

  const run = async () => {
    setPhase({ kind: "waiting", digest: "" });
    if (!passkeysSupported()) {
      setPhase({ kind: "failed", title: tx("这个浏览器不支持通行密钥", "This browser doesn't support passkeys"), desc: tx("换用支持通行密钥的浏览器后再批准。", "Use a browser with passkey support to approve."), retry: false });
      return;
    }
    try {
      const o = await api<Options>("POST", `/proposals/${proposalId}/approve/options`, { body: { confirm: true } });
      setPhase({ kind: "waiting", digest: o.action_digest });
      abort.current = new AbortController();
      const credential = await getAssertion(o.options, abort.current.signal);
      const r = await api<Result>("POST", `/proposals/${proposalId}/approve`, { body: { challenge_id: o.challenge_id, credential, ...(comment.trim() ? { comment: comment.trim().replace(/\s+/g, " ").slice(0, 1000) } : {}) } });
      setPhase(r.status === "approved" && r.execution_id ? { kind: "approved", execution: r.execution_id } : { kind: "pending_second" });
      onDone(r);
    } catch (e) {
      setPhase(describe(e));
    }
  };

  const describe = (e: unknown): Phase => {
    if (e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError"))
      return { kind: "failed", title: tx("验证没有完成，提案仍待批准", "Verification didn't finish; the proposal is still awaiting approval"), desc: tx("可能取消了系统弹窗，或者超过了 5 分钟。", "The system prompt may have been cancelled, or more than 5 minutes passed."), retry: true };
    if (e instanceof ApiError) {
      if (e.code === "approval_passkey_required") return { kind: "needs_passkey" };
      if (e.code === "approval_forbidden")
        return { kind: "failed", title: tx("你不能批准这份提案", "You can't approve this proposal"), desc: tx("需要变更审批权限，且发起人不能批准自己的提案。", "Approving needs the change-approver permission, and the author cannot approve their own proposal."), retry: false };
      if (e.code === "approval_conflict")
        return { kind: "failed", title: tx("提案或验证状态已经变化", "The proposal or verification changed"), desc: tx("刷新页面看最新内容后再批准。", "Refresh to see the latest version, then approve again."), retry: false };
      if (e.code === "approval_passkey_invalid") return { kind: "failed", title: tx("验证没有通过", "Verification failed"), desc: tx("请重新验证。", "Please verify again."), retry: true };
      if (e.code === "approval_rate_limited") return { kind: "failed", title: tx("尝试次数过多", "Too many attempts"), desc: tx("请稍后再试。", "Try again later."), retry: false };
      if (e.code === "invalid_approval") return { kind: "failed", title: tx("这份提案现在不能批准", "This proposal can't be approved now"), desc: e.message, retry: false };
    }
    return { kind: "failed", title: tx("暂时无法批准", "Approval is unavailable right now"), desc: tx("稍后重试；提案仍待批准。", "Try again later; the proposal is still awaiting approval."), retry: true };
  };

  useEffect(() => {
    if (open) void run();
    return () => abort.current?.abort();
    // run once per opening
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const close = () => {
    abort.current?.abort();
    onClose();
  };
  const p = phase;
  return (
    <Dialog open={open} onClose={close} title={tx("用通行密钥批准", "Approve with a passkey")} width={420}>
      <Col gap={10} style={{ padding: 16 }}>
        {p.kind === "waiting" && (
          <>
            <Tile icon="key" size={38} />
            <b>{tx("在浏览器弹窗里完成验证", "Complete verification in the browser prompt")}</b>
            <Muted>{tx("用指纹、面容或安全密钥确认。这次验证只对这份提案有效，5 分钟内有效。", "Confirm with your fingerprint, face or security key. It applies to this proposal only and expires in 5 minutes.")}</Muted>
            <code className="approve-bind">
              {tx("提案", "Proposal")} {proposalId}
              {p.digest && (
                <>
                  <br />
                  {tx("动作摘要", "Action digest")} {p.digest.slice(0, 4)}…{p.digest.slice(-4)}
                </>
              )}
            </code>
            <Row gap={8}>
              <span className="v3-spinner spin" />
              <Muted>{tx("等待验证…", "Waiting for verification…")}</Muted>
              <span style={{ flex: 1 }} />
              <Btn kind="ghost" h={26} onClick={close}>
                {tx("取消", "Cancel")}
              </Btn>
            </Row>
          </>
        )}
        {p.kind === "needs_passkey" && (
          <>
            <Tile icon="alert" size={38} />
            <b>{tx("你还没有可用的通行密钥", "You don't have a passkey yet")}</b>
            <Muted>{tx("批准生产变更必须用通行密钥确认。先到“账号安全”登记一个，再回来批准，这份提案会保持待批准。", "Approving production changes requires a passkey. Register one under Account security, then come back; the proposal stays pending.")}</Muted>
            <Row gap={8}>
              <Btn kind="primary" h={28} to="/settings/account-security">
                {tx("去账号安全登记", "Register in Account security")}
              </Btn>
              <Btn kind="ghost" h={28} onClick={close}>
                {tx("稍后", "Later")}
              </Btn>
            </Row>
          </>
        )}
        {p.kind === "pending_second" && (
          <>
            <Tile icon="check" size={38} />
            <b>{tx("已记录你的批准，还需要 1 位审批人", "Your approval is recorded; one more approver is needed")}</b>
            <Muted>{tx("这类变更需要两位不同的审批人。第二位批准后，执行器开始执行。", "This kind of change needs two different approvers. The executor starts after the second approval.")}</Muted>
            <Row gap={8}>
              <Btn h={28} onClick={close}>
                {tx("知道了", "Got it")}
              </Btn>
            </Row>
          </>
        )}
        {p.kind === "approved" && (
          <>
            <Tile icon="check" size={38} />
            <b>{tx("已批准，执行器开始执行", "Approved; the executor is starting")}</b>
            <Row gap={8}>
              <Btn kind="primary" h={28} to={`/executions/${p.execution}`}>
                {tx("查看执行进度", "View execution")}
              </Btn>
            </Row>
          </>
        )}
        {p.kind === "failed" && (
          <>
            <Icon name="alert" size={18} color="var(--warn)" />
            <b>{p.title}</b>
            <Muted>{p.desc}</Muted>
            <Row gap={8}>
              {p.retry && (
                <Btn h={28} onClick={() => void run()}>
                  {tx("重新验证", "Verify again")}
                </Btn>
              )}
              <Btn kind="ghost" h={28} onClick={close}>
                {tx("关闭", "Close")}
              </Btn>
            </Row>
          </>
        )}
      </Col>
    </Dialog>
  );
}
