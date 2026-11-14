import { api, apiWithMeta } from "../../api/client";
import type { RoleDetail } from "../../api/types";

type AccessRole = {
  id: string;
  origin: "direct" | "legacy" | "department";
  matrix_revision: number;
};

type MemberAccess = {
  enabled: boolean;
  roles: AccessRole[] | null;
  revision: string;
};

export async function addMemberRole(env: string, memberId: string, roleId: string) {
  const { revision } = await apiWithMeta<RoleDetail>("GET", `/roles/${encodeURIComponent(roleId)}`, {
    headers: { "X-AIOps-Environment": env },
  });
  if (!revision) throw new Error("Role revision is unavailable");
  return updateMemberAccess(env, memberId, { roleId, revision });
}

export async function updateMemberAccess(env: string, memberId: string, addition?: { roleId: string; revision: string }) {
  const path = `/members/${encodeURIComponent(memberId)}/access`;
  const headers = { "X-AIOps-Environment": env };
  const current = await api<MemberAccess>("GET", path, { headers });
  const roles = current.roles ?? [];
  if (addition && roles.some((role) => role.id === addition.roleId)) return;
  const roleRevisions = Object.fromEntries(roles.filter((role) => role.origin === "direct")
    .map((role) => [role.id, String(role.matrix_revision)]));
  if (addition) roleRevisions[addition.roleId] = addition.revision;
  await api("PUT", path, { headers, body: {
    expected_revision: current.revision,
    role_ids: Object.keys(roleRevisions),
    role_revisions: roleRevisions,
    enabled: addition ? current.enabled : false,
  } });
}
