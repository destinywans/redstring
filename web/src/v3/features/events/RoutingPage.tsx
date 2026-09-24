import { useState } from "react";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { RouteNode } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, Input, LabelChip, Muted, Row, Section, Skeleton, Spacer, Strong, Tabs, Tile, Toggle, cx } from "../../ui/core";
import { useI18n } from "../../i18n";
import "./events.css";

const CH_ICON: Record<string, string> = { feishu: "feishu", dingtalk: "dingtalk", slack: "slack" };

function Receiver({ r }: { r: RouteNode["receivers"][number] }) {
  return (
    <Row gap={4}>
      {CH_ICON[r.channel] ? <Brand name={CH_ICON[r.channel]} size={18} /> : <Icon name={r.channel === "phone" ? "phone" : r.channel === "sms" ? "message" : "file"} size={14} color="var(--muted)" />}
      <Muted size={11.5}>{r.target}</Muted>
    </Row>
  );
}

function Tree({ n, sel, onSel, depth = 0 }: { n: RouteNode; sel: string; onSel: (n: RouteNode) => void; depth?: number }) {
  const { tx } = useI18n();
  return (
    <Col gap={10}>
      <button type="button" className={cx("route-node lift", sel === n.id && "on")} onClick={() => onSel(n)}>
        <Row gap={6}>
          <b style={{ fontSize: 12.5 }}>{n.title}</b>
          {n.continue && <Badge tone="accent">{tx("继续匹配", "Continue")}</Badge>}
          <Spacer />
          <Muted size={11.5}>{tx("近 24h 命中 {n}", "{n} hits / 24h", { n: n.hits })}</Muted>
        </Row>
        <Row gap={5} wrap>
          {n.matchers.map((m) => (
            <LabelChip key={m.k + m.v} k={m.k} v={m.v} />
          ))}
        </Row>
        <Row gap={5}>
          <Muted size={11.5}>{tx("接收人", "Receivers")}</Muted>
          {n.receivers.length ? n.receivers.map((r) => <Receiver key={r.target} r={r} />) : <Muted size={11.5}>{tx("继承上级", "Inherited")}</Muted>}
          <Spacer />
          {n.timing.map((t, i) => (
            <Badge key={i}>{t}</Badge>
          ))}
        </Row>
      </button>
      {n.children.length > 0 && (
        <div className="route-children" style={{ marginLeft: depth ? 14 : 0 }}>
          {n.children.map((c) => (
            <Tree key={c.id} n={c} sel={sel} onSel={onSel} depth={depth + 1} />
          ))}
        </div>
      )}
    </Col>
  );
}

export default function RoutingPage() {
  const { tx } = useI18n();
  const [tab, setTab] = useState("tree");
  const { data } = useGet<{ root: RouteNode }>("/notification-routes");
  const [sel, setSel] = useState<RouteNode | null>(null);
  const [labels, setLabels] = useState("service=checkout-api, env=prod, team=trade, severity=P1");
  const [result, setResult] = useState<{ path: string[]; receivers: string } | null>(null);
  const cur = sel ?? data?.root.children[0]?.children[0];
  const test = async () => setResult(await api("POST", "/notification-routes/test", { body: { labels } }));
  return (
    <Page flush crumbs={[tx("事件与告警", "Events"), tx("通知与路由", "Routing")]} actions={<Btn kind="primary" icon="plus">{tx("新建路由", "New route")}</Btn>}>
      <Tabs value={tab} onChange={setTab} options={[{ value: "tree", label: tx("路由树", "Route tree") }, { value: "receivers", label: tx("接收人", "Receivers") }, { value: "templates", label: tx("通知模板", "Templates") }, { value: "oncall", label: tx("值班与升级", "On-call & escalation") }, { value: "log", label: tx("发送记录", "Delivery log") }]} />
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            {!data ? <Skeleton h={400} /> : <Tree n={data.root} sel={cur?.id ?? ""} onSel={setSel} />}
            <Card style={{ padding: "12px 14px", display: "flex", flexDirection: "column", gap: 8 }}>
              <Row gap={8}>
                <Icon name="play" size={14} color="var(--accent-text)" />
                <Strong>{tx("测试路由", "Test routing")}</Strong>
                <Muted size={11.5}>{tx("输入一组标签，看会命中哪条路由", "Enter labels to see which route matches")}</Muted>
              </Row>
              <form className="v3-row" style={{ gap: 8 }} onSubmit={(e) => { e.preventDefault(); void test(); }}>
                <Input mono value={labels} onChange={(e) => setLabels(e.target.value)} width="min(560px, 100%)" aria-label={tx("标签", "Labels")} />
                <Btn h={32} type="submit" onClick={test}>
                  {tx("测试", "Test")}
                </Btn>
              </form>
              {result && (
                <Row gap={6} wrap className="anim-rise">
                  <Icon name="check" size={14} color="var(--ok)" />
                  {result.path.map((p, i) => (
                    <Row key={p} gap={6}>
                      {i > 0 && <Icon name="right" size={12} color="var(--faint)" />}
                      <Badge tone={i >= 2 ? "accent" : "neutral"}>{p}</Badge>
                    </Row>
                  ))}
                  <Muted size={11.5}>→ {result.receivers}</Muted>
                </Row>
              )}
            </Card>
          </div>
        </div>
        {cur && (
          <aside className="aside" style={{ width: 400 }}>
            <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
              <Tile icon="branch" tone="accent" />
              <Col gap={0}>
                <Strong size={13.5}>{cur.title}</Strong>
                <Muted size={11.5}>{tx("路由 · 命中后继续匹配子路由", "Route · keeps matching children after a hit")}</Muted>
              </Col>
            </Row>
            <Col gap={14} style={{ padding: 16 }} className="anim-fade" key={cur.id}>
              <Section title={tx("匹配条件", "Matchers")}>
                <Row gap={6} wrap>
                  {cur.matchers.map((m) => (
                    <LabelChip key={m.k + m.v} k={m.k} v={m.v} />
                  ))}
                  <Chip icon="plus">{tx("条件", "Matcher")}</Chip>
                </Row>
              </Section>
              <Section title={tx("分组与时间", "Grouping & timing")}>
                <Row gap={6}>
                  <Muted>{tx("分组依据", "Group by")}</Muted>
                  <Badge>service</Badge>
                  <Badge>alertname</Badge>
                </Row>
                {[
                  [tx("首次等待", "Group wait"), cur.timing[0], tx("合并同组告警后再发", "Merge the group before sending")],
                  [tx("组内间隔", "Group interval"), cur.timing[1], tx("组内有新告警时的最短间隔", "Minimum gap when the group changes")],
                  [tx("重复间隔", "Repeat interval"), cur.timing[2], tx("未恢复时周期性重复提醒", "Repeat while still firing")],
                ].map(([l, v, h]) => (
                  <Col key={l} gap={3}>
                    <Row gap={6}>
                      <Strong>{l}</Strong>
                      <Spacer />
                      <Input defaultValue={v} width={92} aria-label={l} />
                    </Row>
                    <Muted size={11.5}>{h}</Muted>
                  </Col>
                ))}
              </Section>
              <Section title={tx("接收人", "Receivers")}>
                <Row gap={10}>
                  <Brand name="feishu" size={26} />
                  <Col gap={0} grow>
                    <Strong>{tx("飞书 · #sre-oncall", "Feishu · #sre-oncall")}</Strong>
                    <Muted size={11.5}>{tx("卡片消息，可直接认领和静默", "Cards with claim & silence buttons")}</Muted>
                  </Col>
                  <Toggle on label="feishu" />
                </Row>
                <Row gap={10}>
                  <Tile icon="message" size={26} />
                  <Col gap={0} grow>
                    <Strong>{tx("短信 · 当前值班人", "SMS · on-call")}</Strong>
                    <Muted size={11.5}>{tx("仅 P1 / P2", "P1 / P2 only")}</Muted>
                  </Col>
                  <Toggle on label="sms" />
                </Row>
              </Section>
              <Section title={tx("升级策略", "Escalation")}>
                <Row gap={6}>
                  <Badge>{tx("5 分钟未认领", "Unclaimed 5 min")}</Badge>
                  <Icon name="right" size={12} color="var(--faint)" />
                  <span style={{ fontSize: 12 }}>{tx("电话 主值班 王晨", "Call primary Wang Chen")}</span>
                </Row>
                <Row gap={6}>
                  <Badge>{tx("15 分钟未认领", "Unclaimed 15 min")}</Badge>
                  <Icon name="right" size={12} color="var(--faint)" />
                  <span style={{ fontSize: 12 }}>{tx("电话 二线 张磊 · 飞书 @负责人", "Call secondary Zhang Lei · Feishu @owner")}</span>
                </Row>
              </Section>
              <Section title={tx("消息预览", "Message preview")}>
                <div className="msg-preview">
                  <div className="head">{tx("[P1 触发] checkout-api 5xx 比例 8.4%", "[P1 firing] checkout-api 5xx ratio 8.4%")}</div>
                  <div className="body">
                    <span>{tx("生产 · 交易平台 · 5 条告警合并", "prod · trading · 5 alerts merged")}</span>
                    <Muted>{tx("AI 初查：与 14:03 发布的连接池配置变更有关", "AI triage: linked to the 14:03 pool config change")}</Muted>
                    <Row gap={6}>
                      <Badge>{tx("认领", "Claim")}</Badge>
                      <Badge>{tx("打开调查", "Open investigation")}</Badge>
                      <Badge>{tx("静默 1 小时", "Silence 1 h")}</Badge>
                    </Row>
                  </div>
                </div>
              </Section>
            </Col>
          </aside>
        )}
      </div>
    </Page>
  );
}
