import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { LoadError, PartialBanner, Unavailable, UnknownValue, errorReason } from "../../ui/unknown";
import type { AlertDetail, InboxItem, Incident } from "../../api/types";
import { Badge, Brand, Btn, Card, Col, Dot, Icon, IconBtn, KV, LabelChip, Muted, Panel, Row, Sev, Skeleton, Spacer, State, Tabs, Tile, TextLink, Kbd, cx, type IconName, type Tone } from "../../ui/core";
import { MenuButton, toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import { useCurrentEnv } from "../../app/EnvSwitch";
import "./events.css";
import { openExternal, copyText } from "../../ui/demo";
import { Gate } from "../../ui/permission";
import { useWhen, parseStamp } from "../../ui/when";

type Day = "today" | "yesterday" | "earlier";
const KIND_ICON: Record<InboxItem["kind"], IconName> = { alert: "alert", approval: "shield", mention: "message", runbook: "book", report: "file", access: "user", agent: "cpu", assignment: "pin" };

export function AlertDetailView({ id }: { id: string }) {
  const { tx } = useI18n();
  const when = useWhen();
  const curEnv = useCurrentEnv();
  const nav = useNavigate();
  const { data: a, error, refetch } = useGet<AlertDetail>(`/alerts/${id}`);
  // PA-166: the real alert links its incident by ID only; the link reads the incident's title, or the ID when the
  // incident can't be read.
  const linked = API_MODE !== "mock" && a?.grouping.incident.id && !a.grouping.incident.label ? a.grouping.incident.id : null;
  const { data: incident } = useGet<Incident>(linked ? `/incidents/${linked}` : null);
  const [claimed, setClaimed] = useState(false);
  const [starting, setStarting] = useState(false);
  const demo = API_MODE === "mock";
  // A failed read (another environment's alert, a down source) says why instead of loading forever (PA-221).
  if (!a && error) return <LoadError error={error} source={tx("告警记录", "alert records")} />;
  if (!a)
    return (
      <Col gap={12} style={{ padding: 20 }}>
        <Skeleton h={28} w={420} />
        <Skeleton h={90} />
        <Skeleton h={200} />
      </Col>
    );
  const isClaimed = claimed || !!a.assignee_id;
  const investigation = a.triage?.investigation_id ?? a.investigation_id;
  const STATE = { firing: ["danger", tx("触发中", "Firing")], pending: ["warn", tx("待触发", "Pending")], resolved: ["ok", tx("已恢复", "Resolved")] } as const;
  const claim = async () => {
    try {
      // The claim is guarded by the row revision from GET; a stale one is 412.
      await api("POST", `/alerts/${a.id}/claim`, demo ? undefined : { body: { expected_revision: a.revision } });
      setClaimed(true);
      if (!demo) void refetch();
      // V3 has no on-call escalation to stop; only the sample says so (PA-117).
      toast(demo ? tx("已认领，值班升级已停止", "Claimed — escalation stopped") : tx("已认领", "Claimed"), demo ? { undo: () => setClaimed(false) } : {});
    } catch (e) {
      if (e instanceof ApiError && e.status === 412) {
        void refetch();
        return toast(tx("告警状态刚刚变化，已重新读取，请再试一次", "The alert just changed; it was reloaded — try again"), { tone: "danger" });
      }
      if (e instanceof ApiError && e.code === "already_claimed_or_closed") {
        void refetch();
        return toast(tx("这条告警已被他人认领或已经结束", "Someone else claimed this alert, or it has ended"), { tone: "danger" });
      }
      toast(e instanceof ApiError ? e.message : tx("认领失败，请稍后重试", "Couldn't claim it; try again later"), { tone: "danger" });
    }
  };
  // Start the investigation of the alert's group, as the alert page does, so it is linked to the alert (PA-167).
  const investigate = async () => {
    if (!a.group_id) return toast(tx("这条告警不属于任何告警组，请在告警页或事件页发起调查", "This alert belongs to no alert group; start the investigation from the Alerts or Incidents page"), { tone: "danger" });
    setStarting(true);
    try {
      const r = await api<{ results: { outcome: string; investigation_id: string | null }[] }>("POST", "/alert-groups/bulk", {
        body: { action: "investigate", ids: [a.group_id] },
        headers: { "X-AIOps-Environment": a.env },
      });
      const item = r.results[0];
      if (item?.investigation_id && ["investigating", "already_investigating"].includes(item.outcome)) return nav(`/canvas/${item.investigation_id}`);
      const why: Record<string, string> = {
        conflict: tx("这组告警已恢复，或历史记录关联了不同的调查，请从事件页发起调查", "The group has resolved, or its past alerts belong to different investigations; start one from the incident page"),
        not_found: tx("告警组不存在，或你没有处理它的权限", "The group wasn't found, or you can't act on it"),
        session_revoked: tx("会话已失效，请重新登录", "Your session ended; sign in again"),
        human_required: tx("需要本人登录操作", "This needs a signed-in person"),
        group_too_large: tx("告警组太大", "The group is too large"),
      };
      toast(why[item?.outcome ?? ""] ?? tx("没能发起调查，请稍后重试", "Couldn't start the investigation; try again later"), { tone: "danger" });
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("没能发起调查，请稍后重试", "Couldn't start the investigation; try again later"), { tone: "danger" });
    } finally {
      setStarting(false);
    }
  };
  return (
    <div className="alert-detail anim-fade" key={id}>
      <Row gap={8} wrap>
        <Sev p={a.severity} />
        <h2 style={{ fontSize: 16, fontWeight: 600 }}>{a.title}</h2>
        <State tone={STATE[a.state][0]} pulse={a.state === "firing"}>
          {STATE[a.state][1]}
        </State>
        <Spacer />
        {/* A resolved alert can't be claimed (409); say so up front instead (PA-118). */}
        <Gate need={["events", "change"]}>
          <Btn
            kind={isClaimed ? "secondary" : "primary"}
            icon="check"
            disabled={!demo && (isClaimed || a.state === "resolved")}
            title={!demo && !isClaimed && a.state === "resolved" ? tx("告警已恢复，不需要认领", "The alert has resolved; nothing to claim") : undefined}
            onClick={() => void claim()}
          >
            {isClaimed ? tx("已认领", "Claimed") : tx("认领", "Claim")}
          </Btn>
        </Gate>
        {investigation || demo ? (
          <Btn icon="branch" to={investigation ? `/canvas/${investigation}` : "/canvas/INV-231"}>
            {tx("打开调查", "Open investigation")}
          </Btn>
        ) : (
          <Gate need={["events", "change"]}>
            <Btn icon="branch" disabled={starting} onClick={() => void investigate()}>
              {tx("发起调查", "Investigate")}
            </Btn>
          </Gate>
        )}
        <Btn icon="clock" to={demo ? "/silences?new=checkout-api" : "/silences"}>
          {demo ? tx("静默 1 小时", "Silence 1 h") : tx("静默", "Silence")}
        </Btn>
        <MenuButton
          items={[
            { label: tx("创建事件", "Create incident"), icon: "alert", onSelect: () => nav(demo ? "/incidents/INC-1042" : `/incidents?new=incident&alerts=${encodeURIComponent(a.id)}`) },
            { label: tx("复制链接", "Copy link"), icon: "link", onSelect: () => copyText(`${location.origin}/inbox?alert=${id}`, tx("链接已复制", "Link copied")) },
            ...(demo ? [{ label: tx("在 Alertmanager 中打开", "Open in Alertmanager"), icon: "external" as const, onSelect: () => openExternal("Alertmanager", `https://alertmanager.example.internal/#/alerts?filter=${encodeURIComponent(`{alert_id="${id}"}`)}`) }] : []),
          ]}
          trigger={({ onClick, ref }) => (
            <span ref={ref}>
              <IconBtn icon="more" label={tx("更多操作", "More")} onClick={onClick} />
            </span>
          )}
        />
      </Row>
      <Muted>
        {a.source ?? <UnknownValue reason="not_generated" label={tx("来源 —", "source —")} />} · {curEnv?.label ?? "—"} · {when.short(a.since)} · {a.rule ? tx("规则「{r}」", "Rule “{r}”", { r: a.rule }) : <UnknownValue reason="not_generated" label={tx("规则 —", "rule —")} />}
      </Muted>
      {!!a.unavailable?.length && <PartialBanner fields={[...new Set(a.unavailable.map((f) => ({ source: tx("来源", "source"), rule: tx("规则", "rule"), triage: tx("AI 初查", "AI triage"), series: tx("告警值曲线", "value chart"), labels: tx("标签", "labels"), runbook: "Runbook" })[f.split(".")[0]] ?? tx("分组与关联", "grouping")))]} />}
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
        <Panel title={demo ? tx("告警值 · 5xx 比例", "Value · 5xx ratio") : tx("告警值", "Value")}>
          {a.series ? (
            <Lines series={[{ name: demo ? "5xx" : tx("值", "value"), values: a.series.values, color: "var(--c3)", fill: "var(--c3s)" }]} height={96} max={demo ? 10 : undefined} threshold={a.series.threshold} markers={a.series.release_at !== undefined ? [{ at: a.series.release_at, label: demo ? "v2.8.1" : tx("发布", "release"), tone: "warn" }] : []} labels={a.series.labels} format={demo ? (v) => `${v}%` : undefined} grid={2} />
          ) : (
            <Row gap={6} style={{ height: 60 }}>
              <Muted>{tx("告警值曲线", "Value over time")}</Muted>
              <UnknownValue reason="not_generated" />
            </Row>
          )}
          {demo && (
            <Row gap={6} style={{ marginTop: 8 }}>
              <Muted>{tx("阈值 1% · 持续 5 分钟触发", "Threshold 1% · fires after 5 min")}</Muted>
              <Spacer />
              <TextLink icon="external" onClick={() => openExternal("Grafana", "https://grafana.example.internal/d/checkout-api")}>
                {tx("在 Grafana 中打开", "Open in Grafana")}
              </TextLink>
            </Row>
          )}
        </Panel>
        <Panel title={tx("标签与说明", "Labels & summary")}>
          <Col gap={8}>
            <Row gap={6} wrap>
              {a.labels ? a.labels.map((x) => <LabelChip key={x.k} k={x.k} v={x.v} />) : <UnknownValue reason="not_generated" label={tx("标签 —", "labels —")} />}
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
            {a.notifications.map((n, i) => {
              // Real entries are local queue records: "sent" left this platform, it does not prove delivery.
              const done = n.done ?? n.status === "sent";
              const failed = n.status === "failed";
              const statusText = n.status ? { pending: tx("排队中", "Queued"), sending: tx("发送中", "Sending"), retrying: tx("重试中", "Retrying"), failed: tx("发送失败", "Failed"), sent: tx("已发出", "Sent") }[n.status] : "";
              return (
                <li key={n.id ?? n.time ?? i}>
                  <span className="tnum muted" style={{ width: 60, flex: "none" }}>
                    {n.time ?? when.short(n.created_at) ?? ""}
                  </span>
                  {n.channel === "feishu" ? <Brand name="feishu" size={20} /> : <Icon name={n.channel === "sms" ? "message" : "rotate"} size={15} color="var(--muted)" />}
                  <span className="grow">{n.text ?? `${n.channel} · ${statusText}${n.attempts && n.attempts > 1 ? tx(" · 第 {n} 次", " · attempt {n}", { n: n.attempts }) : ""}`}</span>
                  <Icon name={failed ? "x" : done ? "check" : "clock"} size={14} color={failed ? "var(--danger)" : done ? "var(--ok)" : "var(--muted)"} />
                </li>
              );
            })}
            {!a.notifications.length && <li className="muted">{tx("还没有通知记录", "No notifications yet")}</li>}
          </ul>
        </Panel>
        <Panel title={tx("分组与关联", "Grouping & links")}>
          <Col gap={9}>
            <KV k={tx("分组", "Group")} kw={44}>
              {a.grouping.group ? <TextLink to="/alerts">{a.grouping.group}</TextLink> : <UnknownValue reason="not_generated" />}
            </KV>
            <KV k={tx("抑制", "Inhibits")} kw={44}>
              {a.grouping.inhibited ?? <UnknownValue reason="not_generated" />}
            </KV>
            <KV k={tx("事件", "Incident")} kw={44}>
              {a.grouping.incident.id ? <TextLink to={`/incidents/${a.grouping.incident.id}`}>{a.grouping.incident.label ?? incident?.title ?? a.grouping.incident.id}</TextLink> : <Muted>{tx("未关联", "Not linked")}</Muted>}
            </KV>
            <KV k={tx("变更", "Change")} kw={44}>
              {a.grouping.change ? (
                <Row gap={6}>
                  <Brand name="gitlab" size={20} />
                  <TextLink to="/changes">{a.grouping.change.label}</TextLink>
                </Row>
              ) : (
                <UnknownValue reason="not_generated" />
              )}
            </KV>
          </Col>
        </Panel>
      </div>
    </div>
  );
}

export default function InboxPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const [tab, setTab] = useState<"all" | "mine" | "mentions" | "subscribed">("all");
  const { data, isLoading, error } = useGet<{ items: InboxItem[]; counts: Record<string, number> }>("/inbox", { tab });
  const [read, setRead] = useState<Set<string>>(new Set());
  const items = useMemo(() => data?.items ?? [], [data]);
  // Without an item in the URL, show the first item of the current list (never a fixture id).
  const selId = params.get("item") ?? items[0]?.id ?? "";
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
  // Assignments say what was assigned (PA-84); an unknown kind keeps the server's own (localized) line.
  const assignedText = (it: InboxItem) =>
    it.assignment_kind === "alert"
      ? tx("分配给你的告警", "Alert assigned to you")
      : it.assignment_kind === "incident"
        ? tx("分配给你的事件", "Incident assigned to you")
        : it.assignment_kind === "proposal"
          ? tx("分配给你的提案", "Proposal assigned to you")
          : it.sub;
  // Today / yesterday / earlier in the viewer's time zone, from `at` (PA-83).
  const dayOf = (it: InboxItem): Day => {
    const t = parseStamp(it.at) ?? 0;
    const start = new Date();
    start.setHours(0, 0, 0, 0);
    return t >= start.getTime() ? "today" : t >= start.getTime() - 86400_000 ? "yesterday" : "earlier";
  };
  const days: [Day, string][] = [
    ["today", tx("今天", "Today")],
    ["yesterday", tx("昨天", "Yesterday")],
    ["earlier", tx("更早", "Earlier")],
  ];
  return (
    <Page
      flush
      crumbs={[tx("事件与告警", "Events"), tx("收件箱", "Inbox")]}
      actions={
        // The server keeps no read state, so marking everything read only exists in the sample (PA-220, D52).
        API_MODE === "mock" && (
          <Btn kind="ghost" icon="check" onClick={() => { setRead(new Set(items.map((i) => i.id))); toast(tx("已全部标为已读", "All marked as read")); }}>
            {tx("全部标为已读", "Mark all read")}
          </Btn>
        )
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
          {/* A tab the server can't fill says why; an empty one says so, instead of a blank list (PA-178). */}
          {!isLoading && error && !data ? (
            error instanceof ApiError && error.code === "inbox_filter_unavailable" ? (
              <Unavailable reason="unavailable" icon="inbox" title={tx("提及和订阅还没有来源", "Mentions and subscriptions have no source yet")} desc={tx("平台还没有记录谁被提及、谁订阅了什么，这个页签暂时不会有内容。", "The platform doesn't record mentions or subscriptions yet, so this tab stays empty for now.")} />
            ) : (
              <Unavailable reason={errorReason(error)} icon="inbox" title={tx("暂时读不到收件箱", "The inbox can't be read right now")} />
            )
          ) : (
            !isLoading && data && !items.length && (
              <div style={{ padding: 28, textAlign: "center" }}>
                <Muted>{tx("没有数据", "No data")}</Muted>
              </div>
            )
          )}
          {days.map(([d, label]) => {
            const list = items.filter((i) => dayOf(i) === d);
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
                              {it.kind === "assignment" ? assignedText(it) : it.sub}
                            </span>
                          </Col>
                          <Col gap={6} align="flex-end">
                            <Muted size={11.5}>{d === "earlier" ? when.at(it.at) : when.time(it.at)}</Muted>
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
