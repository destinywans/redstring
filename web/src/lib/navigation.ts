import {
  Home,
  GitBranch,
  Box,
  Server,
  Activity,
  Bell,
  Bot,
  BookOpen,
  ScrollText,
  Plug,
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
    label: "Agent",
    icon: Bot,
    children: [
      { to: "/investigations", label: "会话与画布" },
      { to: "/memory", label: "记忆与上下文" },
      { to: "/ai-extensions/skills", label: "Skills" },
      { to: "/ai-extensions/prompts", label: "Prompt 模板" },
      { to: "/settings", label: "自动化任务", admin: true },
    ],
  },
  {
    id: "applications",
    label: "资源中心",
    icon: Box,
    children: [
      { to: "/applications", label: "全部应用" },
      { to: "/resources/catalog", label: "资源目录" },
      { to: "/resources/relations", label: "资源关系" },
    ],
  },
  {
    id: "delivery",
    label: "交付中心",
    icon: GitBranch,
    direct: true,
    children: [{ to: "/delivery", label: "交付中心" }],
  },
  {
    id: "infrastructure",
    label: "基础设施",
    icon: Server,
    children: [
      ["summary", "资源总览"],
      ["clusters", "集群"],
      ["nodes", "节点"],
      ["workloads", "工作负载"],
      ["pods", "Pod 与容器"],
      ["events", "Kubernetes 事件"],
      ["namespaces", "命名空间"],
      ["hosts", "主机与虚拟机"],
      ["network", "网络"],
      ["storage", "存储"],
      ["middleware", "中间件与数据库"],
    ].map(([view, label]) => ({ to: `/infrastructure?view=${view}`, label })),
  },
  {
    id: "observability",
    label: "可观测性",
    icon: Activity,
    children: [
      { to: "/observability/metrics", label: "指标监控" },
      { to: "/observability/logs", label: "日志检索" },
      { to: "/observability/traces", label: "链路追踪" },
      { to: "/observability/topology", label: "调用拓扑" },
    ],
  },
  {
    id: "incidents",
    label: "告警与事件",
    icon: Bell,
    children: [
      { to: "/incidents", label: "事件中心" },
      { to: "/alerts", label: "告警列表" },
      { to: "/alerts/sources", label: "告警来源" },
    ],
  },
  {
    id: "knowledge",
    label: "知识库",
    icon: BookOpen,
    children: [{ to: "/knowledge", label: "Runbook" }],
  },
  {
    id: "integrations",
    label: "集成中心",
    icon: Plug,
    children: [
      { to: "/integrations?tab=tools", label: "连接器" },
      { to: "/ai-extensions/mcp", label: "MCP 服务" },
      { to: "/integrations?tab=models", label: "模型服务" },
      { to: "/integrations?tab=knowledge", label: "Embedding 与检索" },
    ],
  },
  {
    id: "audit",
    label: "审计日志",
    icon: ScrollText,
    admin: true,
    direct: true,
    children: [{ to: "/audit", label: "审计日志" }],
  },
];

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
  ],
};

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
    (pathname.startsWith("/investigations/") ||
      pathname === "/actions" ||
      pathname === "/recovery")
  )
    return true;
  if (target.pathname === "/delivery" && pathname.startsWith("/delivery/"))
    return true;
  if (target.pathname !== pathname) return false;
  const params = new URLSearchParams(search);
  return Array.from(target.searchParams).every(
    ([key, value]) =>
      (params.get(key) ??
        (key === "view" ? "summary" : key === "tab" ? "tools" : "")) === value,
  );
}
