import { mock, MockError } from "../router";
import { NOW, at } from "../time";
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
  { id: "inventory-service", name: "inventory-service", desc: P("库存查询与预占", "Stock lookup & reserve"), tier: "T0", team: P("交易平台", "Trading"), oncall: "周航", status: "warn", slo: { target: "99.9%", budget_left: 57 }, release: { brand: "gitlab", version: "v5.3.0", when: "9/16" }, alerts: 1, connectors: ["prometheus", "elasticsearch", "tempo", "gitlab"] },
  { id: "payment-gateway", name: "payment-gateway", desc: P("支付通道聚合", "Payment routing"), tier: "T0", team: P("支付", "Payments"), oncall: "赵凯", status: "ok", slo: { target: "99.95%", budget_left: 61 }, release: { brand: "gitlab", version: "v1.14.0", when: P("昨天", "yesterday") }, alerts: 0, connectors: ["prometheus", "elasticsearch", "gitlab", "argo"] },
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
  const items = SERVICES.filter((s) => (!q || s.id.includes(q)) && (!tier || s.tier === tier) && (!status || s.status === status)).map((s) => ({ ...s, unavailable: {} }));
  return {
    kpis: [
      { id: "n", label: P("服务", "Services"), value: "9", unknown: 0, sub: P("生产 9 · 预发 9", "prod 9 · staging 9"), series: [] },
      { id: "bad", label: P("异常", "Degraded"), value: "1", unknown: 0, tone: "danger", sub: P("checkout-api · 5xx 8.4%", "checkout-api · 5xx 8.4%"), series: [] },
      { id: "warn", label: P("有告警", "Alerting"), value: "2", unknown: 0, tone: "warn", sub: "inventory-service, inventory-worker", series: [] },
      { id: "budget", label: P("错误预算 < 50%", "Budget < 50%"), value: "1", unknown: 0, tone: "warn", sub: P("按当前速率 2 小时耗尽", "Exhausted in ~2 h at current burn"), series: [] },
      { id: "rel", label: P("本周发布", "Releases this week"), value: "14", unknown: 0, sub: P("失败 1 · 回滚 1", "1 failed · 1 rolled back"), series: [1, 2, 1, 3, 2, 4, 1] },
    ],
    items,
    facets: [
      { key: "team", title: P("团队", "Team"), items: [{ label: P("交易平台", "Trading"), count: 4, on: false }, { label: P("支付", "Payments"), count: 1, on: false }, { label: P("平台", "Platform"), count: 3, on: false }, { label: P("账号", "Accounts"), count: 1, on: false }] },
      { key: "tier", title: P("等级", "Tier"), items: [{ label: "T0", count: 4, on: tier === "T0" }, { label: "T1", count: 3, on: tier === "T1" }, { label: "T2", count: 2, on: tier === "T2" }] },
      { key: "status", title: P("状态", "Status"), items: [{ label: P("异常", "Degraded"), count: 1, on: status === "danger" }, { label: P("有告警", "Alerting"), count: 2, on: status === "warn" }, { label: P("正常", "Healthy"), count: 6, on: status === "ok" }] },
      { key: "env", title: P("环境", "Environment"), items: [{ label: P("生产", "Production"), count: 9, on: true }, { label: P("预发", "Staging"), count: 9, on: false }] },
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
    ai: { text: P("当前状态：异常。5xx 8.4%：库存服务的 inventory-03 先变慢，checkout-api 超时后立即重试 3 次，把库存调用放大到 3.2 倍（INV-231）。14:03 的发布没有改动库存调用；限制重试的提案正在等待复核。", "Status: degraded. 5xx at 8.4%: inventory-03 slowed first and checkout-api retried each timeout 3 times at once, amplifying inventory calls 3.2× (INV-231). The 14:03 release did not touch inventory calls; a proposal to limit retries is awaiting review."), investigation: "INV-231", proposal: "PRP-88", meta: P("由调查助手 v12 生成", "Generated by Investigator v12"), at: at("14:21") },
    signals: [
      { id: "rps", label: P("请求量", "Requests"), value: "1.2k", unit: P("次/秒", "/s"), delta: "+2%", delta_good: true, sub: P("与上周同期持平", "Flat vs last week"), series: base },
      { id: "err", label: P("错误率", "Error rate"), value: "8.4", unit: "%", delta: "+42×", delta_good: false, tone: "danger", sub: P("阈值 1%", "Threshold 1%"), series: [0.2, 0.3, 0.2, 0.3, 0.2, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 0.2, 0.3, 2.1, 5.6, 7.9, 8.4, 8.1, 8.6, 8.4] },
      { id: "p99", label: P("P99 延迟", "P99 latency"), value: "1.6", unit: "s", delta: "+760%", delta_good: false, tone: "warn", sub: P("SLO 目标 300 ms", "SLO 300 ms"), series: [0.19, 0.2, 0.19, 0.21, 0.2, 0.19, 0.2, 0.2, 0.21, 0.19, 0.2, 0.19, 0.2, 0.9, 1.3, 1.5, 1.6, 1.58, 1.62, 1.6] },
      { id: "cpu", label: P("饱和度 · CPU", "Saturation · CPU"), value: "71", unit: "%", delta: "+26%", delta_good: false, tone: "warn", sub: P("HPA 目标 70%", "HPA target 70%"), series: [40, 42, 41, 44, 43, 45, 44, 46, 45, 47, 46, 45, 48, 60, 68, 71, 70, 69, 71, 71] },
    ],
    slo: { availability: "99.87%", target: "99.9%", budget_left: 38, burn: [{ window: P("1 小时燃烧", "1 h burn"), value: "14.4×", tone: "danger", pct: 100 }, { window: P("6 小时", "6 h"), value: "3.1×", tone: "warn", pct: 42 }, { window: P("3 天", "3 d"), value: "0.9×", tone: "ok", pct: 12 }], note: P("按当前速率约 2 小时耗尽", "Exhausted in ~2 h at this rate") },
    changes: [
      { brand: "gitlab", title: "#8812 · v2.8.1 · feat: invoice title on checkout", meta: P("李明 · 成功", "Li Ming · success"), at: at("14:03"), tag: P("代码变更", "Code change") },
      { brand: "argo", title: P("Argo CD 同步 checkout-api → 生产", "Argo CD sync checkout-api → prod"), meta: P("自动 · Synced", "auto · Synced"), at: at("14:04") },
      { brand: "gitlab", title: "#8790 · v2.8.0 · feat: coupon stacking", at: at("10:00", -22), meta: P("李明 · 成功", "Li Ming · success") },
    ],
    deps: {
      upstream: [{ brand: "nginx", name: "api-gateway", status: "ok", meta: "1.2k/s", unavailable: {} }],
      downstream: [{ name: "inventory-service", status: "danger", meta: P("3.9k/s · 超时 31%", "3.9k/s · 31% timeouts"), unavailable: {} }, { name: "payment-gateway", status: "ok", meta: "P99 180 ms", unavailable: {} }, { brand: "postgresql", name: "orders-db", status: "ok", meta: P("等待 P95 6 ms", "wait P95 6 ms"), unavailable: {} }, { brand: "redis", name: "session-cache", status: "ok", meta: P("命中 96%", "hit 96%"), unavailable: {} }, { brand: "kafka", name: "order-events", status: "ok", meta: P("延迟 12 ms", "lag 12 ms"), unavailable: {} }],
    },
    tabs: { changes: 3, deps: 6, resilience: 5, runbooks: 4, incidents: 2 },
    unavailable: {},
  };
});

mock("GET", "/services/:id/resilience", () => ({
  synced: P("最近同步 1 分钟前", "Synced 1 min ago"),
  rollout: {
    title: P("v2.8.2 · 库存调用指数退避与重试预算", "v2.8.2 · backoff and retry budget for inventory calls"),
    status: P("灰度中", "Canary"),
    stable: P("稳定版本 v2.8.1（重试已限为 1 次）", "Stable v2.8.1 (retries limited to 1)"),
    steps: [{ weight: "5%", note: P("完成 · 10 分钟", "Done · 10 min"), state: "done" }, { weight: "25%", note: P("当前 · 分析中", "Current · analysing"), state: "current" }, { weight: "50%", note: P("分析通过后自动", "Auto after analysis"), state: "todo" }, { weight: "100%", note: P("人工确认", "Manual confirm"), state: "todo" }],
    split: { stable: 75, canary: 25 },
    analysis: [{ name: P("成功率", "Success rate"), value: "99.93%", threshold: "≥ 99.9%", ok: true }, { name: P("P99 延迟", "P99 latency"), value: "212 ms", threshold: "≤ 300 ms", ok: true }, { name: P("库存调用 ÷ 下单", "Inventory calls ÷ orders"), value: "1.03", threshold: "≤ 1.1", ok: true }],
    stable_series: series(30, 0.2, 0.05, 8, 21),
    canary_series: series(30, 0.18, 0.06, 8, 22),
    next: P("下一步约 6 分钟后自动推进", "Next step in ~6 min"),
    source: P("Argo Rollouts · rollouts/checkout-api · 同步于 1 分钟前", "Argo Rollouts · rollouts/checkout-api · synced 1 min ago"),
  },
  breakers: [
    { brand: "istio", upstream: "payment-gateway", mechanism: P("Istio · outlierDetection", "Istio · outlierDetection"), state: "closed", threshold: P("连续 5xx ≥ 5 · 驱逐 30 s", "5 consecutive 5xx · eject 30 s"), ejected: "0 / 4" },
    { brand: "sentinel", upstream: "risk-engine", mechanism: P("Sentinel · 慢调用比例", "Sentinel · slow-call ratio"), state: "closed", threshold: P("慢调用 ≥ 50%（RT > 1 s）", "Slow calls ≥ 50% (RT > 1 s)"), ejected: "0" },
    { brand: "", upstream: "inventory-service", mechanism: P("应用内重试（resilience4j）", "In-app retry (resilience4j)"), state: "none", threshold: P("超时 300 ms · 重试 3 次 · 无退避 · 无熔断", "300 ms timeout · 3 retries · no backoff · no breaker"), ejected: "—" },
  ],
  breaker_timeline: [{ from: 0, to: 100, state: "closed" }],
  breaker_note: P("inventory-service 没有熔断：超时后立即重试 3 次、没有退避，下游一变慢调用量就翻倍。PRP-88 提议重试降为 1 次并打开熔断，正在等待复核；平台只给出建议，执行要经过批准。", "inventory-service has no breaker: timeouts are retried 3 times at once with no backoff, so any slowdown doubles the calls. PRP-88 proposes one retry with the breaker on and is awaiting review; the platform only advises, and running it needs approval."),
  limits: [
    { brand: "apisix", route: "POST /orders", mechanism: "APISIX · limit-count", limit: "2,000 r/s", used: "1,210", pct: 61, rejected: 0 },
    { brand: "apisix", route: "POST /payments", mechanism: "APISIX · limit-count", limit: "800 r/s", used: "420", pct: 53, rejected: 0 },
    { brand: "kong", route: "GET /cart", mechanism: "Kong · rate-limiting", limit: "5,000 r/s", used: "820", pct: 16, rejected: 0 },
    { brand: "nginx", route: "/internal/*", mechanism: "Nginx · limit_req", limit: "100 r/s", used: "12", pct: 12, rejected: 0 },
  ],
  limit_note: P("最近 1 小时 429 为 0：本次 5xx 与限流无关（可作为排除证据）", "Zero 429s in the last hour: the 5xx is unrelated to rate limiting (usable as exclusion evidence)"),
  flags: [
    { brand: "unleash", key: "checkout.recommendations", desc: P("14:10 王晨在 Unleash 中关闭 · 减少下单页的库存查询", "Turned off by Wang Chen at 14:10 in Unleash · fewer inventory lookups on checkout"), on: false, tag: { text: P("降级中", "Degraded"), tone: "warn" }, action: P("提议恢复", "Propose restore") },
    { brand: "unleash", key: "checkout.coupon-stacking", desc: P("优惠叠加 · 100% 用户", "Coupon stacking · 100% of users"), on: true, tag: { text: P("生产", "Prod"), tone: "neutral" }, action: P("提议关闭", "Propose disable") },
    { brand: "launchdarkly", key: "payment.fallback-provider", desc: P("备用支付通道 · 10% 流量", "Fallback payment provider · 10% traffic"), on: true, tag: { text: P("灰度 10%", "10% rollout"), tone: "accent" }, action: P("提议调整", "Propose change") },
  ],
  hpa: { min: 4, max: 12, current: 6, desired: 8, replicas: Array(36).fill(60), desired_series: [...Array(17).fill(60), ...Array(19).fill(80)], cpu: bump(series(36, 46, 4, 12, 31), 16, 27, 1.55), target: 70, note: P("14:06 起期望 8 个副本，实际 6 个：2 个 Pod 因节点资源不足处于 Pending。扩容对重试风暴帮助有限：副本越多，发出的重试越多。", "Since 14:06 desired 8, actual 6: 2 pods pending on node capacity. Scaling helps little with a retry storm: more replicas send more retries.") },
  ai: [
    P("**限流未触发**（429 = 0），可排除限流导致 5xx。", "**Rate limits did not fire** (429 = 0) — rate limiting is ruled out."),
    P("payment-gateway 熔断一直关闭、错误率正常：支付**不在这次故障链路上**。", "payment-gateway's breaker stayed closed and its errors are normal: payments are **not on this failure path**."),
    P("recommendations 已降级、HPA 扩容受阻，都没有缓解重试风暴：根子在无退避的重试。", "Degrading recommendations and the blocked HPA scale-out did not relieve the storm: the root is retrying without backoff."),
    P("长期修复 v2.8.2（指数退避与重试预算）灰度 25%，分析指标正常，可继续推进。", "The long-term fix v2.8.2 (backoff and retry budget) is at 25% canary with healthy analysis — safe to continue."),
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
    { id: "g-ful", label: "fulfillment", zone: " · zone-b", x: 500, y: 568, w: 204, h: 196 },
  ],
  nodes: [
    { id: "api-gateway", name: "api-gateway", brand: "nginx", sub: "Ingress · 2.0k/s", status: "warn", group: "g-gw", x: 34, y: 340, impacted: true },
    { id: "checkout-api", name: "checkout-api", brand: "kubernetes", sub: "Deployment · 6/6", status: "danger", group: "g-co", x: 264, y: 250, impacted: true },
    { id: "cart-service", name: "cart-service", brand: "kubernetes", sub: "Deployment · 4/4", status: "ok", group: "g-co", x: 264, y: 420, impacted: false },
    { id: "payment-gateway", name: "payment-gateway", sub: P("P99 180 ms · 错误率正常", "P99 180 ms · errors normal"), status: "ok", group: "g-pay", x: 514, y: 90, impacted: false },
    { id: "risk-engine", name: "risk-engine", sub: "Deployment · 3/3", status: "ok", group: "g-pay", x: 514, y: 190, impacted: false },
    { id: "orders-db", name: "orders-db", brand: "postgresql", sub: P("等待 P95 6 ms", "wait P95 6 ms"), status: "ok", group: "g-data", x: 514, y: 312, impacted: false },
    { id: "session-cache", name: "session-cache", brand: "redis", sub: P("命中 96%", "hit 96%"), status: "ok", group: "g-data", x: 514, y: 392, impacted: false },
    { id: "order-events", name: "order-events", brand: "kafka", sub: P("延迟 12 ms", "lag 12 ms"), status: "ok", group: "g-data", x: 514, y: 472, impacted: false },
    { id: "inventory-worker", name: "inventory-worker", brand: "kubernetes", sub: P("消费者 · 积压 12k", "consumer · lag 12k"), status: "warn", group: "g-ful", x: 514, y: 612, impacted: false },
    { id: "inventory-service", name: "inventory-service", brand: "kubernetes", sub: P("inventory-03 P99 350 ms", "inventory-03 P99 350 ms"), status: "danger", group: "g-ful", x: 514, y: 696, impacted: true },
  ],
  edges: [
    { id: "e1", from: "api-gateway", to: "checkout-api", label: "5xx 8.4%", tone: "danger", fast: true, width: 2, path: "M206 364 C 236 364, 236 274, 264 274", lx: 236, ly: 318 },
    { id: "e2", from: "api-gateway", to: "cart-service", label: "800/s", tone: "ok", width: 1.5, path: "M206 364 C 236 364, 236 444, 264 444", lx: 236, ly: 406 },
    { id: "e3", from: "checkout-api", to: "payment-gateway", label: "420/s · 0.1%", tone: "ok", width: 1.5, path: "M436 274 C 476 274, 476 114, 514 114", lx: 476, ly: 192 },
    { id: "e4", from: "checkout-api", to: "orders-db", label: "500 qps", tone: "ok", width: 1.5, path: "M436 274 C 476 274, 476 336, 514 336", lx: 476, ly: 305 },
    { id: "e5", from: "checkout-api", to: "session-cache", label: "2.3k/s", tone: "ok", width: 1.5, path: "M436 274 C 476 274, 476 416, 514 416", lx: 476, ly: 345 },
    { id: "e6", from: "checkout-api", to: "order-events", label: "380/s", tone: "ok", width: 1.5, path: "M436 274 C 476 274, 476 496, 514 496", lx: 476, ly: 385 },
    { id: "e7", from: "cart-service", to: "session-cache", label: "1.6k/s", tone: "ok", width: 1.5, path: "M436 444 C 476 444, 476 416, 514 416", lx: 476, ly: 431 },
    { id: "e8", from: "payment-gateway", to: "risk-engine", label: "420/s", tone: "ok", width: 1.5, path: "M600 138 L 600 190", lx: 642, ly: 164 },
    { id: "e9", from: "order-events", to: "inventory-worker", label: P("积压 12k", "lag 12k"), tone: "warn", width: 1.5, path: "M600 520 L 600 612", lx: 642, ly: 566 },
    { id: "e10", from: "checkout-api", to: "inventory-service", label: P("3.9k/s · 68% 重试", "3.9k/s · 68% retries"), tone: "danger", fast: true, width: 2.5, path: "M436 274 C 476 274, 476 720, 514 720", lx: 476, ly: 497 },
  ],
  blast: P("3 个服务 · 1 个接口 · 约 38% 的用户请求经过", "3 services · 1 endpoint · ~38% of user requests"),
  tree: [
    { depth: 0, kind: P("集群 · 18 节点", "Cluster · 18 nodes"), name: "prod-sh-1", brand: "kubernetes", status: "danger", replicas: "64", cpu: 58, mem: 61, alerts: 4, open: true },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "checkout", status: "danger", replicas: "10", cpu: 66, mem: 58, alerts: 3, open: true },
    { depth: 2, kind: "Deployment", name: "checkout-api", brand: "kubernetes", status: "danger", replicas: "6/6", cpu: 71, mem: 58, alerts: 3, open: true, hit: true },
    { depth: 3, kind: "Pod · 5xx 9.2%", name: "checkout-api-7f9c-4", status: "danger", replicas: "1/1", cpu: 78, mem: 60 },
    { depth: 3, kind: "Pod · 5xx 8.9%", name: "checkout-api-7f9c-1", status: "danger", replicas: "1/1", cpu: 74, mem: 59 },
    { depth: 3, kind: "Pod · 5xx 1.4%", name: "checkout-api-7f9c-5", status: "warn", replicas: "1/1", cpu: 61, mem: 55 },
    { depth: 2, kind: "Deployment", name: "cart-service", brand: "kubernetes", status: "ok", replicas: "4/4", cpu: 32, mem: 41, alerts: 0, open: false },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "data", status: "ok", replicas: "8", cpu: 44, mem: 72, alerts: 0, open: true },
    { depth: 2, kind: "StatefulSet", name: "orders-db", brand: "postgresql", status: "ok", replicas: "2/2", cpu: 38, mem: 64, alerts: 0, open: false },
    { depth: 2, kind: "StatefulSet", name: "session-cache", brand: "redis", status: "ok", replicas: "3/3", cpu: 21, mem: 48, alerts: 0, open: false },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "payment", status: "ok", replicas: "6", cpu: 52, mem: 49, alerts: 0, open: false },
    { depth: 1, kind: P("命名空间", "Namespace"), name: "fulfillment", status: "danger", replicas: "9", cpu: 63, mem: 55, alerts: 2, open: true },
    { depth: 2, kind: "Deployment", name: "inventory-service", brand: "kubernetes", status: "danger", replicas: "4/4", cpu: 81, mem: 57, alerts: 1, open: true, hit: true },
    { depth: 3, kind: P("Pod · P99 350 ms · 磁盘 IO 等待 180 ms", "Pod · P99 350 ms · disk IO wait 180 ms"), name: "inventory-service-03", status: "danger", replicas: "1/1", cpu: 64, mem: 58 },
    { depth: 3, kind: P("Pod · 超时 27%", "Pod · 27% timeouts"), name: "inventory-service-01", status: "warn", replicas: "1/1", cpu: 88, mem: 56 },
    { depth: 2, kind: "Deployment", name: "inventory-worker", brand: "kubernetes", status: "warn", replicas: "5/5", cpu: 47, mem: 51, alerts: 1, open: false },
  ],
  sankey: {
    cols: [
      [{ id: "gw", label: "api-gateway · 2.0k/s", value: 2000, color: "var(--c1)" }],
      [{ id: "co", label: "checkout-api · 1.2k/s", value: 1200, color: "var(--c3)" }, { id: "ca", label: "cart-service · 800/s", value: 800, color: "var(--c2)" }],
      [{ id: "inv", label: P("inventory-service · 68% 重试", "inventory-service · 68% retries"), value: 700, color: "var(--c3)" }, { id: "pay", label: "payment-gateway · 420/s", value: 420, color: "var(--c4)" }, { id: "sc", label: "session-cache · 880/s", value: 880, color: "var(--c5)" }],
    ],
    links: [
      { from: "gw", to: "co", value: 1200, color: "var(--c3)" }, { from: "gw", to: "ca", value: 800, color: "var(--c2)" }, { from: "co", to: "inv", value: 500, color: "var(--c3)" },
      { from: "co", to: "pay", value: 420, color: "var(--c4)" }, { from: "co", to: "sc", value: 280, color: "var(--c5)" }, { from: "ca", to: "sc", value: 600, color: "var(--c5)" }, { from: "ca", to: "inv", value: 200, color: "var(--c2)" },
    ],
  },
  detail: {
    id: "orders-db", name: "orders-db", brand: "postgresql", meta: P("PostgreSQL 15.4 · 主从 · prod-sh-1", "PostgreSQL 15.4 · primary/replica · prod-sh-1"),
    conns: { used: 212, max: 800 }, from_service: { used: 38, max: 200, service: "checkout-api" },
    wait: series(N, 6, 1.5, 10, 9),
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
  const calls = bump(series(N, 1.21, 0.03, 10, 9), REL + 1, RB, 3.2);
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
    window: {
      label_times: Array.from({ length: N }, (_, i) => at(hhmm(13 * 60 + 35 + Math.round(i * 1.714)))),
      tick_times: ["13:35", "13:50", "14:05", "14:20", "14:35"].map((t) => at(t)),
      release_at: REL, rollback_at: RB, incident: [17, 28], incident_start_at: at("14:05"), incident_end_at: at("14:22"),
    },
    kpis: [
      { id: "rps", label: P("请求量", "Requests"), value: "1.21k", unit: P("次/秒", "/s"), delta: "+2%", delta_good: true, sub: P("与上周同期持平", "Flat vs last week"), series: rps },
      { id: "err", label: P("5xx 比例", "5xx ratio"), value: "8.4", unit: "%", delta: "+42×", delta_good: false, tone: "danger", sub: P("阈值 1%", "Threshold 1%"), series: err },
      { id: "p99", label: P("P99 延迟", "P99 latency"), value: "1.64", unit: "s", delta: "+760%", delta_good: false, tone: "warn", sub: P("SLO 目标 300 ms", "SLO 300 ms"), series: p99 },
      { id: "calls", label: P("库存调用量", "Inventory calls"), value: "3.9k", unit: P("次/秒", "/s"), delta: "+3.2×", delta_good: false, tone: "danger", sub: P("下单量持平 · 68% 为重试", "Orders flat · 68% retries"), series: calls },
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
    unavailable: {},
  };
});
mock("POST", "/metrics/ask", () => ({ conversation_id: "C-320" }));

/* traces: see ./traces.ts */

/* ---------------- logs ---------------- */
mock("GET", "/logs", ({ query }) => {
  const q = String(query.q ?? "");
  const rows = [
    { time: "14:12:58.214", level: "ERROR", service: "checkout-api", message: "inventory.reserve timeout after 300ms, retrying (1/3) instance=inventory-03", trace: "4f2a9c17" },
    { time: "14:12:58.190", level: "WARN", service: "checkout-api", message: "slow request POST /orders 1.2s", trace: "4f2a9c17" },
    { time: "14:12:57.902", level: "ERROR", service: "checkout-api", message: "retry 3/3 failed: inventory.reserve · POST /orders 502", trace: "a07c11de" },
    { time: "14:12:57.655", level: "INFO", service: "checkout-api", message: "payment authorize ok order=8812731", trace: "b51e0a93" },
    { time: "14:12:57.120", level: "ERROR", service: "checkout-api", message: "inventory.reserve timeout after 300ms, retrying (2/3) instance=inventory-01", trace: "91e2b8f0" },
    { time: "14:12:56.874", level: "WARN", service: "inventory-service", message: "disk io wait 182ms device=nvme0n1 host=inventory-03" },
    { time: "14:12:56.511", level: "INFO", service: "checkout-api", message: "cache hit ratio 0.96" },
    { time: "14:12:56.230", level: "ERROR", service: "checkout-api", message: "retry 3/3 failed: inventory.reserve · POST /orders 502", trace: "7fa01b62" },
    { time: "14:12:55.998", level: "INFO", service: "order-service", message: "order status updated id=8812719 status=PAID" },
    { time: "14:12:55.702", level: "ERROR", service: "checkout-api", message: "inventory.reserve timeout after 300ms, retrying (3/3) instance=inventory-04", trace: "e3d4c7a8" },
    { time: "14:12:55.410", level: "WARN", service: "checkout-api", message: "slow request POST /orders 1.2s", trace: "e3d4c7a8" },
  ];
  const levels = String(query.levels ?? "ERROR,WARN,INFO").split(",");
  return {
    total: "38,214",
    errors: "3,547",
    histogram: [[3, 20, 1], [3, 21, 1], [2, 22, 1], [3, 20, 2], [3, 21, 1], [2, 23, 1], [3, 22, 1], [3, 21, 1], [2, 20, 1], [3, 22, 1], [3, 21, 1], [2, 22, 1], [9, 25, 18], [14, 26, 42], [16, 25, 51], [15, 26, 48], [16, 24, 50], [15, 25, 49], [16, 26, 51], [14, 25, 47]].map(([w, i, e]) => [e, w, i]),
    items: rows.filter((r) => levels.includes(r.level) && (!q || r.message.includes(q) || r.service.includes(q))),
    patterns: [
      { template: "inventory.reserve timeout after <*>ms, retrying (<*>/3) instance=<*>", level: "ERROR", count: "2,416", trend: [0, 0, 0, 0, 0, 2, 8, 10, 11, 10], new: true },
      { template: "retry 3/3 failed: inventory.reserve · <*> 502", level: "ERROR", count: "1,131", trend: [0, 0, 0, 0, 1, 3, 7, 9, 9, 9], new: true },
      { template: "slow request <*> <*>s", level: "WARN", count: "2,406", trend: [2, 2, 3, 2, 3, 8, 12, 13, 12, 13] },
      { template: "payment authorize ok order=<*>", level: "INFO", count: "18,220", trend: [20, 21, 20, 22, 21, 18, 17, 17, 18, 17] },
    ],
    unavailable: {},
  };
});

/* ---------------- changes ---------------- */
// Changes & releases, GitLab mode (D11 §11.1), example data. checkout-api: two failed deploys, then #3 at 14:03 two
// minutes before INC-1042; cluster revision 8 came from the lab's bad-release drill, not GitLab. Other services show
// the states: payment-gateway GitLab down, user-center credential rejected, order-service no confirmed code project,
// inventory-service without alert coverage, workload or the pipelines tool; checkout-api over 7 days is too many.
const SHA = (s: string) => s.padEnd(40, "0");
const GL_USERS = { li: { username: "li.ming" }, zhou: { username: "zhou.hang" }, wang: { username: "wang.chen" } };
const glDeploy = (iid: number, status: "success" | "failed", sha: string, pipeline: number, mr: number | null, user: { username: string }, created: string, finished: string, rev: string | null, adjacent: { incident_id: string; offset_seconds: number; basis: "time_adjacent" }[] = []) => ({
  id: `gitlab:deployment:4:${100 + iid}`, deployment_id: 100 + iid, iid, status, environment: "aiops-lab", ref: "main", sha: SHA(sha), short_sha: sha,
  pipeline_id: pipeline, job: { id: 9000 + iid, name: "deploy:aiops-lab" }, user, created_at: created, finished_at: finished,
  web_url: `https://gitlab.lab.internal/aiops-lab/checkout-api/-/environments/1/deployments/${iid}`, merge_request_iid: mr, kubernetes_revision: rev, adjacent_alerts: adjacent,
});
const glPipeline = (id: number, status: string, sha: string, mr: number | null, deployIds: number[], created: string, updated: string, source = "push") => ({
  id: `gitlab:pipeline:4:${id}`, pipeline_id: id, status, ref: "main", sha: SHA(sha), short_sha: sha, source, created_at: created, updated_at: updated,
  web_url: `https://gitlab.lab.internal/aiops-lab/checkout-api/-/pipelines/${id}`, merge_request_iid: mr, deployment_ids: deployIds,
});
const glMr = (iid: number, title: ReturnType<typeof P>, sha: string, author: { username: string }, merged: string, pipelines: number[], deploys: number[]) => ({
  id: `gitlab:mr:4:${iid}`, iid, title, state: "merged", source_branch: `feature/mr-${iid}`, target_branch: "main", author, merged_by: GL_USERS.wang,
  created_at: merged, merged_at: merged, closed_at: null, sha: SHA(sha), merge_commit_sha: SHA(sha), squash_commit_sha: null,
  web_url: `https://gitlab.lab.internal/aiops-lab/checkout-api/-/merge_requests/${iid}`, pipeline_ids: pipelines, deployment_ids: deploys,
});
const GL_KPI = (id: string, zh: string, en: string, value: number, series: number[]) => ({ id, label: P(zh, en), value: String(value), series });
const GL_PROJECT = { connector_id: "gitlab-lab", project_id: "4", path: "aiops-lab/checkout-api", web_url: "https://gitlab.lab.internal/aiops-lab/checkout-api", deploy_environment: "aiops-lab", refs: ["main"] };
mock("GET", "/changes", ({ query }) => {
  const service = String(query.service_id ?? "");
  if (service === "payment-gateway") throw new MockError(503, "source_unavailable", "GitLab is unavailable");
  if (service === "user-center") throw new MockError(503, "credential_unavailable", "The GitLab credential is unavailable or was rejected");
  if (service === "order-service") throw new MockError(503, "change_source_not_configured", "No GitLab change source is configured for this service");
  if (service === "checkout-api" && query.range === "7d") throw new MockError(413, "change_window_too_large", "More changes than one bounded read; choose a shorter range");
  const end = NOW;
  const start = end - 24 * 3600_000;
  const base = {
    service_id: service, environment_id: "prod", source: "gitlab", window: { start_at: new Date(start).toISOString(), end_at: new Date(end).toISOString() },
    project: { ...GL_PROJECT, path: `aiops-lab/${service}` }, series_start_at: new Date(start).toISOString(), series_step_seconds: 3600, captured_at: new Date(end).toISOString(), unavailable: {},
  };
  if (service !== "checkout-api" && service !== "inventory-service")
    return { ...base, kpis: [GL_KPI("deployments", "成功部署", "Successful deploys", 0, []), GL_KPI("failed_deployments", "失败部署", "Failed deploys", 0, []), GL_KPI("pipelines", "流水线", "Pipelines", 0, []), GL_KPI("merged_merge_requests", "已合并 MR", "Merged MRs", 0, [])], deployments: [], pipelines: [], merge_requests: [], alerts: [], kubernetes_revisions: [], coverage: { gitlab: "complete", alerts: "complete", kubernetes: "history_limited" } };
  const deployments = [
    glDeploy(3, "success", "a3f9c21b", 8812, 41, GL_USERS.li, at("13:57"), at("14:03"), "7", [{ incident_id: "INC-1042", offset_seconds: 120, basis: "time_adjacent" }]),
    glDeploy(2, "failed", "9b1e0d47", 8805, 40, GL_USERS.li, at("11:36"), at("11:40"), null),
    glDeploy(1, "failed", "9b1e0d47", 8801, 40, GL_USERS.zhou, at("08:52"), at("08:58"), null),
  ];
  const pipelines = [
    glPipeline(8812, "success", "a3f9c21b", 41, [103], at("13:51"), at("14:03")),
    glPipeline(8810, "success", "a3f9c21b", 41, [], at("13:40"), at("13:47"), "merge_request_event"),
    glPipeline(8809, "success", "77c0a1e5", 39, [], at("12:24"), at("12:31")),
    glPipeline(8805, "failed", "9b1e0d47", 40, [102], at("11:31"), at("11:40")),
    glPipeline(8801, "failed", "9b1e0d47", 40, [101], at("08:47"), at("08:58")),
    glPipeline(8798, "success", "5e2c88f0", 38, [], at("18:02", -1), at("18:10", -1)),
  ];
  const mergeRequests = [
    glMr(41, P("库存调用只重试 1 次，并打开熔断", "Retry inventory once and enable the breaker"), "a3f9c21b", GL_USERS.li, at("13:50"), [8810, 8812], [103]),
    glMr(40, P("优惠券叠加 v2", "Coupon stacking v2"), "9b1e0d47", GL_USERS.li, at("08:46"), [8801, 8805], [101, 102]),
    glMr(39, P("补充下单埋点", "Add checkout metrics"), "77c0a1e5", GL_USERS.wang, at("12:22"), [8809], []),
    glMr(38, P("升级支付 SDK", "Bump the payment SDK"), "5e2c88f0", GL_USERS.zhou, at("18:00", -1), [8798], []),
  ];
  if (service === "inventory-service")
    return {
      ...base,
      kpis: [GL_KPI("deployments", "成功部署", "Successful deploys", 1, [0, 1]), GL_KPI("failed_deployments", "失败部署", "Failed deploys", 0, [0, 0]), GL_KPI("merged_merge_requests", "已合并 MR", "Merged MRs", 1, [0, 1])],
      deployments: [glDeploy(7, "success", "c0ffee12", 7311, 12, GL_USERS.zhou, at("09:10"), at("09:16"), null)],
      pipelines: null,
      merge_requests: [glMr(12, P("库存预占超时改为 300 ms", "Reserve timeout to 300 ms"), "c0ffee12", GL_USERS.zhou, at("09:02"), [7311], [107])],
      alerts: null, kubernetes_revisions: null,
      coverage: { gitlab: "complete", alerts: "unknown", kubernetes: "not_configured" },
      unavailable: { pipelines: "not_configured", "kpis.pipelines": "not_configured", alerts: "not_configured", kubernetes_revisions: "not_configured" },
    };
  return {
    ...base,
    kpis: [GL_KPI("deployments", "成功部署", "Successful deploys", 1, [0, 0, 1]), GL_KPI("failed_deployments", "失败部署", "Failed deploys", 2, [0, 2, 0]), GL_KPI("pipelines", "流水线", "Pipelines", 6, [1, 2, 3]), GL_KPI("merged_merge_requests", "已合并 MR", "Merged MRs", 4, [1, 1, 2])],
    deployments, pipelines, merge_requests: mergeRequests,
    alerts: [{ incident_id: "INC-1042", title: P("checkout-api 5xx 升高", "checkout-api 5xx rising"), href: "/incidents/INC-1042", started_at: at("14:05"), ended_at: at("14:22") }],
    kubernetes_revisions: [
      { revision: "8", created_at: at("14:20"), change_cause: "lab drill bad-release", gitlab_pipeline_id: null },
      { revision: "7", created_at: at("14:03"), change_cause: "gitlab pipeline 8812 a3f9c21b", gitlab_pipeline_id: 8812 },
    ],
    coverage: { gitlab: "complete", alerts: "complete", kubernetes: "history_limited" },
  };
});
mock("GET", "/changes/pipelines/:id", ({ params }) => {
  const id = Number(params.id);
  const failed = id === 8805 || id === 8801;
  const job = (jid: number, name: string, status: string, manual = false) => ({ id: jid, name, status, manual, started_at: at("13:52"), finished_at: at("13:55"), duration_seconds: 140, web_url: `https://gitlab.lab.internal/aiops-lab/checkout-api/-/jobs/${jid}` });
  const commit: Record<number, [ReturnType<typeof P>, string, number, number, number, string[]]> = {
    8812: [P("库存调用只重试 1 次，并打开熔断", "Retry inventory once and enable the breaker"), "a3f9c21b", 3, 86, 12, ["src/checkout/inventory/retry.go", "src/checkout/inventory/breaker.go", "config/retry.yaml"]],
    8805: [P("优惠券叠加 v2", "Coupon stacking v2"), "9b1e0d47", 5, 210, 34, ["src/checkout/coupon/stack.go", "src/checkout/coupon/stack_test.go"]],
    8801: [P("优惠券叠加 v2", "Coupon stacking v2"), "9b1e0d47", 5, 210, 34, ["src/checkout/coupon/stack.go", "src/checkout/coupon/stack_test.go"]],
  };
  const c = commit[id] ?? [P("补充下单埋点", "Add checkout metrics"), "77c0a1e5", 2, 18, 0, ["src/checkout/metrics.go"]];
  return {
    id: `gitlab:pipeline:4:${id}`, pipeline_id: id, status: failed ? "failed" : "success", ref: "main", sha: SHA(c[1]), source: "push", user: GL_USERS.li,
    created_at: at("13:51"), started_at: at("13:51"), finished_at: at("13:57"), duration_seconds: 372, web_url: `https://gitlab.lab.internal/aiops-lab/checkout-api/-/pipelines/${id}`,
    stages: [
      { name: P("构建", "build"), status: "success", jobs: [job(id * 10 + 1, "build", "success"), job(id * 10 + 2, "lint", "success")] },
      { name: P("测试", "test"), status: failed ? "failed" : "success", jobs: [job(id * 10 + 3, "unit", "success"), job(id * 10 + 4, "integration", failed ? "failed" : "success")] },
      { name: P("部署", "deploy"), status: failed ? "skipped" : "success", jobs: [job(id * 10 + 5, "deploy:aiops-lab", failed ? "skipped" : "success", true)] },
    ],
    commit: { sha: SHA(c[1]), title: c[0], authored_at: at("13:40"), parent_ids: [SHA("5e2c88f0")], stats: { files: c[2], additions: c[3], deletions: c[4] }, paths: c[5] },
    merge_request_iid: id === 8812 || id === 8810 ? 41 : id === 8809 ? 39 : id === 8798 ? 38 : 40,
    adjacent_alerts: id === 8812 ? [{ incident_id: "INC-1042", offset_seconds: 120, basis: "time_adjacent" }] : [],
    unavailable: {},
  };
});
