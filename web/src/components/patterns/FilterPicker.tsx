import {
  createContext,
  useContext,
  useRef,
  useState,
  type HTMLAttributes,
} from "react";
import * as Popover from "@radix-ui/react-popover";
import { Command } from "cmdk";
import { Check, ChevronDown, Search } from "../../lib/icons";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import "../ui/system.css";

interface FilterGroupState {
  activeId: string | null;
  setActive: (id: string, open: boolean) => void;
}
const FilterContext = createContext<FilterGroupState | null>(null);

export function FilterGroup({
  className,
  children,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  const [activeId, setActiveId] = useState<string | null>(null);
  return (
    <FilterContext.Provider
      value={{
        activeId,
        setActive: (id, open) =>
          setActiveId((current) =>
            open ? id : current === id ? null : current,
          ),
      }}
    >
      <div className={cn("ui-filter-group", className)} {...props}>
        {children}
      </div>
    </FilterContext.Provider>
  );
}

export interface FilterOption {
  value: string;
  label: string;
  disabled?: boolean;
}
export interface FilterPickerProps {
  id: string;
  label: string;
  options: FilterOption[];
  value: string[];
  onChange: (value: string[]) => void;
  multiple?: boolean;
  searchable?: boolean;
  placeholder?: string;
  width?: number;
}

export function FilterPicker({
  id,
  label,
  options,
  value,
  onChange,
  multiple = false,
  searchable = false,
  placeholder,
  width = 248,
}: FilterPickerProps) {
  const group = useContext(FilterContext);
  const [localOpen, setLocalOpen] = useState(false);
  const [draft, setDraft] = useState<string[]>(value);
  const [query, setQuery] = useState("");
  const commandRef = useRef<HTMLDivElement>(null);
  const open = group ? group.activeId === id : localOpen;
  const selected = multiple ? draft : value;
  const firstLabel =
    options.find((option) => option.value === value[0])?.label ?? value[0];
  function setOpen(next: boolean) {
    if (next) {
      setDraft([...value]);
      setQuery("");
    }
    if (group) group.setActive(id, next);
    else setLocalOpen(next);
  }
  function pick(option: FilterOption) {
    if (multiple)
      setDraft((current) =>
        current.includes(option.value)
          ? current.filter((item) => item !== option.value)
          : [...current, option.value],
      );
    else {
      onChange([option.value]);
      setOpen(false);
    }
  }
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <Button
          id={id}
          variant="outline"
          className={cn(
            "ui-filter-trigger",
            value.length > 0 && "ui-filter-active",
          )}
          aria-label={label}
        >
          <span className="ui-filter-trigger-text">
            {value.length ? firstLabel : label}
          </span>
          {value.length > 1 && (
            <span className="ui-filter-count">+{value.length - 1}</span>
          )}
          <ChevronDown size={14} aria-hidden="true" />
        </Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="ui-filter-content"
          style={{ width }}
          align="start"
          sideOffset={6}
          collisionPadding={12}
          aria-label={`${label}筛选`}
          onOpenAutoFocus={(event) => {
            if (!searchable) {
              event.preventDefault();
              commandRef.current?.focus();
            }
          }}
        >
          <Command
            ref={commandRef}
            className="ui-filter-command"
            tabIndex={searchable ? -1 : 0}
            label={`${label}选项`}
            loop
          >
            {searchable && (
              <div className="ui-filter-search">
                <Search size={16} aria-hidden="true" />
                <Command.Input
                  value={query}
                  onValueChange={setQuery}
                  placeholder={placeholder ?? `搜索${label}`}
                  aria-label={`搜索${label}`}
                />
              </div>
            )}
            <Command.List
              className="ui-filter-list"
              aria-label={`${label}选项`}
            >
              <Command.Empty className="ui-filter-empty">
                没有匹配项
              </Command.Empty>
              {options.map((option) => (
                <Command.Item
                  key={option.value}
                  value={option.value}
                  keywords={[option.label]}
                  disabled={option.disabled}
                  onSelect={() => pick(option)}
                  className="ui-filter-item"
                  data-checked={selected.includes(option.value)}
                  aria-label={`${option.label}${selected.includes(option.value) ? "，已选择" : ""}`}
                >
                  {multiple && (
                    <span className="ui-filter-checkbox" aria-hidden="true">
                      {selected.includes(option.value) && <Check size={12} />}
                    </span>
                  )}
                  <span className="ui-filter-option-label">{option.label}</span>
                  {!multiple && selected.includes(option.value) && (
                    <Check size={15} aria-hidden="true" />
                  )}
                </Command.Item>
              ))}
            </Command.List>
          </Command>
          <div className="ui-filter-footer">
            <Button
              variant="ghost"
              size="sm"
              disabled={!selected.length}
              onClick={() => {
                if (multiple) setDraft([]);
                else {
                  onChange([]);
                  setOpen(false);
                }
              }}
            >
              清空
            </Button>
            {multiple && (
              <div className="ui-filter-actions">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setOpen(false)}
                >
                  取消
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    onChange([...draft]);
                    setOpen(false);
                  }}
                >
                  应用
                </Button>
              </div>
            )}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
