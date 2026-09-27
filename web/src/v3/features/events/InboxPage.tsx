import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { AlertDetail, InboxItem } from "../../api/types";
import { Badge, Brand, Btn, Card, Col, Dot, Icon, IconBtn, KV, LabelChip, Muted, Panel, Row, Sev, Skeleton, Spacer, State, Tabs, Tile, TextLink, Kbd, cx, type IconName, type Tone } from "../../ui/core";
import { MenuButton, toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import "./events.css";

const KIND_ICON: Record<InboxItem["kind"], IconName> = { alert: "alert", approval: "shield", mention: "message", runbook: "book", report: "file", access: "user", agent: "cpu" };

export function AlertDetailView({ id }: { id: string }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { data: a } = useGet<AlertDetail>(`/alerts/${id}`);
  const [claimed, setClaimed] = useState(false);
  if (!a)
    return (
      <Col gap={12} style={{ padding: 20 }}>
        <Skeleton h={28} w={420} />
        <Skeleton h={90} />
        <Skeleton h={200} />
      </Col>
    );
  return (
    <div className="alert-detail anim-fade" key={id}>
      <Row gap={8} wrap>
        <Sev p={a.severity} />
        <h2 style={{ fontSize: 16, fontWeight: 600 }}>{a.title}</h2>
        <State tone="danger" pulse>
          {tx("触发中", "Firing")}
        </State>
        <Spacer />
        <Btn
          kind={claimed ? "secondary" : "primary"}
          icon="check"
          onClick={async () => {
            await api("POST", `/alerts/${a.id}/claim`);
            setClaimed(true);
            toast(tx("已认领，值班升级已停止", "Claimed — escalation stopped"), { undo: () => setClaimed(false) });
          }}
        >
          {claimed ? tx("已认领", "Claimed") : tx("认领", "Claim")}
        </Btn>
        <Btn icon="branch" to={`/canvas/${a.triage?.investigation_id ?? "INV-231"}`}>
          {tx("打开调查", "Open investigation")}
        </Btn>
        <Btn icon="clock" to="/silences?new=checkout-api">
          {tx("静默 1 小时", "Silence 1 h")}
        </Btn>
        <MenuButton
          items={[
            { label: tx("创建事件", "Create incident"), icon: "alert", onSelect: () => nav("/incidents/INC-1042") },
            { label: tx("复制链接", "Copy link"), icon: "link", onSelect: () => toast(tx("链接已复制", "Link copied")) },
            { label: tx("在 Alertmanager 中打开", "Open in Alertmanager"), icon: "external" },
          ]}
          trigger={({ onClick, ref }) => (
            <span ref={ref}>
              <IconBtn icon="more" label={tx("更多操作", "More")} onClick={onClick} />
            </span>
          )}
        />
      </Row>
      <Muted>
        {a.source} · {tx("生产", "Prod")} · {a.since} · {tx("规则「{r}」", "Rule “{r}”", { r: a.rule })}
      </Muted>
      {a.triage && (
        <div className="ai-triage">
          <Icon name="sparkles" size={16} color="var(--accent-text)" style={{ marginTop: 2 }} />
          <Col gap={6} grow>
            <Row gap={8} wrap>
              <b style={{ fontSize: "var(--fs-sm)", color: "var(--accent-text)" }}>{tx("AI 初查", "AI triage")}</b>
              <Badge tone="accent">{tx("置信度 高", "Confidence high")}</Badge>
              <Muted size={11.5}>{tx("{a} · 用时 {d} 秒 · {t} 次工具调用", "{a} · {d}s · {t} tool calls", { a: a.triage.agent, d: a.triage.duration_s, t: a.triage.tools })}</Muted>
            </Row>
            <Markdown text={a.triage.summary} />
            <Row gap={6}>
              <Btn h={26} icon="branch" to={`/canvas/${a.triage.investigation_id}`}>
                {tx("查看调查 {i}", "View {i}", { i: a.triage.investigation_id })}
              </Btn>
              {a.triage.proposal_id && (
                <Btn h={26} kind="ghost" icon="shield" to={`/review/${a.triage.proposal_id}`}>
                  {tx("查看回滚提案", "View rollback proposal")}
                </Btn>
              )}
            </Row>
          </Col>
        </div>
      )}
      <div className="grid-2">
        <Panel title={tx("告警值 · 5xx 比例", "Value · 5xx ratio")}>
          <Lines series={[{ name: "5xx", values: a.series.values, color: "var(--c3)", fill: "var(--c3s)" }]} height={96} max={10} threshold={a.series.threshold} markers={a.series.release_at !== undefined ? [{ at: a.series.release_at, label: "v2.8.1", tone: "warn" }] : []} labels={a.series.labels} format={(v) => `${v}%`} grid={2} />
          <Row gap={6} style={{ marginTop: 8 }}>
            <Muted>{tx("阈值 1% · 持续 5 分钟触发", "Threshold 1% · fires after 5 min")}</Muted>
            <Spacer />
            <TextLink icon="external" onClick={() => window.open("https://grafana.example.internal", "_blank", "noopener")}>
              {tx("在 Grafana 中打开", "Open in Grafana")}
            </TextLink>
          </Row>
        </Panel>
        <Panel title={tx("标签与说明", "Labels & summary")}>
          <Col gap={8}>
            <Row gap={6} wrap>
              {a.labels.map((x) => (
                <LabelChip key={x.k} k={x.k} v={x.v} />
              ))}
            </Row>
            <KV k={tx("摘要", "Summary")} kw={52}>
              {a.summary}
            </KV>
            {a.runbook && (
              <KV k="Runbook" kw={52}>
                <TextLink icon="book" to={`/runbooks/${a.runbook.id}`}>
                  {a.runbook.title}
                </TextLink>
              </KV>
            )}
          </Col>
        </Panel>
      </div>
      <div className="grid-2">
        <Panel title={tx("通知记录", "Notifications")}>
          <ul className="notif-list">
            {a.notifications.map((n) => (
              <li key={n.time}>
                <span className="tnum muted" style={{ width: 60, flex: "none" }}>
                  {n.time}
                </span>
                {n.channel === "feishu" ? <Brand name="feishu" size={20} /> : <Icon name={n.channel === "sms" ? "message" : "rotate"} size={15} color="var(--muted)" />}
                <span className="grow">{n.text}</span>
                <Icon name={n.done ? "check" : "clock"} size={14} color={n.done ? "var(--ok)" : "var(--muted)"} />
              </li>
            ))}
          </ul>
        </Panel>
        <Panel title={tx("分组与关联", "Grouping & links")}>
          <Col gap={9}>
            <KV k={tx("分组", "Group")} kw={44}>
              <TextLink to="/alerts">{a.grouping.group}</TextLink>
            </KV>
            <KV k={tx("抑制", "Inhibits")} kw={44}>
              {a.grouping.inhibited}
            </KV>
            <KV k={tx("事件", "Incident")} kw={44}>
              <TextLink to={`/incidents/${a.grouping.incident.id}`}>{a.grouping.incident.label}</TextLink>
            </KV>
            <KV k={tx("变更", "Change")} kw={44}>
              <Row gap={6}>
                <Brand name="gitlab" size={20} />
                <TextLink to="/changes">{a.grouping.change.label}</TextLink>
              </Row>
            </KV>
          </Col>
        </Panel>
      </div>
    </div>
  );
}

export default function InboxPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const [tab, setTab] = useState<"all" | "mine" | "mentions" | "subscribed">("all");
  const { data, isLoading } = useGet<{ items: InboxItem[]; counts: Record<string, number> }>("/inbox", { tab });
  const [read, setRead] = useState<Set<string>>(new Set());
  const items = useMemo(() => data?.items ?? [], [data]);
  const selId = params.get("item") ?? "i1";
  const sel = items.find((i) => i.id === selId);
  const select = (it: InboxItem) => {
    setRead((r) => new Set(r).add(it.id));
    if (it.kind === "alert") setParams({ item: it.id }, { replace: true });
    else nav(it.href);
  };
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement).closest("input,textarea")) return;
      if (e.key !== "j" && e.key !== "k") return;
      const alerts = items.filter((i) => i.kind === "alert");
      const idx = alerts.findIndex((i) => i.id === selId);
      const next = alerts[Math.max(0, Math.min(alerts.length - 1, idx + (e.key === "j" ? 1 : -1)))];
      if (next) setParams({ item: next.id }, { replace: true });
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [items, selId, setParams]);
  const days: [InboxItem["day"], string][] = [
    ["today", tx("今天", "Today")],
    ["yesterday", tx("昨天", "Yesterday")],
  ];
  return (
    <Page
      flush
      crumbs={[tx("事件与告警", "Events"), tx("收件箱", "Inbox")]}
      actions={
        <Btn kind="ghost" icon="check" onClick={() => { setRead(new Set(items.map((i) => i.id))); toast(tx("已全部标为已读", "All marked as read")); }}>
          {tx("全部标为已读", "Mark all read")}
        </Btn>
      }
    >
      <div className="v3-split">
        <div className="side" style={{ width: 420 }}>
          <Tabs
            value={tab}
            onChange={setTab}
            pad="0 16px"
            options={[
              { value: "all", label: tx("全部", "All"), count: data?.counts.all },
              { value: "mine", label: tx("待我处理", "For me"), count: data?.counts.mine },
              { value: "mentions", label: tx("提及", "Mentions"), count: data?.counts.mentions },
              { value: "subscribed", label: tx("订阅", "Subscribed") },
            ]}
          />
          {isLoading && (
            <Col gap={10} style={{ padding: 14 }}>
              {Array.from({ length: 6 }, (_, i) => (
                <Skeleton key={i} h={44} />
              ))}
            </Col>
          )}
          {days.map(([d, label]) => {
            const list = items.filter((i) => i.day === d);
            if (!list.length) return null;
            return (
              <div key={d}>
                <div className="inbox-day">{label}</div>
                <ul>
                  {list.map((it) => {
                    const unread = it.unread && !read.has(it.id);
                    return (
                      <li key={it.id}>
                        <button type="button" className={cx("inbox-item", it.id === selId && it.kind === "alert" && "on")} onClick={() => select(it)}>
                          <Tile icon={KIND_ICON[it.kind]} tone={it.tone as Tone} size={26} />
                          <Col gap={1} grow>
                            <span className="ellipsis" style={{ fontSize: "var(--fs-sm)", fontWeight: unread ? 600 : 400 }}>
                              {it.title}
                            </span>
                            <span className="ellipsis muted" style={{ fontSize: "var(--fs-sub)" }}>
                              {it.sub}
                            </span>
                          </Col>
                          <Col gap={6} align="flex-end">
                            <Muted size={11.5}>{it.time}</Muted>
                            {unread && <Dot tone="accent" size={7} />}
                          </Col>
                        </button>
                      </li>
                    );
                  })}
                </ul>
              </div>
            );
          })}
          <Row gap={6} style={{ padding: "10px 16px", fontSize: "var(--fs-sub)" }} className="muted">
            <Kbd>J</Kbd>
            <Kbd>K</Kbd>
            {tx("上下切换告警", "move between alerts")}
          </Row>
        </div>
        <div className="main">{sel?.alert_id ? <AlertDetailView id={sel.alert_id} /> : <Card style={{ margin: 20, padding: 20 }}><Muted>{tx("选择一条告警查看详情", "Select an alert to see details")}</Muted></Card>}</div>
      </div>
    </Page>
  );
}
