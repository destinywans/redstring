import { mock } from "../router";
import { l } from "../../i18n";
import { series } from "../../charts";
import { at } from "../time";

const spike = series(30, 14, 4, 12, 1, [18, 26]);

const graph = {
  investigation: { id: "INV-231", title: l("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), status: "waiting_approval", incident_id: "INC-1042", env: "prod", participants: ["王晨", "李明", "AI"], conversation_id: "C-320" },
  loop: [
    { key: "anomaly", label: l("异常", "Anomaly"), detail: "14:05", state: "done" },
    { key: "evidence", label: l("取证", "Evidence"), detail: l("6 条证据", "6 items"), state: "done" },
    { key: "proposal", label: l("提案", "Proposal"), detail: l("回滚连接池配置", "Roll back pool config"), state: "done" },
    { key: "approval", label: l("审批", "Approval"), detail: l("等待复核", "Awaiting review"), state: "active", href: "/review/PRP-88" },
    { key: "execution", label: l("执行", "Execute"), detail: "—", state: "pending" },
    { key: "verify", label: l("180 秒验证", "180 s verify"), detail: "—", state: "pending" },
  ],
  nodes: [
    { id: "root", kind: "root", title: l("异常：5xx 8.4%", "Anomaly: 5xx 8.4%"), subtitle: l("主线 · 上下文 v7", "Main line · context v7"), status: "active", x: 24, y: 300, w: 240,
      summary: l("14:03 发布 v2.8.1 后，5xx 从 0.2% 升到 8.4%。", "After v2.8.1 at 14:03, 5xx rose from 0.2% to 8.4%."), evidence_refs: ["4"], writeback: undefined,
      context: { version: "v7", tokens: 5800, messages: 9, excludes: [] },
      versions: [
        { v: "v7", text: l("回写：连接池上限被改为 50", "Writeback: pool max changed to 50"), at: at("14:21") },
        { v: "v6", text: l("回写：上游依赖已排除", "Writeback: upstream ruled out"), at: at("14:16") },
        { v: "v5", text: l("回写：代码变更已排除", "Writeback: code change ruled out"), at: at("14:13") },
        { v: "v3", text: l("派生 3 个分支", "Forked 3 branches"), at: at("14:09") },
      ] },
    { id: "b-code", kind: "branch", parent: "root", title: l("代码变更 v2.8.1", "Code change v2.8.1"), subtitle: l("独立上下文 v2 · 已回写", "Isolated context v2 · written back"), status: "ruled_out", x: 320, y: 24, w: 230,
      summary: l("diff 不涉及数据访问层，已排除。", "The diff does not touch the data layer — ruled out."), context: { version: "v2", inherits: "v3", tokens: 3100, messages: 12, excludes: [] }, versions: [] },
    { id: "b-pool", kind: "branch", parent: "root", title: l("数据库连接池", "Database connection pool"), subtitle: l("独立上下文 v4 · 继承主线 v5", "Isolated context v4 · inherits main v5"), status: "confirmed", x: 320, y: 112, w: 290,
      summary: l("**连接等待 P95 12 → 420 ms**，与连接池上限 200 → 50 同时发生[1][2]。", "**Connection wait P95 12 → 420 ms**, at the same moment the pool max went 200 → 50[1][2]."),
      confidence: "high", tools: l("3 个工具 · 11k tokens", "3 tools · 11k tokens"), series: spike, evidence_refs: [l("① 配置差异", "① Config diff"), l("② 连接等待", "② Conn wait"), l("③ 超时日志", "③ Timeout logs")],
      context: { version: "v4", inherits: "v5", tokens: 5400, messages: 6, excludes: [l("代码变更", "Code change"), l("上游依赖", "Upstream")] },
      versions: [
        { v: "v4", text: l("AI 回答 · 引用 3 条证据", "AI answer · 3 citations"), at: at("14:14") },
        { v: "v3", text: l("你：继续检查连接等待", "You: keep checking conn wait"), at: at("14:12") },
        { v: "v2", text: l("AI 回答 · 调用 2 个工具", "AI answer · 2 tool calls"), at: at("14:11") },
        { v: "v1", text: l("从主线 v5 派生", "Forked from main v5"), at: at("14:09") },
      ] },
    { id: "b-up", kind: "branch", parent: "root", title: l("上游依赖", "Upstream dependencies"), subtitle: l("独立上下文 v1 · 已回写", "Isolated context v1 · written back"), status: "ruled_out", x: 320, y: 470, w: 230,
      summary: l("payment-gateway 错误率正常，已排除。", "payment-gateway error rate is normal — ruled out."), context: { version: "v1", inherits: "v3", tokens: 2100, messages: 4, excludes: [] }, versions: [] },
    { id: "b-cmp", kind: "branch", parent: "b-pool", title: l("连接池参数对比", "Pool parameter comparison"), subtitle: l("派生自 数据库连接池 v3", "Forked from Database pool v3"), status: "confirmed", x: 680, y: 130, w: 240,
      summary: l("预发 200、生产 50；改动来自 v2.8.1 配置模板。", "Staging 200, prod 50; the change came from the v2.8.1 config template."), writeback: { to: "root", version: "v7" },
      context: { version: "v2", inherits: "v3", tokens: 3100, messages: 4, excludes: [] }, versions: [] },
    { id: "e-chart", kind: "evidence", parent: "b-pool", title: l("证据 · 连接等待 P95", "Evidence · conn wait P95"), subtitle: l("Prometheus · 钉自对话", "Prometheus · pinned from chat"), status: "active", x: 680, y: 320, w: 240,
      evidence: { type: "chart", unit: "ms", values: spike, marker: 18, source: "Prometheus", color: "var(--c3)" }, context: { version: "—", tokens: 400, messages: 0, excludes: [] }, versions: [] },
    { id: "e-log", kind: "evidence", parent: "b-pool", title: l("证据 · 超时日志 1,284 条", "Evidence · 1,284 timeout logs"), subtitle: l("Elasticsearch · 模式聚类", "Elasticsearch · clustered"), status: "active", x: 680, y: 490, w: 240,
      evidence: { type: "log", lines: ["acquire connection timeout (50/50)", "order create failed: TimeoutError", "slow request POST /orders 3.2s"], source: "Elasticsearch", color: "var(--c4)" }, context: { version: "—", tokens: 300, messages: 0, excludes: [] }, versions: [] },
    { id: "e-diff", kind: "evidence", parent: "root", title: l("证据 · 配置差异", "Evidence · config diff"), subtitle: "GitLab #8812", status: "active", x: 24, y: 500, w: 240,
      evidence: { type: "diff", minus: "- max: 200", plus: "+ max: 50", source: "GitLab", color: "var(--c6)" }, context: { version: "—", tokens: 200, messages: 0, excludes: [] }, versions: [] },
  ],
  tray: [
    { id: "t-p99", kind: "chart", title: l("P99 延迟图", "P99 latency chart"), source: l("对话 · 14:21", "Chat · 14:21"), body: { type: "chart", unit: "ms", values: series(30, 190, 30, 10, 8, [17, 8]) } },
    { id: "t-pods", kind: "table", title: l("各实例错误率", "Error rate by instance"), source: l("对话 · 14:21", "Chat · 14:21"), body: { type: "table", rows: [["7f9c-4", "9.2%"], ["7f9c-1", "8.9%"], ["7f9c-3", "7.6%"]] } },
    { id: "t-err", kind: "log", title: l("order create failed", "order create failed"), source: "Elasticsearch", body: { type: "log", lines: ["order create failed: TimeoutError", "retrying (1/3)"] } },
    { id: "t-hpa", kind: "chart", title: l("HPA 副本数", "HPA replicas"), source: "Kubernetes", body: { type: "chart", unit: "", values: [6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 8, 8, 8, 8, 8, 8] } },
  ],
};

mock("GET", "/investigations/:id/canvas", ({ params }) => ({
  ...graph,
  investigation: params.id === "INV-231" ? graph.investigation : { ...graph.investigation, id: params.id },
}));
mock("POST", "/investigations/:id/branches", ({ body }) => {
  const b = body as { parent_id: string; title: string; x: number; y: number };
  return { id: `b-${Date.now()}`, kind: "branch", parent: b.parent_id, title: b.title, subtitle: l("独立上下文 v1 · 刚刚派生", "Isolated context v1 · just forked"), status: "running", x: b.x, y: b.y, w: 240, summary: "", context: { version: "v1", inherits: "v4", tokens: 1200, messages: 0, excludes: [] }, versions: [] };
});
mock("PATCH", "/investigations/:id/nodes/:node", ({ body }) => body);
let derived = 0;
mock("POST", "/investigations/:id/nodes/:node/messages", () => ({
  // The first follow-up in a session makes the AI derive one extra branch, to show how new nodes appear.
  new_branch: derived++ === 0 ? { title: l("预发与生产配置差异", "Staging vs prod config diff"), subtitle: l("AI 派生 · 引用预发配置（只读）", "Derived by AI · reads staging config (read-only)"), summary: l("对比 12 项参数：只有 connection_pool.max 不同（预发 200、生产 50）。", "12 parameters compared: only connection_pool.max differs (staging 200, prod 50).") } : undefined,
  summary: l("连接池排队时长 P95 3.1 s，超过 HTTP 超时 3 s，因此返回 5xx；恢复上限后应在 1 分钟内回落。", "Pool queue P95 is 3.1 s, above the 3 s HTTP timeout, hence the 5xx. Restoring the max should recover within a minute."),
}));
mock("GET", "/investigations/:id/writeback-preview", () => ({
  from: { id: "b-cmp", title: l("连接池参数对比", "Pool parameter comparison"), version: "v2" },
  to: { id: "root", title: l("主线 · 异常", "Main line · anomaly"), version: "v6", next_version: "v7", hops: 2 },
  conclusions: [
    { id: "c1", text: l("生产连接池上限被 v2.8.1 改为 50（预发仍为 200）", "Prod pool max was changed to 50 by v2.8.1 (staging still 200)"), evidence: [l("① 配置差异", "① Config diff")], recommended: true },
    { id: "c2", text: l("连接池占满后请求排队超时，与 5xx 上升时间一致", "Once the pool is full, queued requests time out — matching the 5xx rise"), evidence: [l("② 连接等待", "② Conn wait"), l("③ 超时日志", "③ Timeout logs")], recommended: true },
    { id: "c3", text: l("原始对话 12 条（不推荐：约占主线 3.1k tokens）", "Raw conversation, 12 messages (not recommended: ≈3.1k tokens on the main line)"), evidence: [], recommended: false, tokens: 3100 },
  ],
  timeline: [
    { v: "v1", text: l("告警：checkout-api 5xx 8.4%（Alertmanager）", "Alert: checkout-api 5xx 8.4% (Alertmanager)"), meta: l("14:05 · 自动初查", "14:05 · auto-triage") },
    { v: "v2", text: l("AI：与 14:03 发布 v2.8.1 时间吻合，建议分三路排查", "AI: matches v2.8.1 at 14:03; investigate three ways"), meta: l("14:06 · 3 条证据", "14:06 · 3 evidence") },
    { v: "v3", text: l("派生 3 个分支：代码变更 · 数据库连接池 · 上游依赖", "Forked 3 branches: code · DB pool · upstream"), meta: l("14:09 · 王晨", "14:09 · Wang Chen") },
    { v: "v5", text: l("回写：代码变更已排除（diff 不涉及数据访问层）", "Writeback: code change ruled out (no data-layer changes)"), meta: l("14:13 · 来自 代码变更 v2", "14:13 · from Code change v2") },
    { v: "v6", text: l("回写：上游依赖已排除（payment-gateway 错误率正常）", "Writeback: upstream ruled out (payment-gateway healthy)"), meta: l("14:16 · 来自 上游依赖 v1", "14:16 · from Upstream v1") },
    { v: "v7", text: l("回写：生产连接池上限被改为 50；占满后请求排队超时，与 5xx 上升时间一致。[1][2][3]", "Writeback: prod pool max set to 50; saturation causes queue timeouts matching the 5xx rise.[1][2][3]"), meta: l("14:21 · 来自 连接池参数对比 v2 · 待你确认", "14:21 · from Pool comparison v2 · awaiting you"), isNew: true },
  ],
  checks: [
    { tone: "ok", text: l("无冲突：主线还没有关于连接池的结论", "No conflict: the main line has no pool conclusion yet") },
    { tone: "ok", text: l("证据以只读快照复制，来源和时间可追溯", "Evidence is copied as read-only snapshots with source and time") },
    { tone: "warn", text: l("主线的 2 个下游分支会提示“父节点有更新”，由它们自己决定是否同步", "2 downstream branches will see “parent updated” and decide whether to sync") },
    { tone: "ok", text: l("不会修改来源分支，也不会覆盖主线已有内容", "The source branch and existing main-line content are not modified") },
  ],
  budget: { before: 5200, after: 5840, limit: 28000 },
}));
mock("POST", "/investigations/:id/writeback", () => ({ version: "v7" }));
