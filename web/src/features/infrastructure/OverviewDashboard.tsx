import { useState, type CSSProperties, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { PageHeader } from "../../components/patterns/ListPage";
import { ErrorNotice } from "../../components/ui";
import { request } from "../../lib/api";
import { useEnvironmentScope } from "../../lib/environmentScope";
import { ArrowRight, RefreshCw } from "../../lib/icons";
import InfraResourceDrawer from "./InfraResourceDrawer";
import { kindLabels, moment } from "./review-types";
import type { InfrastructureOverview, MetricPoint } from "./overview-types";
import "./overview-dashboard.css";

const COLORS = ["var(--accent)", "#359a9e", "#8091b5", "#b390c8", "#b4905f"];
const kindViews: Record<string, string> = {
  cluster: "clusters",
  node: "nodes",
  deployment: "workloads",
  host: "hosts",
  namespace: "namespaces",
  network: "network",
  storage: "storage",
  pod: "pods",
  redis: "middleware",
  mysql: "middleware",
  kafka: "middleware",
  event: "events",
};
const formatCount = (value: number) => value.toLocaleString("zh-CN");
const percent = (value: number | null | undefined) =>
  typeof value === "number" && Number.isFinite(value)
    ? `${value.toFixed(1)}%`
    : "—";
const timeLabel = (value: string | number) => {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : date.toLocaleTimeString("zh-CN", {
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      });
};
const collectionLabel = (status: string) =>
  ({
    current: "采集正常",
    healthy: "采集正常",
    connected: "采集正常",
    disconnected: "采集失联",
    stale: "采集延迟",
    pending: "等待首次采集",
    unknown: "状态未知",
  })[status] ?? "状态未知";

function Panel({
  title,
  description,
  action,
  className = "",
  children,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section className={`infra-dashboard-panel ${className}`}>
      <header>
        <div>
          <h2>{title}</h2>
          {description && <p>{description}</p>}
        </div>
        {action}
      </header>
      {children}
    </section>
  );
}
function Empty({ children }: { children: ReactNode }) {
  return <div className="infra-dashboard-empty">{children}</div>;
}
function Trend({
  metric,
  points,
  value,
  containerScoped,
}: {
  metric: "cpu" | "memory";
  points: MetricPoint[];
  value: number | null;
  containerScoped: boolean;
}) {
  const label = metric === "cpu" ? "CPU" : "内存";
  const color = metric === "cpu" ? "var(--accent)" : "#359a9e";
  const data = points.map((point) => ({
    ...point,
    timestamp: new Date(point.time).getTime(),
  }));
  const hasData = points.some(
    (point) => point[metric] !== null && Number.isFinite(point[metric]),
  );
  return (
    <Panel
      title={`${label} 使用率`}
      description="Kubernetes 节点 · 按容量加权"
      action={<span className="infra-dashboard-metric">{percent(value)}</span>}
    >
      <div className="infra-dashboard-trend">
        {hasData ? (
          <ResponsiveContainer width="100%" height={192}>
            <AreaChart
              data={data}
              margin={{ top: 12, right: 12, left: -18, bottom: 0 }}
              accessibilityLayer
            >
              <defs>
                <linearGradient
                  id={`infra-overview-${metric}`}
                  x1="0"
                  y1="0"
                  x2="0"
                  y2="1"
                >
                  <stop offset="0%" stopColor={color} stopOpacity={0.2} />
                  <stop offset="100%" stopColor={color} stopOpacity={0.015} />
                </linearGradient>
              </defs>
              <CartesianGrid
                vertical={false}
                stroke="var(--border)"
                strokeDasharray="3 4"
              />
              <XAxis
                dataKey="timestamp"
                type="number"
                domain={["dataMin", "dataMax"]}
                tickFormatter={timeLabel}
                minTickGap={48}
                tick={{ fill: "var(--muted)", fontSize: 11 }}
                axisLine={false}
                tickLine={false}
              />
              <YAxis
                domain={[0, 100]}
                ticks={[0, 50, 100]}
                tickFormatter={(v) => `${v}%`}
                tick={{ fill: "var(--muted)", fontSize: 11 }}
                axisLine={false}
                tickLine={false}
              />
              <Tooltip
                contentStyle={{
                  background: "var(--panel)",
                  border: "1px solid var(--border)",
                  borderRadius: 8,
                  color: "var(--text)",
                  fontSize: 12,
                }}
                labelFormatter={(v) =>
                  moment(new Date(Number(v)).toISOString())
                }
                formatter={(v, _name, item) => {
                  const point = item.payload as MetricPoint | undefined;
                  const samples = point?.[metric === "cpu" ? "cpu_samples" : "memory_samples"];
                  return [
                    percent(typeof v === "number" ? v : null),
                    `${label} 使用率${typeof samples === "number" ? ` · 覆盖 ${samples} 个节点` : ""}`,
                  ];
                }}
              />
              <Area
                type="linear"
                dataKey={metric}
                stroke={color}
                strokeWidth={1.8}
                fill={`url(#infra-overview-${metric})`}
                connectNulls={false}
                isAnimationActive={false}
                dot={false}
                activeDot={{ r: 3 }}
              />
            </AreaChart>
          </ResponsiveContainer>
        ) : (
          <Empty>最近 15 分钟暂无可用{label}指标</Empty>
        )}
      </div>
      <footer>
        缺失采样保留空白，不计为 0%
        {containerScoped && " · 含容器节点，按各对象上限加权"}
      </footer>
    </Panel>
  );
}

export default function OverviewDashboard() {
  const scope = useEnvironmentScope();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [selected, setSelected] = useState<string | null>(null);
  const [heatMetric, setHeatMetric] = useState<"cpu" | "memory">("cpu");
  const application =
    params.get("application") ?? params.get("application_id") ?? "";
  const source = params.get("source_kind") ?? "";
  const overview = useQuery({
    queryKey: ["infrastructure-overview", scope.key, source, application],
    queryFn: ({ signal }) => {
      const query = new URLSearchParams({
        environment_scope: scope.key || "all",
      });
      if (source) query.set("source_kind", source);
      if (application) query.set("application_id", application);
      return request<InfrastructureOverview>(`/resources/overview?${query}`, {
        signal,
      });
    },
    refetchInterval: 30000,
  });
  const data = overview.data;
  function openView(view: string, filters: Record<string, string> = {}) {
    const query = new URLSearchParams({
      view,
      environment_scope: scope.key || "all",
      ...filters,
    });
    if (source) query.set("source_kind", source);
    if (application) query.set("application_id", application);
    navigate(`/infrastructure?${query}`);
  }
  function openKind(kind: string) {
    openView(
      kindViews[kind] ?? "summary",
      ["redis", "mysql", "kafka"].includes(kind) ? { category: kind } : {},
    );
  }
  const cards = [
    {
      key: "resources_total",
      label: "纳管资源",
      hint: "不含 Pod 与事件",
      view: "catalog",
    },
    {
      key: "clusters_total",
      label: "集群",
      hint: "Kubernetes",
      view: "clusters",
    },
    { key: "nodes_total", label: "节点", hint: "集群计算节点", view: "nodes" },
    {
      key: "hosts_total",
      label: "主机与容器",
      hint: "已登记运行宿主",
      view: "hosts",
    },
    {
      key: "workloads_total",
      label: "工作负载",
      hint: "含 ReplicaSet",
      view: "workloads",
    },
    { key: "runtime_pods_total", label: "运行实例", hint: "Pod", view: "pods" },
  ] as const;
  const distribution = [...(data?.distribution ?? [])].sort(
    (a, b) => b.count - a.count,
  );
  const maximum = Math.max(1, ...distribution.map((item) => item.count));
  const workloads = data?.workload_types ?? [];
  const workloadTotal = workloads.reduce((sum, item) => sum + item.count, 0);
  const visibleNodes = (data?.nodes ?? []).filter(
    (node) => node.kind === "node",
  );
  const nodes = visibleNodes.slice(0, 12);
  const heatTimes = data?.metrics.history.map((point) => point.time) ?? [];
  const heatmapTimes = heatTimes.length
    ? heatTimes
    : (nodes[0]?.history.map((point) => point.time) ?? []);
  return (
    <section className="infra-review infra-dashboard" aria-label="资源总览">
      <PageHeader
        title="资源总览"
        description="资源规模、运行状态与最近 15 分钟的使用情况。"
        actions={
          <div className="infra-dashboard-actions">
            <span className="infra-dashboard-window">最近 15 分钟</span>
            <button
              className="button"
              disabled={overview.isFetching}
              onClick={() => void overview.refetch()}
            >
              <RefreshCw size={15} />
              刷新
            </button>
          </div>
        }
      />
      {overview.error && <ErrorNotice error={overview.error} />}
      <div className="infra-dashboard-meta">
        <span>
          {data
            ? `更新于 ${moment(data.generated_at)} · 每 30 秒刷新`
            : "正在读取当前资源范围…"}
        </span>
        {data && data.stats.stale_resources_total > 0 && (
          <span className="infra-dashboard-delayed">
            {formatCount(data.stats.stale_resources_total)} 项采集延迟或失联
          </span>
        )}
      </div>
      <div className="infra-dashboard-counts" aria-label="资源数量统计">
        {cards.map((card) => (
          <button
            key={card.key}
            onClick={() =>
              card.view === "catalog"
                ? navigate(
                    `/resources/catalog?environment_scope=${encodeURIComponent(scope.key || "all")}${application ? `&application_id=${encodeURIComponent(application)}` : ""}`,
                  )
                : openView(card.view)
            }
          >
            <span>
              {card.label}
              <ArrowRight size={13} />
            </span>
            <strong>{data ? formatCount(data.stats[card.key]) : "—"}</strong>
            <small>{card.hint}</small>
          </button>
        ))}
      </div>
      {!data ? (
        <Empty>
          {overview.error
            ? "暂时无法读取资源总览，请刷新重试。"
            : "正在汇总资源与指标…"}
        </Empty>
      ) : (
        <>
          <div className="infra-dashboard-row infra-dashboard-row--wide">
            <Panel title="资源分布" description="按资源类型统计当前纳管数量">
              {distribution.length ? (
                <div className="infra-dashboard-bars">
                  {distribution.map((item) => (
                    <button
                      key={item.kind}
                      onClick={() => openKind(item.kind)}
                      title={`查看${item.label || kindLabels[item.kind]}：${item.count} 项`}
                    >
                      <span>
                        {item.label || kindLabels[item.kind] || item.kind}
                      </span>
                      <i>
                        <b
                          style={{
                            width: `${Math.max(0, (item.count / maximum) * 100)}%`,
                          }}
                        />
                      </i>
                      <em>{formatCount(item.count)}</em>
                    </button>
                  ))}
                </div>
              ) : (
                <Empty>当前范围尚无纳管资源</Empty>
              )}
            </Panel>
            <Panel
              title="工作负载类型"
              description="按 Kubernetes 控制器类型区分"
            >
              {workloadTotal ? (
                <>
                  <div className="infra-dashboard-donut">
                    <ResponsiveContainer width="100%" height={182}>
                      <PieChart accessibilityLayer>
                        <Pie
                          data={workloads}
                          dataKey="count"
                          nameKey="name"
                          innerRadius={57}
                          outerRadius={76}
                          paddingAngle={workloads.length > 1 ? 3 : 0}
                          stroke="var(--panel)"
                          isAnimationActive={false}
                          onClick={(entry) =>
                            entry.name &&
                            openView("workloads", {
                              filter_workload_type: entry.name,
                            })
                          }
                        >
                          {workloads.map((item, index) => (
                            <Cell
                              key={item.name}
                              fill={COLORS[index % COLORS.length]}
                              cursor="pointer"
                            />
                          ))}
                        </Pie>
                        <Tooltip
                          contentStyle={{
                            background: "var(--panel)",
                            border: "1px solid var(--border)",
                            borderRadius: 8,
                            fontSize: 12,
                            color: "var(--text)",
                          }}
                          formatter={(v) => [`${v} 个`, "数量"]}
                        />
                      </PieChart>
                    </ResponsiveContainer>
                    <div className="infra-dashboard-donut-total">
                      <strong>{formatCount(workloadTotal)}</strong>
                      <span>工作负载</span>
                    </div>
                  </div>
                  <div className="infra-dashboard-legend">
                    {workloads.map((item, index) => (
                      <button
                        key={item.name}
                        onClick={() =>
                          openView("workloads", {
                            filter_workload_type: item.name,
                          })
                        }
                      >
                        <i
                          style={{ background: COLORS[index % COLORS.length] }}
                        />
                        <span>{item.name}</span>
                        <em>{formatCount(item.count)}</em>
                      </button>
                    ))}
                  </div>
                </>
              ) : (
                <Empty>当前范围没有工作负载</Empty>
              )}
            </Panel>
          </div>
          <div className="infra-dashboard-row">
            <Trend
              metric="cpu"
              value={data.metrics.cpu_percent}
              points={data.metrics.history}
              containerScoped={
                (data.compute_groups?.node.container_scoped ?? 0) > 0
              }
            />
            <Trend
              metric="memory"
              value={data.metrics.memory_percent}
              points={data.metrics.history}
              containerScoped={
                (data.compute_groups?.node.container_scoped ?? 0) > 0
              }
            />
          </div>
          <div className="infra-dashboard-row infra-dashboard-row--wide">
            <Panel
              title="节点使用分布"
              description="每格 30 秒 · 点击节点或采样查看详情"
              action={
                <div className="infra-dashboard-toggle" aria-label="热力指标">
                  {(["cpu", "memory"] as const).map((metric) => (
                    <button
                      key={metric}
                      aria-pressed={heatMetric === metric}
                      onClick={() => setHeatMetric(metric)}
                    >
                      {metric === "cpu" ? "CPU" : "内存"}
                    </button>
                  ))}
                </div>
              }
            >
              {nodes.length && heatmapTimes.length ? (
                <div className="infra-dashboard-heat-scroll">
                  <div className="infra-dashboard-heatmap">
                    <div className="infra-dashboard-heat-axis">
                      <span />{" "}
                      <div>
                        {[
                          0,
                          Math.floor((heatmapTimes.length - 1) / 2),
                          heatmapTimes.length - 1,
                        ].map((index) => (
                          <time key={index}>
                            {timeLabel(heatmapTimes[index])}
                          </time>
                        ))}
                      </div>
                    </div>
                    {nodes.map((node) => {
                      const samples = new Map(
                        node.history.map((point) => [
                          point.time,
                          point[heatMetric],
                        ]),
                      );
                      return (
                        <div className="infra-dashboard-heat-row" key={node.id}>
                          <button
                            className="infra-dashboard-node-name"
                            onClick={() => setSelected(node.id)}
                            title={node.name}
                          >
                            {node.name}
                          </button>
                          <div
                            style={{
                              gridTemplateColumns: `repeat(${heatmapTimes.length}, minmax(6px, 1fr))`,
                            }}
                          >
                            {heatmapTimes.map((time) => {
                              const value = samples.get(time);
                              const available =
                                typeof value === "number" &&
                                Number.isFinite(value);
                              const label = `${node.name} · ${node.metrics_scope === "container" ? "容器范围（按容器上限） · " : ""}${moment(time)} · ${heatMetric === "cpu" ? "CPU" : "内存"} ${percent(value)}`;
                              return (
                                <button
                                  key={time}
                                  className={`infra-dashboard-heat-cell${available ? "" : " is-missing"}`}
                                  style={
                                    available
                                      ? ({
                                          "--heat-intensity": `${16 + Math.min(100, Math.max(0, value)) * 0.84}%`,
                                        } as CSSProperties)
                                      : undefined
                                  }
                                  aria-label={label}
                                  title={label}
                                  onClick={() => setSelected(node.id)}
                                />
                              );
                            })}
                          </div>
                        </div>
                      );
                    })}
                  </div>
                </div>
              ) : (
                <Empty>暂无节点趋势采样</Empty>
              )}
              <footer className="infra-dashboard-heat-footer">
                <span className="infra-dashboard-heat-key">
                  低 <i /> 高 <b /> 无数据
                </span>
                <button onClick={() => openView("nodes")}>
                  {visibleNodes.length > 12 || data.nodes_truncated
                    ? "查看全部节点"
                    : "查看节点"}
                  <ArrowRight size={13} />
                </button>
              </footer>
            </Panel>
            <div className="infra-dashboard-stack">
              <Panel title="集群就绪状态">
                <div className="infra-dashboard-clusters">
                  {data.clusters.length ? (
                    data.clusters.map((cluster) => (
                      <button
                        key={cluster.id}
                        onClick={() => setSelected(cluster.id)}
                      >
                        <span>
                          {cluster.name}
                          <small>
                            {cluster.sync_status !== "current" &&
                            cluster.sync_status !== "healthy"
                              ? collectionLabel(cluster.sync_status)
                              : ""}
                          </small>
                        </span>
                        <dl>
                          <div>
                            <dt>就绪节点</dt>
                            <dd>
                              {cluster.sync_status === "current"
                                ? cluster.nodes_ready
                                : "—"}
                              <small> / {cluster.nodes_total}</small>
                            </dd>
                          </div>
                          <div>
                            <dt>就绪副本</dt>
                            <dd>
                              {cluster.sync_status === "current" ? (
                                <>
                                  {cluster.ready_replicas}
                                  <small> / {cluster.desired_replicas}</small>
                                </>
                              ) : (
                                "—"
                              )}
                            </dd>
                          </div>
                        </dl>
                        {cluster.nodes_unknown > 0 && (
                          <small>{cluster.nodes_unknown} 个节点状态未知</small>
                        )}
                      </button>
                    ))
                  ) : (
                    <Empty>当前范围没有集群</Empty>
                  )}
                </div>
              </Panel>
              <Panel
                title="运行异常"
                action={
                  <span
                    className={`infra-dashboard-count-tag${data.stats.anomalies_total ? " has-items" : ""}`}
                  >
                    {formatCount(data.stats.anomalies_total)}
                  </span>
                }
              >
                {data.anomalies.length ? (
                  <div className="infra-dashboard-anomalies">
                    {data.anomalies.slice(0, 4).map((item) => (
                      <button
                        key={item.id}
                        onClick={() => setSelected(item.id)}
                      >
                        <span>{item.name}</span>
                        <small>{item.reason || item.status}</small>
                      </button>
                    ))}
                    {data.stats.anomalies_total > 4 && (
                      <span>
                        另有 {formatCount(data.stats.anomalies_total - 4)}{" "}
                        项，可在各分类中查看
                      </span>
                    )}
                  </div>
                ) : (
                  <p className="infra-dashboard-quiet">
                    最近成功采集未发现运行异常
                  </p>
                )}
                <footer>采集失联单独显示，不计为资源故障</footer>
              </Panel>
            </div>
          </div>
          <div className="infra-dashboard-row infra-dashboard-row--wide">
            <Panel title="采集状态" description="展示最近一次同步结果及其时间">
              {data.sources.length ? (
                <div className="infra-dashboard-sources">
                  {data.sources.map((item) => (
                    <div key={item.id}>
                      <span>
                        {item.name}
                        <small>{item.kind}</small>
                      </span>
                      <span
                        className={`infra-dashboard-source-state is-${item.sync_status}`}
                      >
                        {collectionLabel(item.sync_status)}
                      </span>
                      <time>
                        {item.last_success_at
                          ? moment(item.last_success_at)
                          : "尚无成功记录"}
                      </time>
                      <span>{formatCount(item.resources_count)} 项</span>
                    </div>
                  ))}
                </div>
              ) : (
                <Empty>当前范围没有可见采集源</Empty>
              )}
              {data.stats.missing_resources_total > 0 && (
                <footer>
                  {formatCount(data.stats.missing_resources_total)}{" "}
                  项资源在源中已不存在，未计入当前资源数量
                </footer>
              )}
            </Panel>
            <Panel
              title="指标覆盖"
              description="按已登记指标采集关系的资源统计"
            >
              <div className="infra-dashboard-coverage">
                <strong>
                  {data.metrics.current}
                  <span> / {data.metrics.eligible}</span>
                </strong>
                <p>资源拥有完整、有效的指标</p>
                <div className="infra-dashboard-coverage-track">
                  <i
                    style={{
                      width: `${data.metrics.eligible ? Math.min(100, (data.metrics.current / data.metrics.eligible) * 100) : 0}%`,
                    }}
                  />
                </div>
              </div>
              <dl className="infra-dashboard-coverage-details">
                <div>
                  <dt>部分指标</dt>
                  <dd>{data.metrics.partial}</dd>
                </div>
                <div>
                  <dt>指标过期</dt>
                  <dd>{data.metrics.stale}</dd>
                </div>
                <div>
                  <dt>暂无采样</dt>
                  <dd>{data.metrics.unavailable}</dd>
                </div>
                <div>
                  <dt>尚未接入</dt>
                  <dd>{data.metrics.unsupported}</dd>
                </div>
              </dl>
            </Panel>
          </div>
        </>
      )}
      <InfraResourceDrawer
        resourceId={selected}
        scope={scope.key}
        onClose={() => setSelected(null)}
        onSelect={setSelected}
      />
    </section>
  );
}
