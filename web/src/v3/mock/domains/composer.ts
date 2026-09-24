import { mock, MockError } from "../router";
import { l } from "../../i18n";

const P = (zh: string, en: string) => l(zh, en);
const lv = (id: string, zh: string, en: string, hz?: string, he?: string) => ({ id, label: P(zh, en), hint: hz ? P(hz, he ?? hz) : undefined });

const CLAUDE_LEVELS = [
  lv("low", "低", "Low", "较快：适合简单查询", "Faster: simple lookups"),
  lv("medium", "中", "Medium", "均衡：默认档位", "Balanced: the default"),
  lv("high", "高", "High", "更深入：耗时与成本约为「中」的 1.6 倍", "Deeper: ~1.6× the time and cost of Medium"),
  lv("max", "最高", "Max", "最深入：适合复杂根因分析，耗时最长", "Deepest: complex root-cause work, slowest"),
];
const GPT_LEVELS = [
  lv("minimal", "最小", "Minimal", "最快：几乎不做额外推理", "Fastest: almost no extra reasoning"),
  lv("low", "低", "Low", "较快：适合简单查询", "Faster: simple lookups"),
  lv("medium", "中", "Medium", "均衡：默认档位", "Balanced: the default"),
  lv("high", "高", "High", "更深入：耗时与成本更高", "Deeper: more time and cost"),
];
const THINK = [lv("off", "关", "Off", "直接回答：更快", "Answer directly: faster"), lv("on", "开", "On", "先思考再回答：更准确，也更慢", "Think first: more accurate, slower")];

export const MODELS = [
  { id: "claude:claude-opus-5-5", name: "Claude Opus 5.5", provider: P("Anthropic · Claude 连接器", "Anthropic · Claude connector"), brand: "claude", connector: "claude", api: "messages",
    reasoning: { kind: "levels", levels: CLAUDE_LEVELS, default: "high" }, tools: true, vision: true, context_k: 200, cost: "¥¥¥", note: P("调查助手默认", "Investigator default"), available: true },
  { id: "claude:claude-sonnet-5", name: "Claude Sonnet 5", provider: P("Anthropic · Claude 连接器", "Anthropic · Claude connector"), brand: "claude", connector: "claude", api: "messages",
    reasoning: { kind: "levels", levels: CLAUDE_LEVELS, default: "medium" }, tools: true, vision: true, context_k: 200, cost: "¥¥", note: P("更快，成本更低", "Faster and cheaper"), available: true },
  { id: "openai:gpt-5", name: "GPT-5", provider: P("OpenAI 兼容", "OpenAI-compatible"), brand: "openai", connector: "openai", api: "responses",
    reasoning: { kind: "levels", levels: GPT_LEVELS, default: "medium" }, tools: true, vision: true, context_k: 400, cost: "¥¥", available: true },
  { id: "openai:gpt-5-mini", name: "GPT-5 mini", provider: P("OpenAI 兼容", "OpenAI-compatible"), brand: "openai", connector: "openai", api: "responses",
    reasoning: { kind: "levels", levels: GPT_LEVELS, default: "low" }, tools: true, vision: true, context_k: 400, cost: "¥", available: false, unavailable_reason: P("仅平台团队", "Platform team only") },
  { id: "ollama:qwen3:32b", name: P("Qwen3 32B · 本地", "Qwen3 32B · local"), provider: P("本地 · Ollama", "Local · Ollama"), brand: "qwen", connector: "ollama", api: "chat_completions",
    reasoning: { kind: "toggle", levels: THINK, default: "on" }, tools: true, vision: false, context_k: 128, cost: P("本地", "local"), note: P("数据不出内网", "Data stays on-prem"), available: true },
  { id: "ollama:llama3.3:70b", name: P("Llama 3.3 70B · 本地", "Llama 3.3 70B · local"), provider: P("本地 · Ollama", "Local · Ollama"), brand: "ollama", connector: "ollama", api: "chat_completions",
    reasoning: { kind: "none", levels: [] }, tools: true, vision: false, context_k: 128, cost: P("本地", "local"), available: true },
];

mock("GET", "/models", () => ({ items: MODELS, default: { model: "claude:claude-opus-5-5", reasoning: "high" } }));

mock("POST", "/uploads", ({ body }) => {
  const b = (body ?? {}) as { name?: string; size?: number; mime?: string; kind?: string; url?: string };
  if ((b.size ?? 0) > 20 * 1024 * 1024) throw new MockError(413, "too_large", "file exceeds 20 MB");
  return { id: `up_${Date.now().toString(36)}`, kind: b.kind ?? "file", name: b.name ?? "file", size: b.size ?? 0, mime: b.mime ?? "application/octet-stream", url: b.url };
});

const series = (base: number, jump: number) => [base, base, base * 1.1, base, base * 1.05, base, base * 1.1, jump * 0.8, jump * 0.95, jump * 1.02, jump * 0.98, jump];
const PREVIEWS: Record<string, unknown> = {
  "service:checkout-api": {
    title: "checkout-api", subtitle: P("交易平台 · 负责人 李明 · 值班 王晨", "Trading · owner Li Ming · on call Wang Chen"), tier: "T0", status: "danger", status_label: P("异常", "Degraded"),
    metrics: [
      { label: P("错误率", "Error rate"), value: "8.4%", tone: "danger", series: series(0.2, 8.4) },
      { label: "P99", value: "1.6 s", tone: "warn", series: series(0.3, 1.6) },
      { label: P("错误预算", "Error budget"), value: "38%", tone: "warn", series: [80, 78, 77, 75, 74, 72, 70, 55, 48, 44, 40, 38] },
    ],
    change: { brand: "gitlab", text: "#8812 · v2.8.1 · connection_pool.max 200 → 50", meta: P("14:03 · 李明", "14:03 · Li Ming") },
    related: [{ label: P("2 条告警", "2 alerts"), tone: "danger" }, { label: P("1 个进行中的画布", "1 active canvas"), tone: "accent" }, { label: P("4 篇 Runbook", "4 runbooks"), tone: "t1" }, { label: "SLO 99.9%" }],
    href: "/services/checkout-api",
  },
};
mock("GET", "/mentions/:kind/:id/preview", ({ params }) => {
  const hit = PREVIEWS[`${params.kind}:${params.id}`];
  if (hit) return { kind: params.kind, id: params.id, ...(hit as object) };
  return { kind: params.kind, id: params.id, title: params.id, subtitle: P("点击插入后，AI 会读取它的最新状态", "Once inserted, the AI reads its latest state"), metrics: [], related: [] };
});

const COMMANDS = [
  { id: "investigate", group: "command", name: P("/调查", "/investigate"), desc: P("发起调查：选择模板并带入当前上下文", "Start an investigation with the current context"), icon: "branch", creates: "canvas",
    params: [{ name: P("对象", "Target"), example: "checkout-api", required: true }], usage: P("/调查 checkout-api 5xx 升高", "/investigate checkout-api 5xx rising") },
  { id: "loadtest", group: "command", name: P("/压测计划", "/loadtest"), desc: P("创建一张「计划」类型的画布", "Create a plan canvas"), icon: "layers", badge: P("新", "New"), creates: "canvas",
    params: [
      { name: P("链路", "Path"), example: P("checkout-api · 交易链路", "checkout-api · trade path"), required: true },
      { name: P("目标峰值", "Target peak"), example: P("36k QPS（去年峰值 × 3）", "36k QPS (last year × 3)"), required: true },
      { name: P("窗口", "Window"), example: "11/08 02:00–04:00", required: false },
      { name: P("环境", "Environment"), example: P("预发 → 生产（分阶段）", "staging → prod (staged)"), required: false },
    ],
    usage: P("/压测计划 交易链路 36k QPS 11/08", "/loadtest trade-path 36k QPS 11/08") },
  { id: "report", group: "command", name: P("/报告", "/report"), desc: P("生成或更新故障报告", "Draft or update the incident report"), icon: "file", creates: "report", usage: P("/报告 INC-1042", "/report INC-1042") },
  { id: "silence", group: "command", name: P("/静默", "/silence"), desc: P("静默告警（需要确认）", "Silence alerts (asks for confirmation)"), icon: "mute", usage: P("/静默 inventory-worker 1 小时", "/silence inventory-worker 1h") },
  { id: "compare", group: "command", name: P("/对比", "/compare"), desc: P("对比两个时间段、版本或节点", "Compare two windows, versions or nodes"), icon: "layers", usage: P("/对比 v2.8.0 v2.8.1", "/compare v2.8.0 v2.8.1") },
  { id: "summarize", group: "command", name: P("/总结", "/summarize"), desc: P("总结当前对话，生成要点", "Summarize this conversation"), icon: "sparkles" },
  { id: "sk-capacity", group: "skill", name: P("大促容量评估", "Peak capacity review"), desc: P("Skill v3 · 按历史峰值推算副本与连接数", "Skill v3 · replicas and pools from past peaks"), icon: "book" },
  { id: "sk-rollback", group: "skill", name: P("发布回滚规范", "Release rollback guide"), desc: P("Skill v5 · 回滚前检查与步骤", "Skill v5 · checks and steps before rollback"), icon: "book" },
  { id: "rb-db-pool", group: "runbook", name: P("数据库连接池耗尽", "DB pool exhaustion"), desc: P("适用 checkout-api 等 4 个服务", "Applies to checkout-api and 3 more"), icon: "book" },
  { id: "tpl-inv", group: "template", name: P("故障调查", "Incident investigation"), desc: P("画布模板 · 主线 + 3 个分支", "Canvas template · main + 3 branches"), icon: "grid", creates: "canvas" },
  { id: "tpl-plan", group: "template", name: P("压测计划", "Load-test plan"), desc: P("画布模板 · 6 个节点，执行需审批", "Canvas template · 6 nodes, execution needs approval"), icon: "grid", creates: "canvas" },
];
mock("GET", "/commands", () => ({ items: COMMANDS }));

mock("GET", "/models/admin", () => ({
  providers: [
    { brand: "claude", name: "Claude", api: P("Messages API · 2 个模型", "Messages API · 2 models"), models: 2, status: "ok" },
    { brand: "openai", name: P("OpenAI 兼容", "OpenAI-compatible"), api: P("Responses API（首选）· 回退 Chat Completions · 2 个模型", "Responses API (preferred) · falls back to Chat Completions · 2 models"), models: 2, status: "ok" },
    { brand: "ollama", name: P("Ollama · 本地", "Ollama · local"), api: P("Chat Completions · 2 个模型 · 内网", "Chat Completions · 2 models · on-prem"), models: 2, status: "ok" },
  ],
  items: MODELS.map((m, i) => ({
    ...m,
    used_by: [P("调查助手 · 报告撰写", "Investigator · Reporter"), P("告警初查", "Alert triage"), "—", "—", P("定时巡检", "Patrol"), "—"][i],
    scope: [P("全员", "Everyone"), P("全员", "Everyone"), P("SRE、开发", "SRE, Dev"), P("平台团队", "Platform team"), P("全员", "Everyone"), P("全员", "Everyone")][i],
    status: i === 5 ? "unverified" : "ok",
    is_default: i === 0,
  })),
  detail: {
    model: "openai:gpt-5",
    api_chain: [
      { api: "Responses API", note: P("首选", "Preferred") },
      { api: "Chat Completions", note: P("Responses 不可用时回退，参数改用 reasoning_effort", "Fallback when Responses is unavailable; uses reasoning_effort") },
    ],
    mapping: [
      { level: P("最小", "Minimal"), param: 'reasoning.effort = "minimal"' },
      { level: P("低", "Low"), param: 'reasoning.effort = "low"' },
      { level: P("中", "Medium"), param: 'reasoning.effort = "medium"', is_default: true },
      { level: P("高", "High"), param: 'reasoning.effort = "high"' },
    ],
    checks: [P("工具调用", "Tool calls"), P("流式", "Streaming"), P("看图", "Vision")],
    roles: P("SRE、开发", "SRE, Dev"),
    agents: P("调查助手 v13（灰度）", "Investigator v13 (canary)"),
    limits: P("每轮 8 次模型调用 · 32k 输入", "8 model calls per turn · 32k input"),
  },
}));
