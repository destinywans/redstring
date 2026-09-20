import {
  Home,
  Box,
  Server,
  Activity,
  Bell,
  Bot,
  Plug,
  Settings,
  type IconComponent,
} from "./icons";

export type NavigationSection = {
  id: string;
  group: string;
  label: string;
  icon: IconComponent;
  admin?: boolean;
  children: { to: string; label: string }[];
};

/** Only real destinations belong here. Detail tabs stay with their object. */
export const navigation: NavigationSection[] = [
  {
    id: "overview",
    group: "工作空间",
    label: "工作台",
    icon: Home,
    children: [{ to: "/", label: "运行总览" }],
  },
  {
    id: "applications",
    group: "工作空间",
    label: "应用中心",
    icon: Box,
    children: [{ to: "/applications", label: "全部应用" }],
  },
  {
    id: "infrastructure",
    group: "运行与保障",
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
    group: "运行与保障",
    label: "可观测性",
    icon: Activity,
    children: [
      { to: "/observability/metrics", label: "指标监控" },
      { to: "/observability/logs", label: "日志检索" },
      { to: "/observability/traces", label: "链路追踪" },
      { to: "/observability/topology", label: "服务拓扑" },
    ],
  },
  {
    id: "incidents",
    group: "运行与保障",
    label: "告警与事件",
    icon: Bell,
    children: [
      { to: "/incidents", label: "事件中心" },
      { to: "/alerts", label: "告警列表" },
      { to: "/alerts/sources", label: "告警来源" },
    ],
  },
  {
    id: "agents",
    group: "运行与保障",
    label: "AI 调查",
    icon: Bot,
    children: [
      { to: "/investigations", label: "调查任务" },
      { to: "/knowledge", label: "知识与 Runbook" },
    ],
  },
  {
    id: "integrations",
    group: "平台管理",
    label: "集成中心",
    icon: Plug,
    children: [{ to: "/integrations?tab=tools", label: "连接器" }, { to: "/integrations?tab=models", label: "模型服务" }, { to: "/integrations?tab=knowledge", label: "Embedding 与检索" }],
  },
  {
    id: "settings",
    group: "平台管理",
    label: "平台设置",
    icon: Settings,
    admin: true,
    children: [
      { to: "/platform/settings", label: "平台概览" },
      { to: "/members", label: "成员管理" },
      { to: "/teams", label: "团队管理" },
      { to: "/platform/roles", label: "角色与资源范围" },
      { to: "/settings", label: "自动化策略" },
      { to: "/audit", label: "审计日志" },
    ],
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
    pathname.startsWith("/applications/")
  )
    return true;
  if (
    target.pathname === "/investigations" &&
    (pathname.startsWith("/investigations/") ||
      pathname === "/actions" ||
      pathname === "/recovery")
  )
    return true;
  if (target.pathname !== pathname) return false;
  const params = new URLSearchParams(search);
  return Array.from(target.searchParams).every(
    ([key, value]) =>
      (params.get(key) ?? (key === "view" ? "summary" : key === "tab" ? "tools" : "")) === value,
  );
}
