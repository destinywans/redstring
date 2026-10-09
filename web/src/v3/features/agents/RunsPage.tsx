import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, useGet } from "../../api/client";
import type { RunRow, RunsView } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, Legend, Mono, Muted, Num, Pills, Row, SearchBox, Skeleton, Spacer, State, Strong, TextLink, type IconName } from "../../ui/core";
import { DataTable, Kpi, KpiRow } from "../../ui/data";
import { StackedArea, VBars } from "../../charts";
import { useI18n } from "../../i18n";
import "./agents.css";
import { ChipSelect, RangeChip, openExternal, useFacet } from "../../ui/demo";
import { Unavailable, UnknownValue } from "../../ui/unknown";

export default function RunsPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [filter, setFilter] = useState<"all" | "failed" | "blocked" | "negative">("all");
  const [q, setQ] = useState("");
  const { data } = useGet<RunsView>("/agent-runs", { filter });
  const st = (s: RunRow["status"]) => ({ success: [tx("成功", "Success"), "ok"], failed: [tx("失败", "Failed"), "danger"], blocked: [tx("已拦截", "Blocked"), "warn"] })[s] as [string, "ok" | "danger" | "warn"];
  const fAgent = useFacet(data?.items, (r) => r.agent);
  const fVer = useFacet(data?.items, (r) => r.version);
  const fTrig = useFacet(data?.items, (r) => r.trigger);
  const rows = data?.items.filter((r) => (!q || r.id.includes(q) || r.scope.includes(q)) && fAgent.test(r) && fVer.test(r) && fTrig.test(r));
  return (
    <Page
      crumbs={[tx("Agent 运行台", "Agent console"), tx("运行与 Trace", "Runs & traces")]}
      actions={
        <>
          <RangeChip />
          <Btn icon="external" onClick={() => openExternal("Langfuse", "https://langfuse.example.internal/project/aiops/traces")}>{tx("在 Langfuse 中打开", "Open in Langfuse")}</Btn>
        </>
      }
    >
      <div className="v3-content" style={{ padding: "16px 20px" }}>
        <KpiRow>{data ? data.kpis.map((k) => <Kpi key={k.id} small label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone} values={k.series?.length ? k.series : undefined} />) : Array.from({ length: 6 }, (_, i) => <Skeleton key={i} h={90} style={{ flex: 1 }} />)}</KpiRow>
        <Card style={{ padding: "12px 14px" }}>
          <Row gap={22} align="flex-start">
            <Col gap={8} style={{ flex: "1.7 1 0", minWidth: 0 }}>
              <Row gap={8}>
                <Strong>{tx("运行趋势 · 最近 24 小时", "Runs · last 24 h")}</Strong>
                <Spacer />
                <Legend items={[[tx("成功", "Success"), "var(--c2)"], [tx("失败", "Failed"), "var(--c3)"], [tx("拦截", "Blocked"), "var(--c4)"]]} />
              </Row>
              {data && !data.trend ? <Unavailable reason="not_generated" icon="trend" title={tx("暂无运行趋势", "No run trend yet")} desc={tx("有 Agent 运行记录后显示最近 24 小时的成功、失败和拦截。", "Successes, failures and blocks appear once agents have run.")} /> : data?.trend ? <StackedArea series={[data.trend.ok, data.trend.failed, data.trend.blocked]} colors={["var(--c2)", "var(--c3)", "var(--c4)"]} height={78} names={[tx("成功", "Success"), tx("失败", "Failed"), tx("拦截", "Blocked")]} labels={data.trend.ok.map((_, i) => `${String(i).padStart(2, "0")}:00`)} /> : <Skeleton h={78} />}
            </Col>
            <Col gap={8} style={{ flex: "1 1 0", minWidth: 0 }}>
              <Row gap={8}>
                <Strong>{tx("耗时分布", "Duration distribution")}</Strong>
                <Spacer />
              </Row>
              {data && !data.durations ? <UnknownValue reason="not_generated" /> : data?.durations ? <VBars values={data.durations} height={60} /> : <Skeleton h={60} />}
              <Row gap={0}>
                <Muted size={10.5}>0 s</Muted>
                <Spacer />
                <Muted size={10.5}>60 s</Muted>
                <Spacer />
                <Muted size={10.5}>120 s+</Muted>
              </Row>
            </Col>
          </Row>
        </Card>
        <Row gap={8}>
          <Pills
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: tx("全部", "All"), count: data?.counts?.all },
              { value: "failed", label: tx("失败", "Failed"), count: data?.counts?.failed },
              { value: "blocked", label: tx("已拦截", "Blocked"), count: data?.counts?.blocked },
              { value: "negative", label: tx("有差评", "Negative"), count: data?.counts?.negative },
            ]}
          />
          <SearchBox placeholder={tx("运行 ID、服务或事件", "Run ID, service or incident")} value={q} onChange={setQ} width={220} />
          <ChipSelect label="Agent" {...fAgent.props} />
          <ChipSelect label={tx("版本", "Version")} {...fVer.props} />
          <ChipSelect label={tx("触发方式", "Trigger")} {...fTrig.props} />
          <Spacer />
          {API_MODE === "mock" && <TextLink to="/agents/runs/run_7f3a">{tx("打开运行详情 run_7f3a", "Open run run_7f3a")}</TextLink>}
        </Row>
        <Card style={{ overflow: "hidden" }}>
          <DataTable<RunRow>
            rows={rows}
            loading={!data}
            rowKey={(r) => r.id}
            rowHeight={40}
            onRowClick={(r) => nav(`/agents/runs/${r.id}`)}
            columns={[
              { key: "id", header: tx("运行", "Run"), render: (r) => <Mono color="var(--accent-text)">{r.id}</Mono> },
              {
                key: "agent",
                header: "Agent",
                width: "15%",
                render: (r) => (
                  <Row gap={6}>
                    <span className="ellipsis" style={{ fontWeight: 500 }}>{r.agent}</span>
                    <Badge mono>{r.version}</Badge>
                  </Row>
                ),
              },
              { key: "trigger", header: tx("触发", "Trigger"), width: "15%", render: (r) => <Muted>{r.trigger}</Muted> },
              { key: "scope", header: tx("范围", "Scope"), render: (r) => <Muted>{r.scope}</Muted> },
              { key: "status", header: tx("状态", "Status"), render: (r) => <State tone={st(r.status)[1]}>{st(r.status)[0]}</State> },
              { key: "steps", header: tx("推理 · 工具", "Turns · tools"), render: (r) => (r.steps == null ? <UnknownValue /> : <Num color="var(--muted)">{r.steps}</Num>) },
              { key: "tokens", header: "tokens", render: (r) => (r.tokens == null ? <UnknownValue /> : <Num>{r.tokens}</Num>) },
              { key: "duration", header: tx("耗时", "Duration"), render: (r) => (r.duration == null ? <UnknownValue /> : <Muted>{r.duration}</Muted>) },
              {
                key: "feedback",
                header: tx("反馈", "Feedback"),
                render: (r) =>
                  r.feedback ? (
                    <Row gap={4}>
                      <Icon name={r.feedback.icon as IconName} size={13} color={`var(--${r.feedback.tone})`} />
                      <Muted size={11.5}>{r.feedback.text}</Muted>
                    </Row>
                  ) : (
                    <Muted>—</Muted>
                  ),
              },
              {
                key: "trace",
                header: "Trace",
                render: () => (
                  <Row gap={3}>
                    <Brand name="langfuse" size={18} />
                    <Brand name="phoenix" size={18} />
                  </Row>
                ),
              },
            ]}
          />
        </Card>
      </div>
    </Page>
  );
}
