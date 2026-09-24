import { mock } from "../router";
import { l } from "../../i18n";

const P = (zh: string, en: string) => l(zh, en);

const RUNBOOKS = [
  { id: "rb-db-pool", title: P("连接池容量调整", "Connection pool capacity tuning"), tags: [P("数据库", "Database"), P("容量", "Capacity")], scope: P("checkout-api 等 2 个", "checkout-api +1"), owner: "周航", version: "v3", status: "review", refs_30d: 12, resolve_rate: 83, last_verified: P("12 天前", "12 days ago"), stale: false },
  { id: "rb-rollback", title: P("发布回滚规范", "Release rollback procedure"), tags: [P("发布", "Release")], scope: P("全部服务", "All services"), owner: "张磊", version: "v5", status: "published", refs_30d: 31, resolve_rate: 91, last_verified: P("5 天前", "5 days ago"), stale: false },
  { id: "rb-capacity", title: P("大促容量评估", "Sale capacity assessment"), tags: [P("容量", "Capacity")], scope: P("交易平台", "Trading"), owner: "王晨", version: "v3", status: "published", refs_30d: 8, last_verified: P("20 天前", "20 days ago"), stale: false },
  { id: "rb-kafka", title: P("Kafka 消费积压处理", "Kafka consumer lag"), tags: [P("消息队列", "Messaging")], scope: "inventory-worker", owner: "周航", version: "v2", status: "published", refs_30d: 6, resolve_rate: 67, last_verified: P("41 天前", "41 days ago"), stale: false },
  { id: "rb-node", title: P("节点 NotReady 处理", "Node NotReady"), tags: ["Kubernetes"], scope: P("平台", "Platform"), owner: "张磊", version: "v3", status: "published", refs_30d: 9, resolve_rate: 78, last_verified: P("33 天前", "33 days ago"), stale: false },
  { id: "rb-cert", title: P("证书续期", "Certificate renewal"), tags: [P("证书", "Certificates")], scope: P("平台", "Platform"), owner: "陈雨", version: "v4", status: "published", refs_30d: 2, resolve_rate: 100, last_verified: P("97 天前", "97 days ago"), stale: true },
  { id: "rb-failover", title: P("数据库主从切换", "Database failover"), tags: [P("数据库", "Database")], scope: P("数据库", "Databases"), owner: "周航", version: "v6", status: "published", refs_30d: 1, resolve_rate: 100, last_verified: P("130 天前", "130 days ago"), stale: true },
  { id: "rb-replay", title: P("支付回调重放", "Payment callback replay"), tags: [P("支付", "Payments")], scope: "payment-gateway", owner: "赵凯", version: "v1", status: "draft", refs_30d: 0, last_verified: "—", stale: false },
];
mock("GET", "/runbooks", ({ query }) => {
  const tab = String(query.tab ?? "all");
  const q = String(query.q ?? "").toLowerCase();
  let items = RUNBOOKS;
  if (tab === "review") items = items.filter((r) => r.status === "review");
  if (tab === "stale") items = items.filter((r) => r.stale);
  if (tab === "mine") items = items.filter((r) => r.owner === "王晨" || r.owner === "周航");
  if (q) items = items.filter((r) => `${r.title.zh} ${r.title.en}`.toLowerCase().includes(q));
  return {
    items,
    counts: { all: 45, mine: 6, review: 3, stale: 5 },
    kpis: [
      { id: "pub", label: P("已发布", "Published"), value: "42", sub: P("覆盖 18 个服务", "Covers 18 services"), series: [] },
      { id: "rev", label: P("待复核", "In review"), value: "3", tone: "warn", sub: P("其中 AI 修订 2 个", "2 revised by AI"), series: [] },
      { id: "stale", label: P("超过 90 天未验证", "Unverified > 90 days"), value: "5", tone: "warn", sub: P("建议在预发试跑只读步骤", "Dry-run read-only steps on staging"), series: [] },
      { id: "refs", label: P("近 30 天被引用", "Cited in 30 days"), value: "186", sub: P("Agent 调查中召回并引用", "Recalled by agents during investigations"), series: [8, 12, 9, 14, 11, 16, 15] },
    ],
    suggestions: [
      { id: "s1", icon: "edit", tone: "accent", title: P("「连接池容量调整」建议更新下限", "Raise the floor in “Pool capacity tuning”"), body: P("INV-231 中高峰活跃连接达到 142，建议把“上限不低于 100”改为 150。", "INV-231 saw 142 active connections at peak; change “max ≥ 100” to 150."), actions: [{ label: P("查看修改", "View change"), kind: "secondary", href: "/runbooks/rb-db-pool" }, { label: P("忽略", "Dismiss"), kind: "ghost" }] },
      { id: "s2", icon: "alert", tone: "warn", title: P("「证书续期」可能已过时", "“Certificate renewal” may be outdated"), body: P("97 天未验证；第 3 步引用的脚本 scripts/renew-cert.sh 已在 GitLab 中删除。", "Not verified for 97 days; step 3's scripts/renew-cert.sh was deleted in GitLab."), actions: [{ label: P("标记需更新", "Flag for update"), kind: "secondary" }, { label: P("通知维护人", "Notify owner"), kind: "ghost" }] },
      { id: "s3", icon: "sparkles", tone: "accent", title: P("「Kafka 消费积压处理」效果下降", "“Kafka consumer lag” is less effective"), body: P("最近 3 次使用中 2 次没有解决问题，常见缺口是分区再平衡。", "2 of the last 3 uses didn't resolve the issue; partition rebalancing is the usual gap."), actions: [{ label: P("生成修订草稿", "Draft a revision"), kind: "secondary" }, { label: P("忽略", "Dismiss"), kind: "ghost" }] },
      { id: "s4", icon: "layers", tone: "neutral", title: P("发现重复内容", "Duplicate content"), body: P("「节点 NotReady 处理」与「节点故障排查」约 70% 重叠。", "“Node NotReady” overlaps ~70% with “Node troubleshooting”."), actions: [{ label: P("合并", "Merge"), kind: "secondary" }, { label: P("忽略", "Dismiss"), kind: "ghost" }] },
    ],
  };
});

mock("GET", "/runbooks/:id", ({ params }) => {
  const base = RUNBOOKS.find((r) => r.id === params.id) ?? RUNBOOKS[0];
  return {
    id: base.id,
    title: base.title,
    version: "v4",
    base: "v3",
    owner: base.owner,
    scope: P("适用 checkout-api、order-service", "Applies to checkout-api, order-service"),
    tags: base.tags,
    when: P("服务错误率上升，同时数据库连接等待升高、活跃连接接近上限。常见于发布改动连接池配置，或流量高峰超过容量。", "Error rate rising together with DB connection wait and active connections near the pool max. Usually caused by a release changing the pool config, or peak traffic above capacity."),
    steps: [
      { n: 1, kind: "query", title: P("连接等待与活跃连接", "Connection wait and active connections"), connectors: ["prometheus"], code: 'db_connection_wait_p95{service="$service"}\ndb_pool_active / db_pool_max', readonly: true },
      { n: 2, kind: "check", title: P("连接池是否被打满", "Is the pool saturated?"), body: P("活跃连接 ≥ 上限的 90%，且等待 P95 > 50 ms 持续 5 分钟：判定为连接池不足，继续第 3 步；否则转「慢查询排查」。", "Active ≥ 90% of max and wait P95 > 50 ms for 5 min → pool too small, go to step 3; otherwise switch to “Slow query triage”.") },
      { n: 3, kind: "query", title: P("最近的配置变更", "Recent config changes"), connectors: ["gitlab", "argo"], body: P("对比最近两次发布中 connection_pool.* 的差异", "Diff connection_pool.* between the last two releases"), readonly: true },
      { n: 4, kind: "action", title: P("调整连接池上限", "Adjust the pool max"), suggestion: { source: P("AI 建议 · 来自 INV-231 · 14:25", "AI suggestion · from INV-231 · 14:25"), minus: P("新上限不低于 100，并确认数据库最大连接数余量。", "New max at least 100; confirm DB max_connections headroom."), plus: P("新上限不低于 150（INV-231：高峰活跃连接 142），并确认数据库最大连接数余量。", "New max at least 150 (INV-231: 142 active at peak); confirm DB max_connections headroom.") } },
      { n: 5, kind: "verify", title: P("观测 180 秒", "Watch for 180 s"), body: P("5xx < 0.5% 且连接等待 P95 < 50 ms 持续 180 秒；否则回滚本次调整并升级到数据库值班。", "5xx < 0.5% and wait P95 < 50 ms for 180 s; otherwise roll back and escalate to the DB on-call.") },
    ],
    reviewers: [{ name: "王晨", state: "pending" }, { name: "张磊", state: "optional" }],
    versions: [{ v: "v4", who: P("AI 修订 + 周航", "AI revision + Zhou Hang"), when: P("草稿", "Draft") }, { v: "v3", who: "周航", when: "9/11" }, { v: "v2", who: "周航", when: "7/02" }, { v: "v1", who: "张磊", when: "4/15" }],
    usage: { refs: 12, resolve_rate: 83, weekly: [1, 0, 2, 1, 3, 1, 0, 2, 1, 1, 0, 0], highlight: 4 },
    incidents: [{ id: "INC-1042", title: P("checkout-api 5xx 上升", "checkout-api 5xx spike") }, { id: "INC-1037", title: P("order-service 慢查询", "order-service slow queries") }],
  };
});
mock("POST", "/runbooks/:id/dry-run", () => ({ steps: 3, ok: true }));
mock("POST", "/runbooks/:id/submit", () => ({ ok: true }));

mock("GET", "/postmortems", ({ query }) => {
  const all = [
    { id: "PM-57", title: P("checkout-api 5xx 上升", "checkout-api 5xx spike"), severity: "P1", date: "9/20", duration: P("21 分钟", "21 min"), cause: P("配置变更", "Config change"), actions: "0 / 3", status: "draft" },
    { id: "PM-56", title: P("payment-gateway 延迟升高", "payment-gateway latency"), severity: "P2", date: "9/20", duration: P("1 时 12 分", "1 h 12 m"), cause: P("依赖", "Dependency"), actions: "1 / 2", status: "draft" },
    { id: "PM-55", title: P("order-service 慢查询导致超时", "order-service slow-query timeouts"), severity: "P2", date: "9/19", duration: P("48 分钟", "48 min"), cause: P("代码缺陷", "Code defect"), actions: "2 / 2", status: "published" },
    { id: "PM-54", title: P("search-api 发布后可用性下降", "search-api availability after release"), severity: "P1", date: "9/18", duration: P("35 分钟", "35 min"), cause: P("发布", "Release"), actions: "3 / 4", status: "published" },
    { id: "PM-53", title: P("notification-svc 短信通道失败", "notification-svc SMS failures"), severity: "P3", date: "9/16", duration: P("2 小时", "2 h"), cause: P("第三方", "Third party"), actions: "1 / 1", status: "published" },
    { id: "PM-52", title: P("user-center 登录延迟抖动", "user-center login jitter"), severity: "P2", date: "9/15", duration: P("25 分钟", "25 min"), cause: P("容量", "Capacity"), actions: "2 / 3", status: "published" },
    { id: "PM-51", title: P("inventory-worker 重复消费", "inventory-worker duplicate consumption"), severity: "P3", date: "9/12", duration: P("1 时 40 分", "1 h 40 m"), cause: P("代码缺陷", "Code defect"), actions: "2 / 2", status: "published" },
  ];
  const tab = String(query.tab ?? "all");
  return {
    kpis: [
      { id: "n", label: P("本月事件", "Incidents this month"), value: "9", sub: "P1 2 · P2 4 · P3 3", series: [1, 2, 1, 3, 2] },
      { id: "mtta", label: P("平均响应（MTTA）", "MTTA"), value: "3.2", unit: P("分钟", "min"), tone: "ok", sub: P("较上月 −1.1 分钟", "−1.1 min vs last month"), series: [] },
      { id: "mttr", label: P("平均恢复（MTTR）", "MTTR"), value: "38", unit: P("分钟", "min"), tone: "ok", sub: P("较上月 −9 分钟", "−9 min vs last month"), series: [] },
      { id: "recur", label: P("复发率", "Recurrence"), value: "11%", tone: "warn", sub: P("同根因 90 天内再次发生", "Same cause within 90 days"), series: [] },
      { id: "done", label: P("改进项完成率", "Action items done"), value: "72%", sub: "18 / 25", series: [] },
    ],
    items: tab === "draft" ? all.filter((x) => x.status === "draft") : tab === "published" ? all.filter((x) => x.status === "published") : all,
    causes: [
      { label: P("配置变更", "Config change"), pct: 34, color: "var(--c1)" },
      { label: P("容量", "Capacity"), pct: 22, color: "var(--c2)" },
      { label: P("依赖", "Dependency"), pct: 18, color: "var(--c4)" },
      { label: P("代码缺陷", "Code defect"), pct: 14, color: "var(--c3)" },
      { label: P("基础设施", "Infrastructure"), pct: 8, color: "var(--c6)" },
      { label: P("第三方", "Third party"), pct: 4, color: "var(--c5)" },
    ],
    impact_by_service: [{ label: "checkout-api", value: 58 }, { label: "payment-gateway", value: 42 }, { label: "search-api", value: 35 }, { label: "order-service", value: 26 }, { label: "user-center", value: 18 }, { label: P("其他", "Other"), value: 12 }],
    followups: [
      { text: P("发布流水线加入连接等待冒烟检查", "Add a conn-wait smoke check to the pipeline"), owner: "李明", due: P("逾期 2 天", "2 days overdue"), tone: "danger" },
      { text: P("支付回调重试上限", "Cap payment callback retries"), owner: "赵凯", due: P("逾期 5 天", "5 days overdue"), tone: "danger" },
      { text: P("慢查询告警阈值调整", "Tune the slow-query alert threshold"), owner: "周航", due: P("今天到期", "Due today"), tone: "warn" },
      { text: P("配置模板增加连接池下限校验", "Pool lower-bound check in config template"), owner: "张磊", due: "9/27", tone: "neutral" },
    ],
  };
});

mock("GET", "/memory", () => ({
  layers: [
    { id: "working", name: P("工作记忆", "Working"), count: "18", desc: P("活跃分支的上下文，随分支版本保存", "Context of active branches, versioned with them") },
    { id: "service", name: P("服务记忆", "Service"), count: "96", desc: P("按服务整理的事实，可确认与过期", "Facts per service; confirmable and expiring") },
    { id: "episodic", name: P("情景记忆", "Episodic"), count: "214", desc: P("过去的调查、事件与报告", "Past investigations, incidents and reports") },
    { id: "semantic", name: P("语义记忆", "Semantic"), count: "1,322", desc: P("Runbook 与知识库文档片段", "Runbook and knowledge-base chunks") },
  ],
  services: [{ id: "checkout-api", count: 14 }, { id: "payment-gateway", count: 11 }, { id: "order-service", count: 9 }, { id: "inventory-worker", count: 7 }, { id: "user-center", count: 6 }],
  service: "checkout-api",
  conflict: {
    a: { text: P("连接池上限 50 足够日常流量", "A pool max of 50 is enough for daily traffic"), source: P("来源：2026-05 巡检 · 置信度 中 · 未确认", "From: 2026-05 check · medium confidence · unconfirmed") },
    b: { text: P("高峰活跃连接约 142，上限需 ≥ 150", "About 142 active at peak; max must be ≥ 150"), source: P("来源：INV-231 · 王晨确认 · 证据 2 条", "From: INV-231 · confirmed by Wang Chen · 2 evidence") },
  },
  facts: [
    { id: "f1", fact: P("高峰期活跃连接约 142，连接池上限需 ≥ 150", "Peak active connections ≈142; pool max must be ≥ 150"), source: "INV-231", confidence: "high", confirmed: P("王晨 · 今天", "Wang Chen · today"), recalls: 3, expires: P("180 天后", "in 180 days") },
    { id: "f2", fact: P("依赖 orders-db（PostgreSQL 15）与 session-cache（Redis）", "Depends on orders-db (PostgreSQL 15) and session-cache (Redis)"), source: P("服务目录", "Service catalog"), confidence: "high", confirmed: P("自动同步", "Auto-synced"), recalls: 27, expires: P("随同步", "With sync") },
    { id: "f3", fact: P("发布走 GitLab 流水线 + Argo CD，平均 14 分钟", "Releases go through GitLab + Argo CD, ≈14 min"), source: P("变更记录", "Change log"), confidence: "medium", confirmed: P("自动", "Automatic"), recalls: 9, expires: P("90 天后", "in 90 days") },
    { id: "f4", fact: P("每天 20:00–22:00 为流量高峰，约为平时的 2.4 倍", "Daily peak 20:00–22:00 at ≈2.4× normal"), source: P("指标统计", "Metric stats"), confidence: "medium", confirmed: P("自动", "Automatic"), recalls: 12, expires: P("30 天后", "in 30 days") },
    { id: "f5", fact: P("5xx 告警多由连接池或下游 payment-gateway 引起", "5xx alerts are usually the pool or payment-gateway"), source: P("情景记忆 · 7 次调查", "Episodic · 7 investigations"), confidence: "medium", confirmed: null, recalls: 5, expires: "—" },
    { id: "f6", fact: P("负责人：交易平台；值班群 #sre-oncall", "Owner: Trading; on-call channel #sre-oncall"), source: P("服务目录", "Service catalog"), confidence: "high", confirmed: P("自动同步", "Auto-synced"), recalls: 31, expires: P("随同步", "With sync") },
  ],
  policy: P("写入策略：AI 提取的事实需人工确认后才会被召回；不保存密钥与个人信息；默认 180 天过期。", "Write policy: facts extracted by AI are recalled only after a person confirms them; no secrets or personal data; 180-day default expiry."),
}));
mock("POST", "/memory/facts/:id/:action", () => ({ ok: true }));
mock("POST", "/memory/conflicts/resolve", () => ({ ok: true }));

mock("GET", "/knowledge-sources", () => ({
  items: [
    { id: "ks1", brand: "confluence", name: P("Confluence · SRE 手册", "Confluence · SRE handbook"), kind: P("文档空间", "Space"), sync: P("每 6 小时", "Every 6 h"), docs: 312, chunks: "4,120", last: P("1 小时前", "1 h ago"), status: "connected" },
    { id: "ks2", brand: "notion", name: P("Notion · 交易平台 Wiki", "Notion · Trading wiki"), kind: P("页面", "Pages"), sync: P("每天", "Daily"), docs: 128, chunks: "1,530", last: P("今天 03:00", "Today 03:00"), status: "connected" },
    { id: "ks3", brand: "gitlab", name: "GitLab · platform/runbooks", kind: P("Markdown 仓库", "Markdown repo"), sync: P("推送时", "On push"), docs: 42, chunks: "610", last: "14:25", status: "connected" },
    { id: "ks4", name: P("复盘报告（平台内）", "Postmortems (built-in)"), kind: P("内部", "Internal"), sync: P("实时", "Realtime"), docs: 57, chunks: "890", last: P("实时", "Realtime"), status: "connected" },
    { id: "ks5", name: P("上传文件", "Uploads"), kind: "PDF · Markdown", sync: P("手动", "Manual"), docs: 16, chunks: "240", last: "9/18", status: "connected" },
    { id: "ks6", brand: "confluence", name: P("Confluence · 旧版运维文档", "Confluence · legacy ops docs"), kind: P("文档空间", "Space"), sync: P("已暂停", "Paused"), docs: 204, chunks: "—", last: "8/30", status: "error", error: P("权限已失效", "Credentials expired") },
  ],
  index: { model: "bge-m3 · 1024", store: P("pgvector · 可切换 Qdrant", "pgvector · Qdrant ready"), chunking: P("800 字 · 重叠 100", "800 chars · 100 overlap"), hybrid: P("关键词 0.4 + 向量 0.6", "keyword 0.4 + vector 0.6"), acl: P("按应用与团队可见性", "By app and team visibility") },
}));
mock("POST", "/knowledge/search", ({ body }) => {
  const q = String((body as { q: string }).q ?? "");
  const hits = [
    { title: P("Runbook · 连接池容量调整 v3", "Runbook · Pool capacity tuning v3"), snippet: P("活跃连接 ≥ 上限的 90%，且等待 P95 > 50 ms 持续 5 分钟，判定为连接池不足……", "Active ≥ 90% of max and wait P95 > 50 ms for 5 min means the pool is too small…"), keyword: 0.62, vector: 0.81, score: 0.78 },
    { title: P("复盘 · order-service 慢查询导致超时", "Postmortem · order-service slow queries"), snippet: P("慢查询占用连接导致连接池耗尽，上游出现大量 5xx……", "Slow queries held connections, exhausting the pool; upstream saw many 5xx…"), keyword: 0.41, vector: 0.77, score: 0.66 },
    { brand: "confluence", title: P("SRE 手册 · 数据库连接管理", "SRE handbook · DB connections"), snippet: P("每个服务的连接池上限需要结合数据库 max_connections 统一规划……", "Plan each service's pool max against the database max_connections…"), keyword: 0.55, vector: 0.69, score: 0.64 },
    { brand: "gitlab", title: "platform/runbooks · db-pool.md", snippet: P("修改 connection_pool.max 后需同步更新容量评估表……", "Update the capacity sheet after changing connection_pool.max…"), keyword: 0.49, vector: 0.63, score: 0.58 },
  ];
  return { query: q, hits: q ? hits : [] };
});
