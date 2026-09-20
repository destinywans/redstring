export type User = {
  id: string;
  email: string;
  name: string;
  role: "admin" | "operator" | "viewer";
  disabled?: boolean;
};
export type Application = {
  id: string;
  name: string;
  description: string;
  environment: string;
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
