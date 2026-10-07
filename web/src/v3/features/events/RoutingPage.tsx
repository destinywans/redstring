import { useState } from "react";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { RouteNode } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, Input, LabelChip, Muted, Row, Section, Skeleton, Spacer, Strong, Tabs, Tile, Toggle, cx } from "../../ui/core";
import { useI18n } from "../../i18n";
import { useCreateParam } from "../../ui/forms";
import { NewRouteDrawer } from "./CreateDrawers";
import "./events.css";
import { AddChip, SampleOnly } from "../../ui/demo";

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
          <b style={{ fontSize: "var(--fs-sm)" }}>{n.title}</b>
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
  const [creating, setCreating] = useCreateParam("route");
  const [tab, setTab] = useState("tree");
  const { data } = useGet<{ root: RouteNode }>("/notification-routes");
  const [sel, setSel] = useState<RouteNode | null>(null);
  const [labels, setLabels] = useState("service=checkout-api, env=prod, team=trade, severity=P1");
  const [result, setResult] = useState<{ path: string[]; receivers: string } | null>(null);
  const cur = sel ?? data?.root.children[0]?.children[0];
  const test = async () => setResult(await api("POST", "/notification-routes/test", { body: { labels } }));
  return (
    <Page flush crumbs={[tx("事件与告警", "Events"), tx("通知与路由", "Routing")]} actions={<Btn kind="primary" icon="plus" onClick={() => setCreating(true)}>{tx("新建路由", "New route")}</Btn>}>
      <NewRouteDrawer open={creating} onClose={() => setCreating(false)} />
      <Tabs value={tab} onChange={setTab} options={[{ value: "tree", label: tx("路由树", "Route tree") }, { value: "receivers", label: tx("接收人", "Receivers") }, { value: "templates", label: tx("通知模板", "Templates") }, { value: "oncall", label: tx("值班与升级", "On-call & escalation") }, { value: "log", label: tx("发送记录", "Delivery log") }]} />
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            {tab !== "tree" ? <RoutingTab tab={tab} root={data?.root} /> : !data ? <Skeleton h={400} /> : <Tree n={data.root} sel={cur?.id ?? ""} onSel={setSel} />}
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
                  <AddChip label={tx("条件", "Matcher")} free placeholder="severity=P2" options={["severity=P2", "namespace=checkout", "alertname=HighLatency"]} />
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
                  <span style={{ fontSize: "var(--fs-sub)" }}>{tx("电话 主值班 王晨", "Call primary Wang Chen")}</span>
                </Row>
                <Row gap={6}>
                  <Badge>{tx("15 分钟未认领", "Unclaimed 15 min")}</Badge>
                  <Icon name="right" size={12} color="var(--faint)" />
                  <span style={{ fontSize: "var(--fs-sub)" }}>{tx("电话 二线 张磊 · 飞书 @负责人", "Call secondary Zhang Lei · Feishu @owner")}</span>
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

/** Receivers come from the route tree; templates, on-call and the delivery log are sample-only until their endpoints exist. */
function RoutingTab({ tab, root }: { tab: string; root?: RouteNode }) {
  const { tx } = useI18n();
  if (tab === "receivers") {
    const rows: { route: string; channel: string; target: string }[] = [];
    const walk = (n: RouteNode) => {
      n.receivers.forEach((r) => rows.push({ route: n.title, channel: r.channel, target: r.target }));
      n.children.forEach(walk);
    };
    if (root) walk(root);
    return (
      <Card style={{ overflow: "hidden" }}>
        <table className="v3-table">
          <thead>
            <tr>
              <th>{tx("接收方", "Receiver")}</th>
              <th>{tx("渠道", "Channel")}</th>
              <th>{tx("所属路由", "Route")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.route + r.target} style={{ height: 40 }}>
                <td>
                  <b>{r.target}</b>
                </td>
                <td>
                  <Row gap={6}>
                    <Brand name={r.channel} size={18} />
                    {r.channel}
                  </Row>
                </td>
                <td>
                  <Muted>{r.route}</Muted>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    );
  }
  const sample: Record<string, { title: string; cols: string[]; rows: string[][] }> = {
    templates: {
      title: tx("通知模板", "Templates"),
      cols: [tx("模板", "Template"), tx("渠道", "Channel"), tx("使用的路由", "Used by")],
      rows: [
        [tx("告警卡片 · 默认", "Alert card · default"), "feishu", "4"],
        [tx("P1 电话播报", "P1 phone readout"), "phone", "1"],
        [tx("恢复通知", "Resolved notice"), "feishu / dingtalk", "5"],
      ],
    },
    oncall: {
      title: tx("值班与升级", "On-call & escalation"),
      cols: [tx("值班表", "Schedule"), tx("当前值班", "On call"), tx("升级策略", "Escalation")],
      rows: [
        [tx("交易平台主值班", "Trading primary"), "王晨", tx("5 分钟未认领 → 副值班 → 负责人", "5 min unclaimed → secondary → lead")],
        [tx("支付值班", "Payments"), "李明", tx("10 分钟未认领 → 负责人", "10 min unclaimed → lead")],
        [tx("基础设施", "Infrastructure"), "周航", tx("15 分钟未认领 → SRE 负责人", "15 min unclaimed → SRE lead")],
      ],
    },
    log: {
      title: tx("发送记录", "Delivery log"),
      cols: [tx("时间", "Time"), tx("告警", "Alert"), tx("接收方 · 结果", "Receiver · result")],
      rows: [
        ["14:05", "P1 · checkout-api 5xx", tx("电话 · 王晨 · 已接听", "Phone · Wang Chen · answered")],
        ["14:05", "P1 · checkout-api 5xx", tx("#sre-oncall · 已送达", "#sre-oncall · delivered")],
        ["13:40", "P2 · payment-gateway P99", tx("支付值班群 · 已送达", "Payments on-call · delivered")],
      ],
    },
  };
  const t = sample[tab];
  if (!t) return null;
  return (
    <SampleOnly what={t.title}>
      <Card style={{ overflow: "hidden" }}>
        <table className="v3-table">
          <thead>
            <tr>
              {t.cols.map((c) => (
                <th key={c}>{c}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {t.rows.map((r) => (
              <tr key={r.join()} style={{ height: 40 }}>
                {r.map((c, i) => (
                  <td key={i}>{i === 0 ? <b>{c}</b> : <Muted>{c}</Muted>}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </SampleOnly>
  );
}
