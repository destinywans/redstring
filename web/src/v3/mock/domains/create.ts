/** Mocks for the v4 create flows (incident, rule, route, inhibition, runbook, postmortem, knowledge source, service). */
import { mock, MockError } from "../router";
import { at } from "../time";
import { l } from "../../i18n";

const P = l;
const id = (p: string) => `${p}-${Math.floor(1000 + Math.random() * 8999)}`;
const need = (v: unknown, field: string) => {
  if (v === undefined || v === null || (typeof v === "string" && !v.trim()) || (Array.isArray(v) && !v.length)) throw new MockError(422, "invalid", `${field} is required`);
};

/* ---- incident ---- */
mock("GET", "/incidents/draft", () => ({
  title: P("checkout-api 5xx 升高（发布 v2.8.1 后）", "checkout-api 5xx rising (after v2.8.1)"),
  severity: "P1",
  severity_hint: P("P1：核心交易链路受影响", "P1: core trading path affected"),
  env: "prod",
  services: [{ id: "checkout-api", tier: "T0" }, { id: "payment-gateway", tier: "T0", ai: true }],
  impact: P("约 8.4% 的下单请求失败（基线 0.2%），从 14:05 开始；支付回调正常，暂无数据不一致。", "~8.4% of checkouts fail (baseline 0.2%) since 14:05; payment callbacks are fine, no data inconsistency so far."),
  alerts: [
    { id: "al-1", severity: "P1", text: "5xx > 1% · checkout-api", at: "14:05" },
    { id: "al-2", severity: "P2", text: "P99 > 1 s · checkout-api", at: "14:06" },
    { id: "al-3", severity: "P3", text: P("库存调用超时 > 5% · inventory-service", "Inventory timeouts > 5% · inventory-service"), at: "14:04" },
  ],
  commander: { name: "王晨", note: P("当前值班", "on call") },
  participants: ["李明", "张磊", "AI"],
  war_room: P("INC · checkout-api 5xx", "INC · checkout-api 5xx"),
}));
mock("POST", "/incidents", ({ body }) => {
  const b = (body ?? {}) as { title?: string; open_canvas?: boolean };
  need(b.title, "title");
  const incident = id("INC");
  return { id: incident, href: `/incidents`, investigation_id: b.open_canvas ? "INV-231" : undefined };
});

/* ---- alert rule ---- */
mock("POST", "/alert-rules/generate", ({ body }) => {
  need((body as { text?: string })?.text, "text");
  return {
    query: 'sum(rate(http_requests_total{job="checkout", code=~"5.."}[5m]))\n  / sum(rate(http_requests_total{job="checkout"}[5m]))',
    op: ">", threshold: 1, unit: "%", for_minutes: 5, severity: "P2", service: "checkout-api",
    summary: P("checkout-api 5xx 比例超过 1%（当前 $value）", "checkout-api 5xx ratio above 1% (now $value)"),
  };
});
const FIRE = [0.2, 0.2, 0.3, 0.2, 0.25, 0.2, 0.3, 0.2, 0.2, 0.3, 0.2, 0.25, 0.2, 0.3, 0.2, 0.2, 6.8, 8.1, 8.6, 8.2, 8.4, 8.0, 7.2, 0.4, 0.3, 0.2, 0.3, 0.2, 0.25, 0.2];
mock("POST", "/alert-rules/preview", ({ body }) => {
  const b = (body ?? {}) as { threshold?: number; query?: string };
  if (!b.query?.trim()) return { valid: false, error: P("查询为空", "Query is empty") };
  if ((b.query.match(/\(/g) ?? []).length !== (b.query.match(/\)/g) ?? []).length) return { valid: false, error: P("括号不匹配", "Unbalanced parentheses") };
  const t = Number(b.threshold ?? 1);
  const fires = FIRE.map((v, i) => (v > t ? i : -1)).filter((i) => i >= 0);
  const count = t < 0.3 ? 41 : t <= 2 ? 3 : t <= 8 ? 2 : 0;
  return {
    valid: true, series_count: 1, source: P("Prometheus · 生产", "Prometheus · prod"),
    values: FIRE, current: 8.4, fires: fires.length ? [fires[0], fires[fires.length - 1]] : null,
    backtest: { count, avg_minutes: 12, max_minutes: 26, overlaps: count ? ["INC-1042", "INC-1031"].slice(0, Math.min(2, count)) : [], days: ["9/18", "9/19", "9/20", "9/21", "9/22", "9/23", P("今天", "Today")], hits: count > 3 ? [5, 12, 20, 27, 40, 58, 66, 80, 95] : [27, 58, 95].slice(0, count), noise: count > 10 ? "high" : "low" },
    route: { path: [P("根路由", "Root"), P("交易平台", "Trading"), "P1 / P2"], receivers: [{ brand: "feishu", text: P("飞书「交易值班群」· 卡片 + @值班人", "Feishu “Trading on-call” · card + @on-call") }, { icon: "phone", text: P("值班电话 · 仅夜间 P1", "On-call phone · night P1 only") }] },
    similar: t < 2 ? { id: "rule-17", name: P("checkout-api 5xx > 2%", "checkout-api 5xx > 2%") } : null,
  };
});
mock("POST", "/alert-rules", ({ body }) => {
  const b = (body ?? {}) as { name?: string; query?: string; mode?: string };
  need(b.query, "query");
  return { id: id("rule"), status: b.mode === "dry_run" ? "dry_run" : b.mode === "draft" ? "draft" : "enabled" };
});
mock("POST", "/alert-rules/import", ({ body }) => {
  const text = String((body as { yaml?: string })?.yaml ?? "");
  const names = [...text.matchAll(/alert:\s*([\w-]+)/g)].map((m) => m[1]);
  if (!names.length) throw new MockError(422, "invalid", "no rules found");
  return { items: names.map((n, i) => ({ name: n, status: i === 1 ? "duplicate" : "new", note: i === 1 ? P("与现有规则重复", "Duplicates an existing rule") : P("将创建", "Will be created") })) };
});

/* ---- routes & inhibitions ---- */
mock("POST", "/notification-routes/test", ({ body }) => {
  const labels = ((body as { labels?: Record<string, string> })?.labels) ?? {};
  const hit = labels.team === "trade" && /P1|P2/.test(labels.severity ?? "");
  return { hit, text: hit ? P("命中本路由 → 飞书「交易值班群」+ 值班电话", "Matches this route → Feishu “Trading on-call” + on-call phone") : P("未命中 · 落到「交易平台」默认接收方", "No match · falls through to the Trading default receiver"), matched_24h: 37 };
});
mock("POST", "/notification-routes", ({ body }) => {
  need((body as { name?: string })?.name, "name");
  return { id: id("route") };
});
mock("POST", "/inhibitions/preview", () => ({
  suppressed: 214,
  per_day: [12, 31, 0, 44, 18, 0, 109],
  summary: P("9 次 NodeDown 期间 · 平均每次 23 条 · 最多 109 条（今天 node-7）", "9 NodeDown windows · 23 per window on average · up to 109 (node-7 today)"),
  samples: [
    { severity: "P2", name: "KubePodNotReady · node-7", suppressed: true },
    { severity: "P3", name: "KubePodCrashLooping · node-7", suppressed: true },
    { severity: "P2", name: "HighLatency · checkout-api · node-2", suppressed: false, reason: P("node 不同", "different node") },
  ],
}));
mock("POST", "/inhibitions", () => ({ id: id("inh") }));

/* ---- runbooks & postmortems ---- */
mock("POST", "/runbooks", ({ body }) => {
  const b = (body ?? {}) as { title?: string; source?: string };
  need(b.title, "title");
  return { id: "rb-new", source: b.source ?? "blank", status: "draft" };
});
mock("GET", "/runbooks/draft", () => ({
  title: P("下游超时与重试风暴", "Downstream timeouts and retry storms"),
  applies_to: ["checkout-api", "order-service", "inventory-service"],
  trigger: P("对下游的调用量 ÷ 业务请求量 > 1.5，且 5xx > 1%", "Downstream calls ÷ business requests > 1.5 and 5xx > 1%"),
  steps: [
    { kind: "check", title: P("确认是否被重试放大", "Confirm retries are amplifying"), body: P("对比对下游的调用量与下单量，查看重试日志占比。", "Compare downstream calls with orders and check the share of retry logs."), tool: { brand: "prometheus", text: "sum(rate(rpc_client_calls_total{target=\"inventory-service\"}[1m]))" } },
    { kind: "check", title: P("找出最先变慢的实例", "Find the instance that slowed first"), body: P("按实例看下游 P99 与磁盘 IO 等待，确认谁最先变慢。", "Check downstream P99 and disk IO wait per instance to see which slowed first."), tool: { brand: "tempo", text: "search_traces · POST /orders · error" } },
    { kind: "act", title: P("限制重试并打开熔断", "Limit retries and open the breaker"), body: P("把 inventory.retry.max_attempts 降为 1，打开 inventory.circuit_breaker。", "Set inventory.retry.max_attempts to 1 and turn inventory.circuit_breaker on."), approval: true },
    { kind: "verify", title: P("验证 180 秒", "Verify for 180 s"), body: P("5xx 回落到 0.5% 以下，库存调用量回到 1.3k/s 以下。", "5xx under 0.5%, inventory calls back under 1.3k/s.") },
  ],
  sources: [P("INC-1042 复盘", "INC-1042 postmortem"), P("INC-1031 复盘", "INC-1031 postmortem"), P("对话 C-320", "Chat C-320")],
}));
mock("GET", "/postmortems/candidates", () => ({
  items: [
    { id: "INC-1042", title: P("checkout-api 5xx 升高", "checkout-api 5xx rising"), severity: "P1", resolved: P("今天 14:23", "Today 14:23"), duration: P("18 分钟", "18 min"), evidence: 12 },
    { id: "INC-1039", title: P("Redis 内存使用率 90%", "Redis memory at 90%"), severity: "P2", resolved: "9/21", duration: P("1 小时 10 分钟", "1 h 10 min"), evidence: 7 },
    { id: "INC-1031", title: P("数据库主从切换导致超时", "Timeouts after DB failover"), severity: "P1", resolved: "9/19", duration: P("42 分钟", "42 min"), evidence: 15 },
  ],
}));
mock("GET", "/postmortems/draft", ({ query }) => ({
  incident: { id: String(query.incident ?? "INC-1042"), title: P("checkout-api 5xx 升高", "checkout-api 5xx rising"), severity: "P1", window: "14:05 – 14:23", started_at: at("14:05"), closed_at: at("14:23"), duration: P("18 分钟", "18 min") },
  sections: [
    { key: "summary", title: P("概要", "Summary"), text: P("inventory-03 磁盘变慢，checkout-api 超时后立即重试 3 次，把库存调用放大到 3.2 倍，整个库存服务被压满，下单 5xx 升至 8.4%，持续 18 分钟。重试降为 1 次并打开熔断后恢复。14:03 的发布与此无关。", "inventory-03's disk slowed down; checkout-api retried each timeout 3 times at once, amplifying inventory calls 3.2×, the inventory service saturated and checkout 5xx reached 8.4% for 18 minutes. Retrying once with the breaker on restored service. The 14:03 release was unrelated.") },
    { key: "impact", title: P("影响", "Impact"), text: P("约 1.2 万笔下单失败，其中 83% 用户重试成功；无资金损失，无数据不一致。", "~12k checkouts failed; 83% of users succeeded on retry. No money lost, no data inconsistency.") },
    { key: "root", title: P("根因", "Root cause"), text: P("库存调用的重试配置自 v2.6 起就是超时 300 ms、重试 3 次、无退避，也没有熔断；一台实例变慢就会被放大成整个服务的风暴。", "Since v2.6 inventory calls have used a 300 ms timeout, 3 retries, no backoff and no breaker, so one slow instance becomes a storm across the service.") },
  ],
  timeline: [
    { at: "14:02", text: P("inventory-03 磁盘 IO 等待升高", "inventory-03 disk IO wait rises"), tone: "warn" },
    { at: "14:03", text: P("发布 v2.8.1（无关）", "v2.8.1 released (unrelated)"), tone: "info" },
    { at: "14:05", text: P("告警：5xx > 1%", "Alert: 5xx > 1%"), tone: "danger" },
    { at: "14:09", text: P("AI 初查：派生 3 个分支", "AI triage: forked 3 branches"), tone: "accent" },
    { at: "14:17", text: P("确认：inventory-03 变慢，被重试放大成风暴", "Confirmed: inventory-03 slowed and retries made it a storm"), tone: "accent" },
    { at: "14:19", text: P("批准并执行 PRP-88：重试降为 1 次并打开熔断", "Approved and ran PRP-88: retry once, breaker on"), tone: "warn" },
    { at: "14:23", text: P("180 秒验证通过，事件解决", "180 s verification passed, resolved"), tone: "ok" },
  ],
  actions: [
    { text: P("库存调用改为指数退避，重试预算不超过 10%", "Exponential backoff on inventory calls, retry budget under 10%"), owner: "张磊", due: "9/27" },
    { text: P("库存服务开启离群实例摘除", "Outlier ejection for the inventory service"), owner: "李明", due: "9/30" },
    { text: P("更新 Runbook「下游超时与重试风暴」", "Update runbook “Retry storms”"), owner: "AI", due: P("已起草", "drafted") },
  ],
}));
mock("POST", "/postmortems", ({ body }) => {
  need((body as { incident_id?: string })?.incident_id, "incident_id");
  return { id: id("PM") };
});

/* ---- knowledge sources ---- */
mock("POST", "/knowledge-sources/test", ({ body }) => {
  const b = (body ?? {}) as { type?: string; secret_ref?: string };
  if (!b.secret_ref?.startsWith("vault:")) return { ok: false, message: P("请使用 Vault 引用（vault:…），不要粘贴明文凭据", "Use a Vault reference (vault:…); never paste plaintext credentials") };
  return { ok: true, message: P("连接成功 · 可读取 3 个空间、1,284 篇文档", "Connected · 3 spaces, 1,284 documents readable"), spaces: [{ id: "SRE", name: P("SRE 知识库", "SRE handbook"), docs: 412 }, { id: "TRADE", name: P("交易平台", "Trading platform"), docs: 638 }, { id: "ARCH", name: P("架构决策记录", "Architecture decisions"), docs: 234 }] };
});
mock("POST", "/knowledge-sources", ({ body }) => {
  need((body as { name?: string })?.name, "name");
  return { id: id("ks"), status: "syncing" };
});

/* ---- services ---- */
mock("GET", "/services/discover", () => ({
  cluster: "aiops-test",
  items: [
    { id: "recommendation-api", namespace: "trade", kind: "Deployment", replicas: 4, image: "harbor.internal/trade/recommendation-api:1.14.2", owner_hint: P("交易平台（来自 label team=trade）", "Trading (from label team=trade)"), tier_hint: "T1", matched: ["prometheus", "loki", "gitlab"], missing: ["tempo"] },
    { id: "coupon-service", namespace: "trade", kind: "Deployment", replicas: 2, image: "harbor.internal/trade/coupon-service:0.9.0", owner_hint: P("交易平台", "Trading"), tier_hint: "T2", matched: ["prometheus", "gitlab"], missing: ["loki", "tempo"] },
    { id: "risk-scorer", namespace: "risk", kind: "StatefulSet", replicas: 3, image: "harbor.internal/risk/risk-scorer:2.3.1", owner_hint: P("未识别", "Unknown"), tier_hint: "T2", matched: ["prometheus"], missing: ["loki", "tempo", "gitlab"] },
  ],
  registered: 12,
}));
mock("POST", "/services", ({ body }) => {
  const b = (body ?? {}) as { items?: unknown[] };
  need(b.items, "items");
  return { created: b.items!.length };
});
