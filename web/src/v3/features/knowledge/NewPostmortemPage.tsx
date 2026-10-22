import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { AiAvatar, Badge, Brand, Btn, Col, Dot, Icon, KV, Mono, Muted, Row, Skeleton, Spacer, Stepper, Strong, Textarea, TextLink, type Severity, type Tone } from "../../ui/core";
import type { SeverityReason } from "../../api/types";
import { SeverityBadge } from "../events/peeks";
import { toast } from "../../ui/overlays";
import { Gate } from "../../ui/permission";
import { useI18n } from "../../i18n";
import "./knowledge.css";
import { useWhen } from "../../ui/when";
import { LoadError, PageUnavailable, UnknownValue } from "../../ui/unknown";
import { Callout } from "../../ui/forms";
import { PostmortemActions, cleanActions, useActionsProblem } from "./postmortemParts";

/** severity is null with severity_reason when the incident's recorded severity is unknown (PA-247). */
type Candidate = { id: string; title: string; severity: Severity | null; severity_reason?: SeverityReason; resolved: string; duration: string; evidence: number };
type Draft = {
  /** started_at / closed_at bound the incident (RFC 3339; null with `missing`, PA-83). */
  incident: { id: string; title: string; severity: Severity | null; severity_reason?: SeverityReason; duration: string | null; started_at: string | null; closed_at: string | null };
  sections: { key: string; title: string; text: string }[];
  timeline: { at: string; text: string; tone: Tone }[];
  actions: { text: string; owner: string; due: string }[];
  /** The previewed source; saving must echo it so a changed incident is caught (409), PA-127. */
  source_revision?: string;
  source_close_event_seq?: number;
};

/** /postmortems/new — pick a resolved incident, the AI drafts from its timeline and evidence, a person reviews. */
export default function NewPostmortemPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const incident = params.get("incident");
  const cands = useGet<{ items: Candidate[] }>(incident ? null : "/postmortems/candidates");
  const { data, isLoading, error: draftError } = useGet<Draft>(incident ? "/postmortems/draft" : null, { incident });
  const [sections, setSections] = useState<Draft["sections"]>([]);
  const [actions, setActions] = useState<Draft["actions"]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  useEffect(() => {
    if (data) {
      setSections(data.sections);
      setActions(data.actions);
    }
  }, [data]);
  // The sample shows a fixed AI outline; a real draft is a source outline (not AI-written), so the outline is its own
  // sections, marked by whether they have text yet (PA-129).
  const outline: [string, "ok" | "warn" | "danger"][] =
    API_MODE === "mock"
      ? [
          [tx("摘要", "Summary"), "ok"], [tx("影响", "Impact"), "ok"], [tx("时间线", "Timeline"), "ok"], [tx("根因", "Root cause"), "ok"], [tx("促成因素", "Contributing factors"), "ok"],
          [tx("做得好的", "What went well"), "warn"], [tx("改进项", "Action items"), "ok"], [tx("客户影响", "Customer impact"), "danger"],
        ]
      : sections.map((s) => [s.title, s.text.trim() ? "ok" : "warn"]);
  const stateLabel = { ok: API_MODE === "mock" ? tx("AI 已起草", "AI drafted") : tx("已填写", "Filled in"), warn: tx("需补充", "Needs input"), danger: tx("缺数据", "Missing data") };
  const actionsProblem = useActionsProblem(actions);
  const submit = async (mode: "draft" | "review") => {
    setBusy(mode);
    try {
      const r = await api<{ id: string }>("POST", "/postmortems", {
        body: { incident_id: incident, source_revision: data?.source_revision, source_close_event_seq: data?.source_close_event_seq, sections, actions: cleanActions(actions), status: mode },
      });
      toast(mode === "draft" ? tx("草稿已保存", "Draft saved") : tx("已提交审核", "Submitted for review"));
      // Either way the postmortem now exists: carry on on its own page, where saving again edits it (PA-128).
      nav(`/postmortems/${encodeURIComponent(r.id)}`, { replace: true });
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
    } finally {
      setBusy(null);
    }
  };
  const steps = [tx("选择事件", "Pick incident"), API_MODE === "mock" ? tx("AI 起草", "AI draft") : tx("起草", "Draft"), tx("审核", "Review"), tx("发布", "Publish")];
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), tx("复盘报告", "Postmortems"), tx("新建", "New")]} actions={incident && <Btn kind="ghost" onClick={() => setParams({}, { replace: true })}>{tx("换一个事件", "Pick another incident")}</Btn>}>
      <div style={{ padding: "12px 24px", borderBottom: "1px solid var(--border)", flex: "none" }}>
        <Stepper steps={steps} active={incident ? 1 : 0} />
      </div>
      {!incident ? (
        <div className="v3-content">
          <Strong size={14}>{tx("为哪个事件写复盘？", "Which incident is this postmortem for?")}</Strong>
          <Muted size={12.5}>{tx("只列出已解决、还没有复盘的事件。AI 会读取事件时间线、画布证据和作战群消息来起草。", "Only resolved incidents without a postmortem. The AI drafts from the timeline, canvas evidence and war-room messages.")}</Muted>
          <div className="pm-pick">
            {cands.isLoading && Array.from({ length: 3 }, (_, i) => <Skeleton key={i} h={96} />)}
            {/* PA-221: a failed candidate read says why instead of an empty list. */}
            {!cands.data && cands.error && <LoadError error={cands.error} source={tx("可复盘的事件", "incidents to review")} />}
            {cands.data?.items.map((c) => (
              <button key={c.id} type="button" className="lift" onClick={() => setParams({ incident: c.id }, { replace: true })}>
                <Row gap={6}>
                  <SeverityBadge p={c.severity} reason={c.severity_reason} />
                  <b>{c.id}</b>
                  <Spacer />
                  <Badge tone="ok">{tx("已解决", "Resolved")}</Badge>
                </Row>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{c.title}</span>
                <Muted size={11.5}>{tx("{r} · 持续 {d} · {n} 条证据", "{r} · {d} · {n} evidence", { r: when.at(c.resolved), d: c.duration, n: c.evidence })}</Muted>
              </button>
            ))}
          </div>
        </div>
      ) : (
        <div className="pm-layout">
          <aside className="pm-left">
            {data && (
              <div className="v3-card" style={{ padding: 12, display: "flex", flexDirection: "column", gap: 6 }}>
                <Row gap={6}>
                  <SeverityBadge p={data.incident.severity} reason={data.incident.severity_reason} />
                  <Badge tone="ok">{tx("已解决", "Resolved")}</Badge>
                </Row>
                <Mono size={11.5} style={{ overflowWrap: "anywhere" }}>{data.incident.id}</Mono>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{data.incident.title}</span>
                <Muted size={11.5}>{tx("{w} · 持续 {d}", "{w} · {d}", {
                    w: data.incident.started_at
                      ? `${when.at(data.incident.started_at)} – ${data.incident.closed_at ? (when.date(data.incident.closed_at) === when.date(data.incident.started_at) ? when.time(data.incident.closed_at) : when.at(data.incident.closed_at)) : "—"}`
                      : "—",
                    d: data.incident.duration ?? "—",
                  })}</Muted>
              </div>
            )}
            <Col gap={6}>
              <Muted size={11.5}>{tx("素材", "Materials")}</Muted>
              <KV k={tx("时间线", "Timeline")} kw={56}>{tx("{n} 条", "{n}", { n: data?.timeline.length ?? 0 })}</KV>
              {/* Evidence / war-room counts and the canvas link are sample values; the draft contract doesn't report them. */}
              {API_MODE === "mock" && (
                <>
                  <KV k={tx("证据", "Evidence")} kw={56}>{tx("12 条", "12")}</KV>
                  <KV k={tx("作战群", "War room")} kw={56}>{tx("飞书 · 146 条消息", "Feishu · 146 messages")}</KV>
                  <KV k={tx("画布", "Canvas")} kw={56}><TextLink to="/canvas/INV-231">INV-231</TextLink></KV>
                </>
              )}
            </Col>
            <Col gap={6}>
              <Muted size={11.5}>{tx("章节", "Sections")}</Muted>
              <ul className="pm-outline">
                {outline.map(([n, t]) => (
                  <li key={n}>
                    <Dot tone={t} size={7} />
                    {n}
                    <Spacer />
                    <Muted size={11}>{stateLabel[t]}</Muted>
                  </li>
                ))}
              </ul>
            </Col>
          </aside>
          <div className="pm-center">
            {!data && draftError instanceof ApiError && draftError.code === "postmortem_exists" && typeof draftError.detail.postmortem_id === "string" ? (
              <Callout icon="file" action={<Btn kind="primary" to={`/postmortems/${encodeURIComponent(draftError.detail.postmortem_id)}`}>{tx("打开复盘", "Open the postmortem")}</Btn>}>
                {tx("这个事件已经有复盘了，在复盘页里继续编辑或审核。", "This incident already has a postmortem; carry on editing or reviewing it there.")}
              </Callout>
            ) : !data && draftError instanceof ApiError && draftError.status === 404 ? (
              // The server answers 404 for an incident that isn't resolved or already has a postmortem (PA-221).
              <PageUnavailable icon="search" configure={false} title={tx("这个事件不能起草复盘", "This incident can't be drafted")} desc={tx("只有已解决、还没有复盘的事件可以起草；它也可能在当前环境里看不到。", "Only resolved incidents without a postmortem can be drafted; it may also not be visible in this environment.")} />
            ) : !data && draftError ? (
              <LoadError error={draftError} source={tx("复盘草稿", "the postmortem draft")} />
            ) : isLoading || !data ? (
              <>
                <Skeleton h={28} w={420} />
                <Skeleton h={120} />
                <Skeleton h={160} />
              </>
            ) : (
              <>
                <Row gap={8}>
                  <h1 style={{ fontSize: 18, fontWeight: 600 }}>{tx("复盘：{t}", "Postmortem: {t}", { t: data.incident.title })}</h1>
                  <Badge>{tx("草稿", "Draft")}</Badge>
                  {API_MODE === "mock" && <Badge tone="accent"><Icon name="sparkles" size={11} />{tx("AI 起草", "AI drafted")}</Badge>}
                </Row>
                {sections.map((s, i) => (
                  <div key={s.key} className="pm-section">
                    <b>{s.title}</b>
                    <Textarea rows={3} value={s.text} onChange={(e) => setSections(sections.map((x, j) => (j === i ? { ...x, text: e.target.value } : x)))} />
                  </div>
                ))}
                <div className="pm-section">
                  <b>{tx("影响指标", "Impact metrics")}</b>
                  <div className="pm-kpis">
                    {/* The draft has no impact metrics; failed requests and budget burn are sample values (PA-129). */}
                    <div><Muted size={11}>{tx("失败请求", "Failed requests")}</Muted>{API_MODE === "mock" ? <b style={{ color: "var(--danger)" }}>12k</b> : <b><UnknownValue reason="not_generated" /></b>}</div>
                    <div><Muted size={11}>{tx("持续", "Duration")}</Muted><b>{data.incident.duration ?? "—"}</b></div>
                    <div><Muted size={11}>{tx("错误预算消耗", "Error budget burned")}</Muted>{API_MODE === "mock" ? <b style={{ color: "var(--warn)" }}>23%</b> : <b><UnknownValue reason="not_generated" /></b>}</div>
                    <div><Muted size={11}>{tx("客户投诉", "Complaints")}</Muted><b className="muted">{tx("待补充", "To add")}</b></div>
                  </div>
                </div>
                <div className="pm-section">
                  <b>{tx("时间线", "Timeline")}</b>
                  <ol className="v3-col" style={{ listStyle: "none", gap: 6 }}>
                    {data.timeline.map((t) => (
                      <li key={t.at + t.text} className="v3-row" style={{ gap: 10, fontSize: "var(--fs-sm)" }}>
                        <Mono size={11.5} color="var(--muted)">{when.at(t.at) ?? t.at}</Mono>
                        <Dot tone={t.tone} size={8} />
                        {t.text}
                      </li>
                    ))}
                  </ol>
                </div>
                <div className="pm-section">
                  <b>{tx("改进项", "Action items")}</b>
                  <PostmortemActions actions={actions} onChange={setActions} />
                </div>
              </>
            )}
          </div>
          <aside className="aside" style={{ width: 320 }}>
            <div className="v3-col" style={{ gap: 14, padding: 16 }}>
              {/* AI checks, publish targets and the runbook follow-up are sample content: a real draft is not AI-checked,
                  nothing publishes to Confluence or Feishu, and a runbook needs a published postmortem (PA-129). */}
              {API_MODE === "mock" && (
                <>
                  <Row gap={8}>
                    <AiAvatar size={24} />
                    <Strong size={13}>{tx("复盘检查", "Postmortem checks")}</Strong>
                  </Row>
                  <Col gap={8}>
                    <Row gap={6}><Icon name="check" size={14} color="var(--ok)" /><span style={{ fontSize: "var(--fs-sm)" }}>{tx("没有归咎个人（无责表述）", "Blameless wording")}</span></Row>
                    <Row gap={6}><Icon name="check" size={14} color="var(--ok)" /><span style={{ fontSize: "var(--fs-sm)" }}>{tx("根因引用了证据（2 条）", "Root cause cites evidence (2)")}</span></Row>
                    <Row gap={6}><Icon name="check" size={14} color="var(--ok)" /><span style={{ fontSize: "var(--fs-sm)" }}>{tx("每个改进项都有负责人和期限", "Every action has an owner and due date")}</span></Row>
                    <Row gap={6} align="flex-start"><Icon name="alert" size={14} color="var(--warn)" /><span style={{ fontSize: "var(--fs-sm)" }}>{tx("缺少客户影响：需要客服工单数据", "Customer impact missing: needs support ticket data")}</span></Row>
                  </Col>
                  <Col gap={8}>
                    <Muted size={11.5}>{tx("发布到", "Publish to")}</Muted>
                    <Row gap={8}><Brand name="confluence" size={22} /><span style={{ fontSize: "var(--fs-sm)" }}>{tx("Confluence · SRE 手册 / 复盘", "Confluence · SRE handbook / Postmortems")}</span></Row>
                    <Row gap={8}><Brand name="feishu" size={22} /><span style={{ fontSize: "var(--fs-sm)" }}>{tx("飞书 · 交易平台群摘要", "Feishu · trading group summary")}</span></Row>
                    <Muted size={11}>{tx("发布需要审核通过后手动确认", "Publishing needs review and a manual confirmation")}</Muted>
                  </Col>
                  <Col gap={6}>
                    <Muted size={11.5}>{tx("后续", "Follow-ups")}</Muted>
                    <Btn icon="book" to="/runbooks/new?source=postmortem&from=INC-1042" style={{ justifyContent: "flex-start" }}>{tx("生成 Runbook 修订", "Draft a runbook revision")}</Btn>
                  </Col>
                </>
              )}
              <Row gap={8}>
                <Gate need={["knowledge", "manage"]}>
                  <Btn loading={busy === "draft"} disabled={!data || !!actionsProblem || !!busy} onClick={() => submit("draft")}>{tx("保存草稿", "Save draft")}</Btn>
                </Gate>
                <Gate need={["knowledge", "manage"]}>
                  <Btn kind="primary" icon="check" loading={busy === "review"} disabled={!data || !!actionsProblem || sections.some((x) => !x.text.trim()) || !!busy} onClick={() => submit("review")}>{tx("提交审核", "Submit for review")}</Btn>
                </Gate>
              </Row>
              {data && (actionsProblem || sections.some((x) => !x.text.trim())) && (
                <Muted size={12} style={{ color: "var(--warn)" }}>
                  {actionsProblem ?? tx("提交审核前每个章节都要填写；可以先保存草稿。", "Every section needs text before review; you can save a draft first.")}
                </Muted>
              )}
            </div>
          </aside>
        </div>
      )}
    </Page>
  );
}
