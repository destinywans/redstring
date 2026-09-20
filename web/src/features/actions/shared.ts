import { ApiError, request } from "../../lib/api";
export type Proposal = {
  namespace: string;
  deployment: string;
  uid: string;
  expected_template_digest: string;
  expected_spec_digest: string;
  target_template_digest: string;
  target_revision: string;
  digest: string;
  target_template: Record<string, unknown>;
  observation_seconds: number;
};
export type ActionPlan = {
  id: string;
  investigation_id: string;
  created_by: string;
  created_at: string;
  approved_by?: string;
  approved_at?: string;
  decision: "pending" | "approved" | "rejected" | "revoked";
  decision_by?: string;
  decision_reason?: string;
  decided_at?: string;
  proposal: Proposal;
};
export type Execution = {
  id: string;
  plan_id: string;
  status:
    | "queued"
    | "executing"
    | "verifying"
    | "succeeded"
    | "failed"
    | "needs_attention";
  attempt: number;
  error?: string;
  result: Record<string, unknown> | null;
  created_at: string;
  updated_at: string;
};
export type DeploymentSnapshot = {
  application_id: string;
  namespace: string;
  deployment: string;
  uid: string;
  template_digest: string;
  images: { name: string; image: string }[];
  replicas: number;
  ready_replicas: number;
  updated_replicas: number;
  revisions: {
    revision: string;
    digest: string;
    images: { name: string; image: string }[];
  }[];
  captured_at: string;
};
export type Report = {
  investigation_id: string;
  execution_id: string;
  summary: string;
  closed_at: string;
  closed_by: string;
  recovery: Record<string, unknown>;
};
export async function optional<T>(path: string) {
  try {
    return await request<T>(path);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
}
export const time = (value?: string) =>
  value
    ? new Date(value).toLocaleTimeString("zh-CN", {
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";
export const decisionLabel: Record<ActionPlan["decision"], string> = {
  pending: "等待确认",
  approved: "已批准 · 待执行",
  rejected: "已拒绝",
  revoked: "已撤销",
};
export const executionLabel: Record<Execution["status"], string> = {
  queued: "等待执行",
  executing: "正在执行",
  verifying: "恢复观测中",
  succeeded: "已验证恢复",
  failed: "恢复验证失败",
  needs_attention: "需要人工处理",
};
// Design preview is isolated from live queries and can never authorize an action.
export const previewPlan: ActionPlan = {
  id: "preview-plan",
  investigation_id: "preview-investigation",
  created_by: "preview",
  created_at: "2026-09-20T14:15:00+08:00",
  decision: "pending",
  proposal: {
    namespace: "production",
    deployment: "checkout-api",
    uid: "preview-deployment",
    expected_template_digest: "preview-current-template",
    expected_spec_digest: "preview-current-spec",
    target_template_digest: "preview-target-template",
    target_revision: "42",
    digest: "preview-only-not-an-executable-digest",
    observation_seconds: 180,
    target_template: {
      metadata: { labels: { app: "checkout-api" } },
      spec: {
        containers: [
          {
            name: "checkout-api",
            image: "checkout-api:v2.8.0",
            env: [{ name: "CONNECTION_POOL_MAX", value: "200" }],
          },
        ],
      },
    },
  },
};
export const previewExecution: Execution = {
  id: "preview-execution",
  plan_id: "preview-plan",
  status: "succeeded",
  attempt: 1,
  created_at: "2026-09-20T14:19:00+08:00",
  updated_at: "2026-09-20T14:23:00+08:00",
  result: {
    recovered: true,
    started_at: "2026-09-20T14:20:00+08:00",
    finished_at: "2026-09-20T14:23:00+08:00",
    samples: 36,
    requests: 177,
  },
};
