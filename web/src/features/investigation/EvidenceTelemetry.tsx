import { useMemo } from "react";
import TelemetryResult, {
  type TelemetryResultData,
} from "../overview/TelemetryResult";

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function labels(value: unknown): value is Record<string, string> {
  return (
    record(value) &&
    Object.values(value).every((item) => typeof item === "string")
  );
}
// Saved evidence can come from older versions or manual imports. Validate the
// rendering shape before passing it to charts; unknown formats remain readable.
function parseTelemetry(
  kind: string,
  content: string,
): TelemetryResultData | null {
  if (kind !== "metrics" && kind !== "logs") return null;
  try {
    const value: unknown = JSON.parse(content);
    if (!record(value)) return null;
    const result: TelemetryResultData = {};
    if (kind === "metrics") {
      if (!Array.isArray(value.series)) return null;
      const series: NonNullable<TelemetryResultData["series"]> = [];
      for (const item of value.series) {
        if (
          !record(item) ||
          !labels(item.labels) ||
          !Array.isArray(item.samples)
        )
          return null;
        const samples: { timestamp: number; value: string }[] = [];
        for (const point of item.samples) {
          if (
            !record(point) ||
            typeof point.timestamp !== "number" ||
            !Number.isFinite(point.timestamp) ||
            typeof point.value !== "string"
          )
            return null;
          samples.push({ timestamp: point.timestamp, value: point.value });
        }
        series.push({ labels: item.labels, samples });
      }
      result.series = series;
    } else {
      if (!Array.isArray(value.entries)) return null;
      const entries: NonNullable<TelemetryResultData["entries"]> = [];
      for (const item of value.entries) {
        if (
          !record(item) ||
          !labels(item.labels) ||
          typeof item.timestamp !== "string" ||
          typeof item.line !== "string"
        )
          return null;
        entries.push({
          labels: item.labels,
          timestamp: item.timestamp,
          line: item.line,
        });
      }
      result.entries = entries;
    }
    if (record(value.source)) {
      result.source = {};
      for (const field of [
        "provider",
        "query",
        "query_id",
        "captured_at",
      ] as const) {
        if (typeof value.source[field] === "string")
          result.source[field] = value.source[field];
      }
      if (
        Array.isArray(value.source.warnings) &&
        value.source.warnings.every((item) => typeof item === "string")
      )
        result.source.warnings = value.source.warnings;
    }
    if (typeof value.empty === "boolean") result.empty = value.empty;
    if (typeof value.limit_reached === "boolean")
      result.limit_reached = value.limit_reached;
    return result;
  } catch {
    return null;
  }
}
export default function EvidenceTelemetry({
  kind,
  content,
}: {
  kind: string;
  content: string;
}) {
  const data = useMemo(() => parseTelemetry(kind, content), [kind, content]);
  if (!data) return <pre>{content}</pre>;
  return (
    <div className="inv-evidence-telemetry nodrag nowheel">
      <p className="inv-evidence-snapshot-note">
        已保存的历史证据快照，仅展示当时返回或选中的数据，不代表当前健康状态或完整数据集。
      </p>
      <TelemetryResult result={data} />
      <details>
        <summary>原始证据 JSON 与来源信息</summary>
        <pre>{content}</pre>
      </details>
    </div>
  );
}
