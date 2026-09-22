export type MetricPoint = {
  time: string;
  cpu: number | null;
  memory: number | null;
  cpu_samples?: number;
  memory_samples?: number;
};

export type OverviewNode = {
  id: string;
  name: string;
  kind: "node" | "host";
  source_id: string;
  metrics_status: string;
  metrics_scope: string;
  cpu_percent: number | null;
  memory_percent: number | null;
  history: MetricPoint[];
  capacity_cpu: number | null;
  capacity_memory_bytes: number | null;
};

export type ComputeGroup = {
  total: number;
  capacity_cpu: number | null;
  capacity_memory_bytes: number | null;
  cpu_percent: number | null;
  memory_percent: number | null;
  cpu_covered: number;
  memory_covered: number;
  container_scoped: number;
};

export type InfrastructureOverview = {
  generated_at: string;
  window_seconds: number;
  stats: {
    resources_total: number;
    clusters_total: number;
    nodes_total: number;
    hosts_total: number;
    workloads_total: number;
    runtime_pods_total: number;
    anomalies_total: number;
    stale_resources_total: number;
    missing_resources_total: number;
  };
  distribution: { kind: string; label: string; count: number }[];
  workload_types: { name: string; count: number }[];
  clusters: {
    id: string;
    name: string;
    source_id: string;
    nodes_total: number;
    nodes_ready: number;
    nodes_unknown: number;
    workloads_total: number;
    desired_replicas: number;
    ready_replicas: number;
    sync_status: string;
    last_success_at: string | null;
  }[];
  nodes: OverviewNode[];
  nodes_truncated: boolean;
  compute_groups: { node: ComputeGroup; host: ComputeGroup };
  sources: {
    id: string;
    name: string;
    kind: string;
    sync_status: string;
    last_success_at: string | null;
    resources_count: number;
    metrics_eligible: number;
    metrics_current: number;
  }[];
  anomalies: {
    id: string;
    name: string;
    kind: string;
    reason: string;
    status: string;
  }[];
  anomalies_truncated: boolean;
  metrics: {
    eligible: number;
    current: number;
    partial: number;
    unavailable: number;
    unsupported: number;
    stale: number;
    history: MetricPoint[];
    cpu_percent: number | null;
    memory_percent: number | null;
  };
};
