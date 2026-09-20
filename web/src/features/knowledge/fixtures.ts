export type KnowledgeDocument = {
  id: string;
  application_id: string;
  title: string;
  body: string;
  source: string;
  version: number;
  content_hash: string;
  status: string;
  error?: string;
  created_at: string;
  generation_id: number;
  embedding_provider?: string;
  embedding_model?: string;
  dimensions?: number;
  investigation_id?: string;
  evidence_ids?: string[];
};
export type KnowledgeHit = {
  document_id: string;
  title: string;
  source: string;
  version: number;
  content: string;
  similarity: number;
  chunk: number;
  generation_id: number;
};
export type KnowledgeDraft = {
  id: string;
  investigation_id: string;
  application_id: string;
  title: string;
  body: string;
  evidence_ids: string[];
  created_at: string;
  document_id?: string;
};
export const previewDocuments: KnowledgeDocument[] = [
  {
    id: "preview-runbook",
    application_id: "preview-checkout",
    title: "发布后 5xx 排查",
    source: "运维团队 / Runbook",
    version: 3,
    status: "ready",
    content_hash: "preview",
    created_at: "2026-09-20T06:20:00Z",
    generation_id: 1,
    embedding_provider: "ollama",
    embedding_model: "bge-m3",
    dimensions: 1024,
    body: `## 一、适用条件
应用发布后出现 5xx 比例上升，且可能与数据库连接、下游依赖或资源配置变更有关时，可参照本 Runbook 进行排查。

## 二、排查步骤
### ① 关联最近发布
确认发布的时间、版本和变更内容，识别与问题发生时间的相关性。
### ② 对比连接池配置
检查应用的数据库连接池配置是否变更，重点关注最大连接数、最小连接数等参数。
\`\`\`yaml
# 应用配置（示例）
connection_pool.max: 50  # 之前为 200
\`\`\`
### ③ 检查连接等待与超时
查看数据库的连接等待、超时和拒绝连接等指标，结合应用日志确认是否存在连接耗尽。
### ④ 生成处置方案并确认
根据根因选择合适的处置方案（如调整连接池、回滚版本等），并在变更前评估影响。确认后执行。

## 三、恢复验证
完成处置后，需要确认系统已恢复正常，建议按以下项逐一验证：
- 5xx 比例恢复至正常水平（如 < 1%）
- 数据库连接等待与超时指标恢复正常
- 核心业务流程（下单、支付）可用性正常`,
  },
  ...[
    {
      title: "发布流程",
      body: "## 发布前检查\n核对镜像、变更范围和回滚版本。\n\n## 发布后观察\n观察真实业务请求、错误率和延迟。",
    },
    { title: "灰度发布", body: "## 灰度观察\n逐步扩大流量，并保留上一版本。" },
    {
      title: "连接池管理",
      body: "## 连接池配置\n根据数据库最大连接数、实例数及并发负载配置连接池。",
    },
    {
      title: "慢查询分析",
      body: "## 收集证据\n关联查询耗时、数据库负载和业务请求。",
    },
    {
      title: "连接超时处理",
      body: "## 排查范围\n核对连接池、数据库负载及网络连通性。",
    },
    {
      title: "Pod 异常排查",
      body: "## 查看状态\n检查 Pod 状态、事件以及容器日志。",
    },
    { title: "资源配置", body: "## 容量评估\n结合实际请求负载核对资源限制。" },
    {
      title: "集群运维",
      body: "## 只读取证\n查看工作负载状态与事件，变更需要人工批准。",
    },
  ].map((d, i) => ({
    ...d,
    id: `preview-doc-${i}`,
    application_id: "preview-checkout",
    source: "演示 Runbook",
    version: 1,
    status: "ready",
    content_hash: "preview",
    created_at: "2026-09-20T06:00:00Z",
    generation_id: 1,
  })),
];
export const previewHits: KnowledgeHit[] = [
  {
    document_id: "preview-case-1",
    title: "订单服务连接池耗尽 · 事故复盘",
    source: "事故复盘",
    version: 2,
    content:
      "v2.6.0 发布后出现 5xx 上升，因数据库连接池从 200 调整为 50，导致连接耗尽。",
    similarity: 0.93,
    chunk: 0,
    generation_id: 1,
  },
  {
    document_id: "preview-doc-4",
    title: "数据库连接超时 · Runbook",
    source: "Runbook",
    version: 1,
    content:
      "当应用出现数据库连接超时时，可以从连接池配置、数据库负载和网络三个方向进行排查。",
    similarity: 0.89,
    chunk: 0,
    generation_id: 1,
  },
  {
    document_id: "preview-case-3",
    title: "结算服务配置变更 · 历史调查",
    source: "历史调查",
    version: 1,
    content:
      "v2.3.0 因连接池参数不当导致数据库连接超时，通过调整连接池并重启应用恢复。",
    similarity: 0.85,
    chunk: 0,
    generation_id: 1,
  },
];
export const previewDraft: KnowledgeDraft = {
  id: "preview-draft",
  investigation_id: "preview-investigation",
  application_id: "preview-checkout",
  title: "checkout-api 发布后连接超时",
  body: "v2.8.1 发布后 5xx 从 0.2% 上升至 8.4%，因数据库连接池从 200 调整为 50。处置前核对证据和目标版本，恢复后连续观察业务请求和指标。",
  evidence_ids: ["preview-evidence"],
  created_at: "2026-09-20T06:28:00Z",
};
