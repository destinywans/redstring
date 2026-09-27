import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { AlertGroup, AlertGroupPolicy } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, IconBtn, Kbd, LabelChip, Muted, Pills, Row, SearchBox, Section, Sev, Skeleton, Spacer, State, Strong, Tile, Toggle, Bar, cx, type Tone } from "../../ui/core";
import { toast, Tooltip } from "../../ui/overlays";
import { DataTable } from "../../ui/data";
import { Sparkline } from "../../charts";
import { useI18n } from "../../i18n";
import { PersonPeekLink, ServicePeekLink } from "./peeks";
import "./events.css";

export default function AlertsPage() {
  const { tx } = useI18n();
  const [state, setState] = useState<"firing" | "pending" | "silenced" | "resolved">("firing");
  const [q, setQ] = useState("");
  const [showInhibited, setShowInhibited] = useState(true);
  const { data, isLoading } = useGet<{ items: AlertGroup[]; counts: Record<string, number> }>("/alert-groups", { state, q, show_inhibited: showInhibited });
  const [open, setOpen] = useState<Set<string>>(new Set(["g-checkout-5xx"]));
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const [focus, setFocus] = useState("g-checkout-5xx");
  const policy = useGet<AlertGroupPolicy>(`/alert-groups/${focus}/policy`);
  const STATE: Record<AlertGroup["state"], [Tone, string]> = {
    firing: ["danger", tx("触发中", "Firing")],
    pending: ["warn", tx("待定", "Pending")],
    silenced: ["neutral", tx("已静默", "Silenced")],
    resolved: ["ok", tx("已恢复", "Resolved")],
  };
  const toggle = (set: Set<string>, id: string) => {
    const n = new Set(set);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    return n;
  };
  const bulk = async (action: string, label: string) => {
    const ids = [...checked];
    await api("POST", "/alert-groups/bulk", { body: { action, ids } });
    setChecked(new Set());
    toast(tx("{a}：{n} 组告警", "{a}: {n} alert groups", { a: label, n: ids.length }), { undo: () => setChecked(new Set(ids)) });
  };
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement).closest("input,textarea")) return;
      if (e.key === "Escape") setChecked(new Set());
      if (e.key === "x") setChecked((c) => toggle(c, focus));
      if (e.key === "s" && checked.size) void bulk("silence", tx("批量静默 1 小时", "Silenced 1 h"));
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  });
  return (
    <Page
      flush
      crumbs={[tx("事件与告警", "Events"), tx("告警", "Alerts")]}
      actions={
        <>
          <Btn kind="ghost" icon="sliders" to="/alert-rules">
            {tx("告警规则", "Rules")}
          </Btn>
          <Btn icon="clock" to="/silences?new=silence">
            {tx("新建静默", "New silence")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ paddingBottom: 90 }}>
            <Row gap={8} wrap>
              <Pills
                value={state}
                onChange={setState}
                options={[
                  { value: "firing", label: tx("触发中", "Firing"), count: data?.counts.firing },
                  { value: "pending", label: tx("待定", "Pending"), count: data?.counts.pending },
                  { value: "silenced", label: tx("已静默", "Silenced"), count: data?.counts.silenced },
                  { value: "resolved", label: tx("已恢复", "Resolved") },
                ]}
              />
              <SearchBox placeholder={tx("按标签过滤，如 service=checkout-api", "Filter by label, e.g. service=checkout-api")} value={q} onChange={setQ} width={280} />
              <Chip icon="layers" caret>
                {tx("分组：service, alertname", "Group: service, alertname")}
              </Chip>
              <Spacer />
              <Muted>{tx("显示被抑制的告警", "Show inhibited")}</Muted>
              <Toggle on={showInhibited} onChange={setShowInhibited} label={tx("显示被抑制的告警", "Show inhibited")} />
            </Row>
            <Row gap={6} className="muted" style={{ fontSize: "var(--fs-sub)" }}>
              <Icon name="sparkles" size={13} color="var(--accent-text)" />
              {tx("鼠标停在服务名或人名上查看摘要；行尾是快捷操作；勾选后底部出现批量操作栏。", "Hover a service or person for a summary; row ends have quick actions; select rows for bulk actions.")}
              <Spacer />
              <Kbd>X</Kbd>
              {tx("选择", "select")}
              <Kbd>S</Kbd>
              {tx("静默", "silence")}
              <Kbd>esc</Kbd>
              {tx("取消", "clear")}
            </Row>
            <Col gap={10} className="stagger">
              {isLoading && Array.from({ length: 4 }, (_, i) => <Skeleton key={i} h={48} style={{ borderRadius: 10 }} />)}
              {data?.items.map((g) => {
                const expanded = open.has(g.id);
                return (
                  <Card key={g.id} className={cx("alert-group hrow", expanded && "open", g.inhibited && "dim", checked.has(g.id) && "checked", focus === g.id && "focus")}>
                    <div className="ag-head" onClick={() => setFocus(g.id)}>
                      <input type="checkbox" aria-label={tx("选择", "Select")} checked={checked.has(g.id)} onChange={() => setChecked((c) => toggle(c, g.id))} onClick={(e) => e.stopPropagation()} />
                      <IconBtn icon={expanded ? "down" : "right"} label={expanded ? tx("收起", "Collapse") : tx("展开", "Expand")} size={22} onClick={() => setOpen((o) => toggle(o, g.id))} />
                      <Sev p={g.severity} />
                      <ServicePeekLink id={g.service}>
                        <LabelChip k="service" v={g.service} />
                      </ServicePeekLink>
                      {g.labels.filter((l) => l.k !== "service").map((l) => (
                        <LabelChip key={l.k} k={l.k} v={l.v} />
                      ))}
                      <Badge>{tx("{n} 条", "{n}", { n: g.count })}</Badge>
                      <State tone={STATE[g.state][0]} pulse={g.state === "firing" && g.severity === "P1"}>
                        {STATE[g.state][1]}
                      </State>
                      {g.badges.map((b) => (
                        <Badge key={b.text} tone={b.tone}>
                          {b.text}
                        </Badge>
                      ))}
                      <Spacer />
                      <span className="since muted" style={{ fontSize: "var(--fs-sub)" }}>{g.since}</span>
                      <Row gap={4}>
                        {g.notify.channel && <Brand name={g.notify.channel} size={20} />}
                        <Muted size={11.5}>{g.notify.text}</Muted>
                      </Row>
                      {g.owner && <PersonPeekLink name={g.owner} />}
                      <span className="hact v3-row" style={{ gap: 2 }}>
                        <Tooltip label={tx("发起调查", "Investigate")}>
                          <IconBtn icon="branch" label={tx("发起调查", "Investigate")} size={26} color="var(--accent-text)" to="/canvas/INV-231" />
                        </Tooltip>
                        <Tooltip label={tx("静默", "Silence")}>
                          <IconBtn icon="mute" label={tx("静默", "Silence")} size={26} onClick={() => toast(tx("已静默 1 小时", "Silenced for 1 h"), { undo: () => undefined })} />
                        </Tooltip>
                        <Tooltip label={tx("认领", "Claim")}>
                          <IconBtn icon="user" label={tx("认领", "Claim")} size={26} onClick={() => toast(tx("已认领", "Claimed"))} />
                        </Tooltip>
                      </span>
                    </div>
                    {expanded && g.instances.length > 0 && (
                      <div className="ag-body anim-fade">
                        <DataTable
                          rows={g.instances}
                          rowKey={(r) => r.id}
                          rowHeight={40}
                          headBg
                          columns={[
                            { key: "id", header: tx("实例", "Instance"), width: "32%", render: (r) => <span className="mono" style={{ fontSize: "var(--fs-sub)" }}>{r.id}</span> },
                            { key: "v", header: tx("当前值", "Value"), render: (r) => <b className="tnum" style={{ color: r.state === "firing" ? "var(--danger)" : "var(--warn)", fontWeight: 500 }}>{r.value}</b> },
                            {
                              key: "s",
                              header: tx("状态", "State"),
                              render: (r) =>
                                r.pending ? (
                                  <Col gap={3}>
                                    <State tone="warn">{tx("待定", "Pending")}</State>
                                    <Row gap={6}>
                                      <Bar pct={r.pending.pct} tone="warn" width={60} height={4} />
                                      <Muted size={11}>{r.pending.text}</Muted>
                                    </Row>
                                  </Col>
                                ) : (
                                  <State tone="danger">{tx("触发中", "Firing")}</State>
                                ),
                            },
                            { key: "d", header: tx("持续", "Duration"), render: (r) => <Muted>{r.duration}</Muted> },
                            { key: "t", header: tx("近 30 分钟", "Last 30 min"), render: (r) => <Sparkline values={r.series} width={90} height={20} color={r.state === "firing" ? "var(--danger)" : "var(--warn)"} /> },
                          ]}
                        />
                      </div>
                    )}
                  </Card>
                );
              })}
            </Col>
          </div>
          <div className={cx("bulk-bar", checked.size > 0 && "show")} role="toolbar" aria-label={tx("批量操作", "Bulk actions")}>
            <b>{tx("已选 {n} 组", "{n} selected", { n: checked.size })}</b>
            <button type="button" onClick={() => bulk("merge", tx("已合并为事件", "Merged into an incident"))}>
              <Icon name="merge" size={14} />
              {tx("合并为事件", "Merge into incident")}
            </button>
            <button type="button" onClick={() => bulk("silence", tx("批量静默 1 小时", "Silenced 1 h"))}>
              <Icon name="mute" size={14} />
              {tx("批量静默", "Silence")}
            </button>
            <button type="button" onClick={() => bulk("assign", tx("已指派给 王晨", "Assigned to Wang Chen"))}>
              <Icon name="user" size={14} />
              {tx("指派给…", "Assign…")}
            </button>
            <Link to="/canvas/INV-231" className="primary">
              <Icon name="branch" size={14} />
              {tx("发起联合调查", "Joint investigation")}
            </Link>
            <span className="sep" />
            <span className="hint">esc {tx("取消", "clear")}</span>
            <button type="button" aria-label={tx("取消选择", "Clear selection")} onClick={() => setChecked(new Set())}>
              <Icon name="x" size={15} />
            </button>
          </div>
        </div>
        <aside className="aside" style={{ width: 340 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="layers" tone="accent" />
            <Col gap={0}>
              <Strong size={13.5}>{tx("聚合与通知", "Grouping & notification")}</Strong>
              <Muted size={11.5}>{data?.items.find((g) => g.id === focus)?.labels.map((l) => l.v).join(" / ")}</Muted>
            </Col>
          </Row>
          {policy.data ? (
            <Col gap={16} style={{ padding: 16 }} className="anim-fade" key={focus}>
              <Section title={tx("分组依据", "Group by")}>
                <Row gap={6} wrap>
                  {policy.data.group_by.map((g) => (
                    <Badge key={g}>{g}</Badge>
                  ))}
                  <Muted size={11.5}>{policy.data.route}</Muted>
                </Row>
              </Section>
              <Section title={tx("时间参数", "Timing")}>
                {policy.data.timing.map((t) => (
                  <Col key={t.key} gap={2}>
                    <Row gap={6}>
                      <Strong>{t.label}</Strong>
                      <Spacer />
                      <span className="mono" style={{ fontSize: "var(--fs-sub)" }}>
                        {t.value}
                      </span>
                    </Row>
                    <Muted size={11.5}>{t.help}</Muted>
                  </Col>
                ))}
              </Section>
              <Section title={tx("通知时间线", "Notification timeline")}>
                {policy.data.timeline.map((n) => (
                  <Row key={n.time} gap={8}>
                    <Muted size={11.5}>{n.time}</Muted>
                    {n.channel === "feishu" ? <Brand name="feishu" size={18} /> : <Icon name={n.channel === "sms" ? "message" : "rotate"} size={14} color="var(--muted)" />}
                    <span className="grow" style={{ fontSize: "var(--fs-sub)" }}>
                      {n.text}
                    </span>
                    <Icon name={n.done ? "check" : "clock"} size={13} color={n.done ? "var(--ok)" : "var(--muted)"} />
                  </Row>
                ))}
              </Section>
              <Section title={tx("抑制与延迟", "Inhibition & delay")}>
                {policy.data.inhibition.map((t) => (
                  <p key={t} style={{ fontSize: "var(--fs-sub)" }}>
                    {t}
                  </p>
                ))}
                <Btn kind="ghost" to="/silences" iconRight="right" style={{ alignSelf: "flex-start" }}>
                  {tx("抑制规则", "Inhibition rules")}
                </Btn>
              </Section>
            </Col>
          ) : (
            <Skeleton h={200} style={{ margin: 16 }} />
          )}
        </aside>
      </div>
    </Page>
  );
}
