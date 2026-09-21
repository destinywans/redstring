import { useId, useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight } from "../lib/icons";
import { FilterSelect } from "./filters/FilterBar";
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
  /** Optional surface control; count, paging and cursor semantics remain shared. */
  pageSizeControl?: ReactNode;
};

const positiveInteger = (value: number, fallback: number) =>
  Number.isSafeInteger(value) && value > 0 ? value : fallback;

/** One visual footer for exact totals and cursor-based lists. */
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
  pageSizeControl,
}: PaginationProps) {
  const id = useId();
  const size = positiveInteger(pageSize, 20);
  const exactTotal =
    typeof total === "number" && Number.isFinite(total) && total >= 0
      ? Math.floor(total)
      : undefined;
  const pages =
    exactTotal === undefined
      ? undefined
      : Math.max(1, Math.ceil(exactTotal / size));
  const current = Math.min(positiveInteger(page, 1), pages ?? Infinity);
  const sizes = [...new Set([...pageSizeOptions, size])]
    .filter((value) => Number.isSafeInteger(value) && value > 0)
    .sort((a, b) => a - b);
  const nextAvailable = hasNext ?? (pages !== undefined && current < pages);
  return (
    <nav
      className="list-pagination"
      aria-label={ariaLabel}
      aria-busy={loading || undefined}
    >
      <span className="list-pagination-total" aria-live="polite">
        {totalLabel ??
          (exactTotal === undefined ? "总数未提供" : `共 ${exactTotal} 条`)}
      </span>
      <span className="list-pagination-separator" aria-hidden="true">
        ·
      </span>
      <fieldset className="list-pagination-size" disabled={loading}>
        <span>每页</span>
        {pageSizeControl ?? (
          <FilterSelect
            id={`pagination-size-${id}`}
            label="每页条数"
            values={[String(size)]}
            options={sizes.map((value) => ({
              value: String(value),
              label: String(value),
            }))}
            onChange={(values) => {
              const next = Number(values[0]);
              if (!loading && sizes.includes(next) && next !== size)
                onPageSizeChange(next);
            }}
            includeEmptyOption={false}
            showLabel={false}
            popoverWidth={120}
          />
        )}
        <span>条</span>
      </fieldset>
      <span className="list-pagination-separator" aria-hidden="true">
        ·
      </span>
      <span className="list-pagination-page">
        第{" "}
        {pages === undefined ? (
          current
        ) : (
          <input
            key={`${current}:${pages}`}
            className="list-pagination-input"
            type="text"
            inputMode="numeric"
            aria-label="跳转页码"
            title={`输入 1 至 ${pages}，按回车或离开输入框跳转`}
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
                event.currentTarget.value = String(current);
                event.currentTarget.blur();
              }
            }}
          />
        )}
        {pages === undefined ? "" : ` / ${pages}`} 页
      </span>
      <div className="list-pagination-buttons">
        <button
          type="button"
          className="list-pagination-button"
          aria-label="上一页"
          disabled={loading || current <= 1}
          onClick={() => onPageChange(current - 1)}
        >
          <ChevronLeft size={16} />
        </button>
        <button
          type="button"
          className="list-pagination-button"
          aria-label="下一页"
          disabled={loading || !nextAvailable}
          onClick={() => onPageChange(current + 1)}
        >
          <ChevronRight size={16} />
        </button>
      </div>
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
