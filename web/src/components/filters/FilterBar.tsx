import type { ReactNode } from "react";
import { Search } from "../../lib/icons";
import {
  FilterGroup,
  FilterPicker,
  type FilterOption,
} from "../patterns/FilterPicker";
import { Button } from "../ui/button";
import "./filters.css";

/** Existing page contracts now share the sample's menu and focus behavior. */
export function FilterBar({
  children,
  label = "搜索与筛选",
}: {
  children: ReactNode;
  label?: string;
}) {
  return (
    <FilterGroup className="filter-bar" role="group" aria-label={label}>
      {children}
    </FilterGroup>
  );
}

/** Draft changes and committed queries remain separate for server-backed searches. */
export function FilterSearch({
  value,
  onChange,
  onSubmit,
  placeholder = "搜索名称或关键词",
  label = "搜索",
  maxLength = 200,
  showSubmit = false,
}: {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  placeholder?: string;
  label?: string;
  maxLength?: number;
  showSubmit?: boolean;
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
          title="输入关键词，按回车搜索"
          enterKeyHint="search"
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
        />
      </div>
      {showSubmit && (
        <Button type="submit" variant="outline">
          搜索
        </Button>
      )}
    </form>
  );
}

export type { FilterOption } from "../patterns/FilterPicker";

export function FilterSelect({
  id,
  label,
  options,
  values,
  onChange,
  multiple = false,
  searchable = false,
  emptyLabel,
  includeEmptyOption = true,
  clearable = false,
  popoverWidth,
}: {
  id: string;
  label: string;
  options: FilterOption[];
  values: string[];
  onChange: (values: string[]) => void;
  multiple?: boolean;
  searchable?: boolean;
  emptyLabel?: string;
  /** Retained for source compatibility; triggers display one concise label. */
  showLabel?: boolean;
  includeEmptyOption?: boolean;
  clearable?: boolean;
  popoverWidth?: number;
}) {
  // Empty ordinary filters are identified by their field names. Explicit scope
  // and business defaults (e.g. 全部授权应用 / 最近 30 分钟) retain their meaning.
  const placeholder =
    !emptyLabel || emptyLabel === "全部" || emptyLabel === `全部${label}`
      ? label
      : emptyLabel;
  return (
    <FilterPicker
      id={id}
      label={label}
      options={options.filter((option) => option.value !== "")}
      value={values.filter(Boolean)}
      onChange={onChange}
      multiple={multiple}
      searchable={searchable}
      emptyLabel={placeholder}
      clearable={multiple || clearable || includeEmptyOption}
      toggleSingle={clearable}
      width={popoverWidth}
      className="filter-select-trigger"
    />
  );
}
