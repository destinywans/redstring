import { useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import type { AlertRule, AlertRuleLifecycle, AlertRuleList, AlertRulePending, AlertRuleSummary, Tone } from "../../api/types";
import { Unavailable, UnknownValue } from "../../ui/unknown";
import { useWhen } from "../../ui/when";
import { Badge, Brand, Btn, Card, Chip, Code, Col, Dot, Env, Icon, Input, KV, LabelChip, Legend, Muted, Pills, Radio, Row, SearchBox, Sev, Skeleton, Spacer, State, Tabs, TextLink, Toggle, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import "./events.css";
import { ChipSelect, AddChip, SampleOnly } from "../../ui/demo";
import { useFirstSelection } from "../../ui/selection";
import { DisableRuleDialog } from "./DisableRuleDialog";

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
  // ?q= (e.g. from an execution page) searches and selects the first matching rule.
  const [params] = useSearchParams();
  const [q, setQ] = useState(() => params.get("q") ?? "");
  const [sel, setSel] = useState("");
  const [disabling, setDisabling] = useState(false);
  const [tab, setTab] = useState<"def" | "history" | "preview">("def");
  const demo = API_MODE === "mock";
  const when = useWhen();
  const list = useGet<AlertRuleList>("/alert-rules");
  // items null = no rule source configured; [] with empty_reason = nothing visible to you; [] alone = no rules yet.
  const noSource = !!list.data && list.data.items === null;
  const nothing = !!list.data && list.data.items?.length === 0;
  const preferred = useMemo(() => {
    const items = list.data?.items ?? undefined;
    const want = params.get("q")?.toLowerCase();
    const hit = want ? items?.find((x) => x.name.toLowerCase().includes(want)) : undefined;
    return hit && items ? [hit, ...items.filter((x) => x !== hit)] : items;
  }, [list.data, params]);
  useFirstSelection(preferred, (x) => x.id, sel, setSel);
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
  const nav = useNavigate();
  // D11 §11.6: the lifecycle (where the rule is in enable/disable) is told apart from the trigger state (the dot).
  const LIFE: Record<AlertRuleLifecycle["state"], [Tone, string]> = {
    active: ["ok", tx("已生效", "In effect")],
    disable_pending_approval: ["warn", tx("停用待批准", "Disable pending")],
    disabling: ["accent", tx("停用中", "Disabling")],
    needs_attention: ["warn", tx("需要处理", "Needs attention")],
  };
  const pendingLabel = (x: AlertRulePending): [Tone, string] => {
    const enable = x.operation === "enable";
    const map: Record<AlertRulePending["state"], [Tone, string]> = {
      pending_approval: enable ? ["accent", tx("启用待批准", "Enable pending")] : ["warn", tx("停用待批准", "Disable pending")],
      executing: ["accent", enable ? tx("启用中", "Enabling") : tx("停用中", "Disabling")],
      needs_attention: ["warn", tx("需要处理", "Needs attention")],
      failed: ["danger", tx("失败", "Failed")],
      rejected: ["neutral", tx("已驳回", "Rejected")],
    };
    return map[x.state];
  };
  const pending = list.data?.pending ?? [];
  const life = list.data?.items?.find((x) => x.id === sel)?.lifecycle;
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
          {(pending.length > 0 || list.data?.items?.some((x) => x.lifecycle && x.lifecycle.state !== "active")) && (
            <Muted size={11}>{tx("圆点是触发状态；右侧标签是生命周期，点开看提案或执行。", "The dot is the trigger state; the tag on the right is the lifecycle — open it for the proposal or execution.")}</Muted>
          )}
          {pending.length > 0 && (
            <div>
              <div className="rule-folder">
                <Icon name="clock" size={13} />
                <span className="grow">{tx("待生效的启用和停用", "Enables and disables not in effect yet")}</span>
                <Muted size={11}>{pending.length}</Muted>
              </div>
              {pending.map((x) => {
                const [tone, label] = pendingLabel(x);
                return (
                  <button key={x.proposal_id} type="button" className="rule-item" onClick={() => nav(x.href)} title={x.group}>
                    <Icon name="merge" size={12} color="var(--faint)" />
                    <span className="ellipsis grow mono" style={{ fontSize: "var(--fs-sub)" }}>
                      {x.rule_name}
                    </span>
                    <Badge tone={tone}>{label}</Badge>
                  </button>
                );
              })}
            </div>
          )}
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
                  {x.lifecycle && x.lifecycle.state !== "active" && <Badge tone={LIFE[x.lifecycle.state][0]}>{LIFE[x.lifecycle.state][1]}</Badge>}
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
                <Muted size={11.5}>
                  {r.last_eval ? tx("最近评估 {t}", "Last evaluated {t}", { t: when.clock(r.last_eval) ?? "—" }) : tx("最近评估", "Last evaluated")}
                  {r.evaluation_ms != null && tx(" · 用时 {n} ms", " · {n} ms", { n: Math.round(r.evaluation_ms) })}
                </Muted>
                {!r.last_eval && <UnknownValue />}
                <Spacer />
                {/* D11 decision 7: any rule in effect can be proposed for disabling; the server says when its file isn't executor-merged. */}
                {(!life || life.state === "active") && (
                  <Gate need={["alert_rules", "change"]}>
                    <Btn kind="ghost" icon="x" onClick={() => setDisabling(true)}>
                      {tx("提议停用", "Propose disabling")}
                    </Btn>
                  </Gate>
                )}
                <Btn
                  icon="play"
                  onClick={async () => {
                    try {
                      // The real dry run replays the rule's own expression with its `for` over the last 6 h.
                      const body = demo && !r.partial ? { threshold, for_minutes: forMin } : { expr: r.expr, for: `${Math.round(r.for_seconds ?? 0)}s` };
                      const res = await api<{ would_fire: number; windows: string[] }>("POST", `/alert-rules/${r.id}/dry-run`, { body });
                      // Real windows are "start/end" timestamps.
                      const w = res.windows.map((x) => (x.includes("/") ? x.split("/").map((t) => when.clock(t) ?? t).join("–") : x));
                      setDry(res.would_fire ? tx("最近 6 小时会触发 {n} 次：{w}", "Would have fired {n}× in the last 6 h: {w}", { n: res.would_fire, w: w.join(tx("、", ", ")) }) : tx("最近 6 小时不会触发。", "Wouldn't have fired in the last 6 h."));
                    } catch (e) {
                      // The server's reasons are English; the known ones are said in the page's language (PA-152).
                      const known: Record<string, string> = {
                        rule_interval_unavailable: tx("这条规则的评估间隔不在 15 秒到 5 分钟之间，不能试运行。", "This rule's evaluation interval isn't between 15 s and 5 min, so it can't be dry-run."),
                        dry_run_unavailable: tx("还没有接入可查询历史的 Prometheus。", "No Prometheus with history is connected."),
                      };
                      toast(e instanceof ApiError ? known[e.code] ?? e.message : tx("试运行失败，请稍后重试", "The dry run failed; try again later"), { tone: "danger" });
                    }
                  }}
                >
                  {tx("试运行（最近 6 小时）", "Dry run (last 6 h)")}
                </Btn>
                {/* Saving edits needs the versioned draft body (PUT /alert-rules/{id}); this view has no editor fields in real mode. */}
                {demo && !r.partial && (
                  <Btn kind="primary" icon="check" onClick={() => { void api("PUT", `/alert-rules/${r.id}`, { body: { threshold, for_minutes: forMin, mode, ai } }); toast(tx("规则已保存，下一次评估生效", "Saved — applies at the next evaluation")); }}>
                    {tx("保存", "Save")}
                  </Btn>
                )}
              </Row>
              <DisableRuleDialog open={disabling} onClose={() => setDisabling(false)} ruleId={r.id} ruleName={r.name} app={r.labels.find((l) => l.k === "application")?.v} />
              {life && (
                <Row gap={6} style={{ marginTop: -8 }}>
                  <Muted size={11.5}>{tx("生命周期", "Lifecycle")}</Muted>
                  <Badge tone={LIFE[life.state][0]}>{LIFE[life.state][1]}</Badge>
                  {(life.execution_id || life.proposal_id) && (
                    <TextLink to={life.execution_id ? `/executions/${life.execution_id}` : `/review/${life.proposal_id}`}>
                      <span className="mono">{life.execution_id ?? life.proposal_id}</span>
                    </TextLink>
                  )}
                </Row>
              )}
              {dry && (
                <div className="dry-run anim-rise">
                  <Icon name="play" size={14} />
                  {dry}
                </div>
              )}
              {/* A rule read from its Prometheus file (real, or a sample rule-repository rule) is partial and read-only. */}
              {tab === "def" && (!demo || r.partial) && <RealRuleDefinition r={r} />}
              {tab === "def" && demo && !r.partial && (
                <>
                  <Step n={1} title={tx("查询", "Query")}>
                    <Row gap={8} wrap>
                      <ChipSelect label="" showLabel={false} icon="database" defaultValue={r.datasource} options={[r.datasource, "VictoriaMetrics · prod", "Prometheus · staging"]} />
                      <ChipSelect label={tx("已登记查询", "Registered query")} defaultValue={r.query_ref ?? ""} options={[r.query_ref ?? "", "http_5xx_ratio", "http_p99_latency", "rpc_client_retry_ratio"].filter((v, i, a) => a.indexOf(v) === i)} />
                      <Muted size={11.5}>{tx("或编写 PromQL（需要规则编辑权限）", "or write PromQL (requires rule editor)")}</Muted>
                    </Row>
                    <Code>{r.expr}</Code>
                    <Card style={{ padding: "10px 12px" }}>
                      <Row gap={8}>
                        <Muted>{tx("预览 · 最近 2 小时", "Preview · last 2 h")}</Muted>
                        <Spacer />
                        <Legend items={[[tx("5xx 比例", "5xx ratio"), "var(--c3)"], [tx("阈值 {v}%", "Threshold {v}%", { v: threshold }), "var(--danger)"]]} />
                      </Row>
                      <Lines series={[{ name: "5xx", values: r.preview?.values ?? [], color: "var(--c3)", fill: "var(--c3s)" }]} height={80} max={10} threshold={threshold} grid={2} format={(v) => `${v}%`} />
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
                        <Input type="number" defaultValue={r.condition?.recover_minutes} width={90} right={<Muted>{tx("分钟", "min")}</Muted>} aria-label={tx("恢复时间", "Recovery")} />
                      </Row>
                    </Step>
                    <Step n={3} title={tx("评估方式", "Evaluation")}>
                      <Radio name="eval" checked={mode === "continuous"} onChange={() => setMode("continuous")} label={tx("持续评估 · 每 30 秒", "Continuous · every 30 s")} />
                      <Radio name="eval" checked={mode === "cron"} onChange={() => setMode("cron")} label={tx("按计划评估（Cron）", "Scheduled (cron)")} sub={tx("适合证书到期、容量预测这类周期检查", "For certificate expiry, capacity forecasts and similar")} />
                      <Row gap={8}>
                        <Input mono defaultValue="0 9 * * *" width={140} disabled={mode !== "cron"} aria-label="cron" />
                        <Muted>{tx("无数据时", "No data")}</Muted>
                        <ChipSelect label="" showLabel={false} defaultValue={r.evaluation?.no_data ?? ""} options={[r.evaluation?.no_data ?? "", tx("保持上次状态", "Keep last state"), tx("视为正常", "Treat as OK"), tx("视为告警", "Treat as firing")].filter((v, i, a) => a.indexOf(v) === i)} />
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
                      <Input mono defaultValue={r.annotation ?? ""} aria-label={tx("说明模板", "Annotation")} />
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
                        <Muted size={11.5}>{tx("模板：{t}", "Template: {t}", { t: r.ai_triage?.template ?? "" })}</Muted>
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

/**
 * A real rule's definition (GET /alert-rules/{id}), read-only: name, expression, `for`, labels and annotation come
 * from the bound Prometheus file; the threshold preview, structured condition, evaluation settings, runbook, route
 * and AI triage have no trusted source and are null, so they show as unknown — never as sample values.
 */
function RealRuleDefinition({ r }: { r: AlertRule }) {
  const { tx } = useI18n();
  const dur = (s: number | null | undefined) => (s == null ? null : s % 60 === 0 ? tx("{n} 分钟", "{n} min", { n: s / 60 }) : tx("{n} 秒", "{n} s", { n: Math.round(s) }));
  const kw = 76;
  return (
    <>
      <Step n={1} title={tx("查询", "Query")}>
        <Row gap={12} wrap>
          <Badge>
            <Icon name="database" size={11} />
            {r.datasource}
          </Badge>
          <Row gap={6}>
            <Muted>{tx("已登记查询", "Registered query")}</Muted>
            {r.query_ref ? <span className="mono">{r.query_ref}</span> : <UnknownValue />}
          </Row>
        </Row>
        <Code>{r.expr}</Code>
        {r.preview ? (
          <Card style={{ padding: "10px 12px" }}>
            <Muted>{tx("预览 · 最近 2 小时", "Preview · last 2 h")}</Muted>
            <Lines series={[{ name: r.name, values: r.preview.values, color: "var(--c3)", fill: "var(--c3s)" }]} height={80} threshold={r.preview.threshold} grid={2} />
          </Card>
        ) : (
          <Unavailable icon="activity" title={tx("没有阈值预览", "No threshold preview")} desc={tx("这条规则没有可信的阈值来源，不画预览；触发条件以上面的表达式为准。", "This rule has no trusted threshold source, so no preview is drawn; the expression above is the condition.")} />
        )}
      </Step>
      <div className="grid-2">
        <Step n={2} title={tx("触发条件", "Condition")}>
          <Col gap={8}>
            <KV k={tx("持续", "For")} kw={kw}>
              {dur(r.for_seconds) ?? <UnknownValue />}
            </KV>
            <KV k={tx("阈值", "Threshold")} kw={kw}>
              {r.condition ? `${r.condition.op} ${r.condition.value}${r.condition.unit}` : <UnknownValue />}
            </KV>
            <KV k={tx("恢复", "Recovery")} kw={kw}>
              {r.condition ? dur(r.condition.recover_minutes * 60) : <UnknownValue />}
            </KV>
          </Col>
        </Step>
        <Step n={3} title={tx("评估方式", "Evaluation")}>
          <Col gap={8}>
            <KV k={tx("评估间隔", "Interval")} kw={kw}>
              {r.evaluation_interval_seconds != null ? tx("每 {n}", "Every {n}", { n: dur(r.evaluation_interval_seconds) ?? "" }) : <UnknownValue />}
            </KV>
            <KV k={tx("无数据时", "No data")} kw={kw}>
              {r.evaluation?.no_data ?? <UnknownValue />}
            </KV>
          </Col>
        </Step>
      </div>
      <div className="grid-2">
        <Step n={4} title={tx("标签与说明", "Labels & annotation")}>
          <Row gap={6} wrap>
            {r.labels.map((l) => (
              <LabelChip key={l.k} k={l.k} v={l.v} />
            ))}
          </Row>
          <KV k={tx("说明", "Summary")} kw={kw}>
            {r.annotation ?? <UnknownValue />}
          </KV>
          <KV k="Runbook" kw={kw}>
            {r.runbook ? (
              <Badge>
                <Icon name="book" size={11} />
                {r.runbook}
              </Badge>
            ) : (
              <UnknownValue />
            )}
          </KV>
        </Step>
        <Step n={5} title={tx("通知与 AI", "Notification & AI")}>
          <KV k={tx("匹配路由", "Route")} kw={kw}>
            {r.route_path ? (
              <Row gap={6} wrap>
                {r.route_path.map((x, i) => (
                  <Row key={i} gap={6}>
                    {i > 0 && <Icon name="right" size={12} color="var(--faint)" />}
                    <Badge>{x}</Badge>
                  </Row>
                ))}
              </Row>
            ) : (
              <UnknownValue />
            )}
          </KV>
          <KV k={tx("AI 初查", "AI triage")} kw={kw}>
            {r.ai_triage ? (r.ai_triage.enabled ? tx("触发后自动初查 · 模板：{t}", "On fire · template: {t}", { t: r.ai_triage.template }) : tx("关闭", "Off")) : <UnknownValue />}
          </KV>
        </Step>
      </div>
    </>
  );
}
