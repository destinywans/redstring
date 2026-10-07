import { useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { Resilience, ServiceDetail } from "../../api/types";
import { Badge, Bar, Brand, Btn, Card, Col, Dot, Icon, Muted, Num, Panel, Row, Skeleton, Spacer, State, Strong, TextLink, Tile, type Tone } from "../../ui/core";
import { KBand } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Axis, Gauge, Lines, Swimlane } from "../../charts";
import { useI18n } from "../../i18n";
import { ServiceHeader } from "./ServicesPage";
import "./observe.css";
import { openExternal } from "../../ui/demo";

function useService() {
  const { id = "checkout-api" } = useParams();
  return useGet<ServiceDetail>(`/services/${id}`);
}

function BurnRow({ label, value, tone, pct }: { label: string; value: string; tone: Tone; pct: number }) {
  return (
    <Row gap={8}>
      <Muted>{label}</Muted>
      <Spacer />
      <Bar pct={pct} tone={tone} width={56} />
      <Badge tone={tone} mono>
        {value}
      </Badge>
    </Row>
  );
}

export default function ServiceDetailPage() {
  const { tx } = useI18n();
  const { data: s } = useService();
  if (!s)
    return (
      <Page crumbs={[tx("资源与观测", "Resources"), tx("服务目录", "Services"), "…"]}>
        <div className="v3-content">
          <Skeleton h={60} />
          <Skeleton h={300} />
        </div>
      </Page>
    );
  return (
    <Page crumbs={[tx("资源与观测", "Resources"), tx("服务目录", "Services"), s.name]} actions={<Btn kind="primary" icon="branch" to="/canvas/INV-231">{tx("发起调查", "Investigate")}</Btn>}>
      <ServiceHeader s={s} />
      <div className="v3-content anim-fade">
        <div className="svc-ai">
          <Icon name="sparkles" size={16} color="var(--accent-text)" style={{ marginTop: 2 }} />
          <Col gap={8} grow>
            <Markdown text={s.ai.text} />
            <Row gap={6}>
              <Btn h={26} icon="branch" to={`/canvas/${s.ai.investigation}`}>
                {tx("打开调查", "Open investigation")}
              </Btn>
              {s.ai.proposal && (
                <Btn h={26} icon="shield" to={`/review/${s.ai.proposal}`}>
                  {tx("复核回滚提案", "Review rollback")}
                </Btn>
              )}
              <Muted size={11.5}>{s.ai.meta}</Muted>
            </Row>
          </Col>
        </div>
        <div className="v3-row stagger" style={{ gap: 12, alignItems: "stretch" }}>
          {s.signals.map((k) => (
            <KBand key={k.id} label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone as Tone} values={k.series} delta={k.delta} deltaGood={k.delta_good} style={{ flex: "1 1 0" }} />
          ))}
        </div>
        <div className="svc-grid">
          <Panel title={tx("SLO · 可用性 {t}", "SLO · availability {t}", { t: s.slo.target })}>
            <Row gap={14}>
              <Gauge pct={s.slo.budget_left / 100} size={128} tone="warn" label={`${s.slo.budget_left}%`} sub={tx("错误预算剩余", "budget left")} />
              <Col gap={8} grow>
                <Row gap={6}>
                  <Muted>{tx("本月可用性", "This month")}</Muted>
                  <Spacer />
                  <Num>{s.slo.availability}</Num>
                </Row>
                {s.slo.burn.map((b) => (
                  <BurnRow key={b.window} label={b.window} value={b.value} tone={b.tone} pct={b.pct} />
                ))}
                <Muted size={11.5}>{s.slo.note}</Muted>
              </Col>
            </Row>
          </Panel>
          <Panel title={tx("最近变更", "Recent changes")} right={<TextLink to="/changes">{tx("全部", "All")}</TextLink>}>
            <ul>
              {s.changes.map((c) => (
                <li key={c.title} className="svc-change">
                  <Brand name={c.brand} size={22} />
                  <Col gap={0} grow>
                    <span className="ellipsis" style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>
                      {c.title}
                    </span>
                    <Muted size={11.5}>{c.meta}</Muted>
                  </Col>
                  {c.tag && <Badge tone="warn">{c.tag}</Badge>}
                </li>
              ))}
            </ul>
          </Panel>
          <Panel title={tx("依赖", "Dependencies")} right={<TextLink to="/resources">{tx("拓扑", "Topology")}</TextLink>}>
            <Col gap={7}>
              <Muted size={11}>{tx("上游", "Upstream")}</Muted>
              {s.deps.upstream.map((d) => (
                <Dep key={d.name} d={d} />
              ))}
              <Muted size={11}>{tx("下游", "Downstream")}</Muted>
              {s.deps.downstream.map((d) => (
                <Dep key={d.name} d={d} />
              ))}
            </Col>
          </Panel>
        </div>
      </div>
    </Page>
  );
}

function Dep({ d }: { d: ServiceDetail["deps"]["upstream"][number] }) {
  return (
    <Row gap={8}>
      {d.brand ? <Brand name={d.brand} size={20} /> : <Tile icon="box" size={20} />}
      <span className="grow" style={{ fontSize: "var(--fs-sm)" }}>
        {d.name}
      </span>
      <Muted size={11.5}>{d.meta}</Muted>
      <Dot tone={d.status} size={7} />
    </Row>
  );
}

function Source({ brand, text, link }: { brand: string; text: string; link: string }) {
  return (
    <Row gap={6} style={{ paddingTop: 8, borderTop: "1px solid var(--border)" }}>
      {brand && <Brand name={brand} size={20} />}
      <Muted size={11.5}>{text}</Muted>
      <Spacer />
      <TextLink icon="external" onClick={() => openExternal(brand || link, `https://${brand || "tools"}.example.internal/`)}>{link}</TextLink>
    </Row>
  );
}

export function ResiliencePage() {
  const { tx } = useI18n();
  const { data: s } = useService();
  const { data: r } = useGet<Resilience>(s ? `/services/${s.id}/resilience` : null);
  const propose = (what: string) => toast(tx("已生成提案「{w}」，等待人工批准", "Proposal “{w}” created — awaiting approval", { w: what }), { undo: () => undefined });
  if (!s || !r)
    return (
      <Page crumbs={[tx("资源与观测", "Resources"), tx("服务目录", "Services"), "…"]}>
        <div className="v3-content">
          <Skeleton h={60} />
          <Skeleton h={500} />
        </div>
      </Page>
    );
  const STATE = { closed: ["ok", tx("关闭", "Closed")], open: ["danger", tx("打开", "Open")], half_open: ["warn", tx("半开", "Half-open")], none: ["neutral", tx("未配置", "None")] } as const;
  return (
    <Page crumbs={[tx("资源与观测", "Resources"), tx("服务目录", "Services"), { label: s.name, to: `/services/${s.id}` }, tx("流量与韧性", "Traffic & resilience")]} actions={<Btn kind="primary" icon="branch" to="/canvas/INV-231">{tx("发起调查", "Investigate")}</Btn>}>
      <ServiceHeader s={s} />
      <div className="v3-content anim-fade">
        <Card className="ro-banner">
          <Tile icon="lock" tone="info" />
          <Col gap={1} grow>
            <Strong>{tx("只读视图：规则的真相源在各自系统中", "Read-only: each rule's source of truth is its own system")}</Strong>
            <Muted size={11.5}>{tx("平台聚合展示灰度、熔断、限流、开关与伸缩状态，供排障和 Agent 取证。需要干预时点「提议…」：可逆动作走提案与人工批准，配置类改动在配置仓库发起合并请求。", "The platform aggregates canary, breaker, rate-limit, flag and autoscaling state for troubleshooting and agent evidence. To intervene, use “Propose…”: reversible actions go through proposal and approval; config changes open a merge request in the config repo.")}</Muted>
          </Col>
          <Muted size={11.5}>{r.synced}</Muted>
          <Btn icon="rotate" onClick={() => toast(tx("已从 5 个来源重新同步", "Re-synced from 5 sources"))}>{tx("立即同步", "Sync now")}</Btn>
        </Card>
        <div className="res-grid stagger">
          <Panel title={tx("灰度发布", "Progressive delivery")} icon="branch" right={<Badge>{tx("只读", "Read-only")}</Badge>}>
            <Col gap={12}>
              <Row gap={8}>
                <Strong size={13}>{r.rollout.title}</Strong>
                <State tone="accent">{r.rollout.status}</State>
                <Spacer />
                <Muted size={11.5}>{r.rollout.stable}</Muted>
              </Row>
              <div className="ro-steps">
                {r.rollout.steps.map((st, i) => (
                  <div key={st.weight} className={`ro-step ${st.state}`}>
                    <span className="mk">{st.state === "done" ? <Icon name="check" size={12} /> : i + 1}</span>
                    <b>{st.weight}</b>
                    <Muted size={11}>{st.note}</Muted>
                    {i < r.rollout.steps.length - 1 && <span className={`ln ${st.state === "done" ? "done" : ""}`} />}
                  </div>
                ))}
              </div>
              <Col gap={5}>
                <Row gap={6}>
                  <Muted size={11.5}>{tx("流量分配", "Traffic split")}</Muted>
                  <Spacer />
                  <Muted size={11.5}>{tx("稳定 {a}% · 灰度 {b}%", "stable {a}% · canary {b}%", { a: r.rollout.split.stable, b: r.rollout.split.canary })}</Muted>
                </Row>
                <div className="ctx-bar" style={{ height: 8 }}>
                  <span style={{ width: `${r.rollout.split.stable}%`, background: "var(--c1)" }} />
                  <span style={{ width: `${r.rollout.split.canary}%`, background: "var(--c2)" }} />
                </div>
              </Col>
              <Row gap={14} align="flex-start">
                <Col gap={6} grow>
                  {r.rollout.analysis.map((a) => (
                    <Row key={a.name} gap={8}>
                      <Icon name={a.ok ? "check" : "x"} size={13} color={a.ok ? "var(--ok)" : "var(--danger)"} />
                      <span className="grow" style={{ fontSize: "var(--fs-sub)" }}>{a.name}</span>
                      <Num>{a.value}</Num>
                      <Muted size={11}>{a.threshold}</Muted>
                    </Row>
                  ))}
                </Col>
                <Card style={{ padding: "8px 10px", width: 210 }}>
                  <Muted size={11}>{tx("5xx：灰度 vs 稳定", "5xx: canary vs stable")}</Muted>
                  <Lines series={[{ name: tx("稳定", "stable"), values: r.rollout.stable_series, color: "var(--c1)" }, { name: tx("灰度", "canary"), values: r.rollout.canary_series, color: "var(--c2)" }]} height={48} max={0.6} grid={2} format={(v) => `${v.toFixed(2)}%`} />
                </Card>
              </Row>
              <Row gap={6}>
                <Btn h={26} icon="shield" onClick={() => propose(tx("暂停灰度", "Pause canary"))}>{tx("提议暂停", "Propose pause")}</Btn>
                <Btn h={26} icon="rotate" onClick={() => propose(tx("回滚灰度", "Roll back canary"))}>{tx("提议回滚", "Propose rollback")}</Btn>
                <Spacer />
                <Muted size={11.5}>{r.rollout.next}</Muted>
              </Row>
              <Source brand="argo" text={r.rollout.source} link={tx("在 Argo CD 中打开", "Open in Argo CD")} />
            </Col>
          </Panel>
          <Panel title={tx("熔断", "Circuit breakers")} icon="shield" right={<Badge>{tx("只读", "Read-only")}</Badge>}>
            <Col gap={10}>
              <div className="brk-row head">
                <span>{tx("上游", "Upstream")}</span>
                <span>{tx("机制与阈值", "Mechanism")}</span>
                <span>{tx("状态", "State")}</span>
                <span style={{ textAlign: "right" }}>{tx("驱逐", "Ejected")}</span>
              </div>
              {r.breakers.map((b) => (
                <div key={b.upstream} className="brk-row">
                  <Row gap={8}>
                    {b.brand ? <Brand name={b.brand} size={22} /> : <Tile icon="database" size={22} />}
                    <span className="ellipsis" style={{ fontWeight: 500 }}>{b.upstream}</span>
                  </Row>
                  <Col gap={0}>
                    <span>{b.mechanism}</span>
                    <Muted size={11}>{b.threshold}</Muted>
                  </Col>
                  <span>{b.state === "none" ? <Badge>{STATE.none[1]}</Badge> : <State tone={STATE[b.state][0]}>{STATE[b.state][1]}</State>}</span>
                  <span className="tnum" style={{ textAlign: "right" }}>{b.ejected}</span>
                </div>
              ))}
              <Col gap={6}>
                <Muted size={11.5}>{tx("payment-gateway 熔断状态 · 最近 1 小时", "payment-gateway breaker · last hour")}</Muted>
                <Swimlane labelWidth={0} rows={[{ label: "", segs: r.breaker_timeline.map((t) => ({ from: t.from, to: t.to, tone: STATE[t.state][0], text: STATE[t.state][1] })) }]} />
                <Axis labels={["13:35", "13:50", "14:05", "14:20", "14:35"]} />
              </Col>
              <div className="warn-note">
                <Icon name="alert" size={13} style={{ marginTop: 2 }} />
                <span>{r.breaker_note}</span>
              </div>
              <Row gap={6}>
                <Btn h={26} icon="sparkles" onClick={() => toast(tx("已生成改进建议，加入 Runbook 草稿", "Advice drafted into a runbook"))}>{tx("生成改进建议", "Draft advice")}</Btn>
                <Btn kind="ghost" h={26} icon="shield" onClick={() => propose(tx("调整驱逐阈值", "Tune ejection"))}>{tx("提议调整驱逐阈值", "Propose ejection change")}</Btn>
              </Row>
              <Source brand="istio" text={tx("Istio DestinationRule · Sentinel 控制台 · 同步于 2 分钟前", "Istio DestinationRule · Sentinel console · synced 2 min ago")} link={tx("在 Kiali 中打开", "Open in Kiali")} />
            </Col>
          </Panel>
          <Panel title={tx("限流", "Rate limits")} icon="filter" right={<Badge>{tx("只读", "Read-only")}</Badge>}>
            <Col gap={10}>
              {r.limits.map((x) => (
                <div key={x.route} className="rl-row hrow">
                  <Row gap={8}>
                    <Brand name={x.brand} size={22} />
                    <Col gap={0}>
                      <span className="mono" style={{ fontSize: "var(--fs-sub)" }}>{x.route}</span>
                      <Muted size={11}>{x.mechanism}</Muted>
                    </Col>
                  </Row>
                  <Col gap={1}>
                    <Row gap={6}>
                      <Bar pct={x.pct} tone={x.pct > 90 ? "danger" : x.pct > 70 ? "warn" : "ok"} width="100%" />
                      <Muted size={11}>{x.pct}%</Muted>
                    </Row>
                    <Muted size={11}>
                      {x.used} / {x.limit}
                    </Muted>
                  </Col>
                  <Badge tone={x.rejected ? "danger" : "neutral"} mono>429 · {x.rejected}</Badge>
                </div>
              ))}
              <div className="ok-note">
                <Icon name="check" size={13} />
                <span className="grow">{r.limit_note}</span>
                <TextLink to="/canvas/INV-231" icon="plus">{tx("作为证据加入", "Add as evidence")}</TextLink>
              </div>
              <Row gap={6}>
                <Btn kind="ghost" h={26} icon="shield" onClick={() => propose(tx("临时调整限流阈值（合并请求）", "Temporary limit change (MR)"))}>{tx("提议临时调整阈值", "Propose temporary change")}</Btn>
                <Spacer />
                <Muted size={11.5}>{tx("阈值来自网关配置仓库；调整会生成合并请求", "Limits live in the gateway config repo; changes open a merge request")}</Muted>
              </Row>
              <Source brand="kong" text={tx("APISIX Admin API · Kong · Nginx 配置 · 同步于 1 分钟前", "APISIX Admin API · Kong · Nginx config · synced 1 min ago")} link={tx("在网关控制台打开", "Open gateway console")} />
            </Col>
          </Panel>
          <Panel title={tx("降级与功能开关", "Degradation & feature flags")} icon="sliders" right={<Badge>{tx("只读", "Read-only")}</Badge>}>
            <Col gap={10}>
              {r.flags.map((f) => (
                <Row key={f.key} gap={10} style={{ padding: "10px 0", borderBottom: "1px solid var(--border)" }}>
                  <Brand name={f.brand} size={22} />
                  <Col gap={1} grow>
                    <Row gap={6}>
                      <span className="mono" style={{ fontSize: "var(--fs-sub)" }}>{f.key}</span>
                      <Badge tone={f.tag.tone}>{f.tag.text}</Badge>
                    </Row>
                    <Muted size={11.5}>{f.desc}</Muted>
                  </Col>
                  <span className={`ro-toggle ${f.on ? "on" : ""}`} role="img" aria-label={f.on ? tx("开启（只读）", "On (read-only)") : tx("关闭（只读）", "Off (read-only)")}>
                    <span />
                  </span>
                  <Btn kind="ghost" h={24} onClick={() => propose(`${f.action} ${f.key}`)}>{f.action}</Btn>
                </Row>
              ))}
              <Row gap={6}>
                <Icon name="lock" size={13} color="var(--muted)" />
                <Muted size={11.5}>{tx("开关只能在源系统中修改；平台通过提案调用开关系统 API，执行前需要人工批准。", "Flags change only in their source system; the platform calls the flag API through a proposal after human approval.")}</Muted>
              </Row>
              <Source brand="launchdarkly" text={tx("Unleash · LaunchDarkly · 同步于 30 秒前", "Unleash · LaunchDarkly · synced 30 s ago")} link={tx("在 Unleash 中打开", "Open in Unleash")} />
            </Col>
          </Panel>
          <Panel title={tx("弹性伸缩 · HPA", "Autoscaling · HPA")} icon="activity" right={<Badge>{tx("只读", "Read-only")}</Badge>}>
            <Col gap={10}>
              <div className="hpa-stats">
                {[
                  [tx("最小", "Min"), r.hpa.min, "var(--text)"],
                  [tx("最大", "Max"), r.hpa.max, "var(--text)"],
                  [tx("当前", "Current"), r.hpa.current, "var(--warn)"],
                  [tx("期望", "Desired"), r.hpa.desired, "var(--accent-text)"],
                ].map(([k, v, c]) => (
                  <div key={String(k)}>
                    <Muted size={11}>{k}</Muted>
                    <b className="tnum" style={{ color: String(c) }}>{v}</b>
                  </div>
                ))}
              </div>
              <Lines
                series={[
                  { name: tx("当前副本 ×10", "Current ×10"), values: r.hpa.replicas, color: "var(--c1)" },
                  { name: tx("期望副本 ×10", "Desired ×10"), values: r.hpa.desired_series, color: "var(--c6)", dashed: true },
                  { name: "CPU %", values: r.hpa.cpu, color: "var(--c4)" },
                ]}
                height={70}
                max={100}
                threshold={r.hpa.target}
                markers={[{ at: 17, label: tx("14:06 期望 8", "14:06 desired 8"), tone: "accent" }]}
                grid={2}
                axis={["13:35", "13:50", "14:05", "14:20", "14:35"]}
              />
              <div className="warn-note">
                <Icon name="alert" size={13} style={{ marginTop: 2 }} />
                <span>{r.hpa.note}</span>
              </div>
              <Source brand="kubernetes" text={tx("Kubernetes · hpa/checkout-api · 实时", "Kubernetes · hpa/checkout-api · live")} link={tx("在 Kubernetes 控制台打开", "Open in Kubernetes console")} />
            </Col>
          </Panel>
          <section className="res-ai">
            <Row gap={8}>
              <Tile icon="sparkles" tone="accent" size={24} />
              <Strong size={13}>{tx("AI 解读 · 流量与韧性", "AI read · traffic & resilience")}</Strong>
              <Spacer />
              <Muted size={11.5}>{tx("基于 5 类规则 · 14:31", "From 5 rule types · 14:31")}</Muted>
            </Row>
            <ul>
              {r.ai.map((a) => (
                <li key={a}>
                  <Markdown text={a} />
                </li>
              ))}
            </ul>
            <Row gap={6}>
              <Btn kind="primary" icon="plus" to="/canvas/INV-231">{tx("全部作为证据加入 INV-231", "Add all as evidence to INV-231")}</Btn>
              <Btn icon="file" to="/executions/EXE-51">{tx("写入复盘报告", "Add to report")}</Btn>
            </Row>
          </section>
        </div>
      </div>
    </Page>
  );
}

