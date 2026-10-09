import type { IconName } from "../ui/core";
import { l, type L } from "../i18n";
import { API_MODE } from "../api/client";

export type NavChild = { id: string; label: L; to: string; match?: string[] };
export type NavItem = { id: string; label: L; icon: IconName; to: string; count?: number; children: NavChild[] };

export const NAV: NavItem[] = [
  { id: "home", label: l("总览", "Overview"), icon: "home", to: "/", children: [] },
  {
    id: "conv",
    label: l("对话与画布", "Chat & canvases"),
    icon: "message",
    to: "/conversations",
    children: [
      { id: "all", label: l("对话", "Conversations"), to: "/conversations", match: ["/conversations"] },
      { id: "canvas", label: l("画布", "Canvases"), to: "/canvases", match: ["/canvases", "/canvas"] },
      { id: "templates", label: l("模板", "Templates"), to: "/templates" },
    ],
  },
  {
    id: "events",
    label: l("事件与告警", "Events & alerts"),
    icon: "bell",
    to: "/inbox",
    // The sample badge only; real mode has no unread count source for the sidebar yet.
    count: API_MODE === "mock" ? 4 : undefined,
    children: [
      { id: "inbox", label: l("收件箱", "Inbox"), to: "/inbox" },
      { id: "incidents", label: l("事件", "Incidents"), to: "/incidents" },
      { id: "alerts", label: l("告警", "Alerts"), to: "/alerts" },
      { id: "rules", label: l("告警规则", "Alert rules"), to: "/alert-rules" },
      { id: "routing", label: l("通知与路由", "Routing"), to: "/routing" },
      { id: "silences", label: l("静默与抑制", "Silences"), to: "/silences" },
    ],
  },
  {
    id: "know",
    label: l("知识与记忆", "Knowledge"),
    icon: "book",
    to: "/runbooks",
    children: [
      { id: "runbooks", label: l("Runbook", "Runbooks"), to: "/runbooks" },
      { id: "reports", label: l("复盘报告", "Postmortems"), to: "/postmortems" },
      { id: "memory", label: l("记忆", "Memory"), to: "/memory" },
      { id: "sources", label: l("知识来源", "Sources"), to: "/knowledge-sources" },
    ],
  },
  {
    id: "obs",
    label: l("资源与观测", "Resources"),
    icon: "activity",
    to: "/services",
    children: [
      { id: "services", label: l("服务目录", "Services"), to: "/services" },
      { id: "resources", label: l("资源与拓扑", "Topology"), to: "/resources" },
      { id: "metrics", label: l("指标", "Metrics"), to: "/metrics" },
      { id: "traces", label: l("链路", "Traces"), to: "/traces" },
      { id: "logs", label: l("日志", "Logs"), to: "/logs" },
      { id: "changes", label: l("变更与发布", "Changes"), to: "/changes" },
    ],
  },
  {
    id: "int",
    label: l("集成中心", "Integrations"),
    icon: "plug",
    to: "/integrations",
    children: [
      { id: "connectors", label: l("连接器", "Connectors"), to: "/integrations", match: ["/integrations/new"] },
      { id: "models", label: l("模型", "Models"), to: "/integrations/models" },
      { id: "mcp", label: l("MCP 服务器", "MCP servers"), to: "/integrations/mcp" },
      { id: "ingest", label: l("通用接入", "Generic ingest"), to: "/integrations/ingest" },
      { id: "mapping", label: l("服务映射", "Service mapping"), to: "/integrations/mapping" },
      { id: "coverage", label: l("数据覆盖度", "Data coverage"), to: "/integrations/coverage" },
      { id: "webhooks", label: l("Webhook 与 API", "Webhooks & API"), to: "/integrations/webhooks" },
      { id: "sync", label: l("配置同步", "Config sync"), to: "/integrations/sync" },
    ],
  },
  {
    id: "agent",
    label: l("Agent 运行台", "Agent console"),
    icon: "cpu",
    to: "/agents/lifecycle",
    children: [
      { id: "lifecycle", label: l("生命周期", "Lifecycle"), to: "/agents/lifecycle" },
      { id: "agents", label: l("Agent 与版本", "Agents & versions"), to: "/agents/investigator" },
      { id: "prompts", label: l("提示词与 Skills", "Prompts & skills"), to: "/agents/prompts" },
      { id: "tools", label: l("工具与权限", "Tools & permissions"), to: "/agents/tools" },
      { id: "evals", label: l("评测", "Evaluations"), to: "/agents/evals" },
      { id: "runs", label: l("运行与 Trace", "Runs & traces"), to: "/agents/runs" },
      { id: "guard", label: l("护栏与审计", "Guardrails"), to: "/agents/guardrails" },
    ],
  },
];

/** Settings live on their own page (Linear / Notion style); the app sidebar is replaced by these groups. */
export const SETTINGS_GROUPS: { id: string; label: L; items: (NavChild & { icon: IconName })[] }[] = [
  {
    id: "account",
    label: l("个人", "Account"),
    items: [
      { id: "profile", label: l("个人资料", "Profile"), to: "/settings/profile", icon: "user" },
      { id: "preferences", label: l("偏好设置", "Preferences"), to: "/settings/preferences", icon: "sliders" },
      { id: "notifications", label: l("通知", "Notifications"), to: "/settings/notifications", icon: "bell" },
      { id: "ai", label: l("模型与 AI", "Models & AI"), to: "/settings/ai", icon: "sparkles" },
      { id: "account-security", label: l("账号安全", "Account security"), to: "/settings/account-security", icon: "key" },
    ],
  },
  {
    id: "workspace",
    label: l("工作区", "Workspace"),
    items: [
      { id: "workspace", label: l("通用", "General"), to: "/settings/workspace", icon: "home" },
      { id: "members", label: l("成员与团队", "Members & teams"), to: "/settings/members", icon: "users" },
      { id: "roles", label: l("角色与权限", "Roles & permissions"), to: "/settings/roles", icon: "shield" },
      { id: "security", label: l("安全策略", "Security policy"), to: "/settings/security", icon: "lock" },
      { id: "audit", label: l("审计日志", "Audit log"), to: "/settings/audit", icon: "file" },
    ],
  },
];

export const SETTINGS_NAV: NavItem = {
  id: "settings",
  label: l("设置", "Settings"),
  icon: "sliders",
  to: "/settings/profile",
  children: SETTINGS_GROUPS.flatMap((g) => g.items.map(({ icon: _icon, ...c }) => c)),
};

const prefixOf = (c: NavChild) => [c.to, ...(c.match ?? [])];

/** Which top-level section and child the current path belongs to. */
export function locate(pathname: string): { section?: NavItem; child?: NavChild } {
  let best: { section?: NavItem; child?: NavChild; len: number } = { len: -1 };
  for (const s of [...NAV, SETTINGS_NAV]) {
    if (s.to === "/" && pathname === "/") best = { section: s, len: 1 };
    for (const c of s.children)
      for (const p of prefixOf(c)) {
        const base = p.split("/").slice(0, 3).join("/");
        const hit = pathname === p || pathname.startsWith(p + "/") || (base.length > 1 && pathname.startsWith(base));
        if (hit && p.length > best.len) best = { section: s, child: c, len: p.length };
      }
  }
  return best;
}
