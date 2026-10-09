import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { Incident } from "../../api/types";
import { AiAvatar, Avatar, Brand, Btn, Card, Chip, Col, IconBtn, Input, KV, Mono, Muted, Panel, Pills, Row, Sev, Skeleton, Spacer, State, Stepper, TextLink, cx, type Tone } from "../../ui/core";
import { MenuButton, toast } from "../../ui/overlays";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import { useCreateParam } from "../../ui/forms";
import { NewIncidentDrawer } from "./CreateDrawers";
import "./events.css";

type Summary = Pick<Incident, "id" | "severity" | "title" | "status" | "duration" | "commander">;

export default function IncidentsPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { id: routeId } = useParams();
  const [status, setStatus] = useState<"active" | "resolved" | "all">("active");
  const list = useGet<{ items: Summary[]; counts: Record<string, number> }>("/incidents", { status });
  // Without an id in the route, show the first incident of the current list (never a fixture id).
  const id = routeId ?? list.data?.items[0]?.id ?? "";
  const { data: inc } = useGet<Incident>(id ? `/incidents/${id}` : null);
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
  const cur = stage ?? inc?.stage ?? 0;
  return (
    <Page flush crumbs={[tx("事件与告警", "Events"), tx("事件", "Incidents")]} actions={<Btn kind="primary" icon="plus" onClick={() => setCreating(true)}>{tx("新建事件", "New incident")}</Btn>}>
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
                    {it.duration} · {tx("指挥", "Commander")} {it.commander}
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
                <Btn icon="bell" onClick={() => toast(tx("已通知干系人（飞书、邮件）", "Stakeholders notified (Feishu, email)"))}>
                  {tx("通知干系人", "Notify stakeholders")}
                </Btn>
                <Btn kind="primary" icon="branch" to="/canvas/INV-231">
                  {tx("打开调查画布", "Open canvas")}
                </Btn>
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
                          {e.time}
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
                      const now = new Date();
                      setNotes((n) => [...n, { time: `${now.getHours()}:${String(now.getMinutes()).padStart(2, "0")}`, text: draft.trim() }]);
                      void api("POST", `/incidents/${inc.id}/timeline`, { body: { text: draft } });
                      setDraft("");
                    }}
                  >
                    <Input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={tx("补充一条时间线记录…", "Add a timeline note…")} aria-label={tx("补充时间线", "Add timeline note")} width="100%" right={<IconBtn icon="send" label={tx("添加", "Add")} size={24} submit />} />
                  </form>
                </Panel>
                <Col gap={12}>
                  <Panel title={tx("影响", "Impact")}>
                    <Col gap={8}>
                      <Row gap={6}>
                        <b className="tnum" style={{ color: "var(--danger)" }}>{inc.impact.peak}</b>
                        <Muted>{tx("5xx 峰值", "5xx peak")}</Muted>
                        <Spacer />
                        <b className="tnum" style={{ color: "var(--warn)" }}>{inc.impact.budget_used}</b>
                        <Muted>{tx("本次消耗错误预算", "budget burned")}</Muted>
                      </Row>
                      <Lines series={[{ values: inc.impact.series, color: "var(--c3)", fill: "var(--c3s)" }]} height={44} max={10} grid={1} hover={false} markers={[{ at: 10, label: tx("回滚", "rollback"), tone: "ok" }]} />
                      <div className="ctx-bar">
                        <span style={{ width: `${inc.impact.budget_left}%`, background: "var(--ok)" }} />
                        <span style={{ width: "12%", background: "var(--warn)" }} />
                      </div>
                      <Muted>{inc.impact.note}</Muted>
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
