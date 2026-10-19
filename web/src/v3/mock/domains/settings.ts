import { mock } from "../router";
import { l } from "../../i18n";

const P = (zh: string, en: string) => l(zh, en);

mock("GET", "/me/profile", () => ({
  name: "王晨",
  display_name: "王晨",
  department: P("交易平台 · SRE", "Trading · SRE"),
  title: P("高级 SRE", "Senior SRE"),
  timezone: "Asia/Shanghai (UTC+8)",
  contacts: [
    { kind: "email", label: P("邮箱", "Email"), value: "wangchen@example.com", verified: true },
    { kind: "phone", label: P("手机", "Phone"), value: "+86 138 **** 5678", verified: true },
    { kind: "feishu", label: P("飞书", "Feishu"), value: P("已绑定 · 王晨", "Linked · Wang Chen"), verified: true, note: P("用于通知与卡片审批", "Used for notifications and card approvals") },
  ],
  notifications: [
    { event: "p1", label: P("P1 告警", "P1 alerts"), channels: { feishu: true, email: true, sms: true, phone: true } },
    { event: "p23", label: P("P2 / P3 告警", "P2 / P3 alerts"), channels: { feishu: true, email: true, sms: false, phone: false } },
    { event: "approval", label: P("审批请求", "Approval requests"), channels: { feishu: true, email: true, sms: false, phone: false } },
    { event: "mention", label: P("提及与回复", "Mentions & replies"), channels: { feishu: true, email: false, sms: false, phone: false } },
    { event: "report", label: P("报告与复盘", "Reports & postmortems"), channels: { feishu: true, email: true, sms: false, phone: false } },
    { event: "agent", label: P("Agent 治理待办", "Agent governance"), channels: { feishu: true, email: false, sms: false, phone: false } },
  ],
  security: {
    password_changed: P("上次修改 42 天前", "Changed 42 days ago"),
    mfa_enabled: true,
    org_rule: P("组织要求：生产审批人必须使用通行密钥", "Org rule: production approvers must use passkeys"),
    factors: [
      { kind: "passkey", name: P("通行密钥 · 2 个", "Passkeys · 2"), sub: "MacBook Pro Touch ID · YubiKey 5C" },
      { kind: "totp", name: P("身份验证器 App", "Authenticator app"), sub: P("6 位动态验证码 · 已绑定", "6-digit codes · linked") },
      { kind: "recovery", name: P("备用恢复码", "Recovery codes"), sub: P("剩余 8 个，每个只能用一次", "8 left, single use") },
    ],
    sessions: [
      { id: "s1", device: "MacBook Pro · Chrome", sub: P("上海 · 10.2.3.41 · 活跃中", "Shanghai · 10.2.3.41 · active"), current: true },
      { id: "s2", device: P("iPhone · 飞书内置浏览器", "iPhone · Feishu browser"), sub: P("上海 · 2 小时前", "Shanghai · 2 h ago") },
      { id: "s3", device: "Windows · Edge", sub: P("北京 · 3 天前", "Beijing · 3 days ago") },
    ],
    tokens: P("2 个 · 最长 90 天", "2 · max 90 days"),
  },
  unavailable: {},
}));
mock("DELETE", "/me/avatar", () => undefined);
mock("PUT", "/me/profile", ({ body }) => ({ ok: true, ...(body as object) }));
mock("DELETE", "/me/sessions/:id", () => ({ ok: true }));

const node = (id: string, name: unknown, count: number, depth: number, kind = "org") => ({ id, name, count, depth, kind });
const MEMBERS = [
  { id: "u1", name: "王晨", email: "wangchen@example.com", department: "SRE", roles: [P("管理员", "Admin"), P("生产审批人", "Prod approver")], phone: "138****5678", mfa: "passkey", source: "feishu", status: "active", last_login: P("刚刚", "just now") },
  { id: "u2", name: "李明", email: "liming@example.com", department: P("订单组", "Orders"), roles: [P("开发", "Developer"), P("交易平台值班", "Trading on-call")], phone: "139****1204", mfa: "totp", source: "feishu", status: "active", last_login: P("10 分钟前", "10 min ago") },
  { id: "u3", name: "张磊", email: "zhanglei@example.com", department: P("基础架构", "Infra"), roles: ["SRE", P("生产审批人", "Prod approver")], phone: "136****7781", mfa: "passkey", source: "ldap", status: "active", last_login: P("1 小时前", "1 h ago") },
  { id: "u4", name: "赵倩", email: "zhaoqian@example.com", department: P("支付组", "Payments"), roles: [P("开发", "Developer")], phone: "137****0932", mfa: "none", source: "feishu", status: "active", last_login: P("今天", "today") },
  { id: "u5", name: "周航", email: "zhouhang@example.com", department: P("基础架构", "Infra"), roles: ["DBA"], phone: "135****2210", mfa: "totp", source: "ldap", status: "active", last_login: P("昨天", "yesterday") },
  { id: "u6", name: "陈雨", email: "chenyu@example.com", department: P("账号与安全", "Accounts & security"), roles: [P("安全管理员", "Security admin")], phone: "158****6620", mfa: "passkey", source: "feishu", status: "active", last_login: P("昨天", "yesterday") },
  { id: "svc-investigator", name: P("调查助手", "Investigator"), email: P("svc-investigator · 机器身份", "svc-investigator · machine identity"), department: "—", roles: [P("Agent 服务账号", "Agent service account")], phone: "—", mfa: "na", source: "system", status: "active", last_login: P("刚刚", "just now"), agent: true },
  { id: "u8", name: P("刘洋（外部）", "Liu Yang (external)"), email: "liuyang@partner.example", department: P("订单组", "Orders"), roles: [P("只读", "Read-only")], phone: "186****3301", mfa: "none", source: "invite", status: "disabled", last_login: P("30 天前", "30 days ago") },
];
mock("GET", "/members", ({ query }) => {
  const f = String(query.filter ?? "all");
  const q = String(query.q ?? "").toLowerCase();
  const items = MEMBERS.filter((m) => (f === "all" || (f === "admin" && JSON.stringify(m.roles).includes("Admin")) || (f === "no_mfa" && m.mfa === "none") || (f === "disabled" && m.status === "disabled")) && (!q || JSON.stringify(m).toLowerCase().includes(q)));
  return {
    org: [node("all", P("全公司", "Company"), 48, 0), node("trade", P("交易平台", "Trading"), 12, 1), node("pay", P("支付组", "Payments"), 4, 2, "team"), node("order", P("订单组", "Orders"), 5, 2, "team"), node("plat", P("平台工程", "Platform eng."), 14, 1), node("sre", "SRE", 6, 2, "team"), node("infra", P("基础架构", "Infra"), 8, 2, "team"), node("sec", P("账号与安全", "Accounts & security"), 7, 1), node("data", P("数据", "Data"), 15, 1)],
    groups: [node("approvers", P("生产审批人", "Prod approvers"), 5, 0, "group"), node("oncall", P("值班 · 交易平台", "On-call · Trading"), 4, 0, "group"), node("ext", P("外部协作者", "External"), 2, 0, "group")],
    items,
    counts: { all: 48, admin: 2, no_mfa: 7, disabled: 3 },
    mfa_grace_days: 5,
  };
});
mock("POST", "/members/invitations", () => ({ ok: true }));
mock("POST", "/members/sync", () => ({ added: 2, updated: 5 }));
mock("POST", "/members/mfa-reminders", () => ({ sent: 7 }));

const ROLES = [
  { id: "admin", name: P("管理员", "Admin"), members: 2, kind: "system" },
  { id: "sre", name: "SRE", members: 6, kind: "system" },
  { id: "dev", name: P("开发", "Developer"), members: 21, kind: "system" },
  { id: "viewer", name: P("只读", "Read-only"), members: 9, kind: "system" },
  { id: "secadmin", name: P("安全管理员", "Security admin"), members: 1, kind: "system" },
  { id: "approver", name: P("生产审批人", "Prod approver"), members: 5, kind: "business" },
  { id: "oncall", name: P("交易平台值班", "Trading on-call"), members: 4, kind: "business" },
  { id: "dba", name: "DBA", members: 2, kind: "business" },
  { id: "agent", name: P("Agent 服务账号", "Agent service account"), members: 6, kind: "machine" },
  { id: "ci", name: P("CI 集成", "CI integration"), members: 2, kind: "machine" },
];
mock("GET", "/roles", () => ({ items: ROLES }));
mock("GET", "/roles/:id", ({ params }) => {
  const r = ROLES.find((x) => x.id === params.id) ?? ROLES[5];
  return {
    ...r,
    desc: P("可以批准生产环境的写操作提案；批准时必须使用通行密钥二次验证。", "May approve production write proposals; approval requires a passkey re-check."),
    member_names: ["王晨", "张磊", "陈雨", "周航", "李明"],
    scope: [P("环境：生产", "Env: prod"), P("应用：交易平台全部", "Apps: all Trading"), P("团队：平台工程", "Team: Platform eng.")],
    actions: [P("查看", "View"), P("创建 / 编辑", "Create / edit"), P("删除", "Delete"), P("批准", "Approve"), P("管理", "Manage")],
    matrix: [
      { resource: P("对话", "Conversations"), grants: [1, 1, 0, 0, 0] },
      { resource: P("事件与告警", "Incidents & alerts"), grants: [1, 1, 0, 0, 0] },
      { resource: P("告警规则与路由", "Alert rules & routing"), grants: [1, 1, 0, 0, 0] },
      { resource: P("静默", "Silences"), grants: [1, 1, 1, 0, 0] },
      { resource: P("写操作提案", "Write proposals"), grants: [1, 0, 0, 2, 0] },
      { resource: "Runbook", grants: [1, 1, 0, 1, 0] },
      { resource: P("连接器与映射", "Connectors & mapping"), grants: [1, 0, 0, 0, 0] },
      { resource: P("Agent 配置与发布", "Agent config & release"), grants: [1, 0, 0, 0, 0] },
      { resource: P("成员与角色", "Members & roles"), grants: [0, 0, 0, 0, 0] },
      { resource: P("审计日志", "Audit log"), grants: [1, 0, 0, 0, 0] },
      { resource: P("总览", "Overview"), grants: [1, 0, 0, 0, 0] },
      { resource: P("链路", "Traces"), grants: [1, 0, 0, 0, 0] },
      { resource: P("日志", "Logs"), grants: [1, 0, 0, 0, 0] },
      { resource: P("模型", "Models"), grants: [1, 0, 0, 0, 0] },
      { resource: P("工作区设置", "Workspace settings"), grants: [1, 0, 0, 0, 0] },
      { resource: P("画布", "Canvases"), grants: [1, 1, 0, 0, 0] },
    ],
    notes: P("Agent 服务账号不能被授予“批准”；同一提案的发起人与批准人不能是同一人；高风险动作需要两名批准人。", "Agent service accounts can never be granted Approve; a proposal's author cannot approve it; high-risk actions need two approvers."),
  };
});
mock("PUT", "/roles/:id", ({ body }) => ({ ok: true, ...(body as object) }));

mock("GET", "/security", () => ({
  sso: [
    { brand: "okta", name: "Okta · OIDC", sub: P("okta.example.com · 自动创建账号", "okta.example.com · auto-provision"), enabled: true, default: true },
    { brand: "feishu", name: P("飞书登录", "Feishu login"), sub: P("企业自建应用 · 同步组织架构", "Custom app · syncs org chart"), enabled: true },
    { brand: "keycloak", name: "Keycloak · SAML", sub: P("尚未配置", "Not configured"), enabled: false },
  ],
  scim: true,
  mfa_policy: "all",
  mfa_methods: [
    { id: "passkey", label: P("通行密钥", "Passkeys"), enabled: true },
    { id: "totp", label: P("身份验证器 App", "Authenticator app"), enabled: true },
    { id: "sms", label: P("短信验证码", "SMS codes"), enabled: false, note: P("易被劫持，不推荐", "Easily hijacked; not recommended") },
  ],
  approval_passkey: true,
  session: { idle: P("30 分钟", "30 min"), max: P("12 小时", "12 h"), ip_allow: "10.0.0.0/8, 172.16.0.0/12", new_device_notice: true },
  vault: { name: "HashiCorp Vault", sub: P("vault.example.internal · 14 个连接器使用", "vault.example.internal · used by 14 connectors"), connected: true },
  token_ttl: P("90 天", "90 days"),
  password_policy: P("至少 12 位 · 不能与最近 5 次相同", "12+ chars · not one of the last 5"),
}));
mock("PUT", "/security", ({ body }) => ({ ok: true, ...(body as object) }));

const AUDIT = [
  { id: "a1", time: "14:19:02", actor: { kind: "user", name: "王晨" }, action: P("批准写操作提案", "Approved write proposal"), target: P("库存调用重试降为 1 次（PRP-88）", "Retry inventory calls once (PRP-88)"), result: "ok", source: P("通行密钥 · 10.2.3.41", "Passkey · 10.2.3.41") },
  { id: "a2", time: "14:18:40", actor: { kind: "agent", name: P("调查助手 v12", "Investigator v12") }, action: P("生成写操作提案", "Drafted write proposal"), target: P("checkout-api · 限制重试", "checkout-api · limit retries"), result: "ok", source: "run_7f3a" },
  { id: "a3", time: "14:12:31", actor: { kind: "agent", name: P("调查助手 v12", "Investigator v12") }, action: P("护栏拦截", "Guardrail block"), target: P("工具输出中的指令注入", "Instruction injection in tool output"), result: "blocked", source: "run_7f3a" },
  { id: "a4", time: "14:12:05", actor: { kind: "agent", name: P("调查助手 v12", "Investigator v12") }, action: P("调用 query_metrics", "Called query_metrics"), target: P("Prometheus · 生产", "Prometheus · prod"), result: "ok", source: "run_7f3a" },
  { id: "a5", time: "14:10:11", actor: { kind: "user", name: "王晨" }, action: P("创建静默", "Created silence"), target: "inventory-worker / KafkaConsumerLag", result: "ok", source: "Web" },
  { id: "a6", time: "13:55:30", actor: { kind: "user", name: "张磊" }, action: P("修改路由", "Changed routing"), target: P("生产 → 平台", "Prod → Platform"), result: "ok", source: "Web" },
  { id: "a7", time: "13:40:12", actor: { kind: "system", name: P("系统", "System") }, action: P("同步成员", "Synced members"), target: P("飞书组织架构 +2", "Feishu org chart +2"), result: "ok", source: P("定时任务", "Scheduled job") },
  { id: "a8", time: "09:02:10", actor: { kind: "user", name: "陈雨" }, action: P("修改安全策略", "Changed security policy"), target: P("审批必须使用通行密钥", "Approvals require passkeys"), result: "ok", source: "Web" },
];
mock("GET", "/audit", ({ query }) => {
  const f = String(query.filter ?? "all");
  const q = String(query.q ?? "").toLowerCase();
  return {
    items: AUDIT.filter((a) => (f === "all" || a.actor.kind === f) && (!q || JSON.stringify(a).toLowerCase().includes(q))),
    counts: { all: 1284, user: 402, agent: 861, system: 21 },
    volume: [[8, 20, 1], [12, 31, 0], [15, 44, 1], [6, 18, 0], [9, 25, 1], [14, 40, 0], [16, 52, 1], [7, 21, 0], [10, 27, 1], [13, 38, 0], [17, 49, 1], [5, 16, 0], [8, 22, 1], [12, 35, 0], [15, 47, 1], [6, 19, 0], [11, 30, 1], [14, 42, 0], [18, 55, 1], [7, 20, 0], [9, 26, 1], [13, 37, 0], [16, 58, 2], [6, 22, 0], [10, 29, 1], [15, 44, 0], [19, 71, 1], [8, 24, 0]],
  };
});
mock("GET", "/audit/:id", ({ params }) => {
  const e = AUDIT.find((x) => x.id === params.id) ?? AUDIT[0];
  const full = e.id === "a1";
  return {
    id: e.id,
    title: e.action,
    time: `2026-09-20 ${e.time}`,
    actor_kind: e.actor.kind,
    actor_name: e.actor.name,
    actor: full
      ? [
          { k: P("成员", "Member"), v: P("王晨 · 生产审批人", "Wang Chen · prod approver") },
          { k: P("认证", "Auth"), v: P("通行密钥 · YubiKey 5C", "Passkey · YubiKey 5C") },
          { k: P("来源", "Source"), v: "MacBook Pro · 10.2.3.41" },
        ]
      : [{ k: P("来源", "Source"), v: e.source }],
    links: full
      ? [
          { k: P("事件", "Incident"), v: "INC-1042", to: "/incidents/INC-1042" },
          { k: P("调查", "Investigation"), v: "INV-231", to: "/canvas/INV-231" },
          { k: P("执行", "Execution"), v: "EXE-51", to: "/executions/EXE-51" },
        ]
      : typeof e.source === "string" && e.source.startsWith("run_")
        ? [{ k: P("运行", "Run"), v: e.source, to: `/agents/runs/${e.source}` }]
        : [],
    payload: full ? '{\n  "plan": "plan_231",\n  "action": "rollback_config",\n  "target": "production/checkout-api",\n  "digest": "sha256:9f2c…41ab",\n  "checklist": "3/3",\n  "proposed_by": "agent:investigator@v12"\n}' : JSON.stringify({ action: e.id, target: "…" }, null, 2),
    chain_ok: true,
  };
});
mock("POST", "/audit/export", () => ({ url: "/exports/audit-2026-09-24.csv" }));

mock("GET", "/me/notification-settings", () => ({
  quiet: { enabled: true, from: "22:00", to: "08:00" },
  digest: "daily",
  subscriptions: [
    { id: "watched", label: P("关注的服务出现异常", "Watched services degrade"), sub: P("checkout-api 等 4 个服务", "checkout-api and 3 more"), on: true },
    { id: "ai-triage", label: P("AI 初查完成", "AI triage finished"), sub: P("只包括我负责或关注的服务", "Only services you own or watch"), on: true },
    { id: "approvals", label: P("需要我审批的提案", "Proposals awaiting my approval"), sub: P("包括画布中的执行步骤", "Including execution steps in canvases"), on: true },
    { id: "mentions", label: P("对话与画布中 @ 我", "@mentions in conversations and canvases"), sub: P("飞书卡片，可直接跳转", "Feishu card with a deep link"), on: true },
    { id: "weekly", label: P("每周可靠性周报", "Weekly reliability report"), sub: P("周一 09:00 · 由 AI 生成", "Mondays 09:00 · AI generated"), on: false },
  ],
}));
mock("PUT", "/me/notification-settings", ({ body }) => ({ ok: true, ...(body as object) }));

const workspace = {
  name: P("交易平台", "Trading platform"),
  slug: "trade",
  timezone: "Asia/Shanghai (UTC+08:00)",
  locale: "zh-CN",
  week_start: "monday",
  retention_days: 180,
  data_region: P("本地部署 · 数据不出内网", "Self-hosted · data stays on-prem"),
  brand: { name: "Redstring", logo: null as string | null, pending: false },
};
mock("GET", "/workspace", () => workspace);
mock("PUT", "/workspace", ({ body }) => Object.assign(workspace, body));
