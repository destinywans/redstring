import {
  createContext,
  useContext,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import * as Popover from "@radix-ui/react-popover";
import { Check, ChevronDown, Search } from "../../lib/icons";
import "./filters.css";

const FilterContext = createContext<{
  openId: string | null;
  setOpenId: (id: string | null) => void;
} | null>(null);

/** Keep one floating filter open within each toolbar. */
export function FilterBar({
  children,
  label = "搜索与筛选",
}: {
  children: ReactNode;
  label?: string;
}) {
  const [openId, setOpenId] = useState<string | null>(null);
  return (
    <FilterContext.Provider value={{ openId, setOpenId }}>
      <div className="filter-bar" role="group" aria-label={label}>
        {children}
      </div>
    </FilterContext.Provider>
  );
}

export function FilterSearch({
  value,
  onChange,
  onSubmit,
  placeholder = "搜索名称或关键词",
  label = "搜索",
  maxLength = 200,
}: {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  placeholder?: string;
  label?: string;
  maxLength?: number;
}) {
  return (
    <form
      className="filter-search"
      role="search"
      aria-label={label}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      <div className="filter-search-field">
        <Search size={16} aria-hidden="true" />
        <input
          type="search"
          maxLength={maxLength}
          aria-label={label}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
        />
      </div>
      <button type="submit" className="filter-button">
        搜索
      </button>
    </form>
  );
}

export type FilterOption = {
  value: string;
  label: string;
  description?: string;
  disabled?: boolean;
};

export function FilterSelect({
  id,
  label,
  options,
  values,
  onChange,
  multiple = false,
  searchable = false,
  emptyLabel = "全部",
}: {
  id: string;
  label: string;
  options: FilterOption[];
  values: string[];
  onChange: (values: string[]) => void;
  multiple?: boolean;
  searchable?: boolean;
  emptyLabel?: string;
}) {
  const group = useContext(FilterContext);
  const instanceId = useId();
  const openKey = `${id}-${instanceId}`;
  const [localOpen, setLocalOpen] = useState(false);
  const open = group ? group.openId === openKey : localOpen;
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<string[]>([]);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listId = `filter-options-${instanceId}`;
  const selected = multiple ? draft : values;
  const candidates = multiple
    ? options
    : [{ value: "", label: emptyLabel }, ...options.filter((o) => o.value)];
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visible = candidates.filter((option) =>
    `${option.label} ${option.description ?? ""}`
      .toLocaleLowerCase()
      .includes(normalizedQuery),
  );
  const summary = !values.length
    ? emptyLabel
    : values.length === 1
      ? (options.find((option) => option.value === values[0])?.label ??
        "已选 1 项")
      : `已选 ${values.length} 项`;

  function changeOpen(next: boolean) {
    if (next) {
      setQuery("");
      setDraft([...values]);
    }
    if (group) {
      // An old popover's dismissal must not close the newly opened filter.
      if (next) group.setOpenId(openKey);
      else if (group.openId === openKey) group.setOpenId(null);
    } else setLocalOpen(next);
  }

  function choose(option: FilterOption) {
    if (option.disabled) return;
    if (multiple) {
      setDraft((current) =>
        current.includes(option.value)
          ? current.filter((value) => value !== option.value)
          : [...current, option.value],
      );
    } else {
      onChange(option.value ? [option.value] : []);
      changeOpen(false);
    }
  }

  function focusOption(position: "first" | "last" | "selected") {
    const items = listRef.current?.querySelectorAll<HTMLElement>(
      '[role="option"]:not([aria-disabled="true"])',
    );
    if (!items?.length) return;
    const target =
      position === "last"
        ? items[items.length - 1]
        : position === "selected"
          ? (Array.from(items).find(
              (item) => item.getAttribute("aria-selected") === "true",
            ) ?? items[0])
          : items[0];
    target.focus();
  }

  function navigateOptions(event: KeyboardEvent<HTMLDivElement>) {
    const items = Array.from(
      listRef.current?.querySelectorAll<HTMLElement>(
        '[role="option"]:not([aria-disabled="true"])',
      ) ?? [],
    );
    if (!items.length) return;
    const current = items.indexOf(document.activeElement as HTMLElement);
    let next: number;
    switch (event.key) {
      case "ArrowDown":
        next = (current + 1) % items.length;
        break;
      case "ArrowUp":
        next = (current - 1 + items.length) % items.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = items.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    items[next].focus();
  }

  return (
    <Popover.Root open={open} onOpenChange={changeOpen}>
      <Popover.Trigger asChild>
        <button
          ref={triggerRef}
          type="button"
          className="filter-select-trigger"
          aria-label={`${label} · ${summary}`}
          title={`${label} · ${summary}`}
          data-active={values.length > 0 || undefined}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              changeOpen(true);
            }
          }}
        >
          <span className="filter-trigger-label">{label}</span>
          <span className="filter-trigger-separator" aria-hidden="true">
            ·
          </span>
          <span className="filter-trigger-value">{summary}</span>
          <ChevronDown size={14} aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="filter-popover"
          align="start"
          sideOffset={6}
          collisionPadding={12}
          aria-label={`筛选${label}`}
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            if (searchable) searchRef.current?.focus();
            else focusOption("selected");
          }}
          onCloseAutoFocus={(event) => {
            // Preserve an intentional click on another control; restore focus for
            // keyboard dismissal and explicit Cancel/Apply through Radix's default.
            if (group?.openId && group.openId !== openKey)
              event.preventDefault();
          }}
        >
          {searchable && (
            <div className="filter-option-search">
              <Search size={16} aria-hidden="true" />
              <input
                ref={searchRef}
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                aria-label={`搜索${label}选项`}
                placeholder={`搜索${label}`}
                aria-controls={listId}
                onKeyDown={(event) => {
                  if (event.key === "ArrowDown") {
                    event.preventDefault();
                    focusOption("first");
                  }
                }}
              />
            </div>
          )}
          <div
            ref={listRef}
            id={listId}
            role="listbox"
            aria-label={label}
            aria-multiselectable={multiple || undefined}
            className="filter-options"
            onKeyDown={navigateOptions}
          >
            {visible.map((option) => {
              const checked = option.value
                ? selected.includes(option.value)
                : selected.length === 0;
              return (
                <div
                  key={option.value}
                  role="option"
                  aria-selected={checked}
                  aria-disabled={option.disabled || undefined}
                  tabIndex={option.disabled ? -1 : 0}
                  className="filter-option"
                  title={[option.label, option.description]
                    .filter(Boolean)
                    .join(" · ")}
                  onClick={() => choose(option)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      choose(option);
                    }
                  }}
                >
                  <span
                    className={`filter-option-mark ${multiple ? "is-multiple" : ""}`}
                    aria-hidden="true"
                  >
                    {checked && <Check size={13} weight="bold" />}
                  </span>
                  <span className="filter-option-copy">
                    <span>{option.label}</span>
                    {option.description && <small>{option.description}</small>}
                  </span>
                </div>
              );
            })}
          </div>
          {!visible.length && (
            <div className="filter-empty" role="status">
              没有匹配的{label}
            </div>
          )}
          {multiple && (
            <div className="filter-popover-footer">
              <button
                type="button"
                className="filter-text-button"
                onClick={() => setDraft([])}
              >
                清空选择
              </button>
              <div className="filter-footer-actions">
                <button
                  type="button"
                  className="filter-button"
                  onClick={() => changeOpen(false)}
                >
                  取消
                </button>
                <button
                  type="button"
                  className="filter-button filter-apply"
                  onClick={() => {
                    onChange(draft);
                    changeOpen(false);
                  }}
                >
                  应用
                </button>
              </div>
            </div>
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
