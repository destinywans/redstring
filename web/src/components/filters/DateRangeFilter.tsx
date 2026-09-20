import { useId } from "react";
import "./date-range.css";

type DateRange = { from: string; to: string };

/** Local wall-clock values; the caller controls when to apply and serialize. */
export default function DateRangeFilter({
  from,
  to,
  onChange,
  disabled = false,
}: DateRange & {
  onChange: (range: DateRange) => void;
  disabled?: boolean;
}) {
  const id = useId();
  return (
    <div className="date-range-filter" role="group" aria-label="时间范围">
      <label htmlFor={`${id}-from`}>
        <span>开始</span>
        <input
          id={`${id}-from`}
          type="datetime-local"
          value={from}
          onChange={(event) => onChange({ from: event.target.value, to })}
          disabled={disabled}
        />
      </label>
      <span aria-hidden="true" className="date-range-divider">
        —
      </span>
      <label htmlFor={`${id}-to`}>
        <span>结束</span>
        <input
          id={`${id}-to`}
          type="datetime-local"
          value={to}
          onChange={(event) => onChange({ from, to: event.target.value })}
          disabled={disabled}
        />
      </label>
    </div>
  );
}
