import { mock } from "../router";
import { l } from "../../i18n";
import { at } from "../time";

// INV-231 is the retry storm told on the blog (wans.run/redstring): inventory-03's disk slows down
// first (14:02), checkout-api retries each 300 ms timeout three times with no backoff, one order
// makes four inventory calls, inventory traffic grows 3.2x while orders stay flat, and checkout
// returns 5xx. The 14:03 release is a bystander. Example data.
// inventory-03 P99 (ms), one point a minute from 13:48: it turns at 14:01–14:02, before the release (index 15).
const p99 = [80, 78, 82, 79, 81, 80, 83, 79, 80, 82, 81, 80, 79, 96, 214, 318, 344, 351, 348, 352, 350, 349, 353, 351, 350, 352, 348, 351, 350, 349];
// Inventory calls (k/s) over the same minutes: flat at 1.2k, then 3.9k once retries pile up.
const calls = [1.21, 1.18, 1.22, 1.2, 1.19, 1.23, 1.2, 1.21, 1.19, 1.22, 1.2, 1.24, 1.21, 1.2, 1.22, 1.25, 1.42, 2.31, 3.12, 3.63, 3.81, 3.9, 3.85, 3.92, 3.88, 3.95, 3.9, 3.87, 3.93, 3.9];

const graph = {
  investigation: { id: "INV-231", title: l("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), status: "waiting_approval", incident_id: "INC-1042", env: "prod", participants: ["王晨", "李明", "AI"], conversation_id: "C-320" },
  loop: [
    { key: "anomaly", label: l("异常", "Anomaly"), detail: "14:05", state: "done" },
    { key: "evidence", label: l("取证", "Evidence"), detail: l("4 个工具", "4 tools"), state: "done" },
    { key: "proposal", label: l("提案", "Proposal"), detail: l("重试降为 1 次", "Retry once"), state: "done" },
    { key: "approval", label: l("审批", "Approval"), detail: l("等待复核", "Awaiting review"), state: "active", href: "/review/PRP-88" },
    { key: "execution", label: l("执行", "Execute"), detail: "—", state: "pending" },
    { key: "verify", label: l("180 秒验证", "180 s verify"), detail: "—", state: "pending" },
  ],
  nodes: [
    { id: "root", kind: "root", title: l("异常：5xx 8.4%", "Anomaly: 5xx 8.4%"), subtitle: l("主线 · 上下文 v7", "Main line · context v7"), status: "active", x: 24, y: 300, w: 240,
      summary: l("14:05 起 checkout-api 5xx 从 0.2% 升到 8.4%，14:03 刚发布 v2.8.1。", "checkout-api 5xx rose from 0.2% to 8.4% from 14:05; v2.8.1 shipped at 14:03."), evidence_refs: ["4"], writeback: undefined,
      context: { version: "v7", tokens: 5800, messages: 9, excludes: [] },
      versions: [
        { v: "v7", text: l("回写：inventory-03 变慢，被重试放大成风暴", "Writeback: inventory-03 slowed; retries made it a storm"), at: at("14:21") },
        { v: "v6", text: l("回写：支付网关已排除", "Writeback: payments ruled out"), at: at("14:15") },
        { v: "v5", text: l("回写：代码变更已排除", "Writeback: code change ruled out"), at: at("14:13") },
        { v: "v3", text: l("派生 3 个分支", "Forked 3 branches"), at: at("14:09") },
      ] },
    { id: "b-code", kind: "branch", parent: "root", title: l("代码变更 v2.8.1", "Code change v2.8.1"), subtitle: l("独立上下文 v2 · 已回写", "Isolated context v2 · written back"), status: "ruled_out", x: 320, y: 24, w: 230,
      summary: l("diff 没有改动库存调用，已排除。", "The diff does not touch inventory calls — ruled out."), context: { version: "v2", inherits: "v3", tokens: 3100, messages: 12, excludes: [] }, versions: [] },
    { id: "b-inv", kind: "branch", parent: "root", title: l("库存服务调用", "Inventory calls"), subtitle: l("独立上下文 v4 · 继承主线 v3", "Isolated context v4 · inherits main v3"), status: "confirmed", x: 320, y: 112, w: 290,
      summary: l("**inventory-03 先变慢**：P99 80 → 350 ms；随后 4 个实例的调用超时率 0.2% → 31%[1][3]。", "**inventory-03 slowed first**: P99 80 → 350 ms; then timeouts on all 4 instances went 0.2% → 31%[1][3]."),
      confidence: "high", tools: l("4 个工具 · 13k tokens", "4 tools · 13k tokens"), series: p99, evidence_refs: [l("① 指标", "① Metrics"), l("③ 日志", "③ Logs")],
      context: { version: "v4", inherits: "v3", tokens: 5400, messages: 6, excludes: [l("代码变更", "Code change"), l("支付网关", "Payments")] },
      versions: [
        { v: "v4", text: l("AI 回答 · 引用 2 条证据", "AI answer · 2 citations"), at: at("14:14") },
        { v: "v3", text: l("你：调用量为什么翻了三倍？", "You: why did calls triple?"), at: at("14:12") },
        { v: "v2", text: l("AI 回答 · 调用 4 个工具", "AI answer · 4 tool calls"), at: at("14:11") },
        { v: "v1", text: l("从主线 v3 派生", "Forked from main v3"), at: at("14:09") },
      ] },
    { id: "b-pay", kind: "branch", parent: "root", title: l("支付网关", "Payments"), subtitle: l("独立上下文 v1 · 已回写", "Isolated context v1 · written back"), status: "ruled_out", x: 320, y: 470, w: 230,
      summary: l("payment-gateway 错误率正常，已排除。", "payment-gateway error rate is normal — ruled out."), context: { version: "v1", inherits: "v3", tokens: 2100, messages: 4, excludes: [] }, versions: [] },
    { id: "b-amp", kind: "branch", parent: "b-inv", title: l("调用量为什么翻三倍", "Why did calls triple?"), subtitle: l("派生自 库存服务调用 v3", "Forked from Inventory calls v3"), status: "confirmed", x: 680, y: 24, w: 260,
      summary: l("下单量持平，**每个请求调了 4 次库存**；68% 的调用是重试[2]。", "Orders are flat, yet **each order calls inventory 4 times**; 68% of calls are retries[2]."), writeback: { to: "root", version: "v7" },
      context: { version: "v2", inherits: "v3", tokens: 3100, messages: 4, excludes: [] }, versions: [] },
    { id: "e-trace", kind: "evidence", parent: "b-amp", title: l("证据 · 一个下单请求", "Evidence · one order"), subtitle: "Tempo · trace 4f2a9c", status: "active", x: 680, y: 204, w: 260,
      evidence: { type: "log", lines: ["POST /orders · 1.2 s · 502", "inventory.reserve ×4", "3 × timeout 300ms · 1 failed"], source: "Tempo", color: "var(--c2)" }, context: { version: "—", tokens: 300, messages: 0, excludes: [] }, versions: [] },
    { id: "e-chart", kind: "evidence", parent: "b-inv", title: l("证据 · 谁先变慢", "Evidence · who slowed first"), subtitle: l("Prometheus · inventory-03 P99", "Prometheus · inventory-03 P99"), status: "active", x: 680, y: 356, w: 260,
      evidence: { type: "chart", unit: "ms", values: p99, marker: 15, source: "Prometheus", color: "var(--c3)" }, context: { version: "—", tokens: 400, messages: 0, excludes: [] }, versions: [] },
    { id: "e-log", kind: "evidence", parent: "b-inv", title: l("证据 · 重试日志 2,416 条", "Evidence · 2,416 retry logs"), subtitle: l("Elasticsearch · 模式聚类", "Elasticsearch · clustered"), status: "active", x: 680, y: 522, w: 260,
      evidence: { type: "log", lines: ["timeout 300ms · retry 1/3", "retry 3/3 failed", "POST /orders 502 · 1.2s"], source: "Elasticsearch", color: "var(--c4)" }, context: { version: "—", tokens: 300, messages: 0, excludes: [] }, versions: [] },
    { id: "e-cfg", kind: "evidence", parent: "root", title: l("证据 · 重试配置", "Evidence · retry setting"), subtitle: l("GitLab · v2.6（3 月）以来未改动", "GitLab · unchanged since v2.6 (March)"), status: "active", x: 24, y: 500, w: 240,
      evidence: { type: "table", rows: [["inventory.timeout", "300ms"], ["retry.max_attempts", "3"], ["retry.backoff", "none"]], source: "GitLab", color: "var(--c6)" }, context: { version: "—", tokens: 200, messages: 0, excludes: [] }, versions: [] },
  ],
  tray: [
    { id: "t-p99", kind: "chart", title: l("inventory-03 P99", "inventory-03 P99"), source: l("对话 · 14:21", "Chat · 14:21"), body: { type: "chart", unit: "ms", values: p99 } },
    { id: "t-pods", kind: "table", title: l("各实例调用超时率", "Timeouts by instance"), source: l("对话 · 14:21", "Chat · 14:21"), body: { type: "table", rows: [["inventory-03", "46%"], ["inventory-01", "27%"], ["inventory-04", "26%"]] } },
    { id: "t-err", kind: "log", title: l("inventory.reserve timeout", "inventory.reserve timeout"), source: "Elasticsearch", body: { type: "log", lines: ["timeout 300ms · retry 1/3", "retry 3/3 failed"] } },
    { id: "t-calls", kind: "chart", title: l("库存调用量", "Inventory calls"), source: "Prometheus", body: { type: "chart", unit: "k/s", values: calls } },
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
  new_branch: derived++ === 0 ? { title: l("四个库存实例对比", "The four inventory instances"), subtitle: l("AI 派生 · 读取节点指标（只读）", "Derived by AI · reads node metrics (read-only)"), summary: l("只有 inventory-03 的磁盘 IO 等待升高（2 → 180 ms）；另外 3 个实例的超时来自被放大的重试流量。", "Only inventory-03's disk IO wait rose (2 → 180 ms); the other 3 instances time out under the amplified retry traffic.") } : undefined,
  summary: l("重试占库存调用的 68%：每次超时后立即重试 3 次、没有退避。重试降为 1 次并打开熔断后，调用量应在 1 分钟内回到 1.3k/s 以下。", "Retries are 68% of inventory calls: each timeout is retried 3 times at once, with no backoff. Retrying once with the circuit breaker on should bring calls under 1.3k/s within a minute."),
}));
mock("GET", "/investigations/:id/writeback-preview", () => ({
  from: { id: "b-amp", title: l("调用量为什么翻三倍", "Why did calls triple?"), version: "v2" },
  to: { id: "root", title: l("主线 · 异常", "Main line · anomaly"), version: "v6", next_version: "v7", hops: 2 },
  conclusions: [
    { id: "c1", text: l("inventory-03 先变慢：P99 80 → 350 ms，磁盘 IO 等待同步上涨", "inventory-03 slowed first: P99 80 → 350 ms, with disk IO wait rising alongside"), evidence: [l("① 指标", "① Metrics")], recommended: true },
    { id: "c2", text: l("超时后立即重试 3 次，一个下单请求调 4 次库存，调用放大到 3.2 倍", "Timeouts are retried 3 times at once: one order makes 4 inventory calls, 3.2× the traffic"), evidence: [l("② 链路", "② Trace"), l("③ 日志", "③ Logs"), l("④ 配置", "④ Config")], recommended: true },
    { id: "c3", text: l("原始对话 12 条（不推荐：约占主线 3.1k tokens）", "Raw conversation, 12 messages (not recommended: ≈3.1k tokens on the main line)"), evidence: [], recommended: false, tokens: 3100 },
  ],
  timeline: [
    { v: "v1", text: l("告警：checkout-api 5xx 8.4%（Alertmanager）", "Alert: checkout-api 5xx 8.4% (Alertmanager)"), meta: l("14:05 · 自动初查", "14:05 · auto-triage") },
    { v: "v2", text: l("AI：与 14:03 发布 v2.8.1 时间接近，建议分三路排查", "AI: close to v2.8.1 at 14:03; investigate three ways"), meta: l("14:06 · 3 条证据", "14:06 · 3 evidence") },
    { v: "v3", text: l("派生 3 个分支：代码变更 · 库存服务调用 · 支付网关", "Forked 3 branches: code · inventory calls · payments"), meta: l("14:09 · 王晨", "14:09 · Wang Chen") },
    { v: "v5", text: l("回写：代码变更已排除（diff 没有改动库存调用）", "Writeback: code change ruled out (no inventory calls in the diff)"), meta: l("14:13 · 来自 代码变更 v2", "14:13 · from Code change v2") },
    { v: "v6", text: l("回写：支付网关已排除（payment-gateway 错误率正常）", "Writeback: payments ruled out (payment-gateway healthy)"), meta: l("14:15 · 来自 支付网关 v1", "14:15 · from Payments v1") },
    { v: "v7", text: l("回写：inventory-03 先变慢；超时后立即重试 3 次，调用放大到 3.2 倍，压满整个库存服务。[1][2][3][4]", "Writeback: inventory-03 slowed first; timeouts retried 3 times at once amplified calls 3.2× and saturated the inventory service.[1][2][3][4]"), meta: l("14:21 · 来自 调用量为什么翻三倍 v2 · 待你确认", "14:21 · from Why did calls triple? v2 · awaiting you"), isNew: true },
  ],
  checks: [
    { tone: "ok", text: l("无冲突：主线还没有关于库存服务的结论", "No conflict: the main line has no inventory finding yet") },
    { tone: "ok", text: l("证据以只读快照复制，来源和时间可追溯", "Evidence is copied as read-only snapshots with source and time") },
    { tone: "warn", text: l("主线的 2 个下游分支会提示“父节点有更新”，由它们自己决定是否同步", "2 downstream branches will see “parent updated” and decide whether to sync") },
    { tone: "ok", text: l("不会修改来源分支，也不会覆盖主线已有内容", "The source branch and existing main-line content are not modified") },
  ],
  budget: { before: 5200, after: 5840, limit: 28000 },
}));
mock("POST", "/investigations/:id/writeback", () => ({ version: "v7" }));
