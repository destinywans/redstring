import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, useGet, useSend } from "../../api/client";
import type { EvalCase, Me, Severity } from "../../api/types";
import { Badge, Brand, Btn, Card, Check, Col, Divider, Field, Icon, LabelChip, Muted, Num, Pills, Row, Section, Sev, Skeleton, Spacer, Strong, Textarea } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { useI18n } from "../../i18n";
import "./agents.css";
import { Unavailable } from "../../ui/unknown";
import { Gate } from "../../ui/permission";

// The server's fixed English descriptions of a single-case run, said in the page's language (PA-148).
const OUTPUT_META = "Single human-supplied case; tool-free model output; usage unknown";
const AUTO_NOTE = "Fraction of explicit case-sensitive literal checks satisfied. Not a human correctness label or general Agent quality score.";
const VERDICT: Record<string, [string, string]> = { correct: ["正确", "Correct"], partial: ["部分正确", "Partly"], incorrect: ["错误", "Incorrect"], unsure: ["不确定", "Unsure"] };

export default function EvalCasePage() {
  const { tx, fmt } = useI18n();
  const nav = useNavigate();
  const demo = API_MODE === "mock";
  const { data: me } = useGet<Me>(demo ? null : "/me");
  const { id = demo ? "INV-187" : "" } = useParams();
  const { data: c, error, refetch } = useGet<EvalCase>(id ? `/eval-cases/${id}` : null);
  // A case without a trusted snapshot can still show the input it was registered with.
  const alert = c?.alert ?? c?.human_input?.alert ?? null;
  const evidence = c?.evidence ?? c?.human_input?.evidence ?? null;
  const expected = c?.expected ?? c?.human_input?.expected ?? null;
  const [cause, setCause] = useState("right");
  const [evidenceScore, setEvidence] = useState("4");
  const [safe, setSafe] = useState("safe");
  const [clarity, setClarity] = useState("5");
  const [note, setNote] = useState("");
  const [regress, setRegress] = useState(true);
  const [tools, setTools] = useState(false);
  const submit = useSend<object, { next: string; revision?: number }>("POST", `/eval-cases/${id}/labels`);
  // Real labels store one verdict and a note against the revision that was read; the other scores are sample-only.
  const send = () => {
    if (demo)
      return submit.mutate({ cause, evidence: evidenceScore, safe, clarity, note, regress }, { onSuccess: (r) => { toast(tx("标注已提交", "Review submitted")); nav(`/agents/evals/${r.next}`); } });
    const next = ({ right: "correct", partial: "partial", wrong: "incorrect" } as Record<string, string>)[cause] ?? "unsure";
    submit.mutate(
      { next, note, expected_revision: c?.revision ?? 0 },
      {
        onSuccess: () => {
          setNote("");
          void refetch();
          toast(tx("标注已提交", "Review submitted"));
        },
        onError: (e) => {
          if (e instanceof ApiError && e.status === 409) void refetch();
          toast(e instanceof ApiError && e.status === 409 ? tx("别人刚标注过这个用例，已重新读取，请复核后再提交", "Someone just labelled this case; reloaded — review and submit again") : e.message, { tone: "danger" });
        },
      },
    );
  };
  const scale = ["1", "2", "3", "4", "5"].map((v) => ({ value: v, label: v }));
  return (
    <Page
      flush
      ai={false}
      crumbs={[tx("Agent 运行台", "Agent console"), tx("评测", "Evaluations"), c?.title ?? id]}
      actions={
        <>
          <Muted>{c?.queue}</Muted>
          <Btn kind="ghost" onClick={() => nav("/agents/evals")}>
            {tx("跳过", "Skip")}
          </Btn>
          {/* Labels need agents.change (PA-148). */}
          <Gate need={["agents", "change"]}>
            <Btn
              kind="primary"
              icon="check"
              loading={submit.isPending}
              disabled={!c}
              onClick={send}
            >
              {demo ? tx("提交并下一条", "Submit & next") : tx("提交标注", "Submit review")}
            </Btn>
          </Gate>
        </>
      }
    >
      {!c ? (
        <div className="v3-content">
          {error || !id ? <Unavailable reason="unavailable" icon="file" title={tx("没有读到这个评测用例", "Couldn't read this eval case")} desc={error instanceof Error ? error.message : undefined} /> : <Skeleton h={400} />}
        </div>
      ) : (
        <div className="v3-split anim-fade" key={c.id}>
          <div className="side" style={{ width: 340, padding: 16, display: "flex", flexDirection: "column", gap: 16 }}>
            <Section title={tx("输入 · 告警", "Input · alert")}>
              {!alert ? (
                c.human_input?.input ? <Muted>{c.human_input.input}</Muted> : <Unavailable reason="not_generated" icon="bell" title={tx("没有保存输入", "No input stored")} />
              ) : (
              <Card style={{ padding: "10px 12px" }}>
                <Col gap={6}>
                  <Row gap={8}>
                    <Sev p={alert.sev as Severity} />
                    <Strong>{alert.title}</Strong>
                  </Row>
                  <Row gap={5} wrap>
                    {(alert.labels ?? []).map((l) => (
                      <LabelChip key={l.k} k={l.k} v={l.v} />
                    ))}
                  </Row>
                </Col>
              </Card>
              )}
            </Section>
            <Section title={tx("冻结的证据快照", "Frozen evidence")}>
              <Col gap={10}>
                {!evidence && <Unavailable reason="not_generated" icon="file" title={tx("没有冻结的证据快照", "No frozen evidence snapshot")} />}
                {evidence?.map((e, i) => (
                  <Row key={e.title} gap={10}>
                    <Brand name={e.brand} size={22} />
                    <Col gap={0} style={{ minWidth: 0 }}>
                      <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>
                        <sup className="v3-sup">{i + 1}</sup> {e.title}
                      </span>
                      <Muted size={11.5}>{e.sub}</Muted>
                    </Col>
                  </Row>
                ))}
              </Col>
            </Section>
            <Section title={tx("期望答案", "Expected answer")}>
              {!expected ? (
                <Unavailable reason="not_generated" icon="check" title={tx("没有登记期望答案", "No expected answer registered")} />
              ) : (
                <div className="expected">
                  <b>{tx("根因：", "Cause: ")}</b>
                  {expected.cause}
                  <br />
                  <b>{tx("建议：", "Advice: ")}</b>
                  {expected.advice}
                </div>
              )}
            </Section>
          </div>
          <div className="main">
            <div className="v3-content" style={{ padding: "16px 20px" }}>
              {!c.output ? (
                <Card>
                  <Unavailable reason="not_generated" icon="sparkles" title={tx("还没有可评的输出", "No output to review yet")} desc={tx("实验运行并保存两个版本的输出后，这里显示待评的回答和基线对比。", "Once the experiment has run and saved both outputs, the answer and its baseline show here.")} />
                </Card>
              ) : (
              <>
              <Row gap={8}>
                <Badge tone="accent" mono>
                  {tx("{v} 输出", "{v} output", { v: c.output.version })}
                </Badge>
                <Muted size={11.5}>{!demo && c.output.meta === OUTPUT_META ? tx("单个人工案例；无工具的模型输出；用量未知", "Single human-supplied case; tool-free model output; usage unknown") : c.output.meta}</Muted>
                <Spacer />
                {demo && (
                  <Btn kind="ghost" icon="external" h={26} to="/agents/runs/run_7f1e">
                    {tx("查看 Trace", "View trace")}
                  </Btn>
                )}
              </Row>
              <button type="button" className="tool-sum" aria-expanded={tools} onClick={() => setTools((t) => !t)}>
                <Icon name={tools ? "down" : "right"} size={12} />
                <span style={{ color: "var(--text)", fontWeight: 500 }}>{tx("调用 {n} 个工具", "Called {n} tools", { n: demo ? 4 : c.output.tools.length })}</span>
                {c.output.tools.map((t) => (
                  <Badge key={t} tone="accent">
                    {t}
                  </Badge>
                ))}
              </button>
              {tools && (
                <ol className="tool-steps anim-fade">
                  {/* Sample calls with arguments in mock mode; the real case only names its tools. */}
                  {(API_MODE === "mock" ? ["list_changes(service=checkout-api, window=2h)", "query_metrics(q=upstream_429_ratio)", "query_metrics(q=payment_p99)", "search_logs(q=\"rate limited\")"] : c.output.tools).map((s) => (
                    <li key={s} className="mono">{s}</li>
                  ))}
                </ol>
              )}
              <div style={{ fontSize: "var(--fs-body)", lineHeight: 1.65 }}>
                <Markdown text={c.output.markdown} />
              </div>
              {c.baseline && (
                <>
                  <Divider />
                  <details className="baseline">
                    <summary>{tx("{v} 输出（评分 {s}）", "{v} output (score {s})", { v: c.baseline.version, s: c.baseline.score })}</summary>
                    <p>{c.baseline.text}</p>
                  </details>
                </>
              )}
              </>
              )}
            </div>
          </div>
          <aside className="aside" style={{ width: 330, padding: 16, display: "flex", flexDirection: "column", gap: 14 }}>
            <Section title={tx("人工评分", "Human score")}>
              <Col gap={10}>
                <Field label={tx("根因是否正确", "Root cause correct?")}>
                  <Pills value={cause} onChange={setCause} options={[{ value: "right", label: tx("正确", "Yes") }, { value: "partial", label: tx("部分正确", "Partly") }, { value: "wrong", label: tx("错误", "No") }]} />
                </Field>
                {demo && (
                  <>
                    <Field label={tx("证据是否充分", "Evidence sufficient?")}>
                      <Pills value={evidenceScore} onChange={setEvidence} options={scale} />
                    </Field>
                    <Field label={tx("建议是否安全", "Advice safe?")}>
                      <Pills value={safe} onChange={setSafe} options={[{ value: "safe", label: tx("安全", "Safe") }, { value: "risky", label: tx("有风险", "Risky") }]} />
                    </Field>
                    <Field label={tx("表达是否清晰", "Clear?")}>
                      <Pills value={clarity} onChange={setClarity} options={scale} />
                    </Field>
                  </>
                )}
                <Field label={tx("备注", "Notes")}>
                  <Textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} placeholder={demo ? tx("建议里可以补充限流配额的具体数值来源。", "e.g. cite where the quota number comes from.") : tx("写下判断依据（可选）", "Why (optional)")} />
                </Field>
                {demo && <Check checked={regress} onChange={setRegress} label={tx("加入回归集（v14 起必须通过）", "Add to regression set (required from v14)")} />}
              </Col>
            </Section>
            {!demo && !!c.labels?.length && (
              <Section title={tx("已有标注", "Existing reviews")}>
                <Col gap={6}>
                  {c.labels.map((l, i) => (
                    <Row key={i} gap={6} align="flex-start">
                      <Badge>{VERDICT[l.label] ? tx(...VERDICT[l.label]) : l.label}</Badge>
                      <Col gap={0} style={{ minWidth: 0 }}>
                        <Muted size={11.5}>
                          {me && l.actor === me.id ? tx("你", "You") : tx("成员 {id}", "Member {id}", { id: l.actor.slice(0, 8) })} · {/^\d{4}-\d{2}-\d{2}T/.test(l.time) ? fmt.dateTime(l.time) : l.time}
                        </Muted>
                        {l.note && <span style={{ fontSize: "var(--fs-sub)" }}>{l.note}</span>}
                      </Col>
                    </Row>
                  ))}
                </Col>
              </Section>
            )}
            {c.auto && (
            <Section title={tx("自动评测", "Automatic checks")}>
              <Col gap={7}>
                <Row gap={6}>
                  {/* Real automatic checks are literal phrase matches, not a model judge (PA-148). */}
                  <Badge tone="accent">{demo ? tx("LLM 评审", "LLM judge") : tx("字面检查", "Literal checks")}</Badge>
                  <Num>{c.auto.score}</Num>
                  <Muted size={11.5}>{!demo && c.auto.note === AUTO_NOTE ? tx("命中的检查词比例；不是人工正确性判断，也不是 Agent 的整体质量分。", "Share of the case's literal checks found; not a human correctness label or an overall Agent score.") : c.auto.note}</Muted>
                </Row>
                {c.auto.rules.map((r) => {
                  // The case lists its checks without per-check results: only an all-or-nothing score says which passed.
                  const score = Number(c.auto!.score);
                  const known = demo || score === 1 ? "ok" : score === 0 ? "fail" : null;
                  return (
                    <Row key={r} gap={6}>
                      <Icon name={known === "fail" ? "x" : known ? "check" : "info"} size={14} color={known === "ok" ? "var(--ok)" : known === "fail" ? "var(--danger)" : "var(--muted)"} />
                      <Muted>{r}</Muted>
                    </Row>
                  );
                })}
              </Col>
            </Section>
            )}
          </aside>
        </div>
      )}
    </Page>
  );
}
