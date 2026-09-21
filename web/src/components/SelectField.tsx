import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type SelectHTMLAttributes,
} from "react";
import { FilterPicker, type FilterOption } from "./patterns/FilterPicker";
import "./select-field.css";

/** Keep the actual native select as the form/event authority; share only its UI. */
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
  const uid = useId();
  const [options, setOptions] = useState<FilterOption[]>([]);
  const [current, setCurrent] = useState<string[]>([]);
  const [label, setLabel] = useState("");
  const [invalid, setInvalid] = useState(false);
  const read = () => {
    if (!native.current) return;
    setCurrent(
      Array.from(native.current.selectedOptions).map((option) => option.value),
    );
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
    const reset = () =>
      queueMicrotask(() => {
        read();
        setInvalid(false);
      });
    form?.addEventListener("reset", reset);
    return () => form?.removeEventListener("reset", reset);
  }, []);
  const accessibleLabel =
    props["aria-label"] || label || props.title || "选择选项";
  const choose = (values: string[]) => {
    const element = native.current;
    if (!element || props.disabled) return;
    if (props.multiple)
      Array.from(element.options).forEach((option) => {
        option.selected = values.includes(option.value);
      });
    else element.value = values[0] ?? "";
    // Dispatch through the real native element, never construct a pretend React
    // ChangeEvent. Controlled owners and FormData observe the same form value.
    element.dispatchEvent(new Event("change", { bubbles: true }));
    setCurrent(
      Array.from(element.selectedOptions).map((option) => option.value),
    );
    setInvalid(false);
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
          setCurrent(
            Array.from(event.target.selectedOptions).map(
              (option) => option.value,
            ),
          );
          setInvalid(false);
          onChange?.(event);
        }}
        onInvalid={(event) => {
          event.preventDefault();
          setInvalid(true);
          trigger.current?.focus();
          onInvalid?.(event);
        }}
        onFocus={(event) => {
          trigger.current?.focus();
          props.onFocus?.(event);
        }}
      >
        {children}
      </select>
      <FilterPicker
        id={`${uid}-trigger`}
        label={accessibleLabel}
        independent
        options={options}
        value={current}
        onChange={choose}
        multiple={props.multiple}
        searchable={options.length > 7}
        disabled={props.disabled}
        clearable={false}
        emptyLabel="请选择"
        className="select-field-trigger"
        triggerRef={trigger}
        width={Math.min(
          420,
          Math.max(
            options.length > 7 ? 248 : 192,
            trigger.current?.clientWidth ?? 0,
          ),
        )}
        triggerProps={{
          "aria-labelledby": props["aria-labelledby"],
          "aria-describedby":
            [props["aria-describedby"], invalid ? `${uid}-error` : ""]
              .filter(Boolean)
              .join(" ") || undefined,
          "aria-invalid": invalid || props["aria-invalid"],
          tabIndex: props.tabIndex,
        }}
      />
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
