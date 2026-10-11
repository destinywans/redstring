import { mock, MockError } from "../router";
import { NOW, at } from "../time";
import { l } from "../../i18n";
import { silences as created } from "./identity";
import { rollbackProposal, rolloutExecution } from "./rollback";
import { LIFECYCLE_RULES, RULE_PENDING, alertRuleExecution, alertRuleProposal, isAlertRuleExecution, isAlertRuleProposal } from "./alertRules";

const P = (zh: string, en: string) => l(zh, en);
const hhmm = (m: number) => `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}`;

/* ---------------- inbox ---------------- */
mock("GET", "/inbox", ({ query }) => {
  const items = [
    { id: "i1", kind: "alert", tone: "danger", title: P("P1 · checkout-api 5xx 比例 8.4%", "P1 · checkout-api 5xx ratio 8.4%"), sub: P("AI 初查完成：inventory-03 先变慢，库存调用被重试放大", "AI triage: inventory-03 slowed first; retries amplified inventory calls"), at: at("14:05"), unread: true, href: "/inbox?item=i1", alert_id: "ALR-7731" },
    { id: "i2", kind: "approval", tone: "accent", title: P("审批：库存调用重试降为 1 次并打开熔断", "Approval: retry inventory calls once, breaker on"), sub: P("等待你复核 · 中风险 · INV-231", "Awaiting your review · medium risk · INV-231"), at: at("14:18"), unread: true, href: "/review/PRP-88" },
    { id: "i3", kind: "mention", tone: "neutral", title: P("李明 在 INV-231 中提到了你", "Li Ming mentioned you in INV-231"), sub: P("“只重试 1 次，偶发超时会不会直接让下单失败？”", "“With one retry, will a stray timeout fail the order?”"), at: at("14:20"), unread: true, href: "/canvas/INV-231" },
    { id: "i4", kind: "runbook", tone: "neutral", title: P("Runbook 修订待复核", "Runbook revision to review"), sub: P("「下游超时与重试风暴」v3 → v4 · 2 处修改", "“Retry storms” v3 → v4 · 2 edits"), at: at("14:25"), unread: false, href: "/runbooks/rb-retry-storm" },
    { id: "i5", kind: "report", tone: "neutral", title: P("故障报告草稿已生成", "Incident report draft ready"), sub: P("checkout-api 5xx 上升 · 等你确认后发布", "checkout-api 5xx · publish after you confirm"), at: at("14:25"), unread: false, href: "/executions/EXE-51" },
    { id: "i6", kind: "alert", tone: "warn", title: P("P2 · inventory-03 磁盘 IO 等待 180 ms", "P2 · inventory-03 disk IO wait 180 ms"), sub: P("超过阈值 100 ms · 未认领", "Above the 100 ms threshold · unclaimed"), at: at("14:02"), unread: false, href: "/inbox?item=i6", alert_id: "ALR-7729" },
    { id: "i7", kind: "alert", tone: "neutral", title: P("P3 · inventory-worker Kafka 积压", "P3 · inventory-worker Kafka lag"), sub: P("王晨 已认领 · 已静默至 16:00", "Claimed by Wang Chen · silenced until 16:00"), at: at("22:10", -1), unread: false, href: "/inbox?item=i7", alert_id: "ALR-7702" },
    { id: "i8", kind: "access", tone: "neutral", title: P("李明 申请 inventory-worker 操作权限", "Li Ming requests inventory-worker access"), sub: P("交易平台 · 有效期 7 天", "Trading · valid 7 days"), at: at("18:42", -1), unread: false, href: "/settings/members" },
    { id: "assignment:n1", kind: "assignment", assignment_kind: "incident", tone: "neutral", title: P("INC-1042 · checkout-api 5xx 升高", "INC-1042 · checkout-api 5xx rising"), sub: P("分配给你的事件", "Incident assigned to you"), at: at("14:10"), unread: true, href: "/incidents/INC-1042" },
    { id: "assignment:n2", kind: "assignment", assignment_kind: "proposal", tone: "neutral", title: P("PRP-88 · 库存调用重试降为 1 次", "PRP-88 · retry inventory calls once"), sub: P("分配给你的提案", "Proposal assigned to you"), at: at("14:21"), unread: false, href: "/review/PRP-88" },
    { id: "i9", kind: "agent", tone: "warn", title: P("Agent 治理：调查助手 v13 未通过评测门禁", "Agent governance: Investigator v13 failed an eval gate"), sub: P("成本上升 14%，已阻止灰度扩大到 50%", "Cost +14%; canary blocked at 20%"), at: at("17:05", -1), unread: false, href: "/agents/evals" },
  ];
  const tab = String(query.tab ?? "all");
  const filtered = tab === "mine" ? items.filter((i) => ["approval", "report", "runbook", "access", "assignment"].includes(i.kind)) : tab === "mentions" ? items.filter((i) => i.kind === "mention") : items;
  return { items: filtered, counts: { all: 12, mine: 4, mentions: 2 } };
});

const alertSeries = [0.3, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 0.3, 0.2, 0.2, 0.3, 0.2, 2.1, 5.6, 7.9, 8.4, 8.1, 8.6, 8.3, 8.4];
mock("GET", "/alerts/:id", ({ params }) => ({
  id: params.id,
  severity: "P1",
  title: P("checkout-api 5xx 比例 8.4%", "checkout-api 5xx ratio 8.4%"),
  state: "firing",
  source: "Alertmanager",
  env: "prod",
  since: P("14:05 起，已持续 26 分钟", "since 14:05, firing for 26 min"),
  rule: P("checkout-api 5xx 比例过高", "checkout-api high 5xx ratio"),
  triage: {
    summary: P("5xx 在 14:05 开始上升，离 14:03 发布 v2.8.1 只有 2 分钟，但发布没有改动库存调用；inventory-03 在 14:02 先变慢（P99 80 → 350 ms），随后库存调用从 1.2k/s 涨到 3.9k/s，而下单量持平。[1][2][3]", "5xx started rising at 14:05, 2 minutes after v2.8.1 shipped, but the release does not touch inventory calls; inventory-03 slowed first at 14:02 (P99 80 → 350 ms), then inventory calls went from 1.2k/s to 3.9k/s while orders stayed flat.[1][2][3]"),
    confidence: "high",
    agent: P("调查助手 v12", "Investigator v12"),
    duration_s: 48,
    tools: 4,
    investigation_id: "INV-231",
    proposal_id: "PRP-88",
  },
  series: { labels: Array.from({ length: 20 }, (_, i) => hhmm(13 * 60 + 35 + i * 3)), values: alertSeries, threshold: 1, release_at: 12 },
  labels: [{ k: "alertname", v: "HighErrorRate" }, { k: "service", v: "checkout-api" }, { k: "env", v: "prod" }, { k: "severity", v: "P1" }, { k: "team", v: "trade" }],
  summary: P("5xx 比例 8.4%，阈值 1%", "5xx ratio 8.4%, threshold 1%"),
  runbook: { id: "rb-retry-storm", title: P("下游超时与重试风暴 v3", "Downstream timeouts and retry storms v3") },
  notifications: [
    { time: "14:05:30", channel: "feishu", text: P("飞书 #sre-oncall · 首次通知（5 条合并为 1 条）", "Feishu #sre-oncall · first notice (5 merged)"), done: true },
    { time: "14:06:10", channel: "sms", text: P("短信 王晨 · P1 同时短信", "SMS Wang Chen · P1 escalation"), done: true },
    { time: "14:10:30", channel: "feishu", text: P("飞书 · 组内新增 1 条告警", "Feishu · 1 new alert in group"), done: true },
    { time: "18:05", channel: "repeat", text: P("下次重复提醒（仍未恢复时）", "Next repeat (if still firing)"), done: false },
  ],
  grouping: {
    group: "checkout-api / HighErrorRate · 5",
    inhibited: P("本组抑制了 3 条下游超时告警", "3 downstream timeout alerts inhibited"),
    incident: { id: "INC-1042", label: P("INC-1042 · 进行中", "INC-1042 · active") },
    change: { id: "8812", label: P("流水线 #8812 · v2.8.1 · 14:03", "Pipeline #8812 · v2.8.1 · 14:03") },
  },
}));
mock("POST", "/alerts/:id/claim", () => ({ ok: true }));

/* ---------------- alert groups ---------------- */
const inst = (pod: string, value: string, state: string, duration: string, vals: number[], pending?: { pct: number; text: string }) => ({ id: pod, value, state, duration, series: vals, pending });
const GROUPS = [
  { id: "g-checkout-5xx", severity: "P1", service: "checkout-api", labels: [{ k: "service", v: "checkout-api" }, { k: "alertname", v: "HighErrorRate" }], count: 5, state: "firing", since: P("14:05 起 · 最近 14:30", "since 14:05 · last 14:30"), notify: { channel: "feishu", text: P("已通知 2 次", "Notified 2×") }, badges: [{ text: P("抑制了 3 条", "Inhibits 3"), tone: "neutral" }], owner: "王晨",
    instances: [
      inst("checkout-api-7f9c-1", "8.9%", "firing", P("26 分钟", "26 min") as never, [0.2, 0.3, 2, 6, 8, 8.9, 8.7, 8.9]),
      inst("checkout-api-7f9c-2", "8.1%", "firing", P("26 分钟", "26 min") as never, [0.3, 0.2, 2, 5, 8, 8.1, 8.2, 8.1]),
      inst("checkout-api-7f9c-3", "7.6%", "firing", P("25 分钟", "25 min") as never, [0.2, 0.2, 1, 5, 7, 7.6, 7.4, 7.6]),
      inst("checkout-api-7f9c-4", "9.2%", "firing", P("24 分钟", "24 min") as never, [0.2, 0.4, 3, 6, 9, 9.2, 9.0, 9.2]),
      inst("checkout-api-7f9c-5", "1.4%", "pending", P("3 分钟", "3 min") as never, [0.2, 0.2, 0.3, 0.2, 0.6, 1.1, 1.3, 1.4], { pct: 60, text: P("3 / 5 分钟", "3 / 5 min") as never }),
    ] },
  { id: "g-inventory-p99", severity: "P1", service: "inventory-service", labels: [{ k: "service", v: "inventory-service" }, { k: "alertname", v: "HighLatencyP99" }], count: 1, state: "firing", since: P("14:03 起", "since 14:03"), notify: { channel: "feishu", text: P("已通知 1 次", "Notified 1×") }, badges: [], owner: "周航", instances: [inst("inventory-service-03", "350 ms", "firing", P("28 分钟", "28 min") as never, [80, 82, 96, 214, 318, 344, 351, 350])] },
  { id: "g-disk-io", severity: "P2", service: "inventory-service", labels: [{ k: "host", v: "inventory-03" }, { k: "alertname", v: "NodeDiskIOWaitHigh" }], count: 1, state: "firing", since: P("14:02 起", "since 14:02"), notify: { channel: "dingtalk", text: P("已通知 1 次", "Notified 1×") }, badges: [{ text: P("未认领", "Unclaimed"), tone: "warn" }], owner: "周航",
    instances: [inst("inventory-03 · nvme0n1", "180 ms", "firing", P("29 分钟", "29 min") as never, [2, 2, 3, 40, 120, 170, 182, 180])] },
  { id: "g-inventory-lag", severity: "P2", service: "inventory-worker", labels: [{ k: "service", v: "inventory-worker" }, { k: "alertname", v: "KafkaConsumerLag" }], count: 1, state: "silenced", since: P("12:10 起", "since 12:10"), notify: { text: P("王晨 处理中", "Wang Chen working") }, badges: [], owner: "钱多", instances: [inst("inventory-worker-0", "12k", "firing", P("2 小时", "2 h") as never, [1, 2, 3, 5, 6, 8, 10, 12])] },
  { id: "g-order-cpu", severity: "P2", service: "order-service", labels: [{ k: "service", v: "order-service" }, { k: "alertname", v: "HighCPU" }], count: 1, state: "pending", since: P("2 / 10 分钟", "2 / 10 min"), notify: { text: P("满 10 分钟才触发", "Fires after 10 min") }, badges: [], instances: [inst("order-service-2", "86%", "pending", P("2 分钟", "2 min") as never, [60, 62, 70, 78, 82, 86, 85, 86], { pct: 20, text: P("2 / 10 分钟", "2 / 10 min") as never })] },
  { id: "g-checkout-restart", severity: "P3", service: "checkout-api", labels: [{ k: "service", v: "checkout-api" }, { k: "alertname", v: "PodRestarts" }], count: 1, state: "firing", since: P("14:19 起", "since 14:19"), notify: { text: P("未认领", "Unclaimed") }, badges: [{ text: P("未认领", "Unclaimed"), tone: "warn" }], instances: [inst("checkout-api-7f9c-6", "4", "firing", P("12 分钟", "12 min") as never, [0, 0, 1, 1, 2, 3, 4, 4])] },
  { id: "g-cert", severity: "P3", service: "api-gateway", labels: [{ k: "service", v: "api-gateway" }, { k: "alertname", v: "CertExpiringSoon" }], count: 1, state: "firing", since: P("2 天", "2 days"), notify: { text: P("每日提醒", "Daily reminder") }, badges: [], owner: "孙悦", instances: [inst("gateway-cert", P("29 天", "29 d") as never, "firing", P("2 天", "2 d") as never, [31, 31, 30, 30, 30, 29, 29, 29])] },
  { id: "g-downstream", severity: "P3", service: "checkout-api", labels: [{ k: "service", v: "checkout-api" }, { k: "alertname", v: "DownstreamTimeout" }], count: 3, state: "silenced", since: P("被 HighErrorRate 抑制", "Inhibited by HighErrorRate"), notify: { text: P("不单独通知", "Not notified separately") }, badges: [{ text: P("已抑制", "Inhibited"), tone: "neutral" }], inhibited: true, instances: [] },
];
mock("GET", "/alert-groups", ({ query }) => {
  const st = String(query.state ?? "firing");
  const showInhibited = query.show_inhibited !== "false";
  let items = GROUPS.filter((g) => (st === "all" ? true : st === "firing" ? g.state === "firing" || (showInhibited && g.inhibited) : g.state === st));
  if (!showInhibited) items = items.filter((g) => !g.inhibited);
  const q = String(query.q ?? "").toLowerCase();
  if (q) items = items.filter((g) => g.labels.some((x) => `${x.k}=${x.v}`.includes(q) || x.v.includes(q)));
  return { items, counts: { firing: 6, pending: 1, silenced: 2, resolved: 14 } };
});
mock("GET", "/alert-groups/:id/policy", () => ({
  group_by: ["service", "alertname"],
  route: P("来自路由「生产 · 交易平台」", "From route “prod · trading”"),
  timing: [
    { key: "group_wait", label: P("首次等待", "Group wait"), value: P("30 秒", "30 s"), help: P("同组告警先等 30 秒合并，再发第一条通知", "Wait 30 s to merge the group before the first notice") },
    { key: "group_interval", label: P("组内间隔", "Group interval"), value: P("5 分钟", "5 min"), help: P("组里出现新告警时，最快 5 分钟再通知一次", "Notify again at most every 5 min when the group changes") },
    { key: "repeat_interval", label: P("重复间隔", "Repeat interval"), value: P("4 小时", "4 h"), help: P("一直未恢复时，每 4 小时重复提醒（周期告警）", "Repeat every 4 h while still firing") },
  ],
  timeline: [
    { time: "14:05:30", channel: "feishu", text: P("首次通知 · 5 条合并为 1 条", "First notice · 5 merged"), done: true },
    { time: "14:06:10", channel: "sms", text: P("短信 王晨（P1 升级）", "SMS Wang Chen (P1 escalation)"), done: true },
    { time: "14:10:30", channel: "feishu", text: P("组内新增 1 条告警", "1 new alert in group"), done: true },
    { time: "18:05", channel: "repeat", text: P("下次重复提醒", "Next repeat"), done: false },
  ],
  inhibition: [
    P("本组触发期间，同一服务的 DownstreamTimeout 被抑制（3 条），不单独通知。", "While this group fires, DownstreamTimeout on the same service is inhibited (3) and not notified."),
    P("实例 -5 处于待定：条件已满足 3 分钟，满 5 分钟才会触发。", "Instance -5 is pending: condition met for 3 min, fires at 5 min."),
  ],
}));
mock("POST", "/alert-groups/bulk", ({ body }) => ({ ok: true, ...(body as object) }));

const SERVICES: Record<string, unknown> = {
  "checkout-api": { id: "checkout-api", name: "checkout-api", tier: "T0", team: P("交易平台", "Trading"), env: "prod", status: "danger", kpis: [{ k: "5xx", v: "8.4%", tone: "danger" }, { k: "P99", v: "1.6 s", tone: "warn" }, { k: "SLO", v: "99.87%" }], owner: "李明", oncall: "王晨", change: "v2.8.1 · 14:03" },
  "inventory-service": { id: "inventory-service", name: "inventory-service", tier: "T0", team: P("交易平台", "Trading"), env: "prod", status: "danger", kpis: [{ k: "P99", v: "350 ms", tone: "danger" }, { k: P("调用", "Calls"), v: "3.9k/s", tone: "danger" }, { k: P("超时", "Timeouts"), v: "31%", tone: "danger" }], owner: "周航", oncall: "周航", change: "v5.3.0 · 9/16" },
  "orders-db": { id: "orders-db", name: "orders-db", tier: "T0", team: P("数据平台", "Data"), env: "prod", status: "ok", kpis: [{ k: P("连接", "Conns"), v: "212/800" }, { k: P("等待", "Wait"), v: "6 ms" }, { k: P("慢查询", "Slow"), v: "2/min" }], owner: "周航", oncall: "周航", change: P("无", "none") },
  "payment-gateway": { id: "payment-gateway", name: "payment-gateway", tier: "T0", team: P("支付", "Payments"), env: "prod", status: "ok", kpis: [{ k: "5xx", v: "0.1%" }, { k: "P99", v: "180 ms" }, { k: P("熔断", "Breaker"), v: P("关闭", "closed") }], owner: "赵凯", oncall: "赵凯", change: P("v3.2.0 · 昨天", "v3.2.0 · yesterday") },
  "inventory-worker": { id: "inventory-worker", name: "inventory-worker", tier: "T1", team: P("履约", "Fulfilment"), env: "prod", status: "warn", kpis: [{ k: P("积压", "Lag"), v: "12k", tone: "warn" }, { k: P("消费", "Rate"), v: "380/s" }, { k: P("延迟", "Delay"), v: "32 s", tone: "warn" }], owner: "钱多", oncall: "王晨", change: "v1.9.4 · 9/18" },
  "order-service": { id: "order-service", name: "order-service", tier: "T1", team: P("交易平台", "Trading"), env: "prod", status: "ok", kpis: [{ k: "5xx", v: "0.1%" }, { k: "P99", v: "142 ms" }, { k: "CPU", v: "86%", tone: "warn" }], owner: "李明", oncall: "王晨", change: "v3.2.4 · 3d" },
  "api-gateway": { id: "api-gateway", name: "api-gateway", tier: "T0", team: P("平台", "Platform"), env: "prod", status: "ok", kpis: [{ k: P("请求", "RPS"), v: "2.0k/s" }, { k: P("证书", "Cert"), v: P("29 天", "29 d"), tone: "warn" }, { k: "5xx", v: "0.1%" }], owner: "孙悦", oncall: "张磊", change: P("无", "none") },
};
mock("GET", "/services/:id/peek", ({ params }) => {
  const s = SERVICES[params.id];
  if (!s) throw new MockError(404, "not_found", "service not found");
  return s;
});
const PEOPLE: Record<string, unknown> = {
  王晨: { id: "u-wangchen", name: "王晨", role: P("SRE · 交易平台", "SRE · Trading"), oncall: P("值班中至 20:00", "On call until 20:00"), load: P("处理 2 个事件", "On 2 incidents") },
  周航: { id: "u-zhouhang", name: "周航", role: P("DBA · 数据平台", "DBA · Data"), load: P("处理 1 个事件", "On 1 incident") },
  赵凯: { id: "u-zhaokai", name: "赵凯", role: P("SRE · 支付", "SRE · Payments"), oncall: P("值班中至 22:00", "On call until 22:00"), load: P("空闲", "Available") },
  钱多: { id: "u-qianduo", name: "钱多", role: P("后端 · 履约", "Backend · Fulfilment"), load: P("空闲", "Available") },
  孙悦: { id: "u-sunyue", name: "孙悦", role: P("SRE · 平台", "SRE · Platform"), load: P("处理 0 个事件", "No incidents") },
  李明: { id: "u-liming", name: "李明", role: P("后端 · 交易平台", "Backend · Trading"), load: P("处理 1 个事件", "On 1 incident") },
};
mock("GET", "/people/:name/peek", ({ params }) => PEOPLE[params.name] ?? { id: params.name, name: params.name, role: "—", load: "—" });

/* ---------------- incidents ---------------- */
const INCIDENTS = [
  { id: "INC-1042", severity: "P1", title: P("checkout-api 5xx 上升", "checkout-api 5xx spike"), status: "active", duration: P("已持续 26 分钟", "26 min so far"), commander: "王晨" },
  { id: "INC-1041", severity: "P2", title: P("payment-gateway 延迟升高", "payment-gateway latency up"), status: "mitigated", duration: P("1 小时 12 分钟", "1 h 12 min"), commander: "赵凯" },
  { id: "INC-1037", severity: "P2", title: P("order-service 慢查询导致超时", "order-service slow queries"), status: "resolved", duration: P("昨天 · 48 分钟", "Yesterday · 48 min"), commander: "周航" },
  { id: "INC-1033", severity: "P1", title: P("search-api 发布后可用性下降", "search-api availability drop"), status: "resolved", duration: P("9/18 · 35 分钟", "9/18 · 35 min"), commander: "张磊" },
  { id: "INC-1030", severity: "P3", title: P("notification-svc 短信通道失败", "notification-svc SMS failures"), status: "resolved", duration: P("9/16 · 2 小时", "9/16 · 2 h"), commander: "陈雨" },
  { id: "INC-1028", severity: "P2", title: P("user-center 登录延迟抖动", "user-center login latency jitter"), status: "resolved", duration: P("9/15 · 25 分钟", "9/15 · 25 min"), commander: "王晨" },
];
mock("GET", "/incidents", ({ query }) => {
  const st = String(query.status ?? "active");
  const items = INCIDENTS.filter((i) => (st === "all" ? true : st === "active" ? i.status !== "resolved" : i.status === "resolved"));
  return { items, counts: { active: 2, resolved: 4, all: 6 } };
});
mock("GET", "/incidents/:id", ({ params }) => {
  const base = INCIDENTS.find((i) => i.id === params.id) ?? INCIDENTS[0];
  return {
    ...base,
    stage: base.status === "resolved" ? 4 : base.status === "mitigated" ? 3 : 2,
    timeline: [
      { time: "14:03", actor: { kind: "brand", name: "gitlab" }, text: P("GitLab 流水线 #8812 发布 checkout-api v2.8.1 到生产", "GitLab pipeline #8812 released checkout-api v2.8.1 to prod"), sub: P("经 Argo CD 同步 · 触发人 李明", "Synced via Argo CD · by Li Ming") },
      { time: "14:05", actor: { kind: "brand", name: "prometheus" }, text: P("告警触发：5xx 比例 8.4%（阈值 1%）", "Alert fired: 5xx 8.4% (threshold 1%)"), sub: P("分组 5 条 · 抑制 3 条下游告警", "5 grouped · 3 downstream inhibited") },
      { time: "14:06", actor: { kind: "ai", name: "AI" }, text: P("AI 自动初查完成，建立调查 INV-231", "AI triage done; opened INV-231"), sub: P("模板：发布后错误率上升 · 3 条证据", "Template: post-release errors · 3 evidence") },
      { time: "14:08", actor: { kind: "person", name: "王晨" }, text: P("王晨 认领并担任指挥", "Wang Chen claimed and is commanding") },
      { time: "14:12", actor: { kind: "brand", name: "feishu" }, text: P("已通知 #trade-incident 群与业务负责人", "Notified #trade-incident and business owners"), sub: P("每 15 分钟同步一次进展", "Updates every 15 min") },
      { time: "14:18", actor: { kind: "ai", name: "AI" }, text: P("AI 提出提案：库存调用重试降为 1 次并打开熔断", "AI proposed: retry inventory calls once and turn the breaker on"), sub: P("中风险 · 等待复核", "Medium risk · awaiting review") },
      { time: "14:20", actor: { kind: "person", name: "李明" }, text: P("李明：确认风暴前库存调用超时率只有 0.2%", "Li Ming: confirmed inventory timeouts were 0.2% before the storm") },
    ],
    impact: { peak: "8.4%", budget_used: "12%", budget_left: 38, note: P("月度错误预算剩余 38% · 影响下单接口", "38% monthly error budget left · order API affected"), series: [0.2, 0.3, 2.1, 5.6, 7.9, 8.4, 8.1, 8.6, 8.3, 8.4, 6.2, 3.1, 1.1, 0.4, 0.3, 0.2] },
    roles: [
      { role: P("指挥", "Commander"), name: "王晨" },
      { role: P("技术负责人", "Tech lead"), name: "李明" },
      { role: P("对外沟通", "Comms"), name: "赵凯" },
      { role: P("AI 助手", "AI assistant"), name: P("调查助手 v12", "Investigator v12"), ai: true },
    ],
    links: [
      { kind: "alert_group", label: "checkout-api / HighErrorRate", href: "/alerts" },
      { kind: "investigation", label: P("INV-231 · 4 个分支", "INV-231 · 4 branches"), href: "/canvas/INV-231" },
      { kind: "proposal", label: P("库存调用重试降为 1 次 · 待复核", "Retry inventory once · awaiting review"), href: "/review/PRP-88" },
      { kind: "change", label: "GitLab #8812", href: "/changes" },
    ],
  };
});
mock("POST", "/incidents/:id/timeline", ({ body }) => ({ ok: true, ...(body as object) }));
// D11 §11.3: changes around an incident's start. One incident per coverage case (sample data).
const SHA = (short: string) => `${short}${"0".repeat(40 - short.length)}`;
const INCIDENT_CHANGES: Record<string, { service: string; start: string; gitlab: string; kubernetes: string; items: (min: number) => Record<string, unknown>[] }> = {
  "INC-1042": {
    service: "checkout-api", start: at("14:05"), gitlab: "complete", kubernetes: "history_limited",
    items: () => [
      { kind: "gitlab_deployment", id: "gitlab:deployment:4:103", offset_seconds: -120, status: "success", sha: SHA("a3f9c21"), short_sha: "a3f9c21", ref: "main", pipeline_id: 8812, merge_request: { iid: 41, title: P("库存调用只重试 1 次", "Retry inventory calls once") }, kubernetes_revision: "7", web_url: "https://gitlab.lab.internal/aiops-lab/checkout-api/-/environments/1/deployments/3", href: "/changes?service_id=checkout-api&range=24h&focus=gitlab:deployment:4:103" },
      { kind: "kubernetes_revision", id: "kubernetes:aiops-lab/checkout-api:7", offset_seconds: -90, revision: "7", change_cause: "gitlab pipeline 8812", gitlab_deployment_id: 103 },
      { kind: "kubernetes_revision", id: "kubernetes:aiops-lab/checkout-api:8", offset_seconds: 540, revision: "8", change_cause: "lab drill bad-release", gitlab_deployment_id: null },
    ],
  },
  "INC-1041": {
    service: "payment-gateway", start: at("12:50"), gitlab: "unavailable", kubernetes: "history_limited",
    items: () => [{ kind: "kubernetes_revision", id: "kubernetes:pay/payment-gateway:14", offset_seconds: -360, revision: "14", change_cause: null, gitlab_deployment_id: null }],
  },
  "INC-1037": { service: "order-service", start: at("16:20", -1), gitlab: "not_configured", kubernetes: "history_limited", items: () => [] },
  "INC-1033": { service: "search-api", start: at("10:30", -23), gitlab: "complete", kubernetes: "history_limited", items: () => [] },
  "INC-1030": {
    service: "notification-svc", start: at("09:10", -25), gitlab: "complete", kubernetes: "not_configured",
    // Only the 2-hour window reaches the earlier failed deployment.
    items: (min) => [
      ...(min > 60 ? [{ kind: "gitlab_deployment", id: "gitlab:deployment:9:61", offset_seconds: -5280, status: "failed", sha: SHA("77c0e4b"), short_sha: "77c0e4b", ref: "main", pipeline_id: 4410, merge_request: { iid: 18, title: P("短信通道切到备用供应商", "Switch SMS to the backup provider") }, kubernetes_revision: null, web_url: null, href: "/changes?service_id=notification-svc&range=24h&focus=gitlab:deployment:9:61" }] : []),
      { kind: "gitlab_deployment", id: "gitlab:deployment:9:62", offset_seconds: -1500, status: "success", sha: SHA("9b1d3aa"), short_sha: "9b1d3aa", ref: "main", pipeline_id: 4412, merge_request: { iid: 19, title: P("短信模板加签名", "Sign SMS templates") }, kubernetes_revision: null, web_url: null, href: "/changes?service_id=notification-svc&range=24h&focus=gitlab:deployment:9:62" },
    ],
  },
  "INC-1028": {
    service: "user-center", start: at("15:40", -26), gitlab: "no_authorized_target", kubernetes: "history_limited",
    items: () => [{ kind: "kubernetes_revision", id: "kubernetes:account/user-center:31", offset_seconds: -720, revision: "31", change_cause: "rollout restart", gitlab_deployment_id: null }],
  },
};
mock("GET", "/incidents/:id/changes", ({ params, query }) => {
  const c = INCIDENT_CHANGES[params.id];
  if (!c) throw new MockError(404, "not_found", "incident not found");
  const min = String(query.before ?? "60m") === "120m" ? 120 : 60;
  const start = Date.parse(c.start);
  const end = Math.min(start + 15 * 60_000, NOW);
  const iso = (t: number) => new Date(t).toISOString();
  const items = c.items(min).map((i) => ({ ...i, at: iso(start + (i.offset_seconds as number) * 1000), basis: "time_adjacent" }));
  return {
    incident_id: params.id, service_id: c.service, environment_id: "prod", alert_started_at: c.start,
    window: { start_at: iso(start - min * 60_000), end_at: iso(end) },
    items: items.filter((i) => Date.parse(i.at) >= start - min * 60_000 && Date.parse(i.at) <= end),
    coverage: { gitlab: c.gitlab, kubernetes: c.kubernetes },
    note: "time_adjacent_not_causal",
    unavailable: c.gitlab === "unavailable" ? { "coverage.gitlab": "unavailable" } : {},
  };
});


/* ---------------- alert rules ---------------- */
const RULES = [
  { id: "r-5xx", folder: P("交易平台 / checkout-api", "Trading / checkout-api"), name: P("5xx 比例过高", "High 5xx ratio"), state: "firing" },
  { id: "r-p99", folder: P("交易平台 / checkout-api", "Trading / checkout-api"), name: P("P99 超过 SLO", "P99 above SLO"), state: "ok" },
  { id: "r-retry", folder: P("交易平台 / checkout-api", "Trading / checkout-api"), name: P("下游重试占比过高", "High downstream retry ratio"), state: "firing" },
  { id: "r-restart", folder: P("交易平台 / checkout-api", "Trading / checkout-api"), name: P("实例重启频繁", "Frequent restarts"), state: "ok" },
  // GitOps-managed rules (D11 §11.6), one per lifecycle state.
  ...LIFECYCLE_RULES.map((r) => ({ ...r, folder: P("规则仓库 / checkout-api", "Rule repository / checkout-api"), schedule: undefined })),
  { id: "r-pay-p99", folder: P("交易平台 / payment-gateway", "Trading / payment-gateway"), name: P("P99 超过 SLO", "P99 above SLO"), state: "ok" },
  { id: "r-pay-cb", folder: P("交易平台 / payment-gateway", "Trading / payment-gateway"), name: P("支付回调失败率", "Payment callback failures"), state: "ok" },
  { id: "r-node", folder: P("平台基础", "Platform"), name: P("节点 NotReady", "Node NotReady"), state: "ok" },
  { id: "r-disk", folder: P("平台基础", "Platform"), name: P("磁盘使用率 > 85%", "Disk usage > 85%"), state: "pending" },
  { id: "r-diskio", folder: P("平台基础", "Platform"), name: P("磁盘 IO 等待 > 100 ms", "Disk IO wait > 100 ms"), state: "firing" },
  { id: "r-cert", folder: P("周期规则", "Scheduled"), name: P("证书 30 天内到期", "Certificate expires in 30 days"), state: "ok", schedule: P("每天 09:00", "Daily 09:00") },
  { id: "r-diskpred", folder: P("周期规则", "Scheduled"), name: P("磁盘 7 天内写满预测", "Disk full in 7 days (forecast)"), state: "ok", schedule: P("每 6 小时", "Every 6 h") },
];
mock("GET", "/alert-rules", () => ({ items: RULES, counts: { all: 27, firing: 4, scheduled: 2 }, empty_reason: null, unavailable: {}, pending: RULE_PENDING }));
mock("GET", "/alert-rules/:id", ({ params }) => {
  const r = RULES.find((x) => x.id === params.id) ?? RULES[0];
  return {
    ...r,
    datasource: P("Prometheus · 生产", "Prometheus · prod"),
    query_ref: "error_rate",
    expr: 'sum(rate(http_requests_total{service="checkout-api", code=~"5.."}[5m]))\n  / sum(rate(http_requests_total{service="checkout-api"}[5m]))',
    preview: { values: [0.2, 0.3, 0.2, 0.3, 0.2, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 0.2, 0.9, 2.4, 5.1, 7.8, 8.4, 8.2, 8.6, 8.3, 8.4, 8.5], threshold: 1 },
    condition: { op: ">", value: 1, unit: "%", for_minutes: 5, recover_minutes: 2 },
    evaluation: { mode: r.schedule ? "cron" : "continuous", interval: "30s", cron: "0 9 * * *", no_data: P("视为正常", "Treat as OK") },
    labels: [{ k: "severity", v: "P1" }, { k: "team", v: "trade" }, { k: "service", v: "checkout-api" }],
    annotation: P("5xx 比例 {value}，阈值 1%", "5xx ratio {value}, threshold 1%"),
    runbook: P("发布回滚规范 v5", "Release rollback v5"),
    route_path: [P("生产", "prod"), P("交易平台", "trading"), "P1"],
    ai_triage: { enabled: true, template: P("发布后错误率上升", "Post-release errors") },
    last_eval: P("最近评估 14:31:00 · 用时 42 ms", "Last evaluated 14:31:00 · 42 ms"),
  };
});
mock("POST", "/alert-rules/:id/dry-run", () => ({ would_fire: 3, windows: [P("14:05 – 14:24", "14:05 – 14:24")] }));
mock("PUT", "/alert-rules/:id", ({ body }) => body);

/* ---------------- routing ---------------- */
mock("GET", "/notification-routes", () => ({
  root: {
    id: "root", title: P("根路由 · 默认", "Root · default"), matchers: [{ k: "*", v: P("所有告警", "all alerts") }], receivers: [{ channel: "feishu", target: "#sre-alerts" }], timing: ["30s", "5m", "4h"], hits: 214,
    children: [
      { id: "prod", title: P("生产", "Production"), matchers: [{ k: "env", v: "prod" }], receivers: [], timing: ["30s", "5m", "4h"], hits: 168, children: [
        { id: "trade", title: P("交易平台", "Trading"), matchers: [{ k: "env", v: "prod" }, { k: "team", v: "trade" }], receivers: [{ channel: "feishu", target: "#sre-oncall" }, { channel: "sms", target: P("短信", "SMS") }], timing: ["30s", "5m", "4h"], hits: 42, children: [
          { id: "p1", title: P("P1 · 电话值班", "P1 · phone on-call"), matchers: [{ k: "severity", v: "P1" }], receivers: [{ channel: "phone", target: P("电话 · 当前值班", "Phone · on-call") }], timing: ["0s", "5m", "1h"], hits: 6, continue: true, children: [] },
        ] },
        { id: "pay", title: P("支付", "Payments"), matchers: [{ k: "env", v: "prod" }, { k: "team", v: "payment" }], receivers: [{ channel: "dingtalk", target: P("支付值班群", "Payments on-call") }], timing: ["30s", "5m", "4h"], hits: 51, children: [] },
        { id: "platform", title: P("平台", "Platform"), matchers: [{ k: "env", v: "prod" }, { k: "team", v: "platform" }], receivers: [{ channel: "slack", target: "#platform-alerts" }], timing: ["1m", "10m", "12h"], hits: 75, children: [] },
      ] },
      { id: "staging", title: P("预发", "Staging"), matchers: [{ k: "env", v: "staging" }], receivers: [{ channel: "email", target: P("邮件摘要 · 每小时", "Hourly email digest") }], timing: ["5m", "1h", "24h"], hits: 46, children: [] },
    ],
  },
}));
mock("POST", "/notification-routes/test", ({ body }) => {
  const labels = String((body as { labels: string }).labels);
  const p1 = labels.includes("P1");
  const trade = labels.includes("trade");
  return {
    path: [P("根路由", "Root"), ...(labels.includes("prod") ? [P("生产", "Production")] : []), ...(trade ? [P("交易平台", "Trading")] : []), ...(trade && p1 ? [P("P1 · 电话值班", "P1 · phone")] : [])],
    receivers: trade ? (p1 ? P("飞书 #sre-oncall、短信、电话", "Feishu #sre-oncall, SMS, phone") : P("飞书 #sre-oncall、短信", "Feishu #sre-oncall, SMS")) : P("飞书 #sre-alerts", "Feishu #sre-alerts"),
  };
});

/* ---------------- silences & inhibitions ---------------- */
const SILENCES = [
  { id: "s1", matchers: [{ k: "service", v: "inventory-worker" }, { k: "alertname", v: "KafkaConsumerLag" }], state: "active", starts_at: at("14:00"), ends_at: at("16:00"), remaining: { pct: 26, text: P("1 时 29 分", "1 h 29 m") }, affects: 1, creator: "王晨", reason: P("正在扩容消费者", "Scaling consumers") },
  { id: "s2", matchers: [{ k: "cluster", v: "prod-sh-1" }, { k: "node", v: "node-7" }], state: "active", starts_at: at("13:30"), ends_at: at("15:30"), remaining: { pct: 52, text: P("58 分", "58 m") }, affects: 4, creator: "周航", reason: P("节点内核升级", "Node kernel upgrade") },
  { id: "s3", matchers: [{ k: "env", v: "staging" }], state: "planned", starts_at: at("22:00"), ends_at: at("02:00", 1), affects: 0, creator: "张磊", reason: P("预发压测维护窗口", "Staging load-test window") },
  { id: "s4", matchers: [{ k: "alertname", v: "Watchdog" }], state: "active", starts_at: at("00:00"), ends_at: at("00:00", 1), affects: 1, creator: P("系统", "System"), reason: P("心跳告警不通知", "Heartbeat alerts are not notified") },
  { id: "s5", matchers: [{ k: "service", v: "search-api" }, { k: "alertname", v: "HighLatencyP99" }], state: "expired", starts_at: at("10:00", -22), ends_at: at("12:00", -22), affects: 3, creator: "李明", reason: P("索引重建", "Index rebuild") },
];
mock("GET", "/silences", ({ query }) => {
  const st = String(query.state ?? "active");
  const extra = created.map((c) => ({ id: c.id, matchers: [{ k: "service", v: "checkout-api" }], state: "active", starts_at: new Date(NOW).toISOString(), ends_at: new Date(NOW + 3600_000).toISOString(), remaining: { pct: 2, text: P("1 小时", "1 h") }, affects: 3, creator: "王晨", reason: c.comment ?? "" }));
  const all = [...extra, ...SILENCES];
  return { items: all.filter((s) => s.state === st), counts: { active: all.filter((s) => s.state === "active").length, planned: 1, expired: 1 } };
});
mock("GET", "/inhibitions", () => ({
  items: [
    { id: "ih1", source: [{ k: "alertname", v: "HighErrorRate" }, { k: "severity", v: "P1" }], target: [{ k: "alertname", v: "DownstreamTimeout" }], equal: ["service"], active: true, stat: P("当前抑制 3 条", "Inhibiting 3 now") },
    { id: "ih2", source: [{ k: "alertname", v: "ClusterDown" }], target: [{ k: "alertname", v: "~ .+" }], equal: ["cluster"], active: false, stat: P("近 7 天 2 次", "2× in 7 days") },
    { id: "ih3", source: [{ k: "alertname", v: "NodeNotReady" }], target: [{ k: "alertname", v: "PodCrashLooping" }], equal: ["node"], active: false, stat: P("近 7 天 5 次", "5× in 7 days") },
    { id: "ih4", source: [{ k: "alertname", v: "DatabaseDown" }], target: [{ k: "alertname", v: "HighErrorRate" }], equal: ["database"], active: false, stat: P("近 30 天 0 次", "0× in 30 days") },
  ],
}));
mock("POST", "/silences/preview", ({ body }) => {
  const m = (body as { matchers: { k: string; v: string }[] }).matchers ?? [];
  const svc = m.find((x) => x.k === "service")?.v;
  return { affects: svc === "checkout-api" ? 3 : svc ? 1 : 0, sample: svc === "checkout-api" ? ["checkout-api-7f9c-1", "checkout-api-7f9c-2", "+1"] : [] };
});

/* ---------------- proposals & executions ---------------- */
const proposal = {
  id: "PRP-88",
  title: P("库存调用重试降为 1 次，并打开熔断", "Retry inventory calls once and turn the circuit breaker on"),
  status: "pending",
  risk: "medium",
  impact: P("影响 6 个 Pod", "Affects 6 pods"),
  reversible: true,
  proposed_by: P("AI 在 INV-231 中提出", "Proposed by AI in INV-231"),
  proposed_at: "14:18",
  investigation_id: "INV-231",
  target: P("配置中心 · production / checkout-api · 集群 prod-sh-1", "Config center · production / checkout-api · cluster prod-sh-1"),
  diff: { path: P("config/checkout-api.yaml · 配置中心", "config/checkout-api.yaml · config center"), lines: ["-  inventory.retry.max_attempts: 3", "+  inventory.retry.max_attempts: 1", "-  inventory.circuit_breaker: off", "+  inventory.circuit_breaker: on", "   inventory.timeout: 300ms"] },
  method: P("配置中心热更新 · 分 2 批生效 · 预计 1 分钟 · 执行前再次比对配置摘要，不一致则拒绝执行", "Hot config update · 2 batches · ≈1 min · the config digest is re-checked before execution; any mismatch is refused"),
  evidence: [
    { n: 1, icon: "activity", title: P("inventory-03 P99 80 → 350 ms", "inventory-03 P99 80 → 350 ms"), desc: P("Prometheus · 14:02 先变慢，磁盘 IO 等待 2 → 180 ms", "Prometheus · slowed first at 14:02; disk IO wait 2 → 180 ms"), series: [80, 79, 81, 80, 96, 214, 318, 344, 351, 350, 352, 350] },
    { n: 2, icon: "layers", title: P("一个下单请求调了 4 次库存", "One order called inventory 4 times"), desc: P("Tempo · trace 4f2a9c · 3 次超时 300 ms，1 次失败", "Tempo · trace 4f2a9c · 3 timeouts at 300 ms, 1 failure") },
    { n: 3, icon: "terminal", title: P("重试日志 2,416 条 / 15 分钟", "2,416 retry logs / 15 min"), desc: 'Elasticsearch · "inventory.reserve timeout" retrying' },
    { n: 4, icon: "file", title: P("重试配置 v2.6 以来未改动", "Retry setting unchanged since v2.6"), desc: P("GitLab · timeout 300ms · max_attempts 3 · backoff none", "GitLab · timeout 300ms · max_attempts 3 · backoff none") },
  ],
  verification: [P("6 / 6 副本加载新配置", "6 / 6 replicas load the new config"), P("5xx 比例低于 0.5%，并持续 180 秒", "5xx ratio below 0.5% for 180 s"), P("库存调用量回到 1.3k/s 以下", "Inventory calls back under 1.3k/s")],
  verification_note: P("任一条件不满足：标记为“需要人工处理”，不自动重试。", "If any check fails the run is marked “needs attention” — no automatic retry."),
  checklist: [P("目标与环境正确：production / checkout-api", "Target and environment are right: production / checkout-api"), P("变更内容与依据一致", "The change matches the evidence"), P("了解影响范围与回退方式", "I understand the impact and how to revert")],
  requirement: P("需 1 人批准 · 你是生产环境审批人", "1 approval needed · you are a production approver"),
  discussion: [
    { author: "李明", time: "14:20", text: P("只重试 1 次，偶发超时会不会直接让下单失败？", "With one retry, will a stray timeout fail the order?") },
    { author: "AI", ai: true, time: "14:20", text: P("风暴前库存调用超时率只有 0.2%，保留 1 次重试足够覆盖偶发超时；熔断只在 10 秒内失败率超过 50% 时打开，30 秒后半开探测。[1][4]", "Before the storm inventory timeouts were 0.2%, so one retry covers stray timeouts; the breaker opens only above 50% failures in 10 s and probes half-open after 30 s.[1][4]") },
  ],
};
// PRP-RB* / EXE-RB* are the sample rollback proposal and executions (docs/api/v3-rollback.md).
mock("GET", "/proposals/:id", ({ params }) =>
  params.id.startsWith("PRP-RB") ? rollbackProposal(params.id) : isAlertRuleProposal(params.id) ? alertRuleProposal(params.id) : { ...proposal, id: params.id, action_kind: "configcenter.patch", rollback: null, alert_rule: null },
);
mock("POST", "/proposals/:id/approve", () => ({ execution_id: "EXE-51" }));
mock("POST", "/proposals/:id/reject", () => ({ ok: true }));
mock("POST", "/proposals/:id/comments", ({ body }) => ({ author: "王晨", time: new Date().toTimeString().slice(0, 5), text: (body as { text: string }).text }));

// Retries stop at once when the config lands, so the drop is steep (the same curve as wans.run/redstring).
const obs = [8.1, 8.6, 8.3, 8.9, 8.4, 8.2, 8.7, 8.5, 8.3, 8.8, 8.4, 8.6, 7.9, 5.2, 1.9, 0.6, 0.38, 0.3, 0.26, 0.24, 0.22, 0.24, 0.21, 0.2, 0.23, 0.21, 0.19, 0.22, 0.2, 0.21];
mock("GET", "/executions/:id", ({ params }) => params.id.startsWith("EXE-RB") ? rolloutExecution(params.id) : isAlertRuleExecution(params.id) ? alertRuleExecution(params.id) : ({
  id: params.id,
  proposal_id: "PRP-88",
  status: "succeeded",
  steps: [
    { time: "14:19:02", icon: "user", tone: "accent", title: P("王晨 批准", "Approved by Wang Chen"), desc: P("核对清单 3/3 · 审计 #A-5521", "Checklist 3/3 · audit #A-5521") },
    { time: "14:19:05", icon: "clock", tone: "neutral", title: P("执行器排队", "Queued on executor"), desc: P("尝试 1 · 与 Agent 进程隔离", "Attempt 1 · isolated from the agent") },
    { time: "14:19:08", icon: "shield", tone: "neutral", title: P("执行前检查通过", "Pre-flight passed"), desc: P("配置摘要与批准时一致", "Config digest matches approval") },
    { time: "14:19:51", icon: "rotate", tone: "neutral", title: P("配置热更新完成", "Hot config update done"), desc: P("6 / 6 副本已加载", "6 / 6 replicas loaded") },
    { time: "14:22:51", icon: "check", tone: "ok", title: P("180 秒验证通过", "180 s verification passed"), desc: P("3 项条件全部满足 · 已生成报告草稿", "All 3 checks met · report drafted") },
  ],
  observation: { values: obs, threshold: 0.5, labels: obs.map((_, i) => hhmm(14 * 60 + 16 + Math.floor(i / 3.6))), band: [15, 29], marker: 13, checks: [{ text: P("5xx 比例", "5xx ratio"), value: "0.21%" }, { text: P("库存调用量", "Inventory calls"), value: "1.2k/s" }, { text: P("新配置加载", "New config loaded"), value: "6 / 6" }] },
  report: {
    title: P("checkout-api 5xx 上升故障报告", "checkout-api 5xx spike — incident report"),
    severity: "P1",
    meta: P("2026-09-20 · 持续 18 分钟 · 负责人 王晨", "2026-09-20 · 18 min · owner Wang Chen"),
    summary: P("inventory-03 磁盘变慢，让一部分库存调用超过 300 ms[1]；checkout-api 超时后立即重试 3 次（v2.6 以来的配置）[4]，一个下单请求调 4 次库存，调用放大到 3.2 倍，整个库存服务被压满，下单返回 5xx[2][3]。重试降为 1 次并打开熔断后 3 分钟内恢复。14:03 的发布与本次故障无关。", "inventory-03's disk slowed down and pushed some inventory calls past 300 ms[1]; checkout-api retried each timeout three times at once (a setting since v2.6)[4], so one order made 4 inventory calls, traffic grew 3.2×, the inventory service saturated and checkout returned 5xx[2][3]. Retrying once with the circuit breaker on recovered it within 3 minutes. The 14:03 release was unrelated."),
    impact: P("5xx 比例峰值 8.4%，约 [受影响订单数，待业务确认] 笔下单失败。", "5xx peaked at 8.4%; about [affected orders — pending business confirmation] orders failed."),
    timeline: [
      { time: "14:02", text: P("inventory-03 磁盘 IO 等待升高，P99 开始上涨", "inventory-03 disk IO wait rises; its P99 starts climbing") },
      { time: "14:03", text: P("发布 v2.8.1（与本次故障无关）", "Released v2.8.1 (unrelated)") },
      { time: "14:05", text: P("Alertmanager 告警：5xx 超过阈值", "Alertmanager: 5xx above threshold") },
      { time: "14:06", text: P("AI 自动初查，派生 3 个分支", "AI triage forked 3 branches") },
      { time: "14:18", text: P("AI 提出提案：重试降为 1 次并打开熔断", "AI proposed: retry once, breaker on") },
      { time: "14:19", text: P("王晨 批准，执行器开始执行", "Wang Chen approved; executor started") },
      { time: "14:22", text: P("验证通过，服务恢复", "Verification passed; service recovered") },
    ],
    actions: [
      { text: P("库存调用改为指数退避，重试预算不超过 10%", "Exponential backoff on inventory calls, retry budget under 10%"), owner: "张磊", due: "9/27", done: false },
      { text: P("库存服务开启离群实例摘除", "Outlier ejection for the inventory service"), owner: "李明", due: "9/30", done: false },
      { text: P("inventory-03 更换磁盘", "Replace the disk on inventory-03"), owner: "王晨", due: "9/21", done: true },
    ],
    refs: P("引用：4 条证据 · 1 个 Runbook · 执行记录 exec_231_1", "References: 4 evidence · 1 runbook · execution exec_231_1"),
  },
}));
mock("POST", "/executions/:id/report/publish", () => ({ ok: true }));

