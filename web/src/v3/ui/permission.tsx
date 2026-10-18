import { cloneElement, isValidElement, type ReactElement, type ReactNode } from "react";
import { useGet } from "../api/client";
import type { Me } from "../api/types";
import { useEnv } from "../app/environment";
import { useI18n } from "../i18n";
import { Tooltip } from "./overlays";

/** Module and action names as the roles matrix shows them (same wording as the server's role catalogue). */
const MODULES: Record<string, [string, string]> = {
  conversation: ["对话与画布", "Conversations & canvases"],
  canvas: ["画布", "Canvases"],
  events: ["事件与告警", "Incidents & alerts"],
  alert_rules: ["告警规则与路由", "Alert rules & routing"],
  silences: ["静默", "Silences"],
  proposals: ["写操作提案", "Write proposals"],
  runbooks: ["Runbook", "Runbook"],
  knowledge: ["知识来源与检索", "Knowledge sources & search"],
  services: ["服务目录", "Service catalog"],
  connectors: ["连接器与映射", "Connectors & mapping"],
  agents: ["Agent 配置与发布", "Agent config & release"],
  identity: ["成员与角色", "Members & roles"],
  members: ["成员", "Members"],
  roles: ["角色", "Roles"],
  audit: ["审计日志", "Audit log"],
  resources: ["资源目录与关系", "Resource catalog & relations"],
  home: ["总览", "Overview"],
  traces: ["链路", "Traces"],
  logs: ["日志", "Logs"],
  models: ["模型", "Models"],
  workspace: ["工作区设置", "Workspace settings"],
};
const ACTIONS: Record<string, [string, string]> = {
  view: ["查看", "View"],
  change: ["修改", "Change"],
  delete: ["删除", "Delete"],
  approve: ["批准", "Approve"],
  manage: ["管理", "Manage"],
};

export type Need = readonly [module: string, action: string];

/**
 * Says why the current person can't use a write entry, or null when they may (PA-64, D10). Reads the effective
 * grants `/me` returns for the selected environment: any reach ("all", "scoped", "conditional") keeps the entry
 * enabled and the server still decides per object; a missing action disables it. A `/me` without `permissions`
 * (sample data, older servers) or an environment the answer doesn't cover yet (mid-switch) never disables anything.
 */
export function usePermission() {
  const { tx, zh } = useI18n();
  const env = useEnv((s) => s.env);
  const { data: me } = useGet<Me>("/me");
  return ([module, action]: Need): string | null => {
    const grants = me?.permissions;
    if (!grants) return null;
    const scopes = env === "all" ? Object.values(grants) : grants[env] ? [grants[env]] : null;
    if (!scopes || scopes.some((m) => !!m?.[module]?.[action])) return null;
    const m = MODULES[module] ?? [module, module];
    const a = ACTIONS[action] ?? [action, action];
    return tx("需要 {m}·{a} 权限", "Needs {m} · {a} permission", { m: zh ? m[0] : m[1], a: zh ? a[0] : a[1] });
  };
}

/** Shorthand for one entry. */
export function useNeed(need: Need) {
  return usePermission()(need);
}

/**
 * Wraps one button-like element: when the permission is missing it renders disabled in its existing style, with a
 * tooltip (and screen-reader text) naming the permission to ask for. Disabled buttons swallow hover, so the wrapper
 * takes the pointer instead.
 */
export function Gate({ need, children, grow }: { need: Need; children: ReactNode; grow?: number }) {
  const reason = useNeed(need);
  return (
    <Gated reason={reason} grow={grow}>
      {children}
    </Gated>
  );
}

/** `Gate` for callers that already computed the reason (lists, several entries with one check). */
export function Gated({ reason, children, grow, kind = "permission" }: {
  reason: string | null;
  children: ReactNode;
  /** flex-grow of the entry inside a row */
  grow?: number;
  /** "availability" holds an entry whose source isn't configured; only permission gates must name a grant. */
  kind?: "permission" | "availability";
}) {
  if (!reason || !isValidElement(children)) return <>{children}</>;
  return (
    <Tooltip label={reason} grow={grow}>
      <span className={kind === "permission" ? "perm-gate" : "avail-gate"} style={grow ? { flexGrow: 1 } : undefined}>
        {cloneElement(children as ReactElement<{ disabled?: boolean }>, { disabled: true })}
        <span className="sr-only">{reason}</span>
      </span>
    </Tooltip>
  );
}
