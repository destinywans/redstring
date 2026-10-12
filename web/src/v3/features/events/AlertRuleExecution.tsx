import { useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import type { AlertRuleExecState, AlertRuleExecutionDetail, ExecutionAlertRule, Proposal, Tone } from "../../api/types";
import { Avatar, Badge, Btn, Card, CardHead, Col, Icon, KV, Muted, Row, Spacer, TextLink, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Gate } from "../../ui/permission";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";
import { DisableRuleDialog } from "./DisableRuleDialog";

const TERMINAL: AlertRuleExecState[] = ["enabled", "disabled", "failed", "needs_attention"];
const short = (sha: string | null) => (sha ? sha.slice(0, 7) : null);

/**
 * Execution view of an alert rule enable or disable (approved D11 board, §11.5): the executor's steps as a timeline
 * (committing → merging → merged → loading → enabled/disabled), the merge request, commit and readback on the
 * right, and for needs_attention, uncertain and failed one sentence on top with the one thing to do. Rereads every
 * 3 s until a terminal state; uncertain keeps rereading while the executor reconciles read-only.
 */
export function AlertRuleExecutionPage({ id }: { id: string }) {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const demo = API_MODE === "mock";
  const { data: e, refetch } = useGet<AlertRuleExecutionDetail>(`/executions/${id}`, undefined, {
    refetchInterval: (q) => (q.state.data?.alert_rule && !TERMINAL.includes(q.state.data.alert_rule.state) ? 3000 : false),
  });
  const ar = e?.alert_rule;
  // The projection has no rule name; the proposal has the whole rule.
  const { data: p } = useGet<Proposal>(ar ? `/proposals/${ar.proposal_id}` : null);
  const [rechecking, setRechecking] = useState(false);
  const [observed, setObserved] = useState<string | null>(null);
  const [disabling, setDisabling] = useState(false);
  if (!e || !ar) return null;
  const enable = ar.operation === "enable";
  const rule = p?.alert_rule?.rule;
  const target = ar.repository.target_branch;
  const timeoutMin = Math.round((p?.alert_rule?.prometheus.readback_timeout_seconds ?? 300) / 60);
  const code = ar.failure?.code;
  const [tone, label] = ((): [Tone, string] => {
    switch (ar.state) {
      case "enabled":
        return ["ok", tx("已生效", "In effect")];
      case "disabled":
        return ["ok", tx("已停用", "Disabled")];
      case "failed":
        return ["danger", code === "rule_health_error" ? tx("求值出错", "Evaluation error") : tx("失败", "Failed")];
      case "needs_attention":
        return ["warn", tx("需要处理", "Needs attention")];
      case "uncertain":
        return ["warn", tx("对账中", "Reconciling")];
      default:
        return ["accent", tx("执行中", "Executing")];
    }
  })();
  const failureText = (): string => {
    switch (code) {
      case "stale_base":
        return tx("提交前发现 {b} 已有新提交，不是批准时的 {base}。执行器没有写 {b}。", "Before committing, {b} had a new commit, not {base} from the approval. The executor didn't write {b}.", { b: target, base: short(p?.alert_rule?.repository.base_sha ?? null) ?? tx("基础提交", "the base commit") });
      case "load_timeout": {
        const reload = ar.readback.reload;
        const base = tx("规则仓库已合并，但 {m} 分钟内没读到这条规则", "The rule repository is merged, but the rule wasn't read back within {m} min", { m: timeoutMin });
        if (reload?.last_successful === false)
          return base + tx("：Prometheus 最近一次重载失败（上次成功 {t}）。", ": Prometheus's last reload failed (last success {t}).", { t: when.clock(reload.last_success_at) ?? "—" });
        return base + tx("。", ".");
      }
      case "rule_health_error":
        return tx("health=err · lastError: {e}。规则仍在运行。", "health=err · lastError: {e}. The rule is still running.", { e: ar.readback.last_error ?? "—" });
      case "branch_conflict":
        return tx("源分支已存在且内容不符，执行器没有覆盖。", "The source branch already exists with different content; the executor didn't overwrite it.");
      case "mr_closed":
        return tx("合并请求被人关闭了，执行器没有重新打开。", "Someone closed the merge request; the executor didn't reopen it.");
      case "protection_changed":
        return tx("{b} 的保护设置和批准时不一样，执行器没有合并。", "{b}'s protection settings differ from the approval; the executor didn't merge.", { b: target });
      case "credential_rejected":
        return tx("GitLab 拒绝了执行器的写凭据。", "GitLab rejected the executor's write credential.");
      case "approval_invalid":
        return tx("两票批准重验没有通过（批准被撤回或已过期）。", "The two approvals didn't pass the recheck (revoked or expired).");
      case "merge_refused":
        return tx("GitLab 拒绝了合并。", "GitLab refused the merge.");
      case "plan_changed":
        return tx("写入前目标文件或分支变了，执行器没有写。", "The target file or branch changed before the write; the executor didn't write.");
      case "source_changed":
        return tx("写入前规则组或要停用的规则变了，执行器没有写。", "The rule group or the observed rule changed before the write; the executor didn't write.");
      default:
        return ar.readback.state === "mismatch" ? tx("读回的规则组和合并后的文件不一致。", "The rule group read back doesn't match the merged file.") : (ar.failure?.message ?? tx("执行器没有给出原因。", "The executor gave no reason."));
    }
  };

  // Actions (§5.4): a new proposal, a read-only recheck, or proposing to disable. Only the recheck has a route in
  // mind; the others are the sample's until the backend names them.
  const repropose = () => {
    if (demo) {
      toast(tx("已按当前 {b} 重新提案，等待两人批准", "Proposed again from the current {b}; awaiting two approvals", { b: target }));
      return nav("/review/PRP-115");
    }
    nav("/alert-rules/new");
  };
  // Disabling needs the rule list ID: the observed rule, else find it on the rule list by name.
  const ruleId = ar.observed_rule_id ?? observed;
  const proposeDisable = () => (ruleId ? setDisabling(true) : nav(`/alert-rules?q=${encodeURIComponent(rule?.name ?? "")}`));
  const recheck = async () => {
    setRechecking(true);
    try {
      // D11 §5.4: one read-only observation; a late load is recorded as enabled. The page rereads the execution.
      const r = await api<{ proposal_id: string; execution_id: string; status: string; observed_rule_id?: string }>("POST", `/executions/${e.id}/recheck`, { body: {} });
      if (r.observed_rule_id) setObserved(r.observed_rule_id);
      await refetch();
      toast(r.status === "enabled" ? tx("重新核对：已生效", "Rechecked: in effect") : r.status === "disabled" ? tx("重新核对：已停用", "Rechecked: disabled") : tx("已重新核对", "Rechecked"));
    } catch (err) {
      const code = err instanceof ApiError ? err.code : "";
      toast(
        code === "execution_not_observable"
          ? tx("这次执行现在不能重新核对。", "This execution can't be rechecked now.")
          : code === "rule_content_changed"
            ? tx("已生效的规则和合并的文件不再一致。", "The rule in effect no longer matches the merged file.")
            : err instanceof ApiError && err.status === 503
              ? tx("GitLab 或 Prometheus 现在读不到，稍后再试。", "GitLab or Prometheus can't be read right now; try again later.")
              : err instanceof ApiError
                ? err.message
                : tx("重新核对失败，请稍后再试", "Couldn't recheck; try again later"),
        { tone: "danger" },
      );
    } finally {
      setRechecking(false);
    }
  };
  const banner = ((): { tone: "warn" | "danger" | "info"; title?: string; text: string; action?: ReactNode } | null => {
    if (ar.state === "uncertain") return { tone: "info", text: tx("结果不明时只读不写；对账完成后会自动继续，或停在“需要处理”。", "While the outcome is unknown it only reads; once reconciled it continues on its own or stops at “needs attention”.") };
    if (ar.state === "needs_attention" && code === "load_timeout")
      return {
        tone: "warn",
        title: tx("加载超时", "Load timed out"),
        text: tx("规则仓库已经合并。修好 Prometheus 的加载后重新核对：只读，晚到的加载会记为已生效。", "The rule repository is merged. Fix Prometheus's loading, then recheck: read-only, a late load is recorded as in effect."),
        action: (
          <Btn h={28} kind="primary" icon="rotate" loading={rechecking} onClick={() => void recheck()}>
            {tx("重新核对", "Recheck")}
          </Btn>
        ),
      };
    if (ar.state === "needs_attention")
      return {
        tone: "warn",
        title: code === "stale_base" ? tx("基础提交已变化", "The base commit changed") : tx("需要处理", "Needs attention"),
        text: code === "stale_base" ? tx("执行器不会自动变基或覆盖。请按当前 {b} 重新提案并批准。", "The executor never rebases or overwrites on its own. Propose again from the current {b} and approve it.", { b: target }) : tx("执行器不会自动重试。处理后重新提案。", "The executor never retries on its own. Sort it out, then propose again."),
        action: (demo || enable) && (
          <Gate need={["alert_rules", "change"]}>
            <Btn h={28} kind="primary" icon="undo" onClick={repropose}>
              {tx("重新提案", "Propose again")}
            </Btn>
          </Gate>
        ) || undefined,
      };
    if (ar.state === "failed")
      return {
        tone: "danger",
        title: code === "rule_health_error" ? tx("已加载但求值出错", "Loaded, but evaluation fails") : tx("执行失败", "Execution failed"),
        text: code === "rule_health_error" ? tx("规则已经在 Prometheus 里运行但求值出错，不会自动回退。可以提议停用，经两人批准后从文件里删掉。", "The rule is running in Prometheus but fails to evaluate, and nothing rolls it back on its own. Propose disabling it; after two approvals it's removed from the file.") : failureText(),
        action: (enable && code === "rule_health_error" && (
          <Gate need={["alert_rules", "change"]}>
            <Btn h={28} kind="primary" icon="x" onClick={proposeDisable}>
              {tx("提议停用", "Propose disabling")}
            </Btn>
          </Gate>
        )) || undefined,
      };
    return null;
  })();
  const approvers = e.approvers?.map((a) => a.name).join(tx("、", ", "));
  return (
    <Page
      crumbs={[{ label: tx("告警规则", "Alert rules"), to: "/alert-rules" }, tx("执行 {id}", "Execution {id}", { id: e.id.length > 12 ? `${e.id.slice(0, 8)}…` : e.id })]}
      actions={
        <>
          <Badge tone={tone}>{label}</Badge>
          <Btn kind="ghost" icon="file" to={`/review/${ar.proposal_id}`}>
            {tx("查看提案", "Proposal")}
          </Btn>
          <Btn kind="ghost" icon="shield" to="/settings/audit">
            {tx("审计记录", "Audit trail")}
          </Btn>
        </>
      }
    >
      <div className="v3-content anim-fade" style={{ gap: 14 }}>
        <Col gap={4}>
          <h1 style={{ fontSize: 19, fontWeight: 600, letterSpacing: "-0.01em" }}>
            {enable ? tx("启用告警规则 · {r}", "Enable alert rule · {r}", { r: rule?.name ?? "…" }) : tx("停用告警规则 · {r}", "Disable alert rule · {r}", { r: rule?.name ?? "…" })}
          </h1>
          <Muted>
            <span className="mono">{ar.repository.path}</span>
            {approvers && <> · {tx("批准：{n}", "Approved by {n}", { n: approvers })}</>}
            {e.approved_at && <> · {when.at(e.approved_at)}</>}
          </Muted>
        </Col>
        {banner && (
          <div className={cx("ar-banner", banner.tone)}>
            <Icon name={banner.tone === "info" ? "info" : "alert"} size={15} style={{ marginTop: 2 }} />
            <Col gap={2} grow>
              {banner.title && <b>{banner.title}</b>}
              <span>{banner.text}</span>
            </Col>
            {banner.action}
          </div>
        )}
        {rule && ruleId && <DisableRuleDialog open={disabling} onClose={() => setDisabling(false)} ruleId={ruleId} ruleName={rule.name} app={rule.labels.application} />}
        <div className="ar-cols">
          <Card>
            <CardHead title={tx("执行时间线", "Execution timeline")} icon="activity" />
            <Timeline ar={ar} timeoutMin={timeoutMin} failure={failureText} updatedAt={e.updated_at} />
          </Card>
          <Col gap={16}>
            <Card>
              <CardHead title={tx("结果", "Result")} icon="check" />
              <Col gap={8} style={{ padding: "14px 16px" }}>
                <KV k={tx("合并请求", "Merge request")} kw={72}>
                  {ar.repository.mr_iid === null ? (
                    "—"
                  ) : (
                    <span>
                      {ar.repository.mr_url ? (
                        <TextLink icon="external" onClick={() => window.open(ar.repository.mr_url!, "_blank", "noopener")}>
                          !{ar.repository.mr_iid}
                        </TextLink>
                      ) : (
                        `!${ar.repository.mr_iid}`
                      )}{" "}
                      · {ar.repository.merged_sha ? tx("已合并", "Merged") : tx("打开中", "Open")}
                    </span>
                  )}
                </KV>
                <KV k={tx("提交", "Commit")} kw={72}>
                  <span className="mono">{short(ar.repository.merged_sha ?? ar.repository.commit_sha) ?? "—"}</span>
                </KV>
                <KV k={tx("读回", "Readback")} kw={72}>
                  <ReadbackText ar={ar} />
                </KV>
                {ar.readback.checked_at && (
                  <Muted size={11.5}>{tx("核对于 {t}", "Checked {t}", { t: when.clock(ar.readback.checked_at) ?? "—" })}</Muted>
                )}
              </Col>
            </Card>
            {e.approvers && e.approvers.length > 0 && (
              <Card>
                <CardHead title={tx("批准", "Approvals")} icon="key" />
                <Col gap={8} style={{ padding: "14px 16px" }}>
                  {e.approvers.map((a) => (
                    <Row key={a.name + a.verified_at} gap={8}>
                      <Avatar name={a.name} size={20} />
                      <span style={{ fontSize: "var(--fs-sm)" }}>{a.name}</span>
                      <Spacer />
                      <Muted size={11.5}>{tx("通行密钥 · {t}", "Passkey · {t}", { t: when.at(a.verified_at) ?? "—" })}</Muted>
                    </Row>
                  ))}
                  <KV k={tx("执行编号", "Execution")} kw={64}>
                    <span className="mono">{e.id}</span>
                  </KV>
                </Col>
              </Card>
            )}
          </Col>
        </div>
      </div>
    </Page>
  );
}

function ReadbackText({ ar }: { ar: ExecutionAlertRule }) {
  const { tx } = useI18n();
  const r = ar.readback;
  switch (r.state) {
    case "loaded":
      return <span>{tx("组、文件、规则逐字段一致 · health={h}", "Group, file and rule match field by field · health={h}", { h: r.health ?? "—" })}</span>;
    case "absent":
      return <span>{tx("规则组里已经没有这条规则", "The rule is gone from its group")}</span>;
    case "mismatch":
      return <span style={{ color: "var(--warn)" }}>{tx("与合并后的文件不一致", "Doesn't match the merged file")}</span>;
    case "error":
      return <span style={{ color: "var(--danger)" }}>health=err</span>;
    case "timeout":
      return <span style={{ color: "var(--warn)" }}>{tx("未读到", "Not read back")}</span>;
    default:
      return <Muted>{ar.state === "loading" ? tx("等待加载", "Waiting for the load") : "—"}</Muted>;
  }
}

type TlRow = { key: string; title: string; code: string; desc: string; at: string | null; tone: "done" | "now" | "att" | "unsure" | "bad" | "todo" };
/** Where a step sits on the happy path; later ones are listed greyed while the execution runs. */
const ORDER: Record<string, number> = { reserved: 0, committing: 1, merging: 2, pending_review: 2, merged: 3, loading: 4, enabled: 5, disabled: 5 };

/**
 * Every recorded step, in order: a retry (uncertain → committing again) shows as its own rows, never folded away.
 * Only a merging step directly followed by merged shares a row, as on the board. When the page's state isn't the
 * last step (a load timeout is reported on the loading step), a row for the state closes the list.
 */
function Timeline({ ar, timeoutMin, failure, updatedAt }: { ar: ExecutionAlertRule; timeoutMin: number; failure: () => string; updatedAt?: string }) {
  const { tx } = useI18n();
  const when = useWhen();
  const enable = ar.operation === "enable";
  const repo = ar.repository;
  const steps = ar.steps;
  const terminal = TERMINAL.includes(ar.state);
  const lastOf = (state: string) => steps.map((x) => x.state).lastIndexOf(state as AlertRuleExecState);
  const firstOf = (state: string) => steps.findIndex((x) => x.state === state);
  const loaded = ar.readback.first_seen_at !== null || ar.state === "enabled" || ar.state === "disabled";
  const TITLE: Record<string, string> = {
    reserved: tx("已领取", "Claimed"),
    committing: tx("提交", "Commit"),
    uncertain: tx("结果不明", "Outcome unknown"),
    merging: tx("合并中", "Merging"),
    pending_review: tx("等待合并", "Awaiting merge"),
    merged: tx("已合并", "Merged"),
    loading: loaded ? (enable ? tx("已加载", "Loaded") : tx("已重载", "Reloaded")) : tx("加载中", "Loading"),
    enabled: tx("已生效", "In effect"),
    disabled: tx("已停用", "Disabled"),
    failed: ar.failure?.code === "rule_health_error" ? tx("求值出错", "Evaluation error") : tx("失败", "Failed"),
    needs_attention: ar.failure?.code === "load_timeout" ? tx("加载超时", "Load timed out") : tx("需要处理", "Needs attention"),
  };
  const descOf = (state: string, i: number, current: boolean): string => {
    const latest = i === lastOf(state);
    const retry = i > firstOf(state);
    switch (state) {
      case "reserved":
        return tx("两票批准重验通过，绑定的指纹一致", "Both approvals rechecked; the bound fingerprint matches");
      case "committing":
        if (latest && repo.commit_sha) return (retry ? tx("对账后重新提交 · ", "Committed again after reconciling · ") : "") + tx("分支 {b} · 提交 {s}", "Branch {b} · commit {s}", { b: repo.source_branch, s: short(repo.commit_sha) ?? "—" });
        return retry ? tx("对账后重新提交到分支 {b}", "Committing again to branch {b} after reconciling", { b: repo.source_branch }) : tx("在分支 {b} 上提交", "Committing on branch {b}", { b: repo.source_branch });
      case "uncertain":
        return current
          ? repo.mr_iid
            ? tx("合并请求 !{m} 的请求超时，正在只读对账：先查分支，再查 MR 和 {b} 的头。不会再建第二个 MR。", "The request for merge request !{m} timed out; reconciling read-only: the branch first, then the MR and the head of {b}. No second MR is opened.", { m: repo.mr_iid, b: repo.target_branch })
            : tx("上一步的请求结果不明，正在只读对账：先查分支，再查 MR 和 {b} 的头。", "The last request's outcome is unknown; reconciling read-only: the branch first, then the MR and the head of {b}.", { b: repo.target_branch })
          : tx("上一步的请求结果不明，只读对账后继续", "The last request's outcome was unknown; it continued after a read-only reconcile");
      case "merging":
      case "pending_review":
        return latest && repo.mr_iid ? tx("MR !{m} 等待 fast-forward 合并", "MR !{m} waiting for a fast-forward merge", { m: repo.mr_iid }) : tx("正在建合并请求", "Opening the merge request");
      case "merged":
        return tx("MR !{m} 合并 · {b} = {s} · 文件摘要与批准一致", "MR !{m} merged · {b} = {s} · file digest matches the approval", { m: repo.mr_iid ?? "—", b: repo.target_branch, s: short(repo.merged_sha) ?? "—" });
      case "loading":
        if (loaded) return enable ? tx("Prometheus 已读到这条规则", "Prometheus has read the rule") : tx("Prometheus 已重载", "Prometheus has reloaded");
        return enable
          ? tx("等 Prometheus 加载，{m} 分钟内读回一致才算生效", "Waiting for Prometheus to load it; in effect only if the readback matches within {m} min", { m: timeoutMin })
          : tx("等 Prometheus 重载，{m} 分钟内读不到这条规则才算停用", "Waiting for Prometheus to reload; disabled only when the rule is gone within {m} min", { m: timeoutMin });
      case "enabled":
        return tx("读回逐字段一致 · health={h} · 最近求值 {t}", "Readback matches field by field · health={h} · last evaluation {t}", { h: ar.readback.health ?? "—", t: when.clock(ar.readback.last_evaluation_at) ?? "—" });
      case "disabled":
        return tx("读回：规则组里已经没有这条规则", "Readback: the rule is gone from its group");
      case "failed":
      case "needs_attention":
        return failure();
      default:
        return "";
    }
  };
  const toneOf = (state: string, current: boolean): TlRow["tone"] => {
    if (state === "failed") return "bad";
    if (state === "needs_attention") return "att";
    if (state === "uncertain") return current ? "att" : "unsure";
    if (!current || terminal) return "done";
    return "now";
  };
  const rows: TlRow[] = [];
  for (let i = 0; i < steps.length; i++) {
    const s = steps[i];
    const next = steps[i + 1];
    const current = i === steps.length - 1 && s.state === ar.state;
    if ((s.state === "merging" || s.state === "pending_review") && next?.state === "merged") {
      const cur = i + 1 === steps.length - 1 && ar.state === "merged";
      rows.push({ key: `${i}`, title: TITLE.merged, code: `${s.state} → merged`, desc: descOf("merged", i + 1, cur), at: next.at, tone: toneOf("merged", cur) });
      i++;
      continue;
    }
    const retry = s.state === "committing" && i > firstOf("committing");
    // A commit attempt whose outcome turned out unknown is an attempt, not "committed".
    const commitTitle = retry ? tx("重新提交", "Commit again") : next?.state === "uncertain" ? tx("提交", "Commit") : tx("已提交", "Committed");
    rows.push({ key: `${i}`, title: s.state === "committing" ? commitTitle : TITLE[s.state] ?? s.state, code: s.state, desc: descOf(s.state, i, current), at: s.at, tone: toneOf(s.state, current) });
  }
  const last = steps.at(-1);
  if (last && last.state !== ar.state) {
    // The view's state differs from the last recorded step (e.g. a load timeout on the loading step).
    rows.push({ key: "state", title: TITLE[ar.state] ?? ar.state, code: `${last.state} → ${ar.state}`, desc: descOf(ar.state, steps.length, true), at: updatedAt ?? null, tone: toneOf(ar.state, true) });
  }
  if (!terminal && ar.state !== "uncertain") {
    const end = enable ? "enabled" : "disabled";
    const reached = ORDER[ar.state] ?? -1;
    for (const k of ["reserved", "committing", "merged", "loading", end])
      if (ORDER[k] > reached)
        rows.push({ key: `todo-${k}`, title: { reserved: tx("领取", "Claim"), committing: tx("提交", "Commit"), merged: tx("合并", "Merge"), loading: tx("加载", "Load"), enabled: tx("生效", "In effect"), disabled: tx("停用", "Disabled") }[k] ?? k, code: k, desc: "", at: null, tone: "todo" });
  }
  return (
    <ol className="ar-tl" aria-label={tx("执行进度", "Execution progress")}>
      {rows.map((r) => (
        <li key={r.key} className={r.tone}>
          <span className="dot">{r.tone === "done" ? <Icon name="check" size={11} /> : r.tone === "bad" ? <Icon name="x" size={11} /> : r.tone === "att" ? "!" : r.tone === "unsure" ? "?" : null}</span>
          <Col gap={2} grow style={{ minWidth: 0 }}>
            <span className="t">
              {r.title}
              <code>{r.code}</code>
            </span>
            {r.desc && <span className="d">{r.desc}</span>}
          </Col>
          <span className="tm">{when.clock(r.at) ?? ""}</span>
        </li>
      ))}
    </ol>
  );
}
