import { useId, useState } from "react";
import * as SelectPrimitive from "@radix-ui/react-select";
import { Check, ChevronDown, ChevronUp } from "../../lib/icons";
import { cn } from "../../lib/utils";
import "./system.css";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface SelectProps {
  options: SelectOption[];
  value?: string;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  name?: string;
  required?: boolean;
  disabled?: boolean;
  placeholder?: string;
  "aria-label"?: string;
  id?: string;
  className?: string;
}

export function Select({
  options,
  value,
  defaultValue = "",
  onValueChange,
  name,
  required,
  disabled,
  placeholder = "请选择",
  "aria-label": ariaLabel,
  id,
  className,
}: SelectProps) {
  const uniqueId = useId();
  const [internalValue, setInternalValue] = useState(defaultValue);
  const selected = value ?? internalValue;
  // Radix reserves the empty string for the placeholder. A private menu value
  // lets keyboard users clear a selection while the form value remains empty.
  let clearValue = `__ui_clear_${uniqueId}`;
  while (options.some((option) => option.value === clearValue))
    clearValue += "_";
  const emptyOption = options.find((option) => option.value === "");
  return (
    <SelectPrimitive.Root
      value={selected}
      onValueChange={(next) => {
        const normalized = next === clearValue ? "" : next;
        if (value === undefined) setInternalValue(normalized);
        onValueChange?.(normalized);
      }}
      name={name}
      required={required}
      disabled={disabled}
    >
      <SelectPrimitive.Trigger
        id={id}
        aria-label={ariaLabel}
        className={cn("ui-select-trigger", className)}
      >
        <SelectPrimitive.Value placeholder={placeholder} />
        <SelectPrimitive.Icon asChild>
          <ChevronDown size={15} />
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content
          className="ui-select-content"
          position="popper"
          sideOffset={5}
          collisionPadding={12}
        >
          <SelectPrimitive.ScrollUpButton className="ui-select-scroll">
            <ChevronUp size={15} />
          </SelectPrimitive.ScrollUpButton>
          <SelectPrimitive.Viewport className="ui-select-viewport">
            {(!required || emptyOption) && (
              <SelectPrimitive.Item
                className="ui-select-item ui-select-clear"
                value={clearValue}
                disabled={emptyOption?.disabled}
              >
                <SelectPrimitive.ItemText>
                  {emptyOption?.label ?? "清空选择"}
                </SelectPrimitive.ItemText>
              </SelectPrimitive.Item>
            )}
            {options
              .filter((option) => option.value !== "")
              .map((option) => (
                <SelectPrimitive.Item
                  className="ui-select-item"
                  key={option.value}
                  value={option.value}
                  disabled={option.disabled}
                >
                  <SelectPrimitive.ItemText>
                    {option.label}
                  </SelectPrimitive.ItemText>
                  <SelectPrimitive.ItemIndicator className="ui-select-check">
                    <Check size={15} />
                  </SelectPrimitive.ItemIndicator>
                </SelectPrimitive.Item>
              ))}
          </SelectPrimitive.Viewport>
          <SelectPrimitive.ScrollDownButton className="ui-select-scroll">
            <ChevronDown size={15} />
          </SelectPrimitive.ScrollDownButton>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
}
