import { useQuery } from "@tanstack/react-query";
import {
  CartesianGrid,
  ResponsiveContainer,
  Scatter,
  ScatterChart,
  Tooltip,
  XAxis,
  YAxis,
  ZAxis,
} from "recharts";
import { request } from "../../lib/api";
import { ErrorNotice } from "../../components/ui";
import { moment } from "./review-types";
import type { InfrastructureOverview, OverviewNode } from "./overview-types";
import "./compute-overview.css";

function percentage(value: number | null | undefined) {
  return typeof value === "number" && Number.isFinite(value)
    ? `${value.toFixed(1)}%`
    : "—";
}
function capacity(value: number | null | undefined, memory = false) {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0)
    return "—";
  return `${(memory ? value / 1024 ** 3 : value).toLocaleString("zh-CN", { maximumFractionDigits: 1 })} ${memory ? "GiB" : "核"}`;
}
function Gauge({
  label,
  value,
  totalCapacity,
  covered,
  total,
  memory = false,
  containerScoped = false,
}: {
  label: string;
  value: number | null | undefined;
  totalCapacity: number | null | undefined;
  covered: number | undefined;
  total: number | undefined;
  memory?: boolean;
  containerScoped?: boolean;
}) {
  const available = typeof value === "number" && Number.isFinite(value);
  const perimeter = 2 * Math.PI * 36;
  const color = memory ? "#359a9e" : "var(--accent)";
  return (
    <div className="infra-compute-gauge">
      <div
        className="infra-compute-ring"
        role="img"
        aria-label={`${label}使用率 ${percentage(value)}`}
      >
        <svg viewBox="0 0 88 88" aria-hidden="true">
          <circle cx="44" cy="44" r="36" className="infra-compute-ring-track" />
          {available && (
            <circle
              cx="44"
              cy="44"
              r="36"
              stroke={color}
              strokeDasharray={`${(perimeter * Math.max(0, Math.min(100, value))) / 100} ${perimeter}`}
              transform="rotate(-90 44 44)"
              strokeLinecap="round"
            />
          )}
        </svg>
        <span>{percentage(value)}</span>
      </div>
      <div>
        <h3>{label}使用率</h3>
        <p>
          {containerScoped ? "对象容量合计" : "总容量"}{" "}
          <span>{capacity(totalCapacity, memory)}</span>
        </p>
        <small>
          {covered === undefined || total === undefined
            ? "正在读取采样范围…"
            : `${covered} / ${total} 项有有效采样与容量`}
        </small>
      </div>
    </div>
  );
}

export default function ComputeOverview({
  kind,
  scope,
  sourceKind,
  applicationId,
  onSelect,
}: {
  kind: "node" | "host";
  scope: string;
  sourceKind: string;
  applicationId: string;
  onSelect: (id: string) => void;
}) {
  const overview = useQuery({
    queryKey: ["infrastructure-overview", scope, sourceKind, applicationId],
    queryFn: ({ signal }) => {
      const query = new URLSearchParams({ environment_scope: scope || "all" });
      if (sourceKind) query.set("source_kind", sourceKind);
      if (applicationId) query.set("application_id", applicationId);
      return request<InfrastructureOverview>(`/resources/overview?${query}`, {
        signal,
      });
    },
    refetchInterval: 30000,
  });
  const group = overview.data?.compute_groups?.[kind];
  const resources = (overview.data?.nodes ?? []).filter(
    (node) => node.kind === kind,
  );
  const points = resources.filter(
    (node) =>
      ["current", "partial"].includes(node.metrics_status) &&
      typeof node.cpu_percent === "number" &&
      Number.isFinite(node.cpu_percent) &&
      typeof node.memory_percent === "number" &&
      Number.isFinite(node.memory_percent),
  );
  const label = kind === "node" ? "节点" : "主机与容器";
  const containerCount = group?.container_scoped ?? 0;
  return (
    <section
      className="infra-compute-overview"
      aria-label={`${label}容量与使用分布`}
    >
      {overview.error && (
        <div className="infra-compute-error">
          <ErrorNotice error={overview.error} />
        </div>
      )}
      <section className="infra-compute-panel infra-compute-capacity">
        <header>
          <h2>容量与使用率</h2>
          <span>当前范围全部{label}</span>
        </header>
        <div className="infra-compute-gauges">
          <Gauge
            label="CPU"
            value={group?.cpu_percent}
            totalCapacity={group?.capacity_cpu}
            covered={group?.cpu_covered}
            total={group?.total}
            containerScoped={containerCount > 0}
          />
          <Gauge
            label="内存"
            value={group?.memory_percent}
            totalCapacity={group?.capacity_memory_bytes}
            covered={group?.memory_covered}
            total={group?.total}
            containerScoped={containerCount > 0}
            memory
          />
        </div>
        <footer>
          {containerCount > 0
            ? `按各对象上限加权；${containerCount} 项采用容器口径，对象容量合计含共享上限，不代表物理总容量。`
            : "按有效采样对应的容量加权；容量不完整时总容量显示 —。"}
        </footer>
      </section>
      <section className="infra-compute-panel infra-compute-scatter">
        <header>
          <h2>CPU × 内存分布</h2>
          <span>
            {points.length} 项有效采样
            {overview.data?.nodes_truncated ? " · 部分资源" : ""}
          </span>
        </header>
        {points.length ? (
          <div className="infra-compute-scatter-chart">
            <ResponsiveContainer width="100%" height={184}>
              <ScatterChart
                margin={{ top: 9, right: 16, left: 0, bottom: 12 }}
                accessibilityLayer
              >
                <CartesianGrid stroke="var(--border)" strokeDasharray="3 4" />
                <XAxis
                  type="number"
                  dataKey="cpu_percent"
                  name="CPU 使用率"
                  unit="%"
                  domain={[0, 100]}
                  ticks={[0, 25, 50, 75, 100]}
                  tick={{ fill: "var(--muted)", fontSize: 11 }}
                  tickLine={false}
                  axisLine={false}
                  label={{
                    value: "CPU (%)",
                    position: "insideBottomRight",
                    offset: -9,
                    fill: "var(--muted)",
                    fontSize: 11,
                  }}
                />
                <YAxis
                  type="number"
                  dataKey="memory_percent"
                  name="内存使用率"
                  unit="%"
                  domain={[0, 100]}
                  ticks={[0, 50, 100]}
                  width={52}
                  tick={{ fill: "var(--muted)", fontSize: 11 }}
                  tickLine={false}
                  axisLine={false}
                  label={{
                    value: "内存 (%)",
                    position: "insideLeft",
                    angle: -90,
                    offset: 6,
                    fill: "var(--muted)",
                    fontSize: 11,
                  }}
                />
                <ZAxis range={[44, 44]} />
                <Tooltip
                  cursor={{ strokeDasharray: "3 3", stroke: "var(--muted)" }}
                  content={({ active, payload }) => {
                    const point = payload?.[0]?.payload as
                      OverviewNode | undefined;
                    return active && point ? (
                      <div className="infra-compute-tooltip">
                        <strong>{point.name}</strong>
                        <span>
                          {point.metrics_scope === "container"
                            ? "容器口径 · 容器上限可能来自共享宿主机"
                            : "主机 / 节点容量口径"}
                        </span>
                        <dl>
                          <div>
                            <dt>CPU</dt>
                            <dd>{percentage(point.cpu_percent)}</dd>
                          </div>
                          <div>
                            <dt>内存</dt>
                            <dd>{percentage(point.memory_percent)}</dd>
                          </div>
                          <div>
                            <dt>
                              {point.metrics_scope === "container"
                                ? "CPU 上限"
                                : "CPU 容量"}
                            </dt>
                            <dd>{capacity(point.capacity_cpu)}</dd>
                          </div>
                          <div>
                            <dt>
                              {point.metrics_scope === "container"
                                ? "内存上限"
                                : "内存容量"}
                            </dt>
                            <dd>
                              {capacity(point.capacity_memory_bytes, true)}
                            </dd>
                          </div>
                        </dl>
                      </div>
                    ) : null;
                  }}
                />
                <Scatter
                  name={label}
                  data={points}
                  fill="var(--accent)"
                  fillOpacity={0.72}
                  stroke="var(--panel)"
                  strokeWidth={1.5}
                  isAnimationActive={false}
                  onClick={(entry) => {
                    const point = entry.payload as OverviewNode | undefined;
                    if (point?.id) onSelect(point.id);
                  }}
                  cursor="pointer"
                />
              </ScatterChart>
            </ResponsiveContainer>
          </div>
        ) : (
          <div className="infra-compute-empty">
            {overview.isLoading
              ? "正在读取真实采样…"
              : overview.error
                ? "暂时无法读取指标分布"
                : "暂无同时包含 CPU 与内存的有效采样"}
          </div>
        )}
        <footer>
          点击采样查看详情 ·{" "}
          {overview.data
            ? `更新于 ${moment(overview.data.generated_at)}`
            : "等待采集"}
        </footer>
      </section>
    </section>
  );
}
