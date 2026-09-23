export type Dependency = { node_id: string; on?: string };
export type Task = {
  id: string;
  name: string;
  kind: string;
  depends_on?: Dependency[];
  join?: string;
  when?: {
    from_node: string;
    output: string;
    operator: "eq" | "ne";
    value: string;
  };
  image?: string;
  command?: string[];
  args?: string[];
  inputs?: {
    name: string;
    value?: string;
    from_node?: string;
    output?: string;
  }[];
  outputs?: { name: string; path: string }[];
  timeout_seconds?: number;
  source_node_id?: string;
  target_id?: string;
  approval_node_id?: string;
  deploy_node_id?: string;
  [key: string]: unknown;
};
export type Definition = {
  provider: "native" | "gitlab";
  source_id?: string;
  project_id?: number;
  ref?: string;
  nodes?: Task[];
};
export type Pipeline = {
  id: string;
  name: string;
  application_id: string;
  environment_id: string;
  version: number;
  digest: string;
  definition: Definition;
  created_at: string;
};
export type Run = {
  id: string;
  pipeline_id: string;
  version: number;
  application_id: string;
  environment_id: string;
  provider: string;
  status: string;
  external_id?: string;
  snapshot: Pipeline;
  projection: {
    status: string;
    external_status?: string;
    commit_sha?: string;
    web_url?: string;
    message?: string;
    nodes: {
      id: string;
      name: string;
      status: string;
      outputs?: Record<string, string>;
    }[];
  };
  created_at: string;
};
export type Template = {
  id: string;
  name: string;
  description: string;
  required_inputs: string[];
  definition: Definition;
};
export type Catalog = {
  gitlab_sources: {
    id: string;
    name: string;
    bindings: {
      application_id: string;
      environment_id: string;
      project_id: number;
      allowed_refs: string[];
      allow_actions: boolean;
      external_governance: boolean;
      manual_jobs: string[];
    }[];
  }[];
  native_enabled: boolean;
  artifact_store_configured: boolean;
  native_deployment_enabled: boolean;
  templates: Template[];
};
export type Items<T> = { items: T[] };
export const statusLabel = (s: string) =>
  ({
    queued: "排队中",
    pending: "待处理",
    running: "运行中",
    succeeded: "成功",
    success: "成功",
    failed: "失败",
    cancelled: "已取消",
    canceled: "已取消",
    manual: "等待手动操作",
    waiting: "等待中",
    skipped: "已跳过",
    needs_attention: "需要处理",
    unknown: "结果待核对",
    approved: "已批准",
    verifying: "验证中",
  })[s] ?? s;
export const displayTime = (s: string) => new Date(s).toLocaleString("zh-CN");
