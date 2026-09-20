import type { User } from "./types";

/** Use only authorized directory results and the signed-in user's own profile. */
export function memberLabel(
  id: string | undefined,
  currentUser: User,
  knownUsers: User[] = [],
): string {
  if (!id) return "待指定";
  const member =
    id === currentUser.id
      ? currentUser
      : knownUsers.find((user) => user.id === id);
  if (member?.name?.trim()) return member.name;
  return `成员 ${id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id}`;
}
