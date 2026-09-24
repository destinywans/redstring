import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { GuardEvent, GuardrailsView } from "../../api/types";
import { Badge, Btn, Card, Chip, Code, Col, Icon, Legend, Mono, Muted, Pills, Row, SearchBox, Section, Skeleton, Spacer, State, Strong, TextLink, Tile, Toggle, cx, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { StackedBars } from "../../charts";
import { useI18n } from "../../i18n";
import "./agents.css";

export default function GuardrailsPage() {
  const { tx } = useI18n();
  const [filter, setFilter] = useState<"all" | "blocked" | "masked">("all");
  const [q, setQ] = useState("");
  const { data } = useGet<GuardrailsView>("/guardrails", { filter });
  const [off, setOff] = useState<Set<string>>(new Set());
  const [sel, setSel] = useState("g1");
  const patch = useSend<{ id: string; enabled: boolean }>("PATCH", (b) => `/guardrails/${b.id}`);
  const act = (a: GuardEvent["action"]) => (a === "blocked" ? ([tx("已拦截", "Blocked"), "danger"] as const) : ([tx("已脱敏", "Masked"), "warn"] as const));
  const d = data?.detail;
  const rows = data?.events.filter((e) => !q || e.detail.includes(q) || e.run.includes(q));
  return (
    <Page flush crumbs={[tx("Agent 运行台", "Agent console"), tx("护栏与审计", "Guardrails & audit")]} actions={<Btn icon="download">{tx("导出审计报告", "Export audit report")}</Btn>}>
      <div className="v3-split">
        <div className="side" style={{ width: 360, padding: 14, display: "flex", flexDirection: "column", gap: 8 }}>
          <Section title={tx("护栏策略", "Guardrail policies")} right={<TextLink icon="plus">{tx("新建", "New")}</TextLink>}>
            <ul className="pol-list stagger">
              {data?.policies.map((p) => {
                const on = !off.has(p.id);
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
                        onChange={(v) => {
                          setOff((s) => {
                            const n = new Set(s);
                            if (v) n.delete(p.id);
                            else n.add(p.id);
                            return n;
                          });
                          patch.mutate({ id: p.id, enabled: v });
                          toast(v ? tx("已开启「{p}」", "“{p}” enabled", { p: p.name }) : tx("已关闭「{p}」，此操作已写入审计", "“{p}” disabled — recorded in the audit log", { p: p.name }), { undo: () => setOff((s) => { const n = new Set(s); if (v) n.add(p.id); else n.delete(p.id); return n; }) });
                        }}
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
              {data ? <StackedBars data={data.trend} colors={["var(--c3)", "var(--c4)"]} height={46} gap={6} names={[tx("拦截", "Blocked"), tx("脱敏", "Masked")]} labels={["9/17", "9/18", "9/19", "9/20", "9/21", "9/22", tx("今天", "Today")]} /> : <Skeleton h={46} />}
              <Row gap={0}>
                <Muted size={10.5}>9/17</Muted>
                <Spacer />
                <Muted size={10.5}>9/20</Muted>
                <Spacer />
                <Muted size={10.5}>{tx("今天", "Today")}</Muted>
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
              <Chip caret>Agent</Chip>
              <Chip caret>{tx("规则", "Rule")}</Chip>
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
                  { key: "time", header: tx("时间", "Time"), render: (e) => <Muted>{e.time}</Muted> },
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
                  { key: "rule", header: tx("规则", "Rule"), width: "17%", render: (e) => <span style={{ fontSize: 12 }}>{e.rule}</span> },
                  { key: "action", header: tx("处理", "Action"), render: (e) => <State tone={act(e.action)[1]}>{act(e.action)[0]}</State> },
                  { key: "detail", header: tx("详情", "Detail"), width: "39%", render: (e) => <span className="ellipsis muted" style={{ fontSize: 12 }}>{e.detail}</span> },
                ]}
              />
            </Card>
            {d && sel === d.id && (
              <Card className="anim-rise" style={{ padding: "14px 16px" }}>
                <Col gap={10}>
                  <Row gap={8}>
                    <Icon name="shield" size={16} color="var(--danger)" />
                    <b style={{ fontSize: 13.5 }}>{d.title}</b>
                    <State tone="danger">{tx("已拦截", "Blocked")}</State>
                    <Spacer />
                    <Muted size={11.5}>{d.meta}</Muted>
                  </Row>
                  <Muted>{d.source}</Muted>
                  <Code>
                    {d.snippet}
                    <span className="masked">{d.masked}</span>
                  </Code>
                  <Row gap={8} align="flex-start">
                    <Icon name="check" size={14} color="var(--ok)" style={{ marginTop: 2 }} />
                    <span style={{ fontSize: 12.5 }}>{d.verdict}</span>
                  </Row>
                  <Row gap={6}>
                    <Btn h={26} icon="play" to="/agents/runs/run_7f3a">
                      {tx("查看运行", "View run")}
                    </Btn>
                    <Btn h={26} icon="plus" to="/agents/evals">
                      {tx("加入评测用例（越权与注入）", "Add as eval case (injection)")}
                    </Btn>
                    <Btn h={26} kind="ghost" onClick={() => toast(tx("已标记为误报，规则会复核", "Marked as false positive for rule review"))}>
                      {tx("误报", "False positive")}
                    </Btn>
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
