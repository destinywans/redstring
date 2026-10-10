import { useMemo, useState } from "react";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { AlertRule, AlertRuleList, AlertRuleSummary } from "../../api/types";
import { Unavailable } from "../../ui/unknown";
import { Badge, Brand, Btn, Card, Chip, Code, Col, Dot, Env, Icon, Input, LabelChip, Legend, Muted, Pills, Radio, Row, SearchBox, Sev, Skeleton, Spacer, State, Tabs, TextLink, Toggle, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import "./events.css";
import { ChipSelect, AddChip, SampleOnly } from "../../ui/demo";
import { useFirstSelection } from "../../ui/selection";

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <section className="rule-step">
      <span className="n">{n}</span>
      <div className="v3-col grow" style={{ gap: 8 }}>
        <b style={{ fontSize: "var(--fs-body)" }}>{title}</b>
        {children}
      </div>
    </section>
  );
}

export default function RulesPage() {
  const { tx } = useI18n();
  const [filter, setFilter] = useState<"all" | "firing" | "scheduled">("all");
  const [q, setQ] = useState("");
  const [sel, setSel] = useState("");
  const [tab, setTab] = useState<"def" | "history" | "preview">("def");
  const list = useGet<AlertRuleList>("/alert-rules");
  // items null = no rule source configured; [] with empty_reason = nothing visible to you; [] alone = no rules yet.
  const noSource = !!list.data && list.data.items === null;
  const nothing = !!list.data && list.data.items?.length === 0;
  useFirstSelection(list.data?.items ?? undefined, (x) => x.id, sel, setSel);
  const { data: r } = useGet<AlertRule>(sel ? `/alert-rules/${sel}` : null);
  const [threshold, setThreshold] = useState(1);
  const [forMin, setForMin] = useState(5);
  const [mode, setMode] = useState<"continuous" | "cron">("continuous");
  const [ai, setAi] = useState(true);
  const [dry, setDry] = useState<string | null>(null);
  const folders = useMemo(() => {
    const items = (list.data?.items ?? []).filter((x) => (filter === "all" || (filter === "firing" ? x.state === "firing" : !!x.schedule)) && (!q || x.name.toLowerCase().includes(q.toLowerCase())));
    const m = new Map<string, AlertRuleSummary[]>();
    items.forEach((x) => m.set(x.folder, [...(m.get(x.folder) ?? []), x]));
    return [...m.entries()];
  }, [list.data, filter, q]);
  const STATE = { firing: ["danger", tx("触发中", "Firing")], pending: ["warn", tx("待定", "Pending")], ok: ["ok", tx("正常", "OK")] } as const;
  return (
    <Page
      flush
      error={!list.data ? list.error : undefined}
      errorSource={tx("告警规则来源", "alert rule source")}
      crumbs={[tx("事件与告警", "Events"), tx("告警规则", "Alert rules")]}
      actions={
        <>
          <Gate need={["alert_rules", "change"]}>
            <Btn kind="ghost" icon="download" to="/alert-rules/new?tab=import">
              {tx("导入 Prometheus 规则", "Import Prometheus rules")}
            </Btn>
          </Gate>
          <Gate need={["alert_rules", "change"]}>
            <Btn kind="primary" icon="plus" to="/alert-rules/new">
              {tx("新建规则", "New rule")}
            </Btn>
          </Gate>
        </>
      }
    >
      <div className="v3-split">
        <div className="side" style={{ width: 320, padding: 12, display: "flex", flexDirection: "column", gap: 10 }}>
          <SearchBox placeholder={tx("搜索规则", "Search rules")} value={q} onChange={setQ} width="100%" />
          <Pills value={filter} onChange={setFilter} options={[{ value: "all", label: tx("全部", "All"), count: list.data?.counts?.all }, { value: "firing", label: tx("触发中", "Firing"), count: list.data?.counts?.firing }, { value: "scheduled", label: tx("周期规则", "Scheduled"), count: list.data?.counts?.scheduled }]} />
          {folders.map(([folder, rules]) => (
            <div key={folder}>
              <div className="rule-folder">
                <Icon name="down" size={12} />
                <Icon name="grid" size={13} />
                <span className="grow">{folder}</span>
                <Muted size={11}>{rules.length}</Muted>
              </div>
              {rules.map((x) => (
                <button key={x.id} type="button" className={cx("rule-item", x.id === sel && "on")} onClick={() => setSel(x.id)}>
                  <Dot tone={x.state === "firing" ? "danger" : x.state === "pending" ? "warn" : "ok"} size={7} pulse={x.state === "firing"} />
                  <span className="ellipsis grow">{x.name}</span>
                  {x.schedule && <Muted size={11}>{x.schedule}</Muted>}
                </button>
              ))}
            </div>
          ))}
        </div>
        <div className="main">
          <Tabs value={tab} onChange={setTab} pad="0 22px" options={[{ value: "def", label: tx("定义", "Definition") }, { value: "history", label: tx("评估历史", "Evaluation history") }, { value: "preview", label: tx("通知预览", "Notification preview") }]} />
          {noSource ? (
            <div style={{ padding: 22 }}>
              <Unavailable reason={list.data?.unavailable.items} source={tx("告警规则来源", "an alert rule source")} title={tx("告警规则来源未接入", "No alert rule source connected")} icon="bell" />
            </div>
          ) : nothing ? (
            <div style={{ padding: 22 }}>
              {list.data?.empty_reason ? (
                <Unavailable reason={list.data.empty_reason} title={tx("没有你能查看的规则", "No rules you can view")} icon="bell" />
              ) : (
                <Unavailable reason="not_generated" title={tx("还没有告警规则", "No alert rules yet")} desc={tx("新建一条规则，或导入现有的 Prometheus 规则文件。", "Create a rule or import an existing Prometheus rule file.")} icon="bell" />
              )}
            </div>
          ) : !r ? (
            <Skeleton h={300} style={{ margin: 22 }} />
          ) : (
            <div className="rule-editor anim-fade" key={r.id + tab}>
              <Row gap={8} wrap>
                <h2 style={{ fontSize: 16, fontWeight: 600 }}>{r.name}</h2>
                <State tone={STATE[r.state][0]}>{STATE[r.state][1]}</State>
                <Muted size={11.5}>{r.last_eval}</Muted>
                <Spacer />
                <Btn
                  icon="play"
                  onClick={async () => {
                    const res = await api<{ would_fire: number; windows: string[] }>("POST", `/alert-rules/${r.id}/dry-run`, { body: { threshold, for_minutes: forMin } });
                    setDry(tx("最近 6 小时会触发 {n} 次：{w}", "Would have fired {n}× in the last 6 h: {w}", { n: res.would_fire, w: res.windows.join(", ") }));
                  }}
                >
                  {tx("试运行（最近 6 小时）", "Dry run (last 6 h)")}
                </Btn>
                <Btn kind="primary" icon="check" onClick={() => { void api("PUT", `/alert-rules/${r.id}`, { body: { threshold, for_minutes: forMin, mode, ai } }); toast(tx("规则已保存，下一次评估生效", "Saved — applies at the next evaluation")); }}>
                  {tx("保存", "Save")}
                </Btn>
              </Row>
              {dry && (
                <div className="dry-run anim-rise">
                  <Icon name="play" size={14} />
                  {dry}
                </div>
              )}
              {tab === "def" && (
                <>
                  <Step n={1} title={tx("查询", "Query")}>
                    <Row gap={8} wrap>
                      <ChipSelect label="" showLabel={false} icon="database" defaultValue={r.datasource} options={[r.datasource, "VictoriaMetrics · prod", "Prometheus · staging"]} />
                      <ChipSelect label={tx("已登记查询", "Registered query")} defaultValue={r.query_ref} options={[r.query_ref, "http_5xx_ratio", "http_p99_latency", "rpc_client_retry_ratio"].filter((v, i, a) => a.indexOf(v) === i)} />
                      <Muted size={11.5}>{tx("或编写 PromQL（需要规则编辑权限）", "or write PromQL (requires rule editor)")}</Muted>
                    </Row>
                    <Code>{r.expr}</Code>
                    <Card style={{ padding: "10px 12px" }}>
                      <Row gap={8}>
                        <Muted>{tx("预览 · 最近 2 小时", "Preview · last 2 h")}</Muted>
                        <Spacer />
                        <Legend items={[[tx("5xx 比例", "5xx ratio"), "var(--c3)"], [tx("阈值 {v}%", "Threshold {v}%", { v: threshold }), "var(--danger)"]]} />
                      </Row>
                      <Lines series={[{ name: "5xx", values: r.preview.values, color: "var(--c3)", fill: "var(--c3s)" }]} height={80} max={10} threshold={threshold} grid={2} format={(v) => `${v}%`} />
                    </Card>
                  </Step>
                  <div className="grid-2">
                    <Step n={2} title={tx("触发条件", "Condition")}>
                      <Row gap={8} wrap>
                        <ChipSelect label="" showLabel={false} defaultValue="gt" options={[{ value: "gt", label: tx("大于", "Above") }, { value: "lt", label: tx("小于", "Below") }, { value: "eq", label: tx("等于", "Equal to") }]} />
                        <Input type="number" value={threshold} step={0.5} onChange={(e) => setThreshold(Number(e.target.value))} width={90} right={<Muted>%</Muted>} aria-label={tx("阈值", "Threshold")} />
                        <Muted>{tx("持续", "for")}</Muted>
                        <Input type="number" value={forMin} onChange={(e) => setForMin(Number(e.target.value))} width={90} right={<Muted>{tx("分钟", "min")}</Muted>} aria-label={tx("持续时间", "Duration")} />
                      </Row>
                      <Muted size={11.5}>{tx("条件需连续满足 {m} 分钟才触发，期间显示为“待定”，避免瞬时抖动误报。", "The condition must hold for {m} minutes; meanwhile it shows as “pending” to avoid flapping.", { m: forMin })}</Muted>
                      <Row gap={8}>
                        <Muted>{tx("恢复：低于阈值持续", "Recover after below threshold for")}</Muted>
                        <Input type="number" defaultValue={r.condition.recover_minutes} width={90} right={<Muted>{tx("分钟", "min")}</Muted>} aria-label={tx("恢复时间", "Recovery")} />
                      </Row>
                    </Step>
                    <Step n={3} title={tx("评估方式", "Evaluation")}>
                      <Radio name="eval" checked={mode === "continuous"} onChange={() => setMode("continuous")} label={tx("持续评估 · 每 30 秒", "Continuous · every 30 s")} />
                      <Radio name="eval" checked={mode === "cron"} onChange={() => setMode("cron")} label={tx("按计划评估（Cron）", "Scheduled (cron)")} sub={tx("适合证书到期、容量预测这类周期检查", "For certificate expiry, capacity forecasts and similar")} />
                      <Row gap={8}>
                        <Input mono defaultValue="0 9 * * *" width={140} disabled={mode !== "cron"} aria-label="cron" />
                        <Muted>{tx("无数据时", "No data")}</Muted>
                        <ChipSelect label="" showLabel={false} defaultValue={r.evaluation.no_data} options={[r.evaluation.no_data, tx("保持上次状态", "Keep last state"), tx("视为正常", "Treat as OK"), tx("视为告警", "Treat as firing")].filter((v, i, a) => a.indexOf(v) === i)} />
                      </Row>
                    </Step>
                  </div>
                  <div className="grid-2">
                    <Step n={4} title={tx("标签与说明", "Labels & annotation")}>
                      <Row gap={6} wrap>
                        {r.labels.map((l) => (
                          <LabelChip key={l.k} k={l.k} v={l.v} />
                        ))}
                        <AddChip label={tx("标签", "Label")} free placeholder="team=trade" options={["team=trade", "severity=P2", "slo=availability"]} />
                      </Row>
                      <Input mono defaultValue={r.annotation} aria-label={tx("说明模板", "Annotation")} />
                      <Row gap={8}>
                        <Muted>Runbook</Muted>
                        <Badge>
                          <Icon name="book" size={11} />
                          {r.runbook}
                        </Badge>
                      </Row>
                    </Step>
                    <Step n={5} title={tx("通知与 AI", "Notification & AI")}>
                      <Row gap={6} wrap>
                        <Muted>{tx("匹配路由", "Route")}</Muted>
                        <Env env="prod" />
                        <Icon name="right" size={12} color="var(--faint)" />
                        <Badge>{tx("交易平台", "Trading")}</Badge>
                        <Icon name="right" size={12} color="var(--faint)" />
                        <Sev p="P1" />
                        <TextLink to="/routing">{tx("查看", "View")}</TextLink>
                      </Row>
                      <Row gap={8}>
                        <Toggle on={ai} onChange={setAi} label={tx("触发后 AI 自动初查", "AI triage on fire")} />
                        <span style={{ fontSize: "var(--fs-sm)" }}>{tx("触发后 AI 自动初查", "AI auto-triage when firing")}</span>
                        <Muted size={11.5}>{tx("模板：{t}", "Template: {t}", { t: r.ai_triage.template })}</Muted>
                      </Row>
                      <Row gap={8}>
                        <Toggle on={false} label={tx("同时抑制下游超时告警", "Inhibit downstream timeouts")} />
                        <span style={{ fontSize: "var(--fs-sm)" }}>{tx("同时抑制下游超时告警", "Also inhibit downstream timeouts")}</span>
                        <Muted size={11.5}>{tx("由抑制规则统一管理", "Managed by inhibition rules")}</Muted>
                      </Row>
                    </Step>
                  </div>
                </>
              )}
              {/* Evaluation history and the notification preview are sample content: no endpoint reports them yet. */}
              {tab === "history" && (
                <SampleOnly what={tx("评估历史", "Evaluation history")}>
                <Card style={{ padding: 16 }}>
                  <Col gap={8}>
                    {["14:31:00", "14:30:30", "14:30:00", "14:29:30", "14:05:00", "14:04:30"].map((t, i) => (
                      <Row key={t} gap={10} style={{ fontSize: "var(--fs-sm)" }}>
                        <span className="mono muted">{t}</span>
                        <State tone={i < 4 ? "danger" : i === 4 ? "danger" : "warn"}>{i < 5 ? tx("触发中", "Firing") : tx("待定", "Pending")}</State>
                        <span className="tnum">{[8.4, 8.3, 8.6, 8.2, 2.1, 1.2][i]}%</span>
                        <Muted>{i === 4 ? tx("满足持续 5 分钟 → 触发，已发送通知", "Held 5 min → fired, notified") : i === 5 ? tx("首次超过阈值", "First above threshold") : tx("评估 42 ms", "Evaluated in 42 ms")}</Muted>
                      </Row>
                    ))}
                  </Col>
                </Card>
                </SampleOnly>
              )}
              {tab === "preview" && (
                <SampleOnly what={tx("通知预览", "Notification preview")}>
                <Card style={{ padding: 16, maxWidth: 520 }}>
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
                </Card>
                </SampleOnly>
              )}
            </div>
          )}
        </div>
      </div>
    </Page>
  );
}
