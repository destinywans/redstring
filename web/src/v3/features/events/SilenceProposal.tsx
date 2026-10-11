import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import type { CanvasSummary, ServiceCatalog } from "../../api/types";
import { Badge, Btn, Col, Icon, IconBtn, Input, Muted, Row, Section, Segmented, Spacer, Textarea, Tile } from "../../ui/core";
import { Dialog, Drawer, toast } from "../../ui/overlays";
import { ChipSelect } from "../../ui/demo";
import { Unavailable } from "../../ui/unknown";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import { serviceApplicationId } from "../../api/ids";
import "./events.css";


/** Open investigations: the largest page the server serves (PA-85), newest update first. */
const OPEN_INVESTIGATIONS = { kind: "investigation", status: "active", page_size: 100 } as const;

export type OpenInvestigation = { id: string; title: string; application_id: string | undefined; appKey: string | null; updated_at: string; creator: string | null };

/** Investigation canvases as picker rows, with the application key the alert labels use (the service name). */
function useInvestigationRows(list: CanvasSummary[] | undefined, enabled = true): OpenInvestigation[] {
  const svc = useGet<ServiceCatalog>(enabled ? "/services" : null, undefined, { staleTime: 5 * 60_000 });
  return useMemo(() => {
    const appKey = new Map<string, string>();
    for (const s of svc.data?.items ?? []) {
      const app = serviceApplicationId(s.id);
      if (app) appKey.set(app, s.name);
    }
    return (list ?? [])
      .filter((c) => c.kind === "investigation" && !!c.investigation_id && !["done", "resolved", "closed"].includes(c.status))
      .map((c) => {
        const app = c.services[0];
        // participants holds the creator when one was recorded; older investigations have none.
        // Demo canvases name the service itself; real ones carry an opaque application ID.
        return { id: c.investigation_id!, title: c.title, application_id: app, appKey: app ? appKey.get(app) ?? (API_MODE === "mock" ? app : null) : null, updated_at: c.updated_at, creator: c.participants?.[0] ?? null };
      })
      .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
  }, [list, svc.data]);
}

/**
 * Open investigations. GET /investigations isn't implemented in V3, so they come from the investigation canvases
 * (GET /canvases?kind=investigation&status=active), whose services are application IDs. `more`: the server has more
 * than one page, so the picker searches it.
 */
export function useOpenInvestigations(enabled: boolean) {
  const inv = useGet<{ items: CanvasSummary[]; total?: number }>(enabled ? "/canvases" : null, OPEN_INVESTIGATIONS, { staleTime: 15_000 });
  const items = useInvestigationRows(inv.data?.items, enabled);
  const total = typeof inv.data?.total === "number" ? inv.data.total : items.length;
  const more = !!inv.data && total > inv.data.items.length;
  return { items, more, total, loading: inv.isLoading, error: inv.error };
}

export type SubmittedSilence = { proposal_id: string; matchers: { k: string; op: string; v: string }[]; duration: number; at: number };

const OPS = ["=", "!=", "=~", "!~"];
const DURATIONS = [1800, 3600, 14400, 43200, 86400];

/** `text` with the first case-insensitive match of `q` marked. */
function hl(text: string, q: string) {
  const i = q ? text.toLowerCase().indexOf(q) : -1;
  if (i < 0) return text;
  return (
    <>
      {text.slice(0, i)}
      <mark>{text.slice(i, i + q.length)}</mark>
      {text.slice(i + q.length)}
    </>
  );
}

/** More open investigations than this get a search box (approved design L2kEarHhdRadMRQuLYCGrP). */
const SEARCH_FROM = 8;

/**
 * Investigation picker shared by create and expire, rollback and new rule: open investigations only, newest update
 * first, each with "service · updated · creator". Past eight it searches by ID, title and service; when the server
 * has more than one page, the typed text also searches all open investigations there (q matches titles).
 */
export function InvestigationPicker({ value, onChange, items, more, total, accept }: { value: string; onChange: (id: string) => void; items: OpenInvestigation[]; more?: boolean; total?: number; /** Narrows server search results the way `items` was narrowed (e.g. one application). */ accept?: (i: OpenInvestigation) => boolean }) {
  const { tx, fmt } = useI18n();
  const [q, setQ] = useState("");
  const [dq, setDq] = useState("");
  useEffect(() => {
    const t = window.setTimeout(() => setDq(q.trim()), 250);
    return () => window.clearTimeout(t);
  }, [q]);
  const remote = useGet<{ items: CanvasSummary[] }>(more && dq ? "/canvases" : null, { ...OPEN_INVESTIGATIONS, q: dq }, { staleTime: 15_000 });
  const remoteRows = useInvestigationRows(remote.data?.items, !!(more && dq));
  // A pick from the server search stays listed after the menu closes.
  const [picked, setPicked] = useState<OpenInvestigation | null>(null);
  const searchable = items.length > SEARCH_FROM || !!more;
  const query = q.trim().toLowerCase();
  const all = useMemo(() => {
    const seen = new Set<string>();
    const out: OpenInvestigation[] = [];
    for (const i of [...items, ...(picked ? [picked] : [])]) if (!seen.has(i.id)) (seen.add(i.id), out.push(i));
    return out;
  }, [items, picked]);
  const shown = useMemo(() => {
    if (!query) return all;
    const local = all.filter((i) => i.id.toLowerCase().includes(query) || i.title.toLowerCase().includes(query) || (i.appKey ?? "").toLowerCase().includes(query));
    if (!more || !dq) return local;
    const seen = new Set(local.map((i) => i.id));
    return [...local, ...remoteRows.filter((i) => !seen.has(i.id) && (!accept || accept(i)))].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
  }, [all, query, more, dq, remoteRows, accept]);
  if (!items.length && !more)
    return (
      <Col gap={6} style={{ padding: "10px 12px", border: "1px solid var(--border)", borderRadius: 8 }}>
        <Muted size={12.5}>{tx("这个环境里没有开放的调查。静默必须挂在调查下，方便事后追溯。", "There is no open investigation in this environment. Silences hang off an investigation so they can be traced later.")}</Muted>
        <Btn h={26} style={{ alignSelf: "flex-start" }} to="/canvases?new=investigation">
          {tx("发起调查", "Start an investigation")}
        </Btn>
      </Col>
    );
  const sub = (i: OpenInvestigation) => [i.appKey, tx("更新于 {t}", "updated {t}", { t: fmt.ago(i.updated_at) }), i.creator].filter(Boolean).join(" · ");
  const searching = !!(more && query && (q.trim() !== dq || remote.isFetching));
  const count = total ?? all.length;
  // Long investigation titles are ellipsized so the chip stays inside the drawer; the full text is in the tooltip.
  return (
    <div className="inv-picker">
      <ChipSelect
        label={<span className="inv-label inv-placeholder">{tx("选择调查", "Choose an investigation")}</span>}
        ariaLabel={(() => {
          // Investigation IDs don't always carry an INV- prefix (e.g. ones started from an alert group).
          const cur = all.find((i) => i.id === value);
          return cur ? tx("关联的调查：{t}", "Investigation: {t}", { t: `${cur.id} · ${cur.title ?? ""}` }) : tx("关联的调查：未选择", "Investigation: none chosen");
        })()}
        showLabel={false}
        value={value || undefined}
        onChange={(v) => {
          const id = v as string;
          const row = shown.find((i) => i.id === id);
          if (row && !items.some((i) => i.id === id)) setPicked(row);
          onChange(id);
        }}
        options={shown.map((i) => ({
          value: i.id,
          label: (
            <span className="inv-label" title={`${i.id} · ${i.title ?? ""}`}>
              {hl(i.id, query)} · {hl(i.title ?? "", query)}
            </span>
          ),
          hint: sub(i) || undefined,
        }))}
        search={
          searchable
            ? {
                query: q,
                onQuery: setQ,
                placeholder: tx("搜索调查 ID、标题或服务", "Search ID, title or service"),
                // Without a total for this (narrowed) list while the server has more, only the matches are counted.
                count: searching
                  ? tx("在全部调查中搜索…", "Searching all…")
                  : !query
                    ? undefined
                    : more && total === undefined
                      ? tx("匹配 {n} 个", "{n} matches", { n: shown.length })
                      : tx("匹配 {n} 个，共 {t} 个", "{n} of {t}", { n: shown.length, t: count }),
                empty: searching ? undefined : (
                  <>
                    <span>{tx("没有找到包含“{q}”的进行中调查。", "No open investigation matches “{q}”.", { q: q.trim() })}</span>
                    <Btn h={26} to="/canvases?new=investigation">{tx("发起调查", "Start an investigation")}</Btn>
                  </>
                ),
              }
            : undefined
        }
        footer={
          searchable
            ? more
              ? tx("进行中的调查超过 100 个：输入后会在全部调查里搜索。", "Over 100 open investigations: typing searches all of them.")
              : tx("按最近更新排序 · 可输入调查 ID 前几位、标题或服务名", "Most recently updated first · type an ID prefix, a title or a service")
            : undefined
        }
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
  const { items, more, total } = useOpenInvestigations(open);
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
            <Gate need={["silences", "manage"]}>
              <Btn kind="primary" loading={busy} disabled={!invId || !complete || !reason.trim()} onClick={() => void submit()}>
                {tx("提交静默提案", "Submit proposal")}
              </Btn>
            </Gate>
          </>
        )
      }
    >
      {notConnected ? (
        <Unavailable reason="not_connected" title={tx("静默执行未接入", "Silence execution isn't connected")} desc={tx("这个实例还没有开启静默执行器，暂时不能提交静默提案。现有静默照常显示。", "This instance hasn't enabled the silence executor, so proposals can't be submitted yet. Existing silences still show.")} icon="mute" />
      ) : (
        <Col gap={14} style={{ padding: 16 }}>
          <Section title={tx("关联的调查", "Investigation")}>
            <InvestigationPicker value={invId} onChange={setInvId} items={items} more={more} total={total} />
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
  const { items, more, total } = useOpenInvestigations(!!silenceId);
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
          <Gate need={["silences", "manage"]}>
            <Btn kind="primary" loading={busy} disabled={!invId || !reason.trim()} onClick={() => void submit()}>{tx("提交结束提案", "Submit proposal")}</Btn>
          </Gate>
        </>
      }
    >
      <Col gap={12}>
        <Muted>{tx("结束也要两人批准。批准并执行前，这条静默继续生效。", "Ending also needs two approvers. Until it's approved and executed, the silence stays in effect.")}</Muted>
        <Section title={tx("关联的调查", "Investigation")}>
          <InvestigationPicker value={invId} onChange={setInvId} items={items} more={more} total={total} />
        </Section>
        <Section title={tx("原因", "Reason")}>
          <Textarea rows={2} value={reason} onChange={(e) => setReason(e.target.value)} placeholder={tx("例如：回滚已完成，恢复告警", "e.g. rollback done; resume alerts")} style={{ minHeight: 56 }} />
        </Section>
      </Col>
    </Dialog>
  );
}
