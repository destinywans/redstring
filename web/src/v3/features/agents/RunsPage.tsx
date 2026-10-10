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

const fmtMs = (ms: number) => (ms >= 60_000 ? `${Math.floor(ms / 60_000)} m ${String(Math.round((ms % 60_000) / 1000)).padStart(2, "0")} s` : ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.round(ms)} ms`);
const fmtTokens = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n));
/** Real durations arrive as sorted ms values; bucket them like the sample histogram (10 s wide, last bucket 120 s+). */
const durationBuckets = (ms: number[]) => {
  const out = new Array<number>(13).fill(0);
  for (const v of ms) out[Math.min(12, Math.floor(v / 10_000))]++;
  return out;
};

export default function RunsPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const demo = API_MODE === "mock";
  const [filter, setFilter] = useState<"all" | "failed" | "blocked" | "negative">("all");
  const [q, setQ] = useState("");
  const { data } = useGet<RunsView>("/agent-runs", { filter });
  const st = (s: RunRow["status"]): [string, "ok" | "danger" | "warn" | "neutral"] =>
    (({ success: [tx("成功", "Success"), "ok"], failed: [tx("失败", "Failed"), "danger"], blocked: [tx("已拦截", "Blocked"), "warn"], running: [tx("运行中", "Running"), "neutral"] }) as Record<string, [string, "ok" | "danger" | "warn" | "neutral"]>)[s] ?? [s, "neutral"];
  // Real KPI labels come back in English; name the ones the server sends.
  const kpiLabel = (id: string, label: string) => (demo ? label : ({ persisted_runs: tx("运行记录", "Run records"), failed: tx("失败", "Failed"), blocked: tx("已拦截", "Blocked") } as Record<string, string>)[id] ?? label);
  const kpiSub = (id: string, sub?: string) => (demo ? sub : id === "persisted_runs" ? tx("有权限查看的记录", "Records you can see") : tx("已记录的运行", "Recorded runs"));
  const trendLabels = (t: NonNullable<RunsView["trend"]>) => {
    if (!t.window_start_at || !t.interval_minutes) return t.ok.map((_, i) => `${String(i).padStart(2, "0")}:00`);
    const start = new Date(t.window_start_at).getTime();
    return t.ok.map((_, i) => new Date(start + i * t.interval_minutes! * 60_000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }));
  };
  const durations = data?.durations ? (demo ? data.durations : durationBuckets(data.durations)) : null;
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
          {/* No trace backend is connected for agent runs yet; the sample link points at a placeholder host. */}
          {demo && <Btn icon="external" onClick={() => openExternal("Langfuse", "https://langfuse.example.internal/project/aiops/traces")}>{tx("在 Langfuse 中打开", "Open in Langfuse")}</Btn>}
        </>
      }
    >
      <div className="v3-content" style={{ padding: "16px 20px" }}>
        <KpiRow>{data ? data.kpis.map((k) => <Kpi key={k.id} small label={kpiLabel(k.id, k.label)} value={k.value} unit={k.unit} sub={kpiSub(k.id, k.sub)} tone={k.tone} values={k.series?.length ? k.series : undefined} />) : Array.from({ length: 6 }, (_, i) => <Skeleton key={i} h={90} style={{ flex: 1 }} />)}</KpiRow>
        <Card style={{ padding: "12px 14px" }}>
          <Row gap={22} align="flex-start">
            <Col gap={8} style={{ flex: "1.7 1 0", minWidth: 0 }}>
              <Row gap={8}>
                <Strong>{tx("运行趋势 · 最近 24 小时", "Runs · last 24 h")}</Strong>
                <Spacer />
                <Legend items={[[tx("成功", "Success"), "var(--c2)"], [tx("失败", "Failed"), "var(--c3)"], [tx("拦截", "Blocked"), "var(--c4)"]]} />
              </Row>
              {data && !data.trend ? <Unavailable reason="not_generated" icon="trend" title={tx("暂无运行趋势", "No run trend yet")} desc={tx("有 Agent 运行记录后显示最近 24 小时的成功、失败和拦截。", "Successes, failures and blocks appear once agents have run.")} /> : data?.trend ? <StackedArea series={[data.trend.ok, data.trend.failed, data.trend.blocked]} colors={["var(--c2)", "var(--c3)", "var(--c4)"]} height={78} names={[tx("成功", "Success"), tx("失败", "Failed"), tx("拦截", "Blocked")]} labels={trendLabels(data.trend)} /> : <Skeleton h={78} />}
            </Col>
            <Col gap={8} style={{ flex: "1 1 0", minWidth: 0 }}>
              <Row gap={8}>
                <Strong>{tx("耗时分布", "Duration distribution")}</Strong>
                <Spacer />
              </Row>
              {data && !durations ? <UnknownValue reason="not_generated" /> : durations ? <VBars values={durations} height={60} /> : <Skeleton h={60} />}
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
              { key: "status", header: tx("状态", "Status"), render: (r) => <State tone={st(r.status)[1]} pulse={r.status === "running"}>{st(r.status)[0]}</State> },
              { key: "steps", header: demo ? tx("推理 · 工具", "Turns · tools") : tx("步骤", "Steps"), render: (r) => (r.steps == null ? <UnknownValue reason="not_generated" /> : <Num color="var(--muted)">{r.steps}</Num>) },
              { key: "tokens", header: "tokens", render: (r) => (r.tokens == null ? <UnknownValue reason="not_generated" /> : <Num>{typeof r.tokens === "number" ? fmtTokens(r.tokens) : r.tokens}</Num>) },
              {
                key: "duration",
                header: tx("耗时", "Duration"),
                render: (r) => {
                  const d = r.duration_ms != null ? fmtMs(r.duration_ms) : r.duration;
                  return d == null ? r.status === "running" ? <Muted>{tx("运行中", "Running")}</Muted> : <UnknownValue reason="not_generated" /> : <Muted>{d}</Muted>;
                },
              },
              {
                key: "feedback",
                header: tx("反馈", "Feedback"),
                render: (r) => {
                  const f = r.feedback;
                  if (!f || ("up" in f && f.up + f.down === 0)) return <Muted>—</Muted>;
                  if ("up" in f)
                    return (
                      <Row gap={8}>
                        {f.up > 0 && (
                          <Row gap={3}>
                            <Icon name="check" size={13} color="var(--ok)" />
                            <Muted size={11.5}>{f.up}</Muted>
                          </Row>
                        )}
                        {f.down > 0 && (
                          <Row gap={3}>
                            <Icon name="x" size={13} color="var(--danger)" />
                            <Muted size={11.5}>{f.down}</Muted>
                          </Row>
                        )}
                      </Row>
                    );
                  return (
                    <Row gap={4}>
                      <Icon name={f.icon as IconName} size={13} color={`var(--${f.tone})`} />
                      <Muted size={11.5}>{f.text}</Muted>
                    </Row>
                  );
                },
              },
              // Started time for real runs; the sample rows show where traces would be exported instead.
              demo
                ? {
                    key: "trace",
                    header: "Trace",
                    render: () => (
                      <Row gap={3}>
                        <Brand name="langfuse" size={18} />
                        <Brand name="phoenix" size={18} />
                      </Row>
                    ),
                  }
                : { key: "started", header: tx("开始时间", "Started"), render: (r) => <Muted>{r.started_at ? new Date(r.started_at).toLocaleString() : "—"}</Muted> },
            ]}
          />
        </Card>
      </div>
    </Page>
  );
}
