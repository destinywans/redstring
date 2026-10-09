import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet, useSend, API_MODE } from "../../api/client";
import type { GuardEvent, GuardrailsView } from "../../api/types";
import { Badge, Btn, Card, Chip, Code, Col, Icon, Legend, Mono, Muted, Pills, Row, SearchBox, Section, Skeleton, Spacer, State, Strong, TextLink, Tile, Toggle, cx, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { StackedBars } from "../../charts";
import { useI18n } from "../../i18n";
import "./agents.css";
import { ChipSelect, useFacet, downloadFile, openForm } from "../../ui/demo";
import { Unavailable } from "../../ui/unknown";

const localTime = (t: string) => (/^\d{4}-\d{2}-\d{2}T/.test(t) ? new Date(t).toLocaleString() : t);

export default function GuardrailsPage() {
  const { tx } = useI18n();
  const [filter, setFilter] = useState<"all" | "blocked" | "masked">("all");
  const [q, setQ] = useState("");
  const { data, refetch } = useGet<GuardrailsView>("/guardrails", { filter });
  const [off, setOff] = useState<Set<string>>(new Set());
  // Real switches show the requested state while the PATCH is in flight, then the server's.
  const [pending, setPending] = useState<Record<string, boolean>>({});
  const demo = API_MODE === "mock";
  const [selPick, setSel] = useState(demo ? "g1" : "");
  const patch = useSend<{ id: string; enabled: boolean }>("PATCH", (b) => `/guardrails/${b.id}`);
  const act = (a: GuardEvent["action"]) => (a === "blocked" ? ([tx("已拦截", "Blocked"), "danger"] as const) : a === "masked" ? ([tx("已脱敏", "Masked"), "warn"] as const) : ([a, "neutral"] as const));
  // Real detail only describes the newest event; any selected event shows what its row recorded.
  const sel = selPick || (demo ? "" : data?.events[0]?.id ?? "");
  const selEvent = data?.events.find((e) => e.id === sel);
  const d = demo ? data?.detail : selEvent && { id: selEvent.id, title: `${act(selEvent.action)[0]} · ${selEvent.target_kind ?? selEvent.rule}`, meta: `${selEvent.agent} · ${localTime(selEvent.time)}`, source: null, snippet: null, masked: null, verdict: null, run: selEvent.run };
  const trendLabels = !demo && data?.trend_days?.length ? data.trend_days.map((x) => `${Number(x.slice(5, 7))}/${Number(x.slice(8, 10))}`) : ["9/17", "9/18", "9/19", "9/20", "9/21", "9/22", tx("今天", "Today")];
  const toggle = (id: string, name: string, v: boolean) => {
    const done = v ? tx("已开启「{p}」", "“{p}” enabled", { p: name }) : tx("已关闭「{p}」，此操作已写入审计", "“{p}” disabled — recorded in the audit log", { p: name });
    if (demo) {
      const flip = (enabled: boolean) =>
        setOff((s) => {
          const n = new Set(s);
          if (enabled) n.delete(id);
          else n.add(id);
          return n;
        });
      flip(v);
      patch.mutate({ id, enabled: v });
      return toast(done, { undo: () => flip(!v) });
    }
    // Real toggles report the server's answer; a refusal (forced policy, permission) puts the switch back.
    const clear = () =>
      setPending((m) => {
        const n = { ...m };
        delete n[id];
        return n;
      });
    setPending((m) => ({ ...m, [id]: v }));
    patch.mutate(
      { id, enabled: v },
      {
        onSuccess: () => {
          void refetch().finally(clear);
          toast(done);
        },
        onError: (e) => {
          clear();
          toast(e.message, { tone: "danger" });
        },
      },
    );
  };
  const fAgent = useFacet(data?.events, (e) => e.agent);
  const fRule = useFacet(data?.events, (e) => e.rule);
  const rows = data?.events.filter((e) => (!q || e.detail.includes(q) || e.run.includes(q)) && fAgent.test(e) && fRule.test(e));
  return (
    <Page flush crumbs={[tx("Agent 运行台", "Agent console"), tx("护栏与审计", "Guardrails & audit")]} actions={
        <Btn
          icon="download"
          onClick={() => {
            downloadFile(`guardrails-report-${new Date().toISOString().slice(0, 10)}.json`, JSON.stringify({ generated_at: new Date().toISOString(), sample_data: API_MODE === "mock", policies: data?.policies, events: data?.events }, null, 2));
            toast(tx("审计报告已导出", "Audit report exported"));
          }}
        >
          {tx("导出审计报告", "Export audit report")}
        </Btn>
      }>
      <div className="v3-split">
        <div className="side" style={{ width: 360, padding: 14, display: "flex", flexDirection: "column", gap: 8 }}>
          <Section title={tx("护栏策略", "Guardrail policies")} right={
            // Real registration only accepts the built-in rule names (POST /guardrails); the sample form takes free text.
            demo && <TextLink
              icon="plus"
              onClick={async () => {
                const v = await openForm({
                  title: tx("新建护栏策略", "New guardrail policy"),
                  fields: [
                    { key: "name", label: tx("名称", "Name"), required: true },
                    { key: "stage", label: tx("检查位置", "Checks"), type: "select", options: [{ value: "input", label: tx("模型输入", "Model input") }, { value: "tool", label: tx("工具输出", "Tool output") }, { value: "answer", label: tx("最终回答", "Final answer") }] },
                    { key: "action", label: tx("命中后", "On match"), type: "select", options: [{ value: "block", label: tx("拦截", "Block") }, { value: "mask", label: tx("脱敏", "Mask") }, { value: "flag", label: tx("仅记录", "Log only") }] },
                    { key: "rule", label: tx("规则", "Rule"), type: "textarea", placeholder: tx("例如：回答中出现内网 IP 时脱敏", "e.g. mask internal IPs in answers") },
                  ],
                  submit: tx("创建（先以观察模式运行）", "Create (observe mode first)"),
                });
                if (v) toast(tx("策略「{n}」已创建，观察 24 小时后再决定是否强制", "Policy “{n}” created in observe mode for 24 h", { n: v.name }));
              }}
            >
              {tx("新建", "New")}
            </TextLink>
          }>
            <ul className="pol-list stagger">
              {data?.policies.map((p) => {
                const on = demo ? !off.has(p.id) : pending[p.id] ?? p.enabled;
                return (
                  <li key={p.id} className={cx(p.id === "injection" && "on", !on && "off")}>
                    <Tile icon={p.icon as IconName} size={28} />
                    <Col gap={2} grow style={{ minWidth: 0 }}>
                      <Row gap={6}>
                        <Strong>{p.name}</Strong>
                        {p.forced && <Badge tone="accent">{tx("强制", "Enforced")}</Badge>}
                      </Row>
                      <Muted size={11.5}>{p.desc}</Muted>
                      <Muted size={11.5}>{p.stat}</Muted>
                    </Col>
                    {p.forced ? (
                      <Badge>{tx("不可关闭", "Always on")}</Badge>
                    ) : (
                      <Toggle
                        on={on}
                        label={p.name}
                        onChange={(v) => toggle(p.id, p.name, v)}
                      />
                    )}
                  </li>
                );
              })}
            </ul>
          </Section>
          <Card style={{ padding: "10px 12px" }}>
            <Col gap={6}>
              <Row gap={6}>
                <Strong>{tx("近 7 天触发", "Triggers · 7 days")}</Strong>
                <Spacer />
                <Legend items={[[tx("拦截", "Blocked"), "var(--c3)"], [tx("脱敏", "Masked"), "var(--c4)"]]} />
              </Row>
              {data ? <StackedBars data={data.trend} colors={["var(--c3)", "var(--c4)"]} height={46} gap={6} names={[tx("拦截", "Blocked"), tx("脱敏", "Masked")]} labels={trendLabels} /> : <Skeleton h={46} />}
              <Row gap={0}>
                <Muted size={10.5}>{trendLabels[0]}</Muted>
                <Spacer />
                <Muted size={10.5}>{trendLabels[Math.floor(trendLabels.length / 2)]}</Muted>
                <Spacer />
                <Muted size={10.5}>{demo ? tx("今天", "Today") : trendLabels[trendLabels.length - 1]}</Muted>
              </Row>
            </Col>
          </Card>
        </div>
        <div className="main">
          <div className="v3-content" style={{ padding: "14px 20px" }}>
            <Row gap={8}>
              <Pills
                value={filter}
                onChange={setFilter}
                options={[
                  { value: "all", label: tx("全部", "All"), count: data?.counts.all },
                  { value: "blocked", label: tx("已拦截", "Blocked"), count: data?.counts.blocked },
                  { value: "masked", label: tx("已脱敏", "Masked"), count: data?.counts.masked },
                ]}
              />
              <SearchBox placeholder={tx("搜索事件", "Search events")} value={q} onChange={setQ} width={220} />
              <ChipSelect label="Agent" {...fAgent.props} />
              <ChipSelect label={tx("规则", "Rule")} {...fRule.props} />
              <Spacer />
              <TextLink to="/settings/audit">{tx("Agent 操作审计", "Agent audit log")}</TextLink>
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable<GuardEvent>
                rows={rows}
                loading={!data}
                rowKey={(e) => e.id}
                rowHeight={40}
                selected={sel}
                onRowClick={(e) => setSel(e.id)}
                columns={[
                  { key: "time", header: tx("时间", "Time"), render: (e) => <Muted>{localTime(e.time)}</Muted> },
                  {
                    key: "agent",
                    header: tx("Agent · 运行", "Agent · run"),
                    width: "24%",
                    render: (e) => (
                      <Row gap={6}>
                        <span className="ellipsis" style={{ fontWeight: 500 }}>{e.agent}</span>
                        <Mono color="var(--muted)" size={11}>{e.run}</Mono>
                      </Row>
                    ),
                  },
                  { key: "rule", header: tx("规则", "Rule"), width: "17%", render: (e) => <span style={{ fontSize: "var(--fs-sub)" }}>{e.rule}</span> },
                  { key: "action", header: tx("处理", "Action"), render: (e) => <State tone={act(e.action)[1]}>{act(e.action)[0]}</State> },
                  { key: "detail", header: tx("详情", "Detail"), width: "39%", render: (e) => <span className="ellipsis muted" style={{ fontSize: "var(--fs-sub)" }}>{e.detail}</span> },
                ]}
              />
            </Card>
            {d && sel === d.id && (
              <Card className="anim-rise" style={{ padding: "14px 16px" }}>
                <Col gap={10}>
                  <Row gap={8}>
                    <Icon name="shield" size={16} color="var(--danger)" />
                    <b style={{ fontSize: "var(--fs-md)" }}>{d.title}</b>
                    {demo ? <State tone="danger">{tx("已拦截", "Blocked")}</State> : selEvent && <State tone={act(selEvent.action)[1]}>{act(selEvent.action)[0]}</State>}
                    <Spacer />
                    <Muted size={11.5}>{d.meta}</Muted>
                  </Row>
                  {!demo && selEvent && (
                    <Row gap={6}>
                      <Muted>{tx("规则", "Rule")}</Muted>
                      <Mono size={11.5}>{selEvent.rule}</Mono>
                      <Muted>· {tx("原因", "Reason")}</Muted>
                      <Mono size={11.5}>{selEvent.detail}</Mono>
                    </Row>
                  )}
                  {d.source === null && d.snippet === null && d.verdict === null ? (
                    <Unavailable reason="not_generated" icon="file" title={tx("没有保存原文和判定", "Source text and verdict aren't stored")} desc={tx("护栏只记录命中的规则和原因，不保存被拦截的原文、脱敏后的内容和判定说明。", "Guardrails record the rule and reason only — not the blocked text, the masked content or a verdict.")} />
                  ) : (
                    <>
                      <Muted>{d.source}</Muted>
                      <Code>
                        {d.snippet}
                        <span className="masked">{d.masked}</span>
                      </Code>
                      <Row gap={8} align="flex-start">
                        <Icon name="check" size={14} color="var(--ok)" style={{ marginTop: 2 }} />
                        <span style={{ fontSize: "var(--fs-sm)" }}>{d.verdict}</span>
                      </Row>
                    </>
                  )}
                  <Row gap={6}>
                    {(() => {
                      // The run of the selected event; the sample data points at its sample run.
                      const run = demo ? "run_7f3a" : selEvent?.run;
                      return (
                        run && (
                          <Btn h={26} icon="play" to={`/agents/runs/${run}`}>
                            {tx("查看运行", "View run")}
                          </Btn>
                        )
                      );
                    })()}
                    <Btn h={26} icon="plus" to="/agents/evals">
                      {demo ? tx("加入评测用例（越权与注入）", "Add as eval case (injection)") : tx("去评测实验", "Go to evals")}
                    </Btn>
                    {API_MODE === "mock" && (
                      <Btn h={26} kind="ghost" onClick={() => toast(tx("已标记为误报，规则会复核", "Marked as false positive for rule review"))}>
                        {tx("误报", "False positive")}
                      </Btn>
                    )}
                  </Row>
                </Col>
              </Card>
            )}
          </div>
        </div>
      </div>
    </Page>
  );
}
