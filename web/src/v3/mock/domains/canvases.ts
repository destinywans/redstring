import { mock, MockError, paginate } from "../router";
import { l } from "../../i18n";
import { at } from "../time";

const P = l;
type Thumb = { nodes: [number, number, number, number, string][]; edges: string[] };
const T_INV: Thumb = {
  nodes: [[12, 44, 44, 22, "danger"], [84, 10, 50, 16, "ok"], [84, 36, 62, 34, "accent"], [84, 82, 50, 16, "ok"], [174, 30, 54, 18, "accent"], [174, 58, 54, 16, "c3"]],
  edges: ["M56 55 C 70 55, 70 18, 84 18", "M56 55 C 70 55, 70 53, 84 53", "M56 55 C 70 55, 70 90, 84 90", "M146 50 L 174 39", "M146 56 L 174 66"],
};
const T_PLAN: Thumb = {
  nodes: [[10, 44, 42, 22, "info"], [78, 8, 56, 16, "sunken"], [78, 30, 56, 30, "sunken"], [78, 66, 56, 16, "sunken"], [78, 88, 56, 14, "warn"], [160, 26, 66, 38, "accent"], [160, 74, 66, 18, "danger"]],
  edges: ["M52 55 C 65 55, 65 16, 78 16", "M52 55 C 65 55, 65 45, 78 45", "M52 55 C 65 55, 65 74, 78 74", "M52 55 C 65 55, 65 95, 78 95", "M134 45 L 160 45", "M193 64 L 193 74"],
};
const T_REV: Thumb = {
  nodes: [[14, 16, 60, 18, "t1"], [14, 46, 60, 18, "sunken"], [14, 76, 60, 18, "sunken"], [110, 30, 56, 20, "ok"], [110, 62, 56, 20, "warn"], [190, 44, 40, 24, "accent"]],
  edges: ["M74 25 L 110 40", "M74 55 L 110 40", "M74 85 L 110 72", "M166 40 L 190 56", "M166 72 L 190 56"],
};
const T_ANA: Thumb = {
  nodes: [[10, 40, 46, 26, "dev"], [84, 12, 58, 20, "c5"], [84, 44, 58, 20, "c2"], [84, 76, 58, 20, "c4"], [170, 40, 58, 26, "accent"]],
  edges: ["M56 53 C 70 53, 70 22, 84 22", "M56 53 L 84 54", "M56 53 C 70 53, 70 86, 84 86", "M142 22 C 156 22, 156 53, 170 53", "M142 54 L 170 53", "M142 86 C 156 86, 156 53, 170 53"],
};

const CANVASES = [
  { id: "INV-231", title: P("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), kind: "investigation", status: "active", meta: P("4 个分支 · 12 条证据 · INV-231", "4 branches · 12 evidence · INV-231"), participants: ["王晨", "李明", "AI"], updated_at: at("14:26"), href: "/canvas/INV-231", investigation_id: "INV-231", services: ["checkout-api"], thumb: T_INV },
  // The lab bad-release drill (PA-08 rollback samples): revision 7 of aiops-lab/checkout-api answers 5xx.
  { id: "INV-236", title: P("checkout-api 修订 7 上线后 5xx", "checkout-api 5xx after revision 7"), kind: "investigation", status: "active", meta: P("2 个分支 · 3 条证据 · INV-236", "2 branches · 3 evidence · INV-236"), participants: ["王晨", "AI"], updated_at: at("10:50"), href: "/canvas/INV-236", investigation_id: "INV-236", services: ["checkout-api"], thumb: T_INV },
  { id: "CV-318", title: P("双十一大促压测计划", "Singles' Day load-test plan"), kind: "plan", status: "waiting_approval", meta: P("6 个节点 · 执行步骤需审批", "6 nodes · execution needs approval"), participants: ["王晨", "AI"], updated_at: at("13:40"), href: "/canvas/CV-318", services: ["checkout-api", "payment-gateway", "order-service"], thumb: T_PLAN },
  { id: "INV-229", title: P("payment-gateway 延迟排查", "payment-gateway latency"), kind: "investigation", status: "active", meta: P("2 个分支 · 5 条证据", "2 branches · 5 evidence"), participants: ["赵敏", "AI"], updated_at: at("13:52"), href: "/canvas/INV-231", investigation_id: "INV-229", services: ["payment-gateway"], thumb: T_INV },
  { id: "CV-311", title: P("v3.0 发布评审：订单拆单", "v3.0 release review: order split"), kind: "review", status: "done", meta: P("4 项风险 · 全部关闭", "4 risks · all closed"), participants: ["李明", "周航"], updated_at: at("16:10", -1), href: "/canvas/CV-318", services: ["order-service"], thumb: T_REV },
  { id: "CV-305", title: P("Q4 容量评估：交易链路", "Q4 capacity: trade path"), kind: "analysis", status: "active", meta: P("4 个服务 · 预测到 12/31", "4 services · forecast to 12/31"), participants: ["张磊", "AI"], updated_at: at("11:20", -1), href: "/canvas/CV-318", services: ["checkout-api", "order-service"], thumb: T_ANA },
  { id: "CV-298", title: P("Harbor 镜像保留策略", "Harbor retention policy"), kind: "plan", status: "draft", meta: P("3 个节点 · 预计释放 1.2 TB", "3 nodes · frees ~1.2 TB"), participants: ["周航"], updated_at: at("10:00", -2), href: "/canvas/CV-318", services: ["harbor"], thumb: T_PLAN },
  { id: "INV-214", title: P("Redis 内存告警复盘", "Redis memory alert review"), kind: "investigation", status: "resolved", meta: P("3 个分支 · 已回写 Runbook", "3 branches · written back to runbook"), participants: ["王晨", "AI"], updated_at: at("18:30", -3), href: "/canvas/INV-231", investigation_id: "INV-214", services: ["redis"], thumb: T_INV },
  { id: "CV-290", title: P("数据库迁移风险评审", "DB migration risk review"), kind: "review", status: "active", meta: P("5 项风险 · 2 项待确认", "5 risks · 2 to confirm"), participants: ["周航", "李明"], updated_at: at("15:00", -4), href: "/canvas/CV-318", services: ["orders-db"], thumb: T_REV },
];

mock("GET", "/canvases", ({ query }) => {
  const kind = query.kind;
  const q = String(query.q ?? "").toLowerCase();
  const all = CANVASES.filter((c) => !q || JSON.stringify(c.title).toLowerCase().includes(q));
  const counts = { all: all.length, investigation: 0, plan: 0, review: 0, analysis: 0, custom: 0 } as Record<string, number>;
  for (const c of all) counts[c.kind]++;
  return { ...paginate(all.filter((c) => !kind || c.kind === kind), query), counts };
});

const TPL = (id: string, kind: string, zh: string, en: string, dz: string, de: string, nz: string, ne: string, is_default?: boolean) => ({ id, kind, name: P(zh, en), desc: P(dz, de), nodes: P(nz, ne), is_default });
mock("GET", "/canvas-templates", () => ({
  items: [
    TPL("tpl-inv", "investigation", "故障调查", "Incident investigation", "从告警或事件开始，按假设派生分支取证", "Start from an alert and fork branches per hypothesis", "主线 + 3 个分支", "Main + 3 branches", true),
    TPL("tpl-regress", "investigation", "发布后回归", "Post-release regression", "对比发布前后指标、日志和变更", "Compare metrics, logs and changes around a release", "主线 + 2 个分支", "Main + 2 branches"),
    TPL("tpl-storm", "investigation", "告警风暴分析", "Alert storm", "找出根因告警并给出抑制建议", "Find the root alert and suggest inhibitions", "主线 + 分组节点", "Main + group nodes"),
    TPL("tpl-inv-blank", "investigation", "空白调查", "Blank investigation", "只创建主线节点", "Just the main node", "1 个节点", "1 node"),
    TPL("tpl-plan", "plan", "压测计划", "Load-test plan", "流量模型、场景、依赖与风险、执行编排、回滚预案", "Traffic model, scenarios, risks, rollout, rollback", "6 个节点 · 执行需审批", "6 nodes · execution needs approval", true),
    TPL("tpl-scale", "plan", "扩容计划", "Scale-out plan", "容量测算、扩容步骤与回退", "Sizing, scale steps and rollback", "4 个节点", "4 nodes"),
    TPL("tpl-migrate", "plan", "迁移计划", "Migration plan", "分批迁移、双写校验与切换窗口", "Batches, dual-write checks and cutover window", "5 个节点", "5 nodes"),
    TPL("tpl-chaos", "plan", "故障演练", "Game day", "混沌实验范围、观测与止损", "Chaos scope, observation and stop conditions", "5 个节点", "5 nodes"),
    TPL("tpl-release", "review", "发布评审", "Release review", "变更范围、风险、灰度与回滚方案", "Scope, risks, canary and rollback", "4 个节点", "4 nodes", true),
    TPL("tpl-arch", "review", "架构评审", "Architecture review", "依赖、容量与单点风险", "Dependencies, capacity and single points of failure", "4 个节点", "4 nodes"),
    TPL("tpl-access", "review", "权限评审", "Access review", "新增权限与最小授权检查", "New grants and least-privilege checks", "3 个节点", "3 nodes"),
    TPL("tpl-capacity", "analysis", "容量评估", "Capacity review", "趋势、预测与余量", "Trends, forecast and headroom", "4 个节点", "4 nodes", true),
    TPL("tpl-cost", "analysis", "成本分析", "Cost analysis", "按服务拆分资源与模型用量", "Resource and model spend by service", "4 个节点", "4 nodes"),
    TPL("tpl-noise", "analysis", "告警质量分析", "Alert quality", "噪声、重复与未处理告警", "Noise, duplicates and unhandled alerts", "3 个节点", "3 nodes"),
    TPL("tpl-blank", "custom", "空白画布", "Blank canvas", "从一个主题节点开始，自由派生", "Start from one topic node and fork freely", "1 个节点", "1 node", true),
  ],
}));

mock("POST", "/canvases", ({ body }) => {
  const b = (body ?? {}) as { title?: string; kind?: string; template_id?: string; services?: string[]; goal?: string; ai_draft?: boolean };
  if (!b.title?.trim()) throw new MockError(422, "invalid", "title is required");
  // The demo always opens the load-test plan (or the investigation) so that the new page has content.
  const id = b.kind === "investigation" ? "INV-231" : "CV-318";
  return { id, href: `/canvas/${id}` };
});

const PLAN = {
  id: "CV-318",
  title: P("双十一大促压测计划", "Singles' Day load-test plan"),
  kind: "plan",
  status: "waiting_approval",
  env: "prod",
  participants: ["王晨", "周航", "AI"],
  note: P("6 个节点 · 4 个产物 · AI 起草，王晨修改了 3 处", "6 nodes · 4 artifacts · drafted by AI, 3 edits by Wang Chen"),
  proposal: { id: "PRP-93", href: "/review/PRP-88" },
  nodes: [
    { id: "goal", x: 16, y: 290, w: 230, icon: "layers", tone: "info", title: P("目标与范围", "Goal & scope"), sub: P("主线 · 由 AI 起草", "Main · drafted by AI"),
      blocks: [
        { type: "kv", items: [[P("目标", "Target"), P("36k QPS（去年峰值 × 3）", "36k QPS (last year × 3)")], [P("指标", "SLO"), "P99 < 800 ms · 5xx < 0.5%"], [P("窗口", "Window"), "11/08 02:00–04:00"]] },
        { type: "tags", items: ["checkout-api", "payment-gateway", "order-service", "inventory-worker"] },
      ] },
    { id: "traffic", x: 276, y: 24, w: 240, icon: "activity", tone: "neutral", title: P("流量模型", "Traffic model"), sub: P("分支 · 去年大促 + 近 30 天", "Branch · last sale + 30 days"),
      blocks: [
        { type: "bars", values: [6, 5, 4, 3, 3, 4, 7, 12, 16, 18, 17, 19, 21, 20, 19, 22, 26, 30, 34, 36, 33, 25, 16, 9], highlight: 19, color: "var(--c5)" },
        { type: "text", text: P("读写比 7:3 · 峰值在 20:00 · 华东流量 62%", "Read/write 7:3 · peak at 20:00 · 62% East China") },
      ] },
    { id: "scen", x: 276, y: 212, w: 240, icon: "grid", tone: "neutral", title: P("压测场景", "Scenarios"), sub: P("3 个场景 · 脚本由 AI 生成", "3 scenarios · scripts by AI"),
      blocks: [
        { type: "rows", items: [
          { badge: "A", badge_tone: "accent", text: P("下单链路", "Checkout path"), value: "36k", state: P("脚本就绪", "Ready"), state_tone: "ok" },
          { badge: "B", badge_tone: "accent", text: P("商品详情读", "Product reads"), value: "60k", state: P("脚本就绪", "Ready"), state_tone: "ok" },
          { badge: "C", badge_tone: "accent", text: P("库存扣减 · Kafka", "Stock · Kafka"), value: "8k/s", state: P("待确认", "To confirm"), state_tone: "warn" },
        ] },
        { type: "text", text: P("A：checkout → payment → order；支付通道使用挡板", "A: checkout → payment → order; payment uses a stub") },
      ] },
    { id: "risks", x: 276, y: 446, w: 240, icon: "alert", tone: "warn", title: P("依赖与风险", "Dependencies & risks"), sub: P("分支 · 4 项", "Branch · 4 items"),
      blocks: [
        { type: "rows", items: [
          { dot: "danger", text: P("支付通道限流 500 QPS → 挡板", "Payment rate limit 500 QPS → stub") },
          { dot: "warn", text: P("Kafka 分区 12 → 需扩到 24", "Kafka partitions 12 → need 24") },
          { dot: "ok", text: P("orders-db 连接上限 800 · 余量足够", "orders-db 800 connections · enough headroom") },
          { dot: "ok", text: P("库存调用已限为重试 1 次并打开熔断", "Inventory calls now retry once with the breaker on") },
        ] },
        { type: "text", text: P("引用：INC-1042 复盘、Runbook「下游超时与重试风暴」", "Refs: INC-1042 postmortem, runbook “Retry storms”") },
      ] },
    { id: "exec", x: 548, y: 40, w: 300, icon: "play", tone: "accent", title: P("执行编排", "Rollout"), sub: P("分支 · 写操作需审批", "Branch · writes need approval"),
      blocks: [
        { type: "steps", items: [
          { text: P("预发全链路 1× → 3×", "Staging full path 1× → 3×"), sub: P("11/05 02:00 · 30 分钟 · 自动", "11/05 02:00 · 30 min · automatic"), state: "done" },
          { text: P("生产 10% 流量", "Prod 10% traffic"), sub: P("11/08 02:00 · 需要王晨、张磊批准", "11/08 02:00 · needs Wang Chen and Zhang Lei"), state: "gate" },
          { text: P("生产 30% 流量", "Prod 30% traffic"), sub: P("02:20 · 上一步指标达标后", "02:20 · once the previous step meets SLO"), state: "todo" },
          { text: P("生产 60% → 100%", "Prod 60% → 100%"), sub: P("02:40 · 每步 10 分钟", "02:40 · 10 min per step"), state: "todo" },
        ] },
        { type: "callout", tone: "warn", icon: "shield", text: P("生产步骤会生成提案 PRP-93，由执行器按批准结果逐步放量。", "Prod steps become proposal PRP-93; the executor ramps up only as approved.") },
      ] },
    { id: "stop", x: 548, y: 396, w: 300, icon: "shield", tone: "danger", title: P("熔断与回滚预案", "Stop & rollback"), sub: P("分支 · 已演练 1 次", "Branch · rehearsed once"),
      blocks: [
        { type: "rows", items: [
          { badge: P("自动停止", "Auto stop"), badge_tone: "danger", text: P("5xx > 1% 或 P99 > 1.5 s，持续 1 分钟", "5xx > 1% or P99 > 1.5 s for 1 min") },
          { badge: P("回滚", "Rollback"), text: P("关闭压测流量开关 → 恢复副本数", "Turn off test traffic → restore replicas") },
          { badge: P("观测", "Watch"), text: P("Grafana「大促压测」看板 + 飞书作战群", "Grafana “Load test” board + Feishu war room") },
        ] },
      ] },
  ],
  edges: [
    { from: "goal", to: "traffic", tone: "info" }, { from: "goal", to: "scen", tone: "info" }, { from: "goal", to: "risks", tone: "info" },
    { from: "scen", to: "exec", tone: "accent", live: true }, { from: "risks", to: "exec", tone: "warn" }, { from: "exec", to: "stop", dash: true },
  ],
  artifacts: [
    { id: "a1", x: 548, y: 574, ext: "MD", tone: "info", title: P("计划文档 v2", "Plan doc v2"), sub: P("plan.md · 由画布生成", "plan.md · from canvas") },
    { id: "a2", x: 700, y: 574, ext: "CSV", tone: "ok", title: P("容量表", "Capacity sheet"), sub: P("capacity.csv · 4 个服务", "capacity.csv · 4 services") },
    { id: "a3", x: 548, y: 634, brand: "grafana", title: P("大促压测看板", "Load-test board"), sub: P("Grafana · 深链", "Grafana · deep link") },
    { id: "a4", x: 700, y: 634, icon: "shield", tone: "warn", title: P("提案 PRP-93", "Proposal PRP-93"), sub: P("等待审批", "Awaiting approval"), href: "/review/PRP-88" },
  ],
  threads: {
    exec: { context: P("分支 · 上下文 v3 · 继承主线 v4", "Branch · context v3 · inherits main v4"), turns: [
      { id: "t1", role: "user", author: "王晨", text: P("第 2 步改成 02:00 开始，审批人加上张磊。", "Start step 2 at 02:00 and add Zhang Lei as approver.") },
      { id: "t2", role: "assistant", author: "AI", model: "Claude Opus 5.5", reasoning_label: P("高", "High"), brand: "claude",
        text: P("已更新：生产 10% 放量改为 11/08 02:00，审批人为王晨、张磊。放量前会检查预发结果和「熔断与回滚预案」是否就绪。", "Updated: prod 10% now starts 11/08 02:00 with Wang Chen and Zhang Lei approving. Before ramping I will check the staging result and that “Stop & rollback” is ready.") },
    ] },
  },
};

mock("GET", "/canvases/:id", ({ params }) => {
  if (params.id.startsWith("INV-")) throw new MockError(404, "not_found", "investigation canvases are served by /investigations/{id}/canvas");
  return { ...PLAN, id: params.id };
});

mock("POST", "/canvases/:id/nodes/:node/messages", ({ body }) => {
  const b = (body ?? {}) as { text?: string };
  return {
    reply: P(
      `已记录：「${b.text ?? ""}」。我会在这个节点的上下文里更新计划；涉及生产的步骤仍会生成提案等待审批。`,
      `Noted: “${b.text ?? ""}”. I will update the plan in this node's context; production steps still become proposals for approval.`,
    ),
  };
});

mock("POST", "/canvas-templates", ({ body }) => ({ id: `tpl-${Date.now().toString(36)}`, ...(body as object) }));
