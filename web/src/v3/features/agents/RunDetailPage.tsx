import { Fragment, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet, useSend, API_MODE, ApiError, api } from "../../api/client";
import { Gate } from "../../ui/permission";
import type { RunDetail } from "../../api/types";
import { Badge, Btn, Card, CardHead, Col, Icon, IconBtn, Mono, Muted, Row, Skeleton, Spacer, Tabs, Tile, cx, type IconName } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Axis } from "../../charts";
import { useI18n } from "../../i18n";
import { AgentTrace } from "./AgentTrace";
import "./agents.css";
import { openForm, openExternal, downloadFile } from "../../ui/demo";
import { Unavailable } from "../../ui/unknown";

export default function RunDetailPage() {
  const { tx } = useI18n();
  const { id = API_MODE === "mock" ? "run_7f3a" : "" } = useParams();
  const demo = API_MODE === "mock";
  const { data: r } = useGet<RunDetail>(id ? `/agent-runs/${id}` : null);
  const [tab, setTab] = useState("trace");
  const [stepId, setStepId] = useState(demo ? "s2" : "");
  const [vote, setVote] = useState<"up" | "down" | null>(null);
  const feedback = useSend<{ vote: "up" | "down" }>("POST", `/agent-runs/${id}/feedback`);
  const step = r?.steps?.find((s) => s.id === stepId);
  // Steps are milliseconds from the run start; an unfinished run has no total, so scale to the last step.
  const total = r ? r.total ?? Math.max(1, ...(r.steps ?? []).map((s) => s.start + (s.dur ?? 0))) : 1;
  const axis = [0, 0.25, 0.5, 0.75, 1].map((f) => `${Math.round((f * total) / 1000)} s`);
  const rate = (v: "up" | "down") =>
    feedback.mutate(
      { vote: v },
      {
        onSuccess: () => {
          setVote(v);
          toast(v === "up" ? tx("已标记为有用", "Marked helpful") : tx("已标记为无用，会进入改进队列", "Marked not helpful — added to the improvement queue"));
        },
        onError: (e) => toast(e.message, { tone: "danger" }),
      },
    );
  return (
    <Page
      flush
      crumbs={[tx("Agent 运行台", "Agent console"), tx("运行与 Trace", "Runs & traces"), tx("运行 {id}", "Run {id}", { id })]}
      actions={
        <>
          {/* Feedback and annotations need agents.change (PA-150). */}
          <Gate need={["agents", "change"]}>
            <IconBtn icon="check" label={tx("有用", "Helpful")} active={vote === "up"} onClick={() => rate("up")} />
          </Gate>
          <Gate need={["agents", "change"]}>
            <IconBtn icon="x" label={tx("无用", "Not helpful")} active={vote === "down"} onClick={() => rate("down")} />
          </Gate>
          {/* Adding a run to an eval dataset needs a dataset picker (POST /eval-datasets/{id}/items); sample data only for now. */}
          {demo && <Btn icon="plus" onClick={() => toast(tx("已加入数据集「根因分析 · 回归」", "Added to dataset “Root cause · regression”"))}>{tx("加入数据集", "Add to dataset")}</Btn>}
          <Gate need={["agents", "change"]}>
          <Btn
            icon="edit"
            onClick={async () => {
              const v = await openForm({
                title: tx("标注运行 {id}", "Annotate run {id}", { id }),
                fields: [
                  { key: "score", label: tx("结论质量", "Answer quality"), type: "select", initial: "4", options: ["5", "4", "3", "2", "1"].map((s) => ({ value: s, label: s })) },
                  { key: "note", label: tx("备注", "Note"), type: "textarea", placeholder: tx("哪里对、哪里不对，评测会用到", "What was right or wrong; evals use this") },
                ],
                submit: tx("保存标注", "Save annotation"),
              });
              if (!v) return;
              if (demo) return toast(tx("标注已保存：{s} 分", "Annotation saved: {s}/5", { s: v.score }));
              try {
                await api("POST", `/agent-runs/${id}/annotations`, { body: { score: Number(v.score), ...(v.note?.trim() ? { note: v.note.trim() } : {}) } });
                toast(tx("标注已保存：{s} 分", "Annotation saved: {s}/5", { s: v.score }));
              } catch (e) {
                toast(e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
              }
            }}
          >
            {tx("标注", "Annotate")}
          </Btn>
          </Gate>
          {demo && <Btn kind="ghost" icon="external" onClick={() => openExternal("Phoenix", `https://phoenix.example.internal/projects/aiops/traces/${id}`)}>{tx("在 Phoenix 中打开", "Open in Phoenix")}</Btn>}
          <Btn
            kind="ghost"
            icon="download"
            onClick={() => {
              downloadFile(`agent-run-${id}.json`, JSON.stringify({ sample_data: API_MODE === "mock", run: r }, null, 2));
              toast(tx("运行记录已导出", "Run exported"));
            }}
          >
            {tx("导出", "Export")}
          </Btn>
        </>
      }
    >
      <Tabs
        value={tab}
        onChange={setTab}
        options={[
          { value: "trace", label: "Trace" },
          { value: "run", label: tx("运行概览", "Overview") },
          { value: "memory", label: tx("记忆", "Memory") },
          { value: "skills", label: tx("Skills 与工具", "Skills & tools") },
          { value: "policy", label: tx("自动化策略", "Automation policy") },
          { value: "usage", label: tx("模型用量", "Model usage") },
        ]}
      />
      {tab === "trace" ? (
        <AgentTrace runId={id} />
      ) : !r ? (
        <div className="v3-content">
          <Skeleton h={60} />
          <Skeleton h={300} />
        </div>
      ) : (
        <div className="v3-content anim-fade" style={{ padding: "14px 24px" }} key={tab}>
          <Card className="run-chain">
            {r.chain.map((c, i) => {
              const last = i === r.chain.length - 2;
              const inner = (
                <>
                  <Icon name={c.icon as IconName} size={15} color={last ? "var(--accent-text)" : "var(--muted)"} />
                  <span className="v3-col" style={{ gap: 0, lineHeight: 1.25 }}>
                    <span style={{ fontSize: "var(--fs-cap)", color: "var(--muted)" }}>{c.label}</span>
                    <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{c.value}</span>
                  </span>
                </>
              );
              return (
                <Fragment key={c.label}>
                  {c.to ? (
                    <Link to={c.to} className={cx("chain-node", last && "on")}>
                      {inner}
                    </Link>
                  ) : (
                    <span className={cx("chain-node", last && "on")}>{inner}</span>
                  )}
                  {i < r.chain.length - 1 && <Icon name="right" size={14} color="var(--faint)" />}
                </Fragment>
              );
            })}
            <Spacer />
            {r.stats.map((s) => (
              <span key={s.k} className="v3-col" style={{ gap: 0, lineHeight: 1.3 }}>
                <span style={{ fontSize: "var(--fs-cap)", color: "var(--muted)" }}>{s.k}</span>
                <b className="tnum" style={{ fontSize: "var(--fs-lg)", fontWeight: 600 }}>{s.v}</b>
              </span>
            ))}
          </Card>
          {tab === "run" && (
            <div className="run-grid">
              <Card>
                <CardHead
                  title={tx("执行时间线", "Execution timeline")}
                  right={
                    <Row gap={12}>
                      <span className="lg-dot">
                        <i style={{ background: "var(--c1)" }} />
                        {tx("模型", "Model")}
                      </span>
                      <span className="lg-dot">
                        <i style={{ background: "var(--c2)" }} />
                        {tx("工具", "Tool")}
                      </span>
                    </Row>
                  }
                />
                <div className="gantt">
                  <div className="g-row axis">
                    <span />
                    <Axis labels={axis} style={{ marginTop: 0 }} />
                    <span />
                  </div>
                  {!r.steps && <Muted size={11.5}>{tx("这次运行没有留下步骤记录", "This run recorded no steps")}</Muted>}
                  {r.steps?.map((s, i) => (
                    <button key={s.id} type="button" className={cx("g-row", stepId === s.id && "on", s.kind === "model" && "model")} onClick={() => s.kind === "tool" && setStepId(s.id)} disabled={s.kind === "model"} style={{ animationDelay: `${i * 50}ms` }}>
                      <span className="name">
                        <Icon name={s.icon as IconName} size={14} />
                        {s.name}
                      </span>
                      <span className="track">
                        <i style={{ left: `${(100 * s.start) / total}%`, width: `max(4px, ${(100 * (s.dur ?? 0)) / total}%)`, background: s.kind === "model" ? "var(--c1)" : "var(--c2)" }} />
                      </span>
                      <span className="meta tnum">{s.meta}</span>
                    </button>
                  ))}
                </div>
              </Card>
              <Card>
                <CardHead title={tx("上下文组成", "Context composition")} right={r.budget && <Mono>{r.budget}</Mono>} />
                {!r.context ? (
                  <div style={{ padding: 16 }}>
                    <Unavailable reason="not_generated" title={tx("上下文组成", "Context composition")} desc={tx("还没有记录每次运行的上下文组成。", "Per-run context composition isn't recorded yet.")} icon="layers" />
                  </div>
                ) : (
                <Col gap={12} style={{ padding: "14px 16px" }}>
                  <div className="ctx-stack">
                    {r.context.map((c) => (
                      <span key={c.name} title={c.name} style={{ width: `${c.pct}%`, background: `var(--${c.color})` }} />
                    ))}
                  </div>
                  <ul className="ctx-rows">
                    {r.context.map((c) => (
                      <li key={c.name}>
                        <i style={{ background: `var(--${c.color})` }} />
                        <span className="grow">{c.name}</span>
                        <Mono color="var(--text)">{c.tokens}</Mono>
                      </li>
                    ))}
                  </ul>
                  <div className="isolated">
                    <Muted size={11.5} style={{ fontWeight: 500 }}>{tx("未进入本次上下文（分支隔离）", "Not in this context (branch isolation)")}</Muted>
                    <Row gap={6} wrap>
                      {r.isolated?.map((x) => (
                        <Badge key={x}>{x}</Badge>
                      ))}
                    </Row>
                  </div>
                </Col>
                )}
              </Card>
              {step && (
                <Card className="anim-fade" key={step.id}>
                  <CardHead
                    title={tx("步骤详情 · {n}", "Step · {n}", { n: step.name })}
                    right={
                      <Badge tone="ok">
                        <Icon name="check" size={11} />
                        {tx("成功", "OK")}
                      </Badge>
                    }
                  />
                  <Col gap={10} style={{ padding: "12px 16px" }}>
                    <pre className="v3-code" style={{ margin: 0 }}>
                      {[step.call, ...(step.result ?? [])].join("\n")}
                    </pre>
                    <Row gap={14}>
                      <Muted>
                        {tx("连接器", "Connector")} <b style={{ color: "var(--text)", fontWeight: 500 }}>{step.connector}</b>
                      </Muted>
                      <Muted>{tx("只读", "Read-only")}</Muted>
                      <Muted>
                        {tx("耗时", "Took")} {step.meta}
                      </Muted>
                    </Row>
                    <Row gap={8}>
                      {demo && (
                        <>
                          <Btn h={26} icon="file" to="/canvas/INV-231">
                            {tx("查看证据", "View evidence")}
                          </Btn>
                          <Btn h={26} kind="ghost" icon="external" onClick={() => openExternal("Grafana", "https://grafana.example.internal/d/checkout-api")}>
                            {tx("在 Grafana 中打开", "Open in Grafana")}
                          </Btn>
                        </>
                      )}
                      <Spacer />
                      <Mono color="var(--muted)" size={11.5}>span · execute_tool</Mono>
                    </Row>
                  </Col>
                </Card>
              )}
              <MemoryCard r={r} />
            </div>
          )}
          {tab === "memory" && <MemoryCard r={r} />}
          {tab !== "run" && tab !== "memory" && tab !== "trace" && (
            <Card style={{ padding: 20 }}>
              <Muted>{tx("这一栏展示本次运行加载的 Skills、工具定义、自动化策略与模型用量，数据结构与运行记录相同，待后端提供。", "This tab shows the skills, tool definitions, automation policy and model usage loaded for this run; same data shape as the run record, pending backend.")}</Muted>
            </Card>
          )}
        </div>
      )}
    </Page>
  );
}

function MemoryCard({ r }: { r: RunDetail }) {
  const { tx } = useI18n();
  return (
    <Card>
      <CardHead title={tx("记忆", "Memory")} right={API_MODE === "mock" && <Badge>{tx("读取 3 · 写入 1", "3 read · 1 written")}</Badge>} />
      {!r.memory && (
        <div style={{ padding: 16 }}>
          <Unavailable reason="not_generated" title={tx("记忆读写", "Memory reads and writes")} desc={tx("还没有记录运行中的记忆读写。", "Memory reads and writes during runs aren't recorded yet.")} icon="memory" />
        </div>
      )}
      <ul className="mem-layers">
        {r.memory?.map((m) => (
          <li key={m.name}>
            <Tile icon={m.icon as IconName} size={26} />
            <Col gap={3} grow style={{ minWidth: 0 }}>
              <Row gap={8} align="baseline">
                <b style={{ fontSize: "var(--fs-sm)", fontWeight: 600 }}>{m.name}</b>
                <Muted size={11.5}>{m.desc}</Muted>
              </Row>
              {m.hits.map((h) => (
                <Row key={h.text} gap={8}>
                  <span className="grow" style={{ fontSize: "var(--fs-sm)" }}>{h.text}</span>
                  {h.score && <Mono>{h.score}</Mono>}
                </Row>
              ))}
            </Col>
          </li>
        ))}
      </ul>
    </Card>
  );
}
