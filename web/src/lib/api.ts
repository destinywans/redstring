export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}
const sessionRequests = new AbortController();
export function expireSession() {
  sessionRequests.abort();
  window.location.replace("/?session=expired");
}
let expiring = false;
export async function request<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    signal: init.signal ?? sessionRequests.signal,
    credentials: "same-origin",
    headers: {
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...init.headers,
    },
  });
  if (
    response.status === 401 &&
    !["/auth/login", "/auth/me"].includes(path) &&
    !expiring
  ) {
    expiring = true;
    expireSession();
    throw new ApiError(401, "unauthorized", "登录已过期，请重新登录。");
  }
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new ApiError(
      response.status,
      body?.error?.code ?? "request_failed",
      body?.error?.message ?? `请求失败 (${response.status})`,
    );
  }
  if (response.status === 204) return undefined as T;
  const type = response.headers.get("content-type") ?? "";
  if (!type.includes("application/json"))
    throw new ApiError(
      502,
      "invalid_response",
      "服务返回了非 JSON 响应，请检查 API 连接。",
    );
  return response.json() as Promise<T>;
}
export const post = <T>(path: string, body?: unknown) =>
  request<T>(path, {
    method: "POST",
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
export const patch = <T>(path: string, body: unknown) =>
  request<T>(path, { method: "PATCH", body: JSON.stringify(body) });
export const put = <T>(path: string, body: unknown) =>
  request<T>(path, { method: "PUT", body: JSON.stringify(body) });
