import type { ReactNode } from "react";
import { useGet } from "../../api/client";
import { ALERT_RULE_KINDS, ROLLBACK_KIND, type Proposal, type Tone } from "../../api/types";
import { Avatar, Badge, Btn, Col, Icon, KV, Muted, Row, Spacer } from "../../ui/core";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";

type Approver = { name: string; at: string };
/** Execution states after which nothing more happens on its own. */
const DONE = ["succeeded", "confirmed", "verified", "enabled", "disabled"];

/**
 * The review page's right column once a proposal is decided (approved design D37, option A): the outcome first, then
 * the way to the execution; what was written at submission (checklist and requirement, frozen into the digest) is
 * folded away and read-only. Approvers come from `decision` when the server sends it, else from a rollback's or alert
 * rule's execution; without either they are in the audit log. Display only: the approval flow doesn't change.
 */
export function ReviewDecision({ p, execBox }: { p: Proposal; execBox: ReactNode }) {
  const { tx } = useI18n();
  const when = useWhen();
  const state = p.decision?.state ?? (p.status === "rejected" ? "rejected" : (p.status as string) === "expired" ? "expired" : "approved");
  // Rollback and alert rule executions list who approved; the others don't (yet).
  const execApprovers = !p.decision && state === "approved" && p.execution && (p.action_kind === ROLLBACK_KIND || ALERT_RULE_KINDS.includes(p.action_kind ?? ""));
  const { data: exec } = useGet<{ approvers?: { name: string; verified_at: string }[] }>(execApprovers ? `/executions/${p.execution!.execution_id}` : null);
  const approvers: Approver[] | null = p.decision?.approvers?.length ? p.decision.approvers.map((a) => ({ name: a.name, at: a.at })) : exec?.approvers?.length ? exec.approvers.map((a) => ({ name: a.name, at: a.verified_at })) : null;
  const finished = !!p.execution?.status && DONE.includes(p.execution.status);
  const repropose = (
    <Btn h={26} icon="branch" style={{ alignSelf: "flex-start" }} to={`/canvas/${p.investigation_id}`}>
      {tx("在调查中重新提案", "Propose again in the investigation")}
    </Btn>
  );
  const fold = (
    <details className="rv-fold">
      <summary>{tx("提交时的核对清单与审批要求（只读）", "Checklist and requirement at submission (read-only)")}</summary>
      <Col gap={8}>
        {/* Ticked as submitted; read-only, so a static tick rather than a disabled checkbox. */}
        <ul className="checklist">
          {p.checklist.map((c) => (
            <li key={c} className="rv-ck">
              <span className="box" aria-hidden="true">
                <Icon name="check" size={10} />
              </span>
              <span>{c}</span>
            </li>
          ))}
        </ul>
        {p.requirement && <span className="rv-frozen">{p.requirement}</span>}
        <Muted size={12}>{tx("这是提交时写下并计入摘要的原文，描述的是提交那一刻，不是现在的状态。", "This is the text written at submission and counted in the digest; it describes that moment, not the current state.")}</Muted>
      </Col>
    </details>
  );
  const head = (chip: ReactNode) => (
    <Row gap={8}>
      <Icon name="shield" size={16} color="var(--accent-text)" />
      <h2 style={{ fontSize: "var(--fs-lg)", fontWeight: 600 }}>{tx("批准结果", "Decision")}</h2>
      <Spacer />
      {chip}
    </Row>
  );
  const banner = (tone: "ok" | "danger" | "mute", icon: "check" | "x" | "clock", title: string, sub: string) => (
    <div className={`rv-banner ${tone}`}>
      <Icon name={icon} size={14} style={{ marginTop: 2 }} />
      <span className="t">
        <b>{title}</b>
        <span>{sub}</span>
      </span>
    </div>
  );

  if (state === "rejected") {
    const at = p.decision?.rejected_by?.at ?? p.decision?.decided_at ?? null;
    return (
      <Col gap={14} style={{ padding: 16 }}>
        {head(<Badge tone="danger">{tx("已拒绝", "Rejected")}</Badge>)}
        {banner("danger", "x", tx("审批人拒绝了这个提案", "An approver rejected this proposal"), tx("没有执行，也不会执行。拒绝说明在下方讨论里。", "Nothing ran and nothing will. The reason is in the discussion below."))}
        {at && (
          <KV k={tx("拒绝于", "Rejected")} kw={64}>
            {when.at(at)}
          </KV>
        )}
        {repropose}
        {fold}
      </Col>
    );
  }
  if (state === "expired")
    return (
      <Col gap={14} style={{ padding: 16 }}>
        {head(<Badge>{tx("已过期", "Expired")}</Badge>)}
        {banner("mute", "clock", tx("审批期限内没有凑齐两票", "Two approvals weren't in before the deadline"), tx("没有执行。目标可能已经变化，需要的话重新提案，会按当时的内容重新核对。", "Nothing ran. The target may have changed; propose again if needed and it is checked against what is current then."))}
        {repropose}
      </Col>
    );

  const count = approvers?.length ?? 2;
  const tone: Tone = "ok";
  return (
    <Col gap={14} style={{ padding: 16 }}>
      {head(<Badge tone={tone}>{tx("已批准 {n}/{n}", "Approved {n}/{n}", { n: count })}</Badge>)}
      {finished || !p.execution
        ? banner("ok", "check", tx("两人已用通行密钥批准，执行器已执行", "Two people approved with passkeys and the executor ran it"), approvers ? tx("不需要再操作。之后的状态看执行记录。", "Nothing more to do. Follow the execution record from here.") : tx("批准人记录在审计日志里。", "The approvers are recorded in the audit log."))
        : // Not covered by the board: approved while the execution is still running or didn't succeed — only say what is true.
          banner("ok", "check", tx("两人已用通行密钥批准", "Two people approved with passkeys"), approvers ? tx("执行状态看下方执行记录。", "See the execution below for its state.") : tx("批准人记录在审计日志里；执行状态看下方执行记录。", "The approvers are recorded in the audit log; see the execution below for its state."))}
      {approvers && (
        <Col gap={8}>
          <Muted size={12}>{tx("批准人", "Approvers")}</Muted>
          {approvers.map((a) => (
            <Row key={a.name + a.at} gap={8}>
              <Avatar name={a.name} size={22} />
              <span style={{ fontSize: "var(--fs-sm)" }} className="grow">
                {a.name}
              </span>
              <Muted size={12}>{tx("通行密钥 · {t}", "Passkey · {t}", { t: when.short(a.at) ?? "—" })}</Muted>
            </Row>
          ))}
        </Col>
      )}
      {execBox}
      {!approvers && (
        <Btn h={26} icon="shield" style={{ alignSelf: "flex-start" }} to="/settings/audit">
          {tx("在审计日志中查看", "View in the audit log")}
        </Btn>
      )}
      {fold}
    </Col>
  );
}
