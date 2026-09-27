import { useMemo, useState } from "react";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { AlertRule, AlertRuleSummary } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Code, Col, Dot, Env, Icon, Input, LabelChip, Legend, Muted, Pills, Radio, Row, SearchBox, Sev, Skeleton, Spacer, State, Tabs, TextLink, Toggle, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import "./events.css";

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
  const [sel, setSel] = useState("r-5xx");
  const [tab, setTab] = useState<"def" | "history" | "preview">("def");
  const list = useGet<{ items: AlertRuleSummary[]; counts: Record<string, number> }>("/alert-rules");
  const { data: r } = useGet<AlertRule>(`/alert-rules/${sel}`);
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
      crumbs={[tx("事件与告警", "Events"), tx("告警规则", "Alert rules")]}
      actions={
        <>
          <Btn kind="ghost" icon="download" to="/alert-rules/new?tab=import">
            {tx("导入 Prometheus 规则", "Import Prometheus rules")}
          </Btn>
          <Btn kind="primary" icon="plus" to="/alert-rules/new">
            {tx("新建规则", "New rule")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="side" style={{ width: 320, padding: 12, display: "flex", flexDirection: "column", gap: 10 }}>
          <SearchBox placeholder={tx("搜索规则", "Search rules")} value={q} onChange={setQ} width="100%" />
          <Pills value={filter} onChange={setFilter} options={[{ value: "all", label: tx("全部", "All"), count: list.data?.counts.all }, { value: "firing", label: tx("触发中", "Firing"), count: list.data?.counts.firing }, { value: "scheduled", label: tx("周期规则", "Scheduled"), count: list.data?.counts.scheduled }]} />
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
          {!r ? (
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
                      <Chip caret>
                        <Brand name="prometheus" size={18} />
                        {r.datasource}
                      </Chip>
                      <Chip caret>{tx("已登记查询：{q}", "Registered query: {q}", { q: r.query_ref })}</Chip>
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
                        <Chip caret>{tx("大于", "Above")}</Chip>
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
                        <Chip caret>{r.evaluation.no_data}</Chip>
                      </Row>
                    </Step>
                  </div>
                  <div className="grid-2">
                    <Step n={4} title={tx("标签与说明", "Labels & annotation")}>
                      <Row gap={6} wrap>
                        {r.labels.map((l) => (
                          <LabelChip key={l.k} k={l.k} v={l.v} />
                        ))}
                        <Chip icon="plus">{tx("标签", "Label")}</Chip>
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
              {tab === "history" && (
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
              )}
              {tab === "preview" && (
                <Card style={{ padding: 16, maxWidth: 520 }}>
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
                </Card>
              )}
            </div>
          )}
        </div>
      </div>
    </Page>
  );
}
