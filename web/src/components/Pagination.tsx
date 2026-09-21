import { useId, useState } from "react";
import { ChevronLeft, ChevronRight } from "../lib/icons";
import { Select } from "./ui/select";
import { pageNumbers } from "./pageNumbers";
import "./pagination.css";

export type PaginationProps = {
  page: number;
  pageSize: number;
  /** Omit when the API has no exact total. Loaded counts belong in totalLabel. */
  total?: number;
  onPageChange: (page: number) => void;
  onPageSizeChange: (size: number) => void;
  pageSizeOptions?: number[];
  loading?: boolean;
  /** Explicit cursor availability overrides totals; never infer it from a full response page. */
  hasNext?: boolean;
  totalLabel?: string;
  ariaLabel?: string;
};

const positiveInteger = (value: number, fallback: number) =>
  Number.isSafeInteger(value) && value > 0 ? value : fallback;

/** Exact totals get numbered navigation; cursors retain sequential navigation only. */
export function Pagination({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = [10, 20, 50],
  loading = false,
  hasNext,
  totalLabel,
  ariaLabel = "列表分页",
}: PaginationProps) {
  const id = useId();
  const size = positiveInteger(pageSize, 20);
  const exactTotal =
    typeof total === "number" && Number.isSafeInteger(total) && total >= 0
      ? total
      : undefined;
  const pages =
    exactTotal === undefined
      ? undefined
      : Math.max(1, Math.ceil(exactTotal / size));
  const current = Math.min(positiveInteger(page, 1), pages ?? Infinity);
  const sizes = [...new Set([...pageSizeOptions, size])]
    .filter((value) => Number.isSafeInteger(value) && value > 0)
    .sort((a, b) => a - b);
  const nextAvailable =
    (hasNext ?? (pages !== undefined && current < pages)) &&
    (pages === undefined || current < pages);
  const countLabel =
    totalLabel ??
    (exactTotal === undefined ? undefined : `共 ${exactTotal} 条`);
  return (
    <nav
      className="list-pagination"
      aria-label={ariaLabel}
      aria-busy={loading || undefined}
    >
      {countLabel && (
        <span className="list-pagination-total" aria-live="polite">
          {countLabel}
        </span>
      )}
      <div className="list-pagination-buttons">
        <button
          type="button"
          className="list-pagination-button"
          aria-label="上一页"
          disabled={loading || current <= 1}
          onClick={() => onPageChange(current - 1)}
        >
          <ChevronLeft size={16} aria-hidden="true" />
        </button>
        {pages === undefined ? (
          <span className="list-pagination-page" aria-live="polite">
            第 {current} 页
          </span>
        ) : (
          pageNumbers(current, pages).map((item) =>
            typeof item === "number" ? (
              <button
                key={item}
                type="button"
                className="list-pagination-button list-pagination-number"
                aria-label={`第 ${item} 页`}
                aria-current={item === current ? "page" : undefined}
                disabled={loading || pages <= 1}
                onClick={() => {
                  if (item !== current) onPageChange(item);
                }}
              >
                {item}
              </button>
            ) : (
              <span
                key={item}
                className="list-pagination-ellipsis"
                aria-hidden="true"
              >
                …
              </span>
            ),
          )
        )}
        <button
          type="button"
          className="list-pagination-button"
          aria-label="下一页"
          disabled={loading || !nextAvailable}
          onClick={() => onPageChange(current + 1)}
        >
          <ChevronRight size={16} aria-hidden="true" />
        </button>
      </div>
      <div className="list-pagination-size">
        <Select
          id={`pagination-size-${id}`}
          aria-label="每页条数"
          required
          disabled={loading}
          value={String(size)}
          options={sizes.map((value) => ({
            value: String(value),
            label: `${value} 条/页`,
          }))}
          onValueChange={(value) => {
            const next = Number(value);
            if (!loading && sizes.includes(next) && next !== size)
              onPageSizeChange(next);
          }}
        />
      </div>
      {pages !== undefined && (
        <label className="list-pagination-jump">
          <span>前往</span>
          <input
            key={`${current}:${pages}`}
            className="list-pagination-input"
            type="text"
            inputMode="numeric"
            aria-label="跳转页码"
            title={`共 ${pages} 页，输入 1 至 ${pages}，按回车或离开输入框跳转`}
            defaultValue={current}
            disabled={loading || pages <= 1}
            onFocus={(event) => event.currentTarget.select()}
            onBlur={(event) => {
              const raw = event.currentTarget.value.trim();
              const next = /^\d+$/.test(raw) ? Number(raw) : NaN;
              event.currentTarget.value = String(current);
              if (
                !loading &&
                Number.isSafeInteger(next) &&
                next >= 1 &&
                next <= pages &&
                next !== current
              )
                onPageChange(next);
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                event.currentTarget.blur();
              } else if (event.key === "Escape") {
                event.preventDefault();
                event.stopPropagation();
                event.currentTarget.value = String(current);
                event.currentTarget.blur();
              }
            }}
          />
          <span>页</span>
        </label>
      )}
    </nav>
  );
}
export default Pagination;

/** Paginate an available array; callers retain responsibility for its completeness. */
export function useListPagination<T>(
  items: T[],
  resetKey: string,
  defaultPageSize = 20,
) {
  const [state, setState] = useState({
    key: resetKey,
    page: 1,
    pageSize: positiveInteger(defaultPageSize, 20),
  });
  const total = items.length;
  const pages = Math.max(1, Math.ceil(total / state.pageSize));
  // Commit filter resets and page clamping; restoring old data must not restore an old page.
  if (state.key !== resetKey || state.page > pages)
    setState({
      ...state,
      key: resetKey,
      page: state.key === resetKey ? pages : 1,
    });
  const page = state.key === resetKey ? Math.min(state.page, pages) : 1;
  return {
    items: items.slice((page - 1) * state.pageSize, page * state.pageSize),
    page,
    pageSize: state.pageSize,
    total,
    setPage: (next: number) =>
      setState((current) => ({
        ...current,
        key: resetKey,
        page: Math.max(1, Math.min(positiveInteger(next, 1), pages)),
      })),
    setPageSize: (next: number) =>
      setState((current) => ({
        ...current,
        key: resetKey,
        page: 1,
        pageSize: positiveInteger(next, current.pageSize),
      })),
  };
}
