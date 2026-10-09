import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, useGet } from "../../api/client";
import { Unavailable, errorReason } from "../../ui/unknown";
import type { TraceList } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Dot, Icon, Legend, Muted, Num, Panel, Row, Skeleton, Spacer, cx, type Tone } from "../../ui/core";
import { StackedBars } from "../../charts";
import { useI18n } from "../../i18n";
import "./trace.css";
import { ChipSelect, RangeChip, openForm } from "../../ui/demo";
import { toast } from "../../ui/overlays";

export const fmtMs = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(ms >= 10000 ? 1 : 2)} s` : `${Math.round(ms)} ms`);

export function ScopeSwitch({ scope, onChange }: { scope: "app" | "agent"; onChange: (s: "app" | "agent") => void }) {
  const { tx } = useI18n();
  return (
    <div className="tr-scope" role="group" aria-label={tx("链路范围", "Trace scope")}>
      {(["app", "agent"] as const).map((s) => (
        <button key={s} type="button" aria-pressed={scope === s} className={cx(scope === s && "on")} onClick={() => onChange(s)}>
          <Icon name={s === "app" ? "activity" : "cpu"} size={13} />
          {s === "app" ? tx("应用", "Application") : "Agent"}
        </button>
      ))}
    </div>
  );
}

/** Traces v2 — facets, TraceQL, saved views, count histogram, duration scatter and a table. One explorer for application and agent traces. */
export default function TracesPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const scope = params.get("scope") === "agent" ? "agent" : "app";
  const [view, setView] = useState(scope === "agent" ? "all" : "errors");
  const [checked, setChecked] = useState<Record<string, boolean>>(API_MODE === "mock" ? { "service:checkout-api": true, [`status:${tx("错误", "Error")}`]: true, "op:POST /api/orders": true, "version:v2.8.1": true } : {});
  const [q, setQ] = useState("");
  // Real mode sends the submitted query; the demo filters by view only.
  const [applied, setApplied] = useState("");
  const { data, isLoading, error, refetch } = useGet<TraceList>("/traces", { scope, view, service: params.get("service") ?? undefined, q: API_MODE === "mock" ? undefined : applied || undefined });
  const [saved, setSaved] = useState<{ name: string; query: string }[]>([]);
  useEffect(() => setView(scope === "agent" ? "all" : "errors"), [scope]);
  useEffect(() => {
    const v = data?.views.find((x) => x.id === view);
    if (v) setQ(v.query);
  }, [data, view]);
  const maxMs = useMemo(() => Math.max(1, ...(data?.items ?? []).map((r) => r.duration_ms)), [data]);
  const setScope = (s: "app" | "agent") => setParams(s === "agent" ? { scope: "agent" } : {}, { replace: true });
  const toggle = (k: string) => setChecked((c) => ({ ...c, [k]: !c[k] }));
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("链路", "Traces")]}
      actions={
        <>
          <ScopeSwitch scope={scope} onChange={setScope} />
          {scope === "agent" ? (
            <Badge>{tx("Agent 运行记录", "Agent runs")}</Badge>
          ) : (
            API_MODE === "mock" && <ChipSelect icon="database" label="" showLabel={false} defaultValue="tempo" options={[{ value: "tempo", label: tx("Tempo · 生产", "Tempo · prod") }, { value: "jaeger", label: "Jaeger · prod" }]} />
          )}
          <RangeChip key={scope} defaultValue={scope === "agent" ? "24h" : "1h"} />
        </>
      }
    >
      <div className="tr-layout">
        <aside className="tr-facets" aria-label={tx("筛选", "Filters")}>
          {isLoading && !error && <Skeleton h={300} />}
          {data?.facets.map((f) => (
            <section key={f.key}>
              <b>{f.label}</b>
              {f.items.map((it) => {
                const k = `${f.key}:${it.value}`;
                return (
                  <label key={k}>
                    <input type="checkbox" checked={!!checked[k]} onChange={() => toggle(k)} />
                    {it.color ? <i className="tr-sw" style={{ background: it.color }} /> : it.tone ? <Dot tone={it.tone} size={7} /> : null}
                    <span className="grow ellipsis">{it.value}</span>
                    <Muted size={11}>{it.count.toLocaleString()}</Muted>
                  </label>
                );
              })}
            </section>
          ))}
          {scope === "app" && data && API_MODE === "mock" && (
            <section>
              <b>{tx("耗时", "Duration")}</b>
              <div className="tr-durhist">
                {[2, 5, 12, 26, 38, 30, 18, 9, 6, 4, 3, 2, 2, 3, 4, 3, 2, 1].map((v, i) => (
                  <span key={i} style={{ height: `${(v / 38) * 100}%`, background: i >= 9 ? "var(--accent)" : "var(--faint)" }} />
                ))}
              </div>
              <Row gap={4}>
                <Muted size={11.5}>&gt; 1 s</Muted>
                <Spacer />
                <Muted size={11.5}>{tx("最长 3.4 s", "max 3.4 s")}</Muted>
              </Row>
            </section>
          )}
        </aside>
        <div className="tr-main">
          <form
            className="tr-query"
            onSubmit={(e) => {
              e.preventDefault();
              if (API_MODE !== "mock" && q !== applied) setApplied(q);
              else void refetch();
            }}
          >
            <label className="grow">
              <Badge tone="info" mono>{scope === "agent" ? tx("筛选", "Filter") : "TraceQL"}</Badge>
              <input value={q} onChange={(e) => setQ(e.target.value)} aria-label={tx("链路查询", "Trace query")} spellCheck={false} placeholder={scope === "agent" ? 'agent = "investigator" && model = "claude-opus-5-5"' : "{ status = error }"} />
            </label>
            <Btn kind="primary" icon="search" h={34} type="submit">{tx("搜索", "Search")}</Btn>
          </form>
          {error && !data && (
            <Card>
              <Unavailable reason={errorReason(error)} source="Tempo" desc={error instanceof ApiError && error.status === 422 ? error.message : undefined} />
            </Card>
          )}
          <Row gap={6} wrap>
            <Muted size={11.5}>{tx("视图", "Views")}</Muted>
            {data?.views.map((v) => (
              <Chip key={v.id} active={view === v.id} onClick={() => setView(v.id)}>
                {v.count !== undefined && <Dot tone="danger" size={6} />}
                {v.label}
                {v.count !== undefined && ` ${v.count}`}
              </Chip>
            ))}
            {saved.map((v) => (
              <Chip key={v.name} active={q === v.query} onClick={() => setQ(v.query)}>
                {v.name}
              </Chip>
            ))}
            {/* Saved views are demo-only: there is no shared view store yet. */}
            {API_MODE === "mock" && (
            <Chip
              icon="plus"
              onClick={async () => {
                const v = await openForm({ title: tx("保存视图", "Save view"), desc: tx("保存当前的查询语句，团队成员都能使用。", "Saves the current query for your team."), fields: [{ key: "name", label: tx("名称", "Name"), required: true }, { key: "query", label: tx("查询", "Query"), initial: q, mono: true, required: true }], submit: tx("保存", "Save") });
                if (v) {
                  setSaved((s) => [...s, { name: v.name, query: v.query }]);
                  toast(tx("视图「{n}」已保存", "View “{n}” saved", { n: v.name }));
                }
              }}
            >
              {tx("保存视图", "Save view")}
            </Chip>
            )}
            <Spacer />
            {data && <Muted size={11.5}>{tx("找到 {n} 条 · {t}", "{n} found · {t}", { n: data.total.toLocaleString(), t: data.took })}</Muted>}
          </Row>
          <div className="tr-charts">
            <Panel title={tx("数量 · 按状态", "Count · by status")} right={<Legend items={[[tx("正常", "OK"), "var(--c1s)"], [tx("错误", "Error"), "var(--danger)"]]} />} bodyStyle={{ padding: "10px 14px" }}>
              {data ? (
                <div className="v3-col" style={{ gap: 6 }}>
                  <StackedBars data={data.histogram.counts} colors={["var(--c1s)", "var(--danger)"]} height={96} names={[tx("正常", "OK"), tx("错误", "Error")]} />
                  <Row gap={0} style={{ justifyContent: "space-between" }}>
                    {data.histogram.axis.map((a) => (
                      <Muted key={a} size={10.5}>{a}</Muted>
                    ))}
                  </Row>
                </div>
              ) : error ? (
                <Muted>—</Muted>
              ) : (
                <Skeleton h={110} />
              )}
            </Panel>
            <Panel title={tx("耗时分布", "Duration")} right={<Legend items={[[tx("正常", "OK"), "var(--c1)"], [tx("错误", "Error"), "var(--danger)"]]} />} bodyStyle={{ padding: "10px 14px" }}>
              {data ? (
                <div className="v3-col" style={{ gap: 6 }}>
                  <svg width="100%" height="105" viewBox="0 0 440 110" preserveAspectRatio="none" role="img" aria-label={tx("耗时散点图", "Duration scatter")}>
                    <line x1={0} x2={440} y1={36} y2={36} className="ch-grid" />
                    <line x1={0} x2={440} y1={73} y2={73} className="ch-grid" />
                    {scope === "app" && API_MODE === "mock" && <rect x={232} y={6} width={104} height={58} rx={4} className="tr-brush" />}
                    {data.scatter.map((p, i) => (
                      <circle key={i} cx={p.x} cy={p.y} r={p.error ? 3.2 : 2.6} fill={p.error ? "var(--danger)" : "var(--c1)"} className={cx(p.id && "clickable")} onClick={() => p.id && nav(scope === "agent" ? `/agents/runs/${p.id}` : `/traces/${p.id}`)} />
                    ))}
                  </svg>
                  <Row gap={6}>
                    <Muted size={11.5}>{scope === "app" ? tx("框选一段查看这些链路", "Brush a region to filter these traces") : tx("点一个点打开运行", "Click a point to open the run")}</Muted>
                    <Spacer />
                    <Muted size={11.5}>{tx("纵轴：耗时", "Y: duration")}</Muted>
                  </Row>
                </div>
              ) : error ? (
                <Muted>—</Muted>
              ) : (
                <Skeleton h={110} />
              )}
            </Panel>
          </div>
          <Card style={{ overflow: "hidden" }}>
            <table className="v3-table tr-table">
              <thead>
                <tr>
                  <th style={{ width: "15%" }}>{tx("开始时间", "Start")}</th>
                  <th style={{ width: scope === "agent" ? "26%" : "26%" }}>{scope === "agent" ? tx("运行", "Run") : tx("根操作", "Root operation")}</th>
                  <th style={{ width: "16%" }}>{scope === "agent" ? tx("模型", "Model") : tx("服务", "Services")}</th>
                  <th>{tx("耗时", "Duration")}</th>
                  <th style={{ width: 60 }}>Span</th>
                  {scope === "agent" && <th style={{ width: 70 }}>tokens</th>}
                  {scope === "agent" && <th style={{ width: 64 }}>{tx("成本", "Cost")}</th>}
                  <th style={{ width: 60 }}>{tx("错误", "Errors")}</th>
                </tr>
              </thead>
              <tbody>
                {data?.items.map((r) => (
                  <tr key={r.id} className="clickable" onClick={() => nav(r.href)}>
                    <td className="mono muted" style={{ fontSize: "var(--fs-sub)" }}>{r.time}</td>
                    <td>
                      <span className="v3-col" style={{ gap: 0 }}>
                        <span className={cx(scope === "app" && "mono")} style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{r.op}</span>
                        {r.agent && <Muted size={11}>{r.agent}</Muted>}
                      </span>
                    </td>
                    <td>
                      {scope === "agent" ? (
                        <Muted size={12}>{r.model}</Muted>
                      ) : (
                        <Row gap={3}>
                          {r.services.map((s) => (
                            <i key={s} className="tr-sw" title={s} style={{ background: data.colors[s] }} />
                          ))}
                          <Muted size={11}>{r.services.length}</Muted>
                        </Row>
                      )}
                    </td>
                    <td>
                      <div className="tr-durbar">
                        <span style={{ width: `${(100 * r.duration_ms) / maxMs}%`, background: r.errors ? "var(--danger)" : "var(--c1)" }} />
                        <Num color={r.errors ? "var(--danger)" : undefined}>{fmtMs(r.duration_ms)}</Num>
                      </div>
                    </td>
                    <td className="tnum muted">{r.spans}</td>
                    {scope === "agent" && <td className="tnum">{r.tokens}</td>}
                    {scope === "agent" && <td className="tnum">{r.cost}</td>}
                    <td>{r.errors ? <Badge tone={"danger" as Tone}>{r.errors}</Badge> : <Muted>—</Muted>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </div>
      </div>
    </Page>
  );
}
