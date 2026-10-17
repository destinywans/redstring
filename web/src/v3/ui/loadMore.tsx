import { useEffect, useState } from "react";
import { API_MODE, api, type Query } from "../api/client";
import { useEnv } from "../app/environment";
import { useI18n } from "../i18n";
import { Btn, Muted, Spacer } from "./core";
import "./ui.css";

/** A first page as the list endpoints return it: page-numbered (total, page, page_size) or cursor-based (next_cursor). */
type FirstPage<T> = { items: T[]; total?: number; page?: number; page_size?: number; next_cursor?: string | null };

/**
 * Later pages of a list whose first page comes from useGet (PA-133, design A "load more"). Pages are appended in
 * order and kept until the query, environment or language changes; items already shown (same id) are skipped, so a
 * row that moved between pages isn't listed twice. Page-numbered lists know the total; cursor lists know only
 * whether there is more.
 */
export function useMorePages<T extends { id: string }>(path: string, query: Query, first: FirstPage<T> | undefined) {
  const env = useEnv((s) => s.env);
  const { locale } = useI18n();
  const key = JSON.stringify([path, query, env, locale]);
  const [state, setState] = useState<{ key: string; items: T[]; page: number; cursor: string | null; done: boolean }>({ key, items: [], page: 1, cursor: null, done: false });
  const [loading, setLoading] = useState(false);
  const fresh = state.key !== key;
  useEffect(() => {
    if (fresh) setState({ key, items: [], page: 1, cursor: null, done: false });
  }, [fresh, key]);
  const extra = fresh ? [] : state.items;
  const seen = new Set((first?.items ?? []).map((i) => i.id));
  const items = [...(first?.items ?? []), ...extra.filter((i) => !seen.has(i.id))];
  const cursorMode = first?.page === undefined;
  const cursor = fresh || !state.items.length ? (first?.next_cursor ?? null) : state.cursor;
  const hasMore = API_MODE !== "mock" && !!first && !(fresh ? false : state.done) && (cursorMode ? !!cursor : (first.total ?? 0) > items.length);
  const pageSize = first?.page_size ?? first?.items.length ?? 0;
  const more = async () => {
    if (!first || loading || !hasMore) return;
    setLoading(true);
    try {
      const page = fresh ? 1 : state.page;
      const next = await api<FirstPage<T>>("GET", path, { query: { ...query, ...(cursorMode ? { cursor: cursor ?? undefined } : { page: page + 1 }) } });
      setState((s) => {
        const base = s.key === key ? s : { key, items: [], page: 1, cursor: null, done: false };
        return { key, items: [...base.items, ...next.items], page: cursorMode ? base.page : page + 1, cursor: next.next_cursor ?? null, done: !next.items.length };
      });
    } finally {
      setLoading(false);
    }
  };
  return { items, hasMore, more, loading, pageSize, total: first?.total };
}

/** The footer of a list with more to load (PA-133, design A): what is shown, of how many, and the button for the next page. */
export function LoadMore({ shown, total, step, loading, onMore }: { shown: number; total?: number | null; step: number; loading: boolean; onMore: () => void }) {
  const { tx } = useI18n();
  return (
    <div className="v3-loadmore">
      <Muted size={12}>{total != null ? tx("已显示 {n} 条，共 {t} 条", "Showing {n} of {t}", { n: shown, t: total }) : tx("已显示 {n} 条", "Showing {n}", { n: shown })}</Muted>
      <Spacer />
      <Btn h={26} kind="secondary" loading={loading} onClick={onMore}>
        {step > 0 ? tx("再加载 {n} 条", "Load {n} more", { n: step }) : tx("加载更多", "Load more")}
      </Btn>
    </div>
  );
}
