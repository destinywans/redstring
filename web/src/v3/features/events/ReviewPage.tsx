import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, api, useGet } from "../../api/client";
import type { Me, Proposal, Tone } from "../../api/types";
import { AiAvatar, Avatar, Badge, Btn, Card, CardHead, Col, Icon, IconBtn, KV, Muted, Row, Skeleton, Spacer, Textarea, type IconName } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Sparkline } from "../../charts";
import { useI18n } from "../../i18n";
import { PasskeyApproval } from "./PasskeyApproval";
import { RollbackReviewCards } from "./RollbackReview";
import { AlertRuleReviewCards } from "./AlertRuleReview";
import { usePermission, Gated } from "../../ui/permission";
import "./events.css";

export default function ReviewPage() {
  const { id = "" } = useParams();
  const { tx, fmt } = useI18n();
  const nav = useNavigate();
  const can = usePermission();
  const { data: me } = useGet<Me>("/me");
  // After approval, poll briefly until the executor records its status and the upstream ID.
  const { data: p, refetch } = useGet<Proposal>(id ? `/proposals/${id}` : null, undefined, {
    refetchInterval: (q) => (q.state.data?.execution && !q.state.data.execution.status ? 3000 : false),
  });
  const [approving, setApproving] = useState(false);
  const [checks, setChecks] = useState<Set<number>>(new Set());
  const [note, setNote] = useState("");
  const [reply, setReply] = useState("");
  const [thread, setThread] = useState<Proposal["discussion"]>([]);
  const [rejecting, setRejecting] = useState(false);
  if (!p)
    return (
      <Page crumbs={[tx("事件收件箱", "Inbox"), "…"]}>
        <div className="v3-content">
          <Skeleton h={40} />
          <Skeleton h={300} />
        </div>
      </Page>
    );
  const risk = { low: ["ok", tx("低风险", "Low risk")], medium: ["warn", tx("中风险", "Medium risk")], high: ["danger", tx("高风险", "High risk")] } as const;
  const all = checks.size === p.checklist.length;
  const approve = () => setApproving(true);
  // Approving and rejecting both need the approve grant; "conditional" still goes through the passkey ceremony.
  const decideBlocked = can(["proposals", "approve"]);
  // The API sends the proposer's user ID; show a name when it's the viewer, otherwise a short ID (PA-74).
  const isMine = !!me && (p.proposed_by === me.id || p.proposed_by === me.name);
  const idLike = /^[0-9a-f-]{16,}$/i.test(p.proposed_by);
  const proposer = isMine ? me!.name : p.proposed_by_name || (idLike ? tx("用户 {id}", "User {id}", { id: `${p.proposed_by.slice(0, 6)}…` }) : p.proposed_by);
  const when = (t: string) => (/^\d{4}-\d{2}-\d{2}T/.test(t) ? fmt.dateTime(t) : t);
  const [statusTone, statusLabel] = ((): [Tone, string] => {
    switch (p.status) {
      case "pending":
        return !decideBlocked && !isMine ? ["accent", tx("待你复核", "Awaiting your review")] : ["neutral", tx("待审批", "Awaiting approval")];
      case "approved":
        return ["ok", tx("已批准", "Approved")];
      case "rejected":
        return ["danger", tx("已拒绝", "Rejected")];
      case "executed":
        return ["ok", tx("已执行", "Executed")];
      default:
        return (p.status as string) === "expired" ? ["neutral", tx("已过期", "Expired")] : ["neutral", p.status];
    }
  })();
  // Executor ledger states (silence, rollback, alert rule) in words; terminal ones get their own tone.
  const execStatus = (s: string): [Tone, string] => {
    if (s === "enabled") return ["ok", tx("已生效", "In effect")];
    if (s === "disabled") return ["ok", tx("已停用", "Disabled")];
    if (["succeeded", "confirmed", "verified"].includes(s)) return ["ok", p.rollback ? tx("已恢复", "Recovered") : tx("已完成", "Completed")];
    if (s === "failed") return ["danger", tx("失败", "Failed")];
    if (s === "needs_attention") return ["warn", tx("需要人工处理", "Needs attention")];
    return ["accent", tx("执行中", "Executing")];
  };
  return (
    <Page
      crumbs={[tx("事件收件箱", "Inbox"), p.title]}
      actions={
        <>
          <Badge tone={statusTone}>{statusLabel}</Badge>
          <Btn kind="ghost" icon="branch" to={`/canvas/${p.investigation_id}`}>
            {tx("在调查中查看", "View in investigation")}
          </Btn>
        </>
      }
    >
      <div className="review-layout">
        <Col gap={16} style={{ flex: "1 1 0", minWidth: 0, maxWidth: 760 }} className="stagger">
          <Col gap={6}>
            <h1 style={{ fontSize: 19, fontWeight: 600, letterSpacing: "-0.01em" }}>{p.title}</h1>
            <Row gap={8} wrap style={{ fontSize: "var(--fs-sm)" }}>
              {/* Rollbacks are always proposed by a person (the AI only suggests). */}
              {p.rollback || API_MODE !== "mock" ? <Avatar name={proposer} size={18} /> : <AiAvatar size={18} />}
              <Muted size={12.5}>
                {proposer} · {when(p.proposed_at)}
              </Muted>
              <Badge tone={risk[p.risk][0]}>{risk[p.risk][1]}</Badge>
              {/* A rollback's or alert rule's impact is a sentence; it gets its own card below. */}
              {!p.rollback && !p.alert_rule && <Badge>{p.impact}</Badge>}
              {p.reversible && <Badge tone="ok">{tx("可回退", "Reversible")}</Badge>}
            </Row>
          </Col>
          {p.rollback ? (
            <RollbackReviewCards p={p} rb={p.rollback} />
          ) : p.alert_rule ? (
            <AlertRuleReviewCards p={p} ar={p.alert_rule} />
          ) : (
          <Card>
            <CardHead title={tx("变更内容", "Change")} icon="terminal" />
            <Col gap={12} style={{ padding: "14px 16px" }}>
              <KV k={tx("目标", "Target")}>{p.target}</KV>
              <div className="review-diff">
                <div className="path">{p.diff.path}</div>
                {p.diff.lines.map((l) => (
                  <div key={l} className={l.startsWith("-") ? "del" : l.startsWith("+") ? "add" : ""}>
                    {l}
                  </div>
                ))}
              </div>
              <KV k={tx("执行方式", "Method")}>{p.method}</KV>
            </Col>
          </Card>
          )}
          <Card>
            <CardHead title={tx("依据", "Evidence")} count={p.evidence.length} right={<Btn kind="ghost" h={24} iconRight="right" to={`/canvas/${p.investigation_id}`}>{tx("查看推理过程", "See reasoning")}</Btn>} />
            <ul>
              {p.evidence.map((e) => (
                <li key={e.n} className="review-ev">
                  <span className="n">{e.n}</span>
                  <Icon name={e.icon as IconName} size={15} color="var(--muted)" />
                  <Col gap={0} grow>
                    <span style={{ fontWeight: 500 }}>{e.title}</span>
                    <Muted>{e.desc}</Muted>
                  </Col>
                  {e.series && <Sparkline values={e.series} width={72} height={20} color="var(--danger)" />}
                </li>
              ))}
            </ul>
          </Card>
          {!p.rollback && !p.alert_rule && (
          <Card>
            <CardHead title={tx("恢复验证 · 执行后自动观测 180 秒", "Verification · 180 s automatic watch")} icon="clock" />
            <ul style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 9 }}>
              {p.verification.map((v) => (
                <li key={v} className="v3-row" style={{ gap: 10, fontSize: "var(--fs-sm)", alignItems: "flex-start" }}>
                  <Icon name="shield" size={15} color="var(--muted)" style={{ marginTop: 1 }} />
                  {v}
                </li>
              ))}
              <li style={{ paddingLeft: 25 }}>
                <Muted>{p.verification_note}</Muted>
              </li>
            </ul>
          </Card>
          )}
        </Col>
        <Card className="approve-card">
          <Col gap={14} style={{ padding: 16 }}>
            <Row gap={8}>
              <Icon name="shield" size={16} color="var(--accent-text)" />
              <h2 style={{ fontSize: "var(--fs-lg)", fontWeight: 600 }}>{tx("复核并批准", "Review & approve")}</h2>
            </Row>
            <fieldset className="checklist">
              <legend>{tx("核对清单（{n} 项都勾选后可批准）", "Checklist ({n} items required)", { n: p.checklist.length })}</legend>
              {p.checklist.map((c, i) => (
                <label key={c} className={checks.has(i) ? "on" : ""}>
                  <input
                    type="checkbox"
                    checked={checks.has(i)}
                    onChange={() =>
                      setChecks((s) => {
                        const n = new Set(s);
                        if (n.has(i)) n.delete(i);
                        else n.add(i);
                        return n;
                      })
                    }
                  />
                  <span>{c}</span>
                </label>
              ))}
            </fieldset>
            <Col gap={4}>
              <Muted>{tx("审批要求", "Requirement")}</Muted>
              <span style={{ fontSize: "var(--fs-sm)" }}>{p.requirement}</span>
            </Col>
            <label className="v3-field">
              <span className="label">{tx("备注（可选）", "Note (optional)")}</span>
              <Textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} placeholder={tx("写给执行记录和报告的说明…", "A note for the execution record and report…")} style={{ minHeight: 60 }} />
            </label>
            <Row gap={8}>
              <Gated reason={decideBlocked} grow={1}>
                <Btn kind="danger" h={32} style={{ flexGrow: 1 }} onClick={() => setRejecting(true)}>
                  {tx("拒绝", "Reject")}
                </Btn>
              </Gated>
              <Gated reason={decideBlocked} grow={2}>
                <Btn kind="primary" icon="key" h={32} style={{ flexGrow: 2 }} disabled={!all} onClick={approve} title={all ? undefined : tx("先完成核对清单", "Complete the checklist first")}>
                  {tx("用通行密钥批准", "Approve with a passkey")}
                </Btn>
              </Gated>
            </Row>
            <Row gap={8} align="flex-start">
              <Icon name="shield" size={14} color="var(--muted)" style={{ marginTop: 1 }} />
              <Muted>{tx("批准后由独立执行器执行，AI 自身没有写权限；批准记录写入审计日志。", "An independent executor runs it after approval; the AI has no write access. Approvals are audited.")}</Muted>
            </Row>
            {p.execution && (
              <Col gap={6} className="anim-fade" style={{ padding: "10px 12px", borderRadius: 8, background: "var(--sunken)" }}>
                <Row gap={8}>
                  <Icon name="play" size={14} color="var(--accent-text)" />
                  <b style={{ fontSize: "var(--fs-sm)" }}>{tx("执行", "Execution")}</b>
                  <Spacer />
                  {p.execution.status ? (
                    <Badge tone={execStatus(p.execution.status)[0]}>{execStatus(p.execution.status)[1]}</Badge>
                  ) : (
                    <Row gap={6}>
                      <span className="v3-spinner spin" style={{ color: "var(--accent)" }} />
                      <Muted size={11.5}>{tx("等待执行器记录…", "Waiting for the executor to record it…")}</Muted>
                    </Row>
                  )}
                </Row>
                <KV k={tx("批准于", "Approved")} kw={64}>{when(p.execution.approved_at)}</KV>
                <KV k={tx("执行 ID", "Execution")} kw={64}><span className="mono">{p.execution.execution_id}</span></KV>
                {(p.rollback || p.alert_rule) && (
                  <Btn h={26} icon="play" style={{ alignSelf: "flex-start" }} to={`/executions/${p.execution.execution_id}`}>
                    {tx("查看执行", "View execution")}
                  </Btn>
                )}
                {p.execution.upstream_id && <KV k={tx("上游 ID", "Upstream ID")} kw={64}><span className="mono">{p.execution.upstream_id}</span></KV>}
              </Col>
            )}
          </Col>
          <Col gap={12} style={{ padding: 16, borderTop: "1px solid var(--border)" }}>
            <Row gap={8}>
              <h3 style={{ fontSize: "var(--fs-sm)", fontWeight: 600 }}>{tx("讨论", "Discussion")}</h3>
              <Spacer />
            </Row>
            <ul className="discussion">
              {[...p.discussion, ...thread].map((m, i) => (
                <li key={i} className={i >= p.discussion.length ? "anim-rise" : undefined}>
                  {m.ai ? <AiAvatar /> : <Avatar name={m.author} />}
                  <Col gap={2}>
                    <Row gap={8} style={{ fontSize: "var(--fs-sub)" }}>
                      <b style={{ fontWeight: 600 }}>{m.author}</b>
                      <Muted>{when(m.time)}</Muted>
                    </Row>
                    <Markdown text={m.text} />
                  </Col>
                </li>
              ))}
            </ul>
            <form
              className="reply"
              onSubmit={async (e) => {
                e.preventDefault();
                if (!reply.trim()) return;
                const m = await api<Proposal["discussion"][number]>("POST", `/proposals/${p.id}/comments`, { body: { text: reply } });
                setThread((t) => [...t, m]);
                setReply("");
              }}
            >
              <input value={reply} onChange={(e) => setReply(e.target.value)} placeholder={tx("回复，或 @ 同事…", "Reply, or @ a teammate…")} aria-label={tx("回复讨论", "Reply")} />
              <IconBtn icon="send" label={tx("发送回复", "Send")} size={26} submit />
            </form>
          </Col>
        </Card>
      </div>
      <Dialog
        open={rejecting}
        onClose={() => setRejecting(false)}
        title={tx("拒绝提案", "Reject proposal")}
        footer={
          <>
            <Btn kind="ghost" onClick={() => setRejecting(false)}>
              {tx("取消", "Cancel")}
            </Btn>
            <Btn
              kind="danger"
              onClick={async () => {
                const comment = note.trim().replace(/\s+/g, " ").slice(0, 1000);
                try {
                  await api("POST", `/proposals/${p.id}/reject`, { body: comment ? { comment } : {} });
                  setRejecting(false);
                  toast(tx("已拒绝提案", "Proposal rejected"));
                } catch (e) {
                  toast(e instanceof Error ? e.message : tx("拒绝失败，请稍后重试", "Couldn't reject; try again later"), { tone: "danger" });
                }
                void refetch();
              }}
            >
              {tx("确认拒绝", "Reject")}
            </Btn>
          </>
        }
      >
        <Col gap={8}>
          <Muted>{tx("说明拒绝原因，AI 会把它作为新的约束继续调查。", "Explain why; the AI uses it as a new constraint.")}</Muted>
          <Textarea value={note} onChange={(e) => setNote(e.target.value)} autoFocus />
        </Col>
      </Dialog>
      <PasskeyApproval proposalId={p.id} comment={note} open={approving} onClose={() => setApproving(false)} onDone={() => void refetch()} />
    </Page>
  );
}
