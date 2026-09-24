/** Mocks for the v4 create flows (incident, rule, route, inhibition, runbook, postmortem, knowledge source, service). */
import { mock, MockError } from "../router";
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
    { id: "al-3", severity: "P3", text: P("连接等待 > 200 ms · orders-db", "Conn wait > 200 ms · orders-db"), at: "14:04" },
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
  title: P("数据库连接池耗尽", "Database connection pool exhaustion"),
  applies_to: ["checkout-api", "order-service", "payment-gateway"],
  trigger: P("连接等待 P95 > 200 ms，且 5xx > 1%", "Conn wait P95 > 200 ms and 5xx > 1%"),
  steps: [
    { kind: "check", title: P("确认连接池是否被打满", "Confirm the pool is saturated"), body: P("查看连接等待 P95 与活跃连接数。", "Check conn wait P95 and active connections."), tool: { brand: "prometheus", text: "db_pool_wait_seconds{quantile=\"0.95\"}" } },
    { kind: "check", title: P("对比最近配置变更", "Compare recent config changes"), body: P("查看最近 2 小时的发布与配置差异。", "Look at releases and config diffs in the last 2 hours."), tool: { brand: "gitlab", text: "list_changes · 2h" } },
    { kind: "act", title: P("回滚连接池配置", "Roll back the pool config"), body: P("将 connection_pool.max 恢复到上一版本的值。", "Restore connection_pool.max to the previous value."), approval: true },
    { kind: "verify", title: P("验证 180 秒", "Verify for 180 s"), body: P("5xx 回落到 0.5% 以下，连接等待 P95 < 50 ms。", "5xx under 0.5%, conn wait P95 < 50 ms.") },
  ],
  sources: [P("INC-1042 复盘", "INC-1042 postmortem"), P("INC-1031 复盘", "INC-1031 postmortem"), P("对话 C-320", "Chat C-320")],
}));
mock("GET", "/postmortems/candidates", () => ({
  items: [
    { id: "INC-1042", title: P("checkout-api 5xx 升高", "checkout-api 5xx rising"), severity: "P1", resolved: P("今天 14:31", "Today 14:31"), duration: P("26 分钟", "26 min"), evidence: 12 },
    { id: "INC-1039", title: P("Redis 内存使用率 90%", "Redis memory at 90%"), severity: "P2", resolved: "9/21", duration: P("1 小时 10 分钟", "1 h 10 min"), evidence: 7 },
    { id: "INC-1031", title: P("数据库主从切换导致超时", "Timeouts after DB failover"), severity: "P1", resolved: "9/19", duration: P("42 分钟", "42 min"), evidence: 15 },
  ],
}));
mock("GET", "/postmortems/draft", ({ query }) => ({
  incident: { id: String(query.incident ?? "INC-1042"), title: P("checkout-api 5xx 升高", "checkout-api 5xx rising"), severity: "P1", window: "14:05 – 14:31", duration: P("26 分钟", "26 min") },
  sections: [
    { key: "summary", title: P("概要", "Summary"), text: P("14:03 发布 v2.8.1 将 connection_pool.max 从 200 改为 50，导致高峰期连接池被打满，下单 5xx 升至 8.4%，持续 26 分钟。回滚配置后恢复。", "v2.8.1 at 14:03 changed connection_pool.max from 200 to 50; the pool saturated at peak and checkout 5xx reached 8.4% for 26 minutes. Rolling back the config restored service.") },
    { key: "impact", title: P("影响", "Impact"), text: P("约 1.2 万笔下单失败，其中 83% 用户重试成功；无资金损失，无数据不一致。", "~12k checkouts failed; 83% of users succeeded on retry. No money lost, no data inconsistency.") },
    { key: "root", title: P("根因", "Root cause"), text: P("配置模板中生产环境的连接池上限被误设为 50；评审只覆盖了代码 diff，未覆盖配置 diff。", "The prod pool max in the config template was set to 50 by mistake; review covered the code diff but not the config diff.") },
  ],
  timeline: [
    { at: "14:03", text: P("发布 v2.8.1", "v2.8.1 released"), tone: "info" },
    { at: "14:05", text: P("告警：5xx > 1%", "Alert: 5xx > 1%"), tone: "danger" },
    { at: "14:09", text: P("AI 初查：派生 3 个分支", "AI triage: forked 3 branches"), tone: "accent" },
    { at: "14:21", text: P("确认连接池上限被改为 50", "Confirmed pool max changed to 50"), tone: "accent" },
    { at: "14:24", text: P("批准并执行回滚 PRP-88", "Approved and ran rollback PRP-88"), tone: "warn" },
    { at: "14:31", text: P("180 秒验证通过，事件解决", "180 s verification passed, resolved"), tone: "ok" },
  ],
  actions: [
    { text: P("配置 diff 纳入发布评审", "Include config diffs in release review"), owner: "李明", due: "10/08" },
    { text: P("连接池上限加入告警规则", "Alert on pool max changes"), owner: "王晨", due: "10/01" },
    { text: P("更新 Runbook「连接池容量调整」", "Update runbook “Pool sizing”"), owner: "AI", due: P("已起草", "drafted") },
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
