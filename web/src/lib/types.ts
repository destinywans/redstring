export type User = {
  id: string;
  email: string;
  name: string;
  role: "admin" | "operator" | "viewer";
  disabled?: boolean;
};
export type ApplicationProfile = {
  business_unit?: string;
  service_type?: string;
  lifecycle?: string;
  criticality?: string;
  language?: string;
  framework?: string;
  default_branch?: string;
  build_command?: string;
  image_repository?: string;
  documentation_url?: string;
};
export type EnvironmentConfiguration = {
  replicas?: number | null;
  cpu_request_millicores?: number | null;
  cpu_limit_millicores?: number | null;
  memory_request_mib?: number | null;
  memory_limit_mib?: number | null;
  port?: number | null;
  connection_pool_max?: number | null;
  request_timeout_ms?: number | null;
  rate_limit_rps?: number | null;
  retries?: number | null;
  circuit_breaker_percent?: number | null;
};
export type Application = {
  id: string;
  name: string;
  description: string;
  environment: string;
  key?: string;
  owner_team_id?: string;
  maintainer_id?: string;
  tags?: string[];
  repository_url?: string;
  runbook_url?: string;
  archived?: boolean;
  environments?: Environment[];
  profile?: ApplicationProfile;
  maintainer_name?: string;
  owner_team_name?: string;
};
export type Model = {
  id: string;
  name: string;
  provider: string;
  available: boolean;
  availability?: string;
  tools?: boolean;
  streaming?: boolean;
};
export type Message = {
  id: string;
  role: "user" | "assistant" | "tool";
  content: string;
  created_at: string;
  model_id?: string;
  evidence_ids?: string[];
};
export type Branch = {
  id: string;
  investigation_id: string;
  parent_id?: string;
  title: string;
  model_id: string;
  revision: number;
  status: string;
  messages: Message[];
  inherited_parent_revision?: number;
  synced_parent_revision?: number;
  current_parent_revision?: number;
  parent_has_updates: boolean;
};
export type Evidence = {
  id: string;
  branch_id: string;
  kind: string;
  title: string;
  content: string;
  source: string;
  created_at: string;
};
export type Run = {
  id: string;
  branch_id: string;
  status: string;
  model_id: string;
  attempt: number;
  error?: string;
  created_at: string;
};
export type Investigation = {
  id: string;
  application_id: string;
  environment_id?: string;
  title: string;
  status: string;
  created_at: string;
  updated_at: string;
  branches: Branch[];
  evidence: Evidence[];
  runs: Run[];
};
export type MergeConflict = {
  kind: string;
  review: string;
  message: string;
  source_message_ids: string[];
};
export type Preview = {
  id: string;
  source_branch_id: string;
  target_branch_id: string;
  source_revision: number;
  target_revision: number;
  additions: Message[];
  conflicts: string[];
  evidence: Evidence[];
  conflict_details: MergeConflict[];
};
export type ContextRevision = {
  branch_id: string;
  revision: number;
  created_at: string;
  message_count: number;
  provenance: {
    kind: string;
    source_branch_id?: string;
    source_revision?: number;
    preview_id?: string;
    merge_mode?: string;
    message_ids?: string[];
  };
  messages?: Message[];
  evidence?: Evidence[];
};
export type Event = {
  seq: number;
  type: string;
  investigation_id: string;
  branch_id?: string;
  payload: unknown;
  created_at: string;
};
export type Items<T> = { items: T[] | null };

export type Environment = {
  id: string;
  application_id: string;
  key: string;
  name: string;
  archived: boolean;
  binding_revision: number;
  configuration?: EnvironmentConfiguration;
  configuration_revision?: number;
  bindings: { kind: string; connector_id: string; resource: string }[];
};
export type Team = { id: string; name: string; description: string };
export type Grant = {
  subject_type: "user" | "team";
  subject_id: string;
  permission: "view" | "operate";
};
