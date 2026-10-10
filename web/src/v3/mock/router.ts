import { currentLocale } from "../app/preferences";
import { pick, type L } from "../i18n";
import { currentEnv } from "../app/environment";

type Ctx = { params: Record<string, string>; query: Record<string, unknown>; body: unknown };
type Handler = (ctx: Ctx) => unknown;
type Route = { method: string; re: RegExp; keys: string[]; handler: Handler };

const routes: Route[] = [];

/** Register a contract endpoint served from fixtures. Paths use `:param` segments. */
export function mock(method: string, path: string, handler: Handler) {
  const keys: string[] = [];
  const re = new RegExp(
    "^" +
      path.replace(/\//g, "\\/").replace(/:(\w+)/g, (_, k) => {
        keys.push(k);
        return "([^/]+)";
      }) +
      "$",
  );
  routes.push({ method, re, keys, handler });
}

export class MockError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public detail: Record<string, unknown> = {},
  ) {
    super(message);
  }
}

function isL(v: unknown): v is L {
  return typeof v === "object" && v !== null && "zh" in v && "en" in v && Object.keys(v).length === 2;
}

/**
 * Fixtures are written as the production instance. When the demo shows a non-production instance,
 * production env tags and cluster names are rewritten to the selected environment, since a
 * non-production instance never holds production data.
 */
function lowerEnv(): { id: string; cluster: string } | null {
  try {
    if (localStorage.getItem("aiops-v3-demo-instance") !== "nonprod") return null;
  } catch {
    return null;
  }
  const env = currentEnv();
  const id = env && env !== "all" ? env : "staging";
  return { id, cluster: id === "test" ? "aiops-test" : id.startsWith("feature-") ? "dev-sh-1" : `${id}-sh-1` };
}

/** Fixtures hold bilingual literals; the "server" answers in the request language, as the real API will. */
function localise(value: unknown, lower: ReturnType<typeof lowerEnv>, key = ""): unknown {
  if (isL(value)) return localise(pick(currentLocale(), value), lower, key);
  if (typeof value === "string" && lower) {
    if (key === "env" && value === "prod") return lower.id;
    return value.replace(/\bprod-(sh|bj)-1\b/g, lower.cluster);
  }
  if (Array.isArray(value)) return value.map((v) => localise(v, lower, key));
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value)) out[k] = localise(v, lower, k);
    return out;
  }
  return value;
}

const delay = () => new Promise((r) => setTimeout(r, 140 + Math.random() * 220));
let loaded: Promise<unknown> | null = null;

export async function handleMock<T>(method: string, path: string, query: Record<string, unknown>, body: unknown): Promise<T> {
  loaded ??= import("./handlers");
  await loaded;
  await delay();
  for (const r of routes) {
    if (r.method !== method) continue;
    const m = path.match(r.re);
    if (!m) continue;
    const params = Object.fromEntries(r.keys.map((k, i) => [k, decodeURIComponent(m[i + 1])]));
    try {
      return localise(r.handler({ params, query, body }), path === "/instance" ? null : lowerEnv()) as T;
    } catch (e) {
      if (e instanceof MockError) {
        const { ApiError } = await import("../api/client");
        throw new ApiError(e.status, e.code, e.message, { code: e.code, message: e.message, ...e.detail });
      }
      throw e;
    }
  }
  const { ApiError } = await import("../api/client");
  throw new ApiError(404, "not_found", `No mock for ${method} ${path}`);
}

/** Paginate a fixture list with the contract's page/page_size semantics. */
export function paginate<T>(items: T[], query: Record<string, unknown>) {
  const page = Math.max(1, Number(query.page ?? 1));
  const size = Math.max(1, Number(query.page_size ?? 50));
  return { items: items.slice((page - 1) * size, page * size), total: items.length, page, page_size: size };
}
