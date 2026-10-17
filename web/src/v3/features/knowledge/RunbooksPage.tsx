import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, useGet } from "../../api/client";
import type { Kpi as KpiT, RunbookSuggestion, RunbookSummary } from "../../api/types";
import { Avatar, Badge, Bar, Btn, Card, Chip, Col, Icon, Mono, Muted, Num, Pills, Row, SearchBox, State, Strong, Tile, type IconName, type Tone } from "../../ui/core";
import { DataTable, Kpi, KpiRow } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import { NewRunbookDialog } from "./NewRunbookPage";
import { PageHead } from "../conversation/ConversationsPage";
import "./knowledge.css";
import { ChipSelect, useFacet } from "../../ui/demo";
import { PartialBanner, Unavailable, UnknownValue } from "../../ui/unknown";
import { useWhen } from "../../ui/when";
import { useNames } from "../../ui/names";

/**
 * A Runbook's scope with names (PA-111). The server sends "app:<application ID> · team:<team ID> · env:prod,staging"
 * ("workspace" instead of the application when it isn't tied to one); parts that can't be resolved stay as sent.
 */
export function useRunbookScope() {
  const { tx } = useI18n();
  const names = useNames();
  return (scope: string) =>
    scope
      .split(" · ")
      .map((part) => {
        if (part === "workspace") return tx("工作区", "Workspace");
        const [, kind, value] = /^(app|env):(.*)$/.exec(part) ?? [];
        if (kind === "app") return names.service(value) ?? part;
        if (kind === "env") {
          const labels = value.split(",").map((e) => names.env(e));
          return labels.every((x) => x !== null) ? labels.join(tx("、", ", ")) : part;
        }
        return part;
      })
      .join(" · ");
}

export default function RunbooksPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const [tab, setTab] = useState<"all" | "mine" | "review" | "stale">("all");
  const [q, setQ] = useState("");
  const [start, setStart] = useState<null | "postmortem" | "blank">(null);
  const [dismissed, setDismissed] = useState<Set<string>>(new Set());
  const { data, isLoading } = useGet<{ items: RunbookSummary[]; counts: Record<string, number>; kpis: KpiT[] | null; suggestions: RunbookSuggestion[] | null; unknown_fields?: string[] }>("/runbooks", { tab, q });
  const FIELD: Record<string, string> = { refs_30d: tx("近 30 天引用", "Cited (30d)"), resolve_rate: tx("解决率", "Resolve rate"), kpis: tx("统计", "Statistics"), suggestions: tx("AI 维护建议", "AI maintenance"), verification_provenance: tx("验证来源", "Verification source") };
  const missing = [...new Set((data?.unknown_fields ?? []).map((f) => FIELD[f] ?? f))];
  const scopeLabel = useRunbookScope();
  const fScope = useFacet(data?.items, (r) => r.scope);
  const fOwner = useFacet(data?.items, (r) => r.owner);
  const ST: Record<RunbookSummary["status"], [Tone, string]> = { published: ["ok", tx("已发布", "Published")], review: ["warn", tx("待复核", "In review")], draft: ["neutral", tx("草稿", "Draft")] };
  return (
    <Page
      flush
      crumbs={[tx("知识与记忆", "Knowledge"), "Runbook"]}
      actions={
        <>
          <Gate need={["knowledge", "manage"]}>
            <Btn icon="sparkles" onClick={() => setStart("postmortem")}>
              {tx("从复盘生成", "Generate from postmortems")}
            </Btn>
          </Gate>
          <Gate need={["knowledge", "manage"]}>
            <Btn kind="primary" icon="plus" onClick={() => setStart("blank")}>
              {tx("新建 Runbook", "New runbook")}
            </Btn>
          </Gate>
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <PageHead title="Runbook" desc={tx("可执行的排障手册：步骤可以绑定连接器查询；Agent 调查时会召回并引用，结束后提出修订。", "Executable runbooks: steps can bind connector queries; agents recall and cite them during investigations and propose revisions afterwards.")} />
            {data?.kpis && <KpiRow>{data.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} sub={k.sub} tone={k.tone} values={k.series?.length ? k.series : undefined} />)}</KpiRow>}
            <PartialBanner fields={missing} />
            <Row gap={8}>
              <Pills value={tab} onChange={setTab} options={[{ value: "all", label: tx("全部", "All"), count: data?.counts.all }, { value: "mine", label: tx("我维护的", "Mine"), count: data?.counts.mine }, { value: "review", label: tx("待复核", "In review"), count: data?.counts.review }, { value: "stale", label: tx("需要验证", "Needs verification"), count: data?.counts.stale }]} />
              <SearchBox placeholder={tx("搜索 Runbook", "Search runbooks")} value={q} onChange={setQ} width={220} />
              <ChipSelect icon="filter" label={tx("适用范围", "Scope")} {...fScope.props} options={fScope.props.options.map((o) => ({ ...o, label: scopeLabel(o.value) }))} />
              <ChipSelect icon="filter" label={tx("维护人", "Owner")} {...fOwner.props} />
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={data?.items.filter((r) => fScope.test(r) && fOwner.test(r))}
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
                  { key: "s", header: tx("适用范围", "Scope"), render: (r) => <Muted>{scopeLabel(r.scope)}</Muted> },
                  { key: "o", header: tx("维护人", "Owner"), render: (r) => <Row gap={6}><Avatar name={r.owner} size={20} /><Muted>{r.owner}</Muted></Row> },
                  { key: "v", header: tx("版本", "Version"), render: (r) => <Row gap={6}><Mono color="var(--text)">{r.version}</Mono><State tone={ST[r.status][0]}>{ST[r.status][1]}</State></Row> },
                  { key: "n", header: tx("近 30 天引用", "Cited (30d)"), sort: (a, b) => (a.refs_30d ?? -1) - (b.refs_30d ?? -1), render: (r) => (r.refs_30d == null ? <UnknownValue /> : <Num>{r.refs_30d}</Num>) },
                  { key: "p", header: tx("解决率", "Resolve rate"), sort: (a, b) => (a.resolve_rate ?? 0) - (b.resolve_rate ?? 0), render: (r) => (r.resolve_rate == null ? <UnknownValue /> : <Row gap={6}><Bar pct={r.resolve_rate} tone={r.resolve_rate >= 80 ? "ok" : "warn"} width={48} height={4} /><Num>{r.resolve_rate}%</Num></Row>) },
                  { key: "l", header: tx("最近验证", "Last verified"), render: (r) => (r.last_verified == null ? <UnknownValue /> : r.stale ? <Badge tone="warn"><Icon name="clock" size={11} />{when.date(r.last_verified)}</Badge> : <Muted>{when.date(r.last_verified)}</Muted>) },
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
          {data && data.suggestions === null && <Unavailable reason="not_generated" icon="sparkles" title={tx("AI 维护建议尚未生成", "No AI maintenance suggestions yet")} desc={tx("有新的调查、复盘或仓库变化后，AI 会在这里提出修订建议。", "Suggestions appear here after new investigations, postmortems or repository changes.")} />}
          <ul className="rb-suggestions">
            {data?.suggestions?.filter((s) => !dismissed.has(s.id)).map((s) => (
              <li key={s.id} className="anim-rise">
                <Row gap={8}>
                  <Icon name={s.icon as IconName} size={15} color={`var(--${s.tone === "neutral" ? "muted" : s.tone === "accent" ? "accent-text" : s.tone})`} />
                  <b style={{ fontSize: "var(--fs-sm)" }}>{s.title}</b>
                </Row>
                <p className="muted" style={{ fontSize: "var(--fs-sub)", lineHeight: 1.5 }}>
                  {s.body}
                </p>
                <Row gap={6}>
                  {s.actions.map((a) => (
                    <Btn key={a.label} kind={a.kind} h={26} to={a.href} onClick={a.href ? undefined : () => { if (a.kind === "ghost") setDismissed((d) => new Set(d).add(s.id)); else if (API_MODE === "mock") toast(a.label); }}>
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
