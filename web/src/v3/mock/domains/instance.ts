import { mock, MockError } from "../router";
import { l } from "../../i18n";

const P = l;
/** Demo only: Settings › Workspace can show the UI as a production or a non-production instance. */
export const DEMO_TIER_KEY = "aiops-v3-demo-instance";
const tier = () => {
  try {
    return localStorage.getItem(DEMO_TIER_KEY) === "nonprod" ? "nonprod" : "production";
  } catch {
    return "production";
  }
};

mock("GET", "/instance", () =>
  tier() === "production"
    ? {
        tier: "production",
        name: P("生产", "Production"),
        domain: "ops.example.com",
        environments: [{ id: "prod", label: P("生产", "Production"), kind: "standing", tone: "prod", clusters: ["prod-sh-1", "prod-bj-1"] }],
        default_env: "prod",
        other: { label: P("前往非生产平台", "Open non-production platform"), url: "https://ops-nonprod.example.com", domain: "ops-nonprod.example.com" },
      }
    : {
        tier: "nonprod",
        name: P("非生产", "Non-production"),
        domain: "ops-nonprod.example.com",
        environments: [
          { id: "dev", label: P("开发", "Development"), kind: "standing", tone: "dev", clusters: ["dev-sh-1"] },
          { id: "test", label: P("测试", "Test"), kind: "standing", tone: "dev", clusters: ["aiops-test"] },
          { id: "staging", label: P("预发", "Staging"), kind: "standing", tone: "stg", clusters: ["staging-sh-1"] },
          { id: "feature-1234", label: P("feature-1234 · 下单拆单", "feature-1234 · order split"), kind: "feature", tone: "dev", clusters: ["feat-1234"], note: P("3 天后回收", "reclaimed in 3 days") },
          { id: "feature-1290", label: P("feature-1290 · 优惠券", "feature-1290 · coupons"), kind: "feature", tone: "dev", clusters: ["feat-1290"], note: P("今天回收", "reclaimed today") },
        ],
        default_env: "staging",
        other: { label: P("前往生产平台", "Open production platform"), url: "https://ops.example.com", domain: "ops.example.com" },
      },
);

/* Production only: read-only sources from lower environments, for comparison. */
const lower = [
  { id: "ls-1", name: P("配置仓库 · staging 分支", "Config repo · staging branch"), brand: "gitlab", env: "staging", env_label: P("预发", "Staging"), purpose: P("对比预发与生产配置", "Compare staging and production config"), credential: P("只读部署密钥 · 生产 Vault", "Read-only deploy key · production Vault") },
  { id: "ls-2", name: P("Prometheus · 预发", "Prometheus · staging"), brand: "prometheus", env: "staging", env_label: P("预发", "Staging"), purpose: P("对比预发与生产指标", "Compare staging and production metrics"), credential: P("只读令牌 · 生产 Vault", "Read-only token · production Vault") },
];
mock("GET", "/integrations/lower-sources", () => {
  if (tier() !== "production") throw new MockError(404, "not_available", "non-production instances cannot connect to production");
  return { items: lower };
});

/* Config promotion through Git: non-production exports, production reviews and imports. */
// Production reports merged, verified changes as `merged` (docs/api/v3-config-sync.md). Each sample shows one outcome
// of the import: cs-412 imports, cs-409 conflicts, cs-415 can't be verified, cs-418 is refused (422).
const SRC = "sha256:9f2a6c1e5b8d4f3a7c2e9b1d6a4f8c3e5b7d9a2c4e6f8b1d3a5c7e9f2b4d6a8c";
const imports = [
  { id: "cs-412", kind: "runbook", kind_label: "Runbook", title: P("下游超时与重试风暴 v5", "Downstream timeouts and retry storms v5"), mr: "!412", author: "李明", meta: P("预发验证 3 次通过", "passed 3 staging runs"), status: "merged" },
  { id: "cs-409", kind: "alert_rule", kind_label: P("告警规则", "Alert rule"), title: "checkout-api 5xx > 1%", mr: "!409", author: "王晨", meta: P(`来源版本：${SRC}`, `Source version: ${SRC}`), status: "merged" },
  { id: "cs-415", kind: "agent_version", kind_label: P("Agent 版本", "Agent version"), title: P("调查助手 v13", "Investigator v13"), mr: "!415", author: "周航", meta: P("评测通过率 94%", "94% eval pass"), status: "merged" },
  { id: "cs-418", kind: "template", kind_label: P("画布模板", "Canvas template"), title: P("发布后错误率上升 · 调查模板", "Post-release errors · investigation template"), mr: "!418", author: "张磊", meta: P("预发使用 12 次", "used 12 times in staging"), status: "merged" },
  { id: "cs-401", kind: "runbook", kind_label: "Runbook", title: P("证书到期续签", "Certificate renewal"), mr: "!401", author: "陈雨", meta: P("昨天导入", "imported yesterday"), status: "imported" },
  // PA-100 / PA-103: candidates nothing could be read from (kind unknown); no import is offered.
  { id: "cs-421", kind: "unknown", kind_label: P("未知类别", "Unknown kind"), title: "!421", mr: "!421", author: "", meta: "", status: "verifying", reason: "not_ready" },
  { id: "cs-419", kind: "unknown", kind_label: P("未知类别", "Unknown kind"), title: "!419", mr: "!419", author: "", meta: "", status: "unverifiable", reason: "gitlab_unavailable" },
  { id: "cs-417", kind: "unknown", kind_label: P("未知类别", "Unknown kind"), title: "!417", mr: "!417", author: "", meta: "", status: "unverifiable", reason: "untrusted" },
];
const exportsList = [
  { id: "ex-412", kind: "runbook", kind_label: "Runbook", title: P("下游超时与重试风暴 v5", "Downstream timeouts and retry storms v5"), mr: "!412", author: "李明", meta: P("等待生产负责人审核", "awaiting production review"), status: "exported" },
  { id: "ex-409", kind: "alert_rule", kind_label: P("告警规则", "Alert rule"), title: "checkout-api 5xx > 1%", mr: "!409", author: "王晨", meta: P(`来源版本：${SRC}`, `Source version: ${SRC}`), status: "exported" },
  { id: "ex-398", kind: "template", kind_label: P("画布模板", "Canvas template"), title: P("压测计划", "Load-test plan"), mr: "!398", author: "王晨", meta: P("已合并 · 生产已导入", "merged · imported in production"), status: "merged" },
];
mock("GET", "/config-sync", () => (tier() === "production" ? { direction: "import", items: imports } : { direction: "export", items: exportsList }));
mock("POST", "/config-sync/:id/import", ({ params }) => {
  const it = imports.find((x) => x.id === params.id);
  if (!it) throw new MockError(404, "not_found", "no such change");
  if (it.id === "cs-409") throw new MockError(409, "promotion_conflict", "Configuration source or target has changed");
  if (it.id === "cs-415") throw new MockError(503, "config_source_untrusted", "Merged configuration provenance cannot be verified");
  if (it.id === "cs-418") throw new MockError(422, "invalid_promotion", "Invalid configuration promotion");
  it.status = "imported";
  return it;
});
// Promotion outcomes by runbook: rb-rollback's first try gets an unknown GitLab result (503) and the retry reuses the
// same MR; rb-kafka is already being exported; rb-capacity is refused by a pre-check; the rest export.
const promotionTries: Record<string, number> = {};
mock("POST", "/promotions", ({ body }) => {
  if (tier() === "production") throw new MockError(409, "already_production", "this is the production instance");
  const b = (body ?? {}) as { kind?: string; id?: string };
  const id = b.id ?? "item";
  promotionTries[id] = (promotionTries[id] ?? 0) + 1;
  if (id === "rb-rollback" && promotionTries[id] === 1) throw new MockError(503, "config_sync_unavailable", "GitLab export result is unknown");
  if (id === "rb-kafka") throw new MockError(409, "promotion_in_progress", "Configuration export is in progress");
  if (id === "rb-capacity") throw new MockError(422, "alert_rule_label_not_exportable", "The alert rule has labels or a severity that cannot be promoted");
  return { mr: id === "rb-rollback" ? "!418" : "!417", path: `runbooks/${id}.json` };
});
