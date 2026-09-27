import { useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { Card, Delta, Icon, Muted, Skeleton, cx, toneColor, type Tone } from "./core";
import { Lines, Sparkline } from "../charts";
import { useI18n } from "../i18n";

export type Column<T> = {
  key: string;
  header: ReactNode;
  width?: string | number;
  render: (row: T) => ReactNode;
  sort?: (a: T, b: T) => number;
  align?: "left" | "right" | "center";
};

/** Data table with sorting, row selection (checkboxes), hover actions and loading skeletons. */
export function DataTable<T>({
  rows,
  columns,
  rowKey,
  onRowClick,
  selected,
  selectable,
  checked,
  onCheck,
  rowHeight = 44,
  headBg,
  loading,
  empty,
  rowClass,
}: {
  rows: T[] | undefined;
  columns: Column<T>[];
  rowKey: (row: T) => string;
  onRowClick?: (row: T) => void;
  selected?: string | null;
  selectable?: boolean;
  checked?: Set<string>;
  onCheck?: (next: Set<string>) => void;
  rowHeight?: number;
  headBg?: boolean;
  loading?: boolean;
  empty?: ReactNode;
  rowClass?: (row: T) => string | undefined;
}) {
  const { tx } = useI18n();
  const [sort, setSort] = useState<{ key: string; dir: 1 | -1 } | null>(null);
  const sorted = useMemo(() => {
    if (!rows || !sort) return rows ?? [];
    const col = columns.find((c) => c.key === sort.key);
    if (!col?.sort) return rows;
    return [...rows].sort((a, b) => col.sort!(a, b) * sort.dir);
  }, [rows, sort, columns]);
  const allKeys = sorted.map(rowKey);
  const allOn = selectable && allKeys.length > 0 && allKeys.every((k) => checked?.has(k));
  const toggle = (k: string) => {
    const next = new Set(checked);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    onCheck?.(next);
  };
  return (
    <div className="dt-scroll">
    <table className={cx("v3-table", headBg && "head-bg")}>
      <colgroup>
        {selectable && <col style={{ width: 36 }} />}
        {columns.map((c) => (
          <col key={c.key} style={{ width: c.width }} />
        ))}
      </colgroup>
      <thead>
        <tr>
          {selectable && (
            <th>
              <input type="checkbox" aria-label={tx("全选", "Select all")} checked={!!allOn} onChange={() => onCheck?.(allOn ? new Set() : new Set(allKeys))} style={{ accentColor: "var(--accent)" }} />
            </th>
          )}
          {columns.map((c) => (
            <th
              key={c.key}
              className={c.sort ? "sortable" : undefined}
              style={{ textAlign: c.align }}
              onClick={c.sort ? () => setSort((s) => (s?.key === c.key ? (s.dir === 1 ? { key: c.key, dir: -1 } : null) : { key: c.key, dir: 1 })) : undefined}
              aria-sort={sort?.key === c.key ? (sort.dir === 1 ? "ascending" : "descending") : undefined}
            >
              <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                {c.header}
                {sort?.key === c.key && <Icon name={sort.dir === 1 ? "up" : "down"} size={11} />}
              </span>
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {loading &&
          Array.from({ length: 5 }, (_, i) => (
            <tr key={"s" + i} style={{ height: rowHeight }}>
              {selectable && <td />}
              {columns.map((c) => (
                <td key={c.key}>
                  <Skeleton w={`${50 + ((i * 17 + c.key.length * 7) % 40)}%`} h={10} />
                </td>
              ))}
            </tr>
          ))}
        {!loading &&
          sorted.map((r) => {
            const k = rowKey(r);
            return (
              <tr
                key={k}
                className={cx("hrow", onRowClick && "clickable", selected === k && "selected", checked?.has(k) && "selected", rowClass?.(r))}
                style={{ height: rowHeight }}
                onClick={onRowClick ? () => onRowClick(r) : undefined}
              >
                {selectable && (
                  <td onClick={(e) => e.stopPropagation()}>
                    <input type="checkbox" aria-label={tx("选择", "Select")} checked={!!checked?.has(k)} onChange={() => toggle(k)} style={{ accentColor: "var(--accent)" }} />
                  </td>
                )}
                {columns.map((c) => (
                  <td key={c.key} style={{ textAlign: c.align }}>
                    {c.render(r)}
                  </td>
                ))}
              </tr>
            );
          })}
        {!loading && sorted.length === 0 && (
          <tr>
            <td colSpan={columns.length + (selectable ? 1 : 0)} style={{ padding: 0 }}>
              {empty ?? <div style={{ padding: 28, textAlign: "center" }}><Muted>{tx("没有数据", "No data")}</Muted></div>}
            </td>
          </tr>
        )}
      </tbody>
    </table>
    </div>
  );
}

/** Compact KPI card with value, delta and sparkline. */
export function Kpi({ label, value, unit, sub, tone, values, delta, deltaGood = true, small }: { label: ReactNode; value: ReactNode; unit?: string; sub?: ReactNode; tone?: Tone; values?: number[]; delta?: string; deltaGood?: boolean; small?: boolean }) {
  const color = tone ? toneColor(tone) : "var(--text)";
  return (
    <Card className="v3-kpi anim-rise">
      <div className="v3-row" style={{ gap: 6 }}>
        <Muted>{label}</Muted>
        <span style={{ flexGrow: 1 }} />
        {delta && <Delta value={delta} good={deltaGood} />}
      </div>
      <div className="v3-row" style={{ gap: 8, alignItems: "flex-end" }}>
        <b className={cx("value", small && "sm")} style={{ color }}>
          {value}
          {unit && <span className="unit">{unit}</span>}
        </b>
        <span style={{ flexGrow: 1 }} />
        {values && !small && <Sparkline values={values} color={tone ? toneColor(tone) : "var(--accent)"} width={72} height={24} />}
      </div>
      {sub && <Muted size={11.5}>{sub}</Muted>}
    </Card>
  );
}

/** Large KPI band card: label + delta, big value, full-width area sparkline. */
export function KBand({ label, value, unit, sub, tone, values, delta, deltaGood = true, style }: { label: ReactNode; value: ReactNode; unit?: string; sub?: ReactNode; tone?: Tone; values: number[]; delta?: string; deltaGood?: boolean; style?: CSSProperties }) {
  const color = tone ? toneColor(tone) : "var(--text)";
  const line = tone ? toneColor(tone) : "var(--accent)";
  const fill = tone === "danger" ? "var(--danger-soft)" : tone === "warn" ? "var(--warn-soft)" : tone === "ok" ? "var(--ok-soft)" : "var(--accent-soft)";
  return (
    <Card className="v3-kpi anim-rise" style={style}>
      <div className="v3-row" style={{ gap: 6 }}>
        <Muted>{label}</Muted>
        <span style={{ flexGrow: 1 }} />
        {delta && <Delta value={delta} good={deltaGood} />}
      </div>
      <b className="value" style={{ color }}>
        {value}
        {unit && <span className="unit">{unit}</span>}
      </b>
      {sub && <Muted size={11}>{sub}</Muted>}
      <div style={{ marginTop: 6 }}>
        <Lines series={[{ values, color: line, fill }]} height={30} grid={1} hover={false} />
      </div>
    </Card>
  );
}

export function KpiRow({ children }: { children: ReactNode }) {
  return <div className="v3-row stagger" style={{ gap: 12, alignItems: "stretch" }}>{children}</div>;
}
