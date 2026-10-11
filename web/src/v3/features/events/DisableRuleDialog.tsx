import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, api, newIdempotencyKey } from "../../api/client";
import { Btn, Col, Icon, Muted, Row } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { Gate } from "../../ui/permission";
import { useI18n } from "../../i18n";
import { InvestigationPicker, useOpenInvestigations, type OpenInvestigation } from "./SilenceProposal";

/**
 * Propose disabling an alert rule (D11 decision 7): POST /alert-rules/{id}/disable { investigation_id } with an
 * Idempotency-Key kept for the dialog, so a retry replays the same proposal. `ruleId` is the rule list's ID; `app` is
 * the rule's application key (its D32 `application` label) and narrows the investigations to that application.
 * Nothing changes remotely until two people approve the proposal.
 */
export function DisableRuleDialog({ open, onClose, ruleId, ruleName, app }: { open: boolean; onClose: () => void; ruleId: string; ruleName: string; app?: string | null }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { items, more } = useOpenInvestigations(open);
  const sameApp = (i: OpenInvestigation) => !app || !i.appKey || i.appKey === app;
  const shown = items.filter(sameApp);
  const [inv, setInv] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const key = useRef("");
  useEffect(() => {
    if (!open) return;
    setInv("");
    setErr(null);
    key.current = newIdempotencyKey();
  }, [open, ruleId]);
  useEffect(() => {
    // Preselect when exactly one investigation fits.
    if (open && !inv && shown.length === 1) setInv(shown[0].id);
  }, [open, inv, shown]);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api<{ proposal_id: string; status: string }>("POST", `/alert-rules/${encodeURIComponent(ruleId)}/disable`, { body: { investigation_id: inv }, idempotencyKey: key.current });
      toast(tx("已提议停用 {r}，等待两人批准", "Proposed disabling {r}; awaiting two approvals", { r: ruleName }));
      onClose();
      nav(`/review/${r.proposal_id}`);
    } catch (e) {
      const code = e instanceof ApiError ? e.code : "";
      const status = e instanceof ApiError ? e.status : 0;
      setErr(
        code === "disable_unavailable"
          ? tx("这条规则不在执行器合并的规则文件里，不能在这里停用。", "This rule isn't in a rule file the executor merges, so it can't be disabled here.")
          : code === "source_changed"
            ? tx("核对期间规则变了。刷新规则列表后再提议。", "The rule changed while it was checked. Refresh the rule list and propose again.")
            : status === 404
              ? tx("找不到这条规则，它可能已经被删掉了。", "The rule wasn't found; it may have been removed.")
              : status === 403
                ? tx("你没有提议停用这条规则的权限。", "You don't have permission to propose disabling this rule.")
                : e instanceof ApiError
                  ? e.message
                  : tx("没能提议停用，请稍后重试。", "Couldn't propose disabling; try again later."),
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={480}
      title={tx("提议停用 {r}", "Propose disabling {r}", { r: ruleName })}
      footer={
        <>
          <Btn kind="ghost" onClick={onClose}>
            {tx("取消", "Cancel")}
          </Btn>
          {/* The server needs alert_rules · change and proposals · change on the rule's application. */}
          <Gate need={["proposals", "change"]}>
            <Btn kind="primary" icon="x" loading={busy} disabled={!inv} onClick={() => void submit()}>
              {tx("提议停用", "Propose disabling")}
            </Btn>
          </Gate>
        </>
      }
    >
      <Col gap={12}>
        <Muted size={12.5}>
          {tx("停用会从规则文件里删掉这一条。需要两位非发起人用通行密钥批准；批准后由执行器合并，并读回到 Prometheus 里没有这条规则为止。现在不会改动任何东西。", "Disabling removes this rule from its rule file. Two people other than you approve with passkeys; then the executor merges it and reads back until Prometheus no longer has the rule. Nothing changes now.")}
        </Muted>
        <Col gap={6}>
          <b style={{ fontSize: "var(--fs-sm)", fontWeight: 600 }}>{tx("关联的调查", "Investigation")}</b>
          {shown.length === 0 && !more ? (
            <Muted size={11.5}>{app ? tx("{a} 没有进行中的调查。停用要挂在同一应用的开放调查下。", "{a} has no open investigation. A disable hangs off an open investigation of the same application.", { a: app }) : tx("没有进行中的调查。停用要挂在同一应用的开放调查下。", "No open investigation. A disable hangs off an open investigation of the same application.")}</Muted>
          ) : (
            <InvestigationPicker value={inv} onChange={setInv} items={shown} more={more} accept={sameApp} />
          )}
        </Col>
        {err && (
          <Row gap={6} align="flex-start" className="ich-callout warn">
            <Icon name="alert" size={13} style={{ marginTop: 2 }} />
            <span>{err}</span>
          </Row>
        )}
      </Col>
    </Dialog>
  );
}
