import { useState } from "react";
import { useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { Execution } from "../../api/types";
import { Badge, Btn, Card, CardHead, Col, Icon, Mono, Muted, Row, Sev, Skeleton, Spacer, Textarea, Tile, type IconName } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import "./events.css";

export default function ExecutionPage() {
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
      crumbs={[tx("事件收件箱", "Inbox"), { label: tx("回滚 checkout-api 连接池配置", "Roll back checkout-api pool config"), to: "/review/PRP-88" }, tx("执行与报告", "Execution & report")]}
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
            <Btn kind="ghost" icon="download" onClick={() => toast(tx("已导出 Markdown", "Exported Markdown"))}>
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
