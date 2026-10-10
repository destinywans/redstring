import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { PartialBanner, Unavailable, UnknownValue, errorReason } from "../../ui/unknown";
import type { ServiceCatalog, TraceList } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Dot, Icon, IconBtn, Legend, Muted, Num, Panel, Row, Skeleton, Spacer, cx, type Tone } from "../../ui/core";
import { StackedBars } from "../../charts";
import { useI18n } from "../../i18n";
import "./trace.css";
import { ChipSelect, RangeChip, openForm, usePickLists } from "../../ui/demo";
import { toast } from "../../ui/overlays";
import { BudgetNotice, DenseCard, coveredWindow, isScopeLimit, useAxisNote, useWindowText, withErrors } from "./TraceLimit";
import { useWhen } from "../../ui/when";

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
  const when = useWhen();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const scope = params.get("scope") === "agent" ? "agent" : "app";
  const [view, setView] = useState(scope === "agent" ? "all" : "errors");
  const [checked, setChecked] = useState<Record<string, boolean>>(API_MODE === "mock" ? { "service:checkout-api": true, [`status:${tx("错误", "Error")}`]: true, "op:POST /api/orders": true, "version:v2.8.1": true } : {});
  const [q, setQ] = useState("");
  // Real mode sends the submitted query; the demo filters by view only.
  const [applied, setApplied] = useState("");
  const [range, setRange] = useState(scope === "agent" ? "24h" : "1h");
  const real = API_MODE !== "mock";
  const service = params.get("service") ?? undefined;
  // Real toolbar (approved design): trace sources bound to the chosen service, service picker, team views.
  const lists = usePickLists();
  const { data: catalog } = useGet<ServiceCatalog>(real && scope === "app" ? "/services" : null, undefined, { staleTime: 5 * 60_000 });
  const bound: string[] = (catalog?.items.find((x) => x.id === service)?.connectors ?? []).filter((c) => c === "tempo" || c === "jaeger");
  const [source, setSource] = useState<string | undefined>(undefined);
  const activeSource = source && bound.includes(source) ? source : bound.length === 1 ? bound[0] : undefined;
  const teamViews = useGet<{ items: { id: string; name: string; query: string; owner: string }[] }>(real ? "/trace-views" : null, { scope });
  const setService = (v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set("service", v);
    else next.delete("service");
    setParams(next, { replace: true });
    setSource(undefined);
  };
  const saveTeamView = async () => {
    const v = await openForm({ title: tx("保存为团队视图", "Save as a team view"), desc: tx("保存的只是查询文字，团队成员都能使用；不会改变谁能看到什么。", "Only the query text is saved for your team; it doesn't change who can see what."), fields: [{ key: "name", label: tx("名称", "Name"), required: true }, { key: "query", label: tx("查询", "Query"), initial: q, mono: true }], submit: tx("保存", "Save") });
    if (!v) return;
    try {
      await api("POST", "/trace-views", { body: { scope, name: v.name.trim().slice(0, 160), query: v.query } });
      void teamViews.refetch();
      toast(tx("团队视图「{n}」已保存", "Team view “{n}” saved", { n: v.name }));
    } catch (e) {
      toast(e instanceof ApiError && e.code === "ambiguous_team" ? tx("你属于多个团队或没有团队，暂时不能保存团队视图。", "You belong to several teams or none, so a team view can't be saved.") : e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
    }
  };
  const deleteTeamView = async (id: string, name: string) => {
    try {
      await api("DELETE", `/trace-views/${id}`);
      void teamViews.refetch();
      toast(tx("已删除团队视图「{n}」", "Team view “{n}” deleted", { n: name }));
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("删除失败，请稍后重试", "Couldn't delete; try again later"), { tone: "danger" });
    }
  };
  const { data, isLoading, error, refetch, dataUpdatedAt } = useGet<TraceList>("/traces", { scope, view, service, q: applied || undefined, range });
  const u = data?.unavailable ?? {};
  // items is null only when no trace source is bound at all.
  const noSource = !!data && data.items === null;
  // PA-78: the search stopped at its budget (covered window), or even one second is too dense to list (413).
  const covered = coveredWindow(data, dataUpdatedAt);
  const dense = scope === "app" && isScopeLimit(error);
  const axisNote = useAxisNote(covered, range);
  const wt = useWindowText();
  const [pickNonce, setPickNonce] = useState(0);
  // A 413 returns no views; keep the last ones for this scope so a narrower view stays one click away.
  const [knownViews, setKnownViews] = useState<{ scope: string; views: TraceList["views"] }>({ scope, views: [] });
  useEffect(() => {
    if (data) setKnownViews({ scope, views: data.views });
  }, [data, scope]);
  const views = data?.views ?? (knownViews.scope === scope ? knownViews.views : []);
  const shortcuts = (order: ("svc" | "err" | "r15")[]) => {
    const all = {
      svc: scope === "app" && !service && (
        <Btn key="svc" h={26} icon="box" onClick={() => setPickNonce((n) => n + 1)}>
          {tx("选择服务", "Choose a service")}
        </Btn>
      ),
      err: scope === "app" && !/\bstatus\s*=\s*error\b/.test(applied) && (
        <Btn key="err" h={26} icon="alert" onClick={() => { const next = withErrors(q); setQ(next); setApplied(next); }}>
          {tx("只看错误", "Only errors")}
        </Btn>
      ),
      r15: range !== "15m" && (
        <Btn key="r15" h={26} kind="ghost" icon="clock" onClick={() => setRange("15m")}>
          {tx("最近 15 分钟", "Last 15 minutes")}
        </Btn>
      ),
    };
    const out = order.map((k) => all[k]).filter(Boolean);
    return out.length ? out : null;
  };
  const [saved, setSaved] = useState<{ name: string; query: string }[]>([]);
  useEffect(() => {
    setView(scope === "agent" ? "all" : "errors");
    setRange(scope === "agent" ? "24h" : "1h");
  }, [scope]);
  // Fill the box with a view's query only when the view is picked; syncing on every load wiped the query just searched.
  const viewSynced = useRef("");
  useEffect(() => {
    const key = `${scope}:${view}`;
    if (viewSynced.current === key) return;
    const v = data?.views.find((x) => x.id === view);
    if (v) {
      setQ(v.query);
      viewSynced.current = key;
    }
  }, [data, view, scope]);
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
            !real && <ChipSelect icon="database" label="" showLabel={false} defaultValue="tempo" options={[{ value: "tempo", label: tx("Tempo · 生产", "Tempo · prod") }, { value: "jaeger", label: "Jaeger · prod" }]} />
          )}
        </>
      }
    >
      <div className="tr-layout">
        <aside className="tr-facets" aria-label={tx("筛选", "Filters")}>
          {isLoading && !error && <Skeleton h={300} />}
          {data && data.facets === null && (
            <section>
              <b>{tx("筛选", "Filters")}</b>
              <Row gap={6}>
                <Muted size={11.5}>{tx("分面统计", "Facet counts")}</Muted>
                <UnknownValue reason={u.facets} source="Tempo" />
              </Row>
            </section>
          )}
          {data?.facets?.map((f) => (
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
              if (q !== applied) setApplied(q);
              else void refetch();
            }}
          >
            {real && scope === "app" && bound.length === 1 && <Chip icon="database">{bound[0] === "tempo" ? "Tempo" : "Jaeger"}</Chip>}
            {real && scope === "app" && bound.length > 1 && <ChipSelect icon="database" label="" showLabel={false} value={activeSource} onChange={(v) => setSource(v as string)} options={bound.map((b) => ({ value: b, label: b === "tempo" ? "Tempo" : "Jaeger" }))} />}
            {scope === "app" && <ChipSelect icon="box" label="" showLabel={false} value={service ?? "all"} onChange={(v) => setService(v === "all" ? "" : (v as string))} options={[{ value: "all", label: tx("全部服务", "All services") }, ...lists.services]} openNonce={pickNonce} />}
            <span className={cx("tr-range", dense && "hl")}>
              <RangeChip key={scope} value={range} onChange={setRange} />
            </span>
            <label className="grow">
              <Badge tone="info" mono>{scope === "agent" ? tx("筛选", "Filter") : "TraceQL"}</Badge>
              <input value={q} onChange={(e) => setQ(e.target.value)} aria-label={tx("链路查询", "Trace query")} spellCheck={false} placeholder={scope === "agent" ? 'agent = "investigator" && model = "claude-opus-5-5"' : "{ status = error }"} />
            </label>
            <Btn kind="primary" icon="search" h={34} type="submit">{tx("搜索", "Search")}</Btn>
          </form>
          {/* An invalid TraceQL condition is explained right under the box (422), not as a missing source. */}
          {real && scope === "app" && error instanceof ApiError && error.status === 422 && (
            <Muted size={11.5} style={{ color: "var(--danger)", marginTop: -6 }}>
              {tx("只支持简单比较，可用 && 和 ||；不支持管道（|）、正则和嵌套选择器。", "Only simple comparisons joined by && and || are supported; no pipes (|), regexes or nested selectors.")} {error.message}
            </Muted>
          )}
          {error && !data && !dense && !(real && error instanceof ApiError && error.status === 422) && (
            <Card>
              <Unavailable reason={errorReason(error)} source="Tempo" desc={error instanceof ApiError && error.status === 422 ? error.message : undefined} />
            </Card>
          )}
          {covered && data && !noSource && !dense && <BudgetNotice count={data.items?.length ?? 0} w={covered} range={range} actions={shortcuts(["svc", "err", "r15"])} />}
          {data?.partial && !covered && !noSource && !dense && (
            <PartialBanner fields={[]}>
              {tx("这里只列出这次有界搜索找到并确认已结束的链路，不是完整结果：总数、分面、数量分布和耗时分布暂缺。", "Only the finished traces this bounded search found are listed — not a complete result: totals, facets and the count and duration charts are missing.")}
            </PartialBanner>
          )}
          <Row gap={6} wrap>
            <Muted size={11.5}>{tx("视图", "Views")}</Muted>
            {views.map((v) => (
              <Chip key={v.id} active={view === v.id} onClick={() => { setView(v.id); if (!real) setApplied(""); }}>
                {typeof v.count === "number" && <Dot tone="danger" size={6} />}
                {v.label}
                {typeof v.count === "number" && ` ${v.count}`}
              </Chip>
            ))}
            {teamViews.data?.items.map((v) => (
              <span key={v.id} className="tr-team-view">
                <Chip active={applied === v.query} onClick={() => { setQ(v.query); setApplied(v.query); }}>
                  {v.name}
                  <Muted size={11}>· {v.owner}</Muted>
                </Chip>
                <IconBtn icon="x" label={tx("删除团队视图 {n}", "Delete team view {n}", { n: v.name })} size={20} onClick={() => void deleteTeamView(v.id, v.name)} />
              </span>
            ))}
            {real && (
              <Chip icon="plus" onClick={() => void saveTeamView()}>
                {tx("保存为团队视图", "Save as team view")}
              </Chip>
            )}
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
            {data && !dense && data.total !== null && !covered && <Muted size={11.5}>{tx("找到 {n} 条 · {t}", "{n} found · {t}", { n: data.total.toLocaleString(), t: data.took })}</Muted>}
            {data && !dense && data.total !== null && covered && (
              <Muted size={11.5}>{tx("最近 {w}共 {t} 条，列出 {n} 条 · {took}", "{t} in the last {w}, {n} listed · {took}", { w: wt.span(covered.seconds), t: data.total.toLocaleString(), n: data.items?.length ?? 0, took: data.took })}</Muted>
            )}
            {data && !dense && data.total === null && data.items && (
              <Muted size={11.5}>
                {tx("列出 {n} 条 · 总数", "{n} listed · total", { n: data.items.length })} <UnknownValue reason={u.total} source="Tempo" /> · {data.took}
              </Muted>
            )}
          </Row>
          {dense && <DenseCard serviceExample={service ? null : (lists.services[0]?.label ?? "checkout-api")} actions={shortcuts(["err", "svc", "r15"])} />}
          {!dense && (
          <div className="tr-charts">
            <Panel title={tx("数量 · 按状态", "Count · by status")} right={<Legend items={[[tx("正常", "OK"), "var(--c1s)"], [tx("错误", "Error"), "var(--danger)"]]} />} bodyStyle={{ padding: "10px 14px" }}>
              {data && !data.histogram ? (
                <Unavailable reason={u.histogram} source="Tempo" title={tx("数量分布", "Count by status")} icon="activity" />
              ) : data?.histogram ? (
                <div className="v3-col" style={{ gap: 6 }}>
                  <StackedBars data={data.histogram.counts} colors={["var(--c1s)", "var(--danger)"]} height={96} names={[tx("正常", "OK"), tx("错误", "Error")]} />
                  <Row gap={0} style={{ justifyContent: "space-between" }}>
                    {(covered ? wt.ticks(covered) : data.histogram.axis).map((a, i) => (
                      <Muted key={`${i}-${a}`} size={10.5}>{a}</Muted>
                    ))}
                  </Row>
                  {axisNote && <Muted size={11}>{axisNote}</Muted>}
                </div>
              ) : error ? (
                <Muted>—</Muted>
              ) : (
                <Skeleton h={110} />
              )}
            </Panel>
            <Panel title={tx("耗时分布", "Duration")} right={<Legend items={[[tx("正常", "OK"), "var(--c1)"], [tx("错误", "Error"), "var(--danger)"]]} />} bodyStyle={{ padding: "10px 14px" }}>
              {data && !data.scatter ? (
                <Unavailable reason={u.scatter} source="Tempo" title={tx("耗时分布", "Duration")} icon="activity" />
              ) : data?.scatter ? (
                <div className="v3-col" style={{ gap: 6 }}>
                  <svg width="100%" height="105" viewBox="0 0 440 110" preserveAspectRatio="none" role="img" aria-label={tx("耗时散点图", "Duration scatter")}>
                    <line x1={0} x2={440} y1={36} y2={36} className="ch-grid" />
                    <line x1={0} x2={440} y1={73} y2={73} className="ch-grid" />
                    {scope === "app" && API_MODE === "mock" && !covered && <rect x={232} y={6} width={104} height={58} rx={4} className="tr-brush" />}
                    {data.scatter.map((p, i) => (
                      <circle key={i} cx={p.x} cy={p.y} r={p.error ? 3.2 : 2.6} fill={p.error ? "var(--danger)" : "var(--c1)"} className={cx(p.id && "clickable")} onClick={() => p.id && nav(scope === "agent" ? `/agents/runs/${p.id}` : `/traces/${p.id}`)} />
                    ))}
                  </svg>
                  {covered && (
                    <Row gap={0} style={{ justifyContent: "space-between" }}>
                      {wt.ticks(covered).map((a, i) => (
                        <Muted key={`${i}-${a}`} size={10.5}>{a}</Muted>
                      ))}
                    </Row>
                  )}
                  {axisNote && <Muted size={11}>{axisNote}</Muted>}
                  <Row gap={6}>
                    <Muted size={11.5}>{scope === "app" ? (API_MODE === "mock" && !covered ? tx("框选一段查看这些链路", "Brush a region to filter these traces") : tx("点一个点打开链路", "Click a point to open the trace")) : tx("点一个点打开运行", "Click a point to open the run")}</Muted>
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
          )}
          {dense ? null : noSource ? (
            <Card>
              <Unavailable reason={u.items} source={tx("链路来源", "a trace source")} icon="branch" />
            </Card>
          ) : (
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
                {data?.items?.map((r) => (
                  <tr key={r.id} className="clickable" onClick={() => nav(r.href)}>
                    <td className="mono muted" style={{ fontSize: "var(--fs-sub)" }}>{when.precise(r.time)}</td>
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
                {data?.items && !data.items.length && (
                  <tr>
                    <td colSpan={scope === "agent" ? 8 : 6} className="muted" style={{ textAlign: "center", padding: 24 }}>
                      {tx("这个时间范围内没有找到链路", "No traces found in this range")}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </Card>
          )}
        </div>
      </div>
    </Page>
  );
}
