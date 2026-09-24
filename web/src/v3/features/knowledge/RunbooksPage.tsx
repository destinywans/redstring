import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { Kpi as KpiT, RunbookSuggestion, RunbookSummary } from "../../api/types";
import { Avatar, Badge, Bar, Btn, Card, Chip, Col, Icon, Mono, Muted, Num, Pills, Row, SearchBox, State, Strong, Tile, type IconName, type Tone } from "../../ui/core";
import { DataTable, Kpi, KpiRow } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { NewRunbookDialog } from "./NewRunbookPage";
import { PageHead } from "../conversation/ConversationsPage";
import "./knowledge.css";

export default function RunbooksPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [tab, setTab] = useState<"all" | "mine" | "review" | "stale">("all");
  const [q, setQ] = useState("");
  const [start, setStart] = useState<null | "postmortem" | "blank">(null);
  const [dismissed, setDismissed] = useState<Set<string>>(new Set());
  const { data, isLoading } = useGet<{ items: RunbookSummary[]; counts: Record<string, number>; kpis: KpiT[]; suggestions: RunbookSuggestion[] }>("/runbooks", { tab, q });
  const ST: Record<RunbookSummary["status"], [Tone, string]> = { published: ["ok", tx("已发布", "Published")], review: ["warn", tx("待复核", "In review")], draft: ["neutral", tx("草稿", "Draft")] };
  return (
    <Page
      flush
      crumbs={[tx("知识与记忆", "Knowledge"), "Runbook"]}
      actions={
        <>
          <Btn icon="sparkles" onClick={() => setStart("postmortem")}>
            {tx("从复盘生成", "Generate from postmortems")}
          </Btn>
          <Btn kind="primary" icon="plus" onClick={() => setStart("blank")}>
            {tx("新建 Runbook", "New runbook")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <PageHead title="Runbook" desc={tx("可执行的排障手册：步骤可以绑定连接器查询；Agent 调查时会召回并引用，结束后提出修订。", "Executable runbooks: steps can bind connector queries; agents recall and cite them during investigations and propose revisions afterwards.")} />
            <KpiRow>{data?.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} sub={k.sub} tone={k.tone} values={k.series.length ? k.series : undefined} />)}</KpiRow>
            <Row gap={8}>
              <Pills value={tab} onChange={setTab} options={[{ value: "all", label: tx("全部", "All"), count: data?.counts.all }, { value: "mine", label: tx("我维护的", "Mine"), count: data?.counts.mine }, { value: "review", label: tx("待复核", "In review"), count: data?.counts.review }, { value: "stale", label: tx("需要验证", "Needs verification"), count: data?.counts.stale }]} />
              <SearchBox placeholder={tx("搜索 Runbook", "Search runbooks")} value={q} onChange={setQ} width={220} />
              <Chip icon="filter" caret>
                {tx("服务", "Service")}
              </Chip>
              <Chip icon="filter" caret>
                {tx("团队", "Team")}
              </Chip>
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={data?.items}
                loading={isLoading}
                rowKey={(r) => r.id}
                onRowClick={(r) => nav(`/runbooks/${r.id}`)}
                rowHeight={52}
                columns={[
                  {
                    key: "t",
                    header: tx("标题", "Title"),
                    width: "29%",
                    render: (r) => (
                      <Row gap={10}>
                        <Icon name="book" size={15} color="var(--muted)" />
                        <Col gap={3} style={{ minWidth: 0 }}>
                          <span className="ellipsis" style={{ fontWeight: 500 }}>{r.title}</span>
                          <Row gap={4}>{r.tags.map((t) => <Badge key={t}>{t}</Badge>)}</Row>
                        </Col>
                      </Row>
                    ),
                  },
                  { key: "s", header: tx("适用范围", "Scope"), render: (r) => <Muted>{r.scope}</Muted> },
                  { key: "o", header: tx("维护人", "Owner"), render: (r) => <Row gap={6}><Avatar name={r.owner} size={20} /><Muted>{r.owner}</Muted></Row> },
                  { key: "v", header: tx("版本", "Version"), render: (r) => <Row gap={6}><Mono color="var(--text)">{r.version}</Mono><State tone={ST[r.status][0]}>{ST[r.status][1]}</State></Row> },
                  { key: "n", header: tx("近 30 天引用", "Cited (30d)"), sort: (a, b) => a.refs_30d - b.refs_30d, render: (r) => <Num>{r.refs_30d}</Num> },
                  { key: "p", header: tx("解决率", "Resolve rate"), sort: (a, b) => (a.resolve_rate ?? 0) - (b.resolve_rate ?? 0), render: (r) => (r.resolve_rate === undefined ? <Muted>—</Muted> : <Row gap={6}><Bar pct={r.resolve_rate} tone={r.resolve_rate >= 80 ? "ok" : "warn"} width={48} height={4} /><Num>{r.resolve_rate}%</Num></Row>) },
                  { key: "l", header: tx("最近验证", "Last verified"), render: (r) => (r.stale ? <Badge tone="warn"><Icon name="clock" size={11} />{r.last_verified}</Badge> : <Muted>{r.last_verified}</Muted>) },
                ]}
              />
            </Card>
          </div>
        </div>
        <aside className="aside" style={{ width: 340 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="sparkles" tone="accent" />
            <Col gap={0}>
              <Strong size={13.5}>{tx("AI 维护建议", "AI maintenance")}</Strong>
              <Muted size={11.5}>{tx("根据调查、复盘与仓库变化自动提出", "From investigations, postmortems and repo changes")}</Muted>
            </Col>
          </Row>
          <ul className="rb-suggestions">
            {data?.suggestions.filter((s) => !dismissed.has(s.id)).map((s) => (
              <li key={s.id} className="anim-rise">
                <Row gap={8}>
                  <Icon name={s.icon as IconName} size={15} color={`var(--${s.tone === "neutral" ? "muted" : s.tone === "accent" ? "accent-text" : s.tone})`} />
                  <b style={{ fontSize: 12.5 }}>{s.title}</b>
                </Row>
                <p className="muted" style={{ fontSize: 12, lineHeight: 1.5 }}>
                  {s.body}
                </p>
                <Row gap={6}>
                  {s.actions.map((a) => (
                    <Btn key={a.label} kind={a.kind} h={26} to={a.href} onClick={a.href ? undefined : () => { if (a.kind === "ghost") setDismissed((d) => new Set(d).add(s.id)); else toast(a.label); }}>
                      {a.label}
                    </Btn>
                  ))}
                </Row>
              </li>
            ))}
          </ul>
        </aside>
      </div>
      {start && <NewRunbookDialog open onClose={() => setStart(null)} initial={start} />}
    </Page>
  );
}
