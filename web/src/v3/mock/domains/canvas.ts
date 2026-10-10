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

// INV-236: the lab bad-release drill (PA-72). The AI read the workload revisions and suggests rolling back to 6;
// an older branch still carries yesterday's suggestion (from revision 6), which is out of date now.
const CHECKOUT = "Y2hlY2tvdXQtYXBpAHByb2Q";
const suggestion = {
  service_id: CHECKOUT,
  workload: "aiops-lab/checkout-api",
  from_revision: "7",
  target_revision: "6",
  target_created_at: at("09:15"),
  target_change_cause: "lab drill bad-release reverted",
  reasons: [
    { text: l("修订 7 上线时间与 5xx 升高吻合", "Revision 7 went out when 5xx rose"), cites: [1] },
    { text: l("两次修订只差 LAB_FAULTS", "The two revisions differ only in LAB_FAULTS"), cites: [3] },
    { text: l("修订 6 运行期间错误率 0.2%，低于 2% 的验证阈值", "Errors were 0.2% on revision 6, under the 2% verification threshold"), cites: [1] },
  ],
  reply_id: "INR-7f31",
  evidence_ids: ["EVD-a1", "EVD-a2", "EVD-a3"],
  suggested_at: at("10:49"),
};
const ctx = (version: string, messages: number) => ({ version, inherits: "v2", tokens: 3200, messages, excludes: [] });
const badRelease = {
  investigation: { id: "INV-236", title: l("checkout-api 修订 7 上线后 5xx", "checkout-api 5xx after revision 7"), status: "active", env: "prod", participants: ["王晨", "AI"] },
  loop: [
    { key: "anomaly", label: l("异常", "Anomaly"), detail: "10:44", state: "done" },
    { key: "evidence", label: l("取证", "Evidence"), detail: l("5 个工具", "5 tools"), state: "done" },
    { key: "proposal", label: l("提案", "Proposal"), detail: "—", state: "active" },
    { key: "approval", label: l("审批", "Approval"), detail: "—", state: "pending" },
    { key: "execution", label: l("执行", "Execute"), detail: "—", state: "pending" },
    { key: "verify", label: l("验证", "Verify"), detail: "—", state: "pending" },
  ],
  nodes: [
    { id: "root", kind: "root", title: l("异常：/checkout 5xx 48%", "Anomaly: /checkout 5xx 48%"), subtitle: l("主线 · 上下文 v2", "Main line · context v2"), status: "active", x: 24, y: 260, w: 240,
      summary: l("10:44 起 /checkout 5xx 比例从 0.2% 升到 48%，10:42 刚上线修订 7。", "/checkout 5xx rose from 0.2% to 48% from 10:44; revision 7 went out at 10:42."), evidence_refs: ["2"], writebacks: { received: 0, sources: [] },
      context: { version: "v2", tokens: 2600, messages: 3, excludes: [] }, versions: [], rollback_suggestion: null },
    { id: "b-release", kind: "branch", parent: "root", title: l("是不是发布引起的？", "Was it the release?"), subtitle: l("独立上下文 v2 · 继承主线 v2", "Isolated context v2 · inherits main v2"), status: "confirmed", x: 340, y: 30, w: 250,
      summary: l("是。修订 7 上线后 /checkout 5xx 从 0.2% 升到 48%[1]；错误日志只出现在修订 7 的 Pod[2]；修订 7 与 6 只差环境变量 LAB_FAULTS[3]。", "Yes. After revision 7, /checkout 5xx went from 0.2% to 48%[1]; the errors only come from revision 7 Pods[2]; revisions 7 and 6 differ only in LAB_FAULTS[3]."),
      tools: l("5 个工具 · 9.4k tokens", "5 tools · 9.4k tokens"), confidence: "high", evidence_refs: [l("① 指标", "① Metrics"), l("② 日志", "② Logs"), l("③ Kubernetes", "③ Kubernetes")], context: ctx("v2", 2), versions: [], rollback_suggestion: suggestion },
    { id: "b-deps", kind: "branch", parent: "root", title: l("下游依赖是否异常", "Are dependencies failing?"), subtitle: l("独立上下文 v1", "Isolated context v1"), status: "ruled_out", x: 340, y: 400, w: 240,
      summary: l("库存与支付的错误率和延迟都正常，已排除。", "Inventory and payments are healthy — ruled out."), context: ctx("v1", 2), versions: [], rollback_suggestion: null },
    { id: "b-yesterday", kind: "branch", parent: "root", title: l("昨天 16:40 的同类告警", "Yesterday's 16:40 alert"), subtitle: l("独立上下文 v1", "Isolated context v1"), status: "confirmed", x: 340, y: 520, w: 250,
      summary: l("昨天修订 6 上线后也短暂出现 5xx，当时建议回到修订 5。", "Revision 6 also caused brief 5xx yesterday; the suggestion then was revision 5."), evidence_refs: [l("① 指标", "① Metrics")], context: ctx("v1", 2), versions: [],
      rollback_suggestion: { ...suggestion, from_revision: "6", target_revision: "5", target_change_cause: "lab drill bad-release: /checkout answers 5xx at ratio 0.2", reasons: [{ text: l("修订 6 上线后 5xx 升高", "5xx rose after revision 6"), cites: [1] }], reply_id: "INR-5c02", evidence_ids: ["EVD-b1"], suggested_at: at("16:41", -1) } },
  ],
  tray: [],
};
mock("GET", "/investigations/:id/canvas", ({ params }) =>
  params.id === "INV-236"
    ? badRelease
    : {
        ...graph,
        investigation: params.id === "INV-231" ? graph.investigation : { ...graph.investigation, id: params.id },
      },
);
// GET …/nodes/:node/replies (PA-75): each branch's persisted AI replies, newest first, cursor-paged.
const MODEL = { id: "deepseek-chat", name: "DeepSeek V3", provider: "deepseek" };
type MockReply = { id: string; text: unknown; author: string | null; answer: unknown; state: string; failure_code: string | null; model: typeof MODEL; reasoning: string | null; created_at: string; completed_at: string | null; evidence_ids: string[]; rollback_suggestion: unknown };
const reply = (id: string, text: unknown, answer: unknown, created: string, extra: Partial<MockReply> = {}): MockReply => ({
  id, text, author: "王晨", answer, state: "completed", failure_code: null, model: MODEL, reasoning: "high", created_at: created, completed_at: created, evidence_ids: [], rollback_suggestion: null, ...extra,
});
const QUESTIONS: Record<string, unknown> = {
  root: l("为什么 checkout-api 的 5xx 升高了？", "Why did checkout-api's 5xx rise?"),
  "b-code": l("v2.8.1 的改动会影响库存调用吗？", "Does v2.8.1 touch inventory calls?"),
  "b-inv": l("调用量为什么翻了三倍？", "Why did calls triple?"),
  "b-pay": l("支付网关有没有异常？", "Is the payment gateway failing?"),
  "b-amp": l("一个下单请求调了几次库存？", "How many inventory calls does one order make?"),
};
function mockReplies(investigation: string, node: string): MockReply[] {
  if (investigation === "INV-236") {
    if (node === "root")
      // An on-call engineer checking the error rate every two minutes: enough replies for "load earlier".
      return Array.from({ length: 22 }, (_, i) => {
        const minutes = 10 * 60 + 46 + i * 2;
        const pct = Math.max(0.2, 48 - i * 2.2).toFixed(1);
        const hhmm = `${Math.floor(minutes / 60)}:${String(minutes % 60).padStart(2, "0")}`;
        return reply(`INR-r${i}`, l("现在 /checkout 的 5xx 比例是多少？", "What's the /checkout 5xx ratio now?"), l(`${hhmm}：最近 5 分钟 5xx 比例 ${pct}%（Prometheus）[1]。`, `${hhmm}: 5xx ratio ${pct}% over the last 5 minutes (Prometheus)[1].`), at(hhmm), { evidence_ids: ["EVD-a1"] });
      }).reverse();
    if (node === "b-release")
      return [
        reply("INR-7f31", l("回滚到哪个修订合适？", "Which revision should we roll back to?"), badRelease.nodes[1].summary, at("10:49"), { evidence_ids: ["EVD-a1", "EVD-a2", "EVD-a3"], rollback_suggestion: suggestion }),
        reply("INR-7f20", l("是不是 10:42 的发布引起的？", "Was it the 10:42 release?"), l("先看了服务概况和错误率：/checkout 5xx 从 10:44 开始升高[1]，错误日志集中在新 Pod[2]。还需要对比修订才能确定。", "From the overview and error ratio: /checkout 5xx rose from 10:44[1], errors cluster on the new Pods[2]. Comparing revisions is still needed."), at("10:47"), { evidence_ids: ["EVD-a1", "EVD-a2"] }),
        reply("INR-7f11", l("是不是 10:42 的发布引起的？", "Was it the 10:42 release?"), null, at("10:45"), { state: "failed", failure_code: "model_unavailable", completed_at: null }),
      ];
    const n = badRelease.nodes.find((x) => x.id === node);
    if (!n) return [];
    return [reply(n.rollback_suggestion?.reply_id ?? `INR-${node}`, n.id === "b-yesterday" ? l("昨天的告警是怎么回事？", "What was yesterday's alert?") : l("下游依赖有没有问题？", "Are dependencies failing?"), n.summary, n.id === "b-yesterday" ? at("16:41", -1) : at("10:48"), { evidence_ids: ["EVD-b1"], rollback_suggestion: n.rollback_suggestion ?? null })];
  }
  const n = graph.nodes.find((x) => x.id === node);
  if (!n || !("summary" in n) || !n.summary) return [];
  return [reply(`INR-${node}`, QUESTIONS[node] ?? l("继续分析这个分支", "Keep analysing this branch"), n.summary, at("14:14"))];
}
mock("GET", "/investigations/:id/nodes/:node/replies", ({ params, query }) => {
  const all = mockReplies(params.id, params.node);
  const size = Math.min(50, Math.max(1, Number(query.page_size ?? 20)));
  const from = Number(query.cursor ?? 0);
  const page = all.slice(from, from + size);
  const next = from + size < all.length ? String(from + size) : null;
  return { investigation_id: params.id, node_id: params.node, replies: page, has_more: next !== null, next_cursor: next };
});
mock("POST", "/investigations/:id/branches", ({ body }) => {
  const b = body as { parent_id: string; title: string; x: number; y: number };
  return { id: `b-${Date.now()}`, kind: "branch", parent: b.parent_id, title: b.title, subtitle: l("独立上下文 v1 · 刚刚派生", "Isolated context v1 · just forked"), status: "running", x: b.x, y: b.y, w: 240, summary: "", context: { version: "v1", inherits: "v4", tokens: 1200, messages: 0, excludes: [] }, versions: [] };
});
mock("PATCH", "/investigations/:id/nodes/:node", ({ body }) => body);
let derived = 0;
mock("POST", "/investigations/:id/nodes/:node/messages", ({ params }) => params.id === "INV-236" ? { summary: badRelease.nodes[1].summary, rollback_suggestion: suggestion } : ({
  rollback_suggestion: null,
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
