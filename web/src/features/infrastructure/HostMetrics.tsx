import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { RefreshCw, Server } from "lucide-react";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { ErrorNotice } from "../../components/ui";
import type { TelemetryData } from "../observability/types";

const metrics = [
  ["vm_up", "指标采集", "state"],
  ["vm_cpu_used_percent", "CPU 使用率", "%"],
  ["vm_memory_used_percent", "内存使用率", "%"],
  ["vm_memory_available_bytes", "可用内存", "bytes"],
  ["vm_rootfs_used_percent", "根磁盘使用率", "%"],
  ["vm_network_receive_bytes_per_second", "网络接收", "bytes/s"],
  ["vm_network_transmit_bytes_per_second", "网络发送", "bytes/s"],
  ["vm_systemd_unit_state", "服务状态", "service"],
] as const;
type Registration = {
  metric_query_ids: string[];
  limits?: {
    max_window_seconds: number;
    max_lookback_seconds: number;
    min_step_seconds: number;
    max_samples_per_series: number;
  } | null;
};
type Reading = { id: string; data?: TelemetryData; error?: unknown };
function identity(labels: Record<string, string>) {
  return labels.instance || labels.node || "";
}
function latest(series: NonNullable<TelemetryData["series"]>[number]) {
  return series.samples.reduce<(typeof series.samples)[number] | undefined>(
    (value, sample) =>
      !value || sample.timestamp > value.timestamp ? sample : value,
    undefined,
  );
}
function format(value: number, unit: string) {
  if (unit === "%") return `${value.toFixed(1)} %`;
  if (unit.startsWith("bytes")) {
    const scale = Math.max(
      0,
      Math.min(3, Math.floor(Math.log2(Math.max(1, Math.abs(value))) / 10)),
    );
    return `${(value / 1024 ** scale).toFixed(1)} ${["B", "KiB", "MiB", "GiB"][scale]}${unit.endsWith("/s") ? "/s" : ""}`;
  }
  return String(value);
}
export default function HostMetrics() {
  const { application, environment, preview } = useWorkspace();
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);
  const registration = useQuery({
    queryKey: ["telemetry", application?.id, environment?.id, preview],
    queryFn: () =>
      request<Registration>(
        `/applications/${application!.id}/telemetry?environment_id=${encodeURIComponent(environment!.id)}`,
      ),
    enabled: !!application && !!environment && !preview,
    retry: false,
  });
  const configured = metrics.filter(([id]) =>
    registration.data?.metric_query_ids.includes(id),
  );
  const readings = useQuery({
    queryKey: [
      "host-metrics",
      application?.id,
      environment?.id,
      environment?.binding_revision,
      configured.map(([id]) => id).join(","),
    ],
    enabled:
      !!application && !!environment && !preview && configured.length > 0,
    retry: false,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const end = new Date(),
        limits = registration.data?.limits;
      const seconds = Math.min(
        300,
        limits?.max_window_seconds ?? 300,
        limits?.max_lookback_seconds ?? 300,
      );
      const step = Math.max(
        limits?.min_step_seconds ?? 15,
        Math.ceil(
          seconds / Math.max(1, (limits?.max_samples_per_series ?? 100) - 1),
        ),
      );
      const values: Reading[] = await Promise.all(
        configured.map(async ([id]) => {
          try {
            const data = await request<TelemetryData>(
              `/applications/${application!.id}/metrics`,
              {
                method: "POST",
                signal,
                body: JSON.stringify({
                  environment_id: environment!.id,
                  query_id: id,
                  start: new Date(end.getTime() - seconds * 1000).toISOString(),
                  end: end.toISOString(),
                  step_seconds: step,
                }),
              },
            );
            return { id, data };
          } catch (error) {
            if (signal.aborted) throw error;
            return { id, error };
          }
        }),
      );
      return {
        values,
        requestedAt: end.getTime(),
        freshnessSeconds: Math.max(120, step * 2),
      };
    },
  });
  const values = readings.data?.values ?? [];
  const hosts = [
    ...new Set(
      values.flatMap(({ data }) =>
        (data?.series ?? [])
          .map((series) => identity(series.labels))
          .filter(Boolean),
      ),
    ),
  ].sort();
  const unassigned = values.some(({ data }) =>
    data?.series?.some((series) => !identity(series.labels)),
  );
  function fresh(sample: ReturnType<typeof latest>) {
    return (
      !!sample &&
      Number.isFinite(Number(sample.value)) &&
      Math.max(now, Date.now()) / 1000 - sample.timestamp <=
        (readings.data?.freshnessSeconds ?? 120)
    );
  }
  return (
    <section className="card infra-snapshot infra-hosts">
      <header>
        <div>
          <h2>登记主机指标</h2>
          <p>
            {application?.name || "尚未选择应用"} /{" "}
            {environment?.name || "尚未选择环境"}
          </p>
        </div>
        <button
          className="btn"
          disabled={
            preview ||
            registration.isFetching ||
            readings.isFetching ||
            !configured.length
          }
          onClick={() => void readings.refetch()}
        >
          <RefreshCw size={15} />
          {readings.isFetching ? "采集中…" : "刷新指标"}
        </button>
      </header>
      <p className="infra-scope-note">
        通过当前环境登记的 Prometheus 查询读取，按 instance / node
        标签归属。这是指标快照，不是自动发现的资产清单；采集正常不代表业务健康。
      </p>
      {registration.error && <ErrorNotice error={registration.error} />}
      {readings.error && <ErrorNotice error={readings.error} />}
      {registration.isPending && !preview && (
        <p role="status">正在读取主机指标配置…</p>
      )}
      {(preview || (!registration.isPending && !configured.length)) && (
        <div className="infra-empty">
          <Server size={26} />
          <h3>
            {preview ? "设计预览没有真实主机数据" : "当前环境未配置主机指标"}
          </h3>
          <p>
            为当前应用环境登记 vm_* 查询后，可在此查看
            CPU、内存、磁盘、网络及服务状态。
          </p>
        </div>
      )}
      {readings.isFetching && (
        <p role="status" className="infra-scope-note">
          正在读取指标；已有内容是上次采集的快照。
        </p>
      )}
      {readings.data && (
        <p className="infra-scope-note">
          查询截止：
          {new Date(readings.data.requestedAt).toLocaleString("zh-CN")} ·
          手动刷新更新快照
        </p>
      )}
      {values
        .filter((value) => value.error)
        .map((value) => (
          <div key={value.id}>
            <small>{value.id}</small>
            <ErrorNotice error={value.error} />
          </div>
        ))}
      {unassigned && (
        <p className="management-notice">
          有指标缺少 instance / node 标签，无法可靠归属主机，未混入下方卡片。
        </p>
      )}
      {readings.data && !hosts.length && (
        <p className="infra-empty">
          本次未返回可归属主机的数据；请检查查询配置、标签或采集状态。
        </p>
      )}
      {hosts.map((host) => {
        const up =
          values
            .find((value) => value.id === "vm_up")
            ?.data?.series?.filter(
              (series) => identity(series.labels) === host,
            ) ?? [];
        const available =
          up.length === 1 &&
          fresh(latest(up[0])) &&
          Number(latest(up[0])?.value) === 1;
        const down = up.some(
          (series) =>
            fresh(latest(series)) && Number(latest(series)?.value) === 0,
        );
        return (
          <article key={host} className="infra-host">
            <div className="infra-host-heading">
              <h3>
                <Server size={17} /> {host}
              </h3>
              <span
                className={`badge ${available ? "" : "infra-host-unavailable"}`}
              >
                {available
                  ? "指标采集正常"
                  : down
                    ? "指标采集异常"
                    : "采集状态未知或已过期"}
              </span>
            </div>
            {!available && (
              <p className="management-notice">
                采集未确认正常，资源与服务指标不展示为当前值。请刷新或检查采集器。
              </p>
            )}
            <div className="infra-host-grid">
              {metrics
                .filter(([id]) => id !== "vm_up")
                .map(([id, label, unit]) => {
                  const reading = values.find((value) => value.id === id);
                  const series =
                    reading?.data?.series?.filter(
                      (item) => identity(item.labels) === host,
                    ) ?? [];
                  const missing = !configured.some(([value]) => value === id);
                  return (
                    <div className="infra-host-metric" key={id}>
                      <span>{label}</span>
                      {missing ? (
                        <strong>未配置</strong>
                      ) : reading?.error ? (
                        <strong>查询失败</strong>
                      ) : !available ? (
                        <strong>暂不可用</strong>
                      ) : !series.length ? (
                        <strong>无数据</strong>
                      ) : (
                        series.map((item, index) => {
                          const sample = latest(item),
                            valid = fresh(sample);
                          return (
                            <div key={index}>
                              <strong>
                                {!valid
                                  ? "样本已过期或无效"
                                  : unit === "service"
                                    ? `${item.labels.name || item.labels.unit || "未标记服务"} · ${item.labels.state || "未标记状态"}：${Number(sample!.value) === 1 ? "是" : "否"}`
                                    : format(Number(sample!.value), unit)}
                              </strong>
                              {item.labels.device && (
                                <small>设备：{item.labels.device}</small>
                              )}
                              {sample && (
                                <small>
                                  样本：
                                  {new Date(
                                    sample.timestamp * 1000,
                                  ).toLocaleString("zh-CN")}
                                </small>
                              )}
                            </div>
                          );
                        })
                      )}
                      <small>
                        来源：{reading?.data?.source?.provider ?? "Prometheus"}{" "}
                        · {id}
                      </small>
                      {reading?.data?.source?.captured_at && (
                        <small>
                          采集：
                          {new Date(
                            reading.data.source.captured_at,
                          ).toLocaleString("zh-CN")}
                        </small>
                      )}
                      {reading?.data?.source?.warnings?.map((warning) => (
                        <small key={warning}>{warning}</small>
                      ))}
                      {reading?.data?.limit_reached && (
                        <small>返回结果达到上限，当前数据不完整。</small>
                      )}
                    </div>
                  );
                })}
            </div>
          </article>
        );
      })}
    </section>
  );
}
