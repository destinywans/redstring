import { useState } from "react";
import { useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { ROLLBACK_KIND, type Execution, type ExecutionDetail } from "../../api/types";
import { RolloutExecutionPage } from "./RolloutExecution";
import { useWhen } from "./rollback";
import { PartialBanner, Unavailable, UnknownValue } from "../../ui/unknown";
import { Badge, Btn, Card, CardHead, Col, Icon, Mono, Muted, Row, Sev, Skeleton, Spacer, Textarea, Tile, type IconName } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import "./events.css";
import { downloadFile } from "../../ui/demo";

export default function ExecutionPage() {
  const { id = "" } = useParams();
  // Rollback executions carry `kind`; the older projections don't (docs/api/v3-rollback.md §4). Same cache key as the pages below.
  const { data } = useGet<{ kind?: string }>(id ? `/executions/${id}` : null);
  if (data?.kind === ROLLBACK_KIND) return <RolloutExecutionPage id={id} />;
  return API_MODE === "mock" ? <SampleExecutionPage /> : <RealExecutionPage />;
}

/** Real execution record: persisted events, recovery summary and closed report; nothing is filled in from samples. */
function RealExecutionPage() {
  const { id = "" } = useParams();
  const { tx } = useI18n();
  // Times in the app's language, like the rest of the product (PA-74).
  const when = useWhen();
  const fmtTime = (t: string | null) => when.at(t) ?? "—";
  const { data: e, error, refetch } = useGet<ExecutionDetail>(id ? `/executions/${id}` : null);
  const [publishing, setPublishing] = useState(false);
  if (!e)
    return (
      <Page crumbs={[tx("事件收件箱", "Inbox"), tx("执行与报告", "Execution & report")]} error={error} errorSource={tx("执行记录", "execution records")}>
        <div className="v3-content">
          <Skeleton h={300} />
        </div>
      </Page>
    );
  const STATUS = {
    queued: ["neutral", tx("排队中", "Queued")],
    executing: ["accent", tx("执行中", "Executing")],
    verifying: ["accent", tx("验证中", "Verifying")],
    succeeded: ["ok", tx("已完成", "Succeeded")],
    failed: ["danger", tx("失败", "Failed")],
    needs_attention: ["warn", tx("需要人工处理", "Needs attention")],
  } as const;
  const DECISION = { pending: ["neutral", tx("待批准", "Pending")], approved: ["ok", tx("已批准", "Approved")], rejected: ["danger", tx("已驳回", "Rejected")], revoked: ["warn", tx("已撤回", "Revoked")] } as const;
  const o = e.observation;
  const r = e.report;
  const canPublish = !!r && !r.published && e.status === "succeeded" && o?.recovered === true;
  const publish = async () => {
    setPublishing(true);
    try {
      await api("POST", `/executions/${e.id}/report/publish`);
      void refetch();
      toast(tx("报告已发布", "Report published"));
    } catch (err) {
      toast(err instanceof ApiError ? err.message : tx("发布失败，请稍后重试", "Couldn't publish; try again later"), { tone: "danger" });
    } finally {
      setPublishing(false);
    }
  };
  return (
    <Page
      crumbs={[tx("事件收件箱", "Inbox"), { label: tx("调查", "Investigation"), to: `/canvas/${e.investigation_id}` }, tx("执行与报告", "Execution & report")]}
      actions={
        <>
          <Badge tone={STATUS[e.status][0]}>{STATUS[e.status][1]}</Badge>
          {o?.recovered === true && (
            <Badge tone="ok">
              <Icon name="check" size={11} />
              {tx("已验证恢复", "Recovery verified")}
            </Badge>
          )}
          <Btn kind="ghost" icon="shield" to="/settings/audit">
            {tx("审计记录", "Audit trail")}
          </Btn>
        </>
      }
    >
      {e.missing.length > 0 && (
        <div style={{ padding: "12px 24px 0" }}>
          <PartialBanner fields={e.missing} />
        </div>
      )}
      <div className="exec-layout">
        <Col gap={16} style={{ width: 480, flex: "none" }}>
          <Card className="anim-rise">
            <CardHead title={tx("执行记录", "Execution")} right={<Mono>{e.id}</Mono>} />
            <Col gap={10} style={{ padding: "12px 16px" }}>
              <Row gap={8}>
                <Muted>{tx("批准", "Approval")}</Muted>
                <Badge tone={DECISION[e.approval.decision][0]}>{DECISION[e.approval.decision][1]}</Badge>
                {e.approval.approved_by && <Muted>{e.approval.approved_by}</Muted>}
                {!e.approval.digest_match && <Badge tone="danger">{tx("动作摘要不一致", "Action digest mismatch")}</Badge>}
                <Spacer />
                <Muted size={11.5}>{tx("第 {n} 次尝试", "Attempt {n}", { n: e.attempt })}</Muted>
              </Row>
              {e.error && <div className="danger-note"><Icon name="alert" size={13} />{e.error}</div>}
            </Col>
            <ol className="exec-steps">
              {e.steps.map((s, i) => (
                <li key={s.seq}>
                  <span className="tnum muted" style={{ width: 58, flex: "none", fontSize: "var(--fs-sub)", paddingTop: 2 }}>
                    {when.clock(s.time)}
                  </span>
                  <span className="rail">
                    <Tile icon="check" size={22} />
                    {i < e.steps.length - 1 && <span className="line" />}
                  </span>
                  <Col gap={0} style={{ paddingBottom: 12 }}>
                    <span className="mono" style={{ fontSize: "var(--fs-sm)" }}>{s.type}</span>
                  </Col>
                </li>
              ))}
            </ol>
            {!e.steps.length && <Muted size={11.5} style={{ padding: "0 16px 12px", display: "block" }}>{tx("还没有执行事件", "No execution events yet")}</Muted>}
            {e.steps_truncated && <Muted size={11.5} style={{ padding: "0 16px 12px", display: "block" }}>{tx("只显示前 100 条事件", "Showing the first 100 events")}</Muted>}
          </Card>
          <Card className="anim-rise">
            <CardHead title={tx("恢复观测", "Recovery check")} />
            {!o ? (
              <Unavailable reason="not_generated" title={tx("还没有恢复观测", "No recovery check yet")} desc={tx("执行完成后会记录恢复验证的结果。", "The recovery check is recorded after the execution finishes.")} icon="activity" />
            ) : (
              <Col gap={8} style={{ padding: "12px 16px" }}>
                <Row gap={8}>
                  <Muted>{tx("结果", "Result")}</Muted>
                  <Spacer />
                  {o.recovered === null ? <UnknownValue reason="unavailable" /> : o.recovered ? <Badge tone="ok">{tx("已恢复", "Recovered")}</Badge> : <Badge tone="danger">{tx("未恢复", "Not recovered")}</Badge>}
                </Row>
                <Row gap={8}>
                  <Muted>{tx("观测窗口", "Window")}</Muted>
                  <Spacer />
                  <Muted>{fmtTime(o.started_at)} – {fmtTime(o.finished_at)}</Muted>
                </Row>
                <Row gap={8}>
                  <Muted>{tx("样本 / 请求", "Samples / requests")}</Muted>
                  <Spacer />
                  <Mono color="var(--text)">{o.samples ?? "—"} / {o.requests ?? "—"}</Mono>
                </Row>
                {o.reason && <Muted size={11.5}>{o.reason}</Muted>}
              </Col>
            )}
          </Card>
        </Col>
        <Card className="report-card anim-rise">
          <header className="report-head">
            <Icon name="file" size={15} color="var(--muted)" />
            <h2>{tx("故障报告", "Incident report")}</h2>
            {r?.published ? (
              <Badge tone="ok">
                <Icon name="check" size={11} />
                {tx("已发布", "Published")}
              </Badge>
            ) : r ? (
              <Badge tone="accent">{tx("待发布", "Not published")}</Badge>
            ) : null}
            <Spacer />
            {r && (
              <Btn
                kind="ghost"
                icon="download"
                onClick={() => {
                  downloadFile(`${e.id}-report.md`, [`# ${r.title}`, "", r.summary, ""].join("\n"), "text/markdown");
                  toast(tx("已导出 Markdown", "Exported Markdown"));
                }}
              >
                {tx("导出 Markdown", "Export Markdown")}
              </Btn>
            )}
            {r && !r.published && (
              <Btn kind="primary" icon="check" disabled={!canPublish} loading={publishing} onClick={() => void publish()}>
                {tx("确认发布", "Publish")}
              </Btn>
            )}
          </header>
          {!r ? (
            <Unavailable reason="not_generated" title={tx("报告尚未生成", "No report yet")} desc={tx("调查关闭后会生成故障报告，再在这里确认发布。", "A report is created when the investigation closes; publish it here.")} icon="file" />
          ) : (
            <article className="report-doc">
              <h1>{r.title}</h1>
              <Muted>
                {tx("{c} 关闭于 {t} · 批准人 {a}", "Closed by {c} at {t} · approved by {a}", { c: r.closed_by, t: fmtTime(r.closed_at), a: r.approved_by })}
                {r.published && r.published_at && tx(" · {p} 发布于 {t}", " · published by {p} at {t}", { p: r.published_by ?? "—", t: fmtTime(r.published_at) })}
              </Muted>
              <h3>{tx("概要", "Summary")}</h3>
              <Markdown text={r.summary} />
              {!canPublish && !r.published && <Muted size={11.5}>{tx("执行成功且恢复验证通过后才能发布。", "Publishing needs a succeeded execution with a verified recovery.")}</Muted>}
            </article>
          )}
        </Card>
      </div>
    </Page>
  );
}

/** The sample execution report: every field is filled sample data (mock mode only). */
function SampleExecutionPage() {
  const { id = "EXE-51" } = useParams();
  const { tx } = useI18n();
  const { data: e } = useGet<Execution>(`/executions/${id}`);
  const [editing, setEditing] = useState(false);
  const [published, setPublished] = useState(false);
  const [done, setDone] = useState<Set<number>>(new Set([2]));
  const [impact, setImpact] = useState<string | null>(null);
  if (!e)
    return (
      <Page crumbs={[tx("事件收件箱", "Inbox"), "…"]}>
        <div className="v3-content">
          <Skeleton h={300} />
        </div>
      </Page>
    );
  const r = e.report;
  return (
    <Page
      crumbs={[tx("事件收件箱", "Inbox"), { label: tx("库存调用重试降为 1 次", "Retry inventory calls once"), to: "/review/PRP-88" }, tx("执行与报告", "Execution & report")]}
      actions={
        <>
          <Badge tone="ok">
            <Icon name="check" size={11} />
            {tx("已验证恢复", "Recovery verified")}
          </Badge>
          <Btn kind="ghost" icon="shield" to="/settings/audit">
            {tx("审计记录", "Audit trail")}
          </Btn>
        </>
      }
    >
      <div className="exec-layout">
        <Col gap={16} style={{ width: 480, flex: "none" }}>
          <Card className="anim-rise">
            <CardHead title={tx("执行记录", "Execution")} right={<Mono>exec_231_1</Mono>} />
            <ol className="exec-steps">
              {e.steps.map((s, i) => (
                <li key={s.time} style={{ animationDelay: `${i * 80}ms` }}>
                  <span className="tnum muted" style={{ width: 58, flex: "none", fontSize: "var(--fs-sub)", paddingTop: 2 }}>
                    {s.time}
                  </span>
                  <span className="rail">
                    <Tile icon={s.icon as IconName} tone={s.tone} size={22} />
                    {i < e.steps.length - 1 && <span className="line" />}
                  </span>
                  <Col gap={0} style={{ paddingBottom: 12 }}>
                    <span style={{ fontWeight: 500, fontSize: "var(--fs-sm)" }}>{s.title}</span>
                    <Muted>{s.desc}</Muted>
                  </Col>
                </li>
              ))}
            </ol>
          </Card>
          <Card className="anim-rise">
            <CardHead title={tx("恢复观测 · 5xx 比例", "Recovery · 5xx ratio")} right={<Muted size={11.5}>{tx("阈值 0.5%", "Threshold 0.5%")}</Muted>} />
            <Col gap={12} style={{ padding: "14px 16px" }}>
              <Lines
                series={[{ name: "5xx", values: e.observation.values, color: "var(--c3)", fill: "var(--c3s)" }]}
                height={120}
                max={10}
                threshold={e.observation.threshold}
                bands={[{ from: e.observation.band[0], to: e.observation.band[1], color: "var(--ok-soft)" }]}
                markers={[{ at: e.observation.marker, label: tx("更新完成", "Rolled out"), tone: "ok" }]}
                labels={e.observation.labels}
                format={(v) => `${v}%`}
                grid={2}
                axis={["14:16", tx("14:19 批准", "14:19 approved"), tx("14:21 更新完成", "14:21 rolled out"), "14:24:40"]}
              />
              <ul className="v3-col" style={{ gap: 6 }}>
                {e.observation.checks.map((c) => (
                  <li key={c.text} className="v3-row" style={{ gap: 8, fontSize: "var(--fs-sm)" }}>
                    <Icon name="check" size={14} color="var(--ok)" />
                    <span className="grow">{c.text}</span>
                    <Mono color="var(--text)">{c.value}</Mono>
                  </li>
                ))}
              </ul>
            </Col>
          </Card>
        </Col>
        <Card className="report-card anim-rise">
          <header className="report-head">
            <Icon name="file" size={15} color="var(--muted)" />
            <h2>{tx("故障报告", "Incident report")}</h2>
            {published ? (
              <Badge tone="ok">
                <Icon name="check" size={11} />
                {tx("已发布", "Published")}
              </Badge>
            ) : (
              <Badge tone="accent">
                <Icon name="sparkles" size={11} />
                {tx("AI 起草 · 待你确认", "AI draft · awaiting you")}
              </Badge>
            )}
            <Spacer />
            <Btn kind="ghost" icon={editing ? "check" : "edit"} onClick={() => setEditing((x) => !x)}>
              {editing ? tx("完成", "Done") : tx("编辑", "Edit")}
            </Btn>
            <Btn kind="ghost" icon="download" onClick={() => {
                const md = [`# ${r.title}`, "", `${r.severity} · ${r.meta}`, "", `## ${tx("摘要", "Summary")}`, r.summary, "", `## ${tx("影响", "Impact")}`, r.impact, "", `## ${tx("时间线", "Timeline")}`, ...r.timeline.map((t) => `- ${t.time} ${t.text}`), "", `## ${tx("改进项", "Action items")}`, ...r.actions.map((a) => `- [${a.done ? "x" : " "}] ${a.text} (${a.owner}, ${a.due})`), "", r.refs].join("\n");
                downloadFile(`${e.id}-report.md`, md, "text/markdown");
                toast(tx("已导出 Markdown", "Exported Markdown"));
              }}>
              {tx("导出 Markdown", "Export Markdown")}
            </Btn>
            <Btn icon="send" onClick={() => toast(tx("已推送飞书 #trade-incident", "Posted to Feishu #trade-incident"))}>
              {tx("推送飞书", "Post to Feishu")}
            </Btn>
            <Btn
              kind="primary"
              icon="check"
              disabled={published}
              onClick={async () => {
                await api("POST", `/executions/${e.id}/report/publish`);
                setPublished(true);
                toast(tx("报告已发布，并同步到复盘报告", "Report published and added to postmortems"));
              }}
            >
              {tx("确认发布", "Publish")}
            </Btn>
          </header>
          <article className="report-doc">
            <h1>{r.title}</h1>
            <Row gap={8} style={{ marginTop: 8 }}>
              <Sev p={r.severity} />
              <Muted>{r.meta}</Muted>
            </Row>
            <h3>{tx("概要", "Summary")}</h3>
            <Markdown text={r.summary} />
            <h3>{tx("影响", "Impact")}</h3>
            {editing ? (
              <Textarea value={impact ?? r.impact} onChange={(ev) => setImpact(ev.target.value)} style={{ minHeight: 60 }} />
            ) : (
              <p className="impact">
                {(impact ?? r.impact).split(/(\[[^\]]+\])/).map((seg, i) =>
                  seg.startsWith("[") ? (
                    <span key={i} className="placeholder" title={tx("待确认的占位内容", "Placeholder to confirm")}>
                      {seg}
                    </span>
                  ) : (
                    seg
                  ),
                )}
              </p>
            )}
            <h3>{tx("时间线", "Timeline")}</h3>
            <table className="v3-table head-bg report-table">
              <thead>
                <tr>
                  <th style={{ width: 80 }}>{tx("时间", "Time")}</th>
                  <th>{tx("事件", "Event")}</th>
                </tr>
              </thead>
              <tbody>
                {r.timeline.map((t) => (
                  <tr key={t.time} style={{ height: 32 }}>
                    <td className="mono">{t.time}</td>
                    <td>{t.text}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <h3>{tx("后续改进", "Action items")}</h3>
            <ul className="v3-col" style={{ gap: 2 }}>
              {r.actions.map((a, i) => (
                <li key={a.text} className="v3-row" style={{ gap: 8, fontSize: "var(--fs-sm)", padding: "3px 0" }}>
                  <input
                    type="checkbox"
                    aria-label={a.text}
                    checked={done.has(i)}
                    onChange={() =>
                      setDone((s) => {
                        const n = new Set(s);
                        if (n.has(i)) n.delete(i);
                        else n.add(i);
                        return n;
                      })
                    }
                    style={{ accentColor: "var(--accent)" }}
                  />
                  <span className="grow" style={{ textDecoration: done.has(i) ? "line-through" : undefined, color: done.has(i) ? "var(--muted)" : undefined }}>
                    {a.text}
                  </span>
                  <Badge>{a.owner}</Badge>
                  <Muted style={{ width: 40, textAlign: "right" }}>{a.due}</Muted>
                </li>
              ))}
            </ul>
            <Muted style={{ display: "block", marginTop: 14 }}>{r.refs}</Muted>
          </article>
        </Card>
      </div>
    </Page>
  );
}
