import { ApiError } from "../../lib/api";

export function memberError(error: unknown): unknown {
  if (!(error instanceof ApiError)) return error;
  const messages: Record<string, string> = {
    last_admin: "必须保留至少一位启用的管理员。请先安排其他管理员，再更改此成员。",
    import_preview_stale: "成员目录已发生变化，请重新预览并核对后再导入。",
    import_conflict: "导入名单存在冲突，请修正后重新预览。",
    invalid_member: "成员信息不符合要求。请检查邮箱、姓名和字段长度；新导入成员需先设置至少 12 字符的密码，再恢复使用。",
  };
  return messages[error.code] ? new Error(messages[error.code]) : error;
}

export function importReason(reason?: string): string {
  if (!reason) return "—";
  const messages: Record<string, string> = {
    "Duplicate external identity or email in import": "导入名单中的外部标识或邮箱重复。",
    "External identity belongs to a deleted member": "此外部标识已关联已删除的成员，不能自动恢复。",
    "Email belongs to another identity; automatic takeover is forbidden": "邮箱已被另一账号使用，不能自动覆盖或接管。",
  };
  return messages[reason] ?? reason;
}
