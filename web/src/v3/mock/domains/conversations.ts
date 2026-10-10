import { mock, MockError, paginate } from "../router";
import { l } from "../../i18n";
import { series } from "../../charts";
import { ago, at } from "../time";

const LIST = [
  { id: "INV-231", title: l("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), mode: "canvas", status: "waiting_approval", source: "alert_triage", scope: l("INC-1042 · checkout-api · 生产", "INC-1042 · checkout-api · prod"), incident_id: "INC-1042", participants: ["王晨", "李明", "AI"], size: l("4 个分支", "4 branches"), updated_at: ago(3), tokens: 48000, team: true, mine: true },
  { id: "C-311", title: l("checkout-api 大促扩容评估", "checkout-api capacity for the sale"), mode: "chat", status: "active", source: "manual", scope: l("checkout-api · 生产", "checkout-api · prod"), participants: ["王晨", "AI"], size: l("6 轮", "6 turns"), updated_at: ago(12), tokens: 12000, team: true, mine: true },
  { id: "C-330", title: l("checkout-api 修订 7 上线后 5xx", "checkout-api 5xx after revision 7"), mode: "chat", status: "active", source: "manual", scope: l("INV-236 · checkout-api · 生产", "INV-236 · checkout-api · prod"), participants: ["王晨", "AI"], size: l("1 条建议", "1 suggestion"), updated_at: ago(3), tokens: 6400, team: true, mine: true },
  { id: "C-320", title: l("checkout-api 故障复盘", "checkout-api incident review"), mode: "chat", status: "active", source: "manual", scope: l("INC-1042 · checkout-api · 生产", "INC-1042 · checkout-api · prod"), incident_id: "INC-1042", participants: ["王晨", "AI"], size: l("4 个产物", "4 artifacts"), updated_at: ago(5), tokens: 21000, team: true, mine: true },
  { id: "INV-229", title: l("payment-gateway P99 升高排查", "payment-gateway P99 regression"), mode: "canvas", status: "active", source: "manual", scope: l("INC-1041 · payment-gateway · 生产", "INC-1041 · payment-gateway · prod"), incident_id: "INC-1041", participants: ["赵凯", "AI"], size: l("2 个分支", "2 branches"), updated_at: ago(26), tokens: 21000, team: true },
  { id: "C-305", title: l("inventory-worker Kafka 积压", "inventory-worker Kafka lag"), mode: "chat", status: "active", source: "im", scope: l("inventory-worker · 生产", "inventory-worker · prod"), participants: ["王晨", "AI"], size: l("3 轮", "3 turns"), updated_at: ago(60), tokens: 6000, team: true, mine: true },
  { id: "C-302", title: l("生产集群证书到期检查", "Prod cluster certificate expiry check"), mode: "chat", status: "resolved", source: "schedule", scope: l("prod-sh-1 · 生产", "prod-sh-1 · prod"), participants: ["AI"], size: l("1 轮", "1 turn"), updated_at: at("09:00"), tokens: 2000, team: true },
  { id: "INV-224", title: l("order-service 慢查询", "order-service slow queries"), mode: "canvas", status: "resolved", source: "manual", scope: l("INC-1037 · order-service · 生产", "INC-1037 · order-service · prod"), incident_id: "INC-1037", participants: ["周航", "AI"], size: l("3 个分支", "3 branches"), updated_at: at("16:20", -1), tokens: 35000, team: true },
  { id: "C-298", title: l("Harbor 镜像保留策略", "Harbor image retention policy"), mode: "chat", status: "resolved", source: "manual", scope: l("平台 · 全部环境", "Platform · all envs"), participants: ["王晨", "AI"], size: l("5 轮", "5 turns"), updated_at: at("11:05", -1), tokens: 9000, team: true, mine: true },
  { id: "INV-219", title: l("search-api 发布回滚复盘", "search-api rollback review"), mode: "canvas", status: "archived", source: "manual", scope: l("INC-1033 · search-api · 生产", "INC-1033 · search-api · prod"), incident_id: "INC-1033", participants: ["张磊", "李明", "AI"], size: l("5 个分支", "5 branches"), updated_at: at("10:00", -6), tokens: 64000 },
  { id: "C-290", title: l("如何给 user-center 配置 SLO", "How to set SLOs for user-center"), mode: "chat", status: "resolved", source: "manual", scope: l("user-center · 预发", "user-center · staging"), participants: ["陈雨", "AI"], size: l("4 轮", "4 turns"), updated_at: at("15:40", -6), tokens: 7000, team: true },
  { id: "C-287", title: l("payment-gateway 告警风暴初查", "payment-gateway alert storm triage"), mode: "chat", status: "resolved", source: "alert_triage", scope: l("payment-gateway · 生产", "payment-gateway · prod"), participants: ["AI"], size: l("1 轮", "1 turn"), updated_at: at("22:10", -7), tokens: 4000, team: true },
];

mock("GET", "/conversations", ({ query }) => {
  const tab = String(query.tab ?? "mine");
  const q = String(query.q ?? "").toLowerCase();
  let items = LIST.filter((c) =>
    tab === "mine" ? c.mine && c.status !== "archived" : tab === "team" ? c.team && c.status !== "archived" : tab === "triage" ? c.source === "alert_triage" : c.status === "archived",
  );
  if (query.mode) items = items.filter((c) => c.mode === query.mode);
  if (query.status) items = items.filter((c) => c.status === query.status);
  if (q) items = items.filter((c) => `${c.title.zh} ${c.title.en} ${c.scope.zh} ${c.id}`.toLowerCase().includes(q));
  return {
    ...paginate(items, query),
    counts: { mine: LIST.filter((c) => c.mine && c.status !== "archived").length, team: 31, triage: 23, archived: 12 },
    stats: [
      { id: "active", label: l("进行中", "Active"), value: "6", sub: l("其中画布 2 个", "2 on canvas"), series: [3, 4, 4, 5, 6, 5, 6] },
      { id: "waiting", label: l("等待审批", "Awaiting approval"), value: "2", tone: "warn", sub: l("最早一条已等待 13 分钟", "Oldest waiting 13 min"), series: [0, 1, 1, 2, 1, 2, 2] },
      { id: "resolved", label: l("本周已解决", "Resolved this week"), value: "14", tone: "ok", sub: l("平均解决 38 分钟", "Avg 38 min to resolve"), series: [1, 2, 3, 2, 4, 1, 1] },
      { id: "triage", label: l("本周自动初查", "Auto-triage this week"), value: "23", sub: l("其中 17 条给出根因候选", "17 with a root-cause candidate"), series: [2, 5, 3, 4, 6, 1, 2] },
    ],
  };
});

/* ---------- C-311: capacity assessment (chat mode board) ---------- */
const p99 = Array.from({ length: 42 }, (_, i) => Math.round(200 + 92 * (0.5 + 0.5 * Math.sin((2 * Math.PI * i) / 6 - Math.PI / 2)) + (((i * 37) % 11) - 5) * 2.2));
p99[33] = 338;
p99[41] = 312;
const days = Array.from({ length: 42 }, (_, i) => `9/${17 + Math.floor(i / 6)} ${String((i % 6) * 4).padStart(2, "0")}:00`);

const capacity = {
  id: "C-311",
  title: l("checkout-api 大促扩容评估", "checkout-api capacity for the sale"),
  mode: "chat",
  status: "active",
  scope: { service: "checkout-api", env: "prod", inferred: true },
  messages: [
    { id: "m1", role: "user", author: "王晨", created_at: at("14:31"), parts: [{ type: "text", markdown: l("checkout-api 现在的 P99 是多少？这周六大促要不要扩容？", "What is checkout-api's P99 now? Should we scale out for Saturday's sale?") }] },
    {
      id: "m2",
      role: "assistant",
      author: "AI",
      created_at: at("14:31"),
      run: { id: "run_7f40", agent: l("调查助手", "Investigator"), version: "v12", model: l("默认模型", "Default model"), tokens: 6200, duration_s: 9.8 },
      parts: [
        {
          type: "tool_calls",
          summary: l("调用 4 个工具", "4 tool calls"),
          tokens: 6200,
          duration_s: 9.8,
          calls: [
            { id: "t1", connector: "prometheus", tool: "query_metrics", args: "p99_latency · 7d", duration_ms: 420, status: "ok" },
            { id: "t2", connector: "prometheus", tool: "query_metrics", args: "cpu_usage · 7d", duration_ms: 380, status: "ok" },
            { id: "t3", connector: "confluence", tool: "search_runbooks", args: l("大促容量评估", "sale capacity"), duration_ms: 210, status: "ok" },
            { id: "t4", connector: "grafana", tool: "get_annotations", args: "618", duration_ms: 160, status: "ok" },
          ],
        },
        {
          type: "text",
          markdown: l(
            "当前 P99 **312 ms**（SLO 500 ms），近 7 天峰值 338 ms；CPU 均值 46%、峰值 71%，共 6 个副本[1]。按上次 618 大促的峰值倍率（约 2.5 倍）估算[2]，周六峰值会超出当前容量[3]。",
            "Current P99 is **312 ms** (SLO 500 ms); the 7-day peak was 338 ms. CPU averages 46% and peaks at 71% across 6 replicas[1]. Using the 618 sale's peak multiplier (≈2.5×)[2], Saturday's peak would exceed current capacity[3].",
          ),
        },
        { type: "chart", title: l("P99 延迟 · 近 7 天", "P99 latency · 7 days"), unit: "ms", labels: days, series: [{ name: "P99", values: p99 }], threshold: 500, source: "Prometheus", deep_link: "https://grafana.example.internal/d/checkout" },
        {
          type: "table",
          title: l("容量估算", "Capacity estimate"),
          columns: [l("指标", "Metric"), l("当前峰值", "Current peak"), l("大促预估 ×2.5", "Sale estimate ×2.5"), l("结论", "Verdict")],
          rows: [
            ["QPS", "1.2k", "3.0k", "—"],
            [l("单 Pod CPU", "CPU per pod"), "71%", l("约 178%", "≈178%"), l("需扩容", "Scale out")],
            [l("副本数", "Replicas"), "6", l("建议 14", "Suggest 14"), l("HPA 上限 20", "HPA max 20")],
          ],
        },
        {
          type: "proposal",
          id: "PRP-90",
          title: l("周五 20:00 前把副本扩到 14，HPA 上限调到 20", "Scale to 14 replicas and raise HPA max to 20 before Fri 20:00"),
          risk: "low",
          reversible: true,
          target: "production / checkout-api",
          detail: l("扩容是写操作：生成提案后需人工批准，由独立执行器执行，AI 不直接改动集群。", "Scaling is a write: it becomes a proposal, a person approves it, and an independent executor runs it."),
        },
        {
          type: "citations",
          items: [
            { n: 1, label: l("Prometheus · p99 / cpu 7 天", "Prometheus · p99 / cpu 7d") },
            { n: 2, label: l("Runbook「大促容量评估」v3", "Runbook “Sale capacity” v3"), ref: { kind: "runbook", id: "rb-capacity", label: "rb" } },
            { n: 3, label: l("618 大促复盘 · 2026-06-18", "618 sale postmortem · 2026-06-18") },
          ],
        },
      ],
    },
    { id: "m3", role: "user", author: "王晨", created_at: at("14:32"), parts: [{ type: "text", markdown: l("2.5 倍是怎么估出来的？", "How did you get 2.5×?") }] },
    {
      id: "m4",
      role: "assistant",
      author: "AI",
      created_at: at("14:32"),
      run: { id: "run_7f41", agent: l("调查助手", "Investigator"), version: "v12", model: l("默认模型", "Default model"), tokens: 2100, duration_s: 4.1 },
      parts: [
        { type: "tool_calls", summary: l("调用 1 个工具", "1 tool call"), tokens: 2100, duration_s: 4.1, calls: [{ id: "t5", connector: "prometheus", tool: "query_metrics", args: "qps · 2026-06-18", duration_ms: 510, status: "ok" }] },
        {
          type: "text",
          markdown: l(
            "618 当天的峰值 QPS 是平日峰值的 **2.46 倍**，持续约 40 分钟；今年大促的活动力度与 618 相近，所以取 2.5 倍作为估算上限。若活动页流量占比更高，建议再预留 20% 余量。",
            "On 618 the peak QPS was **2.46×** the normal daily peak and lasted about 40 minutes. This sale has a similar campaign size, so 2.5× is the upper estimate. If landing-page traffic is heavier, keep another 20% headroom.",
          ),
        },
        { type: "next_steps", items: [
          { icon: "shield", label: l("生成扩容提案", "Create scaling proposal"), action: "proposal" },
          { icon: "branch", label: l("转为画布", "Open as canvas"), action: "canvas" },
          { icon: "book", label: l("保存为 Runbook 草稿", "Save as runbook draft"), action: "runbook" },
        ] },
      ],
    },
  ],
  artifacts: [],
  side: {
    connectors: [
      { name: l("Prometheus · 生产", "Prometheus · prod"), calls: 3 },
      { name: "Grafana", calls: 1 },
      { name: l("Runbook 知识库", "Runbook library"), calls: 1 },
    ],
    context: { used: 6200, budget: 32000, parts: [
      { label: l("系统", "System"), tokens: 900, color: "var(--faint)" },
      { label: "Skills", tokens: 1300, color: "var(--c4)" },
      { label: l("证据", "Evidence"), tokens: 2400, color: "var(--c1)" },
      { label: l("历史", "History"), tokens: 1600, color: "var(--c2)" },
    ] },
    recalled: [
      { kind: "runbook", title: l("Runbook「大促容量评估」v3", "Runbook “Sale capacity” v3"), score: 0.81 },
      { kind: "postmortem", title: l("618 大促复盘", "618 sale postmortem"), score: 0.77 },
    ],
  },
};

/* ---------- C-320: incident review with artifacts + full parts catalogue ---------- */
// INV-231 (the retry storm on wans.run/redstring), one point every 1.7 min from 13:35: inventory calls
// (k/s) climb from 14:04 while orders stay flat, and fall back once retries are limited at 14:19.
const calls = [1.21, 1.18, 1.22, 1.2, 1.19, 1.23, 1.2, 1.21, 1.19, 1.22, 1.2, 1.24, 1.21, 1.2, 1.22, 1.25, 1.24, 1.42, 2.31, 3.12, 3.63, 3.81, 3.9, 3.85, 3.92, 3.88, 3.95, 2.04, 1.26, 1.21, 1.19, 1.22, 1.2, 1.21, 1.18, 1.2];
const orders = calls.map((_, i) => Math.round((1.2 + (((i * 7) % 5) - 2) * 0.01) * 100) / 100);
const err = [0.2, 0.3, 0.2, 0.3, 0.2, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 0.2, 0.2, 0.3, 0.2, 0.3, 2.1, 5.6, 7.9, 8.4, 8.1, 8.6, 8.3, 8.4, 8.2, 1.9, 0.6, 0.3, 0.22, 0.21, 0.2, 0.23, 0.21, 0.2];
const labels36 = Array.from({ length: 36 }, (_, i) => {
  const m = 35 + Math.round(i * 1.7);
  return `${13 + Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}`;
});
const review = {
  id: "C-320",
  title: l("checkout-api 故障复盘", "checkout-api incident review"),
  mode: "chat",
  status: "active",
  scope: { service: "checkout-api", env: "prod", inferred: false },
  investigation_id: "INV-231",
  messages: [
    { id: "r1", role: "user", author: "王晨", created_at: at("14:21"), parts: [{ type: "text", markdown: l("checkout-api 为什么 5xx 升高了？", "Why did checkout-api's 5xx go up?") }] },
    {
      id: "r2",
      role: "assistant",
      author: "AI",
      created_at: at("14:21"),
      run: { id: "run_7f3a", agent: l("调查助手", "Investigator"), version: "v12", model: l("默认模型", "Default model"), tokens: 11200, duration_s: 9.8 },
      parts: [
        { type: "tool_calls", summary: l("调用 4 个工具", "4 tool calls"), tokens: 11200, duration_s: 9.8, calls: [
          { id: "a", connector: "prometheus", tool: "query_metrics", args: "p99 by instance · node_disk · 1h", duration_ms: 400, status: "ok" },
          { id: "b", connector: "tempo", tool: "search_traces", args: "POST /orders · error · 30m", duration_ms: 500, status: "ok" },
          { id: "c", connector: "elasticsearch", tool: "search_logs", args: '"retry" inventory.reserve · 30m', duration_ms: 700, status: "ok" },
          { id: "d", connector: "gitlab", tool: "get_file_history", args: "checkout-api.yaml · inventory.retry", duration_ms: 300, status: "ok" },
        ] },
        { type: "tasks", title: l("调查计划", "Investigation plan"), items: [
          { label: l("读取告警上下文", "Read alert context"), state: "done", meta: "0.2 s" },
          { label: l("查询最近 2 小时变更", "Query changes in the last 2 hours"), state: "done", meta: "0.3 s" },
          { label: l("查询库存服务各实例的指标", "Query metrics for each inventory instance"), state: "done", meta: "0.4 s" },
          { label: l("抽取一条失败请求的链路", "Pull one failed request's trace"), state: "done", meta: "0.5 s" },
          { label: l("生成结论与提案", "Conclude and propose"), state: "done", meta: "1.1 s" },
        ] },
        { type: "text", markdown: l(
          "**结论：一台库存实例变慢，被重试放大成风暴。** 14:02 inventory-03 先变慢，P99 从 80 ms 升到 350 ms，磁盘 IO 等待同步上涨[1]：\n\n- 一个下单请求调了 4 次库存：3 次超时、1 次失败[2]\n- 日志里全是超时后立即重试，共 2,416 条[3]\n- 重试配置自 v2.6 没改过：300 ms、3 次、无退避[4]\n\n14:03 的发布没有改动库存调用，时间接近只是巧合。",
          "**Conclusion: one slow inventory instance, amplified by retries into a storm.** At 14:02 inventory-03 slowed first: P99 went from 80 ms to 350 ms as its disk IO wait rose[1]:\n\n- One order called inventory 4 times: 3 timeouts and 1 failure[2]\n- The logs are all timeouts retried at once — 2,416 of them[3]\n- The retry setting has not changed since v2.6: 300 ms, 3 retries, no backoff[4]\n\nThe 14:03 release does not touch inventory calls; the timing is a coincidence.",
        ) },
        { type: "chart", title: l("库存调用量与下单量 · checkout-api", "Inventory calls vs orders · checkout-api"), unit: "k/s", labels: labels36, series: [{ name: l("库存调用", "Inventory calls"), values: calls }, { name: l("下单", "Orders"), values: orders }], markers: [{ at: 17, label: l("v2.8.1 发布", "v2.8.1 release"), tone: "warn" }], source: "Prometheus", deep_link: "https://grafana.example.internal" },
        { type: "table", title: l("库存各实例", "Inventory instances"), filter: l("超时率 > 1%", "Timeouts > 1%"), columns: [l("实例", "Instance"), l("超时率", "Timeouts"), "P99", l("磁盘 IO 等待", "Disk IO wait")], rows: [
          ["inventory-03", "46%", "350 ms", "180 ms"],
          ["inventory-01", "27%", "310 ms", "3 ms"],
          ["inventory-04", "26%", "305 ms", "2 ms"],
          ["inventory-02", "25%", "300 ms", "2 ms"],
        ] },
        { type: "diff", path: "config/checkout-api.yaml", from: "v2.8.1", to: "PRP-88", left: ["inventory:", "  timeout: 300ms", "  retry:", "-   max_attempts: 3", "    backoff: none", "-  circuit_breaker: off"], right: ["inventory:", "  timeout: 300ms", "  retry:", "+   max_attempts: 1", "    backoff: none", "+  circuit_breaker: on"] },
        { type: "image", title: l("调用关系示意", "Call graph sketch"), caption: l("AI 生成 · 1280 × 360 · 84 KB", "AI generated · 1280 × 360 · 84 KB"), alt: l("gateway 调用 checkout-api，再调用 payment-gateway 与 inventory-service", "gateway calls checkout-api, which calls payment-gateway and inventory-service"), diagram: "call_graph" },
        { type: "topology", caption: l("gateway → checkout-api → inventory-service · 红色为异常链路 · 连线速度代表流量", "gateway → checkout-api → inventory-service · red is unhealthy · dash speed shows traffic"), href: "/resources" },
        { type: "files", files: [
          { id: "f1", name: l("故障报告.md", "incident-report.md"), kind: "md", size: "6 KB · v3", preview: l("# checkout-api 5xx 上升\n## 概要\ninventory-03 先变慢，超时后立即重试 3 次，把库存调用放大到 3.2 倍…", "# checkout-api 5xx spike\n## Summary\ninventory-03 slowed first; timeouts retried 3 times at once amplified inventory calls 3.2×…") },
          { id: "f2", name: "inventory-instances.csv", kind: "csv", size: l("4 行", "4 rows"), preview: "instance,timeouts,p99,disk_io_wait\ninventory-03,46%,350ms,180ms\ninventory-01,27%,310ms,3ms\ninventory-04,26%,305ms,2ms" },
          { id: "f3", name: "checkout-api.yaml", kind: "yaml", size: "1.2 KB", preview: "inventory:\n  timeout: 300ms\n  retry:\n    max_attempts: 3\n    backoff: none" },
          { id: "f4", name: "retry.log", kind: "log", size: l("2,416 行", "2,416 lines"), preview: "14:12:58 ERROR inventory.reserve timeout, retrying (1/3)\n14:12:57 ERROR retry 3/3 failed: inventory.reserve\n14:12:57 ERROR inventory.reserve timeout, retrying (2/3)" },
        ] },
        { type: "proposal", id: "PRP-88", title: l("库存调用重试降为 1 次，并打开熔断", "Retry inventory calls once and turn the breaker on"), risk: "medium", reversible: true, target: "production / checkout-api", detail: l("配置热更新约 1 分钟 · 执行后自动观测 180 秒", "Hot config update ≈1 min · 180 s automatic verification afterwards") },
        { type: "form", title: l("继续之前，请确认：", "Before continuing, please confirm:"), fields: [
          { id: "env", label: l("环境", "Environment"), kind: "radio", options: [l("生产", "Production"), l("预发", "Staging")], value: l("生产", "Production") },
          { id: "window", label: l("时间窗口", "Time window"), kind: "select", options: ["14:00 – 14:35", "13:30 – 14:35"], value: "14:00 – 14:35" },
          { id: "downstream", label: l("同时检查下游 payment-gateway", "Also check downstream payment-gateway"), kind: "check", value: true },
        ] },
        { type: "citations", items: [
          { n: 1, label: l("Prometheus · inventory-03 P99 与磁盘", "Prometheus · inventory-03 P99 and disk") },
          { n: 2, label: l("Tempo · trace 4f2a9c", "Tempo · trace 4f2a9c") },
          { n: 3, label: l("Elasticsearch · 重试日志", "Elasticsearch · retry logs") },
          { n: 4, label: l("GitLab · 重试配置历史", "GitLab · retry setting history") },
        ] },
        { type: "data_gap", text: l("缺少 inventory-03 的磁盘 SMART 数据（主机监控未接入），磁盘为什么变慢的置信度：中", "No SMART data for inventory-03's disk (host monitoring not connected) — confidence in why the disk slowed: medium"), href: "/integrations/coverage" },
        { type: "next_steps", items: [
          { icon: "shield", label: l("生成变更提案", "Create change proposal"), action: "proposal" },
          { icon: "branch", label: l("派生分支验证", "Fork a branch to verify"), action: "canvas" },
          { icon: "search", label: l("查看 inventory-03 的磁盘", "Check inventory-03's disk"), action: "ask" },
          { icon: "file", label: l("加入报告", "Add to report"), action: "report" },
        ] },
      ],
    },
    { id: "r3", role: "user", author: "王晨", created_at: at("14:24"), parts: [{ type: "text", markdown: l("把这次故障整理成报告，附上关键图表和受影响实例清单。", "Turn this incident into a report with the key charts and the list of affected instances.") }] },
    {
      id: "r4",
      role: "assistant",
      author: "AI",
      created_at: at("14:26"),
      run: { id: "run_7f52", agent: l("报告撰写", "Report writer"), version: "v7", model: l("默认模型", "Default model"), tokens: 8900, duration_s: 70 },
      parts: [
        { type: "text", markdown: l("已生成 4 个产物，点击可在右侧查看、编辑或钉到 Canvas：", "I produced 4 artifacts. Open any of them on the right to view, edit, or pin to the canvas:") },
        { type: "artifacts", ids: ["report", "chart", "csv", "diff"] },
        { type: "status", text: l("故障报告.md 正在按你的批注修改", "incident-report.md is being revised per your note") },
        { type: "next_steps", items: [
          { icon: "send", label: l("推送到飞书", "Share to Feishu"), action: "share" },
          { icon: "check", label: l("确认并发布报告", "Confirm and publish"), action: "publish" },
          { icon: "book", label: l("更新 Runbook", "Update runbook"), action: "runbook" },
        ] },
      ],
    },
  ],
  artifacts: [
    {
      id: "report",
      kind: "report",
      title: l("故障报告.md", "incident-report.md"),
      meta: l("v3 · 正在按你的批注修改", "v3 · revising per your note"),
      version: 3,
      updated_at: at("14:26"),
      content: {
        kind: "report",
        markdown: l(
          "# checkout-api 5xx 上升故障报告\n\n## 概要\ninventory-03 磁盘变慢，让一部分库存调用超过 300 ms[1]；checkout-api 超时后立即重试 3 次，一个下单请求调 4 次库存，调用放大到 3.2 倍，整个库存服务被压满，下单返回 5xx[2][3]。重试降为 1 次并打开熔断后 3 分钟内恢复。\n\n## 影响\n5xx 峰值 8.4%，影响下单接口；受影响订单数以业务侧统计为准。\n\n## 根因\n重试配置自 v2.6 起就是超时 300 ms、重试 3 次、无退避[4]，一台实例变慢就会被放大成整个服务的风暴。14:03 的发布与本次故障无关。\n\n## 改进项\n- 库存调用改为指数退避，重试预算不超过 10%（张磊 · 9/27）\n- 库存服务开启离群实例摘除（李明）",
          "# checkout-api 5xx spike — incident report\n\n## Summary\ninventory-03's disk slowed down and pushed some inventory calls past 300 ms[1]; checkout-api retried each timeout three times at once, so one order made 4 inventory calls, traffic grew 3.2×, the inventory service saturated and checkout returned 5xx[2][3]. Retrying once with the circuit breaker on recovered it within 3 minutes.\n\n## Impact\n5xx peaked at 8.4%, affecting order creation; affected order counts come from the business side.\n\n## Root cause\nSince v2.6 the retry setting has been a 300 ms timeout, 3 retries and no backoff[4], so one slow instance becomes a storm across the service. The 14:03 release was unrelated.\n\n## Action items\n- Exponential backoff on inventory calls, retry budget under 10% (Zhang Lei · 9/27)\n- Outlier ejection for the inventory service (Li Ming)",
        ),
        editing_note: l("AI 正在修改「影响」部分", "AI is revising the Impact section"),
        annotations: [{ author: "王晨", text: l("影响部分不要写具体订单数，等业务确认。", "Don't state order counts in Impact until the business confirms.") }],
      },
    },
    {
      id: "chart",
      kind: "chart",
      title: l("库存调用量与 5xx 对比", "Inventory calls vs 5xx"),
      meta: l("图表 · 2 个面板联动", "Chart · 2 linked panels"),
      version: 1,
      updated_at: at("14:25"),
      content: {
        kind: "chart",
        labels: labels36,
        markers: [{ at: 17, label: l("14:03 发布", "14:03 release"), tone: "warn" }, { at: 27, label: l("14:19 限制重试", "14:19 retries limited"), tone: "ok" }],
        panels: [
          { title: l("库存调用量（k/s）", "Inventory calls (k/s)"), unit: "k/s", peak: "3.9", series: { name: l("调用", "Calls"), values: calls } },
          { title: l("5xx 比例（%）", "5xx ratio (%)"), unit: "%", peak: "8.4", series: { name: "5xx", values: err }, threshold: 1 },
        ],
      },
    },
    {
      id: "csv",
      kind: "table",
      title: l("受影响实例.csv", "affected-instances.csv"),
      meta: l("表格 · 4 行", "Table · 4 rows"),
      version: 1,
      updated_at: at("14:25"),
      content: { kind: "table", columns: [l("实例", "Instance"), l("超时率", "Timeouts"), "P99", l("磁盘 IO 等待", "Disk IO wait"), l("开始", "Started")], rows: [
        ["inventory-03", "46%", "350 ms", "180 ms", "14:02"], ["inventory-01", "27%", "310 ms", "3 ms", "14:04"],
        ["inventory-04", "26%", "305 ms", "2 ms", "14:04"], ["inventory-02", "25%", "300 ms", "2 ms", "14:05"],
      ] },
    },
    {
      id: "diff",
      kind: "diff",
      title: l("checkout-api.yaml 差异", "checkout-api.yaml diff"),
      meta: l("配置 · 当前 → PRP-88", "Config · current → PRP-88"),
      version: 1,
      updated_at: at("14:25"),
      content: { kind: "diff", path: "config/checkout-api.yaml", from: "v2.8.1", to: "PRP-88",
        left: ["inventory:", "  timeout: 300ms", "  retry:", "-   max_attempts: 3", "    backoff: none", "-  circuit_breaker: off"],
        right: ["inventory:", "  timeout: 300ms", "  retry:", "+   max_attempts: 1", "    backoff: none", "+  circuit_breaker: on"] },
    },
  ],
  side: {
    connectors: [{ name: l("Prometheus · 生产", "Prometheus · prod"), calls: 1 }, { name: "Tempo", calls: 1 }, { name: "Elasticsearch", calls: 1 }, { name: "GitLab", calls: 1 }],
    context: { used: 11200, budget: 32000, parts: [
      { label: l("系统", "System"), tokens: 900, color: "var(--faint)" },
      { label: "Skills", tokens: 1400, color: "var(--c4)" },
      { label: l("证据", "Evidence"), tokens: 5200, color: "var(--c1)" },
      { label: l("历史", "History"), tokens: 3700, color: "var(--c2)" },
    ] },
    recalled: [{ kind: "runbook", title: l("Runbook「下游超时与重试风暴」", "Runbook “Downstream timeouts and retry storms”"), score: 0.86 }, { kind: "postmortem", title: l("INC-0987 重试放大事故", "INC-0987 retry amplification"), score: 0.74 }],
  },
};

/* ---------- C-330: lab bad-release drill, AI suggests a rollback (PA-08 sample) ---------- */
const badRelease = {
  ...review,
  id: "C-330",
  title: l("checkout-api 修订 7 上线后 5xx", "checkout-api 5xx after revision 7"),
  investigation_id: "INV-236",
  messages: [
    { id: "b1", role: "user", author: "王晨", created_at: at("10:48"), parts: [{ type: "text", markdown: l("checkout-api 10:42 之后 5xx 变多了，是发布引起的吗？", "checkout-api has more 5xx since 10:42 — is it the release?") }] },
    {
      id: "b2",
      role: "assistant",
      author: "AI",
      created_at: at("10:49"),
      parts: [
        { type: "status", text: l("Prometheus 样本：checkout-api / http_error_ratio", "Prometheus samples: checkout-api / http_error_ratio") },
        { type: "status", text: l("Kubernetes：checkout-api 的 ReplicaSet 修订", "Kubernetes: checkout-api ReplicaSet revisions") },
        { type: "status", text: l("Loki：aiops-lab/checkout-api 错误日志", "Loki: aiops-lab/checkout-api error logs") },
        {
          type: "text",
          markdown: l(
            "是。10:42 修订 7 上线后，/checkout 的 5xx 比例从 0.2% 升到 48%[1]。修订 7 和上一个修订 6 只差环境变量 LAB_FAULTS，镜像没变[2]；错误日志 injected fault 也只出现在修订 7 的 Pod[3]。",
            "Yes. After revision 7 went out at 10:42, /checkout 5xx rose from 0.2% to 48%[1]. Revisions 7 and 6 differ only in the LAB_FAULTS env var, same image[2], and the injected-fault errors only come from revision 7 Pods[3].",
          ),
        },
        {
          type: "rollback_suggestion",
          // Encoded service ID, as the real suggestion carries it (base64url("checkout-api\0prod")).
          service_id: "Y2hlY2tvdXQtYXBpAHByb2Q",
          workload: "aiops-lab/checkout-api",
          from_revision: "7",
          target_revision: "6",
          target_created_at: at("09:15"),
          target_change_cause: "lab drill bad-release reverted",
          reasons: [
            { text: l("修订 7 上线时间与 5xx 升高吻合", "Revision 7 went out when 5xx rose"), cites: [1] },
            { text: l("两次修订只差 LAB_FAULTS", "The two revisions differ only in LAB_FAULTS"), cites: [2] },
            { text: l("修订 6 运行期间错误率 0.2%，低于 2% 的验证阈值", "Errors were 0.2% on revision 6, under the 2% verification threshold"), cites: [1] },
          ],
        },
      ],
    },
  ],
  artifacts: [],
  side: { ...review.side, connectors: [{ name: "prometheus", calls: 1 }, { name: "kubernetes", calls: 1 }, { name: "loki", calls: 1 }], recalled: [] },
};

const STORE: Record<string, typeof capacity | typeof review> = { "C-311": capacity, "C-320": review, "C-330": badRelease as never };
let seq = 400;

mock("GET", "/conversations/:id", ({ params }) => {
  const c = STORE[params.id];
  if (c) return c;
  const row = LIST.find((x) => x.id === params.id);
  if (!row) throw new MockError(404, "not_found", "conversation not found");
  return { ...capacity, id: row.id, title: row.title, messages: capacity.messages.slice(0, 2) };
});

mock("POST", "/conversations", ({ body }) => {
  const b = body as { text: string; context?: string };
  const id = `C-${++seq}`;
  STORE[id] = {
    ...capacity,
    id,
    title: l(b.text.slice(0, 40) || "新对话", b.text.slice(0, 40) || "New conversation") as never,
    messages: [],
    artifacts: [],
  } as never;
  return { id };
});

mock("POST", "/conversations/:id/messages", ({ params, body }) => {
  const c = STORE[params.id];
  if (!c) throw new MockError(404, "not_found", "conversation not found");
  const b = body as { text: string; model?: string; reasoning?: string; attachments?: unknown[] };
  const msg = { id: `u-${Date.now()}`, role: "user", author: "王晨", created_at: new Date().toISOString(), parts: [{ type: "text", markdown: b.text }], attachments: b.attachments ?? [] };
  (c.messages as unknown[]).push(msg);
  return { message: msg, reply_id: `a-${Date.now()}` };
});

/** The mock event stream saves the finished reply, as the server would, so reopening the conversation shows it. */
export function saveReply(conversationId: string, message: unknown) {
  const c = STORE[conversationId];
  if (c) (c.messages as unknown[]).push(message);
}

/** Canned streamed reply used by the mock event stream. */
export function cannedReply(text: string) {
  const lower = text.toLowerCase();
  const incident = /5xx|错误|故障|error|incident/.test(lower);
  return {
    tools: incident
      ? [
          { id: "s1", connector: "prometheus", tool: "query_metrics", args: "error_rate · 1h", duration_ms: 380, status: "ok" },
          { id: "s2", connector: "gitlab", tool: "list_changes", args: "2h", duration_ms: 260, status: "ok" },
        ]
      : [{ id: "s1", connector: "prometheus", tool: "query_metrics", args: "p99 · cpu · 1h", duration_ms: 420, status: "ok" }],
    text: incident
      ? l(
          "5xx 从 14:05 开始上升，离 14:03 发布 v2.8.1 只有 2 分钟，但发布没有改动库存调用[2]。同一时间库存调用从 1.2k/s 涨到 3.9k/s，而下单量持平[1]，更像是下游变慢后被重试放大。建议派生分支，分别查谁先变慢、一个请求调了几次库存。",
          "5xx started rising at 14:05, 2 minutes after v2.8.1 shipped, but the release does not touch inventory calls[2]. Meanwhile inventory calls went from 1.2k/s to 3.9k/s while orders stayed flat[1] — it looks like a downstream slowdown amplified by retries. Fork branches to check who slowed first and how many inventory calls one request makes.",
        )
      : l(
          "我看了最近 1 小时的数据：checkout-api 的 P99 为 1.6 s，高于 SLO 300 ms；CPU 71%，接近 HPA 目标 70%[1]。主要时间花在等待下游库存服务，而不是计算资源。需要的话我可以把这个问题转成 Canvas 调查，分支验证库存服务和支付网关。",
          "Over the last hour checkout-api's P99 is 1.6 s, above its 300 ms SLO, and CPU is 71%, near the 70% HPA target[1]. Most of the time goes to waiting on the inventory service rather than compute. I can turn this into a canvas investigation and verify inventory and payments in separate branches.",
        ),
    citations: [
      { n: 1, label: l("Prometheus · 最近 1 小时", "Prometheus · last hour") },
      { n: 2, label: l("GitLab #8812", "GitLab #8812") },
    ],
  };
}
