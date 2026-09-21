import type { ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { request } from "../../../lib/api";
import type { User, Grant } from "../../../lib/types";
import { useWorkspace } from "../../../lib/workspace";
import { Info } from "../../../lib/icons";
import { ErrorNotice } from "../../../components/ui";
import {
  FilterSelect,
  type FilterOption,
} from "../../../components/filters/FilterBar";
import { ResourceOverlay } from "../../resources/ResourceDrawer";
import "./directory.css";

export type DirectoryMember = User & {
  department?: string;
  job_title?: string;
  source?: string;
  external_id?: string;
  deleted_at?: string | null;
  created_at?: string;
  is_mock?: boolean;
  team_ids?: string[];
};
export type DirectoryTeam = {
  id: string;
  name: string;
  description: string;
  owner_id?: string;
  owner_name?: string;
  department?: string;
  created_at?: string | null;
  updated_at?: string | null;
  is_mock?: boolean;
  member_ids?: string[];
  applications?: {
    id: string;
    name: string;
    key?: string;
    permission?: string;
    relationship?: string;
  }[];
};
export type DirectoryRole = {
  id: User["role"];
  name: string;
  description: string;
  member_count: number;
  permissions: Record<
    "view" | "investigate" | "approve" | "execute" | "manage",
    boolean
  >;
  scope: string;
};
export type Directory = {
  members: DirectoryMember[];
  teams: DirectoryTeam[];
  departments: { name: string; member_count: number; team_count: number }[];
  roles: DirectoryRole[];
  applications: { id: string; name: string; key?: string }[];
  grants: (Grant & { application_id: string })[];
  mock_notice?: string;
};
export const roleLabels = {
  admin: "管理员",
  operator: "操作员",
  viewer: "只读成员",
};
export const dateLabel = (value?: string | null) =>
  value ? value.slice(0, 10) : "—";
export function useDirectory() {
  const { user, preview } = useWorkspace();
  return useQuery({
    queryKey: ["settings-directory"],
    queryFn: ({ signal }) =>
      request<Directory>("/settings/directory", { signal }),
    enabled: user.role === "admin" && !preview,
  });
}
export function useRefreshDirectory() {
  const qc = useQueryClient();
  return () =>
    Promise.all(
      [
        "settings-directory",
        "users",
        "teams",
        "team-members",
        "grants",
        "audit",
      ].map((key) => qc.invalidateQueries({ queryKey: [key] })),
    );
}
export function DirectoryState({
  loading,
  error,
  empty,
  retry,
}: {
  loading: boolean;
  error: unknown;
  empty?: boolean;
  retry: () => unknown;
}) {
  if (error)
    return (
      <div className="directory-state">
        <ErrorNotice error={error} />
        <button className="btn" onClick={() => void retry()}>
          重新加载
        </button>
      </div>
    );
  if (loading)
    return (
      <div className="directory-state" role="status">
        正在读取目录…
      </div>
    );
  if (empty)
    return (
      <div className="directory-state">没有符合条件的记录，请调整筛选。</div>
    );
  return null;
}
export function DirectoryHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description: string;
  actions?: ReactNode;
}) {
  return (
    <header className="directory-heading">
      <div>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {actions && <div className="directory-actions">{actions}</div>}
    </header>
  );
}
export function DirectoryNotice({ children }: { children: ReactNode }) {
  return (
    <div className="directory-notice">
      <Info size={16} />
      <span>{children}</span>
    </div>
  );
}
export function Avatar({ name }: { name: string }) {
  return (
    <span className="directory-avatar" aria-hidden="true">
      {name.slice(0, 1).toUpperCase()}
    </span>
  );
}
export function RoleBadge({ role, label }: { role: string; label?: string }) {
  return (
    <span className={`directory-role directory-role--${role}`}>
      {label || roleLabels[role as keyof typeof roleLabels] || role}
    </span>
  );
}
export function DirectoryFilter({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: FilterOption[];
  onChange: (value: string) => void;
}) {
  return (
    <FilterSelect
      id={`directory-${label}`}
      label={label}
      values={value ? [value] : []}
      options={options}
      onChange={(values) => onChange(values[0] || "")}
      emptyLabel={label}
      showLabel={false}
      includeEmptyOption={false}
      popoverWidth={200}
      clearable
      searchable={options.length > 7}
    />
  );
}
export function DirectoryDrawer({
  title,
  subtitle,
  children,
  onClose,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
  onClose: () => void;
}) {
  return (
    <ResourceOverlay title={title} subtitle={subtitle} onClose={onClose}>
      <div className="resource-overlay-body directory-drawer">{children}</div>
    </ResourceOverlay>
  );
}
export function DrawerFooter({
  busy,
  onClose,
  label = "保存",
  disabled = false,
}: {
  busy: boolean;
  onClose: () => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <footer className="directory-form-footer">
      <button type="button" className="btn" disabled={busy} onClick={onClose}>
        取消
      </button>
      <button
        type="submit"
        className="btn btn-primary"
        disabled={busy || disabled}
      >
        {busy ? "保存中…" : label}
      </button>
    </footer>
  );
}
