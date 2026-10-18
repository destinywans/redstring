import { useMemo, useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { toast } from "../../ui/overlays";
import type { RouteNode, RouteNodeRead } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, Input, LabelChip, Muted, Row, Section, Skeleton, Spacer, Strong, Tabs, Tile, Toggle, cx } from "../../ui/core";
import { useI18n } from "../../i18n";
import { useCreateParam } from "../../ui/forms";
import { NewRouteDrawer } from "./CreateDrawers";
import { Gated, useNeed } from "../../ui/permission";
import "./events.css";
import { AddChip, SampleOnly } from "../../ui/demo";
import { PartialBanner, Unavailable, UnknownValue } from "../../ui/unknown";

const CH_ICON: Record<string, string> = { feishu: "feishu", dingtalk: "dingtalk", slack: "slack" };

function Receiver({ r }: { r: RouteNode["receivers"][number] }) {
  return (
    <Row gap={4}>
      {CH_ICON[r.channel] ? <Brand name={CH_ICON[r.channel]} size={18} /> : <Icon name={r.channel === "phone" ? "phone" : r.channel === "sms" ? "message" : "file"} size={14} color="var(--muted)" />}
      <Muted size={11.5}>{r.target}</Muted>
    </Row>
  );
}

/** Real nodes carry redacted matcher values and opaque receiver refs; map them onto the page's node shape. */
function normalizeRoute(n: RouteNode | RouteNodeRead): RouteNode {
  if ("receivers" in n) return n;
  return {
    id: n.id,
    title: n.title,
    matchers: n.matchers.map((m) => ({ k: m.k, op: m.op, v: m.value_redacted ? "•••" : m.value ?? "", redacted: m.value_redacted })),
    receivers: n.receiver_refs.map((r) => ({ channel: "", target: r })),
    timing: [n.group_wait, n.group_interval, n.repeat_interval],
    hits: null,
    continue: n.continue,
    children: (n.children ?? []).map(normalizeRoute),
  };
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
          {n.hits === null ? <Muted size={11.5}>{tx("近 24h 命中", "Hits / 24h")} <UnknownValue reason="not_generated" /></Muted> : <Muted size={11.5}>{tx("近 24h 命中 {n}", "{n} hits / 24h", { n: n.hits })}</Muted>}
        </Row>
        <Row gap={5} wrap>
          {n.matchers.map((m, i) => (
            <LabelChip key={m.k + m.v + i} k={m.op && m.op !== "=" ? `${m.k} ${m.op}` : m.k} v={m.v} />
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
  // root is null (root_unavailable: not_configured) when no route branch matches this environment exactly.
  const { data, error } = useGet<{ root: RouteNode | RouteNodeRead | null; root_unavailable?: "not_configured" | null; config_revision?: string | null; partial?: boolean; unavailable?: string[] }>("/notification-routes");
  // PA-141: a route draft needs the reviewed GitOps route repository and an exact Alertmanager source (route_drafts).
  const needRoutes = useNeed(["alert_rules", "manage"]);
  // POST /notification-routes/test is marked alert_rules.manage as well (PA-228).
  const needTest = needRoutes;
  const routesHeld = API_MODE === "mock" ? null
    : !data ? tx("暂时读不到路由配置，不能新建", "The routing configuration can't be read right now")
    : data.unavailable?.includes("route_drafts") ? tx("这个环境还没接入经审查的 GitOps 路由仓库", "No reviewed GitOps route repository is connected for this environment")
    : null;
  const root = useMemo(() => (data?.root ? normalizeRoute(data.root) : null), [data]);
  const [sel, setSel] = useState<RouteNode | null>(null);
  const [labels, setLabels] = useState(API_MODE === "mock" ? "service=checkout-api, env=prod, team=trade, severity=P1" : "");
  const [result, setResult] = useState<{ path: string[]; receivers: string } | { paths: { node_ids: string[]; receiver_refs: string[] }[] } | null>(null);
  const cur = sel ?? root?.children[0]?.children[0] ?? root?.children[0] ?? root ?? undefined;
  const titleOf = (id: string) => {
    const walk = (n: RouteNode): string | null => (n.id === id ? n.title : n.children.map(walk).find(Boolean) ?? null);
    return (root && walk(root)) ?? id;
  };
  const test = async () => {
    if (needTest) return;
    if (API_MODE === "mock") return setResult(await api("POST", "/notification-routes/test", { body: { labels } }));
    // The real test takes a label object ("k=v, k2=v2" here) and needs an application label.
    const obj = Object.fromEntries(
      labels
        .split(/[,\n]/)
        .map((x) => x.trim())
        .filter(Boolean)
        .map((x) => [x.slice(0, x.indexOf("=")).trim(), x.slice(x.indexOf("=") + 1).trim()])
        .filter(([k]) => k),
    );
    if (!obj.application) return toast(tx("测试需要 application=应用名 这个标签", "The test needs an application=<name> label"), { tone: "danger" });
    try {
      setResult(await api("POST", "/notification-routes/test", { body: { labels: obj } }));
    } catch (e) {
      setResult(null);
      toast(e instanceof ApiError ? e.message : tx("测试失败，请稍后重试", "The test failed; try again later"), { tone: "danger" });
    }
  };
  return (
    <Page flush error={!data ? error : undefined} errorSource="Alertmanager" crumbs={[tx("事件与告警", "Events"), tx("通知与路由", "Routing")]} actions={<Gated reason={needRoutes ?? routesHeld} kind={needRoutes ? "permission" : "availability"}><Btn kind="primary" icon="plus" onClick={() => setCreating(true)}>{tx("新建路由", "New route")}</Btn></Gated>}>
      <NewRouteDrawer open={creating} onClose={() => setCreating(false)} />
      <Tabs value={tab} onChange={setTab} options={[{ value: "tree", label: tx("路由树", "Route tree") }, { value: "receivers", label: tx("接收人", "Receivers") }, { value: "templates", label: tx("通知模板", "Templates") }, { value: "oncall", label: tx("值班与升级", "On-call & escalation") }, { value: "log", label: tx("发送记录", "Delivery log") }]} />
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            {tab !== "tree" ? (
              <RoutingTab tab={tab} root={root ?? undefined} loading={!data} rootUnavailable={data?.root_unavailable} />
            ) : !data ? (
              <Skeleton h={400} />
            ) : !root ? (
              <Card>
                <Unavailable reason={data.root_unavailable ?? "not_configured"} source={tx("这个环境的通知路由", "routing for this environment")} title={tx("这个环境还没有通知路由", "No routing for this environment")} desc={tx("Alertmanager 的路由树里没有只匹配这个环境的分支，所以这里不显示也不能编辑。", "Alertmanager's routing tree has no branch that matches exactly this environment, so nothing is shown or editable here.")} icon="bell" />
              </Card>
            ) : (
              <>
                {data.partial && !!data.unavailable?.length && (
                  <PartialBanner fields={[]}>
                    {tx("路由树只显示结构：匹配值和接收方名称已隐藏，命中次数、值班和升级暂缺。", "Only the tree's structure is shown: matcher values and receiver names are hidden; hits, on-call and escalation are missing.")}
                  </PartialBanner>
                )}
                <Tree n={root} sel={cur?.id ?? ""} onSel={setSel} />
              </>
            )}
            <Card style={{ padding: "12px 14px", display: "flex", flexDirection: "column", gap: 8 }}>
              <Row gap={8}>
                <Icon name="play" size={14} color="var(--accent-text)" />
                <Strong>{tx("测试路由", "Test routing")}</Strong>
                <Muted size={11.5}>{tx("输入一组标签，看会命中哪条路由", "Enter labels to see which route matches")}</Muted>
              </Row>
              <form className="v3-row" style={{ gap: 8 }} onSubmit={(e) => { e.preventDefault(); void test(); }}>
                <Input mono value={labels} onChange={(e) => setLabels(e.target.value)} width="min(560px, 100%)" aria-label={tx("标签", "Labels")} placeholder="application=checkout-api, severity=P1" />
                <Gated reason={needTest}>
                  <Btn h={32} type="submit">
                    {tx("测试", "Test")}
                  </Btn>
                </Gated>
              </form>
              {result && "paths" in result && (
                <Col gap={4} className="anim-rise">
                  {result.paths.length ? (
                    result.paths.map((leaf, j) => (
                      <Row key={j} gap={6} wrap>
                        <Icon name="check" size={14} color="var(--ok)" />
                        {leaf.node_ids.map((id, i) => (
                          <Row key={id} gap={6}>
                            {i > 0 && <Icon name="right" size={12} color="var(--faint)" />}
                            <Badge tone={i === leaf.node_ids.length - 1 ? "accent" : "neutral"}>{titleOf(id)}</Badge>
                          </Row>
                        ))}
                        <Muted size={11.5}>→ {leaf.receiver_refs.join(", ")}</Muted>
                      </Row>
                    ))
                  ) : (
                    <Muted size={11.5}>{tx("没有命中任何路由", "No route matched")}</Muted>
                  )}
                </Col>
              )}
              {result && "path" in result && (
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
                  {/* Nothing on this panel is saved yet (route edits are merge-request drafts); real mode shows it read-only. */}
                  {API_MODE === "mock" && <AddChip label={tx("条件", "Matcher")} free placeholder="severity=P2" options={["severity=P2", "namespace=checkout", "alertname=HighLatency"]} />}
                </Row>
              </Section>
              <Section title={tx("分组与时间", "Grouping & timing")}>
                {API_MODE === "mock" && (
                  <Row gap={6}>
                    <Muted>{tx("分组依据", "Group by")}</Muted>
                    <Badge>service</Badge>
                    <Badge>alertname</Badge>
                  </Row>
                )}
                {[
                  [tx("首次等待", "Group wait"), cur.timing[0], tx("合并同组告警后再发", "Merge the group before sending")],
                  [tx("组内间隔", "Group interval"), cur.timing[1], tx("组内有新告警时的最短间隔", "Minimum gap when the group changes")],
                  [tx("重复间隔", "Repeat interval"), cur.timing[2], tx("未恢复时周期性重复提醒", "Repeat while still firing")],
                ].map(([l, v, h]) => (
                  <Col key={l} gap={3}>
                    <Row gap={6}>
                      <Strong>{l}</Strong>
                      <Spacer />
                      {API_MODE === "mock" ? <Input defaultValue={v} width={92} aria-label={l} /> : <span className="mono">{v || "—"}</span>}
                    </Row>
                    <Muted size={11.5}>{h}</Muted>
                  </Col>
                ))}
              </Section>
              {API_MODE !== "mock" && (
                <Section title={tx("接收人", "Receivers")}>
                  {cur.receivers.length ? (
                    cur.receivers.map((r) => (
                      <Row key={r.channel + r.target} gap={10}>
                        <Tile icon="bell" size={26} />
                        <Col gap={0} grow>
                          <Strong>{r.target}</Strong>
                          <Muted size={11.5}>{r.channel}</Muted>
                        </Col>
                      </Row>
                    ))
                  ) : (
                    <Muted size={11.5}>{tx("这条路由没有直接的接收方", "This route has no direct receiver")}</Muted>
                  )}
                </Section>
              )}
              {API_MODE === "mock" && <Section title={tx("接收人", "Receivers")}>
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
              </Section>}
              {API_MODE === "mock" && <>
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
                    <Muted>{tx("AI 初查：inventory-03 先变慢，库存调用量随后上涨", "AI triage: inventory-03 slowed first, then inventory calls rose")}</Muted>
                    <Row gap={6}>
                      <Badge>{tx("认领", "Claim")}</Badge>
                      <Badge>{tx("打开调查", "Open investigation")}</Badge>
                      <Badge>{tx("静默 1 小时", "Silence 1 h")}</Badge>
                    </Row>
                  </div>
                </div>
              </Section>
              </>}
            </Col>
          </aside>
        )}
      </div>
    </Page>
  );
}

/** Receivers come from the route tree; templates, on-call and the delivery log are sample-only until their endpoints exist. */
function RoutingTab({ tab, root, loading, rootUnavailable }: { tab: string; root?: RouteNode; loading?: boolean; rootUnavailable?: string | null }) {
  const { tx } = useI18n();
  // Without a route tree (still loading, or no branch for this environment) the tab says so like the tree does (PA-229);
  // a failed read is the page's error.
  if (tab === "receivers" && loading) return <Skeleton h={200} />;
  if (tab === "receivers" && !root)
    return (
      <Card>
        <Unavailable reason={rootUnavailable ?? "not_configured"} source={tx("这个环境的通知路由", "routing for this environment")} title={tx("这个环境还没有通知路由", "No routing for this environment")} desc={tx("Alertmanager 的路由树里没有只匹配这个环境的分支，所以这里不显示也不能编辑。", "Alertmanager's routing tree has no branch that matches exactly this environment, so nothing is shown or editable here.")} icon="bell" />
      </Card>
    );
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
                  {/* The real route tree names receivers only; which channel each one uses isn't read (PA-229). */}
                  {r.channel ? (
                    <Row gap={6}>
                      <Brand name={r.channel} size={18} />
                      {r.channel}
                    </Row>
                  ) : (
                    <UnknownValue reason="not_tracked" />
                  )}
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
