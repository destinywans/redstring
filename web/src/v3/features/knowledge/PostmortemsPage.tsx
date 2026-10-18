import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, useGet } from "../../api/client";
import type { PostmortemSummary } from "../../api/types";
import { Badge, Btn, Card, Chip, Col, Icon, Muted, Num, Panel, Pills, Row, SearchBox, Section, Sev, State, TextLink, Spacer } from "../../ui/core";
import { DataTable, Kpi, KpiRow } from "../../ui/data";
import { Donut, Treemap } from "../../charts";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import { PageHead } from "../conversation/ConversationsPage";
import { ChipSelect, useFacet } from "../../ui/demo";
import { Unavailable, UnknownValue } from "../../ui/unknown";
import { useWhen } from "../../ui/when";

export default function PostmortemsPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const [tab, setTab] = useState<"all" | "draft" | "published">("all");
  const [q, setQ] = useState("");
  const [done, setDone] = useState<Set<number>>(new Set());
  const { data, isLoading, error } = useGet<PostmortemSummary>("/postmortems", { tab });
  const fCause = useFacet(data?.items, (p) => p.cause);
  const fSvc = useFacet(data?.items, (p) => p.title.split(" ")[0]);
  const rows = (data?.items ?? []).filter((p) => (!q || p.title.toLowerCase().includes(q.toLowerCase())) && fCause.test(p) && fSvc.test(p));
  return (
    <Page crumbs={[tx("知识与记忆", "Knowledge"), tx("复盘报告", "Postmortems")]} actions={<Gate need={["knowledge", "manage"]}><Btn kind="primary" icon="plus" to="/postmortems/new">{tx("新建复盘", "New postmortem")}</Btn></Gate>}>
      <div className="v3-content">
        <PageHead title={tx("复盘报告", "Postmortems")} desc={tx("AI 根据事件时间线和证据起草，负责人确认后发布；改进项会一直跟踪到完成。", "AI drafts from the incident timeline and evidence; the owner publishes; action items are tracked to completion.")} />
        <KpiRow>{data?.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone} values={k.series?.length ? k.series : undefined} />)}</KpiRow>
        <Row gap={8}>
          <Pills value={tab} onChange={setTab} options={[{ value: "all", label: tx("全部", "All"), count: API_MODE === "mock" ? 57 : undefined }, { value: "draft", label: tx("AI 草稿", "AI drafts"), count: API_MODE === "mock" ? 2 : undefined }, { value: "published", label: tx("已发布", "Published"), count: API_MODE === "mock" ? 55 : undefined }]} />
          <SearchBox placeholder={tx("搜索报告", "Search reports")} value={q} onChange={setQ} width={220} />
          <ChipSelect icon="filter" label={tx("根因类别", "Cause")} {...fCause.props} />
          <ChipSelect icon="filter" label={tx("服务", "Service")} {...fSvc.props} />
        </Row>
        <div className="v3-row" style={{ gap: 16, alignItems: "flex-start" }}>
          <Card style={{ overflow: "hidden", flex: "1 1 0", minWidth: 0 }}>
            <DataTable
              rows={data ? rows : undefined}
              loading={isLoading}
              error={error}
              rowKey={(r) => r.id}
              // Sample rows open the sample execution report; real rows open their incident (there is no postmortem detail page yet).
              onRowClick={(r) => (API_MODE === "mock" ? nav("/executions/EXE-51") : r.incident_id && nav(`/incidents/${r.incident_id}`))}
              rowHeight={44}
              columns={[
                { key: "t", header: tx("标题", "Title"), width: "34%", render: (r) => <Row gap={10}><Icon name="file" size={15} color="var(--muted)" /><span className="ellipsis" style={{ fontWeight: 500 }}>{r.title}</span></Row> },
                { key: "s", header: tx("等级", "Sev"), render: (r) => (r.severity ? <Sev p={r.severity} /> : <UnknownValue />) },
                { key: "d", header: tx("日期", "Date"), render: (r) => <Muted>{when.date(r.date)}</Muted> },
                { key: "du", header: tx("持续", "Duration"), render: (r) => (r.duration ? <Muted>{r.duration}</Muted> : <UnknownValue />) },
                { key: "c", header: tx("根因类别", "Cause"), render: (r) => <Badge>{r.cause}</Badge> },
                { key: "a", header: tx("改进项", "Actions"), render: (r) => <Num>{r.actions}</Num> },
                { key: "st", header: tx("状态", "Status"), render: (r) => (r.status === "draft" ? <Badge tone="accent"><Icon name="sparkles" size={11} />{tx("AI 草稿", "AI draft")}</Badge> : <State tone="ok">{tx("已发布", "Published")}</State>) },
              ]}
            />
          </Card>
          <Col gap={16} style={{ width: 360, flex: "none" }}>
            <Panel title={tx("根因分布 · 近 90 天", "Root causes · 90 days")}>
              {data && !data.causes && !data.impact_by_service && <Unavailable reason="not_generated" icon="trend" title={tx("暂无根因统计", "No root-cause statistics yet")} desc={tx("复盘里还没有可统计的根因分类和影响时长。", "Postmortems don't have countable cause categories or impact minutes yet.")} />}
              {data && (data.causes || data.impact_by_service) && (
                <Col gap={4}>
                  {data.causes ? (
                  <Row gap={14}>
                    <Donut parts={data.causes.map((c) => ({ value: c.pct, color: c.color, label: c.label }))} size={112} thick={16} sub={tx("根因占比", "share of causes")} />
                    <Col gap={3} grow>
                      {data.causes.map((c) => (
                        <Row key={c.label} gap={6} style={{ fontSize: "var(--fs-sub)" }}>
                          <i style={{ width: 8, height: 8, borderRadius: 2, background: c.color }} />
                          <span className="grow">{c.label}</span>
                          <Num>{c.pct}%</Num>
                        </Row>
                      ))}
                    </Col>
                  </Row>
                  ) : (
                    <Muted>{tx("根因分布：", "Root causes: ")}<UnknownValue reason="not_generated" /></Muted>
                  )}
                  <Section title={tx("影响时长 · 按服务", "Impact minutes · by service")} style={{ marginTop: 10 }}>
                    {data.impact_by_service ? <Treemap items={data.impact_by_service} height={72} /> : <UnknownValue reason="not_generated" />}
                  </Section>
                </Col>
              )}
            </Panel>
            <Panel title={tx("需要跟进的改进项", "Follow-ups")} right={data?.followups ? <Muted size={11.5}>{tx("最紧急的 {n} 项", "Most urgent {n}", { n: data.followups.length })}</Muted> : undefined}>
              {data && !data.followups && <Unavailable reason="not_generated" icon="check" title={tx("暂无改进项", "No follow-ups yet")} desc={tx("复盘发布后，这里列出还没完成的改进项。", "Open follow-ups appear here once postmortems are published.")} />}
              <ul>
                {data?.followups?.map((f, i) => (
                  <li key={f.text} className="v3-row" style={{ gap: 8, padding: "7px 0", borderBottom: "1px solid var(--border)", fontSize: "var(--fs-sm)" }}>
                    <input type="checkbox" aria-label={f.text} checked={done.has(i)} onChange={() => setDone((s) => { const n = new Set(s); if (n.has(i)) n.delete(i); else n.add(i); return n; })} style={{ accentColor: "var(--accent)", margin: 0 }} />
                    <span className="ellipsis grow" style={{ textDecoration: done.has(i) ? "line-through" : undefined }}>{f.text}</span>
                    <Badge>{f.owner}</Badge>
                    <Badge tone={done.has(i) ? "ok" : f.tone}>{done.has(i) ? tx("完成", "Done") : f.due}</Badge>
                  </li>
                ))}
              </ul>
              <Spacer />
            </Panel>
          </Col>
        </div>
      </div>
    </Page>
  );
}
