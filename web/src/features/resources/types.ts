export type ResourceOption = { value: string; label: string; count?: number };
export type ResourceApplication = {
  id: string;
  key: string;
  name: string;
  environment_id: string;
  environment_key: string;
  environment_name: string;
};
export type Resource = {
  id: string;
  kind: string;
  name: string;
  description: string;
  external_id: string;
  source_kind: string;
  source_id: string;
  owner_user_id: string;
  owner_name: string;
  owner_team_id: string;
  team_name: string;
  applications: ResourceApplication[];
  attributes: Record<string, unknown>;
  tags: string[];
  is_mock: boolean;
  created_at: string;
  updated_at: string;
  observed_at?: string;
};
export type ResourceRelation = {
  id: string;
  source_resource_id: string;
  target_resource_id: string;
  kind: string;
  provenance: "declared" | "discovered";
  source_kind: string;
  source_id: string;
  description: string;
  is_mock: boolean;
  updated_at: string;
};
export type ResourceChange = {
  id: number;
  action: string;
  description: string;
  created_at: string;
  is_mock: boolean;
};
export type ResourceList = {
  items: Resource[];
  total: number;
  page: number;
  page_size: number;
  facets: {
    kinds: ResourceOption[];
    applications: ResourceOption[];
    sources: ResourceOption[];
    owners: ResourceOption[];
    teams: ResourceOption[];
  };
};
export type ResourceDetail = {
  resource: Resource;
  related_resources: Resource[];
  relations: ResourceRelation[];
  changes: ResourceChange[];
};
export type ResourceGraph = {
  nodes: Resource[];
  edges: ResourceRelation[];
  truncated: boolean;
};
export const resourceKinds: Record<string, string> = {
  application: "应用",
  cluster: "Kubernetes 集群",
  node: "节点",
  deployment: "Deployment",
  pod: "Pod",
  event: "Kubernetes 事件",
  namespace: "命名空间",
  host: "主机 / 虚拟机",
  network: "网络",
  storage: "存储",
  redis: "Redis",
  mysql: "MySQL",
  kafka: "Kafka",
};
export const relationKinds: Record<string, string> = {
  depends_on: "依赖",
  deployed_on: "部署于",
  deployed_to: "部署于",
  belongs_to: "归属于",
  contains: "包含",
  runs_on: "运行于",
  connects_to: "连接",
  uses: "使用",
};
export function resourceKindLabel(kind: string) {
  return resourceKinds[kind] ?? kind;
}
export function sourceLabel(kind: string) {
  return (
    (
      {
        mock: "示例数据",
        manual: "人工登记",
        kubernetes: "Kubernetes",
        redis: "Redis",
        mysql: "MySQL",
        kafka: "Kafka",
      } as Record<string, string>
    )[kind] ?? kind
  );
}
export function resourceTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : date.toLocaleString("zh-CN", {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      });
}
export function attributeText(value: unknown) {
  if (value == null || value === "") return "—";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}
