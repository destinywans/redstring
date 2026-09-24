import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { Span, TraceDetail, TraceSearch } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Code, Input, LabelChip, Legend, Mono, Muted, Num, Pills, Row, Skeleton, Spacer, Strong, cx } from "../../ui/core";
import { Axis } from "../../charts";
import { useI18n } from "../../i18n";
import "./observe.css";

export default function TracesPage() {
  const { tx } = useI18n();
  const [errors, setErrors] = useState<"errors" | "all">("errors");
  const [min, setMin] = useState("1000");
  const list = useGet<TraceSearch>("/traces", { errors: errors === "errors", min_ms: min });
  const [tid, setTid] = useState("4bf92f35");
  const { data: tr } = useGet<TraceDetail>(`/traces/${tid}`);
  const [spanId, setSpanId] = useState("s4");
  const span: Span | undefined = tr?.spans.find((s) => s.id === spanId);
  return (
    <Page flush crumbs={[tx("资源与观测", "Resources"), tx("链路", "Traces")]} actions={<Chip icon="clock" caret>{tx("最近 30 分钟", "Last 30 min")}</Chip>}>
      <div className="trace-bar">
        <Chip caret>
          <Brand name="tempo" size={18} />
          {tx("Tempo · 生产", "Tempo · prod")}
        </Chip>
        <Chip caret>{tx("服务：checkout-api", "Service: checkout-api")}</Chip>
        <Chip caret>{tx("接口：POST /orders", "Endpoint: POST /orders")}</Chip>
        <Input value={min} onChange={(e) => setMin(e.target.value.replace(/\D/g, ""))} width={110} aria-label={tx("最小耗时（毫秒）", "Min duration (ms)")} right={<Muted>ms</Muted>} />
        <Pills value={errors} onChange={setErrors} options={[{ value: "errors", label: tx("错误", "Errors") }, { value: "all", label: tx("全部", "All") }]} />
        <Spacer />
        <Btn kind="primary" icon="search" h={30} onClick={() => list.refetch()}>
          {tx("搜索", "Search")}
        </Btn>
      </div>
      <div className="v3-split">
        <div className="side" style={{ width: 480, padding: "14px 16px", display: "flex", flexDirection: "column", gap: 10 }}>
          <Row gap={8}>
            <Strong>{tx("耗时分布", "Duration distribution")}</Strong>
            <Spacer />
            <Legend items={[[tx("正常", "OK"), "var(--accent-line)"], [tx("错误", "Error"), "var(--danger)"]]} />
          </Row>
          {list.data ? (
            <svg width="100%" viewBox="0 0 440 140" className="scatter" role="img" aria-label={tx("耗时散点图", "Duration scatter")}>
              {[35, 70, 105].map((y) => (
                <line key={y} x1={0} x2={440} y1={y} y2={y} className="ch-grid" />
              ))}
              <line x1={list.data.release_at} x2={list.data.release_at} y1={0} y2={140} className="ch-marker" style={{ stroke: "var(--muted)" }} />
              {list.data.points.map((p, i) => (
                <circle
                  key={i}
                  cx={p.t}
                  cy={140 - p.ms / 25}
                  r={p.id === tid ? 5.5 : 3.5}
                  className={cx("pt", p.id && "clickable")}
                  style={{ fill: p.error ? "var(--danger)" : "var(--accent-line)", stroke: p.id === tid ? "var(--text)" : "none", strokeWidth: 1.5 }}
                  onClick={() => p.id && setTid(p.id)}
                >
                  <title>{`${p.ms} ms`}</title>
                </circle>
              ))}
            </svg>
          ) : (
            <Skeleton h={140} />
          )}
          <Axis labels={["14:01", tx("14:03 发布", "14:03 release"), "14:16", "14:31"]} />
          <Card style={{ overflow: "hidden" }}>
            <table className="v3-table">
              <thead>
                <tr>
                  <th>Trace</th>
                  <th>{tx("入口", "Root")}</th>
                  <th>{tx("耗时", "Duration")}</th>
                  <th>Span</th>
                  <th>{tx("状态", "Status")}</th>
                  <th>{tx("时间", "Time")}</th>
                </tr>
              </thead>
              <tbody>
                {list.data?.items.map((t) => (
                  <tr key={t.id} className={cx("clickable", t.id === tid && "selected")} style={{ height: 36 }} onClick={() => setTid(t.id)}>
                    <td>
                      <Mono color="var(--text)">{t.id}…</Mono>
                    </td>
                    <td className="ellipsis">{t.op}</td>
                    <td>
                      <Num color={t.error ? "var(--danger)" : undefined}>{(t.duration_ms / 1000).toFixed(2)} s</Num>
                    </td>
                    <td>
                      <Num color="var(--muted)">{t.spans}</Num>
                    </td>
                    <td>{t.error ? <Badge tone="danger">{tx("错误", "Error")}</Badge> : <Muted>—</Muted>}</td>
                    <td>
                      <Muted>{t.time}</Muted>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </div>
        <div className="main" style={{ display: "flex", flexDirection: "column" }}>
          {!tr ? (
            <Skeleton h={300} style={{ margin: 16 }} />
          ) : (
            <>
              <div style={{ padding: "14px 16px 10px", borderBottom: "1px solid var(--border)" }} className="anim-fade" key={tr.id}>
                <Row gap={6} wrap>
                  <b style={{ fontSize: 14 }}>{tr.op}</b>
                  <Num color="var(--danger)">{(tr.duration_ms / 1000).toFixed(2)} s</Num>
                  <Badge>{tx("{n} 个 span", "{n} spans", { n: tr.spans.length + 6 })}</Badge>
                  <Badge tone="danger">{tx("1 个错误", "1 error")}</Badge>
                  <Spacer />
                  <Btn h={26} icon="external">
                    {tx("在 Jaeger 中打开", "Open in Jaeger")}
                  </Btn>
                  <Btn h={26} kind="soft" icon="branch" to="/canvas/INV-231">
                    {tx("带入调查", "Add to investigation")}
                  </Btn>
                </Row>
                <Muted size={11.5}>
                  trace {tr.id}77b34da6a3ce929d0e0e4736 · {tr.time}
                </Muted>
              </div>
              <ol className="waterfall" key={tr.id + "w"}>
                <li className="wf-axis">
                  <span className="wf-name" />
                  <span className="wf-track">
                    <Axis labels={["0", "0.8 s", "1.6 s", "2.4 s", "3.2 s"]} style={{ marginTop: 0 }} />
                  </span>
                  <span className="wf-dur" />
                </li>
                {tr.spans.map((s, i) => {
                  const l = (100 * s.start) / tr.duration_ms;
                  const w = Math.max(0.6, (100 * (s.end - s.start)) / tr.duration_ms);
                  return (
                    <li key={s.id} className={cx("wf-row", s.id === spanId && "on", s.status === "error" && s.depth > 1 && "err")} onClick={() => setSpanId(s.id)} style={{ animationDelay: `${i * 40}ms` }}>
                      <span className="wf-name" style={{ paddingLeft: s.depth * 14 }}>
                        <Muted size={11.5}>{s.service}</Muted>
                        <span className="ellipsis" style={{ fontSize: 12, fontWeight: 500 }}>
                          {s.op}
                        </span>
                      </span>
                      <span className="wf-track">
                        <span className="wf-bar" style={{ left: `${l}%`, width: `${w}%`, background: s.status === "error" && s.depth > 1 ? "var(--danger)" : s.service === "orders-db" ? "var(--ok)" : "var(--accent-line)" }} />
                      </span>
                      <span className="wf-dur tnum">{s.label ?? `${s.end - s.start} ms`}</span>
                    </li>
                  );
                })}
              </ol>
              {span && (
                <div className="span-detail anim-fade" key={span.id}>
                  <Row gap={8}>
                    <Strong>{span.op}</Strong>
                    {span.status === "error" && <Badge tone="danger">{tx("错误", "Error")}</Badge>}
                    <Muted size={11.5}>
                      {span.service} · {span.end - span.start} ms
                    </Muted>
                  </Row>
                  {span.attrs && (
                    <Row gap={5} wrap>
                      {span.attrs.map((a) => (
                        <LabelChip key={a.k} k={a.k} v={a.v} />
                      ))}
                    </Row>
                  )}
                  {span.error && <Code>{span.error}</Code>}
                  {!span.attrs && !span.error && <Muted>{tx("没有额外属性。", "No extra attributes.")}</Muted>}
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </Page>
  );
}

