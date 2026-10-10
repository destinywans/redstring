import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { useEnv } from "../../app/environment";
import { PageUnavailable, PartialBanner, Unavailable, UnknownValue, missingParts, unknownLabels } from "../../ui/unknown";
import type { MetricsDashboard, ServiceSloSettings } from "../../api/types";
import { AiAvatar, Badge, Bar, Brand, Btn, Card, Chip, Cite, Col, Icon, IconBtn, LabelChip, Legend, Muted, Panel, Row, Segmented, Skeleton, Spacer, Strong, TextLink, cx, type Tone } from "../../ui/core";
import { KBand } from "../../ui/data";
import { newConversationUrl } from "../../api/stream";
import { Composer, NextSteps } from "../../ui/Composer";
import { Markdown } from "../../ui/Markdown";
import { toast } from "../../ui/overlays";
import { Axis, BoxPlot, Gauge, HBars, Heatmap, Lines, SmallMultiples, StackedArea } from "../../charts";
import { useI18n } from "../../i18n";
import { useNeed } from "../../ui/permission";
import "./observe.css";
import { ChipSelect, RangeChip, AddChip, openExternal, openForm, usePickLists } from "../../ui/demo";
import { useWhen } from "../../ui/when";

export default function MetricsPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const askBlocked = useNeed(["conversation", "change"]);
  const nav = useNavigate();
  const [params] = useSearchParams();
  const service = params.get("service") ?? (API_MODE === "mock" ? "checkout-api" : "");
  const lists = usePickLists();
  const demo = API_MODE === "mock";
  const env = useEnv((s) => s.env);
  const [align, setAlign] = useState(false);
  const { data: d, error } = useGet<MetricsDashboard>(service ? "/metrics/dashboard" : null, { service, range: demo ? undefined : "1h", align: demo ? undefined : align ? "true" : undefined });
  // Aligning needs events.view; a 403 for the aligned read drops back to the plain window instead of blanking the page.
  useEffect(() => {
    if (align && error instanceof ApiError && error.status === 403) {
      setAlign(false);
      toast(tx("没有查看事件的权限，不能按事件时间对齐", "You can't view incidents here, so the chart can't align to one"), { tone: "danger" });
    }
  }, [align, error]); // eslint-disable-line react-hooks/exhaustive-deps
  const { data: sloCfg, refetch: refetchSlo } = useGet<ServiceSloSettings>(!demo && service ? `/services/${encodeURIComponent(service)}/slo` : null);
  const [ai, setAi] = useState(false);
  const [lat, setLat] = useState<"heat" | "pct" | "box">("heat");
  const [hover, setHover] = useState<number | null>(null);
  const sync = { index: hover, set: setHover };
  if (!service)
    return (
      <Page crumbs={[tx("资源与观测", "Resources"), tx("指标", "Metrics")]}>
        <PageUnavailable icon="activity" configure={false} title={tx("先选一个服务", "Pick a service first")} desc={tx("指标看板按单个服务展示。从服务目录或服务详情进入。", "The metrics dashboard covers one service. Open it from the service catalog or a service page.")} />
      </Page>
    );
  if (!d)
    return (
      <Page crumbs={[tx("资源与观测", "Resources"), tx("指标", "Metrics")]} error={error} errorSource="Prometheus">
        <div className="v3-content">
          <Skeleton h={120} />
          <Skeleton h={260} />
          <Skeleton h={260} />
        </div>
      </Page>
    );
  const w = d.window;
  // PA-83: labels from their instants, in the viewer's time zone (the text forms are UTC "15:04").
  const labels = when.axis(w.label_times, w.labels);
  const ticks = when.axis(w.tick_times, w.ticks);
  const incidentText = w.incident_start_at
    ? tx("事件 · {a}–{b}", "Incident · {a}–{b}", { a: when.time(w.incident_start_at), b: when.time(w.incident_end_at ?? w.incident_start_at) })
    : w.incident_label;
  const u = d.unavailable ?? {};
  // Markers come from the platform's own release / rollback records (null when none fall in the window).
  // The sample data names its sample release; real mode only states what the record is.
  const markers = [
    w.release_at !== null && { at: w.release_at, label: demo ? tx("v2.8.1 发布", "v2.8.1 release") : tx("发布", "Release"), tone: "warn" },
    w.rollback_at !== null && { at: w.rollback_at, label: demo ? tx("限制重试", "Retries limited") : tx("回滚", "Rollback"), tone: "ok" },
  ].filter((m): m is { at: number; label: string; tone: string } => !!m);
  const incident = align && w.incident ? w.incident : undefined;
  const bands = incident ? [{ from: incident[0], to: incident[1], color: "var(--accent-soft)" }] : [];
  // The box plot is estimated from p95 for the demo; real mode has no quartile source.
  const stats =
    demo && d.latency
      ? Array.from({ length: 12 }, (_, k) => {
          const seg = d.latency!.p95.slice(k * 3, k * 3 + 3);
          const m = seg.reduce((a, b) => a + b, 0) / 3;
          return { label: labels[k * 3], min: m * 0.35, q1: m * 0.7, med: m, q3: m * 1.3, max: m * 2.1 };
        })
      : [];
  const missing = unknownLabels([u], {
    kpis: tx("关键指标", "key metrics"),
    requests: tx("请求量", "requests"),
    slo: "SLO",
    latency: tx("延迟分布", "latency"),
    endpoints: tx("接口排行", "endpoints"),
    instances: tx("实例", "instances"),
  });
  const sloTarget = demo ? "99.9" : sloCfg?.target ?? null;
  // Without the heatmap panel (latency.heat null, PA-41) the latency chart falls back to percentiles.
  const latMode = lat === "heat" && !(d.latency?.heat && d.latency?.buckets) ? "pct" : lat;
  const KPI_LABEL: Record<string, string> = { rps: tx("请求量", "Requests"), err: tx("错误率", "Error rate"), p99: tx("P99 延迟", "P99 latency"), wait: tx("等待", "Wait"), cpu: tx("CPU", "CPU") };
  const editSlo = async () => {
    const v = await openForm({
      title: tx("SLO 设置 · {s}", "SLO settings · {s}", { s: service }),
      fields: [
        { key: "target", label: tx("可用性目标", "Availability target"), type: "select", initial: (demo ? "99.9" : sloCfg?.target) ?? "99.9", options: ["99.5", "99.9", "99.95", "99.99"].map((t) => ({ value: t, label: `${t}%` })) },
        { key: "window", label: tx("统计窗口", "Window"), type: "select", initial: (demo ? "30d" : sloCfg?.window) ?? "30d", options: [{ value: "7d", label: tx("7 天", "7 days") }, { value: "30d", label: tx("30 天", "30 days") }] },
      ],
      submit: tx("保存", "Save"),
    });
    if (!v) return;
    if (demo) return toast(tx("SLO 已更新为 {t}% / {w}", "SLO set to {t}% / {w}", { t: v.target, w: v.window }));
    try {
      await api("PUT", `/services/${encodeURIComponent(service)}/slo`, { body: { expected_revision: sloCfg?.revision ?? 0, target: v.target, window: v.window } });
      void refetchSlo();
      toast(tx("SLO 目标已保存为 {t}% / {w}；错误预算由指标来源另行计算", "SLO target saved as {t}% / {w}; the budget is computed from the metrics source", { t: v.target, w: v.window }));
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        void refetchSlo();
        return toast(tx("SLO 刚被其他人修改，已重新读取，请再试一次", "Someone just changed the SLO; it was reloaded — try again"), { tone: "danger" });
      }
      toast(e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
    }
  };
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("指标", "Metrics")]}
      ai={false}
      actions={
        <>
          {align && incidentText ? (
            <Chip icon="clock" onClick={() => setAlign(false)}>
              <span className="tnum">{incidentText}</span>
            </Chip>
          ) : demo ? (
            <RangeChip defaultValue="1h" />
          ) : (
            <Badge>{tx("最近 1 小时", "Last 1 hour")}</Badge>
          )}
          {/* Only offered when the window has a recorded incident to align to. */}
          {(w.incident || align) && (
            <Chip icon="activity" active={align} onClick={() => setAlign((a) => !a)}>
              {tx("对齐到事件时间", "Align to incident")}
            </Chip>
          )}
          {demo && (
            <Btn icon="external" onClick={() => openExternal("Grafana", `https://grafana.example.internal/d/golden-signals?var-service=${service}`)}>
              {tx("在 Grafana 中打开", "Open in Grafana")}
            </Btn>
          )}
          <Chip icon="sparkles" active={ai} onClick={() => setAi((a) => !a)}>
            {tx("问 AI", "Ask AI")}
          </Chip>
        </>
      }
    >
      <div className="metrics-wrap">
        <div className="metrics-bar">
          {demo && <ChipSelect icon="database" label="" showLabel={false} defaultValue="prom" options={[{ value: "prom", label: tx("Prometheus · 生产", "Prometheus · prod") }, { value: "vm", label: "VictoriaMetrics · prod" }]} />}
          <ChipSelect
            icon="grid"
            label={tx("看板", "Dashboard")}
            value={service}
            onChange={(v) => nav(`/metrics?service=${v}`)}
            options={(lists.services.length ? lists.services : [{ value: service, label: service }]).map((s) => ({ value: s.value, label: tx("{s} 黄金信号", "{s} golden signals", { s: s.value }) }))}
          />
          <LabelChip k="service" v={service} />
          <LabelChip k="env" v={demo ? "prod" : env || "—"} />
          {demo && <LabelChip k="pod" v={tx("全部 6", "all 6")} />}
          {demo && <AddChip label={tx("添加变量", "Add variable")} free placeholder="namespace=checkout" options={["namespace=checkout", "zone=sh-a", "version=v2.8.1"]} />}
          <Spacer />
          <Muted size={11.5}>{tx("悬停任一图表，所有面板同步显示同一时刻", "Hover any chart — all panels follow the same moment")}</Muted>
          {demo && <ChipSelect icon="rotate" label={tx("自动刷新", "Refresh")} defaultValue="30s" options={[{ value: "off", label: tx("关闭", "Off") }, { value: "10s", label: "10 s" }, { value: "30s", label: "30 s" }, { value: "1m", label: "1 min" }]} />}
        </div>
        <div className="metrics-body">
          {missing.length > 0 && <PartialBanner fields={missing} />}
          {d.kpis ? (
            <div className="v3-row stagger" style={{ gap: 12, alignItems: "stretch" }}>
              {d.kpis.map((k) => (
                <KBand key={k.id} label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone as Tone} values={k.series} delta={k.delta} deltaGood={k.delta_good} style={{ flex: "1 1 0" }} />
              ))}
              {/* Panels the server couldn't prove come back as kpis.<id> (PA-41). */}
              {missingParts(u, "kpis").map((id) => (
                <Card key={id} className="v3-kpi" style={{ flex: "1 1 0" }}>
                  <Muted>{KPI_LABEL[id] ?? id}</Muted>
                  <UnknownValue reason={u[`kpis.${id}`]} source="Prometheus" />
                </Card>
              ))}
            </div>
          ) : (
            <Card>
              <Unavailable reason={u.kpis} source="Prometheus" title={tx("关键指标", "Key metrics")} icon="activity" />
            </Card>
          )}
          <div className="metrics-row">
            <Panel
              title={tx("请求量 · 按状态码", "Requests · by status")}
              style={{ flex: "2 1 0" }}
              right={
                <Row gap={4}>
                  <Legend items={[["2xx", "var(--c2)"], ["4xx", "var(--c4)"], ["5xx", "var(--c3)"]]} />
                  {demo && <IconBtn icon="pin" label={tx("钉到调查", "Pin to investigation")} size={24} onClick={() => toast(tx("已钉到 INV-231", "Pinned to INV-231"))} />}
                </Row>
              }
            >
              {d.requests ? (
                <StackedArea series={[d.requests.ok, d.requests.client, d.requests.server]} colors={["var(--c2)", "var(--c4)", "var(--c3)"]} height={160} names={["2xx", "4xx", "5xx"]} labels={labels} sync={sync} axis={ticks}>
                  <MarkersOverlay markers={markers} n={labels.length} band={incident} height={160} />
                </StackedArea>
              ) : (
                <Unavailable reason={u.requests} source="Prometheus" title={tx("请求量", "Requests")} icon="activity" />
              )}
            </Panel>
            <Panel title={sloTarget ? tx("SLO · 可用性 {t}%", "SLO · availability {t}%", { t: sloTarget }) : "SLO"} style={{ flex: "1 1 0" }} right={<TextLink onClick={() => void editSlo()}>{tx("SLO 设置", "SLO settings")}</TextLink>}>
              {!d.slo ? (
                <Unavailable reason={u.slo} source="Prometheus" title={tx("错误预算", "Error budget")} icon="shield" desc={!demo && !sloCfg?.target ? tx("还没有设置 SLO 目标；先在右上角设置目标，错误预算才能按指标计算。", "No SLO target yet; set one at the top right so the budget can be computed from metrics.") : undefined} />
              ) : (
              <Col gap={9}>
                <Row gap={16}>
                  <Gauge pct={d.slo.remaining / 100} size={140} tone="warn" label={`${d.slo.remaining}%`} sub={tx("错误预算剩余", "budget left")} />
                  <Col gap={2}>
                    <Muted size={11.5}>{tx("本月可用性", "This month")}</Muted>
                    <b className="tnum" style={{ fontSize: 22, fontWeight: 600 }}>
                      {d.slo.availability}
                    </b>
                    <span className="tnum" style={{ color: "var(--danger)", fontSize: "var(--fs-sub)" }}>{d.slo.delta}</span>
                  </Col>
                </Row>
                {missingParts(u, "slo.burn").map((w) => (
                  <Row key={w} gap={8}>
                    <Muted>{tx("{w} 燃烧速率", "{w} burn", { w })}</Muted>
                    <Spacer />
                    <UnknownValue reason={u[`slo.burn.${w}`]} source="Prometheus" />
                  </Row>
                ))}
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
                {d.slo.note && (
                  <div className="danger-note">
                    <Icon name="alert" size={13} />
                    {d.slo.note}
                  </div>
                )}
              </Col>
              )}
            </Panel>
          </div>
          <div className="metrics-row">
            <Panel
              title={tx("延迟分布 · 按请求耗时", "Latency distribution")}
              style={{ flex: "2 1 0" }}
              right={
                <Row gap={4}>
                  {d.latency && <Segmented label={tx("延迟图表类型", "Latency chart")} value={latMode} onChange={setLat} options={[...(d.latency.heat && d.latency.buckets ? [{ value: "heat" as const, label: tx("热力图", "Heatmap") }] : []), { value: "pct" as const, label: tx("分位线", "Percentiles") }, ...(demo ? [{ value: "box" as const, label: tx("箱线图", "Box plot") }] : [])]} />}
                  {demo && <IconBtn icon="pin" label={tx("钉到调查", "Pin to investigation")} size={24} />}
                </Row>
              }
            >
              {!d.latency ? (
                <Unavailable reason={u.latency} source="Prometheus" title={tx("延迟分布", "Latency distribution")} icon="activity" />
              ) : (
              <>
              <div style={{ height: 176 }} key={latMode} className="anim-fade">
                {latMode === "heat" && d.latency.heat && d.latency.buckets && (
                  <div style={{ position: "relative" }}>
                    <Heatmap matrix={d.latency.heat} height={160} rowLabels={d.latency.buckets} />
                    <div style={{ position: "absolute", left: 56, right: 0, top: 0 }}>
                      <MarkersOverlay markers={markers} n={labels.length} band={incident} height={160} />
                    </div>
                  </div>
                )}
                {latMode === "pct" && (
                  <Lines
                    series={[{ name: "P50", values: d.latency.p50, color: "var(--c1)" }, { name: "P95", values: d.latency.p95, color: "var(--c5)" }, { name: "P99", values: d.latency.p99, color: "var(--c3)" }]}
                    height={160}
                    max={2000}
                    threshold={300}
                    markers={markers}
                    bands={bands}
                    labels={labels}
                    format={(v) => `${Math.round(v)} ms`}
                    sync={sync}
                  />
                )}
                {latMode === "box" && demo && <BoxPlot stats={stats} height={150} max={2000} />}
              </div>
              <Axis labels={ticks} style={{ paddingLeft: latMode === "heat" ? 56 : 0 }} />
              <Row gap={8} style={{ marginTop: 8 }}>
                <Muted size={11.5}>{demo ? tx("热力图颜色越深表示该耗时区间的请求越多；发布后请求整体上移到 500 ms – 1 s。", "Darker cells mean more requests in that latency bucket; after the release requests shift to 500 ms – 1 s.") : tx("热力图颜色越深表示该耗时区间的请求越多。", "Darker cells mean more requests in that latency bucket.")}</Muted>
                <Spacer />
                <Legend items={[["P50", "var(--c1)"], ["P95", "var(--c5)"], ["P99", "var(--c3)"]]} />
              </Row>
              </>
              )}
            </Panel>
            <Panel title={tx("接口 P99 排行", "Endpoint P99")} style={{ flex: "1 1 0" }} right={<Badge>{tx("按 P99 ↓", "By P99 ↓")}</Badge>}>
              {!d.endpoints ? (
                <Unavailable reason={u.endpoints} source="Prometheus" title={tx("接口排行", "Endpoint ranking")} icon="activity" />
              ) : (
                <Col gap={10}>
                  <HBars items={d.endpoints.map((e) => ({ label: e.label, value: e.value, tone: e.tone }))} labelWidth={130} unit=" ms" />
                  <Row gap={6}>
                    {demo && <Muted size={11.5}>{tx("下单相关接口受影响最大", "Order endpoints are hit hardest")}</Muted>}
                    <Spacer />
                    <TextLink to={`/traces?service=${encodeURIComponent(service)}`}>{tx("查看链路", "View traces")}</TextLink>
                  </Row>
                </Col>
              )}
            </Panel>
          </div>
          <Panel
            title={demo ? tx("按实例 · 等待库存 P95（ms）", "By instance · waiting on inventory P95 (ms)") : tx("按实例 · 等待 P95（ms）", "By instance · wait P95 (ms)")}
            right={
              demo && (
                <Row gap={6}>
                  <Muted size={11.5}>{tx("6 个实例中 4 个严重、1 个轻微；7f9c-6 未受影响", "4 of 6 instances severe, 1 mild; 7f9c-6 unaffected")}</Muted>
                  <IconBtn icon="pin" label={tx("钉到调查", "Pin")} size={24} onClick={() => toast(tx("已钉到 INV-231，作为证据节点", "Pinned to INV-231 as an evidence node"))} />
                </Row>
              )
            }
          >
            {d.instances ? (
              <SmallMultiples items={d.instances.map((i) => ({ label: i.label, values: i.values, tone: i.tone }))} cols={6} height={42} />
            ) : (
              <Unavailable reason={u.instances} source="Prometheus" title={tx("实例", "Instances")} icon="cpu" />
            )}
          </Panel>
        </div>
        <aside className={cx("metrics-ai", ai && "open")} aria-label={tx("问 AI", "Ask AI")} aria-hidden={!ai}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <AiAvatar size={28} />
            <Col gap={0} grow>
              <Strong size={14}>{tx("问 AI", "Ask AI")}</Strong>
              <Muted size={11.5}>{demo ? tx("已读取当前视图：6 个面板 · 时间范围跟随看板", "Read the current view: 6 panels · same time range") : tx("提问时会带上当前视图", "Your question carries the current view")}</Muted>
            </Col>
            <IconBtn icon="x" label={tx("关闭", "Close")} onClick={() => setAi(false)} />
          </Row>
          <Col gap={14} style={{ padding: 16, flexGrow: 1, minHeight: 0, overflowY: "auto" }}>
            {/* A canned sample exchange; real mode starts a real conversation from the composer below. */}
            {demo && d.kpis && (
            <>
            <div className="bubble-user">{tx("为什么 14:05 之后 5xx 上升了？", "Why did 5xx rise after 14:05?")}</div>
            <Col gap={10}>
              <Row gap={6}>
                <AiAvatar size={20} />
                <Strong size={12}>AI</Strong>
                <Muted size={11.5}>{tx("读取 4 个指标 · 1 条变更", "4 metrics · 1 change read")}</Muted>
              </Row>
              <Markdown text={tx("各面板在 14:03 发布 v2.8.1 前后同时变化，但请求量持平。**库存调用量从 1.2k/s 升到 3.9k/s**[1]，随后 5xx 升到 8.4%，P99 升到 1.6 s；CPU 只上升 26%，不是主因。", "Every panel moved around v2.8.1 at 14:03, yet requests stayed flat. **Inventory calls went from 1.2k/s to 3.9k/s**[1], then 5xx rose to 8.4% and P99 to 1.6 s; CPU only rose 26%, so it isn't the cause.")} />
              <Card style={{ padding: "10px 12px" }}>
                <Row gap={6}>
                  <Muted size={11.5}>{tx("库存调用量与 5xx 比例", "Inventory calls vs 5xx")}</Muted>
                  <Spacer />
                  <Legend items={[[tx("调用", "calls"), "var(--c3)"], ["5xx", "var(--c7)"]]} />
                </Row>
                <Lines series={[{ name: tx("调用 ×", "calls"), values: d.kpis[3].series.map((v) => (v / 3.9) * 10), color: "var(--c3)" }, { name: "5xx %", values: d.kpis[1].series, color: "var(--c7)" }]} height={64} max={11} markers={w.release_at === null ? [] : [{ at: w.release_at, label: tx("发布", "release"), tone: "warn" }]} grid={2} />
              </Card>
              <Markdown text={tx("发布内容没有改动库存调用[2]；调用量上涨更像重试放大，需要看谁先变慢。", "The release does not touch inventory calls[2]; the rise in calls looks like retry amplification — check who slowed first.")} />
              <Row gap={6} wrap>
                <Cite n={1}>{tx("库存调用量", "Inventory calls")}</Cite>
                <Cite n={2}>GitLab #8812</Cite>
              </Row>
              <NextSteps items={[{ icon: "branch", label: tx("带入调查 INV-231", "Add to INV-231"), onClick: () => nav("/canvas/INV-231") }, { icon: "bell", label: tx("生成告警规则", "Create alert rule"), onClick: () => nav("/alert-rules") }]} />
            </Col>
            </>
            )}
          </Col>
          <div style={{ padding: "0 16px 16px" }}>
            <Composer minRows={1} maxRows={6} blocked={askBlocked} placeholder={tx("继续追问这些图…", "Ask more about these charts…")} attachments={[{ kind: "view", id: "view", label: tx("当前视图", "Current view") }]} viewAttachment={{ kind: "view", id: "view", label: tx("当前视图", "Current view") }} onSend={(t, att, x) => nav(newConversationUrl(t, att, x))} />
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
