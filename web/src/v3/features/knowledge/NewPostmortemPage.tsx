import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, api, useGet } from "../../api/client";
import { AiAvatar, Avatar, Badge, Brand, Btn, Col, Dot, Icon, KV, Mono, Muted, Row, Sev, Skeleton, Spacer, Stepper, Strong, Textarea, TextLink, type Severity, type Tone } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./knowledge.css";

type Candidate = { id: string; title: string; severity: Severity; resolved: string; duration: string; evidence: number };
type Draft = {
  incident: { id: string; title: string; severity: Severity; window: string; duration: string };
  sections: { key: string; title: string; text: string }[];
  timeline: { at: string; text: string; tone: Tone }[];
  actions: { text: string; owner: string; due: string }[];
};

/** /postmortems/new — pick a resolved incident, the AI drafts from its timeline and evidence, a person reviews. */
export default function NewPostmortemPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const incident = params.get("incident");
  const cands = useGet<{ items: Candidate[] }>(incident ? null : "/postmortems/candidates");
  const { data, isLoading } = useGet<Draft>(incident ? "/postmortems/draft" : null, { incident });
  const [sections, setSections] = useState<Draft["sections"]>([]);
  const [actions, setActions] = useState<Draft["actions"]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  useEffect(() => {
    if (data) {
      setSections(data.sections);
      setActions(data.actions);
    }
  }, [data]);
  const outline: [string, "ok" | "warn" | "danger"][] = [
    [tx("摘要", "Summary"), "ok"], [tx("影响", "Impact"), "ok"], [tx("时间线", "Timeline"), "ok"], [tx("根因", "Root cause"), "ok"], [tx("促成因素", "Contributing factors"), "ok"],
    [tx("做得好的", "What went well"), "warn"], [tx("改进项", "Action items"), "ok"], [tx("客户影响", "Customer impact"), "danger"],
  ];
  const stateLabel = { ok: tx("AI 已起草", "AI drafted"), warn: tx("需补充", "Needs input"), danger: tx("缺数据", "Missing data") };
  const submit = async (mode: "draft" | "review") => {
    setBusy(mode);
    try {
      await api("POST", "/postmortems", { body: { incident_id: incident, sections, actions, status: mode } });
      toast(mode === "draft" ? tx("草稿已保存", "Draft saved") : tx("已提交审阅", "Submitted for review"));
      if (mode === "review") nav("/postmortems");
    } finally {
      setBusy(null);
    }
  };
  const steps = [tx("选择事件", "Pick incident"), tx("AI 起草", "AI draft"), tx("审阅", "Review"), tx("发布", "Publish")];
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
            {cands.data?.items.map((c) => (
              <button key={c.id} type="button" className="lift" onClick={() => setParams({ incident: c.id }, { replace: true })}>
                <Row gap={6}>
                  <Sev p={c.severity} />
                  <b>{c.id}</b>
                  <Spacer />
                  <Badge tone="ok">{tx("已解决", "Resolved")}</Badge>
                </Row>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{c.title}</span>
                <Muted size={11.5}>{tx("{r} · 持续 {d} · {n} 条证据", "{r} · {d} · {n} evidence", { r: c.resolved, d: c.duration, n: c.evidence })}</Muted>
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
                  <Sev p={data.incident.severity} />
                  <Strong size={13}>{data.incident.id}</Strong>
                  <Badge tone="ok">{tx("已解决", "Resolved")}</Badge>
                </Row>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{data.incident.title}</span>
                <Muted size={11.5}>{tx("{w} · 持续 {d}", "{w} · {d}", { w: data.incident.window, d: data.incident.duration })}</Muted>
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
            {isLoading || !data ? (
              <>
                <Skeleton h={28} w={420} />
                <Skeleton h={120} />
                <Skeleton h={160} />
              </>
            ) : (
              <>
                <Row gap={8}>
                  <h1 style={{ fontSize: 18, fontWeight: 600 }}>{tx("{id} 复盘：{t}", "{id} postmortem: {t}", { id: data.incident.id, t: data.incident.title })}</h1>
                  <Badge>{tx("草稿", "Draft")}</Badge>
                  <Badge tone="accent"><Icon name="sparkles" size={11} />{tx("AI 起草", "AI drafted")}</Badge>
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
                    <div><Muted size={11}>{tx("失败请求", "Failed requests")}</Muted><b style={{ color: "var(--danger)" }}>12k</b></div>
                    <div><Muted size={11}>{tx("持续", "Duration")}</Muted><b>{data.incident.duration}</b></div>
                    <div><Muted size={11}>{tx("错误预算消耗", "Error budget burned")}</Muted><b style={{ color: "var(--warn)" }}>23%</b></div>
                    <div><Muted size={11}>{tx("客户投诉", "Complaints")}</Muted><b className="muted">{tx("待补充", "To add")}</b></div>
                  </div>
                </div>
                <div className="pm-section">
                  <b>{tx("时间线", "Timeline")}</b>
                  <ol className="v3-col" style={{ listStyle: "none", gap: 6 }}>
                    {data.timeline.map((t) => (
                      <li key={t.at + t.text} className="v3-row" style={{ gap: 10, fontSize: "var(--fs-sm)" }}>
                        <Mono size={11.5} color="var(--muted)">{t.at}</Mono>
                        <Dot tone={t.tone} size={8} />
                        {t.text}
                      </li>
                    ))}
                  </ol>
                </div>
                <div className="pm-section">
                  <b>{tx("改进项", "Action items")}</b>
                  <div className="v3-card" style={{ overflow: "hidden" }}>
                    <table className="v3-table">
                      <thead>
                        <tr>
                          <th>{tx("事项", "Item")}</th>
                          <th>{tx("负责人", "Owner")}</th>
                          <th>{tx("期限", "Due")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {actions.map((a) => (
                          <tr key={a.text}>
                            <td>{a.text}</td>
                            <td><Row gap={6}>{a.owner === "AI" ? <AiAvatar size={18} /> : <Avatar name={a.owner} size={18} />}{a.owner}</Row></td>
                            <td className="muted">{a.due}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  <button type="button" className="f-add" onClick={() => setActions([...actions, { text: tx("新的改进项", "New action item"), owner: API_MODE === "mock" ? "王晨" : "", due: "—" }])}>
                    <Icon name="plus" size={12} />
                    {tx("改进项", "Action item")}
                  </button>
                </div>
              </>
            )}
          </div>
          <aside className="aside" style={{ width: 320 }}>
            <div className="v3-col" style={{ gap: 14, padding: 16 }}>
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
                <Muted size={11}>{tx("发布需要审阅通过后手动确认", "Publishing needs review and a manual confirmation")}</Muted>
              </Col>
              <Col gap={6}>
                <Muted size={11.5}>{tx("后续", "Follow-ups")}</Muted>
                <Btn icon="book" to={`/runbooks/new?source=postmortem&from=${encodeURIComponent(API_MODE === "mock" ? "INC-1042" : incident ?? "")}`} style={{ justifyContent: "flex-start" }}>{tx("生成 Runbook 修订", "Draft a runbook revision")}</Btn>
              </Col>
              <Row gap={8}>
                <Btn loading={busy === "draft"} onClick={() => submit("draft")}>{tx("保存草稿", "Save draft")}</Btn>
                <Btn kind="primary" icon="check" loading={busy === "review"} disabled={!data} onClick={() => submit("review")}>{tx("提交审阅", "Submit for review")}</Btn>
              </Row>
            </div>
          </aside>
        </div>
      )}
    </Page>
  );
}
