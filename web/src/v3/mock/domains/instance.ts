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
const imports = [
  { id: "cs-412", kind: "runbook", kind_label: "Runbook", title: P("下游超时与重试风暴 v5", "Downstream timeouts and retry storms v5"), mr: "!412", author: "李明", meta: P("预发验证 3 次通过", "passed 3 staging runs"), status: "pending" },
  { id: "cs-409", kind: "alert_rule", kind_label: P("告警规则", "Alert rule"), title: "checkout-api 5xx > 1%", mr: "!409", author: "王晨", meta: P("预发回测 7 天触发 3 次", "3 fires in a 7-day staging backtest"), status: "pending" },
  { id: "cs-415", kind: "agent_version", kind_label: P("Agent 版本", "Agent version"), title: P("调查助手 v13", "Investigator v13"), mr: "!415", author: "周航", meta: P("评测通过率 94% · 导入后先灰度", "94% eval pass · canary after import"), status: "pending" },
];
const exportsList = [
  { id: "ex-412", kind: "runbook", kind_label: "Runbook", title: P("下游超时与重试风暴 v5", "Downstream timeouts and retry storms v5"), mr: "!412", author: "李明", meta: P("等待生产负责人审核", "awaiting production review"), status: "exported" },
  { id: "ex-398", kind: "template", kind_label: P("画布模板", "Canvas template"), title: P("压测计划", "Load-test plan"), mr: "!398", author: "王晨", meta: P("已合并 · 生产已导入", "merged · imported in production"), status: "merged" },
];
mock("GET", "/config-sync", () => (tier() === "production" ? { direction: "import", items: imports } : { direction: "export", items: exportsList }));
mock("POST", "/config-sync/:id/import", ({ params }) => {
  const it = imports.find((x) => x.id === params.id);
  if (!it) throw new MockError(404, "not_found", "no such change");
  it.status = "imported";
  return it;
});
mock("POST", "/promotions", ({ body }) => {
  if (tier() === "production") throw new MockError(409, "already_production", "this is the production instance");
  const b = (body ?? {}) as { kind?: string; id?: string };
  return { mr: "!417", path: `ops-config/${b.kind ?? "runbooks"}/${b.id ?? "item"}.yaml` };
});
