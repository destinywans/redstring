import { useState } from "react";
import TelemetryResult from "../overview/TelemetryResult";
import { type QuerySnapshot, type Selection, timeLabel } from "./types";

function logLevel(labels: Record<string, string>, line: string) {
  const value = (
    labels.level ||
    labels.severity ||
    labels.log_level ||
    ""
  ).toLowerCase();
  if (value)
    return { value: value === "warning" ? "warn" : value, inferred: false };
  const match = line.match(/\b(error|warn(?:ing)?|info|debug|fatal|trace)\b/i);
  return {
    value: match?.[1]?.toLowerCase().replace("warning", "warn") ?? "unknown",
    inferred: !!match,
  };
}
export default function ObservationResults({
  snapshot,
  canTransfer,
  onTransfer,
}: {
  snapshot: QuerySnapshot;
  canTransfer: boolean;
  onTransfer: (selection: Selection) => void;
}) {
  const [filter, setFilter] = useState(""),
    [level, setLevel] = useState("all");
  const result = snapshot.data;
  const entries = (result.entries ?? [])
    .map((entry, index) => ({
      ...entry,
      index,
      severity: logLevel(entry.labels, entry.line),
    }))
    .filter(
      (entry) =>
        entry.line.toLowerCase().includes(filter.toLowerCase()) &&
        (level === "all" || entry.severity.value === level),
    );
  const count =
    snapshot.mode === "metrics"
      ? (result.series?.length ?? 0)
      : (result.entries?.length ?? 0);
  return (
    <div className="observation-data">
      <dl className="observation-provenance">
        <div>
          <dt>来源</dt>
          <dd>{result.source?.provider || "未提供来源"}</dd>
        </div>
        <div>
          <dt>来源采集时间</dt>
          <dd>{timeLabel(result.source?.captured_at)}</dd>
        </div>
        <div>
          <dt>实际查询窗口</dt>
          <dd>
            {timeLabel(result.source?.window?.start)} —{" "}
            {timeLabel(result.source?.window?.end)}
          </dd>
        </div>
        {snapshot.mode === "metrics" && result.source?.window?.step_seconds && (
          <div>
            <dt>实际采样步长</dt>
            <dd>{result.source.window.step_seconds} 秒</dd>
          </div>
        )}
        <div>
          <dt>返回数量</dt>
          <dd>
            {count} {snapshot.mode === "metrics" ? "条序列" : "条日志"}
            {result.limit_reached ? " · 已到达上限" : ""}
          </dd>
        </div>
      </dl>
      {result.source?.query && (
        <details className="observation-expression">
          <summary>查看实际查询表达式</summary>
          <pre>{result.source.query}</pre>
        </details>
      )}
      {result.source?.failure && (
        <p className="management-notice">
          数据源报告异常：{result.source.failure.message}
        </p>
      )}
      {result.source?.warnings?.map((warning, i) => (
        <p className="management-notice" key={i}>
          {warning}
        </p>
      ))}
      {(result.empty || !count) && (
        <p className="management-notice">
          此窗口没有返回{snapshot.mode === "metrics" ? "指标序列" : "日志"}
          ，不能据此认定服务健康。
        </p>
      )}
      {result.limit_reached && (
        <p className="management-notice">
          结果达到服务端返回上限，只展示部分数据。请缩小时间范围；固定快照会保留此限制说明。
        </p>
      )}
      {snapshot.mode === "metrics" ? (
        <div className="observation-series-grid">
          {(result.series ?? []).map((series, index) => (
            <section
              className="observation-series"
              key={`${snapshot.id}-${index}`}
            >
              <div className="observation-series-heading">
                <h3>序列 {index + 1}</h3>
                <button
                  className="btn"
                  disabled={!canTransfer}
                  onClick={() => onTransfer({ kind: "metric_series", index })}
                >
                  选此序列调查
                </button>
              </div>
              <TelemetryResult
                result={{ source: result.source, series: [series] }}
              />
              <details>
                <summary>采样值（{series.samples.length} 个）</summary>
                <div className="management-table-wrap observation-sample-table">
                  <table className="management-table">
                    <thead>
                      <tr>
                        <th>时间</th>
                        <th>原始数值</th>
                      </tr>
                    </thead>
                    <tbody>
                      {series.samples.map((sample, sampleIndex) => (
                        <tr key={sampleIndex}>
                          <td>
                            {timeLabel(
                              new Date(sample.timestamp * 1000).toString(),
                            )}
                          </td>
                          <td>{sample.value}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </details>
            </section>
          ))}
        </div>
      ) : (
        <>
          <div className="observation-log-filters">
            <label>
              筛选已返回的日志
              <input
                className="field"
                placeholder="按日志文本筛选…"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
            </label>
            <label>
              级别
              <select
                className="field"
                value={level}
                onChange={(e) => setLevel(e.target.value)}
              >
                <option value="all">全部级别</option>
                {[
                  "fatal",
                  "error",
                  "warn",
                  "info",
                  "debug",
                  "trace",
                  "unknown",
                ].map((value) => (
                  <option key={value} value={value}>
                    {value === "unknown" ? "未标明" : value.toUpperCase()}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <p className="muted observation-help">
            筛选仅作用于本次已返回的 {result.entries?.length ?? 0}{" "}
            条日志，当前显示 {entries.length}{" "}
            条。顶部“带入调查”固定本次返回结果，行内按钮仅固定选中日志。
          </p>
          <div className="observation-log-list">
            {entries.map((entry) => (
              <article key={`${snapshot.id}-${entry.index}`}>
                <header>
                  <time>{timeLabel(entry.timestamp)}</time>
                  <span className="badge">
                    {entry.severity.value.toUpperCase()}
                    {entry.severity.inferred ? " · 文本推断" : ""}
                  </span>
                  <button
                    className="btn"
                    disabled={!canTransfer}
                    onClick={() =>
                      onTransfer({ kind: "log_entry", index: entry.index })
                    }
                  >
                    选此日志调查
                  </button>
                </header>
                <pre>{entry.line}</pre>
                <details>
                  <summary>来源标签</summary>
                  <dl>
                    {Object.entries(entry.labels).map(([key, value]) => (
                      <div key={key}>
                        <dt>{key}</dt>
                        <dd>{value}</dd>
                      </div>
                    ))}
                  </dl>
                </details>
              </article>
            ))}
          </div>
          {!entries.length && !!count && (
            <p className="management-empty">
              没有匹配当前文本与级别筛选的日志。
            </p>
          )}
        </>
      )}
    </div>
  );
}
