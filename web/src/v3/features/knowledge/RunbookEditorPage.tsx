import { useState } from "react";
import { useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import type { Runbook, RunbookStep } from "../../api/types";
import { Avatar, Badge, Brand, Btn, Code, Col, Icon, Mono, Muted, Num, Row, Section, Skeleton, Spacer, State, TextLink, type Tone } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { VBars } from "../../charts";
import { useI18n } from "../../i18n";
import { PromoteButton } from "./PromoteDialog";
import { useRunbookScope } from "./RunbooksPage";
import { useRunbookSourceName } from "./NewRunbookPage";
import "../../ui/forms.css";
import "./knowledge.css";
import { confirmAction } from "../../ui/demo";
import { useWhen } from "../../ui/when";
import { UnknownValue } from "../../ui/unknown";

/** One step's result from POST /runbooks/{id}/dry-run; `reasons` lists the conditions it failed (PA-194). */
type DryRunCheck = { step: number; validated: boolean; reasons?: string[] };

/** Step numbers in order, three or more in a row as a range: [1, 2, 3, 5] → "1–3、5". */
function stepRuns(steps: number[], sep: string) {
  const sorted = [...new Set(steps)].sort((a, b) => a - b);
  const out: string[] = [];
  for (let i = 0; i < sorted.length; ) {
    let j = i;
    while (j + 1 < sorted.length && sorted[j + 1] === sorted[j] + 1) j++;
    if (j - i >= 2) out.push(`${sorted[i]}–${sorted[j]}`);
    else out.push(...sorted.slice(i, j + 1).map(String));
    i = j + 1;
  }
  return out.join(sep);
}

export default function RunbookEditorPage() {
  const { id = "rb-retry-storm" } = useParams();
  const { tx } = useI18n();
  const when = useWhen();
  const { data: rb } = useGet<Runbook>(`/runbooks/${id}`);
  const scopeLabel = useRunbookScope();
  // PA-166: a real record's base is "<source kind>:<source ref>"; a postmortem reads as its title.
  const sourceName = useRunbookSourceName(!!rb?.base?.startsWith("postmortem:"));
  const [accepted, setAccepted] = useState<"pending" | "yes" | "no">("pending");
  const [running, setRunning] = useState(false);
  const KIND: Record<RunbookStep["kind"], [Tone, string]> = { query: ["accent", tx("查询", "Query")], check: ["neutral", tx("判断", "Check")], action: ["warn", tx("动作", "Action")], verify: ["ok", tx("验证", "Verify")] };
  if (!rb)
    return (
      <Page crumbs={[tx("知识与记忆", "Knowledge"), "Runbook", "…"]}>
        <div className="v3-content">
          <Skeleton h={400} />
        </div>
      </Page>
    );
  // PA-197: the badge says this version's review state; only a draft can be submitted (the server answers 409 otherwise).
  const status = rb.status ?? "draft";
  const base = sourceName(rb.base);
  const states: Record<string, [Tone, string]> = {
    draft: ["accent", tx("{v} 草稿 · 基于 {b}", "{v} draft · based on {b}", { v: rb.version, b: base })],
    review: ["warn", tx("{v} 待复核 · 基于 {b}", "{v} in review · based on {b}", { v: rb.version, b: base })],
    published: ["ok", tx("{v} 已发布 · 基于 {b}", "{v} published · based on {b}", { v: rb.version, b: base })],
  };
  const stateBadge: [Tone, string] = states[status] ?? ["neutral", `${rb.version} · ${base}`];
  const submittable = status === "draft";
  // PA-194: which steps failed the check and why, from the server's per-step reasons. A runbook with an action step
  // never passes (by design), so an action step names only that; a step without a reason this page knows (an older
  // server) is still named.
  const dryRunFailure = (checks: DryRunCheck[] | undefined) => {
    const action: number[] = [];
    const writable: number[] = [];
    const unbound: number[] = [];
    const other: number[] = [];
    for (const c of checks ?? []) {
      if (c.validated) continue;
      const reasons = c.reasons ?? [];
      if (reasons.includes("action_step")) action.push(c.step);
      else {
        if (reasons.includes("not_readonly")) writable.push(c.step);
        if (reasons.includes("no_connector")) unbound.push(c.step);
        if (!reasons.includes("not_readonly") && !reasons.includes("no_connector")) other.push(c.step);
      }
    }
    const s = (steps: number[]) => ({ s: stepRuns(steps, tx("、", ", ")) });
    const parts = [
      action.length && tx("第 {s} 步是动作步骤，含动作步骤的 Runbook 不会通过只读校验", action.length > 1 ? "steps {s} are action steps, and a runbook with action steps never passes the read-only check" : "step {s} is an action step, and a runbook with action steps never passes the read-only check", s(action)),
      writable.length && tx("第 {s} 步没有标为只读", writable.length > 1 ? "steps {s} aren't marked read-only" : "step {s} isn't marked read-only", s(writable)),
      unbound.length && tx("第 {s} 步没有绑定连接器", unbound.length > 1 ? "steps {s} have no connector" : "step {s} has no connector", s(unbound)),
      other.length && tx("第 {s} 步不符合要求", other.length > 1 ? "steps {s} didn't pass" : "step {s} didn't pass", s(other)),
    ].filter((p): p is string => !!p);
    return parts.length
      ? tx("结构校验没有通过：{p}", "Structure check failed: {p}", { p: parts.join(tx("；", "; ")) })
      : tx("结构校验没有通过，请检查步骤类型、只读标记和连接器", "Structure check failed; review step kinds, read-only flags and connectors");
  };
  return (
    <Page
      flush
      crumbs={[tx("知识与记忆", "Knowledge"), "Runbook", rb.title]}
      actions={
        <>
          <Btn
            kind="ghost"
            icon="file"
            onClick={() =>
              void confirmAction({
                title: tx("预览 · {t}", "Preview · {t}", { t: rb.title }),
                desc: (
                  <ol className="rb-preview">
                    {rb.steps.map((s) => (
                      <li key={s.n}>
                        <b>{s.title}</b>
                        {s.body && <p>{s.body}</p>}
                        {s.code && <pre>{s.code}</pre>}
                      </li>
                    ))}
                  </ol>
                ),
                confirm: tx("关闭", "Close"),
              })
            }
          >
            {tx("预览", "Preview")}
          </Btn>
          <PromoteButton kind="runbooks" id={rb.id} title={rb.title} />
          <Btn
            kind="primary"
            icon="check"
            disabled={!submittable}
            title={submittable ? undefined : tx("只有草稿可以提交复核", "Only a draft can be submitted for review")}
            onClick={() =>
              // Say "submitted" only once the server accepted it; the sample data names its sample reviewer.
              api("POST", `/runbooks/${rb.id}/submit`).then(
                () => toast(API_MODE === "mock" ? tx("已提交复核，已通知王晨", "Submitted for review; Wang Chen notified") : tx("已提交复核", "Submitted for review")),
                (e) => toast(e instanceof ApiError ? e.message : tx("提交失败，请稍后重试", "Couldn't submit; try again later"), { tone: "danger" }),
              )
            }
          >
            {tx("提交复核", "Submit for review")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ padding: "18px 28px 28px", maxWidth: 980 }}>
            <Row gap={10}>
              <h1 style={{ fontSize: 19, fontWeight: 600 }}>{rb.title}</h1>
              <Badge tone={stateBadge[0]}>{stateBadge[1]}</Badge>
            </Row>
            <Row gap={6} wrap>
              <Muted>
                {tx("维护人", "Owner")} {rb.owner}
              </Muted>
              <Muted>·</Muted>
              <Muted>{scopeLabel(rb.scope)}</Muted>
              <Muted>·</Muted>
              {rb.tags.map((t) => (
                <Badge key={t}>{t}</Badge>
              ))}
            </Row>
            <Section title={tx("适用场景", "When to use")}>
              <p style={{ fontSize: "var(--fs-sm)", lineHeight: 1.6 }}>{rb.when}</p>
            </Section>
            <Section title={tx("排查与处理步骤", "Steps")}>
              <ol className="rb-steps">
                {rb.steps.map((s) => (
                  <li key={s.n} className="anim-rise">
                    <span className="n">{s.n}</span>
                    <Col gap={8} grow>
                      <Row gap={8}>
                        <Badge tone={KIND[s.kind][0]}>{KIND[s.kind][1]}</Badge>
                        <b style={{ fontSize: "var(--fs-body)" }}>{s.title}</b>
                        <Spacer />
                        {s.readonly && <Muted size={11.5}>{tx("只读 · Agent 可自动执行", "Read-only · agents may run it")}</Muted>}
                      </Row>
                      {s.code && (
                        <Row gap={10} align="flex-start">
                          {s.connectors?.map((c) => (
                            <Brand key={c} name={c} size={22} />
                          ))}
                          <Code style={{ flexGrow: 1 }}>{s.code}</Code>
                        </Row>
                      )}
                      {!s.code && s.connectors && (
                        <Row gap={8}>
                          {s.connectors.map((c) => (
                            <Brand key={c} name={c} size={22} />
                          ))}
                          <span style={{ fontSize: "var(--fs-sm)" }}>{s.body}</span>
                        </Row>
                      )}
                      {!s.connectors && s.body && <p style={{ fontSize: "var(--fs-sm)" }}>{s.body}</p>}
                      {s.suggestion && (
                        <Col gap={8}>
                          {accepted === "pending" ? (
                            <div className="rb-suggest">
                              <div className="bar">
                                <Icon name="sparkles" size={13} />
                                <span className="grow">{s.suggestion.source}</span>
                                <Btn kind="primary" icon="check" h={24} onClick={() => { setAccepted("yes"); toast(tx("已采纳 AI 建议", "AI suggestion accepted"), { undo: () => setAccepted("pending") }); }}>
                                  {tx("采纳", "Accept")}
                                </Btn>
                                <Btn kind="ghost" h={24} onClick={() => setAccepted("no")}>
                                  {tx("拒绝", "Reject")}
                                </Btn>
                              </div>
                              <div className="del">− {s.suggestion.minus}</div>
                              <div className="add">+ {s.suggestion.plus}</div>
                            </div>
                          ) : (
                            <p className="anim-fade" style={{ fontSize: "var(--fs-sm)" }}>
                              {accepted === "yes" ? s.suggestion.plus : s.suggestion.minus}
                            </p>
                          )}
                          <Row gap={8}>
                            <Badge tone="warn">
                              <Icon name="shield" size={11} />
                              {tx("需人工批准", "Needs approval")}
                            </Badge>
                            <Muted size={11.5}>{tx("由执行器执行 · Agent 只能生成提案", "Run by the executor · agents can only propose")}</Muted>
                          </Row>
                        </Col>
                      )}
                    </Col>
                  </li>
                ))}
              </ol>
            </Section>
          </div>
        </div>
        <aside className="aside" style={{ width: 320 }}>
          <Col gap={16} style={{ padding: 16 }}>
            <Section title={tx("复核", "Review")}>
              {rb.reviewers.map((r) => (
                <Row key={r.name} gap={8}>
                  <Avatar name={r.name} size={22} accent={r.state === "pending"} />
                  <span className="grow" style={{ fontSize: "var(--fs-sm)" }}>{r.name}</span>
                  {r.state === "pending" ? <State tone="warn">{tx("待复核", "Pending")}</State> : <Muted size={11.5}>{tx("可选", "Optional")}</Muted>}
                </Row>
              ))}
            </Section>
            <Section title={tx("版本", "Versions")}>
              {rb.versions.map((v, i) => (
                <Row key={v.v} gap={10}>
                  <Mono color={i === 0 ? "var(--text)" : undefined}>{v.v}</Mono>
                  <span className="grow" style={{ fontSize: "var(--fs-sm)" }}>{v.who}</span>
                  <Muted size={11.5}>{when.at(v.when)}</Muted>
                </Row>
              ))}
            </Section>
            <Section title={tx("使用情况", "Usage")}>
              <Row gap={6}>
                {rb.usage.refs == null ? <UnknownValue /> : <Num>{rb.usage.refs}</Num>}
                <Muted>{tx("次调查引用", "citations")}</Muted>
                <Spacer />
                {rb.usage.resolve_rate == null ? <UnknownValue /> : <Num>{rb.usage.resolve_rate}%</Num>}
                <Muted>{tx("解决率", "resolved")}</Muted>
              </Row>
              {/* PA-109: real records may have no weekly series yet; the INC-1042 caption belongs to the sample data. */}
              {rb.usage.weekly != null && (
                <>
                  <VBars values={rb.usage.weekly} height={44} color="var(--accent-line)" highlight={new Set(rb.usage.highlight == null ? [] : [rb.usage.highlight])} hiColor="var(--accent)" gap={4} />
                  <Muted size={11.5}>{API_MODE === "mock" ? tx("近 12 周 · 高亮为 INC-1042 所在周", "12 weeks · highlighted week of INC-1042") : tx("近 12 周", "Last 12 weeks")}</Muted>
                </>
              )}
            </Section>
            <Section title={tx("关联事件", "Incidents")}>
              {rb.incidents == null ? (
                <UnknownValue />
              ) : (
                rb.incidents.map((i) => (
                  <TextLink key={i.id} to={`/incidents/${i.id}`}>
                    {i.id} · {i.title}
                  </TextLink>
                ))
              )}
            </Section>
            <Section title={tx("试跑", "Dry run")}>
              {/* PA-112: the server only checks the saved steps (kinds, read-only flags, connectors) in the selected
                  environment; it reaches no system and runs nothing. The staging walkthrough is the sample data's. */}
              <Muted size={11.5}>
                {API_MODE === "mock"
                  ? tx("在预发用只读步骤走一遍，确认查询与判断条件仍然有效。", "Walk through the read-only steps on staging to confirm queries and checks still work.")
                  : tx("检查每一步的类型、只读标记和连接器是否齐全；不连接任何系统，也不执行步骤。", "Checks each step's kind, read-only flag and connectors; it reaches no system and runs no step.")}
              </Muted>
              <Btn
                icon="play"
                loading={running}
                onClick={async () => {
                  setRunning(true);
                  try {
                    const r = await api<{ steps?: number; ok?: boolean; mode?: string; checks?: DryRunCheck[] }>("POST", `/runbooks/${rb.id}/dry-run`);
                    if (API_MODE === "mock") {
                      setTimeout(() => {
                        setRunning(false);
                        toast(tx("试跑完成：3 个只读步骤全部通过", "Dry run finished: all 3 read-only steps passed"));
                      }, 900);
                      return;
                    }
                    // The server only validates the steps' structure; it doesn't run them against any system.
                    toast(
                      r.ok
                        ? tx("结构校验通过：{n} 个步骤（只检查步骤类型、只读标记和连接器，不实际执行）", "Structure check passed: {n} steps (kinds, read-only flags and connectors only; nothing was executed)", { n: r.steps ?? 0 })
                        : dryRunFailure(r.checks),
                      r.ok ? {} : { tone: "danger" },
                    );
                    setRunning(false);
                  } catch (e) {
                    setRunning(false);
                    toast(e instanceof ApiError ? e.message : tx("试跑失败，请稍后重试", "Dry run failed; try again later"), { tone: "danger" });
                  }
                }}
              >
                {API_MODE === "mock" ? tx("在预发试跑只读步骤", "Dry-run on staging") : tx("校验只读步骤", "Check the read-only steps")}
              </Btn>
            </Section>
          </Col>
        </aside>
      </div>
    </Page>
  );
}
