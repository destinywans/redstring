import {
  Home,
  Box,
  Activity,
  Bot,
  BookOpen,
  Settings,
  type IconComponent,
} from "./icons";

export type NavigationSection = {
  id: string;
  label: string;
  icon: IconComponent;
  admin?: boolean;
  direct?: boolean;
  children: { to: string; label: string; admin?: boolean }[];
};

/** Only real destinations belong here. Detail tabs stay with their object. */
export const navigation: NavigationSection[] = [
  {
    id: "overview",
    label: "工作台",
    icon: Home,
    direct: true,
    children: [{ to: "/", label: "工作台" }],
  },
  {
    id: "agents",
    label: "调查",
    icon: Bot,
    children: [
      { to: "/investigations", label: "会话与画布" },
      { to: "/actions", label: "待审批操作" },
      { to: "/recovery", label: "恢复验证" },
      { to: "/memory", label: "记忆与上下文" },
      { to: "/ai-extensions/skills", label: "Skills" },
      { to: "/ai-extensions/prompts", label: "Prompt 模板" },
      { to: "/automation", label: "自动化任务", admin: true },
    ],
  },
  {
    id: "applications",
    label: "应用与资源",
    icon: Box,
    children: [
      { to: "/applications", label: "全部应用" },
      { to: "/resources/catalog", label: "资源目录" },
      { to: "/resources/relations", label: "资源关系" },
    ],
  },
  {
    id: "observability",
    label: "观测与告警",
    icon: Activity,
    children: [
      { to: "/incidents", label: "事件中心" },
      { to: "/alerts", label: "告警列表" },
      { to: "/infrastructure", label: "基础设施" },
      { to: "/observability/metrics", label: "指标监控" },
      { to: "/observability/logs", label: "日志检索" },
      { to: "/observability/traces", label: "链路追踪" },
      { to: "/observability/topology", label: "调用拓扑" },
      { to: "/alerts/sources", label: "告警来源" },
    ],
  },
  {
    id: "knowledge",
    label: "知识与集成",
    icon: BookOpen,
    children: [
      { to: "/knowledge", label: "Runbook" },
      { to: "/integrations", label: "集成中心" },
    ],
  },
];

/** Integration center tabs; `mcp` is rendered by the AI extensions page. */
export const integrationTabs = [
  { id: "tools", to: "/integrations/tools", label: "连接器" },
  { id: "mcp", to: "/integrations/mcp", label: "MCP 服务" },
  { id: "models", to: "/integrations/models", label: "模型服务" },
  { id: "knowledge", to: "/integrations/knowledge", label: "Embedding 与检索" },
] as const;

export const settingsNavigation: NavigationSection = {
  id: "account-settings",
  label: "设置",
  icon: Settings,
  children: [
    { to: "/profile", label: "个人资料" },
    { to: "/platform/settings", label: "平台概览", admin: true },
    { to: "/members", label: "成员管理", admin: true },
    { to: "/teams", label: "部门管理", admin: true },
    { to: "/platform/roles", label: "角色管理", admin: true },
    { to: "/platform/permissions", label: "权限管理", admin: true },
    { to: "/audit", label: "审计日志", admin: true },
  ],
};

/**
 * Old URLs kept working after the navigation regrouping. Query strings
 * (preview, environment scope, filters) are carried over by the redirect.
 */
export const legacyRedirects: {
  from: string;
  to: string | ((params: URLSearchParams) => string);
}[] = [
  { from: "/settings", to: "/automation" },
  { from: "/ai-extensions/mcp", to: "/integrations/mcp" },
  {
    from: "/integrations",
    to: (params) =>
      `/integrations/${integrationTabs.find((t) => t.id === params.get("tab"))?.id ?? "tools"}`,
  },
];

export function isNavigationCurrent(
  to: string,
  pathname: string,
  search: string,
) {
  const target = new URL(to, "http://workspace.local");
  if (
    target.pathname === "/applications" &&
    (pathname === "/applications" || pathname.startsWith("/applications/"))
  )
    return !target.searchParams.has("view");
  if (
    target.pathname === "/investigations" &&
    pathname.startsWith("/investigations/")
  )
    return true;
  if (
    target.pathname === "/integrations" &&
    pathname.startsWith("/integrations/")
  )
    return true;
  if (target.pathname !== pathname) return false;
  const params = new URLSearchParams(search);
  return Array.from(target.searchParams).every(
    ([key, value]) => params.get(key) === value,
  );
}
