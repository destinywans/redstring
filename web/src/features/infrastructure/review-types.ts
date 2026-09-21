export type InfraResource = {
  id: string;
  kind: string;
  name: string;
  description: string;
  external_id: string;
  source_kind: string;
  source_id: string;
  owner_name: string;
  team_name: string;
  applications: {
    id: string;
    key: string;
    name: string;
    environment_id: string;
    environment_key: string;
  }[];
  attributes: Record<string, unknown>;
  tags: string[];
  is_mock: boolean;
  created_at: string;
  updated_at: string;
  observed_at?: string;
};
export type InfraList = {
  items: InfraResource[];
  total: number;
  page: number;
  page_size: number;
};
export type InfraDetail = {
  resource: InfraResource;
  related_resources: InfraResource[];
  relations: {
    id: string;
    source_resource_id: string;
    target_resource_id: string;
    kind: string;
  }[];
  changes: {
    id: number;
    action: string;
    description: string;
    created_at: string;
    is_mock: boolean;
  }[];
};
export function attr(r: InfraResource, key: string): string {
  const value = r.attributes[key];
  return value === undefined || value === null || value === ""
    ? "—"
    : Array.isArray(value)
      ? value.join(" / ")
      : typeof value === "object"
        ? JSON.stringify(value)
        : String(value);
}
export function num(r: InfraResource, key: string): number | undefined {
  const v = r.attributes[key];
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}
export function moment(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime())
    ? "—"
    : d.toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      });
}
export const kindLabels: Record<string, string> = {
  cluster: "Kubernetes 集群",
  node: "节点",
  deployment: "工作负载",
  pod: "Pod",
  event: "事件",
  namespace: "命名空间",
  host: "主机",
  network: "网络",
  storage: "存储",
  redis: "Redis",
  mysql: "MySQL",
  kafka: "Kafka",
  application: "应用",
};
export function tone(status: string): "good" | "warn" | "bad" | "unknown" {
  if (/crash|failed|error|notready|异常|离线|unhealthy/i.test(status))
    return "bad";
  if (
    /warning|pending|pressure|partial|progressing|告警|部分|压力/i.test(status)
  )
    return "warn";
  if (
    /ready|running|healthy|normal|active|available|bound|info|正常|运行|就绪/i.test(
      status,
    )
  )
    return "good";
  return "unknown";
}
