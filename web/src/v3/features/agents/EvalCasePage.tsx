import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, useGet, useSend } from "../../api/client";
import type { EvalCase, Severity } from "../../api/types";
import { Badge, Brand, Btn, Card, Check, Col, Divider, Field, Icon, LabelChip, Muted, Num, Pills, Row, Section, Sev, Skeleton, Spacer, Strong, Textarea } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { useI18n } from "../../i18n";
import "./agents.css";

export default function EvalCasePage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { id = "INV-187" } = useParams();
  const { data: c } = useGet<EvalCase>(`/eval-cases/${id}`);
  const [cause, setCause] = useState("right");
  const [evidence, setEvidence] = useState("4");
  const [safe, setSafe] = useState("safe");
  const [clarity, setClarity] = useState("5");
  const [note, setNote] = useState("");
  const [regress, setRegress] = useState(true);
  const [tools, setTools] = useState(false);
  const submit = useSend<object, { next: string }>("POST", `/eval-cases/${id}/labels`);
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
          <Btn
            kind="primary"
            icon="check"
            loading={submit.isPending}
            onClick={() =>
              submit.mutate({ cause, evidence, safe, clarity, note, regress }, { onSuccess: (r) => { toast(tx("标注已提交", "Review submitted")); nav(`/agents/evals/${r.next}`); } })
            }
          >
            {tx("提交并下一条", "Submit & next")}
          </Btn>
        </>
      }
    >
      {!c ? (
        <div className="v3-content">
          <Skeleton h={400} />
        </div>
      ) : (
        <div className="v3-split anim-fade" key={c.id}>
          <div className="side" style={{ width: 340, padding: 16, display: "flex", flexDirection: "column", gap: 16 }}>
            <Section title={tx("输入 · 告警", "Input · alert")}>
              <Card style={{ padding: "10px 12px" }}>
                <Col gap={6}>
                  <Row gap={8}>
                    <Sev p={c.alert.sev as Severity} />
                    <Strong>{c.alert.title}</Strong>
                  </Row>
                  <Row gap={5} wrap>
                    {c.alert.labels.map((l) => (
                      <LabelChip key={l.k} k={l.k} v={l.v} />
                    ))}
                  </Row>
                </Col>
              </Card>
            </Section>
            <Section title={tx("冻结的证据快照", "Frozen evidence")}>
              <Col gap={10}>
                {c.evidence.map((e, i) => (
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
              <div className="expected">
                <b>{tx("根因：", "Cause: ")}</b>
                {c.expected.cause}
                <br />
                <b>{tx("建议：", "Advice: ")}</b>
                {c.expected.advice}
              </div>
            </Section>
          </div>
          <div className="main">
            <div className="v3-content" style={{ padding: "16px 20px" }}>
              <Row gap={8}>
                <Badge tone="accent" mono>
                  {tx("{v} 输出", "{v} output", { v: c.output.version })}
                </Badge>
                <Muted size={11.5}>{c.output.meta}</Muted>
                <Spacer />
                <Btn kind="ghost" icon="external" h={26} to="/agents/runs/run_7f1e">
                  {tx("查看 Trace", "View trace")}
                </Btn>
              </Row>
              <button type="button" className="tool-sum" aria-expanded={tools} onClick={() => setTools((t) => !t)}>
                <Icon name={tools ? "down" : "right"} size={12} />
                <span style={{ color: "var(--text)", fontWeight: 500 }}>{tx("调用 {n} 个工具", "Called {n} tools", { n: 4 })}</span>
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
              <Divider />
              <details className="baseline">
                <summary>{tx("{v} 输出（评分 {s}）", "{v} output (score {s})", { v: c.baseline.version, s: c.baseline.score })}</summary>
                <p>{c.baseline.text}</p>
              </details>
            </div>
          </div>
          <aside className="aside" style={{ width: 330, padding: 16, display: "flex", flexDirection: "column", gap: 14 }}>
            <Section title={tx("人工评分", "Human score")}>
              <Col gap={10}>
                <Field label={tx("根因是否正确", "Root cause correct?")}>
                  <Pills value={cause} onChange={setCause} options={[{ value: "right", label: tx("正确", "Yes") }, { value: "partial", label: tx("部分正确", "Partly") }, { value: "wrong", label: tx("错误", "No") }]} />
                </Field>
                <Field label={tx("证据是否充分", "Evidence sufficient?")}>
                  <Pills value={evidence} onChange={setEvidence} options={scale} />
                </Field>
                <Field label={tx("建议是否安全", "Advice safe?")}>
                  <Pills value={safe} onChange={setSafe} options={[{ value: "safe", label: tx("安全", "Safe") }, { value: "risky", label: tx("有风险", "Risky") }]} />
                </Field>
                <Field label={tx("表达是否清晰", "Clear?")}>
                  <Pills value={clarity} onChange={setClarity} options={scale} />
                </Field>
                <Field label={tx("备注", "Notes")}>
                  <Textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} placeholder={tx("建议里可以补充限流配额的具体数值来源。", "e.g. cite where the quota number comes from.")} />
                </Field>
                <Check checked={regress} onChange={setRegress} label={tx("加入回归集（v14 起必须通过）", "Add to regression set (required from v14)")} />
              </Col>
            </Section>
            <Section title={tx("自动评测", "Automatic checks")}>
              <Col gap={7}>
                <Row gap={6}>
                  <Badge tone="accent">{tx("LLM 评审", "LLM judge")}</Badge>
                  <Num>{c.auto.score}</Num>
                  <Muted size={11.5}>{c.auto.note}</Muted>
                </Row>
                {c.auto.rules.map((r) => (
                  <Row key={r} gap={6}>
                    <Icon name="check" size={14} color="var(--ok)" />
                    <Muted>{r}</Muted>
                  </Row>
                ))}
              </Col>
            </Section>
          </aside>
        </div>
      )}
    </Page>
  );
}
