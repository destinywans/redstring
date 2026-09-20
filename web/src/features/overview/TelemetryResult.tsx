import { useState } from "react";
import { MetricChart } from "../../components/MetricChart";
import "../workspace/workspace.css";
export type TelemetryResultData = {
  source?: {
    provider?: string;
    query?: string;
    query_id?: string;
    captured_at?: string;
    window?: { start: string; end: string };
    warnings?: string[];
  };
  series?: {
    labels: Record<string, string>;
    samples: { timestamp: number; value: string }[];
  }[];
  entries?: {
    timestamp: string;
    line: string;
    labels: Record<string, string>;
  }[];
  empty?: boolean;
  limit_reached?: boolean;
};
export default function TelemetryResult({
  result,
}: {
  result: TelemetryResultData | null;
}) {
  const [filter, setFilter] = useState(""),
    [level, setLevel] = useState("all");
  if (!result)
    return <p className="management-empty">选择时间范围并读取指标或日志。</p>;
  const entries = (result.entries ?? []).filter(
    (e) =>
      e.line.toLowerCase().includes(filter.toLowerCase()) &&
      (level === "all" ||
        `${e.labels?.level ?? ""} ${e.line}`.toLowerCase().includes(level)),
  );
  return (
    <div>
      <p className="muted">
        {result.source?.provider ?? "数据源"} ·{" "}
        {result.source?.captured_at
          ? new Date(result.source.captured_at).toLocaleString("zh-CN")
          : "未记录采集时间"}
      </p>
      {result.source?.query && (
        <small className="muted">查询：{result.source.query}</small>
      )}
      {result.source?.warnings?.map((w) => (
        <p className="management-notice" key={w}>
          {w}
        </p>
      ))}
      {result.empty && (
        <p className="management-notice">
          该时间窗口没有返回数据，不能据此认定服务健康。
        </p>
      )}
      {result.limit_reached && (
        <p className="management-notice">
          结果达到返回上限。请缩小时间范围以查看完整数据。
        </p>
      )}
      {result.series?.map((s, i) => {
        const points = s.samples
          .map((p) => ({
            time: new Date(p.timestamp * 1000).toLocaleTimeString("zh-CN"),
            value: Number(p.value),
          }))
          .filter((p) => Number.isFinite(p.value));
        return (
          <section className="telemetry-series" key={i}>
            <small>
              {Object.entries(s.labels)
                .map(([k, v]) => `${k}=${v}`)
                .join(" · ") || "指标序列"}
            </small>
            {points.length ? (
              <MetricChart points={points} marker="" unit="" height={180} />
            ) : (
              <p>没有有效数值</p>
            )}
          </section>
        );
      })}
      {result.entries && (
        <>
          <div className="management-actions management-section">
            <input
              className="field"
              aria-label="筛选已返回日志"
              placeholder="筛选已返回日志中的文本…"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
            <select
              className="field"
              aria-label="日志级别"
              value={level}
              onChange={(e) => setLevel(e.target.value)}
            >
              <option value="all">全部级别</option>
              <option value="error">ERROR</option>
              <option value="warn">WARN</option>
              <option value="info">INFO</option>
              <option value="debug">DEBUG</option>
            </select>
          </div>
          <small className="muted">
            当前结果 {entries.length} / {result.entries.length}{" "}
            条，筛选仅作用于已返回日志。
          </small>
          <div className="telemetry-log">
            {entries.map((e, i) => (
              <article key={i}>
                <time>{new Date(e.timestamp).toLocaleString("zh-CN")}</time>
                {e.line}
              </article>
            ))}
            {!entries.length && (
              <p className="management-empty">没有匹配日志</p>
            )}
          </div>
        </>
      )}
    </div>
  );
}
