import { mock, MockError } from "../router";
import { l } from "../../i18n";
import { series } from "../../charts";

const P = (zh: string, en: string) => l(zh, en);
const tag = (label: unknown, tone?: string, mono?: boolean) => ({ label, tone, mono });
const tool = (name: string) => tag(name, "accent", true);

/* ---------------- connectors ---------------- */
const CONNECTORS = [
  { id: "claude", brand: "claude", name: "Claude", kind: P("模型", "Model"), category: "model", status: "ok", tags: [tag(P("推理", "Reasoning")), tag(P("默认模型", "Default"))], meta: P("今日 312 次调用", "312 calls today") },
  { id: "openai", brand: "openai", name: P("OpenAI 兼容", "OpenAI-compatible"), kind: P("模型", "Model"), category: "model", status: "ok", tags: [tag(P("推理", "Reasoning"))], meta: P("备用模型", "Fallback model") },
  { id: "ollama", brand: "ollama", name: P("Ollama · 本地", "Ollama · local"), kind: P("模型 + Embedding", "Model + embedding"), category: "model", status: "ok", tags: [tag("bge-m3")], meta: P("Runbook 检索使用", "Used for runbook search") },
  { id: "prometheus", brand: "prometheus", name: P("Prometheus · 生产", "Prometheus · prod"), kind: P("指标", "Metrics"), category: "observe", status: "ok", tags: [tool("query_metrics"), tag(P("深链 Grafana", "Grafana deep links"))], meta: P("5 个应用 · 2 分钟前检查", "5 apps · checked 2 min ago") },
  { id: "victoriametrics", brand: "victoriametrics", name: "VictoriaMetrics", kind: P("长期指标", "Long-term metrics"), category: "observe", status: "ok", tags: [tool("query_metrics")], meta: P("90 天历史 · 容量评估", "90-day history · capacity") },
  { id: "tempo", brand: "tempo", name: P("Tempo · 生产", "Tempo · prod"), kind: P("链路", "Traces"), category: "observe", status: "ok", tags: [tool("query_traces")], meta: P("4 个应用", "4 apps") },
  { id: "elasticsearch", brand: "elasticsearch", name: "Elasticsearch", kind: P("日志", "Logs"), category: "observe", status: "ok", tags: [tool("search_logs"), tag(P("深链 Kibana", "Kibana deep links"))], meta: P("5 个应用", "5 apps") },
  { id: "grafana", brand: "grafana", name: "Grafana", kind: P("看板与深链", "Dashboards & links"), category: "observe", status: "ok", tags: [tag(P("3 个深链模板", "3 link templates"))], meta: P("只读", "Read-only") },
  { id: "kibana", brand: "kibana", name: "Kibana", kind: P("日志深链", "Log deep links"), category: "observe", status: "unverified", tags: [tag(P("深链模板", "Link template"))], meta: P("尚未完成连接测试", "Connection not tested yet") },
  { id: "alertmanager", brand: "prometheus", name: "Alertmanager", kind: P("告警接入", "Alert intake"), category: "observe", status: "ok", tags: [tag(P("Webhook · 3 条路由", "Webhook · 3 routes"))], meta: P("今日 27 条告警", "27 alerts today") },
  { id: "gitlab", brand: "gitlab", name: "GitLab", kind: P("代码与流水线", "Code & pipelines"), category: "delivery", status: "ok", tags: [tool("get_commit_diff"), tag("Webhook")], meta: P("42 个项目", "42 projects") },
  { id: "githubactions", brand: "githubactions", name: "GitHub Actions", kind: P("流水线", "Pipelines"), category: "delivery", status: "ok", tags: [tool("list_runs")], meta: P("3 个仓库", "3 repos") },
  { id: "argo", brand: "argo", name: "Argo CD", kind: P("部署", "Deployments"), category: "delivery", status: "ok", tags: [tool("get_app_status")], meta: P("27 个应用", "27 apps") },
  { id: "jenkins", brand: "jenkins", name: "Jenkins", kind: P("流水线", "Pipelines"), category: "delivery", status: "unverified", tags: [tool("list_builds")], meta: P("尚未完成连接测试", "Connection not tested yet") },
  { id: "harbor", brand: "harbor", name: "Harbor", kind: P("镜像仓库", "Registry"), category: "delivery", status: "error", tags: [tool("list_images")], meta: P("认证失败（HTTP 401）", "Auth failed (HTTP 401)") },
  { id: "kubernetes", brand: "kubernetes", name: "Kubernetes · prod-sh-1", kind: P("集群", "Cluster"), category: "infra", status: "ok", tags: [tool("inspect_deployment"), tag(P("写：仅经审批", "Writes: approval only"), "warn")], meta: P("12 个工作负载", "12 workloads") },
  { id: "feishu", brand: "feishu", name: P("飞书", "Feishu"), kind: P("IM · 通知与机器人", "IM · bot & notify"), category: "im", status: "ok", tags: [tag(P("双向", "Two-way")), tag(P("卡片审批", "Card approvals"))], meta: P("2 个群 · 13:42", "2 groups · 13:42") },
  { id: "dingtalk", brand: "dingtalk", name: P("钉钉", "DingTalk"), kind: P("IM · 通知", "IM · notify"), category: "im", status: "ok", tags: [tag(P("通知", "Notify"))], meta: P("支付值班群", "Payments on-call") },
  { id: "slack", brand: "slack", name: "Slack", kind: P("IM · 通知", "IM · notify"), category: "im", status: "unverified", tags: [tag(P("通知", "Notify"))], meta: P("尚未完成连接测试", "Connection not tested yet") },
  { id: "confluence", brand: "confluence", name: "Confluence", kind: P("知识来源", "Knowledge"), category: "knowledge", status: "ok", tags: [tag(P("每 6 小时同步", "Syncs every 6 h"))], meta: P("312 篇文档", "312 docs") },
  { id: "langfuse", brand: "langfuse", name: "Langfuse", kind: P("Agent Trace 与评测", "Agent traces & evals"), category: "agent_obs", status: "ok", tags: [tag("OTLP"), tag(P("评测同步", "Eval sync"))], meta: P("今日 312 条 trace", "312 traces today") },
  { id: "phoenix", brand: "phoenix", name: "Arize Phoenix", kind: P("Agent Trace", "Agent traces"), category: "agent_obs", status: "ok", tags: [tag("OTLP")], meta: P("实验环境", "Test env") },
  { id: "vault", brand: "vault", name: "HashiCorp Vault", kind: P("密钥", "Secrets"), category: "security", status: "ok", tags: [tag(P("凭据引用", "Credential refs"))], meta: P("14 个连接器使用", "Used by 14 connectors") },
];
const CATS = [
  ["all", P("全部", "All")],
  ["model", P("模型", "Models")],
  ["observe", P("观测", "Observability")],
  ["delivery", P("代码与交付", "Code & delivery")],
  ["infra", P("基础设施", "Infrastructure")],
  ["im", P("协作与 IM", "Chat & IM")],
  ["knowledge", P("知识", "Knowledge")],
  ["agent_obs", P("Agent 可观测", "Agent observability")],
  ["security", P("安全", "Security")],
] as const;

mock("GET", "/connectors", ({ query }) => {
  const cat = String(query.category ?? "all");
  const q = String(query.q ?? "").toLowerCase();
  const items = CONNECTORS.filter((c) => (cat === "all" || c.category === cat) && (!q || c.id.includes(q) || JSON.stringify(c.name).toLowerCase().includes(q)));
  const counts = { ok: 0, unverified: 0, error: 0 } as Record<string, number>;
  CONNECTORS.forEach((c) => counts[c.status]++);
  return { items, counts, categories: CATS.map(([id, label]) => ({ id, label, count: id === "all" ? CONNECTORS.length : CONNECTORS.filter((c) => c.category === id).length })) };
});

mock("GET", "/connectors/:id", ({ params }) => {
  const c = CONNECTORS.find((x) => x.id === params.id);
  if (!c) throw new MockError(404, "not_found", "connector not found");
  const obs = c.category === "observe";
  return {
    ...c,
    scope: P("全局连接器", "Global connector"),
    address: `https://${c.id}.prod.example.internal`,
    auth: { brand: "vault", text: P("Vault 引用 · 不回显", "Vault reference · never shown") },
    checked: P("2 分钟前 · 126 ms", "2 min ago · 126 ms"),
    services: ["checkout-api", "payment-gateway", "order-service", "inventory-worker", "search-api"],
    queries: obs ? ["p99_latency", "error_rate", "qps", "cpu_usage", "db_connection_wait_p95"] : [],
    tools: c.tags.filter((t) => t.mono).map((t) => ({ name: t.label as string, desc: P("只读 · 每轮最多 8 次", "Read-only · max 8 per turn"), enabled: true })),
    deeplink: obs ? "grafana/d/svc-overview?var-app={app}&from={from}&to={to}" : undefined,
    // Kubernetes (PA-08 design): workload targets with the catalog's rollback setting and the deployer's write approval.
    ...(c.id === "kubernetes"
      ? {
          address: "https://192.168.105.10:6443",
          auth: { brand: "kubernetes", text: P("Bearer 令牌 · 只读 · 不回显", "Bearer token · read-only · never shown") },
          services: ["checkout-api", "order-service", "payment-gateway"],
          workloads: [
            { key: "checkout-api", namespace: "aiops-lab", deployment: "checkout-api", service: "checkout-api", rollback: { allowed: true, observation_seconds: 300 }, write_approved: true },
            { key: "order-service", namespace: "aiops-lab", deployment: "order-api", service: "order-service", rollback: { allowed: true, observation_seconds: 300 }, write_approved: true },
            { key: "payment-gateway", namespace: "pay", deployment: "payment-gateway", service: "payment-gateway", rollback: { allowed: false, observation_seconds: null }, write_approved: false },
          ],
          capabilities: ["workload.read", "revisions.read", "rollback.propose"],
        }
      : {}),
  };
});
mock("POST", "/connectors/:id/test", () => ({ ok: true, latency_ms: 118 }));
mock("PATCH", "/connectors/:id", ({ body }) => ({ ok: true, ...(body as object) }));
mock("POST", "/connectors", () => ({ id: "gitlab-2" }));

const K8S_TYPE = {
  brand: "kubernetes",
  name: "Kubernetes",
  kind: P("集群", "Cluster"),
  version: "v1.0",
  auth_methods: [{ id: "bearer", label: P("Bearer 令牌（只读）", "Bearer token (read-only)"), desc: P("ServiceAccount 令牌，只用于读取工作负载与修订", "A ServiceAccount token used only to read workloads and revisions") }],
  scopes: [
    { id: "deployments_read", label: P("deployments · get / list", "deployments · get / list"), required: true },
    { id: "replicasets_read", label: P("replicasets · get / list", "replicasets · get / list"), required: true },
  ],
  tools: [
    { name: "inspect_deployment", desc: P("Deployment 状态与副本", "Deployment status and replicas") },
    { name: "list_revisions", desc: P("ReplicaSet 修订与差异", "ReplicaSet revisions and differences") },
  ],
  webhook_url: "",
};
mock("GET", "/connector-types/:type", ({ params }) => params.type === "kubernetes" ? K8S_TYPE : ({
  brand: "gitlab",
  name: "GitLab",
  kind: P("代码与流水线", "Code & pipelines"),
  version: "v1.4",
  auth_methods: [
    { id: "oauth", label: P("OAuth 应用（推荐）", "OAuth app (recommended)"), desc: P("按用户授权，操作留痕到个人；令牌自动刷新", "Per-user authorization with personal audit trail; tokens refresh automatically") },
    { id: "pat", label: P("个人访问令牌", "Personal access token"), desc: P("适合服务账号，需定期轮换", "For service accounts; rotate regularly") },
    { id: "deploy", label: P("部署令牌", "Deploy token"), desc: P("只能读取仓库，不能读取流水线", "Repository read only — no pipelines") },
  ],
  scopes: [
    { id: "read_api", label: P("read_api · 读取流水线、合并请求与提交", "read_api · pipelines, merge requests and commits"), required: true },
    { id: "read_repository", label: P("read_repository · 读取代码差异", "read_repository · code diffs"), required: true },
  ],
  tools: [
    { name: "list_pipelines", desc: P("最近的流水线与状态", "Recent pipelines and status") },
    { name: "get_pipeline", desc: P("阶段、作业与日志摘要", "Stages, jobs and log summary") },
    { name: "get_commit_diff", desc: P("提交与配置差异", "Commit and config diffs") },
    { name: "list_merge_requests", desc: P("合并请求与审批", "Merge requests and approvals") },
  ],
  webhook_url: "https://aiops.example.internal/hooks/gitlab/platform",
}));

/* ---------------- MCP servers ---------------- */
const MCP = [
  { id: "cmdb-mcp", brand: "mcp", name: "cmdb-mcp", desc: P("内部 CMDB（自研）", "Internal CMDB (in-house)"), transport: "Streamable HTTP", caps: P("12 工具 · 3 资源 · 2 提示词", "12 tools · 3 resources · 2 prompts"), trust: { label: P("只读已校验", "Read-only verified"), tone: "ok" }, apps: [{ label: P("交易平台", "Trading") }, { label: P("支付", "Payments") }], status: "ok", last: P("2 分钟前", "2 min ago") },
  { id: "release-mcp", brand: "mcp", name: "release-mcp", desc: P("自研发布平台", "In-house release platform"), transport: "Streamable HTTP", caps: P("8 工具 · 其中 2 个写入", "8 tools · 2 write"), trust: { label: P("含写入 · 需审批", "Has writes · approval"), tone: "warn" }, apps: [{ label: P("交易平台", "Trading") }], status: "ok", last: "14:03" },
  { id: "grafana-mcp", brand: "grafana", name: "grafana-mcp", desc: P("Grafana 官方 MCP", "Official Grafana MCP"), transport: P("stdio · 平台托管", "stdio · hosted"), caps: P("14 工具 · 看板与查询", "14 tools · dashboards & queries"), trust: { label: P("只读已校验", "Read-only verified"), tone: "ok" }, apps: [{ label: P("全部应用", "All apps") }], status: "ok", last: P("6 分钟前", "6 min ago") },
  { id: "github-mcp", brand: "github", name: "github-mcp", desc: P("GitHub 官方 MCP", "Official GitHub MCP"), transport: "Streamable HTTP", caps: P("9 工具 · 4 个写入已禁用", "9 tools · 4 writes disabled"), trust: { label: P("只读已校验", "Read-only verified"), tone: "ok" }, apps: [{ label: "shop" }, { label: "platform" }], status: "ok", last: P("1 小时前", "1 h ago") },
  { id: "k8s-mcp", brand: "kubernetes", name: "k8s-mcp", desc: P("社区 · 仅实验集群 aiops-test", "Community · aiops-test cluster only"), transport: "SSE", caps: P("17 工具 · 新版本待审核", "17 tools · new version pending"), trust: { label: P("待审核 2", "2 to review"), tone: "warn" }, apps: [{ label: P("实验环境", "Test env"), tone: "dev" }], status: "pending", last: P("昨天", "Yesterday") },
];
mock("GET", "/mcp-servers", () => ({
  kpis: [
    { id: "servers", label: P("已登记服务器", "Registered servers"), value: "5", sub: P("4 正常 · 1 待审核", "4 healthy · 1 pending"), series: [] },
    { id: "tools", label: P("发现的工具", "Tools discovered"), value: "60", sub: P("只读 50 · 写入 10（默认关闭）", "50 read · 10 write (off by default)"), series: [] },
    { id: "calls", label: P("7 天调用", "Calls · 7 d"), value: "3,214", sub: P("P95 184 ms · 失败 0.4%", "P95 184 ms · 0.4% failed"), series: series(14, 420, 90, 7, 3) },
    { id: "used", label: P("被 Agent 使用", "Used by agents"), value: "31", sub: P("工具 · 在 3 个 Agent 中启用", "tools · across 3 agents"), series: [] },
  ],
  items: MCP,
  pending_change: {
    server: "k8s-mcp",
    title: P("k8s-mcp v0.9 → v1.0：发现 2 个新工具", "k8s-mcp v0.9 → v1.0: 2 new tools"),
    tools: [tag(P("exec_in_pod · 写入", "exec_in_pod · write"), "danger", true), tag(P("get_events · 只读", "get_events · read"), "ok", true)],
    note: P("新工具审核通过前不会对 Agent 可见；写入工具即使通过也只能以提案方式使用。", "New tools stay hidden from agents until reviewed; write tools can only be used through proposals even after approval."),
  },
}));
mock("GET", "/mcp-servers/:id", ({ params }) => {
  const s = MCP.find((x) => x.id === params.id);
  if (!s) throw new MockError(404, "not_found", "server not found");
  return {
    ...s,
    version: "v1.4.2",
    counts: { tools: 12, resources: 3, prompts: 2 },
    tools: [
      { name: "get_service", desc: P("服务元数据、等级与负责人", "Service metadata, tier and owner"), write: false, enabled: true, calls: P("1,204 次", "1,204 calls") },
      { name: "list_dependencies", desc: P("服务上下游依赖", "Upstream and downstream dependencies"), write: false, enabled: true, calls: P("862 次", "862 calls") },
      { name: "search_hosts", desc: P("按标签检索主机与虚拟机", "Find hosts and VMs by label"), write: false, enabled: true, calls: P("311 次", "311 calls") },
      { name: "get_oncall", desc: P("当前值班与升级路径", "Current on-call and escalation"), write: false, enabled: true, calls: P("540 次", "540 calls") },
      { name: "update_owner", desc: P("修改服务负责人", "Change a service owner"), write: true, enabled: false, calls: P("0 次", "0 calls") },
    ],
    resources: [
      { uri: "cmdb://services/{id}", desc: P("服务卡片", "Service card") },
      { uri: "cmdb://hosts/{id}", desc: P("主机详情", "Host detail") },
      { uri: "cmdb://teams/{id}", desc: P("团队与负责人", "Team and owners") },
    ],
    prompts: [
      { name: "service-brief", desc: P("生成服务简报", "Write a service brief") },
      { name: "owner-lookup", desc: P("查找负责人与升级路径", "Find owner and escalation") },
    ],
    calls: [
      { time: "14:31:02", agent: P("调查 Agent", "Investigator"), tool: "get_service", ms: 84, ok: true },
      { time: "14:30:58", agent: P("调查 Agent", "Investigator"), tool: "list_dependencies", ms: 122, ok: true },
      { time: "14:22:10", agent: P("巡查 Agent", "Patrol"), tool: "get_oncall", ms: 64, ok: true },
      { time: "13:58:41", agent: P("调查 Agent", "Investigator"), tool: "search_hosts", ms: 5000, ok: false },
    ],
    trust_info: { readonly_check: P("工具声明 readOnlyHint · 管理员 周航 确认", "Tools declare readOnlyHint · confirmed by admin Zhou Hang"), scope: [P("交易平台", "Trading"), P("支付", "Payments")], limit: P("每轮最多 6 次 · 超时 5 s", "Max 6 per turn · 5 s timeout"), auth: P("OAuth 客户端 · Vault 引用", "OAuth client · Vault reference") },
  };
});
mock("PATCH", "/mcp-servers/:id/tools/:name", ({ body }) => ({ ok: true, ...(body as object) }));
mock("POST", "/mcp-servers/:id/review", () => ({ ok: true }));

/* ---------------- generic ingest ---------------- */
mock("GET", "/ingest/pull/:id", () => ({
  id: "pull-ops-metrics",
  name: P("自研监控平台", "In-house monitoring"),
  step: 2,
  request: {
    method: "GET",
    host: P("HTTPS · 内网 · ops-api.internal", "HTTPS · internal · ops-api.internal"),
    path: "/v2/metrics/query",
    params: [
      { k: "service", v: "{service}", variable: true },
      { k: "metric", v: "{metric}", variable: true },
      { k: "from", v: "{start}", variable: true },
      { k: "to", v: "{end}", variable: true },
      { k: "step", v: "30s", variable: false },
    ],
    auth: "vault://ops-api/token",
    rate: P("每秒 10 次 · 超时 5 s · 失败重试 1 次", "10 req/s · 5 s timeout · 1 retry"),
    paging: P("无", "None"),
  },
  sample: {
    status: 200,
    meta: P("84 ms · 3.2 KB · 取自 14:31 的测试请求", "84 ms · 3.2 KB · test request at 14:31"),
    body: '{\n  "data": [\n    {\n      "svc": "checkout-api",\n      "metric": "db_wait_p95",\n      "unit": "ms",\n      "tags": {"pod": "7f9c-4", "env": "prod"},\n      "points": [\n        [1790160180000, 12.4],\n        [1790160210000, 13.1],\n        [1790160240000, 418.0]\n      ]\n    }\n  ],\n  "next": null\n}',
    highlights: [
      { text: '"checkout-api"', color: "c1" },
      { text: '"db_wait_p95"', color: "c2" },
      { text: '"ms"', color: "c5" },
      { text: '{"pod": "7f9c-4", "env": "prod"}', color: "c6" },
      { text: "1790160180000", color: "c3" },
      { text: "1790160210000", color: "c3" },
      { text: "1790160240000", color: "c3" },
      { text: "12.4", color: "c4" },
      { text: "13.1", color: "c4" },
      { text: "418.0", color: "c4" },
    ],
  },
  contract: "metric",
  contract_version: "MetricSeries v1",
  fields: [
    { field: "service", required: true, source: "$.data[*].svc", color: "c1", state: "ok" },
    { field: "name", required: true, source: "$.data[*].metric", color: "c2", state: "ok" },
    { field: "timestamp", required: true, source: "$.data[*].points[*][0]", transform: P("转换：Unix 毫秒 → RFC 3339", "Transform: Unix ms → RFC 3339"), color: "c3", state: "ok" },
    { field: "value", required: true, source: "$.data[*].points[*][1]", transform: P("类型：float64", "Type: float64"), color: "c4", state: "ok" },
    { field: "unit", required: false, source: "$.data[*].unit", color: "c5", state: "ok" },
    { field: "labels", required: false, source: "$.data[*].tags", transform: P("键值对，保留 pod、env", "Key/value, keep pod and env"), color: "c6", state: "ok" },
    { field: "env", required: false, source: P("常量「生产」", "Constant “prod”"), transform: P("未在响应中找到，按连接器默认值", "Not in response — connector default"), color: "faint", state: "warn" },
  ],
  preview: {
    rows: [
      { service: "checkout-api", name: "db_wait_p95", time: "14:03:00", value: "12.4", labels: { pod: "7f9c-4" } },
      { service: "checkout-api", name: "db_wait_p95", time: "14:03:30", value: "13.1", labels: { pod: "7f9c-4" } },
      { service: "checkout-api", name: "db_wait_p95", time: "14:04:00", value: "418.0", labels: { pod: "7f9c-4" }, bad: true },
    ],
    series: series(30, 12, 3, 9, 9).map((v, i) => (i >= 18 ? v + 380 : v)),
    checks: [tag(P("120 条", "120 rows"), "ok"), tag(P("丢弃 0", "0 dropped")), tag(P("时间单调", "Monotonic time"), "ok"), tag(P("单位一致", "Consistent units"), "ok")],
  },
}));
mock("POST", "/ingest/pull/:id/test", () => ({ ok: true, rows: 120, dropped: 0 }));

mock("GET", "/ingest/push", () => ({
  types: [
    { id: "alert", icon: "bell", name: P("告警", "Alerts"), schema: "alert.v1", today: "1,284", rejected: "0.2%", tone: "ok" },
    { id: "change", icon: "branch", name: P("变更与发布", "Changes & releases"), schema: "change.v1", today: "64", rejected: "0", tone: "ok" },
    { id: "incident", icon: "alert", name: P("事件", "Incidents"), schema: "incident.v1", today: "12", rejected: "0", tone: "ok" },
    { id: "resource", icon: "box", name: P("资源变更", "Resource changes"), schema: "resource.v1", today: "3,420", rejected: "1.1%", tone: "warn" },
    { id: "oncall", icon: "user", name: P("值班与排班", "On-call schedules"), schema: "oncall.v1", today: "8", rejected: "0", tone: "ok" },
  ],
  endpoint: {
    method: "POST",
    path: "/api/v1/events/change",
    host: "aiops.example.internal",
    headers: [
      { k: "Authorization", v: P("Bearer <令牌>", "Bearer <token>"), note: P("令牌范围 changes:write，在「Webhook 与 API」中创建", "Token scope changes:write, created under Webhooks & API") },
      { k: "Idempotency-Key", v: P("发送方生成的唯一键", "Unique key from the sender"), note: P("24 小时内相同键只处理一次，重复返回 409", "Same key processed once per 24 h; duplicates return 409") },
      { k: "X-Signature", v: P("sha256=HMAC(时间戳.正文, 密钥)", "sha256=HMAC(timestamp.body, secret)"), note: P("同时发送 X-Timestamp，偏差超过 5 分钟拒绝", "Send X-Timestamp too; skew over 5 min is rejected") },
    ],
    example: '{\n  "source": "jenkins",\n  "service": "checkout-api",\n  "env": "production",\n  "kind": "deploy",\n  "version": "v2.8.1",\n  "status": "succeeded",\n  "started_at": "2026-09-20T14:01:12+08:00",\n  "finished_at": "2026-09-20T14:03:40+08:00",\n  "commit": "a1f9c2e",\n  "url": "https://jenkins.internal/job/checkout/812"\n}',
    responses: [
      { code: "202", tone: "ok", text: P("已接收，异步处理", "Accepted, processed async") },
      { code: "409", tone: "neutral", text: P("重复：幂等键已处理", "Duplicate idempotency key") },
      { code: "422", tone: "danger", text: P("不符合 schema，返回字段错误", "Schema violation with field errors") },
      { code: "401", tone: "danger", text: P("签名或令牌无效", "Invalid signature or token") },
    ],
  },
  deliveries: [
    { time: "14:03:41", source: "jenkins", key: "jenkins-812-deploy", code: "202", tone: "ok", result: P("已接收", "Accepted"), ms: "38 ms" },
    { time: "14:03:41", source: "jenkins", key: "jenkins-812-deploy", code: "409", tone: "neutral", result: P("重复", "Duplicate"), ms: "6 ms" },
    { time: "13:12:05", source: "gitlab", key: "gl-8811-pipeline", code: "202", tone: "ok", result: P("已接收", "Accepted"), ms: "41 ms" },
    { time: "12:40:19", source: "argo", key: "argo-sync-5521", code: "422", tone: "danger", result: P("env 缺失", "env missing"), ms: "12 ms" },
  ],
  stack: {
    enabled: false,
    components: [
      { brand: "opentelemetry", name: "OpenTelemetry Collector", role: P("接收 OTLP · 转发到下面三个后端", "Receives OTLP · forwards to the backends below") },
      { brand: "victoriametrics", name: "VictoriaMetrics", role: P("指标 · 默认保留 30 天", "Metrics · 30-day retention") },
      { brand: "loki", name: "Loki", role: P("日志 · 默认保留 7 天", "Logs · 7-day retention") },
      { brand: "tempo", name: "Tempo", role: P("链路 · 默认保留 3 天", "Traces · 3-day retention") },
      { brand: "grafana", name: P("Grafana（可选）", "Grafana (optional)"), role: P("看板与深链", "Dashboards & deep links") },
    ],
    helm: "helm install aiops-starter \\\n  oci://registry.example.internal/charts/starter \\\n  -n observability -f values.yaml",
    compose: "curl -fsSL https://aiops.example.internal/starter/compose.yaml -o compose.yaml\ndocker compose -p aiops-starter up -d",
    estimate: P("约 4 vCPU · 8 GiB 内存 · 200 GiB 磁盘（每天约 1 亿个样本）", "~4 vCPU · 8 GiB RAM · 200 GiB disk (~100M samples/day)"),
  },
}));
mock("POST", "/ingest/push/test", () => ({ code: 202 }));

/* ---------------- service mapping ---------------- */
const ok = (value: string, brand?: string) => ({ state: "ok", value, brand });
const sug = (value: string) => ({ state: "suggested", value });
const miss = { state: "missing" };
const MAP_SYSTEMS = [
    { id: "metrics", brand: "prometheus", label: P("指标", "Metrics") },
    { id: "logs", brand: "elasticsearch", label: P("日志", "Logs") },
    { id: "traces", brand: "tempo", label: P("链路", "Traces") },
    { id: "code", brand: "gitlab", label: P("代码", "Code") },
    { id: "deploy", brand: "argo", label: P("部署", "Deploy") },
    { id: "dash", brand: "grafana", label: P("看板", "Dashboards") },
    { id: "workload", brand: "kubernetes", label: P("工作负载", "Workload") },
];
const MAP_ROWS: { service: string; cells: Record<string, { state: string; value?: string; brand?: string }> }[] = [
    { service: "checkout-api", cells: { metrics: ok("job=checkout"), logs: ok("logs-checkout-*"), traces: ok("checkout-api"), code: ok("platform/checkout"), deploy: ok("checkout-prod"), dash: ok("svc-overview"), workload: ok("aiops-lab/checkout-api") } },
    { service: "payment-gateway", cells: { metrics: ok("job=payment"), logs: ok("logs-payment-*"), traces: sug("payment-gw"), code: ok("platform/payment"), deploy: ok("payment-prod"), dash: ok("svc-overview"), workload: ok("pay/payment-gateway") } },
    { service: "order-service", cells: { metrics: ok("job=order"), logs: ok("logs-order-*"), traces: ok("order-service"), code: ok("shop/order-service", "github"), deploy: ok("order-prod"), dash: ok("svc-overview"), workload: ok("trade/order-service") } },
    { service: "inventory-worker", cells: { metrics: ok("job=inventory"), logs: miss, traces: ok("inventory-worker"), code: ok("platform/inventory"), deploy: ok("inventory-prod"), dash: sug("kafka-consumers"), workload: ok("trade/inventory-worker") } },
    { service: "user-center", cells: { metrics: ok("job=user"), logs: ok("logs-user-*"), traces: miss, code: ok("user-center-release", "jenkins"), deploy: sug("user-center-prod"), dash: ok("svc-overview"), workload: ok("account/user-center") } },
    { service: "search-api", cells: { metrics: ok("job=search"), logs: ok("logs-search-*"), traces: miss, code: ok("platform/search"), deploy: ok("search-prod"), dash: ok("search-latency"), workload: ok("search/search-api") } },
    { service: "notification-svc", cells: { metrics: ok("job=notify"), logs: sug("logs-notify-*"), traces: miss, code: ok("platform/notify"), deploy: ok("notify-prod"), dash: miss, workload: ok("platform/notification-svc") } },
];
// Registry stage 2 (approved editor design): each row's save snapshot with real connector IDs, and per data plane the
// connectors of this environment plus the reviewed catalog resources the editor offers.
const MAP_CONNECTOR: Record<string, [string, string]> = { metrics: ["lab-prometheus", "prometheus"], logs: ["lab-elasticsearch", "elasticsearch"], traces: ["lab-tempo", "tempo"], workload: ["lab-kubernetes", "kubernetes"], code: ["gitlab", "gitlab"], deploy: ["argo", "argocd"], dash: ["grafana", "grafana"] };
const mapSave = (r: (typeof MAP_ROWS)[number]) => {
  const bindings = Object.entries(r.cells)
    .filter(([sys, c]) => c.state === "ok" && c.value && MAP_CONNECTOR[sys])
    .map(([sys, c]) => ({ system_id: sys, kind: MAP_CONNECTOR[sys][1], connector_id: MAP_CONNECTOR[sys][0], resource: sys === "logs" && r.service === "payment-gateway" ? "logs-payment-old" : (c.value as string) }));
  // order-service stands for the stage 2–5 window: telemetry rows read-only until mapping writes switch over.
  if (r.service === "order-service") return { service_id: r.service, service_environment_id: "prod", binding_revision: 6, bindings, editable: false, unavailable_reason: "registry_cutover" };
  return { service_id: r.service, service_environment_id: "prod", binding_revision: 6, bindings, editable: true };
};
const keyOfService = (id: string) => {
  try {
    const raw = atob(id.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(id.length / 4) * 4, "="));
    const i = raw.indexOf("\u0000");
    return i > 0 ? raw.slice(0, i) : id;
  } catch {
    return id;
  }
};
const mapOptions = (service: string) => {
  const r = MAP_ROWS.find((x) => x.service === service);
  const v = (sys: string) => (r?.cells[sys]?.state === "ok" ? r.cells[sys].value : undefined);
  return {
    service_id: service,
    environment_id: "prod",
    systems: {
      metrics: {
        connectors: [
          { id: "lab-prometheus", name: "lab-prometheus", provider: "Prometheus", ready: true },
          { id: "lab-victoriametrics", name: "lab-victoriametrics", provider: "VictoriaMetrics", ready: false, not_ready_reason: P("读凭据未批准", "Read credential not approved") },
        ],
        resources: [v("metrics"), v("metrics") && `${v("metrics")}-canary`].filter(Boolean).map((id) => ({ id: id as string, connector_id: "lab-prometheus" })),
      },
      logs: {
        connectors: [
          { id: "lab-elasticsearch", name: "lab-elasticsearch", provider: "Elasticsearch", ready: true },
          { id: "lab-loki", name: "lab-loki", provider: "Loki", ready: true },
        ],
        // inventory-worker has nothing registered for logs: the editor says the deployer has to add it.
        resources:
          service === "inventory-worker"
            ? []
            : [
                ...(service === "payment-gateway" ? ["logs-payment-*"] : [v("logs") ?? `logs-${service}-*`]).map((id) => ({ id, connector_id: "lab-elasticsearch" })),
                { id: `{app="${service}"}`, connector_id: "lab-loki" },
              ],
      },
      // user-center: no trace connector in this environment yet.
      traces: { connectors: service === "user-center" ? [] : [{ id: "lab-tempo", name: "lab-tempo", provider: "Tempo", ready: true }], resources: [{ id: service, connector_id: "lab-tempo" }] },
      workload: { connectors: [{ id: "lab-kubernetes", name: "lab-kubernetes", provider: "Kubernetes", ready: true }], resources: [{ id: v("workload") ?? `aiops-lab/${service}`, connector_id: "lab-kubernetes" }] },
    },
  };
};
mock("GET", "/service-mappings", () => ({ systems: MAP_SYSTEMS, rows: MAP_ROWS.map((r) => ({ ...r, save: mapSave(r) })), suggestions: 5 }));
mock("GET", "/services/:id/external-targets/options", ({ params }) => mapOptions(keyOfService(params.id)));
mock("POST", "/service-mappings/accept", ({ body }) => ({ accepted: (body as { cells?: unknown[] })?.cells?.length ?? 5 }));
mock("POST", "/service-mappings/discover", () => ({ suggestions: 5 }));
mock("PUT", "/service-mappings", ({ body }) => {
  const item = (body as { items?: { service_id: string; bindings: { system_id: string; resource: string }[] }[] })?.items?.[0];
  if (item) {
    const o = mapOptions(item.service_id).systems as Record<string, { resources: { id: string }[] }>;
    const bad = item.bindings.find((b) => o[b.system_id] && !o[b.system_id].resources.some((r) => r.id === b.resource));
    if (bad) throw new MockError(422, "resource_not_in_catalog", `${bad.resource} is not in the catalog`);
  }
  return { ok: true };
});

/* ---------------- coverage ---------------- */
const c = (state: string, extra: Record<string, unknown> = {}) => ({ state, ...extra });
mock("GET", "/coverage", () => ({
  kpis: [
    { id: "services", label: P("服务", "Services"), value: "42", sub: P("按应用映射后的服务数", "Services after app mapping"), series: [] },
    { id: "full", label: P("完整覆盖", "Fully covered"), value: "18", tone: "ok", sub: P("43% · 较上周 +3", "43% · +3 vs last week"), series: [] },
    { id: "partial", label: P("部分覆盖", "Partially covered"), value: "19", tone: "warn", sub: P("多为链路与 Runbook 缺失", "Mostly missing traces and runbooks"), series: [] },
    { id: "gaps", label: P("严重缺失", "Serious gaps"), value: "5", tone: "danger", sub: P("覆盖 < 60%，其中 T1 2 个", "< 60% covered, 2 are T1"), series: [] },
  ],
  types: [
    { label: P("指标", "Metrics"), pct: 95, tone: "ok" },
    { label: P("日志", "Logs"), pct: 81, tone: "warn" },
    { label: P("链路", "Traces"), pct: 52, tone: "danger" },
    { label: P("变更", "Changes"), pct: 86, tone: "ok" },
    { label: P("资源", "Resources"), pct: 97, tone: "ok" },
    { label: "Runbook", pct: 64, tone: "warn" },
    { label: P("告警规则", "Alert rules"), pct: 76, tone: "warn" },
    { label: P("负责人", "Owners"), pct: 95, tone: "ok" },
  ],
  columns: [
    { id: "metrics", label: P("指标", "Metrics") },
    { id: "logs", label: P("日志", "Logs") },
    { id: "traces", label: P("链路", "Traces") },
    { id: "changes", label: P("变更", "Changes") },
    { id: "resources", label: P("资源", "Resources") },
    { id: "runbooks", label: "Runbook" },
    { id: "rules", label: P("告警规则", "Alert rules") },
  ],
  rows: [
    { service: "checkout-api", tier: "T0", owner: "李明", score: 88, cells: { metrics: c("ok", { brand: "prometheus" }), logs: c("ok", { brand: "elasticsearch" }), traces: c("partial", { text: P("服务名未映射", "Name not mapped") }), changes: c("ok", { brand: "gitlab" }), resources: c("ok", { brand: "kubernetes" }), runbooks: c("ok", { text: P("4 篇", "4") }), rules: c("ok", { text: P("6 条", "6") }) } },
    { service: "payment-gateway", tier: "T0", owner: "赵凯", score: 100, cells: { metrics: c("ok", { brand: "prometheus" }), logs: c("ok", { brand: "elasticsearch" }), traces: c("ok", { brand: "tempo" }), changes: c("ok", { brand: "gitlab" }), resources: c("ok", { brand: "kubernetes" }), runbooks: c("ok", { text: P("2 篇", "2") }), rules: c("ok", { text: P("4 条", "4") }) } },
    { service: "orders-db", tier: "T0", owner: "周航", score: 81, cells: { metrics: c("ok", { brand: "prometheus" }), logs: c("partial", { text: P("慢查询日志未接入", "No slow-query log") }), traces: c("na"), changes: c("partial", { text: P("仅 DDL 工单", "DDL tickets only") }), resources: c("ok", { brand: "kubernetes" }), runbooks: c("ok", { text: P("1 篇", "1") }), rules: c("ok", { text: P("3 条", "3") }) } },
    { service: "legacy-billing", tier: "T1", owner: "孙悦", score: 81, cells: { metrics: c("pull", { text: "Pull" }), logs: c("pull", { text: "Pull" }), traces: c("missing", { href: "/integrations/new" }), changes: c("pull", { text: "Push" }), resources: c("pull", { text: "Push" }), runbooks: c("ok", { text: P("3 篇", "3") }), rules: c("ok", { text: P("2 条", "2") }) } },
    { service: "inventory-worker", tier: "T1", owner: "钱多", score: 69, cells: { metrics: c("ok", { brand: "prometheus" }), logs: c("ok", { brand: "elasticsearch" }), traces: c("missing", { href: "/integrations/new" }), changes: c("ok", { brand: "jenkins" }), resources: c("ok", { brand: "kubernetes" }), runbooks: c("missing", { text: P("写 Runbook", "Write runbook"), href: "/runbooks/new" }), rules: c("ok", { text: P("2 条", "2") }) } },
    { service: "search-api", tier: "T1", owner: "吴桐", score: 50, cells: { metrics: c("ok", { brand: "prometheus" }), logs: c("missing", { href: "/integrations/new" }), traces: c("missing", { href: "/integrations/new" }), changes: c("ok", { brand: "githubactions" }), resources: c("ok", { brand: "kubernetes" }), runbooks: c("missing", { text: P("写 Runbook", "Write runbook"), href: "/runbooks/new" }), rules: c("partial", { text: P("1 条", "1") }) } },
    { service: "notification-svc", tier: "T2", score: 38, cells: { metrics: c("pull", { text: "Pull" }), logs: c("ok", { brand: "elasticsearch" }), traces: c("missing", { href: "/integrations/new" }), changes: c("partial", { text: P("无发布记录", "No releases") }), resources: c("ok", { brand: "kubernetes" }), runbooks: c("missing", { text: P("写 Runbook", "Write runbook"), href: "/runbooks/new" }), rules: c("missing", { text: P("加规则", "Add rule"), href: "/alert-rules" }) } },
  ],
  impact: {
    answers: P("412 次回答中标注了数据缺失", "412 answers flagged missing data"),
    flagged: P("23 次", "23"),
    delta: P("−6 次", "−6"),
    lowered: P("其中 19 次置信度降为中或低", "19 had confidence lowered to medium or low"),
    by_type: [
      { label: P("链路", "Traces"), value: 12, tone: "danger" },
      { label: P("日志", "Logs"), value: 6, tone: "warn" },
      { label: "Runbook", value: 3, tone: "warn" },
      { label: P("变更", "Changes"), value: 2 },
    ],
    eval_failures: 4,
  },
  fixes: [
    { text: P("为 checkout-api 映射 Tempo 服务名（checkout → checkout-api）", "Map the Tempo service name for checkout-api (checkout → checkout-api)"), impact: P("可改善 9 次回答", "Improves 9 answers"), action: P("去映射", "Map"), href: "/integrations/mapping" },
    { text: P("search-api 日志：接入 Elasticsearch 索引 search-*", "search-api logs: connect Elasticsearch index search-*"), impact: P("可改善 5 次回答", "Improves 5 answers"), action: P("添加连接器", "Add connector"), href: "/integrations/new" },
    { text: P("notification-svc 没有负责人与告警规则", "notification-svc has no owner or alert rules"), impact: P("影响升级与通知", "Affects escalation"), action: P("设负责人", "Set owner"), href: "/settings/members" },
  ],
}));

/* ---------------- webhooks & API ---------------- */
mock("GET", "/webhooks", () => ({
  inbound: [
    { id: "am-prod", brand: "prometheus", name: P("Alertmanager · 生产", "Alertmanager · prod"), url: "/hooks/alertmanager/prod", verify: "Bearer", count: "27", last: "200 · 14:30", status: "ok" },
    { id: "gitlab", brand: "gitlab", name: P("GitLab 流水线事件", "GitLab pipeline events"), url: "/hooks/gitlab/platform", verify: P("签名令牌", "Secret token"), count: "64", last: "200 · 14:28", status: "ok" },
    { id: "github", brand: "githubactions", name: "GitHub Actions", url: "/hooks/github/shop", verify: "HMAC SHA-256", count: "12", last: "200 · 13:12", status: "ok" },
    { id: "argocd", brand: "argo", name: P("Argo CD 通知", "Argo CD notifications"), url: "/hooks/argocd", verify: "Bearer", count: "27", last: "200 · 14:04", status: "ok" },
    { id: "grafana", brand: "grafana", name: P("Grafana 告警", "Grafana alerts"), url: "/hooks/grafana", verify: "Bearer", count: "0", last: P("401 · 昨天", "401 · yesterday"), status: "error" },
  ],
  deliveries: [
    { time: "14:28:11", event: "pipeline · #8814 running", code: "200", ms: "31 ms" },
    { time: "14:04:02", event: "deployment · checkout-api synced", code: "200", ms: "28 ms" },
    { time: "14:03:40", event: "pipeline · #8812 success", code: "200", ms: "38 ms" },
    { time: "13:49:05", event: "pipeline · #8812 created", code: "200", ms: "35 ms" },
    { time: "13:12:44", event: "pipeline · #8811 failed", code: "200", ms: "30 ms" },
  ],
  tokens: [
    { name: "ci-bot", scopes: ["changes:write", "incidents:read"], used: P("最近使用 14:03 · GitLab 流水线", "Last used 14:03 · GitLab pipeline"), expires: P("剩余 71 天", "71 days left") },
    { name: "grafana-plugin", scopes: ["services:read", "alerts:read"], used: P("最近使用 13:58", "Last used 13:58"), expires: P("剩余 30 天", "30 days left") },
  ],
  mcp: { enabled: true, url: "https://aiops.example.internal/mcp", tools: ["list_incidents", "get_incident", "search_runbooks", "get_service"], clients: P("Claude Desktop（王晨）、内部 Copilot", "Claude Desktop (Wang Chen), internal Copilot") },
  outbound: [
    { event: P("事件状态变化", "Incident state change"), target: "→ ITSM /api/events" },
    { event: P("报告发布", "Report published"), target: P("同步到 SRE 手册", "Sync to SRE handbook"), brand: "confluence" },
  ],
}));
mock("POST", "/webhooks/:id/redeliver", () => ({ ok: true }));
mock("POST", "/api-tokens", () => ({ id: "tok-3", secret_shown_once: "aiops_pat_••••" }));
