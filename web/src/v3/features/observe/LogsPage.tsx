import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { LogLine, LogSearch } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, Input, Muted, Num, Row, Spacer, Strong, Tile, Toggle, cx } from "../../ui/core";
import { StackedBars, Sparkline } from "../../charts";
import { useI18n } from "../../i18n";
import "./observe.css";
import { RangeChip, openExternal, ChipSelect } from "../../ui/demo";

const LV = { ERROR: "danger", WARN: "warn", INFO: "neutral" } as const;

export default function LogsPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [query, setQuery] = useState('service:"checkout-api" and level:(error or warn)');
  const [applied, setApplied] = useState("");
  const [levels, setLevels] = useState<Set<LogLine["level"]>>(new Set(["ERROR", "WARN", "INFO"]));
  const [live, setLive] = useState(false);
  const [extra, setExtra] = useState<LogLine[]>([]);
  const { data } = useGet<LogSearch>("/logs", { q: applied, levels: [...levels].join(",") });
  useEffect(() => {
    if (!live) return;
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
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("日志", "Logs")]}
      actions={
        <>
          <RangeChip defaultValue="30m" />
          <Btn icon="external" onClick={() => openExternal("Kibana", `https://kibana.example.internal/app/discover#/?_a=(query:(language:kuery,query:'${encodeURIComponent(query)}'))`)}>{tx("在 Kibana 中打开", "Open in Kibana")}</Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <form className="v3-row" style={{ gap: 8 }} onSubmit={(e) => { e.preventDefault(); setApplied(query.includes("checkout") ? "checkout" : query); }}>
              <ChipSelect icon="database" label="" showLabel={false} defaultValue="es" options={[{ value: "es", label: tx("Elasticsearch · 生产", "Elasticsearch · prod") }, { value: "loki", label: "Loki · prod" }]} />
              <ChipSelect label="" showLabel={false} defaultValue="kql" options={[{ value: "kql", label: "KQL" }, { value: "lucene", label: "Lucene" }]} />
              <Input mono value={query} onChange={(e) => setQuery(e.target.value)} style={{ flexGrow: 1 }} aria-label={tx("日志查询", "Log query")} />
              <Btn kind="primary" icon="search" h={32} type="submit">
                {tx("搜索", "Search")}
              </Btn>
              <Muted>{tx("实时", "Live")}</Muted>
              <Toggle on={live} onChange={setLive} label={tx("实时", "Live")} />
            </form>
            <Card style={{ padding: "10px 14px" }}>
              <Row gap={8}>
                <Strong>{tx("日志量", "Volume")}</Strong>
                <Muted size={11.5}>{tx("共 {a} 条 · 错误 {b}", "{a} lines · {b} errors", { a: data?.total ?? "…", b: data?.errors ?? "…" })}</Muted>
                <Spacer />
                {(["ERROR", "WARN", "INFO"] as const).map((l) => (
                  <Chip key={l} active={levels.has(l)} onClick={() => toggle(l)}>
                    <i style={{ width: 8, height: 8, borderRadius: 2, background: l === "ERROR" ? "var(--c3)" : l === "WARN" ? "var(--c4)" : "var(--c5s)" }} />
                    {l}
                  </Chip>
                ))}
              </Row>
              {data && <StackedBars data={data.histogram} colors={["var(--c3)", "var(--c4)", "var(--c5s)"]} height={70} names={["ERROR", "WARN", "INFO"]} gap={3} />}
            </Card>
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
                      <td className="mono muted" style={{ fontSize: "var(--fs-sub)" }}>{r.time}</td>
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
                </tbody>
              </table>
            </Card>
          </div>
        </div>
        <aside className="aside" style={{ width: 360 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="sparkles" tone="accent" />
            <Col gap={0}>
              <Strong size={13.5}>{tx("日志模式", "Log patterns")}</Strong>
              <Muted size={11.5}>{tx("AI 把相似日志聚成模板 · 最近 30 分钟", "AI clusters similar lines into templates · last 30 min")}</Muted>
            </Col>
          </Row>
          <ul className="log-patterns">
            {data?.patterns.map((p) => (
              <li key={p.template} className={cx(p.new && "new")}>
                <Row gap={6}>
                  <Badge tone={LV[p.level]} mono>
                    {p.level}
                  </Badge>
                  <Num>{p.count}</Num>
                  {p.new && <Badge tone="danger">{tx("新出现 · 14:05", "New · 14:05")}</Badge>}
                  <Spacer />
                  <Sparkline values={p.trend} width={70} height={18} color={p.level === "ERROR" ? "var(--danger)" : "var(--muted)"} />
                </Row>
                <span className="mono" style={{ fontSize: "var(--fs-sub)", lineHeight: 1.5 }}>{p.template}</span>
              </li>
            ))}
          </ul>
          <Row gap={6} style={{ padding: "0 16px 16px" }}>
            <Btn icon="branch" to="/canvas/INV-231">
              {tx("带入调查", "Add to investigation")}
            </Btn>
            <Btn kind="ghost" icon="bell" to="/alert-rules">
              {tx("按模式建告警", "Alert on pattern")}
            </Btn>
          </Row>
        </aside>
      </div>
    </Page>
  );
}
