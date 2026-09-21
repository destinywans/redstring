import {
  createContext,
  useContext,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type Ref,
} from "react";
import * as Popover from "@radix-ui/react-popover";
import { Command } from "cmdk";
import { Check, ChevronDown, Search } from "../../lib/icons";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import "../ui/system.css";
import "./filter-picker.css";

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
  description?: string;
  group?: string;
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
  /** Text inside the optional option search. */
  placeholder?: string;
  /** Empty trigger copy; omitted for ordinary filters so the field remains identifiable. */
  emptyLabel?: string;
  width?: number;
  clearable?: boolean;
  toggleSingle?: boolean;
  disabled?: boolean;
  /** Form adapters share the menu without inheriting a toolbar's open state. */
  independent?: boolean;
  className?: string;
  triggerRef?: Ref<HTMLButtonElement>;
  triggerProps?: Pick<
    ButtonHTMLAttributes<HTMLButtonElement>,
    | "aria-invalid"
    | "aria-describedby"
    | "aria-labelledby"
    | "onFocus"
    | "onBlur"
    | "tabIndex"
  >;
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
  emptyLabel,
  width,
  clearable = true,
  toggleSingle = false,
  disabled,
  independent = false,
  className,
  triggerRef,
  triggerProps,
}: FilterPickerProps) {
  const groupContext = useContext(FilterContext);
  const group = independent ? null : groupContext;
  const uid = useId();
  const openKey = `${id}-${uid}`;
  const [localOpen, setLocalOpen] = useState(false);
  const [draft, setDraft] = useState<string[]>(value);
  const [query, setQuery] = useState("");
  const commandRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const open = group ? group.activeId === openKey : localOpen;
  const selected = multiple ? draft : value;
  const firstLabel =
    options.find((option) => option.value === value[0])?.label ?? value[0];
  const menuWidth = width ?? (searchable || multiple ? 248 : 192);
  function setOpen(next: boolean) {
    if (next) {
      setDraft([...value]);
      setQuery("");
    }
    if (group) group.setActive(openKey, next);
    else setLocalOpen(next);
  }
  function pick(option: FilterOption) {
    if (option.disabled || disabled) return;
    if (multiple)
      setDraft((current) =>
        current.includes(option.value)
          ? current.filter((item) => item !== option.value)
          : [...current, option.value],
      );
    else {
      onChange(
        toggleSingle && value.includes(option.value) ? [] : [option.value],
      );
      setOpen(false);
    }
  }
  return (
    <Popover.Root open={open && !disabled} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <Button
          id={id}
          ref={(node) => {
            buttonRef.current = node;
            if (typeof triggerRef === "function") triggerRef(node);
            else if (triggerRef) triggerRef.current = node;
          }}
          variant="outline"
          className={cn(
            "ui-filter-trigger",
            value.length > 0 && "ui-filter-active",
            className,
          )}
          aria-label={label}
          title={
            value.length
              ? `${label} · ${firstLabel}${value.length > 1 ? ` 等 ${value.length} 项` : ""}`
              : undefined
          }
          disabled={disabled}
          {...triggerProps}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              setOpen(true);
            }
          }}
        >
          <span className="ui-filter-trigger-text">
            {value.length ? firstLabel : (emptyLabel ?? label)}
          </span>
          {value.length > 1 && (
            <span className="ui-filter-count">+{value.length - 1}</span>
          )}
          <ChevronDown size={14} aria-hidden="true" />
        </Button>
      </Popover.Trigger>
      {/* Native modal dialogs make body portals inert; keep the menu in their top layer. */}
      <Popover.Portal
        container={buttonRef.current?.closest("dialog") ?? undefined}
      >
        <Popover.Content
          className="ui-filter-content"
          style={{ width: menuWidth }}
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
          onCloseAutoFocus={(event) => {
            if (group?.activeId && group.activeId !== openKey)
              event.preventDefault();
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
              {options.map((option, index) => (
                <Command.Item
                  key={`${option.value}-${index}`}
                  value={`${uid}-option-${index}`}
                  keywords={[
                    option.value,
                    option.label,
                    option.description ?? "",
                    option.group ?? "",
                  ]}
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
                  <span className="ui-filter-option-label">
                    {option.label}
                    {(option.description || option.group) && (
                      <small className="ui-filter-option-description">
                        {[option.group, option.description]
                          .filter(Boolean)
                          .join(" · ")}
                      </small>
                    )}
                  </span>
                  {!multiple && selected.includes(option.value) && (
                    <Check size={15} aria-hidden="true" />
                  )}
                </Command.Item>
              ))}
            </Command.List>
          </Command>
          {(clearable || multiple) && (
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
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
