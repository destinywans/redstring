/** Kubernetes rollback (docs/api/v3-rollback.md, PA-08). Sample data from the lab's bad-release drill on checkout-api. */
import { mock, MockError } from "../router";
import { l } from "../../i18n";
import { at } from "../time";
import { serviceParts } from "../../api/ids";

/** Encoded service ID as the real API uses it: base64url("application\0environment"). */
export const CHECKOUT_SERVICE_ID = "Y2hlY2tvdXQtYXBpAHByb2Q";
/** The sample catalog keys services by name; encoded IDs resolve to their application. */
const keyOf = (id: string) => serviceParts(id)?.application ?? id;

const P = (zh: string, en: string) => l(zh, en);
const D7 = "9f2a6c1e5b8d4f3a7c2e9b1d6a4f8c3e5b7d9a2c4e6f8b1d3a5c7e9f2b4d6a8c";
const D6 = "41bc7e2a9d5f3b8c1e6a4d9f2b7c5e8a3d1f6b9c4e7a2d5f8b3c6e9a1d4f7b2c";
const D5 = "c3d8a1f6e4b9c2d7a5f8e3b6c1d9a4f7e2b5c8d3a6f9e1b4c7d2a5f8e3b6c9d1";
const IMAGE = "aiops-test/api:1.4.2";
const plan = { observation_seconds: 300, request_rate_query_id: "request_rate", error_ratio_query_id: "error_rate", sample_age_query_id: "sample_age", max_error_ratio: 0.02, min_request_rate: 0.2 };
const FAULT = '{"http_error_ratio":{"/checkout":0.5}}';

const candidates = (serviceId = CHECKOUT_SERVICE_ID) => ({
  service_id: serviceId,
  application_id: "checkout-api",
  environment_id: "prod",
  workload: { connector_id: "kubernetes", namespace: "aiops-lab", deployment: "checkout-api", uid: "5d1c2e7a-8b3f-4c9d-a1e6-7f2b9c4d8e31", paused: false, current_revision: "7", current_template_digest: D7, replicas: { desired: 2, updated: 2, available: 2 } },
  verification: plan,
  candidates: [
    { revision: "7", current: true, created_at: at("10:42"), change_cause: "lab drill bad-release: /checkout answers 5xx at ratio 0.5", containers: [{ name: "api", image: IMAGE }], changes: [], template_digest: D7 },
    {
      revision: "6",
      current: false,
      created_at: at("09:15"),
      change_cause: "lab drill bad-release reverted",
      containers: [{ name: "api", image: IMAGE }],
      changes: [{ container: "api", field: "env", name: "LAB_FAULTS", from: FAULT, to: null, value_hidden: false }],
      template_digest: D6,
    },
    {
      revision: "5",
      current: false,
      created_at: at("16:20", -1),
      change_cause: "lab drill bad-release: /checkout answers 5xx at ratio 0.2",
      containers: [{ name: "api", image: IMAGE }],
      changes: [
        { container: "api", field: "env", name: "LAB_FAULTS", from: FAULT, to: '{"http_error_ratio":{"/checkout":0.2}}', value_hidden: false },
        { container: "api", field: "env", name: "LAB_API_TOKEN", from: null, to: null, value_hidden: true },
      ],
      template_digest: D5,
    },
  ],
  blocked_reason: null,
  observed_at: new Date().toISOString(),
});

// One sample service per blocked reason, so every state can be opened from Services in sample mode.
const BLOCKED: Record<string, string> = {
  "payment-gateway": "write_not_approved",
  "order-service": "rollback_not_allowed",
  "inventory-worker": "deployment_paused",
  "user-center": "credential_unavailable",
  "search-api": "executor_not_ready",
};
mock("GET", "/services/:id/rollback-candidates", ({ params }) => {
  const key = keyOf(params.id);
  if (key === "checkout-api") return candidates(params.id);
  if (key === "notification-svc") throw new MockError(503, "workload_unavailable", "cluster unreachable");
  return { ...candidates(params.id), application_id: key, workload: null, verification: null, candidates: [], blocked_reason: BLOCKED[key] ?? "workload_not_configured" };
});
let seq = 7;
mock("POST", "/rollback-proposals", ({ body }) => {
  const b = body as { target_revision?: string; reason?: string; expected_template_digest?: string };
  if (!b.reason?.trim()) throw new MockError(422, "invalid_rollback", "reason is required");
  if (b.target_revision === "7") throw new MockError(422, "invalid_rollback", "target is the current revision");
  return { proposal_id: `PRP-RB${++seq}`, status: "pending", action_kind: "kubernetes.deployment.rollback" };
});

/** Proposal detail for PRP-RB* (events domain routes /proposals/:id here). */
export const rollbackProposal = (id: string) => ({
  id,
  title: P("回滚 checkout-api 到修订 6", "Roll back checkout-api to revision 6"),
  status: "pending",
  risk: "medium",
  impact: P("滚动更新期间 2 个副本中最多 1 个不可用，约 1 分钟；回到修订 6 会去掉 LAB_FAULTS 设置，镜像不变。", "During the rolling update at most 1 of 2 replicas is unavailable for about a minute; revision 6 drops the LAB_FAULTS setting and keeps the image."),
  reversible: true,
  proposed_by: "王晨",
  proposed_at: "10:51",
  investigation_id: "INV-236",
  target: "Deployment aiops-lab/checkout-api",
  diff: { path: "Deployment aiops-lab/checkout-api · api", lines: [`- LAB_FAULTS=${FAULT}`] },
  method: P("条件补丁，只改 Pod template；执行时如果修订或 template 已经变化，就不写入。", "A conditional patch that changes only the Pod template; nothing is written if the revision or template changed."),
  evidence: [
    { n: 1, icon: "activity", title: P("Prometheus 样本：checkout-api / http_error_ratio 48%", "Prometheus sample: checkout-api / http_error_ratio 48%"), desc: P("10:44 · 修订 7 上线后 2 分钟", "10:44 · 2 min after revision 7") },
    { n: 2, icon: "layers", title: P("Kubernetes：修订 7 与修订 6 只差 LAB_FAULTS", "Kubernetes: revisions 7 and 6 differ only in LAB_FAULTS"), desc: P("ReplicaSet 对比 · 镜像相同", "ReplicaSet comparison · same image") },
    { n: 3, icon: "terminal", title: P("Loki：injected fault 只出现在修订 7 的 Pod", "Loki: injected fault only in revision 7 Pods"), desc: "aiops-lab · checkout-api" },
  ],
  verification: [],
  verification_note: P("任何一次采样超阈值就判为失败；查询出错记为需要人工处理。执行器不会自动再写。", "Any sample over a threshold fails it; a query error needs attention. The executor never writes again on its own."),
  checklist: [P("目标修订 6 是上线前的版本", "Revision 6 is the version before the release"), P("差异只有预期的改动", "The differences are only the expected ones"), P("观察阈值适合当前流量", "The thresholds suit current traffic")],
  requirement: P("需要 2 位审批人用通行密钥批准，发起人不能批准", "2 approvers confirm with passkeys; the proposer can't approve"),
  discussion: [],
  execution: null,
  action_kind: "kubernetes.deployment.rollback",
  rollback: {
    service_id: CHECKOUT_SERVICE_ID,
    application_id: "checkout-api",
    environment_id: "prod",
    namespace: "aiops-lab",
    deployment: "checkout-api",
    from_revision: "7",
    target_revision: "6",
    containers: [{ name: "api", image: IMAGE }],
    changes: [
      { container: "api", field: "env", name: "LAB_FAULTS", from: FAULT, to: null, value_hidden: false },
      { container: "api", field: "env", name: "LAB_API_TOKEN", from: null, to: null, value_hidden: true },
    ],
    verification: plan,
    reason: P("修订 7 上线后 /checkout 5xx 升到 48%，回到上线前的修订 6", "/checkout 5xx rose to 48% after revision 7; back to revision 6"),
  },
});

const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString();
// One sample every 15 s from the start of verification; null = the query had no value (PA-73), which is never ok.
const samplesFrom = (startAgo: number, values: [number | null, number | null][]) =>
  values.map(([err, rate], i) => ({ at: iso(startAgo - i * 15_000), request_rate: rate, error_ratio: err, sample_age_seconds: err === null ? null : 12 + (i % 4) * 2, ok: err !== null && rate !== null && err <= plan.max_error_ratio && rate >= plan.min_request_rate }));
// The first 60 s still carry the bad revision's traffic in the rate windows (settling period, never judged).
const settling: [number, number][] = [[0.31, 1.5], [0.12, 1.6], [0.05, 1.5], [0.021, 1.6]];
const healthy: [number, number][] = [0.006, 0.004, 0.005, 0.003, 0.004, 0.002, 0.003, 0.004, 0.003].map((e, i) => [e, [1.4, 1.6, 1.5, 1.7, 1.6, 1.5, 1.6, 1.8, 1.6][i]]);

/** Execution detail for EXE-RB* (events domain routes /executions/:id here): verifying, failed, needs attention, recovered. */
export function rolloutExecution(id: string) {
  const base = {
    id,
    kind: "kubernetes.deployment.rollback",
    proposal_id: "PRP-RB7",
    investigation_id: "INV-236",
    application_id: "checkout-api",
    environment_id: "prod",
    approved_at: iso(330_000),
    approvers: [
      { name: "赵倩", verified_at: iso(420_000) },
      { name: "李明", verified_at: iso(330_000) },
    ],
    created_at: iso(325_000),
    updated_at: iso(5_000),
    target: { namespace: "aiops-lab", deployment: "checkout-api", from_revision: "7", target_revision: "6" },
  };
  const tl = (states: [string, number][]) => states.map(([state, ago]) => ({ state, at: iso(ago) }));
  if (id === "EXE-RB2")
    return {
      ...base,
      target: { ...base.target, target_revision: "5" },
      status: "failed",
      status_reason: "verification_failed",
      timeline: tl([["reserved", 210_000], ["dispatching", 209_000], ["applied", 208_000], ["rolling_out", 207_000], ["verifying", 140_000], ["failed", 65_000]]),
      rollout: { desired: 2, updated: 2, available: 2, current_revision: "8", observed_at: iso(140_000) },
      verification: { plan, started_at: iso(140_000), finished_at: iso(65_000), result: "failed", samples: samplesFrom(140_000, [[0.47, 1.5], [0.46, 1.6], [0.45, 1.5], [0.46, 1.5], [0.45, 1.6], [0.46, 1.5]]) },
    };
  // Pods restarted after the settling period: two samples had no requests (null values), so it can't count as recovered.
  if (id === "EXE-RB5")
    return {
      ...base,
      status: "needs_attention",
      status_reason: "verification_unavailable",
      timeline: tl([["reserved", 250_000], ["dispatching", 249_000], ["applied", 248_000], ["rolling_out", 247_000], ["verifying", 180_000], ["needs_attention", 75_000]]),
      rollout: { desired: 2, updated: 2, available: 2, current_revision: "8", observed_at: iso(180_000) },
      verification: { plan, started_at: iso(180_000), finished_at: iso(75_000), result: "unavailable", samples: samplesFrom(180_000, [...settling, [0.004, 1.5], [null, null], [null, null], [0.003, 1.4]]) },
    };
  if (id === "EXE-RB3")
    return {
      ...base,
      status: "needs_attention",
      status_reason: "drift",
      timeline: tl([["reserved", 60_000], ["needs_attention", 58_000]]),
      rollout: null,
      verification: null,
    };
  if (id === "EXE-RB4")
    return {
      ...base,
      status: "succeeded",
      status_reason: null,
      timeline: tl([["reserved", 480_000], ["dispatching", 479_000], ["applied", 478_000], ["rolling_out", 477_000], ["verifying", 412_000], ["succeeded", 52_000]]),
      rollout: { desired: 2, updated: 2, available: 2, current_revision: "8", observed_at: iso(412_000) },
      verification: { plan, started_at: iso(412_000), finished_at: iso(52_000), result: "passed", samples: samplesFrom(412_000, [...settling, ...healthy, ...healthy, ...healthy.slice(0, 2)]) },
    };
  return {
    ...base,
    status: "verifying",
    status_reason: null,
    timeline: tl([["reserved", 262_000], ["dispatching", 261_000], ["applied", 260_000], ["rolling_out", 259_000], ["verifying", 195_000]]),
    rollout: { desired: 2, updated: 2, available: 2, current_revision: "8", observed_at: iso(195_000) },
    verification: { plan, started_at: iso(195_000), finished_at: null, result: "pending", samples: samplesFrom(195_000, [...settling, ...healthy]) },
  };
}
