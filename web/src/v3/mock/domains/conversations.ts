import { mock, MockError, paginate } from "../router";
import { l } from "../../i18n";
import { series } from "../../charts";
import { ago, at } from "../time";

const LIST = [
  { id: "INV-231", title: l("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), mode: "canvas", status: "waiting_approval", source: "alert_triage", scope: l("INC-1042 · checkout-api · 生产", "INC-1042 · checkout-api · prod"), incident_id: "INC-1042", participants: ["王晨", "李明", "AI"], size: l("4 个分支", "4 branches"), updated_at: ago(3), tokens: 48000, team: true, mine: true },
  { id: "C-311", title: l("checkout-api 大促扩容评估", "checkout-api capacity for the sale"), mode: "chat", status: "active", source: "manual", scope: l("checkout-api · 生产", "checkout-api · prod"), participants: ["王晨", "AI"], size: l("6 轮", "6 turns"), updated_at: ago(12), tokens: 12000, team: true, mine: true },
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
const wait = series(36, 14, 4, 12, 1, [22, 26]);
const err = series(36, 0.3, 0.1, 12, 1, [23, 26]);
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
        { type: "tool_calls", summary: l("调用 3 个工具", "3 tool calls"), tokens: 11200, duration_s: 9.8, calls: [
          { id: "a", connector: "prometheus", tool: "query_metrics", args: "db_wait_p95 · 1h", duration_ms: 400, status: "ok" },
          { id: "b", connector: "elasticsearch", tool: "search_logs", args: '"connection timeout" · 30m', duration_ms: 700, status: "ok" },
          { id: "c", connector: "gitlab", tool: "get_commit_diff", args: "#8812", duration_ms: 300, status: "ok" },
        ] },
        { type: "tasks", title: l("调查计划", "Investigation plan"), items: [
          { label: l("读取告警上下文", "Read alert context"), state: "done", meta: "0.2 s" },
          { label: l("查询最近 2 小时变更", "Query changes in the last 2 hours"), state: "done", meta: "0.3 s" },
          { label: l("查询连接池与慢查询指标", "Query pool and slow-query metrics"), state: "done", meta: "0.4 s" },
          { label: l("对比预发配置", "Compare with staging config"), state: "done", meta: "0.2 s" },
          { label: l("生成结论与提案", "Conclude and propose"), state: "done", meta: "1.1 s" },
        ] },
        { type: "text", markdown: l(
          "**结论：连接池被打满。** 14:03 发布的 v2.8.1 把 `connection_pool.max` 从 200 改为 50[1]：\n\n- 连接等待 P95 从 12 ms 升到 420 ms[2]\n- 活跃连接 50 / 50，排队超时返回 5xx[3]",
          "**Conclusion: the connection pool is saturated.** Release v2.8.1 at 14:03 changed `connection_pool.max` from 200 to 50[1]:\n\n- Connection wait P95 rose from 12 ms to 420 ms[2]\n- Active connections at 50 / 50; queued requests time out and return 5xx[3]",
        ) },
        { type: "chart", title: l("连接等待 P95 · checkout-api", "Connection wait P95 · checkout-api"), unit: "ms", labels: labels36, series: [{ name: "P95", values: wait.map((v) => Math.min(v, 460)) }, { name: "P50", values: wait.map((v) => Math.round(Math.min(v, 460) * 0.35)) }], threshold: 50, markers: [{ at: 22, label: l("v2.8.1 发布", "v2.8.1 release"), tone: "warn" }], source: "Prometheus", deep_link: "https://grafana.example.internal" },
        { type: "table", title: l("各实例错误率", "Error rate by instance"), filter: l("错误率 > 1%", "Error rate > 1%"), columns: [l("实例", "Instance"), l("错误率", "Error rate"), l("连接", "Connections"), l("排队 P95", "Queue P95")], rows: [
          ["checkout-api-7f9c-4", "9.2%", "50 / 50", "3.1 s"],
          ["checkout-api-7f9c-1", "8.9%", "50 / 50", "3.0 s"],
          ["checkout-api-7f9c-3", "7.6%", "49 / 50", "2.8 s"],
          ["checkout-api-7f9c-5", "1.4%", "31 / 50", "0.9 s"],
        ] },
        { type: "diff", path: "config/checkout-api.yaml", from: "v2.8.0", to: "v2.8.1", left: ["  pool:", "-   max: 200", "    timeout: 30s"], right: ["  pool:", "+   max: 50", "    timeout: 30s"] },
        { type: "image", title: l("调用关系示意", "Call graph sketch"), caption: l("AI 生成 · 1280 × 360 · 84 KB", "AI generated · 1280 × 360 · 84 KB"), alt: l("gateway 调用 checkout-api，再调用 payment-gateway 与 orders-db", "gateway calls checkout-api, which calls payment-gateway and orders-db"), diagram: "call_graph" },
        { type: "topology", caption: l("gateway → checkout-api → orders-db · 红色为异常链路 · 连线速度代表流量", "gateway → checkout-api → orders-db · red is unhealthy · dash speed shows traffic"), href: "/resources" },
        { type: "files", files: [
          { id: "f1", name: l("故障报告.md", "incident-report.md"), kind: "md", size: "6 KB · v3", preview: l("# checkout-api 5xx 上升\n## 概要\nv2.8.1 把连接池上限从 200 改为 50…", "# checkout-api 5xx spike\n## Summary\nv2.8.1 lowered the pool max from 200 to 50…") },
          { id: "f2", name: "pods.csv", kind: "csv", size: l("12 行", "12 rows"), preview: "pod,error_rate,pool\n7f9c-4,9.2%,50/50\n7f9c-1,8.9%,50/50\n7f9c-3,7.6%,49/50" },
          { id: "f3", name: "deploy.yaml", kind: "yaml", size: "1.2 KB", preview: 'spec:\n  template:\n    env:\n      - name: POOL_MAX\n        value: "50"' },
          { id: "f4", name: "error.log", kind: "log", size: l("1,284 行", "1,284 lines"), preview: "14:30:58 ERROR acquire connection timeout\n14:30:57 ERROR order create failed\n14:30:57 ERROR acquire connection timeout" },
        ] },
        { type: "proposal", id: "PRP-88", title: l("回滚连接池上限 50 → 200", "Roll back pool max 50 → 200"), risk: "medium", reversible: true, target: "production / checkout-api", detail: l("滚动更新约 3 分钟 · 执行后自动观测 180 秒", "Rolling update ≈3 min · 180 s automatic verification afterwards") },
        { type: "form", title: l("继续之前，请确认：", "Before continuing, please confirm:"), fields: [
          { id: "env", label: l("环境", "Environment"), kind: "radio", options: [l("生产", "Production"), l("预发", "Staging")], value: l("生产", "Production") },
          { id: "window", label: l("时间窗口", "Time window"), kind: "select", options: ["14:00 – 14:35", "13:30 – 14:35"], value: "14:00 – 14:35" },
          { id: "downstream", label: l("同时检查下游 payment-gateway", "Also check downstream payment-gateway"), kind: "check", value: true },
        ] },
        { type: "citations", items: [
          { n: 1, label: l("GitLab #8812 · 配置差异", "GitLab #8812 · config diff"), ref: { kind: "change", id: "8812", label: "#8812" } },
          { n: 2, label: l("Prometheus · 连接等待", "Prometheus · connection wait") },
          { n: 3, label: l("Elasticsearch · 超时日志", "Elasticsearch · timeout logs") },
        ] },
        { type: "data_gap", text: l("缺少 checkout-api 的链路数据（Tempo 未映射），结论置信度：中", "Missing traces for checkout-api (Tempo not mapped) — confidence: medium"), href: "/integrations/coverage" },
        { type: "next_steps", items: [
          { icon: "shield", label: l("生成回滚提案", "Create rollback proposal"), action: "proposal" },
          { icon: "branch", label: l("派生分支验证", "Fork a branch to verify"), action: "canvas" },
          { icon: "search", label: l("查看 payment-gateway 的 429", "Check payment-gateway 429s"), action: "ask" },
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
          "# checkout-api 5xx 上升故障报告\n\n## 概要\nv2.8.1 把连接池上限从 200 改为 50，高峰期连接被占满，请求排队超时返回 5xx[1][2]。回滚后 3 分钟内恢复。\n\n## 影响\n5xx 峰值 8.4%，影响下单接口；受影响订单数以业务侧统计为准。\n\n## 根因\n配置模板缺少连接池下限校验；发布前风险评估给出中风险提示但未阻断。\n\n## 改进项\n- 配置模板增加连接池下限校验（张磊 · 9/27）\n- 发布流水线加入连接等待冒烟检查（李明）",
          "# checkout-api 5xx spike — incident report\n\n## Summary\nv2.8.1 lowered the pool max from 200 to 50; at peak the pool saturated and queued requests timed out with 5xx[1][2]. Recovered within 3 minutes of the rollback.\n\n## Impact\n5xx peaked at 8.4%, affecting order creation; affected order counts come from the business side.\n\n## Root cause\nThe config template lacked a lower bound for the pool; the pre-release risk check flagged medium risk but did not block.\n\n## Action items\n- Add a pool lower-bound check to the config template (Zhang Lei · 9/27)\n- Add a connection-wait smoke check to the release pipeline (Li Ming)",
        ),
        editing_note: l("AI 正在修改「影响」部分", "AI is revising the Impact section"),
        annotations: [{ author: "王晨", text: l("影响部分不要写具体订单数，等业务确认。", "Don't state order counts in Impact until the business confirms.") }],
      },
    },
    {
      id: "chart",
      kind: "chart",
      title: l("连接等待与 5xx 对比", "Connection wait vs 5xx"),
      meta: l("图表 · 2 个面板联动", "Chart · 2 linked panels"),
      version: 1,
      updated_at: at("14:25"),
      content: {
        kind: "chart",
        labels: labels36,
        markers: [{ at: 22, label: l("14:03 发布", "14:03 release"), tone: "warn" }, { at: 29, label: l("14:21 回滚", "14:21 rollback"), tone: "ok" }],
        panels: [
          { title: l("连接等待 P95（ms）", "Connection wait P95 (ms)"), unit: "ms", peak: "420", series: { name: "P95", values: wait } },
          { title: l("5xx 比例（%）", "5xx ratio (%)"), unit: "%", peak: "8.4", series: { name: "5xx", values: err }, threshold: 1 },
        ],
      },
    },
    {
      id: "csv",
      kind: "table",
      title: l("受影响实例.csv", "affected-instances.csv"),
      meta: l("表格 · 6 行", "Table · 6 rows"),
      version: 1,
      updated_at: at("14:25"),
      content: { kind: "table", columns: [l("实例", "Instance"), l("错误率", "Error rate"), l("连接池", "Pool"), l("排队 P95", "Queue P95"), l("开始", "Started")], rows: [
        ["7f9c-4", "9.2%", "50 / 50", "3.1 s", "14:05"], ["7f9c-1", "8.9%", "50 / 50", "3.0 s", "14:05"], ["7f9c-2", "8.1%", "50 / 50", "2.9 s", "14:06"],
        ["7f9c-3", "7.6%", "49 / 50", "2.8 s", "14:06"], ["7f9c-5", "1.4%", "31 / 50", "0.9 s", "14:27"], ["7f9c-6", "0.3%", "18 / 50", "0.2 s", "—"],
      ] },
    },
    {
      id: "diff",
      kind: "diff",
      title: l("deploy.yaml 差异", "deploy.yaml diff"),
      meta: l("配置 · v2.8.0 → v2.8.1", "Config · v2.8.0 → v2.8.1"),
      version: 1,
      updated_at: at("14:25"),
      content: { kind: "diff", path: "deploy.yaml", from: "v2.8.0", to: "v2.8.1",
        left: ["spec:", "  template:", "    env:", "      - name: POOL_MAX", '-       value: "200"', "      - name: POOL_TIMEOUT", '        value: "30s"'],
        right: ["spec:", "  template:", "    env:", "      - name: POOL_MAX", '+       value: "50"', "      - name: POOL_TIMEOUT", '        value: "30s"'] },
    },
  ],
  side: {
    connectors: [{ name: l("Prometheus · 生产", "Prometheus · prod"), calls: 2 }, { name: "Elasticsearch", calls: 1 }, { name: "GitLab", calls: 1 }],
    context: { used: 11200, budget: 32000, parts: [
      { label: l("系统", "System"), tokens: 900, color: "var(--faint)" },
      { label: "Skills", tokens: 1400, color: "var(--c4)" },
      { label: l("证据", "Evidence"), tokens: 5200, color: "var(--c1)" },
      { label: l("历史", "History"), tokens: 3700, color: "var(--c2)" },
    ] },
    recalled: [{ kind: "runbook", title: l("Runbook「数据库连接池耗尽」", "Runbook “DB pool exhaustion”"), score: 0.86 }, { kind: "postmortem", title: l("INC-0987 连接池事故", "INC-0987 pool incident"), score: 0.74 }],
  },
};

const STORE: Record<string, typeof capacity | typeof review> = { "C-311": capacity, "C-320": review };
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
          "5xx 在 14:03 发布 v2.8.1 之后开始上升，同时数据库连接等待 P95 从 12 ms 升到 420 ms[1]。发布内容包含 `connection_pool.max 200 → 50`[2]，最可能的原因是连接池被打满。建议先回滚连接池配置，再观察 180 秒。",
          "5xx started rising right after v2.8.1 at 14:03, while DB connection wait P95 went from 12 ms to 420 ms[1]. The release includes `connection_pool.max 200 → 50`[2], so the pool is most likely saturated. Roll back the pool config first, then watch for 180 seconds.",
        )
      : l(
          "我看了最近 1 小时的数据：checkout-api 的 P99 为 1.6 s，高于 SLO 300 ms；CPU 71%，接近 HPA 目标 70%[1]。主要压力来自数据库连接等待，而不是计算资源。需要的话我可以把这个问题转成 Canvas 调查，分支验证连接池和上游依赖。",
          "Over the last hour checkout-api's P99 is 1.6 s, above its 300 ms SLO, and CPU is 71%, near the 70% HPA target[1]. The pressure comes from DB connection wait rather than compute. I can turn this into a canvas investigation and verify the pool and upstream dependencies in separate branches.",
        ),
    citations: [
      { n: 1, label: l("Prometheus · 最近 1 小时", "Prometheus · last hour") },
      { n: 2, label: l("GitLab #8812", "GitLab #8812") },
    ],
  };
}
