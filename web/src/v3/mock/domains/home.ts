import { mock } from "../router";
import { l } from "../../i18n";
import { series } from "../../charts";
import { ago, at } from "../time";


mock("GET", "/home", () => ({
  briefing: {
    summary: l(
      "checkout-api 5xx 升到 8.4%。时间紧挨 14:03 的发布，但 AI 查到的是 inventory-03 先变慢、被无退避的重试放大成风暴[1]，限制重试的提案等你复核[2]。inventory-03 的磁盘 IO 告警从 14:02 起一直没人认领[3]。",
      "checkout-api 5xx rose to 8.4%. It lines up with the 14:03 release, but AI found that inventory-03 slowed first and retries without backoff turned it into a storm[1]; a proposal to limit retries awaits your review[2]. inventory-03's disk IO alert has been unclaimed since 14:02[3].",
    ),
    generated_at: "14:30",
    citations: [
      { n: 1, label: l("画布 INV-231", "Canvas INV-231"), href: "/canvas/INV-231" },
      { n: 2, label: l("提案 PRP-88", "Proposal PRP-88"), href: "/review/PRP-88" },
      { n: 3, label: l("告警 · inventory-03 磁盘", "Alert · inventory-03 disk"), href: "/alerts" },
    ],
    oncall: { name: "王晨", until: "20:00", next: "李明" },
  },
  greeting: {
    name: "王晨",
    summary: l("2 个服务异常，4 项等待你处理。AI 已完成 checkout-api 的自动初查，并提出了限制重试的提案。", "2 services are degraded and 4 items need you. AI finished triage on checkout-api and proposed limiting retries."),
  },
  suggestions: [
    { id: "s1", text: l("分析 checkout-api 的 5xx 异常", "Analyse the checkout-api 5xx spike"), tone: "danger", icon: "alert" },
    { id: "s2", text: l("inventory-03 的磁盘为什么变慢？", "Why did inventory-03's disk slow down?"), tone: "warn", icon: "activity" },
    { id: "s3", text: l("生成本周告警复盘", "Draft this week's alert review"), icon: "file" },
    { id: "s4", text: l("Harbor 存储 82%，给出清理建议", "Harbor storage at 82% — suggest a cleanup"), icon: "box" },
  ],
  kpis: [
    { id: "alerts", label: l("触发中告警", "Firing alerts"), value: "12", unit: l("条", ""), delta: "+5", delta_good: false, tone: "danger", sub: "P1 2 · P2 5 · P3 5", series: series(24, 4, 2, 8, 2, [18, 2.6]) },
    { id: "inv", label: l("进行中调查", "Open investigations"), value: "3", unit: l("个", ""), delta: "+1", delta_good: false, sub: l("其中 AI 自动初查 2 个", "2 started by AI triage"), series: series(24, 2, 1, 6, 3) },
    { id: "approvals", label: l("等待审批", "Awaiting approval"), value: "2", unit: l("项", ""), tone: "warn", sub: l("最早 14:18 · 限制重试提案", "Oldest 14:18 · retry-limit proposal"), series: series(24, 1, 1, 6, 4) },
    { id: "mttr", label: l("平均恢复 · 本周", "MTTR · this week"), value: "38", unit: l("分钟", "min"), delta: "−9", delta_good: true, tone: "ok", sub: l("较上周缩短 19%", "19% faster than last week"), series: [52, 49, 47, 50, 44, 41, 43, 40, 38, 38] },
    { id: "slo", label: l("T0 SLO 达标", "T0 SLOs met"), value: "7 / 8", tone: "warn", sub: l("checkout-api 预算剩余 38%", "checkout-api has 38% budget left"), series: [8, 8, 8, 8, 8, 8, 8, 8, 7, 7] },
  ],
  firing_services: 2,
  attention: [
    { id: "a1", severity: "P1", service: "checkout-api", env: "prod", symptom: l("5xx 比例 8.4%（基线 0.2%）", "5xx ratio 8.4% (baseline 0.2%)"), meta: l("14:05 起 · 根因：库存调用重试放大", "Since 14:05 · cause: inventory retry amplification"), ai: "done", href: "/canvas/INV-231" },
    { id: "a2", severity: "P2", service: "inventory-service", env: "prod", symptom: l("inventory-03 磁盘 IO 等待 180 ms（基线 2 ms）", "inventory-03 disk IO wait 180 ms (baseline 2 ms)"), meta: l("14:02 起 · Prometheus 告警", "Since 14:02 · Prometheus alert"), ai: null, href: "/alerts" },
    { id: "a3", severity: "P3", service: "inventory-worker", env: "prod", symptom: l("Kafka 消费积压 12k 条", "Kafka consumer lag 12k"), meta: l("12:10 起 · 已通知飞书", "Since 12:10 · posted to Feishu"), ai: null, owner: "王晨", href: "/alerts" },
  ],
  todos: [
    { id: "t1", kind: "approval", title: l("库存调用重试降为 1 次并打开熔断", "Retry inventory calls once, breaker on"), meta: l("INV-231 · AI 提案 · 中风险 · 14:18", "INV-231 · AI proposal · medium risk · 14:18"), action: l("复核", "Review"), href: "/review/PRP-88" },
    { id: "t2", kind: "report", title: l("checkout-api 故障报告草稿", "checkout-api incident report draft"), meta: l("AI 起草 · 等你确认后发布", "Drafted by AI · publish after you confirm"), action: l("审阅", "Open"), href: "/executions/EXE-51" },
    { id: "t3", kind: "runbook", title: l("「下游超时与重试风暴」修订 v3 → v4", "“Retry storms” v3 → v4"), meta: l("来自 INV-231 · 2 处修改", "From INV-231 · 2 edits"), action: l("复核", "Review"), href: "/runbooks/rb-retry-storm" },
    { id: "t4", kind: "access", title: l("李明申请 inventory-worker 操作权限", "Li Ming requests inventory-worker access"), meta: l("交易平台 · 有效期 7 天", "Trading · valid 7 days"), action: l("处理", "Handle"), href: "/settings/members" },
  ],
  services: [
    { id: "checkout-api", name: "checkout-api", tier: "T0", status: "danger", budget_pct: 38, p99: "1.6 s", error_rate: "8.4%", series: [2, 2.1, 2, 2.2, 2.1, 2, 2.3, 2.1, 8.6, 8.2, 8.9, 8.4] },
    { id: "inventory-service", name: "inventory-service", tier: "T0", status: "warn", budget_pct: 57, p99: "350 ms", error_rate: "31%", series: [80, 79, 82, 80, 81, 80, 96, 214, 318, 344, 351, 350] },
    { id: "inventory-worker", name: "inventory-worker", tier: "T1", status: "warn", budget_pct: 70, p99: "—", error_rate: "0.0%", series: [1, 2, 3, 5, 6, 8, 9, 10, 11, 12, 12, 12] },
    { id: "order-service", name: "order-service", tier: "T1", status: "ok", budget_pct: 82, p99: "142 ms", error_rate: "0.1%", series: [3, 3.2, 3.1, 3.4, 3.3, 3.2, 3.5, 3.3, 3.4, 3.2, 3.6, 3.5] },
    { id: "search-api", name: "search-api", tier: "T2", status: "ok", budget_pct: 55, p99: "88 ms", error_rate: "0.0%", series: [2.1, 2.2, 2, 2.3, 2.1, 2.2, 2.4, 2.1, 2.2, 2, 2.3, 2.2] },
  ],
  alert_trend: {
    start: at("15:00", -1),
    step_seconds: 3600,
    counts: [[0,1,2,1],[0,0,2,1],[0,1,1,2],[0,0,1,1],[0,0,1,1],[0,1,1,0],[0,0,2,1],[0,1,2,2],[0,1,3,1],[0,2,2,1],[0,1,2,2],[0,1,1,1],[0,2,3,1],[0,1,2,2],[1,2,2,1],[2,3,3,2],[2,4,3,1],[1,3,2,2],[0,2,2,1],[0,1,2,1],[0,1,1,1],[0,1,2,2],[2,5,4,2],[2,5,5,1]],
    summary: l("今日 41 条 · 分组后 9 组 · 抑制 14 条", "41 today · 9 groups · 14 suppressed"),
  },
  activity: [
    { id: "e1", time: at("14:06"), kind: "ai", tone: "accent", text: l("自动初查 checkout-api：与发布时间接近，派生 3 个分支分头取证", "Auto-triage on checkout-api: close to the release, so it forked 3 branches to gather evidence"), href: "/canvas/INV-231" },
    { id: "e2", time: at("14:03"), kind: "change", tone: "info", text: l("GitLab 流水线 #8812 把 checkout-api v2.8.1 发布到生产", "GitLab pipeline #8812 released checkout-api v2.8.1 to prod"), href: "/changes" },
    { id: "e3", time: at("14:02"), kind: "alert", tone: "warn", text: l("inventory-03 磁盘 IO 等待升到 180 ms，已通知钉钉值班群", "inventory-03 disk IO wait reached 180 ms; paged the DingTalk on-call channel"), href: "/alerts" },
    { id: "e4", time: at("13:10"), kind: "runbook", tone: "neutral", text: l("Runbook「下游超时与重试风暴」被 2 次调查引用", "Runbook “Retry storms” cited by 2 investigations"), href: "/runbooks/rb-retry-storm" },
  ],
  recent: [
    { id: "r1", kind: "investigation", title: l("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), environment_id: "prod", environment_label: l("生产", "prod"), at: ago(5), href: "/canvas/INV-231" },
    { id: "r2", kind: "conversation", title: l("checkout-api 大促扩容评估", "checkout-api capacity for the sale"), environment_id: "prod", environment_label: l("生产", "prod"), at: ago(12), href: "/conversations/C-311" },
    { id: "r3", kind: "conversation", title: l("Harbor 镜像保留策略", "Harbor image retention policy"), environment_id: "prod", environment_label: l("生产", "prod"), at: at("16:20", -1), href: "/conversations/C-298" },
    { id: "r4", kind: "artifact", title: l("checkout-api 故障报告 v3", "checkout-api incident report v3"), environment_id: "prod", environment_label: l("生产", "prod"), at: at("14:26"), href: "/conversations/C-320?artifact=report" },
  ],
}));
