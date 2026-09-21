import { ApiError, request } from "../../../lib/api";
export type RbacKind = "roles" | "permissions";
export type RelationKind =
  "permissions" | "users" | "departments" | "resources";
export type RbacObject = {
  id: string;
  name: string;
  description: string;
  builtin: boolean;
  revision: number;
  action?: string;
  scope_type?: string;
  permission_count?: number;
  user_count?: number;
  department_count?: number;
  resource_count?: number;
};
export type Candidate = {
  id: string;
  name: string;
  description?: string;
  selected: boolean;
  inherited?: boolean;
  is_mock?: boolean;
  source_departments?: string[];
  action?: string;
  scope_type?: string;
  resource_count?: number;
  email?: string;
  kind?: string;
  application_id?: string;
  application_name?: string;
  environment?: string;
  department_id?: string;
  department_name?: string;
  member_count?: number;
};
export type Page<T> = {
  items: T[];
  total: number;
  page: number;
  page_size: number;
  revision?: number;
};
export type Delta = { add: string[]; remove: string[] };
export type Deltas = Record<RelationKind, Delta>;
export const emptyDeltas = (): Deltas => ({
  permissions: { add: [], remove: [] },
  users: { add: [], remove: [] },
  departments: { add: [], remove: [] },
  resources: { add: [], remove: [] },
});
export const actionNames: Record<string, string> = {
  view: "查看",
  update: "更改",
  delete: "删除",
  investigate: "调查",
  approve: "审批",
  execute: "执行",
  manage: "管理",
};
export const scopeNames: Record<string, string> = {
  resource: "资源操作",
  application: "平台能力 · 应用范围",
  management: "平台能力 · 管理对象",
  legacy_application: "兼容应用授权",
};
export function queryString(
  values: Record<string, string | number | undefined>,
) {
  const params = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => {
    if (value !== undefined && value !== "") params.set(key, String(value));
  });
  return params.toString();
}
export const getPage = <T>(
  path: string,
  values: Record<string, string | number | undefined>,
  signal?: AbortSignal,
) => request<Page<T>>(`${path}?${queryString(values)}`, { signal });
export function rbacError(error: unknown) {
  if (error instanceof ApiError && error.status === 409)
    return new Error(
      `保存未完成：${error.message}。当前修改已保留；如数据版本已变化，请重新加载后核对关联。`,
    );
  return error;
}
export const selectedCandidate = (row: Candidate, delta: Delta) =>
  delta.add.includes(row.id) ||
  (row.selected && !delta.remove.includes(row.id));
export function updateDelta(
  delta: Delta,
  row: Candidate,
  checked: boolean,
): Delta {
  return {
    add: [
      ...delta.add.filter((id) => id !== row.id),
      ...(!row.selected && checked ? [row.id] : []),
    ],
    remove: [
      ...delta.remove.filter((id) => id !== row.id),
      ...(row.selected && !checked ? [row.id] : []),
    ],
  };
}
