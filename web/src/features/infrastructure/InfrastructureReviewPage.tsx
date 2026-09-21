import { Fragment, useState, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Box,
  Database,
  Layers,
  Network,
  RefreshCw,
  Server,
} from "../../lib/icons";
import { request } from "../../lib/api";
import { useEnvironmentScope } from "../../lib/environmentScope";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { ErrorNotice } from "../../components/ui";
import InfraResourceDrawer, {
  ResourceStatus,
  UsageMeter,
} from "./InfraResourceDrawer";
import {
  attr,
  num,
  kindLabels,
  moment,
  tone,
  type InfraResource,
  type InfraList,
} from "./review-types";
import "./infrastructure-review.css";

type Column = { key: string; label: string };
type Definition = {
  title: string;
  description: string;
  kinds: string[];
  columns: Column[];
  filters: string[];
};
const col = (key: string, label: string): Column => ({ key, label });
const common = col("updated_at", "更新时间");
const definitions: Record<string, Definition> = {
  summary: {
    title: "资源总览",
    description: "已纳管资源的运行状态概览，聚焦当前查看范围。",
    kinds: [],
    filters: ["kind", "status", "cluster", "owner"],
    columns: [
      col("kind", "类型"),
      col("cluster", "所属集群 / 主机"),
      col("status", "状态"),
      col("observed_at", "最近采集时间"),
    ],
  },
  clusters: {
    title: "集群",
    description: "查看 Kubernetes 集群、资源容量与运行状态。",
    kinds: ["cluster"],
    filters: ["application", "status"],
    columns: [
      col("nodes_count", "节点数"),
      col("version", "Kubernetes 版本"),
      col("status", "状态"),
      col("provider", "来源"),
      common,
    ],
  },
  nodes: {
    title: "节点",
    description: "查看集群节点的运行条件与资源使用情况。",
    kinds: ["node"],
    filters: ["cluster", "status", "roles"],
    columns: [
      col("cluster", "集群"),
      col("status", "状态"),
      col("roles", "角色"),
      col("cpu_percent", "CPU"),
      col("memory_percent", "内存"),
      col("pod_count", "Pod 数"),
      col("version", "版本"),
      common,
    ],
  },
  workloads: {
    title: "工作负载",
    description: "查看 Deployment 与 StatefulSet 的副本、镜像和运行状态。",
    kinds: ["deployment"],
    filters: ["cluster", "namespace", "workload_type", "status"],
    columns: [
      col("application", "应用"),
      col("cluster", "集群"),
      col("namespace", "命名空间"),
      col("workload_type", "类型"),
      col("status", "状态"),
      col("replicas", "副本"),
      col("version", "镜像版本"),
      common,
    ],
  },
  pods: {
    title: "Pod 与容器",
    description: "查看 Pod、容器状态与最近的运行记录。",
    kinds: ["pod"],
    filters: ["namespace", "workload", "status", "cluster"],
    columns: [
      col("status", "状态"),
      col("ready", "就绪"),
      col("restarts", "重启次数"),
      col("age", "运行时长"),
      col("node", "所属节点"),
    ],
  },
  events: {
    title: "Kubernetes 事件",
    description: "按时间查看集群资源事件，定位运行变化与异常信息。",
    kinds: ["event"],
    filters: ["cluster", "severity", "namespace"],
    columns: [
      col("severity", "类型"),
      col("reason", "原因"),
      col("involved_object", "资源"),
      col("count", "发生次数"),
      col("last_seen", "最后出现"),
    ],
  },
  namespaces: {
    title: "命名空间",
    description: "查看命名空间的资源配额、归属与工作负载。",
    kinds: ["namespace"],
    filters: ["cluster", "team", "status"],
    columns: [
      col("cluster", "集群"),
      col("cpu_quota", "CPU 配额"),
      col("memory_quota", "内存配额"),
      col("pods_count", "Pod 数"),
      col("team", "所属团队"),
      col("created_at", "创建时间"),
    ],
  },
  hosts: {
    title: "主机与虚拟机",
    description: "查看已登记主机的运行状态与采样指标。",
    kinds: ["host"],
    filters: ["application", "status"],
    columns: [
      col("ip", "IP 地址"),
      col("os", "操作系统"),
      col("cpu_percent", "CPU"),
      col("memory_percent", "内存"),
      col("disk_percent", "磁盘"),
      col("age", "运行时长"),
      col("status", "状态"),
    ],
  },
  network: {
    title: "网络",
    description: "查看 Service 与 Ingress 网络资源和转发关系。",
    kinds: ["network"],
    filters: ["namespace", "service_type", "cluster"],
    columns: [
      col("service_type", "类型"),
      col("namespace", "命名空间"),
      col("workload", "关联工作负载"),
      col("ports", "端口"),
      col("address", "访问地址"),
      col("cluster", "集群"),
      common,
    ],
  },
  storage: {
    title: "存储",
    description: "查看持久化存储、容量及其使用关系。",
    kinds: ["storage"],
    filters: ["cluster", "namespace", "status", "storage_class"],
    columns: [
      col("namespace", "命名空间"),
      col("cluster", "集群"),
      col("status", "状态"),
      col("capacity_gb", "容量 (GiB)"),
      col("storage_class", "存储类"),
      col("workload", "使用方"),
      common,
    ],
  },
  middleware: {
    title: "中间件与数据库",
    description: "查看 Redis、MySQL、Kafka 实例的状态与关联应用。",
    kinds: ["redis", "mysql", "kafka"],
    filters: ["cluster", "application", "status"],
    columns: [
      col("kind", "类型"),
      col("application", "关联应用"),
      col("cluster", "集群"),
      col("status", "连接状态"),
      col("observed_at", "最近查询时间"),
    ],
  },
};
const filterLabels: Record<string, string> = {
  kind: "资源类型",
  status: "状态",
  cluster: "集群",
  owner: "负责人",
  application: "应用",
  roles: "角色",
  namespace: "命名空间",
  workload_type: "类型",
  workload: "工作负载",
  severity: "事件类型",
  team: "所属团队",
  service_type: "类型",
  storage_class: "存储类",
};
function field(r: InfraResource, key: string): string {
  if (key === "kind") return kindLabels[r.kind] ?? r.kind;
  if (key === "service_type" && r.kind === "network") {
    const resourceType =
      r.attributes.resource_type ?? r.attributes.network_type;
    return resourceType === "Ingress" ? "Ingress" : attr(r, "service_type");
  }
  if (key === "application")
    return r.applications.map((a) => a.key).join("、") || "—";
  if (key === "owner") return r.owner_name || "—";
  if (key === "team") return r.team_name || "—";
  if (key === "workload_type") return attr(r, "workload_type");
  if (["updated_at", "created_at", "observed_at"].includes(key))
    return moment(r[key as "updated_at"]);
  if (["last_seen", "first_seen"].includes(key)) return moment(attr(r, key));
  if (key === "replicas")
    return `${attr(r, "ready_replicas")} / ${attr(r, "replicas")}`;
  if (key.endsWith("_quota")) {
    const quota = r.attributes.quota as Record<string, unknown> | undefined;
    const metric = key.replace("_quota", "");
    const used = num(r, `${metric}_used`);
    const total = num(r, `${metric}_total`);
    return used !== undefined && total !== undefined
      ? `${used} / ${total} ${metric === "cpu" ? "核" : "GiB"}`
      : quota?.[metric] !== undefined
        ? String(quota[metric])
        : "—";
  }
  return attr(r, key);
}
async function loadInventory(
  scope: string,
  signal: AbortSignal,
): Promise<InfraResource[]> {
  const rows: InfraResource[] = [];
  let page = 1;
  while (true) {
    const p = new URLSearchParams({
      environment_scope: scope || "all",
      page: String(page),
      page_size: "100",
      sort: "name",
    });
    const result = await request<InfraList>(`/resources?${p}`, { signal });
    rows.push(...result.items.filter((r) => r.kind !== "application"));
    if (result.page * result.page_size >= result.total || !result.items.length)
      break;
    page++;
    if (page > 100)
      throw new Error("当前目录超过浏览范围，请收窄全局环境后重试。");
  }
  return rows;
}
export default function InfrastructureReviewPage() {
  const scope = useEnvironmentScope();
  const [params, setParams] = useSearchParams();
  const view = params.get("view") ?? "summary";
  const def = definitions[view] ?? definitions.summary;
  const [selected, setSelected] = useState<string | null>(null);
  const inventory = useQuery({
    queryKey: ["infrastructure-inventory", scope.key],
    queryFn: ({ signal }) => loadInventory(scope.key, signal),
  });
  const rows = inventory.data ?? [];
  const applicationId =
    params.get("application") ?? params.get("application_id") ?? "";
  const base = useMemo(
    () =>
      rows.filter(
        (r) =>
          (!def.kinds.length || def.kinds.includes(r.kind)) &&
          (!applicationId ||
            r.applications.some((a) => a.id === applicationId)),
      ),
    [rows, def, applicationId],
  );
  const q = params.get("q") ?? "";
  const category = ["network", "middleware"].includes(view)
    ? (params.get("category") ?? "")
    : "";
  const update = (key: string, value: string) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        value ? next.set(key, value) : next.delete(key);
        if (key !== "page") next.delete("page");
        return next;
      },
      { replace: true },
    );
  const filtered = base.filter((r) => {
    if (
      category &&
      (view === "middleware"
        ? r.kind !== category
        : attr(r, "resource_type") !== category)
    )
      return false;
    if (
      q &&
      !`${r.name} ${r.description} ${r.tags.join(" ")} ${JSON.stringify(r.attributes)}`
        .toLowerCase()
        .includes(q.toLowerCase())
    )
      return false;
    return def.filters.every((key) => {
      const selectedValue = params.get(`filter_${key}`);
      if (!selectedValue) return true;
      return key === "application"
        ? r.applications.some((application) => application.id === selectedValue)
        : field(r, key) === selectedValue;
    });
  });
  function groupLabel(r: InfraResource) {
    if (view === "events") {
      const date = new Date(String(r.attributes.last_seen ?? r.updated_at));
      return Number.isNaN(date.getTime())
        ? "时间未知"
        : date.toLocaleDateString("zh-CN");
    }
    return r.kind === "host"
      ? "主机与虚拟机"
      : ["redis", "mysql", "kafka"].includes(r.kind)
        ? "中间件与数据库"
        : "Kubernetes 资源";
  }
  if (view === "events")
    filtered.sort((a, b) =>
      String(b.attributes.last_seen ?? b.updated_at).localeCompare(
        String(a.attributes.last_seen ?? a.updated_at),
      ),
    );
  if (view === "summary")
    filtered.sort(
      (a, b) =>
        ["Kubernetes 资源", "主机与虚拟机", "中间件与数据库"].indexOf(
          groupLabel(a),
        ) -
          ["Kubernetes 资源", "主机与虚拟机", "中间件与数据库"].indexOf(
            groupLabel(b),
          ) ||
        Number(["warn", "bad"].includes(tone(attr(b, "status")))) -
          Number(["warn", "bad"].includes(tone(attr(a, "status")))) ||
        Number(b.kind === "deployment") - Number(a.kind === "deployment") ||
        a.name.localeCompare(b.name),
    );
  const pageSize = 10;
  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const requestedPage = Number(params.get("page"));
  const page = Math.min(
    totalPages,
    Number.isSafeInteger(requestedPage) && requestedPage > 0
      ? requestedPage
      : 1,
  );
  const items = filtered.slice((page - 1) * pageSize, page * pageSize);
  const options = (key: string) => {
    if (key === "application") {
      const applications = new Map(
        base.flatMap((resource) =>
          resource.applications.map(
            (application) =>
              [
                application.id,
                {
                  value: application.id,
                  label: application.key || application.name || application.id,
                },
              ] as const,
          ),
        ),
      );
      return [...applications.values()].sort((a, b) =>
        a.label.localeCompare(b.label, "zh-CN"),
      );
    }
    return [...new Set(base.map((r) => field(r, key)).filter((v) => v !== "—"))]
      .sort()
      .map((value) => ({ value, label: value }));
  };
  const attention = rows.filter((r) =>
    ["warn", "bad"].includes(tone(attr(r, "status"))),
  );
  const latest = rows
    .map((r) => r.observed_at ?? r.updated_at)
    .sort()
    .at(-1);
  const categories =
    view === "middleware"
      ? ["redis", "mysql", "kafka"]
      : view === "network"
        ? ["Service", "Ingress"]
        : [];
  const iconFor = (kind: string) =>
    kind === "host"
      ? Server
      : ["redis", "mysql", "kafka", "storage"].includes(kind)
        ? Database
        : kind === "network"
          ? Network
          : Box;
  return (
    <section className="infra-review" aria-label={def.title}>
      <header className="infra-review-heading">
        <div>
          <h1>{def.title}</h1>
          <p>{def.description}</p>
        </div>
        <div className="infra-review-actions">
          <span className="infra-review-source">
            {rows.some((r) => r.is_mock) ? "含示例数据 · 只读预览" : "资源目录"}
            {latest && <small>最近采样 {moment(latest)}</small>}
          </span>
          <button
            className="button"
            onClick={() => void inventory.refetch()}
            disabled={inventory.isFetching}
          >
            <RefreshCw size={16} />
            刷新
          </button>
        </div>
      </header>
      {inventory.error && <ErrorNotice error={inventory.error} />}
      {view === "summary" && (
        <section className="infra-overview-stats" aria-label="资源统计">
          {[
            {
              label: "工作负载",
              value: rows.filter((r) => r.kind === "deployment").length,
              Icon: Box,
              detail: "Deployment 与 StatefulSet",
            },
            {
              label: "主机",
              value: rows.filter((r) => r.kind === "host").length,
              Icon: Server,
              detail: "已登记主机与虚拟机",
            },
            {
              label: "需要关注",
              value: attention.length,
              Icon: AlertTriangle,
              detail: "示例状态中的告警与异常",
              warning: true,
            },
            {
              label: "集群",
              value: rows.filter((r) => r.kind === "cluster").length,
              Icon: Layers,
              detail: "当前范围纳管集群",
            },
          ].map((s) => (
            <article key={s.label}>
              <div
                className={`infra-stat-icon${s.warning ? " is-warning" : ""}`}
              >
                <s.Icon size={23} />
              </div>
              <div>
                <span>{s.label}</span>
                <strong>{inventory.isLoading ? "—" : s.value}</strong>
                <small>{s.detail}</small>
              </div>
            </article>
          ))}
        </section>
      )}
      <FilterBar label={`${def.title}搜索与筛选`}>
        <FilterSearch
          value={q}
          onChange={(v) => update("q", v)}
          onSubmit={() => {}}
          showSubmit={false}
          placeholder={`搜索${view === "events" ? "事件内容、资源名称" : "资源名称、标识"}或标签`}
          label="搜索基础设施"
        />
        {def.filters.map((k) => (
          <FilterSelect
            id={`infra-filter-${k}`}
            key={k}
            label={filterLabels[k]}
            emptyLabel={filterLabels[k]}
            showLabel={false}
            searchable={options(k).length > 5}
            includeEmptyOption={false}
            clearable
            popoverWidth={210}
            options={options(k)}
            values={
              params.get(`filter_${k}`) ? [params.get(`filter_${k}`)!] : []
            }
            onChange={(v) => update(`filter_${k}`, v[0] ?? "")}
          />
        ))}
        {(q || def.filters.some((k) => params.has(`filter_${k}`))) && (
          <button
            className="infra-reset"
            onClick={() =>
              setParams((p) => {
                const next = new URLSearchParams(p);
                next.delete("q");
                next.delete("page");
                def.filters.forEach((k) => next.delete(`filter_${k}`));
                return next;
              })
            }
          >
            重置
          </button>
        )}
      </FilterBar>
      {categories.length > 0 && (
        <nav className="infra-kind-tabs" aria-label="资源分类">
          {["", ...categories].map((c) => (
            <button
              key={c}
              aria-current={category === c ? "page" : undefined}
              onClick={() => update("category", c)}
            >
              {c ? (kindLabels[c] ?? c) : "全部"}
              <span>
                {
                  base.filter(
                    (r) =>
                      !c ||
                      (view === "middleware"
                        ? r.kind === c
                        : attr(r, "resource_type") === c),
                  ).length
                }
              </span>
            </button>
          ))}
        </nav>
      )}
      <section className="infra-inventory-card">
        <div className="infra-table-scroll">
          <table className={`infra-review-table infra-review-table--${view}`}>
            <thead>
              <tr>
                <th>
                  {view === "events"
                    ? "事件"
                    : view === "storage"
                      ? "存储资源"
                      : def.title === "资源总览"
                        ? "名称"
                        : def.title === "中间件与数据库"
                          ? "实例名称"
                          : "名称"}
                </th>
                {def.columns.map((c) => (
                  <th key={c.key}>{c.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {items.map((r, index) => {
                const Icon = iconFor(r.kind);
                return (
                  <Fragment key={r.id}>
                    {["summary", "events"].includes(view) &&
                      (index === 0 ||
                        groupLabel(items[index - 1]) !== groupLabel(r)) && (
                        <tr className="infra-group-row">
                          <th colSpan={def.columns.length + 1}>
                            {groupLabel(r)}
                          </th>
                        </tr>
                      )}
                    <tr className={selected === r.id ? "is-selected" : ""}>
                      <td>
                        <button
                          className="infra-resource-name"
                          onClick={() => setSelected(r.id)}
                        >
                          <span
                            className={`infra-resource-icon kind-${r.kind}`}
                          >
                            <Icon size={19} />
                          </span>
                          <span>
                            <strong>{r.name}</strong>
                            {["summary", "pods", "middleware"].includes(
                              view,
                            ) && (
                              <small>
                                {view === "middleware"
                                  ? `${attr(r, "ip")}${attr(r, "port") !== "—" ? `:${attr(r, "port")}` : ""}`
                                  : attr(r, "namespace") !== "—"
                                    ? attr(r, "namespace")
                                    : kindLabels[r.kind]}
                              </small>
                            )}
                          </span>
                        </button>
                      </td>
                      {def.columns.map((c) => (
                        <td key={c.key}>
                          {c.key === "status" || c.key === "severity" ? (
                            <ResourceStatus value={field(r, c.key)} />
                          ) : c.key.endsWith("_percent") ? (
                            <UsageMeter value={r.attributes[c.key]} />
                          ) : (
                            <span
                              className={
                                [
                                  "application",
                                  "cluster",
                                  "namespace",
                                ].includes(c.key)
                                  ? "infra-cell-muted"
                                  : ""
                              }
                            >
                              {field(r, c.key)}
                            </span>
                          )}
                        </td>
                      ))}
                    </tr>
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
        {!items.length && (
          <div className="infra-empty">
            {inventory.isLoading
              ? "正在读取资源目录…"
              : inventory.error
                ? "资源暂时无法读取，请重试。"
                : "当前范围没有匹配资源"}
          </div>
        )}
        <footer className="infra-pagination">
          <span>共 {filtered.length} 条</span>
          <button
            className="icon-button"
            aria-label="上一页"
            disabled={page === 1}
            onClick={() => update("page", String(page - 1))}
          >
            <ArrowLeft size={15} />
          </button>
          <span>
            第 {page} / {totalPages} 页
          </span>
          <button
            className="icon-button"
            aria-label="下一页"
            disabled={page === totalPages}
            onClick={() => update("page", String(page + 1))}
          >
            <ArrowRight size={15} />
          </button>
        </footer>
      </section>
      <footer className="infra-review-footnote">
        示例状态用于界面验收，不代表实际基础设施健康。
        <Link
          to={`/infrastructure?view=${encodeURIComponent(view)}&mode=live&environment_scope=${encodeURIComponent(scope.key || "all")}`}
        >
          打开现有实时查询
        </Link>
      </footer>
      <InfraResourceDrawer
        resourceId={selected}
        scope={scope.key}
        onClose={() => setSelected(null)}
        onSelect={setSelected}
      />
    </section>
  );
}
