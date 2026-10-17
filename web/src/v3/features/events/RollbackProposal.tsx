import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { create } from "zustand";
import { API_MODE, ApiError, api, newIdempotencyKey, useGet } from "../../api/client";
import type { RollbackCandidates, ServiceCatalog } from "../../api/types";
import { useEnv } from "../../app/environment";
import { Badge, Btn, Col, Icon, Muted, Row, Section, Skeleton, Spacer, Textarea, Tile, cx } from "../../ui/core";
import { Drawer, toast } from "../../ui/overlays";
import { Gated } from "../../ui/permission";
import { useI18n } from "../../i18n";
import { InvestigationPicker, useOpenInvestigations } from "./SilenceProposal";
import { BlockedCard, ChangeTags, PlanTiles, appOfService, envOfService, useBlockedText, useRollbackBlocked, useWhen } from "./rollback";
import { useRevisionTime } from "../observe/revisionTime";

/**
 * Rollback proposal drawer (approved design board §1; contract docs/api/v3-rollback.md §1–2). Opened from a service,
 * an investigation canvas, an AI suggestion in an investigation chat, or a finished execution ("re-propose").
 * The person picks a revision the Deployment still owns, writes a reason and submits; two passkey approvals and the
 * executor do the rest. It never submits on its own.
 */
export type RollbackRequest = {
  /** Service whose workload is rolled back; when absent it follows the investigation's application in this environment. */
  serviceId?: string;
  /** Fixed investigation (canvas, chat, execution); otherwise the person picks an open one of the same application. */
  investigationId?: string;
  investigationTitle?: string;
  /** Revision an AI suggestion pointed at; re-proposals never preselect. */
  preselect?: string;
};

export const useRollbackDrawer = create<{ req: RollbackRequest | null; open: (r: RollbackRequest) => void; close: () => void }>((set) => ({
  req: null,
  open: (req) => set({ req }),
  close: () => set({ req: null }),
}));

/** Mounted once in the shell so any page can open the drawer. */
export function RollbackDrawerHost() {
  const { req, close } = useRollbackDrawer();
  // A new request remounts the drawer so no selection or reason leaks between openings.
  return req ? <RollbackProposalDrawer key={JSON.stringify(req)} req={req} onClose={close} /> : null;
}

type Field = "revision" | "reason" | null;

function RollbackProposalDrawer({ req, onClose }: { req: RollbackRequest; onClose: () => void }) {
  const { tx } = useI18n();
  const demo = API_MODE === "mock";
  const env = useEnv((s) => s.env);
  const blockedText = useBlockedText();
  const permission = useRollbackBlocked();
  const fmtAt = useWhen().at;
  const revTime = useRevisionTime();
  const { items: investigations, more: moreInvestigations } = useOpenInvestigations(true);
  // Also names the service in the header when the workload can't be read.
  const services = useGet<ServiceCatalog>("/services", undefined, { staleTime: 5 * 60_000 });
  const [invPick, setInvPick] = useState(req.investigationId ?? "");
  const invId = req.investigationId ?? invPick;
  const inv = investigations.find((i) => i.id === invId);
  // From an investigation: the service of its application in the current environment.
  const serviceId = useMemo(() => {
    if (req.serviceId) return req.serviceId;
    const app = inv?.application_id;
    if (!app) return null;
    return services.data?.items.find((s) => appOfService(s.id) === app && (demo || envOfService(s.id) === env))?.id ?? null;
  }, [req.serviceId, inv, services.data, env, demo]);
  const serviceName = services.data?.items.find((s) => s.id === serviceId)?.name;
  const app = serviceId ? appOfService(serviceId) : null;
  const cands = useGet<RollbackCandidates>(serviceId ? `/services/${serviceId}/rollback-candidates` : null, undefined, { staleTime: 0 });
  const data = cands.data;
  const [target, setTarget] = useState<string>("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [stale, setStale] = useState(false);
  const [fieldErr, setFieldErr] = useState<{ field: Field; text: string } | null>(null);
  const [formErr, setFormErr] = useState<string | null>(null);
  const [preselectLost, setPreselectLost] = useState(false);

  // An AI suggestion preselects its revision when it's still proposable; otherwise say so and let the person choose.
  useEffect(() => {
    if (!req.preselect || !data || target) return;
    if (data.candidates.some((c) => c.revision === req.preselect && !c.current)) setTarget(req.preselect);
    else if (!data.blocked_reason) setPreselectLost(true);
  }, [data]); // eslint-disable-line react-hooks/exhaustive-deps

  // Same body → same key, so a retry after a network error replays instead of proposing twice.
  const body = data?.workload && invId && target ? { investigation_id: invId, service_id: serviceId, target_revision: target, expected_template_digest: data.workload.current_template_digest, reason: reason.trim() } : null;
  const key = useMemo(() => newIdempotencyKey(), [JSON.stringify(body)]); // eslint-disable-line react-hooks/exhaustive-deps

  const submit = async () => {
    if (!body) return;
    if (!body.reason) return setFieldErr({ field: "reason", text: tx("请写明回滚原因", "Write down why you're rolling back") });
    setBusy(true);
    setFieldErr(null);
    setFormErr(null);
    try {
      const r = await api<{ proposal_id: string; status: string; action_kind: string }>("POST", "/rollback-proposals", { body, idempotencyKey: key });
      toast(
        <span>
          {tx("已提交回滚提案 {p}，等待两人批准。批准并执行前不会改动 {d}。", "Rollback proposal {p} submitted for two approvals. {d} isn't touched until it's approved and executed.", { p: r.proposal_id, d: data?.workload?.deployment ?? serviceName ?? "" })}{" "}
          <Link to={`/review/${r.proposal_id}`} style={{ color: "inherit", fontWeight: 600, textDecoration: "underline" }}>
            {tx("查看提案", "View proposal")}
          </Link>
        </span>,
      );
      onClose();
    } catch (e) {
      if (!(e instanceof ApiError)) setFormErr(tx("提交失败，请稍后重试", "Couldn't submit; try again later"));
      else if (e.code === "rollback_target_changed") {
        // The Deployment moved since the list was read: keep the drawer and the reason, reread, choose again.
        setStale(true);
        setTarget("");
        void cands.refetch();
      } else if (e.status === 422) setFieldErr({ field: body.reason ? "revision" : "reason", text: e.message });
      else if (e.code === "env_view_only") setFormErr(tx("先在右上角选择一个具体环境，再提议回滚。", "Choose a single environment (top right) before proposing a rollback."));
      else if (e.status === 409 && e.code !== "idempotency_conflict") {
        // Same codes as blocked_reason: reread so the drawer shows why.
        void cands.refetch();
        setFormErr(e.message);
      } else setFormErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  const err = cands.error instanceof ApiError ? cands.error : null;
  const blocked = data?.blocked_reason ?? (err?.status === 503 && err.code === "workload_unavailable" ? "workload_unavailable" : null);
  const proposable = data && !data.blocked_reason ? data.candidates.filter((c) => !c.current) : [];
  const sameApp = (i: { application_id?: string }) => !app || !i.application_id || i.application_id === app;
  const invItems = investigations.filter(sameApp);
  const ready = !!body && !blocked && !busy;
  const footerNote = tx("需要两人通行密钥批准，你不能批准自己的提案。批准后由执行器写入，只改 Pod template。", "Two people approve with passkeys; you can't approve your own proposal. The executor then writes it, changing only the Pod template.");

  let content;
  if (!serviceId) {
    content =
      req.investigationId && (services.isLoading || !investigations.length) ? (
        <Skeleton h={200} />
      ) : (
        <BlockedCard icon="box" title={tx("找不到要回滚的服务", "No service to roll back")} desc={tx("这个调查没有关联到当前环境里的服务。可以从服务详情页提议回滚。", "This investigation isn't linked to a service in this environment. You can propose a rollback from the service's page.")} />
      );
  } else if (cands.isLoading) content = <Skeleton h={260} />;
  else if (blocked) {
    const b = blockedText(blocked);
    content = (
      <BlockedCard icon={b.icon} title={b.title} desc={b.desc}>
        <Row gap={8}>
          {b.to && (
            <Btn h={26} to={b.to}>
              {b.action}
            </Btn>
          )}
          {b.retry && (
            <Btn h={26} icon="rotate" loading={cands.isFetching} onClick={() => void cands.refetch()}>
              {tx("重试", "Retry")}
            </Btn>
          )}
        </Row>
      </BlockedCard>
    );
  } else if (!data) {
    // Route not deployed yet (404/503) or no access: say it's unavailable, never crash.
    content = (
      <BlockedCard icon="plug" title={tx("回滚暂不可用", "Rollback isn't available right now")} desc={err?.message ?? tx("读取可回滚的修订失败。", "Reading the revisions failed.")}>
        <Btn h={26} icon="rotate" loading={cands.isFetching} onClick={() => void cands.refetch()}>
          {tx("重试", "Retry")}
        </Btn>
      </BlockedCard>
    );
  } else if (!proposable.length) {
    const b = blockedText("no_candidates");
    content = <BlockedCard icon={b.icon} title={b.title} desc={b.desc} />;
  } else {
    const w = data.workload!;
    content = (
      <Col gap={14} style={{ padding: 16 }}>
        <Section title={tx("关联的调查", "Investigation")}>
          {req.investigationId ? (
            <Row gap={6} style={{ padding: "6px 10px", borderRadius: 8, background: "var(--sunken)", fontSize: "var(--fs-sm)" }}>
              <Icon name="lock" size={12} color="var(--faint)" />
              <span className="ellipsis">
                {req.investigationId}
                {(req.investigationTitle ?? inv?.title) && ` · ${req.investigationTitle ?? inv?.title}`}
              </span>
            </Row>
          ) : (
            <InvestigationPicker value={invPick} onChange={setInvPick} items={invItems} more={moreInvestigations} accept={sameApp} />
          )}
          <Muted size={11.5}>{req.investigationId ? tx("提案会挂在这个调查下。", "The proposal hangs off this investigation.") : tx("回滚必须挂在同一应用的开放调查下，方便事后追溯。", "A rollback hangs off an open investigation of the same application so it can be traced later.")}</Muted>
        </Section>
        <Col gap={4} style={{ fontSize: "var(--fs-sm)" }}>
          <Row gap={8}>
            <Muted style={{ width: 44 }}>{tx("目标", "Target")}</Muted>
            <span className="mono">
              Deployment {w.namespace}/{w.deployment}
            </span>
          </Row>
          <Row gap={8}>
            <Muted style={{ width: 44 }}>{tx("当前", "Now")}</Muted>
            <span>{tx("修订 {r} · 期望 {d} 个副本，可用 {a} 个", "Revision {r} · {d} desired, {a} available", { r: w.current_revision, d: w.replicas.desired, a: w.replicas.available })}</span>
          </Row>
        </Col>
        <Section title={tx("回滚到", "Roll back to")}>
          <Muted size={11.5}>{tx("只列出 Deployment 仍保留的修订，读取于 {t}。", "Only revisions the Deployment still keeps, read at {t}.", { t: fmtAt(data.observed_at) ?? "—" })}</Muted>
          {stale && (
            <div className="unk-banner warn" role="status">
              <Icon name="alert" size={14} />
              <span className="grow">{tx("修订列表已过期：读取后 Deployment 变化了。已重新读取，请重新选择。", "The revision list was out of date: the Deployment changed after it was read. It's been reread; choose again.")}</span>
            </div>
          )}
          {req.preselect && target === req.preselect && !stale && (
            <div className="unk-banner" role="status">
              <Icon name="sparkles" size={14} />
              <span className="grow">{tx("已按 AI 建议预选修订 {r}，请确认后再提交。", "Revision {r} is preselected from the AI suggestion; check it before you submit.", { r: req.preselect })}</span>
            </div>
          )}
          {preselectLost && !stale && (
            <div className="unk-banner warn" role="status">
              <Icon name="alert" size={14} />
              <span className="grow">{tx("建议的修订 {r} 已不在可回滚的列表里，请按当前列表重新选择。", "The suggested revision {r} is no longer in the list; choose from the current one.", { r: req.preselect ?? "" })}</span>
            </div>
          )}
          <div className="rb-revs" role="radiogroup" aria-label={tx("回滚到哪个修订", "Revision to roll back to")}>
            {data.candidates.map((c) => (
              <button
                key={c.revision}
                type="button"
                role="radio"
                aria-checked={target === c.revision}
                disabled={c.current}
                className={cx("rb-rev", target === c.revision && "on")}
                onClick={() => {
                  setTarget(c.revision);
                  setStale(false);
                  setPreselectLost(false);
                  if (fieldErr?.field === "revision") setFieldErr(null);
                }}
              >
                <span className="o" />
                <span className="body">
                  <span className="meta">
                    <b style={{ color: "var(--text)", fontSize: "var(--fs-sm)" }}>{tx("修订 {r}", "Revision {r}", { r: c.revision })}</b>
                    {c.current && <Badge tone="accent">{tx("当前", "Current")}</Badge>}
                    {/* When the revision took effect, as on the changes page; a reused ReplicaSet's creation isn't it (PA-172). */}
                    {c.at !== undefined ? (
                      c.at ? (
                        <span title={revTime.basis(c.time_basis) ?? undefined}>{fmtAt(c.at)}</span>
                      ) : (
                        <span title={revTime.reason(c.time_unknown_reason) ?? undefined}>{c.not_before || c.not_after ? revTime.between(c.not_before, c.not_after) : tx("时间未知", "Time unknown")}</span>
                      )
                    ) : (
                      c.created_at && <span>{fmtAt(c.created_at)}</span>
                    )}
                  </span>
                  <span className={cx("cause", !c.change_cause && "none")}>{c.change_cause ?? tx("未记录变更原因", "No change cause recorded")}</span>
                  <span className="meta">
                    {c.containers.map((ct) => (
                      <span key={ct.name} className="mono ellipsis" title={ct.image}>
                        {ct.name} · {ct.image}
                      </span>
                    ))}
                  </span>
                  {!c.current && (
                    <span className="meta">
                      <ChangeTags changes={c.changes} />
                    </span>
                  )}
                </span>
              </button>
            ))}
          </div>
          {fieldErr?.field === "revision" && <span className="rb-err">{fieldErr.text}</span>}
        </Section>
        <Section title={tx("原因", "Reason")}>
          <Textarea
            rows={2}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value);
              if (fieldErr?.field === "reason") setFieldErr(null);
            }}
            placeholder={tx("例如：修订 7 上线后 /checkout 5xx 升高，回到上线前的修订", "e.g. /checkout 5xx rose after revision 7; go back to the revision before it")}
            aria-label={tx("回滚原因", "Rollback reason")}
            aria-invalid={fieldErr?.field === "reason"}
            maxLength={2000}
            style={{ minHeight: 60 }}
          />
          {fieldErr?.field === "reason" ? <span className="rb-err">{fieldErr.text}</span> : <Muted size={11.5}>{tx("写进提案和审计，审批人会看到。", "It goes into the proposal and the audit log; approvers see it.")}</Muted>}
        </Section>
        {data.verification && (
          <Section
            title={tx("验证计划", "Verification plan")}
            right={
              <Row gap={4}>
                <Icon name="lock" size={11} color="var(--faint)" />
                <Muted size={11}>{tx("部署方审查，这里不能改", "Set by the deployer; read-only")}</Muted>
              </Row>
            }
          >
            <PlanTiles plan={data.verification} compact />
          </Section>
        )}
        {formErr && (
          <div className="unk-banner warn" role="alert">
            <Icon name="alert" size={14} />
            <span className="grow">{formErr}</span>
          </div>
        )}
      </Col>
    );
  }
  const showFooter = !!data && !blocked && proposable.length > 0;
  return (
    <Drawer
      open
      onClose={onClose}
      width={460}
      title={tx("提议回滚", "Propose a rollback")}
      sub={[data?.workload ? `${data.workload.namespace}/${data.workload.deployment}` : serviceName ?? null].filter(Boolean).join(" · ") || tx("Kubernetes 工作负载", "Kubernetes workload")}
      icon={<Tile icon="undo" tone="accent" size={30} />}
      right={demo ? <Badge tone="warn">{tx("示意数据", "Sample data")}</Badge> : undefined}
      footer={
        showFooter && (
          <>
            <Muted size={11.5} style={{ flex: "1 1 160px" }}>
              <Icon name="key" size={11} /> {footerNote}
            </Muted>
            <Spacer />
            <Btn kind="ghost" onClick={onClose}>
              {tx("取消", "Cancel")}
            </Btn>
            <Gated reason={permission}>
              <Btn kind="primary" loading={busy} disabled={!ready || !reason.trim()} onClick={() => void submit()}>
                {tx("提交回滚提案", "Submit proposal")}
              </Btn>
            </Gated>
          </>
        )
      }
    >
      {content}
    </Drawer>
  );
}
