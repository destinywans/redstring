import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { MetricsDashboard } from "../../api/types";
import { AiAvatar, Badge, Bar, Brand, Btn, Card, Chip, Cite, Col, Icon, IconBtn, LabelChip, Legend, Muted, Panel, Row, Segmented, Skeleton, Spacer, Strong, TextLink, cx, type Tone } from "../../ui/core";
import { KBand } from "../../ui/data";
import { newConversationUrl } from "../../api/stream";
import { Composer, NextSteps } from "../../ui/Composer";
import { Markdown } from "../../ui/Markdown";
import { toast } from "../../ui/overlays";
import { Axis, BoxPlot, Gauge, HBars, Heatmap, Lines, SmallMultiples, StackedArea } from "../../charts";
import { useI18n } from "../../i18n";
import "./observe.css";

export default function MetricsPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const service = params.get("service") ?? "checkout-api";
  const { data: d } = useGet<MetricsDashboard>("/metrics/dashboard", { service });
  const [align, setAlign] = useState(false);
  const [ai, setAi] = useState(false);
  const [lat, setLat] = useState<"heat" | "pct" | "box">("heat");
  const [hover, setHover] = useState<number | null>(null);
  const sync = { index: hover, set: setHover };
  if (!d)
    return (
      <Page crumbs={[tx("资源与观测", "Resources"), tx("指标", "Metrics")]}>
        <div className="v3-content">
          <Skeleton h={120} />
          <Skeleton h={260} />
          <Skeleton h={260} />
        </div>
      </Page>
    );
  const w = d.window;
  const markers = [
    { at: w.release_at, label: tx("v2.8.1 发布", "v2.8.1 release"), tone: "warn" },
    { at: w.rollback_at, label: tx("回滚", "Rollback"), tone: "ok" },
  ];
  const bands = align ? [{ from: w.incident[0], to: w.incident[1], color: "var(--accent-soft)" }] : [];
  const stats = Array.from({ length: 12 }, (_, k) => {
    const seg = d.latency.p95.slice(k * 3, k * 3 + 3);
    const m = seg.reduce((a, b) => a + b, 0) / 3;
    return { label: w.labels[k * 3], min: m * 0.35, q1: m * 0.7, med: m, q3: m * 1.3, max: m * 2.1 };
  });
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("指标", "Metrics")]}
      ai={false}
      actions={
        <>
          <Chip icon="clock" caret>
            <span className="tnum">{align ? w.incident_label : tx("最近 1 小时", "Last hour")}</span>
          </Chip>
          <Chip icon="activity" active={align} onClick={() => setAlign((a) => !a)}>
            {tx("对齐到事件时间", "Align to incident")}
          </Chip>
          <Btn icon="external" onClick={() => window.open("https://grafana.example.internal", "_blank", "noopener")}>
            {tx("在 Grafana 中打开", "Open in Grafana")}
          </Btn>
          <Chip icon="sparkles" active={ai} onClick={() => setAi((a) => !a)}>
            {tx("问 AI", "Ask AI")}
          </Chip>
        </>
      }
    >
      <div className="metrics-wrap">
        <div className="metrics-bar">
          <Chip caret>
            <Brand name="prometheus" size={18} />
            {tx("Prometheus · 生产", "Prometheus · prod")}
          </Chip>
          <Chip icon="grid" caret>
            {tx("看板：{s} 黄金信号", "Dashboard: {s} golden signals", { s: service })}
          </Chip>
          <LabelChip k="service" v={service} />
          <LabelChip k="env" v="prod" />
          <LabelChip k="pod" v={tx("全部 6", "all 6")} />
          <Chip icon="plus">{tx("添加变量", "Add variable")}</Chip>
          <Spacer />
          <Muted size={11.5}>{tx("悬停任一图表，所有面板同步显示同一时刻", "Hover any chart — all panels follow the same moment")}</Muted>
          <Chip icon="rotate" caret>
            {tx("自动刷新 30 s", "Refresh 30 s")}
          </Chip>
        </div>
        <div className="metrics-body">
          <div className="v3-row stagger" style={{ gap: 12, alignItems: "stretch" }}>
            {d.kpis.map((k) => (
              <KBand key={k.id} label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone as Tone} values={k.series} delta={k.delta} deltaGood={k.delta_good} style={{ flex: "1 1 0" }} />
            ))}
          </div>
          <div className="metrics-row">
            <Panel
              title={tx("请求量 · 按状态码", "Requests · by status")}
              style={{ flex: "2 1 0" }}
              right={
                <Row gap={4}>
                  <Legend items={[["2xx", "var(--c2)"], ["4xx", "var(--c4)"], ["5xx", "var(--c3)"]]} />
                  <IconBtn icon="pin" label={tx("钉到调查", "Pin to investigation")} size={24} onClick={() => toast(tx("已钉到 INV-231", "Pinned to INV-231"))} />
                </Row>
              }
            >
              <StackedArea series={[d.requests.ok, d.requests.client, d.requests.server]} colors={["var(--c2)", "var(--c4)", "var(--c3)"]} height={160} names={["2xx", "4xx", "5xx"]} labels={w.labels} sync={sync} axis={w.ticks}>
                <MarkersOverlay markers={markers} n={w.labels.length} band={align ? w.incident : undefined} height={160} />
              </StackedArea>
            </Panel>
            <Panel title={tx("SLO · 可用性 99.9%", "SLO · availability 99.9%")} style={{ flex: "1 1 0" }} right={<TextLink>{tx("SLO 设置", "SLO settings")}</TextLink>}>
              <Col gap={9}>
                <Row gap={16}>
                  <Gauge pct={d.slo.remaining / 100} size={140} tone="warn" label={`${d.slo.remaining}%`} sub={tx("错误预算剩余", "budget left")} />
                  <Col gap={2}>
                    <Muted size={11.5}>{tx("本月可用性", "This month")}</Muted>
                    <b className="tnum" style={{ fontSize: 22, fontWeight: 600 }}>
                      {d.slo.availability}
                    </b>
                    <span className="tnum" style={{ color: "var(--danger)", fontSize: 11.5 }}>{d.slo.delta}</span>
                  </Col>
                </Row>
                {d.slo.burn.map((b) => (
                  <Row key={b.window} gap={8}>
                    <Muted>{b.window}</Muted>
                    <Spacer />
                    <Bar pct={b.pct} tone={b.tone} width={90} />
                    <Badge tone={b.tone} mono>
                      {b.value}
                    </Badge>
                  </Row>
                ))}
                <div className="danger-note">
                  <Icon name="alert" size={13} />
                  {d.slo.note}
                </div>
              </Col>
            </Panel>
          </div>
          <div className="metrics-row">
            <Panel
              title={tx("延迟分布 · 按请求耗时", "Latency distribution")}
              style={{ flex: "2 1 0" }}
              right={
                <Row gap={4}>
                  <Segmented label={tx("延迟图表类型", "Latency chart")} value={lat} onChange={setLat} options={[{ value: "heat", label: tx("热力图", "Heatmap") }, { value: "pct", label: tx("分位线", "Percentiles") }, { value: "box", label: tx("箱线图", "Box plot") }]} />
                  <IconBtn icon="pin" label={tx("钉到调查", "Pin to investigation")} size={24} />
                </Row>
              }
            >
              <div style={{ height: 176 }} key={lat} className="anim-fade">
                {lat === "heat" && (
                  <div style={{ position: "relative" }}>
                    <Heatmap matrix={d.latency.heat} height={160} rowLabels={d.latency.buckets} />
                    <div style={{ position: "absolute", left: 56, right: 0, top: 0 }}>
                      <MarkersOverlay markers={markers} n={w.labels.length} band={align ? w.incident : undefined} height={160} />
                    </div>
                  </div>
                )}
                {lat === "pct" && (
                  <Lines
                    series={[{ name: "P50", values: d.latency.p50, color: "var(--c1)" }, { name: "P95", values: d.latency.p95, color: "var(--c5)" }, { name: "P99", values: d.latency.p99, color: "var(--c3)" }]}
                    height={160}
                    max={2000}
                    threshold={300}
                    markers={markers}
                    bands={bands}
                    labels={w.labels}
                    format={(v) => `${Math.round(v)} ms`}
                    sync={sync}
                  />
                )}
                {lat === "box" && <BoxPlot stats={stats} height={150} max={2000} />}
              </div>
              <Axis labels={w.ticks} style={{ paddingLeft: lat === "heat" ? 56 : 0 }} />
              <Row gap={8} style={{ marginTop: 8 }}>
                <Muted size={11.5}>{tx("热力图颜色越深表示该耗时区间的请求越多；发布后请求整体上移到 500 ms – 1 s。", "Darker cells mean more requests in that latency bucket; after the release requests shift to 500 ms – 1 s.")}</Muted>
                <Spacer />
                <Legend items={[["P50", "var(--c1)"], ["P95", "var(--c5)"], ["P99", "var(--c3)"]]} />
              </Row>
            </Panel>
            <Panel title={tx("接口 P99 排行", "Endpoint P99")} style={{ flex: "1 1 0" }} right={<Badge>{tx("按 P99 ↓", "By P99 ↓")}</Badge>}>
              <Col gap={10}>
                <HBars items={d.endpoints.map((e) => ({ label: e.label, value: e.value, tone: e.tone }))} labelWidth={130} unit=" ms" />
                <Row gap={6}>
                  <Muted size={11.5}>{tx("下单相关接口受影响最大", "Order endpoints are hit hardest")}</Muted>
                  <Spacer />
                  <TextLink to="/traces">{tx("查看链路", "View traces")}</TextLink>
                </Row>
              </Col>
            </Panel>
          </div>
          <Panel title={tx("按实例 · 连接等待 P95（ms）", "By instance · connection wait P95 (ms)")} right={<Row gap={6}><Muted size={11.5}>{tx("6 个实例中 4 个异常；7f9c-6 为回滚后新建", "4 of 6 instances degraded; 7f9c-6 was created after the rollback")}</Muted><IconBtn icon="pin" label={tx("钉到调查", "Pin")} size={24} /></Row>}>
            <SmallMultiples items={d.instances.map((i) => ({ label: i.label, values: i.values, tone: i.tone }))} cols={6} height={42} />
          </Panel>
        </div>
        <aside className={cx("metrics-ai", ai && "open")} aria-label={tx("问 AI", "Ask AI")} aria-hidden={!ai}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <AiAvatar size={28} />
            <Col gap={0} grow>
              <Strong size={14}>{tx("问 AI", "Ask AI")}</Strong>
              <Muted size={11.5}>{tx("已读取当前视图：6 个面板 · 时间范围跟随看板", "Read the current view: 6 panels · same time range")}</Muted>
            </Col>
            <IconBtn icon="x" label={tx("关闭", "Close")} onClick={() => setAi(false)} />
          </Row>
          <Col gap={14} style={{ padding: 16, flexGrow: 1, minHeight: 0, overflowY: "auto" }}>
            <div className="bubble-user">{tx("为什么 14:05 之后 5xx 上升了？", "Why did 5xx rise after 14:05?")}</div>
            <Col gap={10}>
              <Row gap={6}>
                <AiAvatar size={20} />
                <Strong size={12}>AI</Strong>
                <Muted size={11.5}>{tx("读取 4 个指标 · 1 条变更", "4 metrics · 1 change read")}</Muted>
              </Row>
              <Markdown text={tx("所有面板都在 14:03 发布 v2.8.1 之后变化。**连接等待 P95 从 12 ms 升到 420 ms**[1]，随后 5xx 升到 8.4%，P99 升到 1.6 s；CPU 只上升 26%，不是主因。", "Every panel changed after v2.8.1 at 14:03. **Connection wait P95 went from 12 ms to 420 ms**[1], then 5xx rose to 8.4% and P99 to 1.6 s; CPU only rose 26%, so it isn't the cause.")} />
              <Card style={{ padding: "10px 12px" }}>
                <Row gap={6}>
                  <Muted size={11.5}>{tx("连接等待 P95 与 5xx 比例", "Conn wait P95 vs 5xx")}</Muted>
                  <Spacer />
                  <Legend items={[[tx("等待", "wait"), "var(--c3)"], ["5xx", "var(--c7)"]]} />
                </Row>
                <Lines series={[{ name: tx("等待 ×", "wait"), values: d.kpis[3].series.map((v) => (v / 420) * 10), color: "var(--c3)" }, { name: "5xx %", values: d.kpis[1].series, color: "var(--c7)" }]} height={64} max={11} markers={[{ at: w.release_at, label: tx("发布", "release"), tone: "warn" }]} grid={2} />
              </Card>
              <Markdown text={tx("发布内容包含 `connection_pool.max 200 → 50`[2]，最可能的原因是连接池被打满。", "The release includes `connection_pool.max 200 → 50`[2] — most likely the pool is saturated.")} />
              <Row gap={6} wrap>
                <Cite n={1}>{tx("连接等待 P95", "Conn wait P95")}</Cite>
                <Cite n={2}>GitLab #8812</Cite>
              </Row>
              <NextSteps items={[{ icon: "branch", label: tx("带入调查 INV-231", "Add to INV-231"), onClick: () => nav("/canvas/INV-231") }, { icon: "bell", label: tx("生成告警规则", "Create alert rule"), onClick: () => nav("/alert-rules") }]} />
            </Col>
          </Col>
          <div style={{ padding: "0 16px 16px" }}>
            <Composer minRows={1} maxRows={6} placeholder={tx("继续追问这些图…", "Ask more about these charts…")} attachments={[{ kind: "view", id: "view", label: tx("当前视图", "Current view") }]} viewAttachment={{ kind: "view", id: "view", label: tx("当前视图", "Current view") }} onSend={(t, att, x) => nav(newConversationUrl(t, att, x))} />
          </div>
        </aside>
      </div>
    </Page>
  );
}

/** Release / rollback markers and the optional incident band, drawn over non-line charts. */
function MarkersOverlay({ markers, n, band, height }: { markers: { at: number; label: string; tone: string }[]; n: number; band?: [number, number]; height: number }) {
  const x = (i: number) => `${(100 * i) / (n - 1)}%`;
  return (
    <div className="markers-overlay" style={{ height }}>
      {band && <span className="band anim-fade" style={{ left: x(band[0]), width: `calc(${x(band[1])} - ${x(band[0])})` }} />}
      {markers.map((m) => (
        <span key={m.at} className="mk" style={{ left: x(m.at), borderColor: `var(--${m.tone})` }}>
          <em style={{ color: `var(--${m.tone})`, borderColor: `var(--${m.tone})` }}>{m.label}</em>
        </span>
      ))}
    </div>
  );
}
