import { useQuery } from "@tanstack/react-query";
import { request } from "../../../lib/api";
export function useRbacAccess(
  user: { id: string; role: string } | undefined,
  preview = false,
) {
  const query = useQuery({
    queryKey: ["rbac-me", user?.id],
    queryFn: ({ signal }) =>
      request<{ management_objects: string[] }>("/rbac/me", { signal }),
    enabled: !!user && !preview,
  });
  return {
    ...query,
    canManage: (object: string) =>
      user?.role === "admin" ||
      !!query.data?.management_objects?.includes(object),
  };
}
export const isRbacPath = (path: string) =>
  path === "/platform/roles" || path === "/platform/permissions";
