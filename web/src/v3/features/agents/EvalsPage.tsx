import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, useGet, useSend } from "../../api/client";
import { PageUnavailable, Unavailable } from "../../ui/unknown";
import type { EvalExperiment, EvalList } from "../../api/types";
import { DataTable } from "../../ui/data";
import { Badge, Brand, Btn, Card, Col, Icon, Legend, Mono, Muted, Num, Panel, Row, Section, Skeleton, Spacer, Tabs, TextLink, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Radar } from "../../charts";
import { useI18n } from "../../i18n";
import "./agents.css";
import { openForm } from "../../ui/demo";

export default function EvalsPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  // Real mode lists persisted experiments (GET /evals, PA-20) and opens one by ?id=.
  const expId = API_MODE === "mock" ? "exp-v12-v13" : params.get("id");
  const [page, setPage] = useState(1);
  const list = useGet<EvalList>(API_MODE !== "mock" && !expId ? "/evals" : null, { page, page_size: 50 });
  const { data: e } = useGet<EvalExperiment>(expId ? `/evals/${expId}` : null);
  // Hooks stay above the early return below: opening a new experiment must not change the hook order.
  const [tab, setTab] = useState("exp");
  const [selPick, setSel] = useState(API_MODE === "mock" ? "INV-187" : "");
  const sel = selPick || e?.cases?.[0]?.id || "";
  const demo = API_MODE === "mock";
  // Version names come from the experiment; the sample data uses its sample versions.
  const verA = demo ? "v12" : e?.baseline_version ?? "A";
  const verB = demo ? "v13" : e?.candidate_version ?? "B";
  const rerun = useSend<void, { id: string }>("POST", "/evals");
  const create = useSend<{ agent_id: string; title: string; baseline_version: string; candidate_version: string }, { id: string; status: string; execution_available: boolean }>("POST", "/evals");
  const newExperiment = async () => {
    const v = await openForm({
      title: tx("新建评测实验", "New experiment"),
      desc: tx("对比同一 Agent 的两个已发布版本。实验先保存为待运行，不会立即调用模型。", "Compare two published versions of one agent. The experiment is saved as pending; no model is called yet."),
      fields: [
        { key: "agent", label: tx("Agent ID", "Agent ID"), mono: true, required: true },
        { key: "title", label: tx("实验名称", "Title"), required: true },
        { key: "a", label: tx("基线版本", "Baseline version"), mono: true, required: true },
        { key: "b", label: tx("候选版本", "Candidate version"), mono: true, required: true },
      ],
      submit: tx("保存实验", "Save experiment"),
    });
    if (!v) return;
    const r = await create.mutateAsync({ agent_id: v.agent, title: v.title, baseline_version: v.a, candidate_version: v.b }).catch((err: ApiError) => (toast(err.message, { tone: "danger" }), null));
    if (!r) return;
    toast(r.execution_available ? tx("实验已保存，待运行", "Experiment saved, ready to run") : tx("实验已保存；运行器未接入，暂不能执行", "Experiment saved; the runner is not connected, so it can't run yet"));
    setParams({ id: r.id });
  };
  const EVAL_STATUS = { pending: ["neutral", tx("待运行", "Pending")], running: ["accent", tx("运行中", "Running")], passed: ["ok", tx("通过", "Passed")], failed: ["danger", tx("未通过", "Failed")], invalid: ["warn", tx("无效", "Invalid")] } as const;
  if (API_MODE !== "mock" && !expId) {
    const l = list.data;
    return (
      <Page crumbs={[tx("Agent 运行台", "Agent console"), tx("评测", "Evaluations")]} error={!l ? list.error : undefined} errorSource={tx("评测记录", "evaluation records")} actions={<Btn kind="primary" icon="play" onClick={() => void newExperiment()}>{tx("新建实验", "New experiment")}</Btn>}>
        {l && !l.items.length ? (
          <PageUnavailable
            icon="activity"
            configure={false}
            title={l.empty_reason ? tx("没有你能查看的评测实验", "No experiments you can view") : tx("还没有评测实验", "No experiments yet")}
            desc={l.empty_reason ? tx("当前环境里的实验都不在你有权查看的应用里。", "None of the experiments in this environment belong to an application you can view.") : tx("评测对比同一 Agent 的两个版本。新建实验后会出现在这里。", "An evaluation compares two versions of one agent. New experiments appear here.")}
          />
        ) : (
          <div className="v3-content">
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={l?.items}
                loading={!l}
                rowKey={(x) => x.id}
                onRowClick={(x) => setParams({ id: x.id })}
                columns={[
                  { key: "t", header: tx("实验", "Experiment"), width: "32%", render: (x) => <Col gap={0} style={{ minWidth: 0 }}><span className="ellipsis" style={{ fontWeight: 500 }}>{x.title}</span><Muted size={11.5}>{x.agent_name}</Muted></Col> },
                  { key: "v", header: tx("基线 → 候选", "Baseline → candidate"), render: (x) => <Mono size={12}>{x.baseline_version} → {x.candidate_version}</Mono> },
                  { key: "s", header: tx("状态", "Status"), render: (x) => <Badge tone={EVAL_STATUS[x.status][0]}>{EVAL_STATUS[x.status][1]}</Badge> },
                  { key: "c", header: tx("创建", "Created"), render: (x) => <Muted>{new Date(x.created_at).toLocaleString()}</Muted> },
                  { key: "f", header: tx("完成", "Finished"), render: (x) => <Muted>{x.finished_at ? new Date(x.finished_at).toLocaleString() : "—"}</Muted> },
                ]}
              />
            </Card>
            {l && l.total > l.page * l.page_size && (
              <Row gap={8}>
                <Spacer />
                <Muted size={11.5}>{tx("共 {n} 个", "{n} total", { n: l.total })}</Muted>
                <Btn h={26} disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>{tx("上一页", "Previous")}</Btn>
                <Btn h={26} onClick={() => setPage((p) => p + 1)}>{tx("下一页", "Next")}</Btn>
              </Row>
            )}
          </div>
        )}
      </Page>
    );
  }
  const Res = ({ ok }: { ok: boolean }) => <Icon name={ok ? "check" : "x"} size={14} color={ok ? "var(--ok)" : "var(--danger)"} />;
  return (
    <Page flush crumbs={[tx("Agent 运行台", "Agent console"), API_MODE === "mock" ? tx("评测", "Evaluations") : { label: tx("评测", "Evaluations"), to: "/agents/evals" }, ...(API_MODE === "mock" || !e ? [] : [e.title])]} actions={
        <Btn
          kind="primary"
          icon="play"
          onClick={async () => {
            // Real experiments are created with the agent/version form above; the dataset picker below is sample-only.
            if (API_MODE !== "mock") return void newExperiment();
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
          { value: "exp", label: tx("实验", "Experiments"), count: API_MODE === "mock" ? 12 : undefined },
          { value: "data", label: tx("数据集", "Datasets"), count: API_MODE === "mock" ? 5 : undefined },
          { value: "eval", label: tx("评测器", "Evaluators"), count: API_MODE === "mock" ? 8 : undefined },
          { value: "label", label: tx("人工标注", "Human review"), count: API_MODE === "mock" ? 3 : undefined },
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
                      {e.dataset && <Badge>{e.dataset}</Badge>}
                      {!demo && e.status && <Badge tone={e.status === "passed" ? "ok" : e.status === "failed" ? "danger" : "neutral"}>{e.status}</Badge>}
                      {!demo && <Mono color="var(--muted)">{verA} → {verB}</Mono>}
                    </Row>
                    <Muted size={11.5}>{e.meta}</Muted>
                  </Col>
                  <Spacer />
                  {/* Review count and re-run are sample-only; real results and runs come from the execution record. */}
                  {demo && (
                    <>
                      <Btn icon="edit" to={`/agents/evals/${sel}`}>
                        {tx("人工标注 3 条", "Review 3 cases")}
                      </Btn>
                      <Btn kind="ghost" icon="rotate" loading={rerun.isPending} onClick={() => rerun.mutate(undefined, { onSuccess: () => toast(tx("实验已排队，预计 26 分钟", "Experiment queued, ~26 min")) })}>
                        {tx("重新运行", "Re-run")}
                      </Btn>
                    </>
                  )}
                </Row>
              </Card>
              {!e.metrics || !e.cases || !e.radar ? (
                <Card>
                  <Unavailable reason="not_generated" icon="activity" title={tx("这个实验还没有结果", "No results for this experiment yet")} desc={tx("实验运行完成后，这里显示两个版本的指标对比、评测维度和用例结果。", "Once the experiment has run, the metric comparison, dimensions and case results show here.")} />
                </Card>
              ) : (
              <>
              <Row gap={14} align="stretch">
                <Panel title={tx("指标对比", "Metrics")} right={e.gate_failed && <Badge tone="warn"><Icon name="alert" size={11} />{e.gate_failed}</Badge>} style={{ flex: "1 1 0", minWidth: 0 }}>
                  <table className="v3-table">
                    <thead>
                      <tr>
                        <th style={{ width: "34%" }}>{tx("指标", "Metric")}</th>
                        <th>{verA}</th>
                        <th>{verB}</th>
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
                <Panel title={tx("评测维度 · {a} vs {b}", "Dimensions · {a} vs {b}", { a: verA, b: verB })} style={{ width: 280, flex: "none" }}>
                  <Col gap={4} align="center">
                    <Radar axes={e.radar.axes} size={204} series={[{ values: e.radar.a, color: "var(--c5)", name: verA }, { values: e.radar.b, color: "var(--c1)", fill: "var(--c1s)", name: verB }]} />
                    <Legend items={[[verA, "var(--c5)"], [verB, "var(--c1)"]]} />
                    <Muted size={11}>{tx("越靠外越好；速度与成本已归一化", "Outer is better; speed and cost normalised")}</Muted>
                  </Col>
                </Panel>
              </Row>
              <Panel title={demo ? tx("用例结果 · 48 例（显示差异项）", "Case results · 48 (showing differences)") : tx("用例结果 · {n} 例", "Case results · {n}", { n: e.cases.length })} right={<TextLink onClick={() => setTab("label")}>{tx("全部用例", "All cases")}</TextLink>}>
                <table className="v3-table">
                  <thead>
                    <tr>
                      <th style={{ width: "34%" }}>{tx("用例", "Case")}</th>
                      <th>{tx("期望", "Expected")}</th>
                      <th>{verA}</th>
                      <th>{verB}</th>
                      <th>{tx("评分", "Score")}</th>
                      <th>{tx("耗时", "Duration")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(e.cases ?? []).map((c) => (
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
              </>
              )}
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
              {e && !e.datasets && <Muted size={11.5}>{tx("这个实验还没有关联数据集", "No dataset is linked to this experiment yet")}</Muted>}
              {e?.datasets?.map((d) => (
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
              {e && !e.evaluators && <Muted size={11.5}>{tx("还没有记录评测器", "No evaluators are recorded yet")}</Muted>}
              {e?.evaluators?.map((v) => (
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
            {(e.datasets ?? []).map((d) => (
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
            {(e.evaluators ?? []).map((v) => (
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
          {(e.cases ?? []).map((c) => (
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
