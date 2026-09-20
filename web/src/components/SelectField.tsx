import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type SelectHTMLAttributes,
  type KeyboardEvent,
} from "react";
import * as Popover from "@radix-ui/react-popover";
import { Check, ChevronDown, Search } from "../lib/icons";
import "./select-field.css";

type Option = {
  value: string;
  label: string;
  disabled: boolean;
  group: string;
};
/** Native form semantics with the same visible menu as search filters. */
export default function SelectField(
  props: SelectHTMLAttributes<HTMLSelectElement>,
) {
  const {
    children,
    className = "",
    id,
    style,
    onChange,
    onInvalid,
    ...nativeProps
  } = props;
  const native = useRef<HTMLSelectElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const uid = useId();
  const [options, setOptions] = useState<Option[]>([]);
  const [current, setCurrent] = useState(
    String(props.value ?? props.defaultValue ?? ""),
  );
  const [label, setLabel] = useState("");
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [invalid, setInvalid] = useState(false);
  const read = () => {
    if (!native.current) return;
    setCurrent(native.current.value);
    setOptions(
      Array.from(native.current.options).map((option) => ({
        value: option.value,
        label: option.label,
        disabled:
          option.disabled ||
          (option.parentElement instanceof HTMLOptGroupElement &&
            option.parentElement.disabled),
        group:
          option.parentElement instanceof HTMLOptGroupElement
            ? option.parentElement.label
            : "",
      })),
    );
    const labels = Array.from(native.current.labels ?? [])
      .map((element) => {
        const copy = element.cloneNode(true) as HTMLElement;
        copy
          .querySelectorAll("select, button, .select-field-validation")
          .forEach((child) => child.remove());
        return copy.textContent?.trim() ?? "";
      })
      .filter(Boolean);
    setLabel(labels.join(" "));
  };
  useLayoutEffect(read, [children, props.value, props.defaultValue]);
  useEffect(() => {
    const form = native.current?.form;
    const reset = () => {
      queueMicrotask(() => {
        read();
        setInvalid(false);
      });
    };
    form?.addEventListener("reset", reset);
    return () => form?.removeEventListener("reset", reset);
  }, []);
  const selected = options.find((option) => option.value === current);
  const visible = options.filter((option) =>
    `${option.label} ${option.group}`
      .toLocaleLowerCase()
      .includes(query.toLocaleLowerCase()),
  );
  const accessibleLabel =
    props["aria-label"] || label || props.title || "选择选项";
  const choose = (option: Option) => {
    if (!native.current || option.disabled || props.disabled) return;
    native.current.value = option.value;
    native.current.dispatchEvent(new Event("change", { bubbles: true }));
    // Controlled selects may restore the old value until their owner renders.
    setCurrent(native.current.value);
    setInvalid(false);
    setOpen(false);
  };
  const focus = (position: "first" | "last" | "selected") => {
    const items = Array.from(
      list.current?.querySelectorAll<HTMLElement>(
        '[role="option"]:not([aria-disabled="true"])',
      ) ?? [],
    );
    (position === "last"
      ? items.at(-1)
      : position === "selected"
        ? (items.find(
            (item) => item.getAttribute("aria-selected") === "true",
          ) ?? items[0])
        : items[0]
    )?.focus();
  };
  const navigate = (event: KeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(
      list.current?.querySelectorAll<HTMLElement>(
        '[role="option"]:not([aria-disabled="true"])',
      ) ?? [],
    );
    if (!items.length) return;
    const index = items.indexOf(document.activeElement as HTMLElement);
    let next = -1;
    if (event.key === "ArrowDown") next = (index + 1) % items.length;
    else if (event.key === "ArrowUp")
      next = (index - 1 + items.length) % items.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = items.length - 1;
    else if (event.key.length === 1 && event.key !== " ")
      next = items.findIndex(
        (item, i) =>
          i > index &&
          item.textContent
            ?.trim()
            .toLocaleLowerCase()
            .startsWith(event.key.toLocaleLowerCase()),
      );
    if (next >= 0) {
      event.preventDefault();
      items[next].focus();
    }
  };
  return (
    <span className={`select-field ${className}`} style={style}>
      <select
        {...nativeProps}
        id={id}
        ref={native}
        className="select-field-native"
        tabIndex={-1}
        aria-hidden="true"
        onChange={(event) => {
          setCurrent(event.target.value);
          setInvalid(false);
          onChange?.(event);
        }}
        onInvalid={(event) => {
          event.preventDefault();
          setInvalid(true);
          trigger.current?.focus();
          onInvalid?.(event);
        }}
        onFocus={() => trigger.current?.focus()}
      >
        {children}
      </select>
      <Popover.Root
        open={open}
        onOpenChange={(value) => {
          setOpen(value);
          if (value) setQuery("");
        }}
      >
        <Popover.Trigger asChild>
          <button
            ref={trigger}
            type="button"
            className="select-field-trigger"
            disabled={props.disabled}
            aria-label={accessibleLabel}
            aria-labelledby={props["aria-labelledby"]}
            aria-describedby={
              invalid ? `${uid}-error` : props["aria-describedby"]
            }
            aria-invalid={invalid || props["aria-invalid"]}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                event.preventDefault();
                setOpen(true);
              }
            }}
          >
            <span>{selected?.label ?? "请选择"}</span>
            <ChevronDown size={14} aria-hidden="true" />
          </button>
        </Popover.Trigger>
        {/* A native modal's top layer makes body portals inert and invisible. */}
        <Popover.Portal container={native.current?.closest("dialog") ?? undefined}>
          <Popover.Content
            className="select-field-popover"
            align="start"
            sideOffset={6}
            collisionPadding={12}
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              if (options.length > 7) search.current?.focus();
              else focus("selected");
            }}
          >
            {options.length > 7 && (
              <div className="select-field-search">
                <Search size={16} aria-hidden="true" />
                <input
                  ref={search}
                  type="search"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  aria-label={`搜索${accessibleLabel}`}
                  placeholder="搜索选项"
                  onKeyDown={(event) => {
                    if (event.key === "ArrowDown") {
                      event.preventDefault();
                      focus("first");
                    }
                  }}
                />
              </div>
            )}
            <div
              className="select-field-options"
              role="listbox"
              aria-label={accessibleLabel}
              ref={list}
              onKeyDown={navigate}
            >
              {visible.map((option, index) => (
                <div
                  key={`${option.value}-${index}`}
                  role="option"
                  aria-selected={option.value === current}
                  aria-disabled={option.disabled || undefined}
                  tabIndex={option.disabled ? -1 : 0}
                  className="select-field-option"
                  onClick={() => choose(option)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      choose(option);
                    }
                  }}
                >
                  <span>{option.value === current && <Check size={14} />}</span>
                  <span>
                    {option.label}
                    {option.group && <small>{option.group}</small>}
                  </span>
                </div>
              ))}
            </div>
            {!visible.length && (
              <p className="select-field-empty">没有匹配选项</p>
            )}
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      {invalid && (
        <span
          id={`${uid}-error`}
          className="select-field-validation"
          role="alert"
        >
          请选择{accessibleLabel === "选择选项" ? "一个选项" : accessibleLabel}
        </span>
      )}
    </span>
  );
}
