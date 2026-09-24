/** Traces v2: application traces (Tempo/Jaeger style) and agent traces (Langfuse/Phoenix style). */
import { mock } from "../router";
import { l } from "../../i18n";

const P = l;
const COLORS = { "api-gateway": "var(--c5)", "checkout-api": "var(--c1)", "orders-db": "var(--c2)", "payment-gateway": "var(--c6)", kafka: "var(--c4)", "session-cache": "var(--c8)" };
const FULL = ["api-gateway", "checkout-api", "orders-db", "payment-gateway", "kafka"];
const SHORT = ["api-gateway", "checkout-api", "orders-db"];

const APP_ROWS = [
  { id: "4bf92f35", time: "14:30:58.214", op: "POST /api/orders", services: FULL, duration_ms: 3210, spans: 14, errors: 1 },
  { id: "a07c11de", time: "14:30:41.880", op: "POST /api/orders", services: SHORT, duration_ms: 3180, spans: 9, errors: 1 },
  { id: "91e2b8f0", time: "14:30:22.013", op: "POST /api/orders", services: SHORT, duration_ms: 3050, spans: 9, errors: 1 },
  { id: "5d10aa42", time: "14:29:51.502", op: "POST /api/coupons/apply", services: ["api-gateway", "checkout-api", "session-cache"], duration_ms: 2960, spans: 6, errors: 1 },
  { id: "7fa01b62", time: "14:29:33.771", op: "POST /api/orders", services: FULL, duration_ms: 3120, spans: 14, errors: 1 },
  { id: "e3d4c7a8", time: "14:28:10.340", op: "POST /api/orders", services: SHORT, duration_ms: 3020, spans: 9, errors: 1 },
  { id: "0c55d913", time: "14:27:45.118", op: "GET /api/cart", services: ["api-gateway", "checkout-api", "session-cache"], duration_ms: 1420, spans: 7, errors: 0 },
  { id: "b61e0f27", time: "14:27:12.604", op: "POST /api/orders", services: FULL, duration_ms: 2910, spans: 14, errors: 1 },
];
const AGENT_ROWS = [
  { id: "run_7f3a", time: "14:06:12", op: P("调查助手 · checkout-api 5xx", "Investigator · checkout-api 5xx"), agent: P("调查助手 v13", "Investigator v13"), model: "Claude Opus 5.5 · 高", services: ["query_metrics", "search_logs", "search_runbooks"], duration_ms: 41200, spans: 10, errors: 0, tokens: "11.2k", cost: "¥0.84" },
  { id: "run_7f2c", time: "14:05:40", op: P("告警初查 · HighErrorRate", "Alert triage · HighErrorRate"), agent: P("告警初查 v5", "Triage v5"), model: "Claude Sonnet 5 · 中", services: ["query_metrics", "list_changes"], duration_ms: 18400, spans: 7, errors: 0, tokens: "4.8k", cost: "¥0.12" },
  { id: "run_7e91", time: "13:52:03", op: P("调查助手 · payment-gateway 延迟", "Investigator · payment-gateway latency"), agent: P("调查助手 v13", "Investigator v13"), model: "GPT-5 · 中", services: ["query_metrics", "search_traces"], duration_ms: 33800, spans: 9, errors: 1, tokens: "9.6k", cost: "¥0.52" },
  { id: "run_7e40", time: "13:30:00", op: P("定时巡检 · 交易链路", "Patrol · trading path"), agent: P("巡检 v3", "Patrol v3"), model: P("Qwen3 32B · 本地 · 思考 开", "Qwen3 32B · local · thinking on"), services: ["query_metrics"], duration_ms: 12100, spans: 5, errors: 0, tokens: "3.1k", cost: P("本地", "local") },
  { id: "run_7d18", time: "12:10:44", op: P("报告撰写 · INC-1039", "Report · INC-1039"), agent: P("报告撰写 v4", "Reporter v4"), model: "Claude Opus 5.5 · 中", services: ["get_incident", "search_runbooks"], duration_ms: 26500, spans: 6, errors: 0, tokens: "7.4k", cost: "¥0.46" },
];

mock("GET", "/traces", ({ query }) => {
  const scope = query.scope === "agent" ? "agent" : "app";
  if (scope === "agent") {
    return {
      scope, total: 312, took: "0.18 s", colors: { query_metrics: "var(--dev)", search_logs: "var(--dev)", search_runbooks: "var(--info)", list_changes: "var(--dev)", search_traces: "var(--dev)", get_incident: "var(--dev)" },
      facets: [
        { key: "agent", label: P("Agent", "Agent"), items: [{ value: P("调查助手", "Investigator"), count: 188 }, { value: P("告警初查", "Triage"), count: 96 }, { value: P("巡检", "Patrol"), count: 18 }, { value: P("报告撰写", "Reporter"), count: 10 }] },
        { key: "model", label: P("模型", "Model"), items: [{ value: "Claude Opus 5.5", count: 176 }, { value: "Claude Sonnet 5", count: 84 }, { value: "GPT-5", count: 34 }, { value: "Qwen3 32B", count: 18 }] },
        { key: "tool", label: P("工具", "Tool"), items: [{ value: "query_metrics", count: 290 }, { value: "search_logs", count: 204 }, { value: "search_runbooks", count: 150 }] },
        { key: "score", label: P("评分", "Score"), items: [{ value: P("有用", "Helpful"), count: 41, tone: "ok" }, { value: P("无用", "Not helpful"), count: 4, tone: "danger" }, { value: P("未评分", "Unscored"), count: 267 }] },
        { key: "guard", label: P("护栏", "Guardrail"), items: [{ value: P("通过", "Passed"), count: 309, tone: "ok" }, { value: P("拦截", "Blocked"), count: 3, tone: "danger" }] },
      ],
      views: [{ id: "all", label: P("全部", "All"), query: "" }, { id: "slow", label: P("慢于 30 s", "Slower than 30 s"), query: "duration > 30s" }, { id: "cost", label: P("成本最高", "Most expensive"), query: "sort: cost desc" }, { id: "neg", label: P("负反馈", "Negative feedback"), count: 4, query: "feedback = down" }],
      histogram: { counts: [9, 8, 7, 6, 5, 6, 9, 14, 18, 16, 15, 17, 16, 15, 22, 28, 19, 14, 12, 11, 9, 8, 7, 6].map((n, i) => [n, [7, 13, 15].includes(i) ? 1 : 0]), axis: ["00:00", "06:00", "12:00", P("现在", "Now")] },
      scatter: AGENT_ROWS.map((r, i) => ({ x: 40 + i * 80, y: 110 - r.duration_ms / 450, error: r.errors > 0, id: r.id })),
      items: AGENT_ROWS.map((r) => ({ ...r, href: `/agents/runs/${r.id}` })),
    };
  }
  const counts: [number, number][] = [[30, 0], [32, 0], [31, 0], [33, 1], [30, 0], [34, 0], [32, 0], [35, 0], [31, 0], [33, 0], [36, 0], [34, 0], [33, 0], [35, 0], [34, 0], [36, 0], [22, 14], [18, 20], [16, 23], [17, 22], [18, 21], [19, 19], [21, 16], [30, 6], [33, 2], [35, 1], [34, 0], [36, 0], [35, 0], [37, 0]];
  const scatter = Array.from({ length: 70 }, (_, i) => {
    const x = 10 + ((i * 37) % 430);
    const error = x > 235 && x < 330 && i % 3 !== 0;
    return { x, y: error ? 18 + ((i * 53) % 40) : 70 + ((i * 29) % 36), error, id: error && i % 5 === 1 ? "4bf92f35" : undefined };
  });
  const errorsOnly = query.view !== "all";
  return {
    scope, total: errorsOnly ? 214 : 4813, took: "0.42 s", colors: COLORS,
    facets: [
      { key: "service", label: P("服务", "Service"), items: Object.keys(COLORS).map((s, i) => ({ value: s, count: [4102, 4813, 3920, 1830, 2210, 960][i], color: COLORS[s as keyof typeof COLORS] })) },
      { key: "status", label: P("状态", "Status"), items: [{ value: P("错误", "Error"), count: 214, tone: "danger" }, { value: P("正常", "OK"), count: 4599, tone: "ok" }] },
      { key: "op", label: P("接口", "Endpoint"), items: [{ value: "POST /api/orders", count: 902 }, { value: "GET /api/cart", count: 1140 }, { value: "POST /api/coupons/apply", count: 221 }] },
      { key: "version", label: P("版本", "Version"), items: [{ value: "v2.8.1", count: 214 }, { value: "v2.8.0", count: 0 }] },
    ],
    views: [{ id: "all", label: P("全部", "All"), query: "{ }" }, { id: "errors", label: P("错误", "Errors"), count: 214, query: '{ resource.service.name = "checkout-api" && status = error && duration > 1s }' }, { id: "slow", label: P("慢请求 > 1 s", "Slow > 1 s"), query: "{ duration > 1s }" }, { id: "release", label: P("发布后 v2.8.1", "After v2.8.1"), query: '{ resource.service.version = "v2.8.1" }' }],
    histogram: { counts, axis: ["13:35", P("14:03 发布", "14:03 release"), "14:35"] },
    scatter,
    items: APP_ROWS.filter((r) => !errorsOnly || r.errors).map((r) => ({ ...r, href: `/traces/${r.id}` })),
  };
});

mock("GET", "/traces/:id", ({ params }) => ({
  id: params.id,
  trace_id: `${params.id}77b34da6a3ce929d0e0e4736`,
  op: "POST /api/orders",
  duration_ms: 3210,
  time: "14:30:58.214",
  version: "v2.8.1",
  colors: COLORS,
  flame_note: P("93% 的时间花在等待数据库连接：连接池 50/50 已满", "93% of the time is spent waiting for a DB connection: pool 50/50 is full"),
  spans: [
    { id: "s1", depth: 0, service: "api-gateway", op: "POST /api/orders", start: 0, end: 3210, status: "ok", critical: true, attrs: [{ k: "http.method", v: "POST" }, { k: "http.route", v: "/api/orders" }, { k: "http.status_code", v: "500" }] },
    { id: "s2", depth: 1, service: "checkout-api", op: "POST /orders", start: 6, end: 3204, status: "ok", critical: true, attrs: [{ k: "service.version", v: "v2.8.1" }, { k: "k8s.pod.name", v: "checkout-api-7f9c-4" }] },
    { id: "s3", depth: 2, service: "checkout-api", op: "validate_cart", start: 12, end: 44, status: "ok" },
    { id: "s4", depth: 2, service: "session-cache", op: "GET session:u_8812", start: 46, end: 49, status: "ok" },
    {
      id: "s5", depth: 2, service: "checkout-api", op: "db.pool.acquire", start: 80, end: 3080, status: "error", critical: true, label: P("等待连接 3.0 s", "waited 3.0 s for a connection"),
      attrs: [{ k: "db.system", v: "postgresql" }, { k: "db.name", v: "orders" }, { k: "pool.max", v: "50", hot: true }, { k: "pool.active", v: "50", hot: true }, { k: "pool.wait_ms", v: "3000", hot: true }, { k: "otel.status", v: "ERROR" }],
      events: [{ at: "14:30:58.214", name: "exception", text: "TimeoutError: acquire connection timeout after 3000ms\n  at Pool.acquire (pool.go:212)" }],
      logs: [{ time: "14:30:58.214", level: "ERROR", message: "acquire connection timeout (50/50)" }, { time: "14:30:58.190", level: "WARN", message: "slow request POST /orders 3.2s" }, { time: "14:30:57.902", level: "ERROR", message: "order create failed: TimeoutError" }],
      links: [
        { kind: "metric", label: P("指标 · db_connection_wait_p95", "Metric · db_connection_wait_p95"), href: "/metrics" },
        { kind: "change", label: P("变更 · #8812 connection_pool.max 200 → 50", "Change · #8812 connection_pool.max 200 → 50"), href: "/changes", brand: "gitlab" },
        { kind: "runbook", label: P("Runbook · 数据库连接池耗尽", "Runbook · DB pool exhaustion"), href: "/runbooks" },
      ],
    },
    { id: "s6", depth: 3, service: "orders-db", op: "INSERT orders", start: 3082, end: 3112, status: "ok", critical: true, attrs: [{ k: "db.statement", v: "INSERT INTO orders …" }, { k: "rows", v: "1" }] },
    { id: "s7", depth: 2, service: "checkout-api", op: "payment.authorize", start: 3114, end: 3198, status: "ok" },
    { id: "s8", depth: 3, service: "payment-gateway", op: "POST /authorize", start: 3118, end: 3194, status: "ok" },
    { id: "s9", depth: 4, service: "payment-gateway", op: "risk.check", start: 3122, end: 3150, status: "ok" },
    { id: "s10", depth: 2, service: "kafka", op: "produce order-events", start: 3199, end: 3202, status: "ok" },
  ],
  graph: {
    nodes: [
      { id: "api-gateway", x: 20, y: 140, sub: P("入口 · 3.21 s", "entry · 3.21 s") },
      { id: "checkout-api", x: 240, y: 140, sub: P("3.20 s · 1 错误", "3.20 s · 1 error"), danger: true },
      { id: "orders-db", x: 470, y: 45, sub: P("等待 3.0 s", "waited 3.0 s"), danger: true },
      { id: "payment-gateway", x: 470, y: 140, sub: "80 ms" },
      { id: "kafka", x: 470, y: 235, sub: "3 ms" },
    ],
    edges: [
      { from: "api-gateway", to: "checkout-api", label: P("1 次", "1 call") },
      { from: "checkout-api", to: "orders-db", label: P("2 次 · 3.0 s", "2 calls · 3.0 s"), danger: true },
      { from: "checkout-api", to: "payment-gateway", label: P("1 次", "1 call") },
      { from: "checkout-api", to: "kafka", label: P("1 次", "1 call") },
    ],
  },
}));

mock("GET", "/agent-runs/:id/trace", ({ params }) => ({
  id: params.id,
  agent: P("调查助手 v13", "Investigator v13"),
  status: "success",
  stats: [
    { k: P("延迟", "Latency"), v: "41.2 s" }, { k: "tokens", v: "11.2k" }, { k: P("其中推理", "Reasoning"), v: "3.4k" },
    { k: P("成本", "Cost"), v: "¥0.84" }, { k: P("模型调用", "LLM calls"), v: "3" }, { k: P("工具调用", "Tool calls"), v: "3" },
  ],
  scores: [{ label: P("根因正确 · 人工", "Correct root cause · human"), tone: "ok", icon: "check" }, { label: P("引用完整 0.95 · LLM 评审", "Citations 0.95 · LLM judge"), tone: "ok" }, { label: P("护栏通过", "Guardrails passed"), tone: "ok", icon: "shield" }],
  session: "C-318",
  turns: [
    { n: 1, brand: "claude", model: P("Claude Opus 5.5 · 高", "Claude Opus 5.5 · High"), meta: "11.2k · 41 s", current: true, run_id: params.id },
    { n: 2, brand: "claude", model: P("Claude Opus 5.5 · 高", "Claude Opus 5.5 · High"), meta: "3.1k · 12 s", run_id: "run_7f41" },
    { n: 3, brand: "openai", model: P("GPT-5 · 中", "GPT-5 · Medium"), meta: "6.4k · 22 s", switched: true, run_id: "run_7f52" },
    { n: 4, brand: "openai", model: P("GPT-5 · 中", "GPT-5 · Medium"), meta: P("运行中", "running") },
  ],
  total: 41.2,
  spans: [
    { id: "a0", depth: 0, kind: "agent", name: P("调查助手 v13", "Investigator v13"), meta: P("agent · 第 1 轮", "agent · turn 1"), start: 0, dur: 41.2 },
    { id: "a1", depth: 1, kind: "chain", name: "plan_and_gather", meta: "chain", start: 0.1, dur: 23.0 },
    {
      id: "llm1", depth: 2, kind: "llm", name: "Claude Opus 5.5", meta: P("llm · 1.8k → 96 · 推理 1.0k", "llm · 1.8k → 96 · reasoning 1.0k"), start: 0.2, dur: 12.1,
      llm: {
        model: "claude-opus-5-5", effort: P("高", "High"), api: "Messages", max_tokens: 4096, first_token: "1.8 s", cost: "¥0.21", status: "ok",
        usage: { cached: 1400, input: 412, reasoning: 1020, output: 96 },
        params: { model: "claude-opus-5-5", max_tokens: 4096, thinking: { type: "enabled", effort: "high" }, tools: ["query_metrics", "search_logs", "list_changes"] },
        messages: [
          { role: "system", tone: "neutral", tokens: P("1.2k tokens · 提示词 investigator.system v13", "1.2k tokens · prompt investigator.system v13"), folded: true, text: P("你是 SRE 调查助手。只能使用只读工具取证；任何写操作必须生成提案……", "You are an SRE investigator. Use read-only tools only; any write must become a proposal…") },
          { role: "user", tone: "accent", tokens: "38 tokens", text: P("checkout-api 从 14:05 开始 5xx 升高到 8.4%，帮我查原因。", "checkout-api 5xx rose to 8.4% since 14:05 — find the cause.") },
          { role: "context", tone: "info", tokens: P("证据信封 3 条 · 分支历史 6 条 · 2.1k tokens", "3 evidence envelopes · 6 branch history items · 2.1k tokens"), folded: true, text: P("[证据 E-12] 5xx 比例 0.2% → 8.4%（Prometheus）\n[证据 E-13] 发布 v2.8.1 @ 14:03（GitLab）\n[证据 E-14] 超时日志 1,284 条（Elasticsearch）", "[E-12] 5xx 0.2% → 8.4% (Prometheus)\n[E-13] Release v2.8.1 @ 14:03 (GitLab)\n[E-14] 1,284 timeout logs (Elasticsearch)") },
          { role: P("assistant · 工具调用", "assistant · tool calls"), tone: "t0", tokens: "96 tokens", text: P("先看最近的变更和数据库连接等待。", "Check recent changes and DB connection wait first."), json: [{ name: "query_metrics", arguments: { query: "db_connection_wait_p95", window: "30m" } }, { name: "search_logs", arguments: { service: "checkout-api", q: "timeout" } }] },
        ],
        scores: [{ name: P("工具选择合理", "Tool choice"), value: "0.9", source: P("LLM 评审", "LLM judge"), tone: "ok" }, { name: P("遵循只读约束", "Read-only compliance"), value: P("通过", "pass"), source: P("规则", "rule"), tone: "ok" }],
      },
    },
    {
      id: "tool1", depth: 2, kind: "tool", name: "query_metrics", meta: P("tool · Prometheus · 只读", "tool · Prometheus · read-only"), start: 12.4, dur: 0.4,
      tool: { brand: "prometheus", connector: P("连接器 Prometheus · 生产", "Prometheus connector · prod"), budget: P("本轮第 1 次 / 上限 8 次", "call 1 of 8 this turn"), args: { query: "db_connection_wait_p95", service: "checkout-api", window: "30m" }, result: { series: 2, points: 60, p95_before_ms: 12, p95_after_ms: 420, evidence_id: "E-17" }, evidence: "E-17" },
    },
    { id: "tool2", depth: 2, kind: "tool", name: "search_logs", meta: P("tool · Elasticsearch · 只读", "tool · Elasticsearch · read-only"), start: 12.9, dur: 0.7,
      tool: { brand: "elasticsearch", connector: P("连接器 Elasticsearch · 生产", "Elasticsearch connector · prod"), budget: P("本轮第 2 次 / 上限 8 次", "call 2 of 8 this turn"), args: { service: "checkout-api", q: "timeout", window: "30m" }, result: { hits: 1284, top_pattern: "acquire connection timeout (<n>/<n>)", evidence_id: "E-18" }, evidence: "E-18" } },
    {
      id: "llm2", depth: 2, kind: "llm", name: "Claude Opus 5.5", meta: P("llm · 3.9k → 64 · 推理 1.1k", "llm · 3.9k → 64 · reasoning 1.1k"), start: 13.7, dur: 9.8,
      llm: {
        model: "claude-opus-5-5", effort: P("高", "High"), api: "Messages", max_tokens: 4096, first_token: "1.5 s", cost: "¥0.26", status: "ok",
        usage: { cached: 2600, input: 1300, reasoning: 1100, output: 64 },
        params: { model: "claude-opus-5-5", max_tokens: 4096, thinking: { type: "enabled", effort: "high" } },
        messages: [{ role: P("tool 结果 × 2", "tool results × 2"), tone: "info", tokens: "1.3k tokens", folded: true, text: "E-17, E-18" }, { role: "assistant", tone: "t0", tokens: "64 tokens", text: P("连接等待 P95 从 12 ms 升到 420 ms，与发布同时发生；需要检查检索到的 Runbook。", "Conn wait P95 went 12 → 420 ms at release time; checking runbooks next.") }],
        scores: [],
      },
    },
    { id: "r1", depth: 1, kind: "retriever", name: "search_runbooks", meta: P("retriever · 3 条命中 · 最高 0.82", "retriever · 3 hits · top 0.82"), start: 23.6, dur: 0.4 },
    { id: "guard", depth: 1, kind: "guardrail", name: "prompt_injection_check", meta: P("guardrail · 通过", "guardrail · passed"), start: 24.1, dur: 0.1,
      guard: { target: P("工具输出 2 条 · 检索结果 3 条", "2 tool outputs · 3 retrieval results"), rule: P("工具输出中的指令一律当作数据，不执行", "Instructions inside tool output are treated as data, never executed"), result: P("未发现注入内容", "No injection found") } },
    {
      id: "llm3", depth: 1, kind: "llm", name: "Claude Opus 5.5", meta: P("llm · 4.4k → 312 · 推理 1.3k", "llm · 4.4k → 312 · reasoning 1.3k"), start: 24.3, dur: 16.8,
      llm: {
        model: "claude-opus-5-5", effort: P("高", "High"), api: "Messages", max_tokens: 4096, first_token: "2.2 s", cost: "¥0.37", status: "ok",
        usage: { cached: 3100, input: 1300, reasoning: 1300, output: 312 },
        params: { model: "claude-opus-5-5", max_tokens: 4096, thinking: { type: "enabled", effort: "high" } },
        messages: [{ role: "assistant", tone: "t0", tokens: "312 tokens", text: P("最可能的原因是 v2.8.1 把 connection_pool.max 从 200 改为 50，高峰期连接池被打满[1][2]。建议回滚该配置（需要审批）。", "Most likely v2.8.1 changed connection_pool.max from 200 to 50 and the pool saturated at peak [1][2]. Recommend rolling back the config (needs approval).") }],
        scores: [{ name: P("根因正确", "Correct root cause"), value: "👍", source: P("王晨 · 人工", "Wang Chen · human"), tone: "ok" }, { name: P("引用完整", "Citation completeness"), value: "0.95", source: P("LLM 评审", "LLM judge"), tone: "ok" }],
      },
    },
    { id: "out", depth: 1, kind: "output", name: P("最终回答", "Final answer"), meta: P("output · 引用 3 条证据", "output · cites 3 evidence items"), start: 41.1, dur: 0.1 },
  ],
}));
