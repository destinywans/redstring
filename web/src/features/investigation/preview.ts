// Deliberately isolated design-preview fixtures. Never used by live API queries.
export interface ChatMessage {
  id: string;
  role: string;
  content: string;
  created_at: string;
  model_id?: string;
  evidence_ids?: string[];
}
export interface CanvasBranch {
  id: string;
  investigation_id: string;
  parent_id?: string;
  title: string;
  model_id: string;
  revision: number;
  status: string;
  messages: ChatMessage[];
  inherited_parent_revision?: number;
  synced_parent_revision?: number;
  current_parent_revision?: number;
  parent_has_updates?: boolean;
}
export interface CanvasEvidence {
  id: string;
  branch_id: string;
  kind: string;
  title: string;
  content: string;
  source: string;
  created_at: string;
}
export interface CanvasRun {
  id: string;
  branch_id: string;
  status: string;
  model_id: string;
  attempt: number;
  error?: string;
  created_at: string;
}
export interface CanvasInvestigation {
  id: string;
  application_id: string;
  title: string;
  status: string;
  created_at: string;
  updated_at: string;
  branches: CanvasBranch[];
  evidence: CanvasEvidence[];
  runs: CanvasRun[];
}
export interface MergePreview {
  id: string;
  source_branch_id: string;
  target_branch_id: string;
  source_revision: number;
  target_revision: number;
  additions: ChatMessage[];
  conflicts: string[];
  evidence: CanvasEvidence[];
  conflict_details?: {
    kind: string;
    review: string;
    message: string;
    source_message_ids: string[];
  }[];
}
const stamp = "2026-09-20T14:03:00+08:00";
const message = (id: string, role: string, content: string): ChatMessage => ({
  id,
  role,
  content,
  created_at: stamp,
});
const rootMessage = message(
  "m-root",
  "user",
  "14:03 发布后，5xx 比例从 0.2% 上升到 8.4%，请调查 checkout-api。",
);
export const previewInvestigation: CanvasInvestigation = {
  id: "preview-investigation",
  application_id: "preview-checkout",
  title: "发布异常调查",
  status: "open",
  created_at: stamp,
  updated_at: stamp,
  branches: [
    {
      id: "root",
      investigation_id: "preview-investigation",
      title: "发布后 5xx 上升",
      model_id: "preview-model",
      revision: 3,
      status: "idle",
      messages: [rootMessage],
    },
    {
      id: "code",
      investigation_id: "preview-investigation",
      parent_id: "root",
      title: "代码变更",
      model_id: "preview-model",
      revision: 2,
      status: "idle",
      inherited_parent_revision: 1,
      messages: [
        rootMessage,
        message(
          "m-code",
          "assistant",
          "已对照发布前后的代码变更，当前证据中暂未发现代码异常。",
        ),
      ],
    },
    {
      id: "database",
      investigation_id: "preview-investigation",
      parent_id: "root",
      title: "数据库连接调查",
      model_id: "preview-model",
      revision: 4,
      status: "idle",
      inherited_parent_revision: 2,
      synced_parent_revision: 2,
      current_parent_revision: 3,
      parent_has_updates: true,
      messages: [
        rootMessage,
        message("m-db1", "user", "连接池从 200 调整为 50，会带来什么影响？"),
        message(
          "m-db2",
          "assistant",
          "连接池变小可能导致并发请求无法及时获取连接，出现连接等待上升、获取超时，最终引发 5xx 错误。",
        ),
        message("m-db3", "user", "继续检查连接等待"),
        message(
          "m-db4",
          "assistant",
          "我已查看数据库连接等待时间、活跃连接数和超时错误。示例数据中，14:03 之后连接等待明显上升，与连接池从 200 调整为 50 的变更时间一致。",
        ),
      ],
    },
    {
      id: "upstream",
      investigation_id: "preview-investigation",
      parent_id: "root",
      title: "上游正常",
      model_id: "preview-model",
      revision: 2,
      status: "idle",
      inherited_parent_revision: 1,
      messages: [
        rootMessage,
        message("m-up", "assistant", "此预览场景中的上游服务没有异常。"),
      ],
    },
  ],
  evidence: [
    {
      id: "e-config",
      branch_id: "database",
      kind: "configuration",
      title: "发布配置差异",
      source: "预览 / Deployment 发布记录",
      created_at: stamp,
      content:
        "connection_pool.max: 200 → 50\nconnection_pool.min: 20 → 20\nconnection_pool.timeout: 30s → 30s\nconnection_pool.idle: 10s → 10s",
    },
    {
      id: "e-metrics",
      branch_id: "database",
      kind: "metrics",
      title: "错误率与连接等待",
      source: "预览 / Prometheus",
      created_at: stamp,
      content:
        "示意数据：发布后 HTTP 5xx 从 0.2% 上升至 8.4%；数据库连接等待 p95 从约 50ms 上升至 560ms。",
    },
    {
      id: "e-logs",
      branch_id: "database",
      kind: "logs",
      title: "数据库连接超时日志",
      source: "预览 / Loki",
      created_at: stamp,
      content:
        '14:05:21 ERROR [db] connection timeout after 30000ms, pool exhausted (active=50, max=50)\n14:05:24 ERROR [db] Failed to get connection from pool, pool exhausted (active=50, waiting=128)\n14:05:24 WARN  [http] GET /api/orders 503 1284ms - upstream service unavailable\n14:05:24 INFO  [metrics] http_requests_total{status="503"} 1\n14:05:24 INFO  [db] connection pool active=50, idle=0, waiting=128',
    },
  ],
  runs: [],
};
export const previewChart = Array.from({ length: 31 }, (_, i) => ({
  time: `${13 + Math.floor((30 + i * 2) / 60)}:${String((30 + i * 2) % 60).padStart(2, "0")}`,
  errors: i < 16 ? 0.2 + (i % 3) * 0.05 : 7.8 + (i % 5) * 0.15,
  wait: i < 16 ? 42 + (i % 3) * 8 : 530 + (i % 5) * 18,
}));
export function makePreview(
  source: CanvasBranch,
  target: CanvasBranch,
): MergePreview {
  return {
    id: crypto.randomUUID(),
    source_branch_id: source.id,
    target_branch_id: target.id,
    source_revision: source.revision,
    target_revision: target.revision,
    additions: source.messages.filter(
      (m) => !target.messages.some((t) => t.id === m.id),
    ),
    conflicts: [
      "父调查中的连接池记录与新证据存在差异；合并将保留原始消息及证据，不会自动覆盖事实。",
    ],
    evidence: previewInvestigation.evidence.filter(
      (e) => e.branch_id === source.id,
    ),
  };
}
