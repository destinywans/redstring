import { useEffect } from "react";

/**
 * Keep a list selection pointing at a real item: once the list loads, or when the
 * selected id disappears, select the first item ("" when the list is empty).
 * Pages must not default to fixture ids — those do not exist on a real instance.
 */
export function useFirstSelection<T>(items: readonly T[] | undefined, id: (item: T) => string, current: string, set: (value: string) => void) {
  useEffect(() => {
    if (!items) return;
    if (!items.some((item) => id(item) === current)) set(items.length ? id(items[0]) : "");
    // `id` is a selector literal at every call site; re-running on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, current, set]);
}
