import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, newIdempotencyKey, useGet } from "../../api/client";
import { UnknownValue } from "../../ui/unknown";
import type { Incident } from "../../api/types";
import { AiAvatar, Avatar, Brand, Btn, Card, Chip, Col, IconBtn, Input, KV, Mono, Muted, Panel, Pills, Row, Sev, Skeleton, Spacer, State, Stepper, TextLink, cx, type Tone } from "../../ui/core";
import { MenuButton, toast } from "../../ui/overlays";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import { useCreateParam } from "../../ui/forms";
import { NewIncidentDrawer } from "./CreateDrawers";
import "./events.css";
import { useWhen } from "../../ui/when";

type Summary = Pick<Incident, "id" | "severity" | "title" | "status" | "duration" | "commander">;

export default function IncidentsPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const { id: routeId } = useParams();
  const [status, setStatus] = useState<"active" | "resolved" | "all">("active");
  const list = useGet<{ items: Summary[]; counts: Record<string, number> }>("/incidents", { status });
  // Without an id in the route, show the first incident of the current list (never a fixture id).
  const id = routeId ?? list.data?.items[0]?.id ?? "";
  const { data: inc, refetch } = useGet<Incident>(id ? `/incidents/${id}` : null);
  const demo = API_MODE === "mock";
  const [notes, setNotes] = useState<{ time: string; text: string }[]>([]);
  const [draft, setDraft] = useState("");
  const [stage, setStage] = useState<number | null>(null);
  const [creating, setCreating] = useCreateParam("incident");
  const STATUS: Record<Incident["status"], [Tone, string]> = {
    active: ["danger", tx("进行中", "Active")],
    mitigated: ["accent", tx("已缓解", "Mitigated")],
    resolved: ["ok", tx("已解决", "Resolved")],
  };
  const stages = [tx("发现", "Detected"), tx("确认", "Acknowledged"), tx("缓解", "Mitigated"), tx("解决", "Resolved"), tx("复盘", "Postmortem")];
  // Real records have no stage; the stepper follows the confirmed status instead.
  const cur = stage ?? inc?.stage ?? (inc ? { active: 1, mitigated: 2, resolved: 3 }[inc.status] : 0);
  const fmtTime = (t: string) => when.short(t) ?? t;
  const setStatus2 = async (to: "mitigated" | "resolved") => {
    if (!inc?.status_revision) return;
    try {
      await api("POST", `/incidents/${inc.id}/status`, { body: { status: to, expected_revision: inc.status_revision, request_id: newIdempotencyKey() } });
      void refetch();
      void list.refetch();
      toast(to === "mitigated" ? tx("已标记为已缓解", "Marked mitigated") : tx("已标记为已解决", "Marked resolved"));
    } catch (e) {
      void refetch();
      toast(e instanceof ApiError && (e.status === 409 || e.status === 412) ? tx("事件状态刚刚变化，已重新读取", "The incident just changed; it was reloaded") : e instanceof ApiError ? e.message : tx("更新失败，请稍后重试", "Couldn't update; try again later"), { tone: "danger" });
    }
  };
  const openInvestigation = async () => {
    if (!inc) return;
    if (inc.investigation_id) return nav(`/canvas/${inc.investigation_id}`);
    try {
      const r = await api<{ investigation_id?: string }>("POST", `/incidents/${inc.id}/investigation`, { body: {} });
      if (r.investigation_id) nav(`/canvas/${r.investigation_id}`);
      else void refetch();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("没能发起调查，请稍后重试", "Couldn't start an investigation; try again later"), { tone: "danger" });
    }
  };
  return (
    <Page flush crumbs={[tx("事件与告警", "Events"), tx("事件", "Incidents")]} actions={<Gate need={["events", "change"]}><Btn kind="primary" icon="plus" onClick={() => setCreating(true)}>{tx("新建事件", "New incident")}</Btn></Gate>}>
      <NewIncidentDrawer open={creating} onClose={() => setCreating(false)} />
      <div className="v3-split">
        <div className="side" style={{ width: 340 }}>
          <Row gap={8} style={{ padding: "10px 14px", borderBottom: "1px solid var(--border)" }}>
            <Pills value={status} onChange={setStatus} options={[{ value: "active", label: tx("进行中", "Active"), count: list.data?.counts.active }, { value: "resolved", label: tx("已解决", "Resolved") }, { value: "all", label: tx("全部", "All") }]} />
          </Row>
          <ul>
            {list.isLoading && <Skeleton h={60} style={{ margin: 14 }} />}
            {list.data?.items.map((it) => (
              <li key={it.id}>
                <button type="button" className={cx("inc-item", it.id === id && "on")} onClick={() => nav(`/incidents/${it.id}`)}>
                  <Row gap={6}>
                    <Sev p={it.severity} />
                    <Mono size={11.5}>{it.id}</Mono>
                    <Spacer />
                    <State tone={STATUS[it.status][0]}>{STATUS[it.status][1]}</State>
                  </Row>
                  <span className="ellipsis" style={{ fontWeight: 500, fontSize: "var(--fs-sm)" }}>
                    {it.title}
                  </span>
                  <Muted size={11.5}>
                    {it.duration ?? "—"} · {tx("指挥", "Commander")} {it.commander ?? "—"}
                  </Muted>
                </button>
              </li>
            ))}
          </ul>
        </div>
        <div className="main">
          {!inc ? (
            <Col gap={12} style={{ padding: 20 }}>
              <Skeleton h={28} />
              <Skeleton h={60} />
              <Skeleton h={300} />
            </Col>
          ) : (
            <div className="v3-content anim-fade" key={inc.id}>
              <Row gap={8} wrap>
                <Sev p={inc.severity} />
                <Mono>{inc.id}</Mono>
                <h2 style={{ fontSize: 17, fontWeight: 600 }}>{inc.title}</h2>
                <State tone={STATUS[inc.status][0]} pulse={inc.status === "active"}>
                  {STATUS[inc.status][1]}
                </State>
                <Spacer />
                {demo ? (
                  <MenuButton
                    items={stages.map((s, i) => ({ label: s, active: i === cur, onSelect: () => { setStage(i); toast(tx("状态已更新为「{s}」，已同步到飞书 #trade-incident", "Status set to “{s}” and posted to Feishu", { s })); } }))}
                    trigger={({ onClick, ref }) => (
                      <span ref={ref}>
                        <Chip caret onClick={onClick}>
                          {tx("更新状态", "Update status")}
                        </Chip>
                      </span>
                    )}
                  />
                ) : (
                  // Only human-confirmed cases have a status to move: active → mitigated → resolved.
                  inc.record_kind === "case" &&
                  inc.status !== "resolved" && (
                    <MenuButton
                      items={[
                        ...(inc.status === "active" ? [{ label: tx("标记为已缓解", "Mark mitigated"), onSelect: () => void setStatus2("mitigated") }] : []),
                        { label: tx("标记为已解决", "Mark resolved"), onSelect: () => void setStatus2("resolved") },
                      ]}
                      trigger={({ onClick, ref }) => (
                        <span ref={ref}>
                          <Chip caret onClick={onClick}>
                            {tx("更新状态", "Update status")}
                          </Chip>
                        </span>
                      )}
                    />
                  )
                )}
                {demo && (
                  <Btn icon="bell" onClick={() => toast(tx("已通知干系人（飞书、邮件）", "Stakeholders notified (Feishu, email)"))}>
                    {tx("通知干系人", "Notify stakeholders")}
                  </Btn>
                )}
                {demo ? (
                  <Btn kind="primary" icon="branch" to="/canvas/INV-231">
                    {tx("打开调查画布", "Open canvas")}
                  </Btn>
                ) : (
                  <Btn kind="primary" icon="branch" onClick={() => void openInvestigation()}>
                    {inc.investigation_id ? tx("打开调查画布", "Open canvas") : tx("发起调查", "Investigate")}
                  </Btn>
                )}
              </Row>
              <Card style={{ padding: "12px 16px" }}>
                <Stepper steps={stages} active={cur} />
              </Card>
              <div className="inc-grid">
                <Panel title={tx("时间线", "Timeline")}>
                  <ol className="inc-timeline">
                    {[...inc.timeline, ...notes.map((n) => ({ time: n.time, actor: { kind: "person" as const, name: "王晨" }, text: n.text, sub: undefined }))].map((e, i) => (
                      <li key={i} className={i >= inc.timeline.length ? "anim-rise" : undefined}>
                        <span className="tnum muted" style={{ width: 44, flex: "none", fontSize: "var(--fs-sub)", paddingTop: 2 }}>
                          {fmtTime(e.time)}
                        </span>
                        <span style={{ flex: "none", marginTop: 1 }}>{e.actor.kind === "brand" ? <Brand name={e.actor.name} size={20} /> : e.actor.kind === "ai" ? <AiAvatar size={20} /> : <Avatar name={e.actor.name} size={20} />}</span>
                        <Col gap={1}>
                          <span style={{ fontSize: "var(--fs-sm)" }}>{e.text}</span>
                          {e.sub && <Muted size={11.5}>{e.sub}</Muted>}
                        </Col>
                      </li>
                    ))}
                  </ol>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      if (!draft.trim()) return;
                      if (demo) {
                        const now = new Date();
                        setNotes((n) => [...n, { time: `${now.getHours()}:${String(now.getMinutes()).padStart(2, "0")}`, text: draft.trim() }]);
                        void api("POST", `/incidents/${inc.id}/timeline`, { body: { text: draft } });
                        setDraft("");
                        return;
                      }
                      // The note appears once the server has stored it, with its real author and time.
                      const text = draft.trim();
                      api("POST", `/incidents/${inc.id}/timeline`, { body: { text } }).then(
                        () => {
                          setDraft("");
                          void refetch();
                        },
                        (err) => toast(err instanceof ApiError ? err.message : tx("没能添加记录，请稍后重试", "Couldn't add the note; try again later"), { tone: "danger" }),
                      );
                    }}
                  >
                    <Input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={tx("补充一条时间线记录…", "Add a timeline note…")} aria-label={tx("补充时间线", "Add timeline note")} width="100%" right={<IconBtn icon="send" label={tx("添加", "Add")} size={24} submit />} />
                  </form>
                </Panel>
                <Col gap={12}>
                  <Panel title={tx("影响", "Impact")}>
                    <Col gap={8}>
                      <Row gap={6}>
                        {inc.impact.peak !== null ? <b className="tnum" style={{ color: "var(--danger)" }}>{inc.impact.peak}</b> : <UnknownValue reason="not_generated" />}
                        <Muted>{demo ? tx("5xx 峰值", "5xx peak") : tx("峰值", "Peak")}</Muted>
                        <Spacer />
                        {inc.impact.budget_used !== null ? <b className="tnum" style={{ color: "var(--warn)" }}>{inc.impact.budget_used}</b> : <UnknownValue reason="not_generated" />}
                        <Muted>{tx("本次消耗错误预算", "budget burned")}</Muted>
                      </Row>
                      {inc.impact.series && <Lines series={[{ values: inc.impact.series, color: "var(--c3)", fill: "var(--c3s)" }]} height={44} max={demo ? 10 : undefined} grid={1} hover={false} markers={demo ? [{ at: 10, label: tx("回滚", "rollback"), tone: "ok" }] : []} />}
                      {inc.impact.budget_left !== null && (
                        <div className="ctx-bar">
                          <span style={{ width: `${inc.impact.budget_left}%`, background: "var(--ok)" }} />
                          {demo && <span style={{ width: "12%", background: "var(--warn)" }} />}
                        </div>
                      )}
                      {inc.impact.note ? <Muted>{inc.impact.note}</Muted> : !demo && <Muted size={11.5}>{tx("影响还没有人确认", "No one has confirmed the impact yet")}</Muted>}
                    </Col>
                  </Panel>
                  <Panel title={tx("角色", "Roles")}>
                    <Col gap={8}>
                      {inc.roles.map((r) => (
                        <Row key={r.role} gap={6}>
                          <Muted>{r.role}</Muted>
                          <Spacer />
                          {r.ai ? <AiAvatar size={20} /> : <Avatar name={r.name} size={20} />}
                          <span style={{ fontSize: "var(--fs-sm)" }}>{r.name}</span>
                        </Row>
                      ))}
                    </Col>
                  </Panel>
                  <Panel title={tx("关联", "Links")}>
                    <Col gap={8}>
                      {inc.links.map((l) => (
                        <KV key={l.href} k={{ alert_group: tx("告警", "Alert"), investigation: tx("调查", "Investigation"), proposal: tx("提案", "Proposal"), change: tx("变更", "Change") }[l.kind as string] ?? l.kind} kw={44}>
                          <TextLink to={l.href}>{l.label}</TextLink>
                        </KV>
                      ))}
                    </Col>
                  </Panel>
                </Col>
              </div>
            </div>
          )}
        </div>
      </div>
    </Page>
  );
}
