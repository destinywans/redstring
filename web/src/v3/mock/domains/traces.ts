/** Traces v2: application traces (Tempo/Jaeger style) and agent traces (Langfuse/Phoenix style). */
import { mock, MockError } from "../router";

/** The sample spans are written in seconds; the contract reports milliseconds. */
const traceInMs = <T extends { total: number; spans: { start: number; dur: number }[] }>(t: T): T => ({ ...t, total: t.total * 1000, spans: t.spans.map((x) => ({ ...x, start: Math.round(x.start * 1000), dur: Math.round(x.dur * 1000) })) });
import { l } from "../../i18n";

const P = l;
// INV-231 (the retry storm on wans.run/redstring): checkout-api calls inventory-service four times per
// failed order — the first call and three immediate retries. Example data.
const COLORS = { "api-gateway": "var(--c5)", "checkout-api": "var(--c1)", "inventory-service": "var(--c3)", "orders-db": "var(--c2)", "payment-gateway": "var(--c6)", kafka: "var(--c4)", "session-cache": "var(--c8)" };
const FULL = ["api-gateway", "checkout-api", "inventory-service", "orders-db", "payment-gateway", "kafka"];
const SHORT = ["api-gateway", "checkout-api", "inventory-service"];

const APP_ROWS = [
  { id: "4f2a9c17", time: "14:12:58.214", op: "POST /api/orders", services: SHORT, duration_ms: 1214, spans: 9, errors: 4 },
  { id: "a07c11de", time: "14:12:57.902", op: "POST /api/orders", services: SHORT, duration_ms: 1206, spans: 9, errors: 4 },
  { id: "7fa01b62", time: "14:12:56.230", op: "POST /api/orders", services: SHORT, duration_ms: 1209, spans: 9, errors: 4 },
  { id: "e3d4c7a8", time: "14:12:55.702", op: "POST /api/orders", services: FULL, duration_ms: 1012, spans: 15, errors: 3 },
  { id: "0c55d913", time: "14:12:45.118", op: "GET /api/cart", services: ["api-gateway", "checkout-api", "session-cache"], duration_ms: 42, spans: 7, errors: 0 },
  { id: "91e2b8f0", time: "14:12:41.013", op: "POST /api/orders", services: FULL, duration_ms: 712, spans: 13, errors: 2 },
  { id: "5d10aa42", time: "14:12:30.502", op: "POST /api/coupons/apply", services: ["api-gateway", "checkout-api", "session-cache"], duration_ms: 96, spans: 6, errors: 0 },
  { id: "b61e0f27", time: "14:12:12.604", op: "POST /api/orders", services: FULL, duration_ms: 702, spans: 13, errors: 2 },
];
const AGENT_ROWS = [
  { id: "run_7f3a", time: "14:10:12", op: P("调查助手 · 库存服务调用", "Investigator · inventory calls"), agent: P("调查助手 v13", "Investigator v13"), model: "Claude Opus 5.5 · 高", services: ["query_metrics", "search_traces", "search_logs", "get_file_history", "search_runbooks"], duration_ms: 42600, spans: 12, errors: 0, tokens: "11.2k", cost: "¥0.84" },
  { id: "run_7f2c", time: "14:05:40", op: P("告警初查 · HighErrorRate", "Alert triage · HighErrorRate"), agent: P("告警初查 v5", "Triage v5"), model: "Claude Sonnet 5 · 中", services: ["query_metrics", "list_changes"], duration_ms: 18400, spans: 7, errors: 0, tokens: "4.8k", cost: "¥0.12" },
  { id: "run_7e91", time: "13:52:03", op: P("调查助手 · payment-gateway 延迟", "Investigator · payment-gateway latency"), agent: P("调查助手 v13", "Investigator v13"), model: "GPT-5 · 中", services: ["query_metrics", "search_traces"], duration_ms: 33800, spans: 9, errors: 1, tokens: "9.6k", cost: "¥0.52" },
  { id: "run_7e40", time: "13:30:00", op: P("定时巡检 · 交易链路", "Patrol · trading path"), agent: P("巡检 v3", "Patrol v3"), model: P("Qwen3 32B · 本地 · 思考 开", "Qwen3 32B · local · thinking on"), services: ["query_metrics"], duration_ms: 12100, spans: 5, errors: 0, tokens: "3.1k", cost: P("本地", "local") },
  { id: "run_7d18", time: "12:10:44", op: P("报告撰写 · INC-1039", "Report · INC-1039"), agent: P("报告撰写 v4", "Reporter v4"), model: "Claude Opus 5.5 · 中", services: ["get_incident", "search_runbooks"], duration_ms: 26500, spans: 6, errors: 0, tokens: "7.4k", cost: "¥0.46" },
];

// The same selectors the backend adds to the search for its views (PA-80).
const APP_VIEWS = [{ id: "all", label: P("全部", "All"), query: "" }, { id: "errors", label: P("错误", "Errors"), count: 214, query: "{ status = error }" }, { id: "slow", label: P("慢请求 > 1 s", "Slow > 1 s"), query: "{ trace:duration >= 1s }" }, { id: "release", label: P("发布后 v2.8.1", "After v2.8.1"), query: '{ resource.service.version = "v2.8.1" }' }];

// PA-78 demo states (example data). An unnarrowed query (view "All", no service, no condition) goes newest-first and
// stops at the search budget — 40 traces — so only the most recent minutes of the range are covered; one service is
// quieter, so the same budget reaches further back. api-gateway sees every request: even one second of it is too
// dense to list, whatever the range (413).
const RANGE_MIN: Record<string, number> = { "15m": 15, "30m": 30, "1h": 60, "6h": 360, "24h": 1440, "7d": 10080, "30d": 43200 };
const OPS = ["GET /api/cart", "POST /api/orders", "GET /api/products/{id}", "GET /api/orders/{id}", "POST /api/coupons/apply", "GET /api/search"];
const PATHS = [["api-gateway", "checkout-api", "session-cache"], SHORT, ["api-gateway", "search-api"], FULL, ["api-gateway", "checkout-api"], ["api-gateway", "search-api", "session-cache"]];
const pad = (n: number, w = 2) => String(n).padStart(w, "0");
const clock = (t: number) => {
  const d = new Date(t);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
};
function recentList(service: string | undefined, range: string) {
  let seed = service ? 11 : 7;
  const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647);
  const rangeMin = RANGE_MIN[range] ?? 60;
  const coverMin = service ? 26 : 8;
  const partial = coverMin < rangeMin;
  const winMs = Math.min(coverMin, rangeMin) * 60_000;
  const now = Date.now();
  const start = now - winMs;
  const items = Array.from({ length: 40 }, (_, i) => {
    const t = now - ((i + 0.3 + rnd() * 0.5) * winMs) / 40;
    const k = Math.floor(rnd() * OPS.length);
    const errors = rnd() < 0.08 ? 1 + Math.floor(rnd() * 3) : 0;
    const duration_ms = Math.round(errors ? 700 + rnd() * 900 : 20 + rnd() * (k === 1 ? 600 : 180));
    const id = Math.floor(rnd() * 0xffffffff).toString(16).padStart(8, "0");
    return { id, t, time: clock(t), op: OPS[k], services: service ? [service, ...PATHS[k].filter((x) => x !== service)].slice(0, 3) : PATHS[k], duration_ms, spans: PATHS[k].length * 2 + Math.floor(rnd() * 4), errors, href: `/traces/${id}` };
  });
  const counts: [number, number][] = Array.from({ length: 24 }, () => (service ? [3 + Math.floor(rnd() * 4), rnd() < 0.15 ? 1 : 0] : [10 + Math.floor(rnd() * 6), rnd() < 0.2 ? 1 : 0]));
  return {
    scope: "app", total: counts.reduce((a, [ok, er]) => a + ok + er, 0), took: "0.42 s",
    colors: { ...COLORS, "search-api": "var(--c7)", ...(service && !(service in COLORS) ? { [service]: "var(--c7)" } : {}) },
    unavailable: {}, facets: null, views: APP_VIEWS,
    histogram: { counts, axis: [clock(start).slice(0, 5), clock(now).slice(0, 5)] },
    scatter: items.map((r) => ({ x: Math.round((440 * (r.t - start)) / winMs), y: Math.round(r.errors ? 12 + rnd() * 30 : 100 - Math.min(60, r.duration_ms / 12)), error: r.errors > 0, id: r.id })),
    items: items.map(({ t: _t, ...r }) => r),
    ...(partial ? { partial: true, partial_reason: "search_budget", window_start_at: new Date(start).toISOString(), window_end_at: new Date(now).toISOString() } : {}),
  };
}

mock("GET", "/traces", ({ query }) => {
  const scope = query.scope === "agent" ? "agent" : "app";
  if (scope === "agent") {
    return {
      scope, total: 312, took: "0.18 s", unavailable: {}, colors: { query_metrics: "var(--dev)", search_logs: "var(--dev)", search_runbooks: "var(--info)", list_changes: "var(--dev)", search_traces: "var(--dev)", get_file_history: "var(--dev)", get_incident: "var(--dev)" },
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
  const q = String(query.q ?? "");
  const broad = query.view === "all" && !/status\s*=\s*error|duration\s*[<>]|resource\.service\.name|\bname\s*=/.test(q);
  const service = typeof query.service === "string" && query.service ? query.service : undefined;
  // PA-79 demo: the deployment keeps 7 days of searchable traces.
  if (query.range === "30d") throw new MockError(422, "range_exceeds_lookback", "Choose a range within the configured trace lookback", { max_lookback_seconds: 604800, max_range: "7d" });
  // PA-81 demo: search-api's Tempo is connected but the deployment doesn't declare search complete (no counts or
  // charts); notification-svc has no trace source at all.
  if (service === "search-api") return { ...recentList(service, "15m"), total: null, facets: null, histogram: null, scatter: null, partial: true, partial_reason: "search_unattested", unavailable: { total: "not_configured", facets: "not_configured", histogram: "not_configured", scatter: "not_configured" } };
  if (service === "notification-svc") return { scope: "app", total: null, took: "0 ms", colors: {}, facets: null, views: APP_VIEWS, histogram: null, scatter: null, items: null, partial: true, partial_reason: "not_configured", unavailable: { items: "not_configured", total: "not_configured", facets: "not_configured", histogram: "not_configured", scatter: "not_configured" } };
  if (broad && service === "api-gateway") throw new MockError(413, "trace_scope_limit", "Trace population exceeds the bounded search limit");
  if (broad) return recentList(service, String(query.range ?? "1h"));
  const counts: [number, number][] = [[30, 0], [32, 0], [31, 0], [33, 1], [30, 0], [34, 0], [32, 0], [35, 0], [31, 0], [33, 0], [36, 0], [34, 0], [33, 0], [35, 0], [34, 0], [36, 0], [22, 14], [18, 20], [16, 23], [17, 22], [18, 21], [19, 19], [21, 16], [30, 6], [33, 2], [35, 1], [34, 0], [36, 0], [35, 0], [37, 0]];
  const scatter = Array.from({ length: 70 }, (_, i) => {
    const x = 10 + ((i * 37) % 430);
    const error = x > 235 && x < 330 && i % 3 !== 0;
    return { x, y: error ? 18 + ((i * 53) % 40) : 70 + ((i * 29) % 36), error, id: error && i % 5 === 1 ? "4f2a9c17" : undefined };
  });
  const errorsOnly = query.view !== "all" || /status\s*=\s*error/.test(q);
  return {
    scope, total: errorsOnly ? 214 : 4813, took: "0.42 s", colors: COLORS, unavailable: {},
    facets: [
      { key: "service", label: P("服务", "Service"), items: Object.keys(COLORS).map((s, i) => ({ value: s, count: [4102, 4813, 4630, 3920, 1830, 2210, 960][i], color: COLORS[s as keyof typeof COLORS] })) },
      { key: "status", label: P("状态", "Status"), items: [{ value: P("错误", "Error"), count: 214, tone: "danger" }, { value: P("正常", "OK"), count: 4599, tone: "ok" }] },
      { key: "op", label: P("接口", "Endpoint"), items: [{ value: "POST /api/orders", count: 902 }, { value: "GET /api/cart", count: 1140 }, { value: "POST /api/coupons/apply", count: 221 }] },
      { key: "version", label: P("版本", "Version"), items: [{ value: "v2.8.1", count: 214 }, { value: "v2.8.0", count: 0 }] },
    ],
    views: APP_VIEWS,
    histogram: { counts, axis: ["13:35", P("14:03 发布", "14:03 release"), "14:35"] },
    scatter,
    items: APP_ROWS.filter((r) => !errorsOnly || r.errors).map((r) => ({ ...r, href: `/traces/${r.id}` })),
  };
});

mock("GET", "/traces/:id", ({ params }) => ({
  id: params.id,
  trace_id: `${params.id}77b34da6a3ce929d0e0e4736`,
  op: "POST /api/orders",
  duration_ms: 1214,
  time: "14:12:58.214",
  version: "v2.8.1",
  colors: COLORS,
  flame_note: P("99% 的时间花在调用库存：inventory.reserve 调了 4 次，前 3 次各等满 300 ms 超时、立即重试，第 4 次失败", "99% of the time goes to inventory: inventory.reserve is called 4 times — 3 wait the full 300 ms and are retried at once, the 4th fails"),
  spans: [
    { id: "s1", depth: 0, service: "api-gateway", op: "POST /api/orders", start: 0, end: 1214, status: "ok", critical: true, attrs: [{ k: "http.method", v: "POST" }, { k: "http.route", v: "/api/orders" }, { k: "http.status_code", v: "502" }] },
    { id: "s2", depth: 1, service: "checkout-api", op: "POST /orders", start: 4, end: 1210, status: "ok", critical: true, attrs: [{ k: "service.version", v: "v2.8.1" }, { k: "k8s.pod.name", v: "checkout-api-7f9c-4" }] },
    { id: "s3", depth: 2, service: "checkout-api", op: "validate_cart", start: 8, end: 30, status: "ok" },
    { id: "s4", depth: 2, service: "session-cache", op: "GET session:u_8812", start: 32, end: 35, status: "ok" },
    {
      id: "s5", depth: 2, service: "checkout-api", op: "inventory.reserve", start: 38, end: 338, status: "error", critical: true, label: P("第 1 次 · 超时 300 ms", "call 1 · timed out at 300 ms"),
      attrs: [{ k: "peer.instance", v: "inventory-03", hot: true }, { k: "timeout_ms", v: "300", hot: true }, { k: "retry.max_attempts", v: "3", hot: true }, { k: "retry.backoff", v: "none", hot: true }, { k: "otel.status", v: "ERROR" }],
      events: [{ at: "14:12:57.334", name: "exception", text: "TimeoutError: inventory.reserve timed out after 300ms\n  at RetryClient.call (retry.go:88)" }],
      logs: [{ time: "14:12:57.334", level: "ERROR", message: "inventory.reserve timeout after 300ms, retrying (1/3) instance=inventory-03" }, { time: "14:12:57.636", level: "ERROR", message: "inventory.reserve timeout after 300ms, retrying (2/3) instance=inventory-01" }, { time: "14:12:58.214", level: "ERROR", message: "retry 3/3 failed: inventory.reserve · POST /orders 502" }],
      links: [
        { kind: "metric", label: P("指标 · inventory-03 P99", "Metric · inventory-03 P99"), href: "/metrics" },
        { kind: "change", label: P("配置 · checkout-api.yaml 重试 3 次、无退避（v2.6 起）", "Config · checkout-api.yaml 3 retries, no backoff (since v2.6)"), href: "/changes", brand: "gitlab" },
        { kind: "runbook", label: P("Runbook · 下游超时与重试风暴", "Runbook · Downstream timeouts and retry storms"), href: "/runbooks" },
      ],
    },
    { id: "s6", depth: 3, service: "inventory-service", op: "reserve", start: 40, end: 338, status: "error", critical: true, attrs: [{ k: "host", v: "inventory-03", hot: true }, { k: "disk.io_wait_ms", v: "180", hot: true }] },
    { id: "s7", depth: 2, service: "checkout-api", op: "inventory.reserve", start: 339, end: 639, status: "error", critical: true, label: P("重试 1 · 超时 300 ms", "retry 1 · timed out at 300 ms"), attrs: [{ k: "peer.instance", v: "inventory-01" }, { k: "retry.attempt", v: "1" }] },
    { id: "s8", depth: 2, service: "checkout-api", op: "inventory.reserve", start: 640, end: 940, status: "error", critical: true, label: P("重试 2 · 超时 300 ms", "retry 2 · timed out at 300 ms"), attrs: [{ k: "peer.instance", v: "inventory-04" }, { k: "retry.attempt", v: "2" }] },
    { id: "s9", depth: 2, service: "checkout-api", op: "inventory.reserve", start: 941, end: 1198, status: "error", critical: true, label: P("重试 3 · 失败", "retry 3 · failed"), attrs: [{ k: "peer.instance", v: "inventory-02" }, { k: "retry.attempt", v: "3" }, { k: "rpc.status", v: "UNAVAILABLE · overloaded" }] },
  ],
  graph: {
    nodes: [
      { id: "api-gateway", x: 20, y: 140, sub: P("入口 · 1.21 s", "entry · 1.21 s") },
      { id: "checkout-api", x: 240, y: 140, sub: P("1.21 s · 502", "1.21 s · 502"), danger: true },
      { id: "inventory-service", x: 470, y: 90, sub: P("3 次超时 · 1 次失败", "3 timeouts · 1 failure"), danger: true },
      { id: "session-cache", x: 470, y: 190, sub: "3 ms" },
    ],
    edges: [
      { from: "api-gateway", to: "checkout-api", label: P("1 次", "1 call") },
      { from: "checkout-api", to: "inventory-service", label: P("4 次 · 1.16 s", "4 calls · 1.16 s"), danger: true },
      { from: "checkout-api", to: "session-cache", label: P("1 次", "1 call") },
    ],
  },
}));

mock("GET", "/agent-runs/:id/trace", ({ params }) => traceInMs({
  id: params.id,
  agent: P("调查助手 v13", "Investigator v13"),
  status: "success",
  stats: [
    { k: P("延迟", "Latency"), v: "42.6 s" }, { k: "tokens", v: "11.2k" }, { k: P("其中推理", "Reasoning"), v: "3.4k" },
    { k: P("成本", "Cost"), v: "¥0.84" }, { k: P("模型调用", "LLM calls"), v: "3" }, { k: P("工具调用", "Tool calls"), v: "4" },
  ],
  scores: [{ label: P("根因正确 · 人工", "Correct root cause · human"), tone: "ok", icon: "check" }, { label: P("引用完整 0.95 · LLM 评审", "Citations 0.95 · LLM judge"), tone: "ok" }, { label: P("护栏通过", "Guardrails passed"), tone: "ok", icon: "shield" }],
  session: "C-318",
  turns: [
    { n: 1, brand: "claude", model: P("Claude Opus 5.5 · 高", "Claude Opus 5.5 · High"), meta: "11.2k · 43 s", current: true, run_id: params.id },
    { n: 2, brand: "claude", model: P("Claude Opus 5.5 · 高", "Claude Opus 5.5 · High"), meta: "3.1k · 12 s", run_id: "run_7f41" },
    { n: 3, brand: "openai", model: P("GPT-5 · 中", "GPT-5 · Medium"), meta: "6.4k · 22 s", switched: true, run_id: "run_7f52" },
    { n: 4, brand: "openai", model: P("GPT-5 · 中", "GPT-5 · Medium"), meta: P("运行中", "running") },
  ],
  total: 42.6,
  spans: [
    { id: "a0", depth: 0, kind: "agent", name: P("调查助手 v13", "Investigator v13"), meta: P("agent · 第 1 轮", "agent · turn 1"), start: 0, dur: 42.6 },
    { id: "a1", depth: 1, kind: "chain", name: "plan_and_gather", meta: "chain", start: 0.1, dur: 24.8 },
    {
      id: "llm1", depth: 2, kind: "llm", name: "Claude Opus 5.5", meta: P("llm · 1.8k → 96 · 推理 1.0k", "llm · 1.8k → 96 · reasoning 1.0k"), start: 0.2, dur: 12.1,
      llm: {
        model: "claude-opus-5-5", effort: P("高", "High"), api: "Messages", max_tokens: 4096, first_token: "1.8 s", cost: "¥0.21", status: "ok",
        usage: { cached: 1400, input: 412, reasoning: 1020, output: 96 },
        params: { model: "claude-opus-5-5", max_tokens: 4096, thinking: { type: "enabled", effort: "high" }, tools: ["query_metrics", "search_traces", "search_logs", "get_file_history"] },
        messages: [
          { role: "system", tone: "neutral", tokens: P("1.2k tokens · 提示词 investigator.system v13", "1.2k tokens · prompt investigator.system v13"), folded: true, text: P("你是 SRE 调查助手。只能使用只读工具取证；任何写操作必须生成提案……", "You are an SRE investigator. Use read-only tools only; any write must become a proposal…") },
          { role: "user", tone: "accent", tokens: "38 tokens", text: P("checkout-api 从 14:05 开始 5xx 升高到 8.4%，帮我查原因。", "checkout-api 5xx rose to 8.4% since 14:05 — find the cause.") },
          { role: "context", tone: "info", tokens: P("证据信封 3 条 · 分支历史 6 条 · 2.1k tokens", "3 evidence envelopes · 6 branch history items · 2.1k tokens"), folded: true, text: P("[证据 E-12] 5xx 比例 0.2% → 8.4%（Prometheus）\n[证据 E-13] 发布 v2.8.1 @ 14:03，没有改动库存调用（GitLab）\n[证据 E-14] 库存调用 1.2k → 3.9k/s，下单量持平（Prometheus）", "[E-12] 5xx 0.2% → 8.4% (Prometheus)\n[E-13] Release v2.8.1 @ 14:03, no inventory calls changed (GitLab)\n[E-14] Inventory calls 1.2k → 3.9k/s while orders stay flat (Prometheus)") },
          { role: P("assistant · 工具调用", "assistant · tool calls"), tone: "t0", tokens: "96 tokens", text: P("先看库存服务谁先变慢，再抽一条失败请求的链路，查重试日志和重试配置的历史。", "First find which inventory instance slowed first, then pull one failed request's trace, the retry logs and the retry setting's history."), json: [{ name: "query_metrics", arguments: { query: "p99 by instance · node_disk", service: "inventory-service", window: "1h" } }, { name: "search_traces", arguments: { service: "checkout-api", q: "POST /orders status=error", window: "30m" } }, { name: "search_logs", arguments: { service: "checkout-api", q: "retry inventory.reserve", window: "30m" } }, { name: "get_file_history", arguments: { path: "config/checkout-api.yaml", key: "inventory.retry" } }] },
        ],
        scores: [{ name: P("工具选择合理", "Tool choice"), value: "0.9", source: P("LLM 评审", "LLM judge"), tone: "ok" }, { name: P("遵循只读约束", "Read-only compliance"), value: P("通过", "pass"), source: P("规则", "rule"), tone: "ok" }],
      },
    },
    {
      id: "tool1", depth: 2, kind: "tool", name: "query_metrics", meta: P("tool · Prometheus · 只读", "tool · Prometheus · read-only"), start: 12.4, dur: 0.4,
      tool: { brand: "prometheus", connector: P("连接器 Prometheus · 生产", "Prometheus connector · prod"), budget: P("本轮第 1 次 / 上限 8 次", "call 1 of 8 this turn"), args: { query: "p99 by instance · node_disk", service: "inventory-service", window: "1h" }, result: { instances: 4, first_slow: "inventory-03 @ 14:02", p99_before_ms: 80, p99_after_ms: 350, disk_io_wait_ms: "2 → 180", evidence_id: "E-17" }, evidence: "E-17" },
    },
    { id: "tool2", depth: 2, kind: "tool", name: "search_traces", meta: P("tool · Tempo · 只读", "tool · Tempo · read-only"), start: 12.9, dur: 0.5,
      tool: { brand: "tempo", connector: P("连接器 Tempo · 生产", "Tempo connector · prod"), budget: P("本轮第 2 次 / 上限 8 次", "call 2 of 8 this turn"), args: { service: "checkout-api", q: "POST /orders status=error", window: "30m" }, result: { traces: 214, sample: "4f2a9c", inventory_calls_per_order: 4, evidence_id: "E-18" }, evidence: "E-18" } },
    { id: "tool3", depth: 2, kind: "tool", name: "search_logs", meta: P("tool · Elasticsearch · 只读", "tool · Elasticsearch · read-only"), start: 13.5, dur: 0.7,
      tool: { brand: "elasticsearch", connector: P("连接器 Elasticsearch · 生产", "Elasticsearch connector · prod"), budget: P("本轮第 3 次 / 上限 8 次", "call 3 of 8 this turn"), args: { service: "checkout-api", q: "retry inventory.reserve", window: "30m" }, result: { hits: 2416, top_pattern: "inventory.reserve timeout after <n>ms, retrying (<n>/3)", evidence_id: "E-19" }, evidence: "E-19" } },
    { id: "tool4", depth: 2, kind: "tool", name: "get_file_history", meta: P("tool · GitLab · 只读", "tool · GitLab · read-only"), start: 14.3, dur: 0.3,
      tool: { brand: "gitlab", connector: P("连接器 GitLab · platform/checkout", "GitLab connector · platform/checkout"), budget: P("本轮第 4 次 / 上限 8 次", "call 4 of 8 this turn"), args: { path: "config/checkout-api.yaml", key: "inventory.retry" }, result: { timeout_ms: 300, max_attempts: 3, backoff: "none", last_changed: "v2.6 · 2026-03", evidence_id: "E-20" }, evidence: "E-20" } },
    {
      id: "llm2", depth: 2, kind: "llm", name: "Claude Opus 5.5", meta: P("llm · 4.6k → 64 · 推理 1.1k", "llm · 4.6k → 64 · reasoning 1.1k"), start: 14.7, dur: 10.2,
      llm: {
        model: "claude-opus-5-5", effort: P("高", "High"), api: "Messages", max_tokens: 4096, first_token: "1.5 s", cost: "¥0.26", status: "ok",
        usage: { cached: 2600, input: 1300, reasoning: 1100, output: 64 },
        params: { model: "claude-opus-5-5", max_tokens: 4096, thinking: { type: "enabled", effort: "high" } },
        messages: [{ role: P("tool 结果 × 4", "tool results × 4"), tone: "info", tokens: "2.0k tokens", folded: true, text: "E-17, E-18, E-19, E-20" }, { role: "assistant", tone: "t0", tokens: "64 tokens", text: P("inventory-03 在 14:02 先变慢；一个下单请求调了 4 次库存，68% 的调用是重试。接着查 Runbook。", "inventory-03 slowed first at 14:02; one order called inventory 4 times and 68% of calls are retries. Checking runbooks next.") }],
        scores: [],
      },
    },
    { id: "r1", depth: 1, kind: "retriever", name: "search_runbooks", meta: P("retriever · 3 条命中 · 最高 0.86", "retriever · 3 hits · top 0.86"), start: 25, dur: 0.4 },
    { id: "guard", depth: 1, kind: "guardrail", name: "prompt_injection_check", meta: P("guardrail · 通过", "guardrail · passed"), start: 25.5, dur: 0.1,
      guard: { target: P("工具输出 4 条 · 检索结果 3 条", "4 tool outputs · 3 retrieval results"), rule: P("工具输出中的指令一律当作数据，不执行", "Instructions inside tool output are treated as data, never executed"), result: P("未发现注入内容", "No injection found") } },
    {
      id: "llm3", depth: 1, kind: "llm", name: "Claude Opus 5.5", meta: P("llm · 5.1k → 312 · 推理 1.3k", "llm · 5.1k → 312 · reasoning 1.3k"), start: 25.7, dur: 16.9,
      llm: {
        model: "claude-opus-5-5", effort: P("高", "High"), api: "Messages", max_tokens: 4096, first_token: "2.2 s", cost: "¥0.37", status: "ok",
        usage: { cached: 3100, input: 1300, reasoning: 1300, output: 312 },
        params: { model: "claude-opus-5-5", max_tokens: 4096, thinking: { type: "enabled", effort: "high" } },
        messages: [{ role: "assistant", tone: "t0", tokens: "312 tokens", text: P("一台库存实例变慢，被无退避的重试放大成风暴：inventory-03 先变慢[1]，一个下单请求调 4 次库存[2][3]，重试配置是 300 ms、3 次、无退避[4]。建议重试降为 1 次并打开熔断（需要审批）。", "One slow inventory instance, amplified by retries without backoff into a storm: inventory-03 slowed first [1], one order calls inventory 4 times [2][3], and the retry setting is 300 ms, 3 retries, no backoff [4]. Recommend retrying once with the breaker on (needs approval).") }],
        scores: [{ name: P("根因正确", "Correct root cause"), value: "👍", source: P("王晨 · 人工", "Wang Chen · human"), tone: "ok" }, { name: P("引用完整", "Citation completeness"), value: "0.95", source: P("LLM 评审", "LLM judge"), tone: "ok" }],
      },
    },
    { id: "out", depth: 1, kind: "output", name: P("最终回答", "Final answer"), meta: P("output · 引用 4 条证据", "output · cites 4 evidence items"), start: 42.5, dur: 0.1 },
  ],
}));
