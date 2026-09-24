import { mock } from "../router";
import { l } from "../../i18n";
import { series } from "../../charts";

const P = (zh: string, en: string) => l(zh, en);
const N = 36;
const REL = 16;
const RB = 27;
function bump(vals: number[], i0: number, i1: number, k: number, ramp = 2) {
  return vals.map((v, i) => {
    let f = 1;
    if (i >= i0 && i < i1) f = i >= i0 + ramp ? k : 1 + ((k - 1) * (i - i0 + 1)) / (ramp + 1);
    else if (i >= i1 && i < i1 + ramp) f = 1 + (k - 1) * (1 - (i - i1 + 1) / (ramp + 1));
    return Math.round(v * f * 1000) / 1000;
  });
}
const hhmm = (m: number) => `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}`;

/* ---------------- services ---------------- */
const SERVICES = [
  { id: "checkout-api", name: "checkout-api", desc: P("下单与结算", "Checkout & ordering"), tier: "T0", team: P("交易平台", "Trading"), oncall: "王晨", status: "danger", slo: { target: "99.9%", budget_left: 38 }, release: { brand: "gitlab", version: "v2.8.1", when: P("28 分钟前", "28 min ago") }, alerts: 2, connectors: ["prometheus", "elasticsearch", "tempo", "gitlab", "argo"] },
  { id: "payment-gateway", name: "payment-gateway", desc: P("支付通道聚合", "Payment routing"), tier: "T0", team: P("支付", "Payments"), oncall: "赵凯", status: "warn", slo: { target: "99.95%", budget_left: 61 }, release: { brand: "gitlab", version: "v1.14.0", when: P("昨天", "yesterday") }, alerts: 1, connectors: ["prometheus", "elasticsearch", "gitlab", "argo"] },
  { id: "api-gateway", name: "api-gateway", desc: P("统一入口", "Edge gateway"), tier: "T0", team: P("平台", "Platform"), oncall: "张磊", status: "ok", slo: { target: "99.99%", budget_left: 74 }, release: { brand: "argo", version: "nginx 1.25", when: "9/02" }, alerts: 0, connectors: ["prometheus", "nginx", "argo"] },
  { id: "order-service", name: "order-service", desc: P("订单读写", "Order read/write"), tier: "T1", team: P("交易平台", "Trading"), oncall: "李明", status: "ok", slo: { target: "99.9%", budget_left: 82 }, release: { brand: "githubactions", version: "v3.2.4", when: P("3 天前", "3 days ago") }, alerts: 0, connectors: ["prometheus", "elasticsearch", "githubactions", "argo"] },
  { id: "inventory-worker", name: "inventory-worker", desc: P("库存扣减消费者", "Inventory consumer"), tier: "T1", team: P("交易平台", "Trading"), oncall: "周航", status: "warn", slo: { target: "99.5%", budget_left: 70 }, release: { brand: "gitlab", version: "v0.9.8", when: "9/17" }, alerts: 1, connectors: ["prometheus", "kafka", "gitlab"] },
  { id: "user-center", name: "user-center", desc: P("账号与登录", "Accounts & login"), tier: "T1", team: P("账号", "Accounts"), oncall: "陈雨", status: "ok", slo: { target: "99.9%", budget_left: 90 }, release: { brand: "jenkins", version: "v2.1.0", when: "9/15" }, alerts: 0, connectors: ["prometheus", "elasticsearch", "jenkins"] },
  { id: "search-api", name: "search-api", desc: P("商品搜索", "Product search"), tier: "T2", team: P("平台", "Platform"), oncall: "张磊", status: "ok", slo: { target: "99.5%", budget_left: 55 }, release: { brand: "gitlab", version: "v4.0.2", when: "9/18" }, alerts: 0, connectors: ["prometheus", "elasticsearch"] },
  { id: "notification-svc", name: "notification-svc", desc: P("短信与推送", "SMS & push"), tier: "T2", team: P("平台", "Platform"), oncall: "陈雨", status: "ok", slo: { target: "99.0%", budget_left: 96 }, release: { brand: "gitlab", version: "v1.3.1", when: "9/10" }, alerts: 0, connectors: ["prometheus"] },
];
mock("GET", "/services", ({ query }) => {
  const q = String(query.q ?? "").toLowerCase();
  const tier = query.tier as string | undefined;
  const status = query.status as string | undefined;
  const items = SERVICES.filter((s) => (!q || s.id.includes(q)) && (!tier || s.tier === tier) && (!status || s.status === status));
  return {
    kpis: [
      { id: "n", label: P("服务", "Services"), value: "8", sub: P("生产 8 · 预发 8", "prod 8 · staging 8"), series: [] },
      { id: "bad", label: P("异常", "Degraded"), value: "1", tone: "danger", sub: P("checkout-api · 5xx 8.4%", "checkout-api · 5xx 8.4%"), series: [] },
      { id: "warn", label: P("有告警", "Alerting"), value: "2", tone: "warn", sub: "payment-gateway, inventory-worker", series: [] },
      { id: "budget", label: P("错误预算 < 50%", "Budget < 50%"), value: "1", tone: "warn", sub: P("按当前速率 2 小时耗尽", "Exhausted in ~2 h at current burn"), series: [] },
      { id: "rel", label: P("本周发布", "Releases this week"), value: "14", sub: P("失败 1 · 回滚 1", "1 failed · 1 rolled back"), series: [1, 2, 1, 3, 2, 4, 1] },
    ],
    items,
    facets: [
      { key: "team", title: P("团队", "Team"), items: [{ label: P("交易平台", "Trading"), count: 3, on: false }, { label: P("支付", "Payments"), count: 1, on: false }, { label: P("平台", "Platform"), count: 3, on: false }, { label: P("账号", "Accounts"), count: 1, on: false }] },
      { key: "tier", title: P("等级", "Tier"), items: [{ label: "T0", count: 3, on: tier === "T0" }, { label: "T1", count: 3, on: tier === "T1" }, { label: "T2", count: 2, on: tier === "T2" }] },
      { key: "status", title: P("状态", "Status"), items: [{ label: P("异常", "Degraded"), count: 1, on: status === "danger" }, { label: P("有告警", "Alerting"), count: 2, on: status === "warn" }, { label: P("正常", "Healthy"), count: 5, on: status === "ok" }] },
      { key: "env", title: P("环境", "Environment"), items: [{ label: P("生产", "Production"), count: 8, on: true }, { label: P("预发", "Staging"), count: 8, on: false }] },
    ],
  };
});

const base = series(20, 1.2, 0.1, 10, 3);
mock("GET", "/services/:id", ({ params }) => {
  const s = SERVICES.find((x) => x.id === params.id) ?? SERVICES[0];
  return {
    id: s.id, name: s.name, tier: s.tier, team: s.team, env: "prod", status: s.status,
    desc: P("下单与结算 · 负责人 李明 · 值班 王晨 · prod-sh-1 · 6 个副本", "Checkout · owner Li Ming · on call Wang Chen · prod-sh-1 · 6 replicas"),
    deeplinks: [{ brand: "grafana", name: "Grafana", url: "#" }, { brand: "kibana", name: "Kibana", url: "#" }, { brand: "gitlab", name: "GitLab", url: "#" }, { brand: "argo", name: "Argo CD", url: "#" }],
    ai: { text: P("当前状态：异常。5xx 8.4%，与 14:03 发布 v2.8.1 中的连接池配置下调高度相关（INV-231）；回滚提案正在等待复核。", "Status: degraded. 5xx at 8.4%, strongly linked to the pool config change in v2.8.1 at 14:03 (INV-231); a rollback proposal is awaiting review."), investigation: "INV-231", proposal: "PRP-88", meta: P("由调查助手 v12 生成 · 14:21", "Generated by Investigator v12 · 14:21") },
    signals: [
      { id: "rps", label: P("请求量", "Requests"), value: "1.2k", unit: P("次/秒", "/s"), delta: "+2%", delta_good: true, sub: P("与上周同期持平", "Flat vs last week"), series: base },
      { id: "err", label: P("错误率", "Error rate"), value: "8.4", unit: "%", delta: "+42×", delta_good: false, tone: "danger", sub: P("阈值 1%", "Threshold 1%"), series: [0.2, 0.3, 0.2, 0.3, 0.2, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 2.1, 5.6, 7.9, 8.4, 8.1, 8.6, 8.4] },
      { id: "p99", label: P("P99 延迟", "P99 latency"), value: "1.6", unit: "s", delta: "+760%", delta_good: false, tone: "warn", sub: P("SLO 目标 300 ms", "SLO 300 ms"), series: [0.19, 0.2, 0.19, 0.21, 0.2, 0.19, 0.2, 0.2, 0.21, 0.19, 0.2, 0.19, 0.2, 0.9, 1.3, 1.5, 1.6, 1.58, 1.62, 1.6] },
      { id: "cpu", label: P("饱和度 · CPU", "Saturation · CPU"), value: "71", unit: "%", delta: "+26%", delta_good: false, tone: "warn", sub: P("HPA 目标 70%", "HPA target 70%"), series: [40, 42, 41, 44, 43, 45, 44, 46, 45, 47, 46, 45, 48, 60, 68, 71, 70, 69, 71, 71] },
    ],
    slo: { availability: "99.87%", target: "99.9%", budget_left: 38, burn: [{ window: P("1 小时燃烧", "1 h burn"), value: "14.4×", tone: "danger", pct: 100 }, { window: P("6 小时", "6 h"), value: "3.1×", tone: "warn", pct: 42 }, { window: P("3 天", "3 d"), value: "0.9×", tone: "ok", pct: 12 }], note: P("按当前速率约 2 小时耗尽", "Exhausted in ~2 h at this rate") },
    changes: [
      { brand: "gitlab", title: "#8812 · v2.8.1 · chore: tune db pool", meta: P("14:03 · 李明 · 成功", "14:03 · Li Ming · success"), tag: P("配置变更", "Config change") },
      { brand: "argo", title: P("Argo CD 同步 checkout-api → 生产", "Argo CD sync checkout-api → prod"), meta: P("14:04 · 自动 · Synced", "14:04 · auto · Synced") },
      { brand: "gitlab", title: "#8790 · v2.8.0 · feat: coupon stacking", meta: P("9/19 · 李明 · 成功", "9/19 · Li Ming · success") },
    ],
    deps: {
      upstream: [{ brand: "nginx", name: "api-gateway", status: "ok", meta: "1.2k/s" }],
      downstream: [{ name: "payment-gateway", status: "warn", meta: "P99 1.8 s" }, { brand: "postgresql", name: "orders-db", status: "danger", meta: P("连接池 50/50", "pool 50/50") }, { brand: "redis", name: "session-cache", status: "ok", meta: P("命中 96%", "hit 96%") }, { brand: "kafka", name: "order-events", status: "ok", meta: P("延迟 12 ms", "lag 12 ms") }],
    },
    tabs: { changes: 3, deps: 5, resilience: 5, runbooks: 4, incidents: 2 },
  };
});

mock("GET", "/services/:id/resilience", () => ({
  synced: P("最近同步 1 分钟前", "Synced 1 min ago"),
  rollout: {
    title: P("v2.8.2 · 修复连接池下限", "v2.8.2 · fix pool lower bound"),
    status: P("灰度中", "Canary"),
    stable: P("稳定版本 v2.8.0（已回滚）", "Stable v2.8.0 (rolled back)"),
    steps: [{ weight: "5%", note: P("完成 · 10 分钟", "Done · 10 min"), state: "done" }, { weight: "25%", note: P("当前 · 分析中", "Current · analysing"), state: "current" }, { weight: "50%", note: P("分析通过后自动", "Auto after analysis"), state: "todo" }, { weight: "100%", note: P("人工确认", "Manual confirm"), state: "todo" }],
    split: { stable: 75, canary: 25 },
    analysis: [{ name: P("成功率", "Success rate"), value: "99.93%", threshold: "≥ 99.9%", ok: true }, { name: P("P99 延迟", "P99 latency"), value: "212 ms", threshold: "≤ 300 ms", ok: true }, { name: P("连接等待 P95", "Conn wait P95"), value: "14 ms", threshold: "≤ 50 ms", ok: true }],
    stable_series: series(30, 0.2, 0.05, 8, 21),
    canary_series: series(30, 0.18, 0.06, 8, 22),
    next: P("下一步约 6 分钟后自动推进", "Next step in ~6 min"),
    source: P("Argo Rollouts · rollouts/checkout-api · 同步于 1 分钟前", "Argo Rollouts · rollouts/checkout-api · synced 1 min ago"),
  },
  breakers: [
    { brand: "istio", upstream: "payment-gateway", mechanism: P("Istio · outlierDetection", "Istio · outlierDetection"), state: "half_open", threshold: P("连续 5xx ≥ 5 · 驱逐 30 s", "5 consecutive 5xx · eject 30 s"), ejected: "1 / 4" },
    { brand: "sentinel", upstream: "risk-engine", mechanism: P("Sentinel · 慢调用比例", "Sentinel · slow-call ratio"), state: "closed", threshold: P("慢调用 ≥ 50%（RT > 1 s）", "Slow calls ≥ 50% (RT > 1 s)"), ejected: "0" },
    { brand: "", upstream: "orders-db", mechanism: P("应用内连接池（HikariCP）", "In-app pool (HikariCP)"), state: "none", threshold: P("无熔断，排队超时 30 s", "No breaker; 30 s queue timeout"), ejected: "—" },
  ],
  breaker_timeline: [{ from: 0, to: 55, state: "closed" }, { from: 55, to: 62, state: "open" }, { from: 62, to: 100, state: "half_open" }],
  breaker_note: P("orders-db 没有熔断保护：连接池排满时请求会排队 30 s 才失败。建议在代码中配置超时与熔断，平台只给出建议。", "orders-db has no breaker: when the pool is full requests queue 30 s before failing. Configure timeouts and a breaker in code — the platform only advises."),
  limits: [
    { brand: "apisix", route: "POST /orders", mechanism: "APISIX · limit-count", limit: "2,000 r/s", used: "1,210", pct: 61, rejected: 0 },
    { brand: "apisix", route: "POST /payments", mechanism: "APISIX · limit-count", limit: "800 r/s", used: "420", pct: 53, rejected: 0 },
    { brand: "kong", route: "GET /cart", mechanism: "Kong · rate-limiting", limit: "5,000 r/s", used: "820", pct: 16, rejected: 0 },
    { brand: "nginx", route: "/internal/*", mechanism: "Nginx · limit_req", limit: "100 r/s", used: "12", pct: 12, rejected: 0 },
  ],
  limit_note: P("最近 1 小时 429 为 0：本次 5xx 与限流无关（可作为排除证据）", "Zero 429s in the last hour: the 5xx is unrelated to rate limiting (usable as exclusion evidence)"),
  flags: [
    { brand: "unleash", key: "checkout.recommendations", desc: P("14:10 王晨在 Unleash 中关闭 · 降低下单页数据库查询", "Turned off by Wang Chen at 14:10 in Unleash · fewer DB queries on checkout"), on: false, tag: { text: P("降级中", "Degraded"), tone: "warn" }, action: P("提议恢复", "Propose restore") },
    { brand: "unleash", key: "checkout.coupon-stacking", desc: P("优惠叠加 · 100% 用户", "Coupon stacking · 100% of users"), on: true, tag: { text: P("生产", "Prod"), tone: "neutral" }, action: P("提议关闭", "Propose disable") },
    { brand: "launchdarkly", key: "payment.fallback-provider", desc: P("备用支付通道 · 10% 流量", "Fallback payment provider · 10% traffic"), on: true, tag: { text: P("灰度 10%", "10% rollout"), tone: "accent" }, action: P("提议调整", "Propose change") },
  ],
  hpa: { min: 4, max: 12, current: 6, desired: 8, replicas: Array(36).fill(60), desired_series: [...Array(17).fill(60), ...Array(19).fill(80)], cpu: bump(series(36, 46, 4, 12, 31), 16, 27, 1.55), target: 70, note: P("14:06 起期望 8 个副本，实际 6 个：2 个 Pod 因节点资源不足处于 Pending。扩容对连接池耗尽帮助有限。", "Since 14:06 desired 8, actual 6: 2 pods pending on node capacity. Scaling helps little with pool exhaustion.") },
  ai: [
    P("**限流未触发**（429 = 0），可排除限流导致 5xx。", "**Rate limits did not fire** (429 = 0) — rate limiting is ruled out."),
    P("payment-gateway 熔断 14:08 打开后转为半开：下游支付被 checkout-api 的排队拖慢，是**结果不是原因**。", "payment-gateway's breaker opened at 14:08 then went half-open: payments were slowed by checkout-api's queueing — **an effect, not the cause**."),
    P("recommendations 已降级、HPA 扩容受阻，都没有缓解连接池耗尽。", "Degrading recommendations and the blocked HPA scale-out did not relieve the pool."),
    P("修复版本 v2.8.2 灰度 25%，分析指标正常，可继续推进。", "Fix v2.8.2 is at 25% canary with healthy analysis — safe to continue."),
  ],
}));
mock("POST", "/services/:id/resilience/proposals", () => ({ proposal_id: "PRP-91" }));

/* ---------------- topology ---------------- */
mock("GET", "/topology", () => ({
  groups: [
    { id: "g-gw", label: P("网关", "Gateway"), zone: " · zone-a", x: 20, y: 300, w: 200, h: 128 },
    { id: "g-co", label: "checkout", zone: " · zone-a/b", x: 250, y: 200, w: 200, h: 320 },
    { id: "g-pay", label: "payment", zone: " · zone-b", x: 500, y: 40, w: 204, h: 212 },
    { id: "g-data", label: "data", zone: " · zone-a/b", x: 500, y: 272, w: 204, h: 274 },
    { id: "g-ful", label: "fulfillment", zone: " · zone-b", x: 500, y: 578, w: 204, h: 122 },
  ],
  nodes: [
    { id: "api-gateway", name: "api-gateway", brand: "nginx", sub: "Ingress · 2.0k/s", status: "warn", group: "g-gw", x: 34, y: 340, impacted: true },
    { id: "checkout-api", name: "checkout-api", brand: "kubernetes", sub: "Deployment · 6/6", status: "danger", group: "g-co", x: 264, y: 250, impacted: true },
    { id: "cart-service", name: "cart-service", brand: "kubernetes", sub: "Deployment · 4/4", status: "ok", group: "g-co", x: 264, y: 420, impacted: false },
    { id: "payment-gateway", name: "payment-gateway", sub: P("P99 1.8 s · 熔断半开", "P99 1.8 s · breaker half-open"), status: "warn", group: "g-pay", x: 514, y: 90, impacted: true },
    { id: "risk-engine", name: "risk-engine", sub: "Deployment · 3/3", status: "ok", group: "g-pay", x: 514, y: 190, impacted: false },
    { id: "orders-db", name: "orders-db", brand: "postgresql", sub: P("等待 P95 420 ms", "wait P95 420 ms"), status: "danger", group: "g-data", x: 514, y: 312, impacted: true },
    { id: "session-cache", name: "session-cache", brand: "redis", sub: P("命中 96%", "hit 96%"), status: "ok", group: "g-data", x: 514, y: 392, impacted: false },
    { id: "order-events", name: "order-events", brand: "kafka", sub: P("延迟 12 ms", "lag 12 ms"), status: "ok", group: "g-data", x: 514, y: 472, impacted: false },
    { id: "inventory-worker", name: "inventory-worker", brand: "kubernetes", sub: P("消费者 · 积压 12k", "consumer · lag 12k"), status: "warn", group: "g-ful", x: 514, y: 628, impacted: false },
  ],
  edges: [
    { id: "e1", from: "api-gateway", to: "checkout-api", label: "5xx 8.4%", tone: "danger", fast: true, width: 2, path: "M206 364 C 236 364, 236 274, 264 274", lx: 236, ly: 318 },
    { id: "e2", from: "api-gateway", to: "cart-service", label: "800/s", tone: "ok", width: 1.5, path: "M206 364 C 236 364, 236 444, 264 444", lx: 236, ly: 406 },
    { id: "e3", from: "checkout-api", to: "payment-gateway", label: "420/s · 2.1%", tone: "warn", width: 1.5, path: "M436 274 C 476 274, 476 114, 514 114", lx: 476, ly: 192 },
    { id: "e4", from: "checkout-api", to: "orders-db", label: "1.1k qps", tone: "danger", fast: true, width: 2.5, path: "M436 274 C 476 274, 476 336, 514 336", lx: 476, ly: 305 },
    { id: "e5", from: "checkout-api", to: "session-cache", label: "2.3k/s", tone: "ok", width: 1.5, path: "M436 274 C 476 274, 476 416, 514 416", lx: 476, ly: 345 },
    { id: "e6", from: "checkout-api", to: "order-events", label: "380/s", tone: "ok", width: 1.5, path: "M436 274 C 476 274, 476 496, 514 496", lx: 476, ly: 385 },
    { id: "e7", from: "cart-service", to: "session-cache", label: "1.6k/s", tone: "ok", width: 1.5, path: "M436 444 C 476 444, 476 416, 514 416", lx: 476, ly: 431 },
    { id: "e8", from: "payment-gateway", to: "risk-engine", label: "420/s", tone: "ok", width: 1.5, path: "M600 138 L 600 190", lx: 642, ly: 164 },
    { id: "e9", from: "order-events", to: "inventory-worker", label: P("积压 12k", "lag 12k"), tone: "warn", width: 1.5, path: "M600 520 L 600 628", lx: 642, ly: 566 },
  ],
  blast: P("4 个服务 · 2 个接口 · 约 38% 的用户请求经过", "4 services · 2 endpoints · ~38% of user requests"),
  tree: [
    { depth: 0, kind: P("集群 · 18 节点", "Cluster · 18 nodes"), name: "prod-sh-1", brand: "kubernetes", status: "danger", replicas: "64", cpu: 58, mem: 61, alerts: 4, open: true },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "checkout", status: "danger", replicas: "10", cpu: 66, mem: 58, alerts: 3, open: true },
    { depth: 2, kind: "Deployment", name: "checkout-api", brand: "kubernetes", status: "danger", replicas: "6/6", cpu: 71, mem: 58, alerts: 3, open: true, hit: true },
    { depth: 3, kind: "Pod · 5xx 9.2%", name: "checkout-api-7f9c-4", status: "danger", replicas: "1/1", cpu: 78, mem: 60 },
    { depth: 3, kind: "Pod · 5xx 8.9%", name: "checkout-api-7f9c-1", status: "danger", replicas: "1/1", cpu: 74, mem: 59 },
    { depth: 3, kind: "Pod · 5xx 1.4%", name: "checkout-api-7f9c-5", status: "warn", replicas: "1/1", cpu: 61, mem: 55 },
    { depth: 2, kind: "Deployment", name: "cart-service", brand: "kubernetes", status: "ok", replicas: "4/4", cpu: 32, mem: 41, alerts: 0, open: false },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "data", status: "danger", replicas: "8", cpu: 44, mem: 72, alerts: 1, open: true },
    { depth: 2, kind: "StatefulSet", name: "orders-db", brand: "postgresql", status: "danger", replicas: "2/2", cpu: 38, mem: 77, alerts: 1, open: false, hit: true },
    { depth: 2, kind: "StatefulSet", name: "session-cache", brand: "redis", status: "ok", replicas: "3/3", cpu: 21, mem: 48, alerts: 0, open: false },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "payment", status: "warn", replicas: "6", cpu: 52, mem: 49, alerts: 1, open: false },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "fulfillment", status: "warn", replicas: "5", cpu: 47, mem: 51, alerts: 1, open: false },
  ],
  sankey: {
    cols: [
      [{ id: "gw", label: "api-gateway · 2.0k/s", value: 2000, color: "var(--c1)" }],
      [{ id: "co", label: "checkout-api · 1.2k/s", value: 1200, color: "var(--c3)" }, { id: "ca", label: "cart-service · 800/s", value: 800, color: "var(--c2)" }],
      [{ id: "db", label: "orders-db · 700 qps", value: 700, color: "var(--c3)" }, { id: "pay", label: "payment-gateway · 420/s", value: 420, color: "var(--c4)" }, { id: "sc", label: "session-cache · 880/s", value: 880, color: "var(--c5)" }],
    ],
    links: [
      { from: "gw", to: "co", value: 1200, color: "var(--c3)" }, { from: "gw", to: "ca", value: 800, color: "var(--c2)" }, { from: "co", to: "db", value: 500, color: "var(--c3)" },
      { from: "co", to: "pay", value: 420, color: "var(--c4)" }, { from: "co", to: "sc", value: 280, color: "var(--c5)" }, { from: "ca", to: "sc", value: 600, color: "var(--c5)" }, { from: "ca", to: "db", value: 200, color: "var(--c2)" },
    ],
  },
  detail: {
    id: "orders-db", name: "orders-db", brand: "postgresql", meta: P("PostgreSQL 15.4 · 主从 · prod-sh-1", "PostgreSQL 15.4 · primary/replica · prod-sh-1"),
    conns: { used: 312, max: 800 }, from_service: { used: 50, max: 50, service: "checkout-api" },
    wait: bump(series(N, 12, 3, 10, 9), REL, RB, 34),
    health: [{ k: P("慢查询", "Slow queries"), v: P("2 / 分钟", "2 / min") }, { k: P("复制延迟", "Replication lag"), v: "0.2 s" }, { k: "CPU", v: "38%" }],
    owner: "周航", dependents: P("5 个服务", "5 services"), source: P("资源同步 · 2 分钟前", "Resource sync · 2 min ago"),
  },
}));

/* ---------------- metrics ---------------- */
mock("GET", "/metrics/dashboard", ({ query }) => {
  const ok2 = series(N, 1080, 120, 18, 2);
  const c4 = series(N, 26, 8, 12, 4);
  const c5 = bump(series(N, 2.4, 0.6, 9, 5), REL + 1, RB + 1, 40);
  const rps = ok2.map((v, i) => v + c4[i] + c5[i]);
  const err = c5.map((v, i) => Math.round((10000 * v) / rps[i]) / 100);
  const p50 = bump(series(N, 38, 6, 10, 6), REL + 1, RB + 1, 4.2);
  const p95 = bump(series(N, 96, 14, 10, 7), REL + 1, RB + 1, 7.5);
  const p99 = bump(series(N, 190, 24, 10, 8), REL + 1, RB + 1, 8.6);
  const wait = bump(series(N, 12, 3, 10, 9), REL, RB, 34);
  const heat = Array.from({ length: 8 }, (_, r) =>
    Array.from({ length: N }, (_, c) => {
      const hot = c >= REL + 1 && c <= RB;
      const centre = hot ? 1.6 : 5.4;
      const v = Math.max(0, 0.95 - Math.abs(r - centre) * (hot ? 0.34 : 0.42)) + (((r * 7 + c * 11) % 9) - 4) / 40;
      return Math.max(0, Math.min(0.99, v));
    }),
  );
  const pod = (seed: number, k: number, from = REL) => bump(series(N, 12, 3, 10, seed), from, RB, k);
  return {
    window: { labels: Array.from({ length: N }, (_, i) => hhmm(13 * 60 + 35 + Math.round(i * 1.714))), ticks: ["13:35", "13:50", "14:05", "14:20", "14:35"], release_at: REL, rollback_at: RB, incident: [13, 32], incident_label: "INV-231 · 13:58 – 14:30" },
    kpis: [
      { id: "rps", label: P("请求量", "Requests"), value: "1.21k", unit: P("次/秒", "/s"), delta: "+2%", delta_good: true, sub: P("与上周同期持平", "Flat vs last week"), series: rps },
      { id: "err", label: P("5xx 比例", "5xx ratio"), value: "8.4", unit: "%", delta: "+42×", delta_good: false, tone: "danger", sub: P("阈值 1%", "Threshold 1%"), series: err },
      { id: "p99", label: P("P99 延迟", "P99 latency"), value: "1.64", unit: "s", delta: "+760%", delta_good: false, tone: "warn", sub: P("SLO 目标 300 ms", "SLO 300 ms"), series: p99 },
      { id: "wait", label: P("连接等待 P95", "Conn wait P95"), value: "420", unit: "ms", delta: "+34×", delta_good: false, tone: "danger", sub: P("orders-db 连接池 50/50", "orders-db pool 50/50"), series: wait },
      { id: "cpu", label: P("饱和度 · CPU", "Saturation · CPU"), value: "71", unit: "%", delta: "+26%", delta_good: false, tone: "warn", sub: P("HPA 目标 70%", "HPA target 70%"), series: bump(series(N, 45, 4, 12, 3), REL, RB, 1.55) },
    ],
    requests: { ok: ok2, client: c4, server: c5 },
    slo: { remaining: 38, availability: "99.87%", delta: "−0.05%", burn: [{ window: P("1 小时燃烧速率", "1 h burn rate"), value: "14.4×", tone: "danger", pct: 100 }, { window: P("6 小时", "6 h"), value: "3.1×", tone: "warn", pct: 42 }, { window: P("3 天", "3 d"), value: "0.9×", tone: "ok", pct: 12 }], note: P("按当前速率约 2 小时耗尽错误预算", "Error budget gone in ~2 h at this rate") },
    latency: { p50, p95, p99, heat, buckets: [">2 s", "1 s", "500 ms", "250 ms", "100 ms", "50 ms", "25 ms", "<10 ms"] },
    endpoints: [{ label: "POST /orders", value: 1840, tone: "danger" }, { label: "POST /payments", value: 1210, tone: "warn" }, { label: "GET /cart", value: 320 }, { label: "GET /orders/:id", value: 212 }, { label: "POST /coupons/apply", value: 184 }, { label: "GET /healthz", value: 3 }],
    instances: [
      { label: "7f9c-1", values: pod(11, 33), tone: "danger" }, { label: "7f9c-2", values: pod(12, 30), tone: "danger" }, { label: "7f9c-3", values: pod(13, 27), tone: "danger" },
      { label: "7f9c-4", values: pod(14, 35), tone: "danger" }, { label: "7f9c-5", values: pod(15, 9, REL + 3), tone: "warn" }, { label: "7f9c-6", values: series(N, 12, 3, 10, 16) },
    ],
    service: query.service ?? "checkout-api",
  };
});
mock("POST", "/metrics/ask", () => ({ conversation_id: "C-320" }));

/* traces: see ./traces.ts */

/* ---------------- logs ---------------- */
mock("GET", "/logs", ({ query }) => {
  const q = String(query.q ?? "");
  const rows = [
    { time: "14:30:58.214", level: "ERROR", service: "checkout-api", message: "acquire connection timeout after 3000ms (pool active=50 max=50)", trace: "4bf92f35" },
    { time: "14:30:58.190", level: "WARN", service: "checkout-api", message: "slow request POST /orders 3.2s", trace: "4bf92f35" },
    { time: "14:30:57.902", level: "ERROR", service: "checkout-api", message: "order create failed: TimeoutError", trace: "a07c11de" },
    { time: "14:30:57.655", level: "INFO", service: "checkout-api", message: "payment authorize ok order=8812731", trace: "a07c11de" },
    { time: "14:30:57.120", level: "ERROR", service: "checkout-api", message: "acquire connection timeout after 3000ms (pool active=50 max=50)", trace: "91e2b8f0" },
    { time: "14:30:56.874", level: "WARN", service: "payment-gateway", message: "upstream latency 1.8s channel=unionpay" },
    { time: "14:30:56.511", level: "INFO", service: "checkout-api", message: "cache hit ratio 0.96" },
    { time: "14:30:56.230", level: "ERROR", service: "checkout-api", message: "order create failed: TimeoutError", trace: "7fa01b62" },
    { time: "14:30:55.998", level: "INFO", service: "order-service", message: "order status updated id=8812719 status=PAID" },
    { time: "14:30:55.702", level: "ERROR", service: "checkout-api", message: "acquire connection timeout after 3000ms (pool active=50 max=50)", trace: "e3d4c7a8" },
    { time: "14:30:55.410", level: "WARN", service: "checkout-api", message: "slow request POST /orders 2.9s", trace: "e3d4c7a8" },
  ];
  const levels = String(query.levels ?? "ERROR,WARN,INFO").split(",");
  return {
    total: "38,214",
    errors: "2,415",
    histogram: [[3, 20, 1], [3, 21, 1], [2, 22, 1], [3, 20, 2], [3, 21, 1], [2, 23, 1], [3, 22, 1], [3, 21, 1], [2, 20, 1], [3, 22, 1], [3, 21, 1], [2, 22, 1], [9, 25, 18], [14, 26, 42], [16, 25, 51], [15, 26, 48], [16, 24, 50], [15, 25, 49], [16, 26, 51], [14, 25, 47]].map(([w, i, e]) => [e, w, i]),
    items: rows.filter((r) => levels.includes(r.level) && (!q || r.message.includes(q) || r.service.includes(q))),
    patterns: [
      { template: "acquire connection timeout after <*>ms (pool active=<*> max=<*>)", level: "ERROR", count: "1,284", trend: [0, 0, 0, 0, 0, 2, 8, 10, 11, 10], new: true },
      { template: "order create failed: <*>", level: "ERROR", count: "1,131", trend: [0, 0, 0, 0, 1, 3, 7, 9, 9, 9], new: true },
      { template: "slow request <*> <*>s", level: "WARN", count: "2,406", trend: [2, 2, 3, 2, 3, 8, 12, 13, 12, 13] },
      { template: "payment authorize ok order=<*>", level: "INFO", count: "18,220", trend: [20, 21, 20, 22, 21, 18, 17, 17, 18, 17] },
    ],
  };
});

/* ---------------- changes ---------------- */
mock("GET", "/changes", () => ({
  kpis: [
    { id: "deploys", label: P("本周部署", "Deploys this week"), value: "86", sub: P("日均 12 次 · 5 个来源", "12/day · 5 sources"), series: [9, 12, 10, 14, 13, 16, 12] },
    { id: "cfr", label: P("变更失败率", "Change failure rate"), value: "4.7%", tone: "warn", sub: P("4 次引发告警或回滚", "4 caused alerts or rollbacks"), series: [3, 5, 4, 6, 5, 4, 5] },
    { id: "lead", label: P("前置时间 P50", "Lead time P50"), value: "2.4", unit: P("小时", "h"), sub: P("从提交到生产", "Commit to production"), series: [3.1, 2.8, 2.9, 2.6, 2.5, 2.4, 2.4] },
    { id: "mttr", label: P("变更恢复时间", "Time to restore"), value: "38", unit: P("分钟", "min"), tone: "ok", sub: P("回滚或修复 · 本周", "Rollback or fix · this week"), series: [52, 49, 47, 44, 41, 40, 38] },
  ],
  sources: [{ brand: "gitlab", name: "GitLab CI", count: 38, on: true }, { brand: "githubactions", name: "GitHub Actions", count: 12, on: true }, { brand: "jenkins", name: "Jenkins", count: 5, on: true }, { brand: "argo", name: "Argo CD", count: 27, on: true }, { brand: "harbor", name: P("Harbor 镜像", "Harbor images"), count: 64, on: false }],
  lanes: [
    { service: "checkout-api", marks: [{ at: 14, tone: "ok" }, { at: 63, tone: "danger", label: P("#8805 失败", "#8805 failed") }, { at: 91, tone: "ok", label: "v2.8.1 · 14:03" }], alerts: [[92, 100]] },
    { service: "payment-gateway", marks: [{ at: 35, tone: "ok", label: "v1.14.0" }], alerts: [[81, 100]] },
    { service: "order-service", marks: [{ at: 48, tone: "ok" }, { at: 76, tone: "ok", label: "v3.2.4" }], alerts: [] },
    { service: "search-api", marks: [{ at: 10, tone: "ok" }], alerts: [] },
  ],
  axis: [P("昨天 14:00", "Yesterday 14:00"), "20:00", "02:00", "08:00", P("现在", "Now")],
  pipelines: [
    { id: "8812", brand: "gitlab", ref: "#8812", service: "checkout-api", title: "chore: tune db pool config", trigger: "李明", status: "success", stages: ["ok", "ok", "ok", "ok", "ok", "ok", "ok"], duration: P("14 分", "14 m"), time: "14:03" },
    { id: "sync", brand: "argo", ref: "sync", service: "checkout-api", title: P("同步到生产 · v2.8.1", "Sync to prod · v2.8.1"), trigger: P("自动", "auto"), status: "success", stages: ["ok"], duration: P("52 秒", "52 s"), time: "14:04" },
    { id: "5521", brand: "githubactions", ref: "#5521", service: "order-service", title: "fix: retry on 409", trigger: "周航", status: "success", stages: ["ok", "ok", "ok", "ok", "ok"], duration: P("9 分", "9 m"), time: "13:12" },
    { id: "8805", brand: "gitlab", ref: "#8805", service: "checkout-api", title: "feat: coupon stacking v2", trigger: "李明", status: "failed", stages: ["ok", "ok", "danger"], duration: P("6 分", "6 m"), time: "11:40" },
    { id: "231", brand: "jenkins", ref: "#231", service: "user-center", title: "release 2.1.1", trigger: "陈雨", status: "running", stages: ["ok", "ok", "accent", "neutral"], duration: P("4 分", "4 m"), time: "14:28" },
    { id: "8798", brand: "gitlab", ref: "#8798", service: "payment-gateway", title: "chore: bump sdk", trigger: "赵凯", status: "success", stages: ["ok", "ok", "ok", "ok", "ok", "ok", "ok"], duration: P("15 分", "15 m"), time: P("昨天", "Yesterday") },
  ],
  detail: {
    id: "8812",
    title: "#8812 · checkout-api v2.8.1",
    meta: P("platform/checkout · main · a3f9c21 · 李明 · 13:49 触发", "platform/checkout · main · a3f9c21 · Li Ming · triggered 13:49"),
    brand: "gitlab",
    stages: [
      { name: P("构建", "Build"), jobs: [{ name: "build", state: "ok" }, { name: "lint", state: "ok" }] },
      { name: P("测试", "Test"), jobs: [{ name: "unit", state: "ok" }, { name: "integration", state: "ok" }] },
      { name: P("扫描", "Scan"), jobs: [{ name: "SonarQube", state: "ok" }, { name: P("镜像扫描", "Image scan"), state: "ok", brand: "harbor" }] },
      { name: P("预发", "Staging"), jobs: [{ name: P("部署", "Deploy"), state: "ok", brand: "argo" }] },
      { name: P("审批", "Approval"), jobs: [{ name: "李明", state: "ok" }] },
      { name: P("生产", "Production"), jobs: [{ name: P("部署 14:03", "Deploy 14:03"), state: "ok", brand: "argo" }] },
    ],
    diff: { path: P("config/checkout-api.yaml · 1 个文件 · +1 −1", "config/checkout-api.yaml · 1 file · +1 −1"), minus: "-  connection_pool.max: 200", plus: "+  connection_pool.max: 50" },
    risk: { title: P("发布前风险评估 · 中风险", "Pre-release risk · medium"), meta: P("变更风险评估 v2 · 13:58 · 已通知李明，未阻断发布", "Change risk v2 · 13:58 · Li Ming notified, not blocked"), text: P("连接池上限下调 75%。历史上 2 次类似变更（INC-0987、INC-1012）导致故障；建议先在预发压测，或按 10% 灰度发布。", "Pool max reduced by 75%. Two similar changes (INC-0987, INC-1012) caused incidents; load-test on staging first or canary at 10%.") },
    alert: { text: P("发布后 2 分钟触发告警 checkout-api / HighErrorRate", "2 min after release: checkout-api / HighErrorRate fired"), incident: "INC-1042" },
  },
}));
