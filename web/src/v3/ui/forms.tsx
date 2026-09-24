import type { ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { Badge, Icon, IconBtn, Muted, Row, cx, type Tone } from "./core";
import { useI18n } from "../i18n";
import "./forms.css";

/** "AI drafted this" banner shown on top of create flows. */
export function AiBanner({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="f-ai">
      <Icon name="sparkles" size={14} />
      <span className="grow">{children}</span>
      {action}
    </div>
  );
}

/** Dashed frame around a value the AI suggested; the user can still edit it. */
export function Suggested({ children, on = true }: { children: ReactNode; on?: boolean }) {
  return <div className={cx("f-suggested", on && "on")}>{children}</div>;
}

export function PillSelect<T extends string>({ value, options, onChange, label }: { value: T; options: T[]; onChange: (v: T) => void; label: string }) {
  return (
    <div className="f-pills" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button key={o} type="button" role="radio" aria-checked={o === value} className={cx(o === value && "on", /^P[1-4]$/.test(o) && o.toLowerCase())} onClick={() => onChange(o)}>
          {o}
        </button>
      ))}
    </div>
  );
}

export type Matcher = { k: string; op: string; v: string };
export function MatcherList({ items, onChange, addLabel }: { items: Matcher[]; onChange: (m: Matcher[]) => void; addLabel?: string }) {
  const { tx } = useI18n();
  const set = (i: number, patch: Partial<Matcher>) => onChange(items.map((m, j) => (j === i ? { ...m, ...patch } : m)));
  return (
    <div className="v3-col" style={{ gap: 6 }}>
      {items.map((m, i) => (
        <div key={i} className="f-matcher">
          <input className="mono" value={m.k} placeholder="label" aria-label={tx("标签名", "Label")} onChange={(e) => set(i, { k: e.target.value })} />
          <select value={m.op} aria-label={tx("运算符", "Operator")} onChange={(e) => set(i, { op: e.target.value })}>
            {["=", "!=", "=~", "!~"].map((o) => (
              <option key={o}>{o}</option>
            ))}
          </select>
          <input className="mono grow" value={m.v} placeholder="value" aria-label={tx("值", "Value")} onChange={(e) => set(i, { v: e.target.value })} />
          <IconBtn icon="x" label={tx("删除条件", "Remove matcher")} size={24} onClick={() => onChange(items.filter((_, j) => j !== i))} />
        </div>
      ))}
      <button type="button" className="f-add" onClick={() => onChange([...items, { k: "", op: "=", v: "" }])}>
        <Icon name="plus" size={12} />
        {addLabel ?? tx("条件", "Matcher")}
      </button>
    </div>
  );
}

export function FormSection({ title, desc, right, children }: { title: ReactNode; desc?: ReactNode; right?: ReactNode; children: ReactNode }) {
  return (
    <section className="f-section">
      <Row gap={8}>
        <b className="grow">{title}</b>
        {right}
      </Row>
      {desc && <Muted size={11.5}>{desc}</Muted>}
      {children}
    </section>
  );
}

export function Callout({ tone = "neutral", icon = "info", children, action }: { tone?: Tone; icon?: Parameters<typeof Icon>[0]["name"]; children: ReactNode; action?: ReactNode }) {
  return (
    <div className={cx("f-callout", tone)}>
      <Icon name={icon} size={14} />
      <span className="grow">{children}</span>
      {action}
    </div>
  );
}

export function StepList({ steps, active, onPick }: { steps: string[]; active: number; onPick?: (i: number) => void }) {
  return (
    <ol className="f-steps">
      {steps.map((s, i) => (
        <li key={s} className={cx(i < active && "done", i === active && "on")}>
          <button type="button" onClick={() => onPick?.(i)} disabled={!onPick}>
            <span className="mk">{i < active ? <Icon name="check" size={11} /> : i + 1}</span>
            {s}
          </button>
        </li>
      ))}
    </ol>
  );
}

export const AiTag = () => {
  const { tx } = useI18n();
  return (
    <Badge tone="accent">
      <Icon name="sparkles" size={10} />
      {tx("AI 建议", "AI suggested")}
    </Badge>
  );
};

/** Create drawers are addressable: `?new=<kind>` opens them, so "新建" links from other pages land on the open form. */
export function useCreateParam(kind = "1"): [boolean, (open: boolean) => void] {
  const [params, setParams] = useSearchParams();
  const open = params.get("new") === kind;
  const set = (v: boolean) => {
    const next = new URLSearchParams(params);
    if (v) next.set("new", kind);
    else next.delete("new");
    setParams(next, { replace: true });
  };
  return [open, set];
}
