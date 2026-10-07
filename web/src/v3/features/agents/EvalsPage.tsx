import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { EvalExperiment } from "../../api/types";
import { Badge, Brand, Btn, Card, Col, Icon, Legend, Mono, Muted, Num, Panel, Row, Section, Skeleton, Spacer, Tabs, TextLink, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Radar } from "../../charts";
import { useI18n } from "../../i18n";
import "./agents.css";
import { openForm } from "../../ui/demo";

export default function EvalsPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { data: e } = useGet<EvalExperiment>("/evals/exp-v12-v13");
  const [tab, setTab] = useState("exp");
  const [sel, setSel] = useState("INV-187");
  const rerun = useSend<void, { id: string }>("POST", "/evals");
  const Res = ({ ok }: { ok: boolean }) => <Icon name={ok ? "check" : "x"} size={14} color={ok ? "var(--ok)" : "var(--danger)"} />;
  return (
    <Page flush crumbs={[tx("Agent 运行台", "Agent console"), tx("评测", "Evaluations")]} actions={
        <Btn
          kind="primary"
          icon="play"
          onClick={async () => {
            const v = await openForm({
              title: tx("新建评测实验", "New experiment"),
              fields: [
                { key: "agent", label: "Agent", type: "select", options: [{ value: "investigator", label: tx("调查助手", "Investigator") }, { value: "triage", label: tx("告警初查", "Alert triage") }, { value: "reporter", label: tx("报告撰写", "Reporter") }] },
                { key: "a", label: tx("基线版本", "Baseline"), initial: "v12", mono: true, required: true },
                { key: "b", label: tx("候选版本", "Candidate"), initial: "v13", mono: true, required: true },
                { key: "data", label: tx("数据集", "Dataset"), type: "select", options: (e?.datasets ?? []).map((d) => ({ value: d.name, label: d.name, hint: `${d.count}` })) },
              ],
              submit: tx("开始运行", "Start run"),
            });
            if (v) toast(tx("实验已排队：{a} 对比 {b}，预计 6 分钟完成", "Experiment queued: {a} vs {b}, about 6 minutes", { a: v.a, b: v.b }));
          }}
        >
          {tx("新建实验", "New experiment")}
        </Btn>
      }>
      <Tabs
        value={tab}
        onChange={setTab}
        options={[
          { value: "exp", label: tx("实验", "Experiments"), count: 12 },
          { value: "data", label: tx("数据集", "Datasets"), count: 5 },
          { value: "eval", label: tx("评测器", "Evaluators"), count: 8 },
          { value: "label", label: tx("人工标注", "Human review"), count: 3 },
        ]}
      />
      <div className="v3-split">
        <div className="main">
          {!e ? (
            <div className="v3-content">
              <Skeleton h={300} />
            </div>
          ) : tab !== "exp" ? (
            <div className="v3-content anim-fade" style={{ padding: "14px 20px" }} key={tab}>
              <EvalsTab tab={tab} e={e} onOpen={(id) => nav(`/agents/evals/${id}`)} />
            </div>
          ) : (
            <div className="v3-content anim-fade" style={{ padding: "14px 20px" }} key={tab}>
              <Card style={{ padding: "12px 16px" }}>
                <Row gap={12}>
                  <Tile icon="play" tone="accent" size={30} />
                  <Col gap={1}>
                    <Row gap={8}>
                      <b style={{ fontSize: "var(--fs-lg)" }}>{e.title}</b>
                      <Badge>{e.dataset}</Badge>
                    </Row>
                    <Muted size={11.5}>{e.meta}</Muted>
                  </Col>
                  <Spacer />
                  <Btn icon="edit" to={`/agents/evals/${sel}`}>
                    {tx("人工标注 3 条", "Review 3 cases")}
                  </Btn>
                  <Btn kind="ghost" icon="rotate" loading={rerun.isPending} onClick={() => rerun.mutate(undefined, { onSuccess: () => toast(tx("实验已排队，预计 26 分钟", "Experiment queued, ~26 min")) })}>
                    {tx("重新运行", "Re-run")}
                  </Btn>
                </Row>
              </Card>
              <Row gap={14} align="stretch">
                <Panel title={tx("指标对比", "Metrics")} right={e.gate_failed && <Badge tone="warn"><Icon name="alert" size={11} />{e.gate_failed}</Badge>} style={{ flex: "1 1 0", minWidth: 0 }}>
                  <table className="v3-table">
                    <thead>
                      <tr>
                        <th style={{ width: "34%" }}>{tx("指标", "Metric")}</th>
                        <th>v12</th>
                        <th>v13</th>
                        <th>{tx("变化", "Change")}</th>
                        <th>{tx("门禁", "Gate")}</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody className="stagger">
                      {e.metrics.map((m) => (
                        <tr key={m.name} style={{ height: 34 }}>
                          <td style={{ fontWeight: 500 }}>{m.name}</td>
                          <td>
                            <Num color="var(--muted)">{m.a}</Num>
                          </td>
                          <td>
                            <Num>{m.b}</Num>
                          </td>
                          <td>
                            <Num color={m.delta.startsWith("+0") ? "var(--ok)" : m.delta.startsWith("+1") ? "var(--warn)" : "var(--muted)"}>{m.delta}</Num>
                          </td>
                          <td>
                            <Muted>{m.gate}</Muted>
                          </td>
                          <td>{m.pass !== null && <Res ok={m.pass} />}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </Panel>
                <Panel title={tx("评测维度 · v12 vs v13", "Dimensions · v12 vs v13")} style={{ width: 280, flex: "none" }}>
                  <Col gap={4} align="center">
                    <Radar axes={e.radar.axes} size={204} series={[{ values: e.radar.a, color: "var(--c5)", name: "v12" }, { values: e.radar.b, color: "var(--c1)", fill: "var(--c1s)", name: "v13" }]} />
                    <Legend items={[["v12", "var(--c5)"], ["v13", "var(--c1)"]]} />
                    <Muted size={11}>{tx("越靠外越好；速度与成本已归一化", "Outer is better; speed and cost normalised")}</Muted>
                  </Col>
                </Panel>
              </Row>
              <Panel title={tx("用例结果 · 48 例（显示差异项）", "Case results · 48 (showing differences)")} right={<TextLink onClick={() => setTab("label")}>{tx("全部用例", "All cases")}</TextLink>}>
                <table className="v3-table">
                  <thead>
                    <tr>
                      <th style={{ width: "34%" }}>{tx("用例", "Case")}</th>
                      <th>{tx("期望", "Expected")}</th>
                      <th>v12</th>
                      <th>v13</th>
                      <th>{tx("评分", "Score")}</th>
                      <th>{tx("耗时", "Duration")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {e.cases.map((c) => (
                      <tr key={c.id} className={cx("clickable", sel === c.id && "selected")} style={{ height: 36 }} onClick={() => setSel(c.id)} onDoubleClick={() => nav(`/agents/evals/${c.id}`)}>
                        <td>
                          <Row gap={8}>
                            <Mono color="var(--text)">{c.id}</Mono>
                            <span className="ellipsis" style={{ fontWeight: 500 }}>{c.title}</span>
                          </Row>
                        </td>
                        <td>
                          <Muted>{c.expected}</Muted>
                        </td>
                        <td>
                          <Res ok={c.a} />
                        </td>
                        <td>
                          <Res ok={c.b} />
                        </td>
                        <td>
                          <Num color={Number(c.score) < 0.6 ? "var(--danger)" : undefined}>{c.score}</Num>
                        </td>
                        <td>
                          <Muted>{c.duration}</Muted>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Panel>
            </div>
          )}
        </div>
        <aside className="aside" style={{ width: 320, padding: 16, display: "flex", flexDirection: "column", gap: 16 }}>
          <Section title={tx("数据集", "Datasets")} right={
              <TextLink
                icon="plus"
                onClick={async () => {
                  const v = await openForm({
                    title: tx("新建数据集", "New dataset"),
                    fields: [
                      { key: "name", label: tx("名称", "Name"), required: true },
                      { key: "from", label: tx("来源", "Source"), type: "select", options: [{ value: "incidents", label: tx("历史事件回放", "Replayed incidents") }, { value: "runs", label: tx("标注过的运行", "Annotated runs") }, { value: "manual", label: tx("手工录入", "Manual") }] },
                    ],
                    submit: tx("创建", "Create"),
                  });
                  if (v) toast(tx("数据集「{n}」已创建，可在运行详情里“加入数据集”", "Dataset “{n}” created; add runs to it from run details", { n: v.name }));
                }}
              >
                {tx("新建", "New")}
              </TextLink>
            }>
            <ul className="plain-list">
              {e?.datasets.map((d) => (
                <li key={d.name}>
                  <Icon name="layers" size={14} color={`var(--${d.tone === "neutral" ? "muted" : d.tone})`} />
                  <Col gap={2} grow>
                    <Row gap={8}>
                      <b style={{ fontSize: "var(--fs-sm)" }}>{d.name}</b>
                      <Spacer />
                      <Num color="var(--muted)">{d.count}</Num>
                    </Row>
                    <Muted size={11.5}>{d.desc}</Muted>
                  </Col>
                </li>
              ))}
            </ul>
          </Section>
          <Section title={tx("评测器", "Evaluators")}>
            <Col gap={8}>
              {e?.evaluators.map((v) => (
                <Row key={v.name} gap={8}>
                  <Icon name="check" size={13} color="var(--ok)" />
                  <span style={{ fontSize: "var(--fs-sm)", flexGrow: 1 }}>{v.name}</span>
                  <Badge>{v.kind}</Badge>
                </Row>
              ))}
            </Col>
          </Section>
          <Section title={tx("来源", "Sinks")}>
            <Col gap={8}>
              <Row gap={8}>
                <Brand name="langfuse" size={22} />
                <Muted size={11.5}>{tx("实验结果同步到 Langfuse，便于深入分析", "Results sync to Langfuse for deeper analysis")}</Muted>
              </Row>
              <Row gap={8}>
                <Brand name="phoenix" size={22} />
                <Muted size={11.5}>{tx("Trace 通过 OTLP 发送到 Phoenix", "Traces go to Phoenix via OTLP")}</Muted>
              </Row>
            </Col>
          </Section>
        </aside>
      </div>
    </Page>
  );
}

/** Datasets, evaluators and the human-review queue (the experiment tab is the page body above). */
function EvalsTab({ tab, e, onOpen }: { tab: string; e: EvalExperiment; onOpen: (id: string) => void }) {
  const { tx } = useI18n();
  if (tab === "data")
    return (
      <Panel title={tx("数据集", "Datasets")}>
        <table className="v3-table">
          <thead>
            <tr>
              <th>{tx("名称", "Name")}</th>
              <th>{tx("说明", "Description")}</th>
              <th style={{ textAlign: "right" }}>{tx("用例数", "Cases")}</th>
            </tr>
          </thead>
          <tbody>
            {e.datasets.map((d) => (
              <tr key={d.name} style={{ height: 40 }}>
                <td>
                  <b>{d.name}</b>
                </td>
                <td>
                  <Muted>{d.desc}</Muted>
                </td>
                <td style={{ textAlign: "right" }}>
                  <Num>{d.count}</Num>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    );
  if (tab === "eval")
    return (
      <Panel title={tx("评测器", "Evaluators")}>
        <table className="v3-table">
          <thead>
            <tr>
              <th>{tx("名称", "Name")}</th>
              <th>{tx("类型", "Type")}</th>
            </tr>
          </thead>
          <tbody>
            {e.evaluators.map((v) => (
              <tr key={v.name} style={{ height: 40 }}>
                <td>
                  <b>{v.name}</b>
                </td>
                <td>
                  <Badge>{v.kind}</Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    );
  return (
    <Panel title={tx("待人工标注", "Awaiting human review")}>
      <table className="v3-table">
        <thead>
          <tr>
            <th>{tx("用例", "Case")}</th>
            <th>{tx("期望", "Expected")}</th>
            <th style={{ textAlign: "right" }}>{tx("得分", "Score")}</th>
          </tr>
        </thead>
        <tbody>
          {e.cases.map((c) => (
            <tr key={c.id} className="clickable" style={{ height: 40 }} onClick={() => onOpen(c.id)}>
              <td>
                <b>{c.id}</b> <Muted>{c.title}</Muted>
              </td>
              <td>
                <Muted>{c.expected}</Muted>
              </td>
              <td style={{ textAlign: "right" }}>
                <Num>{c.score}</Num>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Panel>
  );
}
