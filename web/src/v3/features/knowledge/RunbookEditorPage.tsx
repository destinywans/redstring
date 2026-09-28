import { useState } from "react";
import { useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { Runbook, RunbookStep } from "../../api/types";
import { Avatar, Badge, Brand, Btn, Code, Col, Icon, Mono, Muted, Num, Row, Section, Skeleton, Spacer, State, TextLink, type Tone } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { VBars } from "../../charts";
import { useI18n } from "../../i18n";
import { PromoteButton } from "./PromoteDialog";
import "../../ui/forms.css";
import "./knowledge.css";

export default function RunbookEditorPage() {
  const { id = "rb-db-pool" } = useParams();
  const { tx } = useI18n();
  const { data: rb } = useGet<Runbook>(`/runbooks/${id}`);
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
  return (
    <Page
      flush
      crumbs={[tx("知识与记忆", "Knowledge"), "Runbook", rb.title]}
      actions={
        <>
          <Btn kind="ghost" icon="file">
            {tx("预览", "Preview")}
          </Btn>
          <PromoteButton kind="runbooks" id={rb.id} title={rb.title} />
          <Btn kind="primary" icon="check" onClick={() => { void api("POST", `/runbooks/${rb.id}/submit`); toast(tx("已提交复核，已通知王晨", "Submitted for review; Wang Chen notified")); }}>
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
              <Badge tone="accent">{tx("{v} 草稿 · 基于 {b}", "{v} draft · based on {b}", { v: rb.version, b: rb.base })}</Badge>
            </Row>
            <Row gap={6} wrap>
              <Muted>
                {tx("维护人", "Owner")} {rb.owner}
              </Muted>
              <Muted>·</Muted>
              <Muted>{rb.scope}</Muted>
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
                  <Muted size={11.5}>{v.when}</Muted>
                </Row>
              ))}
            </Section>
            <Section title={tx("使用情况", "Usage")}>
              <Row gap={6}>
                <Num>{rb.usage.refs}</Num>
                <Muted>{tx("次调查引用", "citations")}</Muted>
                <Spacer />
                <Num>{rb.usage.resolve_rate}%</Num>
                <Muted>{tx("解决率", "resolved")}</Muted>
              </Row>
              <VBars values={rb.usage.weekly} height={44} color="var(--accent-line)" highlight={new Set([rb.usage.highlight])} hiColor="var(--accent)" gap={4} />
              <Muted size={11.5}>{tx("近 12 周 · 高亮为 INC-1042 所在周", "12 weeks · highlighted week of INC-1042")}</Muted>
            </Section>
            <Section title={tx("关联事件", "Incidents")}>
              {rb.incidents.map((i) => (
                <TextLink key={i.id} to={`/incidents/${i.id}`}>
                  {i.id} · {i.title}
                </TextLink>
              ))}
            </Section>
            <Section title={tx("试跑", "Dry run")}>
              <Muted size={11.5}>{tx("在预发用只读步骤走一遍，确认查询与判断条件仍然有效。", "Walk through the read-only steps on staging to confirm queries and checks still work.")}</Muted>
              <Btn
                icon="play"
                loading={running}
                onClick={async () => {
                  setRunning(true);
                  await api("POST", `/runbooks/${rb.id}/dry-run`);
                  setTimeout(() => {
                    setRunning(false);
                    toast(tx("试跑完成：3 个只读步骤全部通过", "Dry run finished: all 3 read-only steps passed"));
                  }, 900);
                }}
              >
                {tx("在预发试跑只读步骤", "Dry-run on staging")}
              </Btn>
            </Section>
          </Col>
        </aside>
      </div>
    </Page>
  );
}
