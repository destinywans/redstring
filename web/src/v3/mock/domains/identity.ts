import { mock, MockError } from "../router";
import { l } from "../../i18n";

const me = {
  id: "u-wangchen",
  name: "王晨",
  email: "wangchen@example.com",
  phone: "+86 138 **** 2046",
  title: "SRE",
  team: l("交易平台", "Trading platform"),
  department: l("基础架构部", "Infrastructure"),
  timezone: "Asia/Shanghai",
  oncall: { active: true, until: "20:00", schedule: l("交易平台主值班", "Trading primary on-call") },
  oncall_state: "verified",
  roles: ["sre", "approver"],
  mfa: { totp: true, passkeys: 2 },
  preferences: { theme: "system", locale: "zh-CN", density: "comfortable", reduce_motion: false },
};

mock("GET", "/me", () => me);
mock("PATCH", "/me/preferences", ({ body }) => ({ ...me.preferences, ...(body as object) }));
mock("PATCH", "/me", ({ body }) => Object.assign(me, body));

mock("GET", "/me/watched-services", () => ({
  items: [
    { id: "checkout-api", name: "checkout-api", status: "danger", signal: "8.4%" },
    { id: "inventory-service", name: "inventory-service", status: "warn", signal: "350 ms" },
    { id: "inventory-worker", name: "inventory-worker", status: "warn", signal: l("积压", "lag") },
    { id: "order-service", name: "order-service", status: "ok" },
  ],
}));

const SEARCH = [
  { kind: "service", id: "checkout-api", title: "checkout-api", subtitle: l("服务 · 异常", "Service · degraded"), status: "danger", href: "/services/checkout-api" },
  { kind: "service", id: "payment-gateway", title: "payment-gateway", subtitle: l("服务 · 告警", "Service · alerting"), status: "warn", href: "/services/payment-gateway" },
  { kind: "service", id: "inventory-service", title: "inventory-service", subtitle: "Deployment · 4", href: "/resources" },
  { kind: "service", id: "orders-db", title: "orders-db", subtitle: "PostgreSQL", href: "/resources" },
  { kind: "incident", id: "INC-1042", title: l("INC-1042 checkout-api 5xx 上升", "INC-1042 checkout-api 5xx rising"), subtitle: l("事件 · 进行中", "Incident · active"), href: "/incidents/INC-1042" },
  { kind: "investigation", id: "INV-231", title: l("INV-231 发布后 checkout-api 5xx 上升", "INV-231 checkout-api 5xx after release"), subtitle: l("调查 · 14:21", "Investigation · 14:21"), href: "/canvas/INV-231" },
  { kind: "incident", id: "INC-1042", label: l("INC-1042 · checkout-api 5xx 升高", "INC-1042 · checkout-api 5xx up"), meta: l("进行中", "active"), status: "danger" },
  { kind: "investigation", id: "INV-231", label: l("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), meta: l("调查 · 进行中", "investigation · active") },
  { kind: "runbook", id: "rb-retry-storm", title: l("下游超时与重试风暴", "Downstream timeouts and retry storms"), subtitle: "Runbook", href: "/runbooks/rb-retry-storm" },
  { kind: "runbook", id: "rb-rollback", title: l("发布回滚规范", "Release rollback procedure"), subtitle: "Runbook v5", href: "/runbooks/rb-rollback" },
  { kind: "change", id: "8812", title: "GitLab #8812 · checkout-api v2.8.1", subtitle: l("变更 · 14:03", "Change · 14:03"), href: "/changes" },
  { kind: "alert_group", id: "g-checkout-5xx", title: l("checkout-api / HighErrorRate", "checkout-api / HighErrorRate"), subtitle: l("告警组 · 5 条", "Alert group · 5"), href: "/alerts" },
  { kind: "person", id: "u-liming", title: "李明", subtitle: l("交易平台 · 后端", "Trading · backend"), href: "/settings/members" },
];

mock("GET", "/search", ({ query }) => {
  const q = String(query.q ?? "").trim().toLowerCase();
  const text = (v: unknown) => (typeof v === "string" ? v : `${(v as { zh: string }).zh} ${(v as { en: string }).en}`).toLowerCase();
  return {
    query: q,
    hits: q ? SEARCH.filter((h) => text(h.title).includes(q) || text(h.subtitle).includes(q)).slice(0, 8) : [],
    recent: SEARCH.filter((h) => ["INV-231", "rb-retry-storm", "INC-1042"].includes(h.id)),
  };
});

const MENTIONS = [
  { kind: "service", id: "checkout-api", label: "checkout-api", tier: "T0", meta: l("异常", "degraded"), status: "danger" },
  { kind: "service", id: "cart-service", label: "cart-service", tier: "T1", meta: l("正常", "healthy") },
  { kind: "service", id: "payment-gateway", label: "payment-gateway", tier: "T0", meta: l("告警", "alerting") },
  { kind: "alert", id: "g-checkout-5xx", label: l("5xx 比例 > 1% · checkout-api", "5xx ratio > 1% · checkout-api"), meta: l("32 分钟", "32 min") },
  { kind: "incident", id: "INC-1042", label: l("INC-1042 · checkout-api 5xx 升高", "INC-1042 · checkout-api 5xx up"), meta: l("进行中", "active"), status: "danger" },
  { kind: "investigation", id: "INV-231", label: l("发布后 checkout-api 5xx 上升", "checkout-api 5xx after release"), meta: l("调查 · 进行中", "investigation · active") },
  { kind: "runbook", id: "rb-retry-storm", label: l("下游超时与重试风暴", "Downstream timeouts and retry storms"), meta: "checkout-api" },
  { kind: "person", id: "u-wangchen", label: "王晨", meta: l("值班中", "on call") },
  { kind: "person", id: "u-liming", label: "李明", meta: l("交易平台", "Trading") },
  { kind: "artifact", id: "art-report", label: l("故障报告.md", "incident-report.md"), meta: l("v3 · 本对话", "v3 · this chat") },
];
mock("GET", "/mentions", ({ query }) => {
  const q = String(query.q ?? "").toLowerCase();
  const text = (v: unknown) => (typeof v === "string" ? v : `${(v as { zh: string }).zh} ${(v as { en: string }).en}`).toLowerCase();
  return { items: MENTIONS.filter((m) => !q || text(m.label).includes(q) || m.id.includes(q)) };
});

/* silences are shared with the events domain */
export const silences: { id: string; matchers: unknown; duration: string; comment?: string; created_at: string }[] = [];
mock("POST", "/silences", ({ body }) => {
  const b = body as { matchers: unknown; duration: string; comment?: string };
  if (!b?.matchers) throw new MockError(422, "invalid_silence", "matchers required");
  const s = { id: `sil-${Date.now()}`, ...b, created_at: new Date().toISOString() };
  silences.push(s);
  return s;
});
mock("DELETE", "/silences/:id", ({ params }) => {
  const i = silences.findIndex((s) => s.id === params.id);
  if (i >= 0) silences.splice(i, 1);
  return undefined;
});

/* Public invitation acceptance (sample: any code of 20+ characters works). */
mock("POST", "/auth/invitations/accept", ({ body }) => {
  const b = (body ?? {}) as { code?: string; name?: string; password?: string };
  if (!b.code || b.code.length < 20) throw new MockError(409, "invitation_invalid", "Invitation code is invalid or expired");
  return { created: true };
});
