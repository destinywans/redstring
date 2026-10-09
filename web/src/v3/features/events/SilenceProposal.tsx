import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, api, useGet } from "../../api/client";
import type { CanvasSummary, ServiceCatalog } from "../../api/types";
import { Badge, Btn, Col, Icon, IconBtn, Input, Muted, Row, Section, Segmented, Spacer, Textarea, Tile } from "../../ui/core";
import { Dialog, Drawer, toast } from "../../ui/overlays";
import { ChipSelect } from "../../ui/demo";
import { Unavailable } from "../../ui/unknown";
import { useI18n } from "../../i18n";
import { serviceApplicationId } from "../../api/ids";
import "./events.css";


/**
 * Open investigations with the application key the alert labels use (the service name). GET /investigations isn't
 * implemented in V3, so they come from the investigation canvases (GET /canvases?filter=investigation), whose
 * services are application IDs.
 */
export function useOpenInvestigations(enabled: boolean) {
  const inv = useGet<{ items: CanvasSummary[] }>(enabled ? "/canvases" : null, { filter: "investigation" }, { staleTime: 15_000 });
  const svc = useGet<ServiceCatalog>(enabled ? "/services" : null, undefined, { staleTime: 5 * 60_000 });
  const items = useMemo(() => {
    const appKey = new Map<string, string>();
    for (const s of svc.data?.items ?? []) {
      const app = serviceApplicationId(s.id);
      if (app) appKey.set(app, s.name);
    }
    return (inv.data?.items ?? [])
      .filter((c) => c.kind === "investigation" && !!c.investigation_id && !["done", "resolved", "closed"].includes(c.status))
      .map((c) => {
        const app = c.services[0];
        return { id: c.investigation_id!, title: c.title, application_id: app, appKey: app ? appKey.get(app) ?? null : null };
      });
  }, [inv.data, svc.data]);
  return { items, loading: inv.isLoading, error: inv.error };
}

export type SubmittedSilence = { proposal_id: string; matchers: { k: string; op: string; v: string }[]; duration: number; at: number };

const OPS = ["=", "!=", "=~", "!~"];
const DURATIONS = [1800, 3600, 14400, 43200, 86400];

/** Investigation picker shared by create and expire: open investigations only, with a way to start one. */
export function InvestigationPicker({ value, onChange, items }: { value: string; onChange: (id: string) => void; items: ReturnType<typeof useOpenInvestigations>["items"] }) {
  const { tx } = useI18n();
  if (!items.length)
    return (
      <Col gap={6} style={{ padding: "10px 12px", border: "1px solid var(--border)", borderRadius: 8 }}>
        <Muted size={12.5}>{tx("这个环境里没有开放的调查。静默必须挂在调查下，方便事后追溯。", "There is no open investigation in this environment. Silences hang off an investigation so they can be traced later.")}</Muted>
        <Btn h={26} style={{ alignSelf: "flex-start" }} to="/canvases?new=investigation">
          {tx("发起调查", "Start an investigation")}
        </Btn>
      </Col>
    );
  // Long investigation titles are ellipsized so the chip stays inside the drawer; the full text is in the tooltip.
  return (
    <div className="inv-picker">
      <ChipSelect
        label={<span className="inv-label inv-placeholder">{tx("选择调查", "Choose an investigation")}</span>}
        ariaLabel={(() => {
          // Investigation IDs don't always carry an INV- prefix (e.g. ones started from an alert group).
          const cur = items.find((i) => i.id === value);
          return cur ? tx("关联的调查：{t}", "Investigation: {t}", { t: `${cur.id} · ${cur.title ?? ""}` }) : tx("关联的调查：未选择", "Investigation: none chosen");
        })()}
        showLabel={false}
        value={value || undefined}
        onChange={(v) => onChange(v as string)}
        options={items.map((i) => ({
          value: i.id,
          label: (
            <span className="inv-label" title={`${i.id} · ${i.title ?? ""}`}>
              {i.id} · {i.title ?? ""}
            </span>
          ),
          hint: i.appKey ?? undefined,
        }))}
      />
    </div>
  );
}

/**
 * New silence as a proposal (approved design): pick an open investigation, the application matcher follows it and
 * can't be removed, the server adds the environment matcher; two approvers and the executor make it effective.
 */
export function SilenceProposalDrawer({ open, onClose, onSubmitted }: { open: boolean; onClose: () => void; onSubmitted: (s: SubmittedSilence) => void }) {
  const { tx } = useI18n();
  const qc = useQueryClient();
  const { items } = useOpenInvestigations(open);
  const [invId, setInvId] = useState("");
  const [extra, setExtra] = useState<{ k: string; op: string; v: string }[]>([]);
  const [duration, setDuration] = useState(3600);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [notConnected, setNotConnected] = useState(false);
  const [preview, setPreview] = useState<{ affects: number } | null>(null);
  const inv = items.find((i) => i.id === invId);
  useEffect(() => {
    if (!open) return;
    setExtra([]);
    setReason("");
    setDuration(3600);
    setPreview(null);
  }, [open]);
  useEffect(() => {
    if (open && !invId && items.length === 1) setInvId(items[0].id);
  }, [open, items]); // eslint-disable-line react-hooks/exhaustive-deps
  const matchers = useMemo(() => (inv?.appKey ? [{ k: "application", op: "=", v: inv.appKey }, ...extra] : extra), [inv, extra]);
  const complete = !!inv?.appKey && extra.every((m) => m.k.trim() && m.v.trim() && OPS.includes(m.op));
  useEffect(() => {
    if (!open || !complete) return setPreview(null);
    let live = true;
    const t = setTimeout(() => {
      api<{ affects: number }>("POST", "/silences/preview", { body: { matchers } }).then(
        (r) => live && setPreview(r),
        () => live && setPreview(null),
      );
    }, 300);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [open, complete, matchers]);
  const submit = async () => {
    setBusy(true);
    try {
      const r = await api<{ proposal_id: string; status: string }>("POST", "/silences", {
        body: { investigation_id: invId, matchers: matchers.map((m) => ({ name: m.k.trim(), op: m.op, value: m.v.trim() })), duration_seconds: duration, comment: reason.trim() },
      });
      onSubmitted({ proposal_id: r.proposal_id, matchers, duration, at: Date.now() });
      qc.invalidateQueries({ predicate: (x) => x.queryKey[0] === "/silences" });
      toast(
        <span>
          {tx("已提交静默提案 {p}，等待审批。批准并执行前告警不会被静默。", "Silence proposal {p} submitted. Nothing is silenced until it's approved and executed.", { p: r.proposal_id })}{" "}
          <Link to={`/review/${r.proposal_id}`} style={{ color: "inherit", fontWeight: 600, textDecoration: "underline" }}>
            {tx("查看提案", "View proposal")}
          </Link>
        </span>,
      );
      onClose();
    } catch (e) {
      if (e instanceof ApiError && e.status === 501) setNotConnected(true);
      else toast(e instanceof ApiError ? e.message : tx("提交失败，请稍后重试", "Couldn't submit; try again later"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };
  const fmtDur = (s: number) => (s < 3600 ? tx("{n} 分钟", "{n} min", { n: s / 60 }) : tx("{n} 小时", "{n} h", { n: s / 3600 }));
  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={420}
      title={tx("提议静默", "Propose a silence")}
      sub={tx("事件与告警 › 静默与抑制", "Events › Silences")}
      icon={<Tile icon="mute" tone="accent" size={30} />}
      footer={
        !notConnected && (
          <>
            <Muted size={11.5}>{tx("需要两人用通行密钥批准，执行器写入 Alertmanager 后才生效。", "Two approvers confirm with passkeys; it takes effect once the executor writes it to Alertmanager.")}</Muted>
            <Spacer />
            <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
            <Btn kind="primary" loading={busy} disabled={!invId || !complete || !reason.trim()} onClick={() => void submit()}>
              {tx("提交静默提案", "Submit proposal")}
            </Btn>
          </>
        )
      }
    >
      {notConnected ? (
        <Unavailable reason="not_connected" title={tx("静默执行未接入", "Silence execution isn't connected")} desc={tx("这个实例还没有开启静默执行器，暂时不能提交静默提案。现有静默照常显示。", "This instance hasn't enabled the silence executor, so proposals can't be submitted yet. Existing silences still show.")} icon="mute" />
      ) : (
        <Col gap={14} style={{ padding: 16 }}>
          <Section title={tx("关联的调查", "Investigation")}>
            <InvestigationPicker value={invId} onChange={setInvId} items={items} />
            {inv && (inv.appKey ? <Muted size={11.5}>{tx("提案会挂在这个调查下；应用 {a} 来自这个调查。", "The proposal hangs off this investigation; application {a} comes from it.", { a: inv.appKey })}</Muted> : <Muted size={11.5}>{tx("找不到这个调查所属应用的告警标签，暂时不能提议静默。", "This investigation's application has no alert label here, so it can't be silenced yet.")}</Muted>)}
          </Section>
          <Section title={tx("匹配条件", "Matchers")}>
            <Muted size={11.5}>{tx("最多 20 条。", "Up to 20.")}</Muted>
            {inv?.appKey && (
              <Row gap={6}>
                <Icon name="lock" size={12} color="var(--faint)" />
                <Badge mono>application</Badge>=<Badge mono>{inv.appKey}</Badge>
                <Muted size={11.5}>{tx("由调查决定", "From the investigation")}</Muted>
              </Row>
            )}
            <Row gap={6}>
              <Icon name="lock" size={12} color="var(--faint)" />
              {/* The server adds its own configured environment label; the page doesn't guess its name. */}
              <Muted size={11.5}>{tx("环境匹配条件由服务端按当前环境补上", "The server adds the environment matcher for this environment")}</Muted>
            </Row>
            {extra.map((m, i) => (
              <Row key={i} gap={6}>
                <Input value={m.k} placeholder="alertname" onChange={(e) => setExtra((ms) => ms.map((x, j) => (j === i ? { ...x, k: e.target.value } : x)))} width={120} aria-label={tx("标签", "Label")} />
                <ChipSelect label="" showLabel={false} value={m.op} onChange={(v) => setExtra((ms) => ms.map((x, j) => (j === i ? { ...x, op: v as string } : x)))} options={OPS} />
                <Input mono value={m.v} onChange={(e) => setExtra((ms) => ms.map((x, j) => (j === i ? { ...x, v: e.target.value } : x)))} style={{ flexGrow: 1 }} aria-label={tx("值", "Value")} />
                <IconBtn icon="x" label={tx("删除", "Remove")} size={24} onClick={() => setExtra((ms) => ms.filter((_, j) => j !== i))} />
              </Row>
            ))}
            {matchers.length < 20 && (
              <Btn kind="ghost" icon="plus" h={26} style={{ alignSelf: "flex-start" }} onClick={() => setExtra((ms) => [...ms, { k: "", op: "=", v: "" }])}>
                {tx("条件", "Matcher")}
              </Btn>
            )}
          </Section>
          <Section title={tx("时长", "Duration")}>
            <Segmented label={tx("时长", "Duration")} value={String(duration)} onChange={(v) => setDuration(Number(v))} options={DURATIONS.map((d) => ({ value: String(d), label: fmtDur(d) }))} />
          </Section>
          <Section title={tx("原因", "Reason")}>
            <Textarea rows={2} value={reason} onChange={(e) => setReason(e.target.value)} placeholder={tx("例如：发布回滚中，避免重复呼叫值班", "e.g. rollback in progress; avoid paging on-call again")} aria-label={tx("原因", "Reason")} style={{ minHeight: 60 }} />
          </Section>
          {preview && (
            <div className="unk-banner" role="status">
              <Icon name="info" size={14} />
              <span className="grow">{tx("影响预览：当前匹配 {n} 条告警。数字来自 Alertmanager 的只读预览。", "Preview: {n} alerts match right now (read-only Alertmanager preview).", { n: preview.affects })}</span>
            </div>
          )}
        </Col>
      )}
    </Drawer>
  );
}

/** Ending a silence early is a proposal too: investigation and reason are required. */
export function ExpireSilenceDialog({ silenceId, onClose, onSubmitted }: { silenceId: string | null; onClose: () => void; onSubmitted: (proposalId: string) => void }) {
  const { tx } = useI18n();
  const { items } = useOpenInvestigations(!!silenceId);
  const [invId, setInvId] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (silenceId) setReason("");
  }, [silenceId]);
  const submit = async () => {
    if (!silenceId) return;
    setBusy(true);
    try {
      const r = await api<{ proposal_id: string }>("DELETE", `/silences/${silenceId}`, { body: { investigation_id: invId, reason: reason.trim() } });
      onSubmitted(r.proposal_id);
      toast(
        <span>
          {tx("已提交结束提案 {p}。批准并执行前，这条静默继续生效。", "End proposal {p} submitted. The silence stays in effect until it's approved and executed.", { p: r.proposal_id })}{" "}
          <Link to={`/review/${r.proposal_id}`} style={{ color: "inherit", fontWeight: 600, textDecoration: "underline" }}>
            {tx("查看提案", "View proposal")}
          </Link>
        </span>,
      );
      onClose();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("提交失败，请稍后重试", "Couldn't submit; try again later"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!silenceId}
      onClose={onClose}
      title={tx("提议提前结束静默", "Propose ending the silence early")}
      footer={
        <>
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" loading={busy} disabled={!invId || !reason.trim()} onClick={() => void submit()}>{tx("提交结束提案", "Submit proposal")}</Btn>
        </>
      }
    >
      <Col gap={12}>
        <Muted>{tx("结束也要两人批准。批准并执行前，这条静默继续生效。", "Ending also needs two approvers. Until it's approved and executed, the silence stays in effect.")}</Muted>
        <Section title={tx("关联的调查", "Investigation")}>
          <InvestigationPicker value={invId} onChange={setInvId} items={items} />
        </Section>
        <Section title={tx("原因", "Reason")}>
          <Textarea rows={2} value={reason} onChange={(e) => setReason(e.target.value)} placeholder={tx("例如：回滚已完成，恢复告警", "e.g. rollback done; resume alerts")} style={{ minHeight: 56 }} />
        </Section>
      </Col>
    </Dialog>
  );
}
