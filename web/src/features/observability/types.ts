import type { TelemetryResultData } from "../overview/TelemetryResult";
export type Mode = "metrics" | "logs";
export type TelemetryData = TelemetryResultData & {
  source?: NonNullable<TelemetryResultData["source"]> & {
    url?: string;
    application_id?: string;
    http_status?: number;
    failure?: { message: string };
    window?: { start: string; end: string; step_seconds?: number };
  };
};
export type QuerySnapshot = {
  id: string;
  signature: string;
  mode: Mode;
  applicationId: string;
  applicationName: string;
  environmentId: string;
  environmentName: string;
  queryId: string;
  requestedWindow: { start: string; end: string };
  receivedAt: string;
  data: TelemetryData;
};
export type Selection =
  | { kind: "returned_result" }
  | { kind: "metric_series"; index: number }
  | { kind: "log_entry"; index: number };
export type Transfer = { snapshot: QuerySnapshot; selection: Selection };
export function selectionLabel(value: Selection) {
  return value.kind === "returned_result"
    ? "本次返回结果"
    : value.kind === "metric_series"
      ? `指标序列 ${value.index + 1}`
      : `日志第 ${value.index + 1} 条`;
}
export function snapshotContent({ snapshot, selection }: Transfer) {
  const data = snapshot.data;
  // Preserve provenance and upstream limitations; a selected row is never a complete query.
  return JSON.stringify({
    ...data,
    ...(snapshot.mode === "metrics"
      ? {
          series:
            selection.kind === "metric_series"
              ? [data.series?.[selection.index]].filter(Boolean)
              : data.series,
        }
      : {
          entries:
            selection.kind === "log_entry"
              ? [data.entries?.[selection.index]].filter(Boolean)
              : data.entries,
        }),
    observation: {
      application_id: snapshot.applicationId,
      environment_id: snapshot.environmentId,
      requested_window: snapshot.requestedWindow,
      received_at: snapshot.receivedAt,
      query_id: snapshot.queryId,
    },
    selection: {
      ...selection,
      description: selectionLabel(selection),
      partial_selection: selection.kind !== "returned_result",
      returned_series: data.series?.length ?? 0,
      returned_entries: data.entries?.length ?? 0,
      complete_dataset: false,
      note: "用户保存的历史查询快照；仅包含本次返回或选中的数据，不能代表所有遥测数据。",
    },
  });
}
export function timeLabel(value?: string) {
  if (!value) return "未提供";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "时间不可解析"
    : date.toLocaleString("zh-CN");
}
