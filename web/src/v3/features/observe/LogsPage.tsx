import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, useGet } from "../../api/client";
import { Unavailable, UnknownValue, errorReason } from "../../ui/unknown";
import type { LogLine, LogSearch, ServiceCatalog } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, Input, Muted, Num, Row, Spacer, Strong, Tile, Toggle, cx } from "../../ui/core";
import { StackedBars, Sparkline } from "../../charts";
import { useI18n } from "../../i18n";
import "./observe.css";
import { RangeChip, openExternal, ChipSelect, useRanges, usePickLists } from "../../ui/demo";
import { useWhen } from "../../ui/when";

const LV = { ERROR: "danger", WARN: "warn", INFO: "neutral" } as const;

export default function LogsPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const service = params.get("service") ?? undefined;
  const real = API_MODE !== "mock";
  // Real toolbar (approved design): only the log sources bound to the chosen service, a service picker and plain keywords.
  const lists = usePickLists();
  const { data: catalog } = useGet<ServiceCatalog>(real ? "/services" : null, undefined, { staleTime: 5 * 60_000 });
  const bound = (catalog?.items.find((s) => s.id === service)?.connectors ?? []).filter((c): c is "loki" | "elasticsearch" => c === "loki" || c === "elasticsearch");
  const [source, setSource] = useState<string | undefined>(undefined);
  const activeSource = source && bound.includes(source as "loki") ? source : bound.length === 1 ? bound[0] : undefined;
  const sourceName = (c: string) => (c === "loki" ? "Loki" : "Elasticsearch");
  const setService = (v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set("service", v);
    else next.delete("service");
    setParams(next, { replace: true });
    setSource(undefined);
  };
  const [range, setRange] = useState("30m");
  const rangeLabel = useRanges().find((r) => r.value === range)?.label ?? range;
  const [query, setQuery] = useState(API_MODE === "mock" ? 'service:"checkout-api" and level:(error or warn)' : "");
  const [applied, setApplied] = useState("");
  const [levels, setLevels] = useState<Set<LogLine["level"]>>(new Set(["ERROR", "WARN", "INFO"]));
  const [live, setLive] = useState(false);
  const [extra, setExtra] = useState<LogLine[]>([]);
  const { data, error, isFetching } = useGet<LogSearch>("/logs", { q: applied, levels: [...levels].join(","), service, range, source: real ? activeSource : undefined });
  const u = data?.unavailable ?? {};
  useEffect(() => {
    // The demo fakes a live tail; real mode has no streaming source yet, so nothing is invented.
    if (!live || API_MODE !== "mock") return;
    const t = setInterval(() => {
      const now = new Date();
      const ts = `${now.toTimeString().slice(0, 8)}.${String(now.getMilliseconds()).padStart(3, "0")}`;
      setExtra((x) => [{ time: ts, level: Math.random() > 0.4 ? "ERROR" : "WARN", service: "checkout-api", message: Math.random() > 0.5 ? "inventory.reserve timeout after 300ms, retrying (1/3) instance=inventory-03" : "retry 3/3 failed: inventory.reserve · POST /orders 502", trace: "4f2a9c17" } as LogLine, ...x].slice(0, 20));
    }, 1800);
    return () => clearInterval(t);
  }, [live]);
  const toggle = (l: LogLine["level"]) =>
    setLevels((s) => {
      const n = new Set(s);
      if (n.has(l)) n.delete(l);
      else n.add(l);
      return n;
    });
  const rows = [...extra.filter((e) => levels.has(e.level)), ...(data?.items ?? [])];
  // items is null only when no log source is bound at all.
  const noSource = !!data && data.items === null;
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("日志", "Logs")]}
      actions={
        <>
          {!real && <RangeChip value={range} onChange={setRange} />}
          {API_MODE === "mock" && <Btn icon="external" onClick={() => openExternal("Kibana", `https://kibana.example.internal/app/discover#/?_a=(query:(language:kuery,query:'${encodeURIComponent(query)}'))`)}>{tx("在 Kibana 中打开", "Open in Kibana")}</Btn>}
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <form className="v3-row" style={{ gap: 8 }} onSubmit={(e) => { e.preventDefault(); setApplied(API_MODE === "mock" && query.includes("checkout") ? "checkout" : query); }}>
              {API_MODE === "mock" && <ChipSelect icon="database" label="" showLabel={false} defaultValue="es" options={[{ value: "es", label: tx("Elasticsearch · 生产", "Elasticsearch · prod") }, { value: "loki", label: "Loki · prod" }]} />}
              {API_MODE === "mock" && <ChipSelect label="" showLabel={false} defaultValue="kql" options={[{ value: "kql", label: "KQL" }, { value: "lucene", label: "Lucene" }]} />}
              {real && bound.length === 1 && <Chip icon="database">{sourceName(bound[0])}</Chip>}
              {real && bound.length > 1 && <ChipSelect icon="database" label="" showLabel={false} value={activeSource} onChange={(v) => setSource(v as string)} options={bound.map((b) => ({ value: b, label: sourceName(b) }))} />}
              {real && <ChipSelect icon="box" label="" showLabel={false} value={service ?? "all"} onChange={(v) => setService(v === "all" ? "" : (v as string))} options={[{ value: "all", label: tx("全部服务", "All services") }, ...lists.services]} />}
              {real && <RangeChip value={range} onChange={setRange} />}
              <Input mono value={query} onChange={(e) => setQuery(e.target.value)} style={{ flexGrow: 1 }} aria-label={tx("日志查询", "Log query")} placeholder={real ? tx("关键字，例如 timeout（包含匹配）", "Keyword, e.g. timeout (contains)") : undefined} />
              <Btn kind="primary" icon="search" h={32} type="submit">
                {tx("搜索", "Search")}
              </Btn>
              {API_MODE === "mock" && (
                <>
                  <Muted>{tx("实时", "Live")}</Muted>
                  <Toggle on={live} onChange={setLive} label={tx("实时", "Live")} />
                </>
              )}
            </form>
            <Card style={{ padding: "10px 14px" }}>
              <Row gap={8}>
                <Strong>{tx("日志量", "Volume")}</Strong>
                {data ? (
                  <Muted size={11.5}>
                    {tx("共", "Total")} {data.total ?? <UnknownValue reason={u.total} source="Loki" />} {tx("条 · 错误", "lines · errors")} {data.errors ?? <UnknownValue reason={u.errors} source="Loki" />}
                  </Muted>
                ) : (
                  <Muted size={11.5}>{error ? "—" : "…"}</Muted>
                )}
                <Spacer />
                {(["ERROR", "WARN", "INFO"] as const).map((l) => (
                  <Chip key={l} active={levels.has(l)} onClick={() => toggle(l)}>
                    <i style={{ width: 8, height: 8, borderRadius: 2, background: l === "ERROR" ? "var(--c3)" : l === "WARN" ? "var(--c4)" : "var(--c5s)" }} />
                    {l}
                  </Chip>
                ))}
              </Row>
              {data?.histogram ? (
                <StackedBars data={data.histogram} colors={["var(--c3)", "var(--c4)", "var(--c5s)"]} height={70} names={["ERROR", "WARN", "INFO"]} gap={3} />
              ) : (
                data && (
                  <Row gap={6} style={{ height: 40 }}>
                    <Muted size={11.5}>{tx("日志量分布", "Volume over time")}</Muted>
                    <UnknownValue reason={u.histogram} source="Loki" />
                  </Row>
                )
              )}
            </Card>
            {error && !isFetching && (
              <Card>
                <Unavailable reason={errorReason(error)} source="Loki" desc={error instanceof ApiError && error.status === 422 ? error.message : undefined} />
              </Card>
            )}
            {noSource ? (
              <Card>
                <Unavailable reason={u.items} source={tx("日志来源", "a log source")} icon="file" />
              </Card>
            ) : (
            <Card style={{ overflow: "hidden" }}>
              <table className="v3-table logs-table">
                <thead>
                  <tr>
                    <th style={{ width: 120 }}>{tx("时间", "Time")}</th>
                    <th style={{ width: 70 }}>{tx("级别", "Level")}</th>
                    <th style={{ width: 130 }}>{tx("服务", "Service")}</th>
                    <th>{tx("消息", "Message")}</th>
                    <th style={{ width: 60 }}>Trace</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r, i) => (
                    <tr key={r.time + i} className={cx(i < extra.length && live && "anim-rise")} style={{ height: 34 }}>
                      <td className="mono muted" style={{ fontSize: "var(--fs-sub)" }}>{when.precise(r.time)}</td>
                      <td>
                        <Badge tone={LV[r.level]} mono>
                          {r.level}
                        </Badge>
                      </td>
                      <td>
                        <Muted>{r.service}</Muted>
                      </td>
                      <td className="mono ellipsis" style={{ fontSize: "var(--fs-sub)", maxWidth: 0 }}>{r.message}</td>
                      <td>{r.trace && <button type="button" className="v3-iconbtn" style={{ width: 24, height: 24 }} aria-label={tx("查看链路", "View trace")} onClick={() => nav(`/traces/${r.trace}`)}><Icon name="branch" size={14} color="var(--accent-text)" /></button>}</td>
                    </tr>
                  ))}
                  {data && !rows.length && (
                    <tr>
                      <td colSpan={5} className="muted" style={{ textAlign: "center", padding: 24 }}>
                        {tx("这个时间范围内没有匹配的日志", "No matching lines in this range")}
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </Card>
            )}
          </div>
        </div>
        <aside className="aside" style={{ width: 360 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="sparkles" tone="accent" />
            <Col gap={0}>
              <Strong size={13.5}>{tx("日志模式", "Log patterns")}</Strong>
              <Muted size={11.5}>{tx("把相似日志聚成模板 · {r}", "Similar lines clustered into templates · {r}", { r: rangeLabel })}</Muted>
            </Col>
          </Row>
          {data && data.patterns === null && (
            <div style={{ padding: 16 }}>
              <Unavailable reason={u.patterns} source={tx("日志模式", "log patterns")} icon="sparkles" />
            </div>
          )}
          <ul className="log-patterns">
            {data?.patterns?.map((p) => (
              <li key={p.template} className={cx(p.new && "new")}>
                <Row gap={6}>
                  <Badge tone={LV[p.level]} mono>
                    {p.level}
                  </Badge>
                  <Num>{p.count}</Num>
                  {p.new && <Badge tone="danger">{API_MODE === "mock" ? tx("新出现 · 14:05", "New · 14:05") : tx("新出现", "New")}</Badge>}
                  <Spacer />
                  <Sparkline values={p.trend} width={70} height={18} color={p.level === "ERROR" ? "var(--danger)" : "var(--muted)"} />
                </Row>
                <span className="mono" style={{ fontSize: "var(--fs-sub)", lineHeight: 1.5 }}>{p.template}</span>
              </li>
            ))}
          </ul>
          <Row gap={6} style={{ padding: "0 16px 16px" }}>
            {API_MODE === "mock" && (
              <Btn icon="branch" to="/canvas/INV-231">
                {tx("带入调查", "Add to investigation")}
              </Btn>
            )}
            <Btn kind="ghost" icon="bell" to="/alert-rules">
              {tx("按模式建告警", "Alert on pattern")}
            </Btn>
          </Row>
        </aside>
      </div>
    </Page>
  );
}
