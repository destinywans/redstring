import { useEffect, useMemo, useState, type ReactNode } from "react";
import { create } from "zustand";
import { API_MODE, useGet } from "../api/client";
import type { MembersView, ServiceCatalog } from "../api/types";
import { Btn, Chip, Empty, Field, Icon, Input, Muted, Textarea, cx, type IconName } from "./core";
import { Dialog, Popover, toast } from "./overlays";
import { useI18n } from "../i18n";
import "./demo.css";

/*
 * Shared interaction primitives so every control on a page does something visible:
 * filter chips that actually select, external deep links, form dialogs and confirmations.
 * In mock mode nothing here pretends to reach a real system; results are labelled as sample data.
 */

/** Opens a deep link in an external tool. Mock mode has no real target, so it says so instead of opening a dead URL. */
export function openExternal(system: string, url: string) {
  if (API_MODE === "http") {
    window.open(url, "_blank", "noopener");
    return;
  }
  const zh = document.documentElement.lang.startsWith("zh");
  toast(zh ? `示意数据：接入真实连接器后会在新标签页打开 ${system}（${hostOf(url)}）` : `Sample data: with a live connector this opens ${system} (${hostOf(url)}) in a new tab`);
}
const hostOf = (url: string) => {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
};

/** Saves generated content as a file (exports, audit reports, templates). */
export function downloadFile(name: string, content: string, mime = "application/json") {
  const url = URL.createObjectURL(new Blob([content], { type: `${mime};charset=utf-8` }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/**
 * Content that exists only as frontend sample data (no contract endpoint yet). With a real backend it
 * shows a "not connected yet" state instead, so sample data never passes for real business state.
 */
export function SampleOnly({ children, what }: { children: ReactNode; what: ReactNode }) {
  const { tx } = useI18n();
  if (API_MODE === "http") return <Empty icon="plug" title={tx("待接入", "Not connected yet")} desc={tx("{w}还没有后端接口", "{w} has no backend endpoint yet", { w: what as string })} />;
  return <>{children}</>;
}

/** Copies text and confirms; falls back to a toast that shows the text when the clipboard is refused. */
export function copyText(text: string, done: string) {
  navigator.clipboard?.writeText(text).then(
    () => toast(done),
    () => toast(text),
  ) ?? toast(text);
}

/** A one-time secret or generated address with a copy button. */
export function SecretBox({ label, value }: { label: ReactNode; value: string }) {
  const { tx } = useI18n();
  return (
    <div className="v3-col" style={{ gap: 8 }}>
      <span>{label}</span>
      <div className="demo-secret">
        <code>{value}</code>
        <Btn h={26} icon="copy" onClick={() => copyText(value, tx("已复制", "Copied"))}>
          {tx("复制", "Copy")}
        </Btn>
      </div>
    </div>
  );
}

/** `disabled` lists an option that can't be chosen (its hint says why). */
export type ChipOption = { value: string; label: ReactNode; hint?: ReactNode; disabled?: boolean };

/**
 * Filter / scope chip with a dropdown. Uncontrolled by default; pass `value` + `onChange` to filter data.
 * `multi` toggles several values; the chip shows the selection inline.
 */
export function ChipSelect({
  label,
  options,
  value,
  onChange,
  defaultValue,
  multi,
  icon,
  allLabel,
  showLabel = true,
  ariaLabel,
  disabled,
  openNonce,
  footer,
}: {
  label: ReactNode;
  options: (ChipOption | string)[];
  value?: string | string[];
  onChange?: (v: string | string[]) => void;
  defaultValue?: string | string[];
  multi?: boolean;
  icon?: IconName;
  /** Adds an "all" entry that clears the selection. */
  allLabel?: ReactNode;
  /** When false, a selected value replaces the label instead of following it (e.g. time ranges). */
  showLabel?: boolean;
  /** Accessible name for the trigger when the visible text alone doesn't say what is being chosen. */
  ariaLabel?: string;
  /** The whole chip can't be opened. */
  disabled?: boolean;
  /** Opens the menu whenever this changes to a new non-zero value (e.g. a "choose a service" shortcut elsewhere). */
  openNonce?: number;
  /** A muted line under the options (e.g. that the list isn't complete). */
  footer?: ReactNode;
}) {
  const [own, setOwn] = useState<string | string[]>(defaultValue ?? (multi ? [] : ""));
  const cur = value ?? own;
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (openNonce && !disabled) setOpen(true);
  }, [openNonce, disabled]);
  const [anchor, setAnchor] = useState<HTMLSpanElement | null>(null);
  const opts = options.map((o) => (typeof o === "string" ? { value: o, label: o } : o));
  const set = (v: string | string[]) => {
    if (value === undefined) setOwn(v);
    onChange?.(v);
  };
  const selected = Array.isArray(cur) ? cur : cur ? [cur] : [];
  const shown = selected.map((v) => opts.find((o) => o.value === v)?.label ?? v);
  const pick = (v: string) => {
    if (!multi) {
      set(v);
      setOpen(false);
      return;
    }
    const next = selected.includes(v) ? selected.filter((x) => x !== v) : [...selected, v];
    set(next);
  };
  return (
    <span ref={setAnchor} className="demo-chip">
      <Chip icon={icon} caret active={selected.length > 0 && !!allLabel} onClick={() => setOpen((o) => !o)} ariaLabel={ariaLabel} expanded={open} disabled={disabled}>
        {shown.length === 0 ? (
          label
        ) : showLabel ? (
          <>
            {label}：<b className="demo-chip-val">{shown.length > 2 ? `${shown.length}` : shown.map((s, i) => <span key={i}>{i > 0 && "、"}{s}</span>)}</b>
          </>
        ) : (
          shown[0]
        )}
      </Chip>
      {/* Long lists scroll inside the menu instead of running off the screen (PA-85); usual lists keep their full height. */}
      <Popover anchor={anchor} open={open} onClose={() => setOpen(false)} style={{ minWidth: 200, padding: 4 }} fit={Math.max(420, Math.round(window.innerHeight * 0.6))}>
        <div className="v3-menu demo-menu" role="listbox" aria-multiselectable={multi || undefined}>
          {allLabel && (
            <button type="button" role="option" aria-selected={selected.length === 0} className={cx(selected.length === 0 && "on")} onClick={() => { set(multi ? [] : ""); setOpen(false); }}>
              <span className="grow">{allLabel}</span>
              {selected.length === 0 && <Icon name="check" size={13} color="var(--accent)" />}
            </button>
          )}
          {opts.map((o) => {
            const on = selected.includes(o.value);
            return (
              <button key={o.value} type="button" role="option" aria-selected={on} aria-disabled={o.disabled || undefined} disabled={o.disabled} className={cx(on && "on")} onClick={() => !o.disabled && pick(o.value)}>
                <span className="grow">
                  {o.label}
                  {o.hint && <small>{o.hint}</small>}
                </span>
                {on && <Icon name="check" size={13} color="var(--accent)" />}
              </button>
            );
          })}
        </div>
        {footer && <div className="demo-menu-foot">{footer}</div>}
      </Popover>
    </span>
  );
}

/**
 * Multi-select facet over a list: options come from the data itself, `test` filters rows.
 * Spread `props` onto a ChipSelect.
 */
export function useFacet<T>(items: T[] | undefined, get: (t: T) => string | string[] | undefined | null) {
  const [sel, setSel] = useState<string[]>([]);
  const options = useMemo(() => {
    const seen = new Set<string>();
    for (const it of items ?? []) {
      const v = get(it);
      for (const x of Array.isArray(v) ? v : v ? [v] : []) seen.add(x);
    }
    return [...seen].map((v) => ({ value: v, label: v }));
  }, [items]); // eslint-disable-line react-hooks/exhaustive-deps
  const test = (t: T) => {
    if (!sel.length) return true;
    const v = get(t);
    return (Array.isArray(v) ? v : v ? [v] : []).some((x) => sel.includes(x));
  };
  return { test, props: { options, value: sel, onChange: (v: string | string[]) => setSel(v as string[]), multi: true as const } };
}

/**
 * "+ Add" chip for form fields: pick from suggestions or type a value (`free`). Added values render
 * before the chip as removable tags. Uncontrolled unless `value` / `onChange` are passed.
 */
export function AddChip({
  label,
  options = [],
  free,
  placeholder,
  value,
  onChange,
}: {
  label: ReactNode;
  options?: (ChipOption | string)[];
  free?: boolean;
  placeholder?: string;
  value?: string[];
  onChange?: (v: string[]) => void;
}) {
  const { tx } = useI18n();
  const [own, setOwn] = useState<string[]>([]);
  const items = value ?? own;
  const set = (v: string[]) => {
    if (value === undefined) setOwn(v);
    onChange?.(v);
  };
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [anchor, setAnchor] = useState<HTMLSpanElement | null>(null);
  const opts = options.map((o) => (typeof o === "string" ? { value: o, label: o } : o)).filter((o) => !items.includes(o.value) && (!q || String(o.value).toLowerCase().includes(q.toLowerCase())));
  const add = (v: string) => {
    const t = v.trim();
    if (!t || items.includes(t)) return;
    set([...items, t]);
    setQ("");
    if (!free) setOpen(false);
  };
  return (
    <>
      {items.map((x) => (
        <span key={x} className="demo-added">
          <span className="mono">{x}</span>
          <button type="button" aria-label={tx("移除 {x}", "Remove {x}", { x })} onClick={() => set(items.filter((i) => i !== x))}>
            <Icon name="x" size={11} />
          </button>
        </span>
      ))}
      <span ref={setAnchor} className="demo-chip">
        <Chip icon="plus" onClick={() => setOpen((o) => !o)}>
          {label}
        </Chip>
      </span>
      <Popover anchor={anchor} open={open} onClose={() => setOpen(false)} style={{ width: 260, padding: 0 }}>
        <form
          className="demo-add-search"
          onSubmit={(e) => {
            e.preventDefault();
            if (free) add(q);
            else if (opts[0]) add(opts[0].value);
          }}
        >
          <Icon name={free ? "plus" : "search"} size={13} />
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={placeholder ?? (free ? tx("输入后回车添加", "Type and press Enter") : tx("搜索", "Search"))} aria-label={String(label)} />
        </form>
        {opts.length > 0 && (
          <div className="v3-menu demo-menu" style={{ maxHeight: 240, overflow: "auto" }}>
            {opts.map((o) => (
              <button key={o.value} type="button" onClick={() => add(o.value)}>
                <span className="grow">
                  {o.label}
                  {o.hint && <small>{o.hint}</small>}
                </span>
              </button>
            ))}
          </div>
        )}
      </Popover>
    </>
  );
}

/** Option lists for pickers: services from the catalog, people from the member directory. */
export function usePickLists() {
  const { data: svc } = useGet<ServiceCatalog>("/services", undefined, { staleTime: 5 * 60_000 });
  const { data: mem } = useGet<MembersView>("/members", { filter: "all" }, { staleTime: 5 * 60_000 });
  return {
    // Real service IDs are opaque; show the name and keep the ID as the value.
    services: (svc?.items ?? []).map((s) => ({ value: s.id, label: s.name, hint: s.tier })),
    people: (mem?.items ?? []).filter((m) => !m.agent && m.status === "active").map((m) => ({ value: m.name, label: m.name, hint: m.department })),
  };
}

/** Time range presets used by the range chips across observe / events / audit pages. */
export function useRanges() {
  const { tx } = useI18n();
  return [
    { value: "15m", label: tx("最近 15 分钟", "Last 15 minutes") },
    { value: "30m", label: tx("最近 30 分钟", "Last 30 minutes") },
    { value: "1h", label: tx("最近 1 小时", "Last 1 hour") },
    { value: "6h", label: tx("最近 6 小时", "Last 6 hours") },
    { value: "24h", label: tx("最近 24 小时", "Last 24 hours") },
    { value: "7d", label: tx("最近 7 天", "Last 7 days") },
    { value: "30d", label: tx("最近 30 天", "Last 30 days") },
  ];
}
export function RangeChip({ value, onChange, defaultValue = "24h" }: { value?: string; onChange?: (v: string) => void; defaultValue?: string }) {
  const ranges = useRanges();
  return <ChipSelect icon="clock" label="" showLabel={false} options={ranges} value={value} defaultValue={defaultValue} onChange={(v) => onChange?.(v as string)} />;
}

/* ---------- form dialog ---------- */
export type FormField =
  | { key: string; label: ReactNode; type?: "text" | "textarea"; placeholder?: string; required?: boolean; initial?: string; hint?: ReactNode; mono?: boolean }
  | { key: string; label: ReactNode; type: "select"; options: ChipOption[]; required?: boolean; initial?: string; hint?: ReactNode };
type FormRequest = {
  title: ReactNode;
  desc?: ReactNode;
  fields: FormField[];
  submit: ReactNode;
  danger?: boolean;
  resolve: (v: Record<string, string> | null) => void;
};
type ConfirmRequest = { title: ReactNode; desc?: ReactNode; confirm: ReactNode; danger?: boolean; resolve: (ok: boolean) => void };
const useDemoHost = create<{ form: FormRequest | null; confirm: ConfirmRequest | null }>(() => ({ form: null, confirm: null }));

/** Opens a small form; resolves with the values, or null when cancelled. */
export function openForm(req: Omit<FormRequest, "resolve">): Promise<Record<string, string> | null> {
  return new Promise((resolve) => useDemoHost.setState({ form: { ...req, resolve } }));
}
/** Asks for confirmation before a disruptive action; resolves true when confirmed. */
export function confirmAction(req: Omit<ConfirmRequest, "resolve">): Promise<boolean> {
  return new Promise((resolve) => useDemoHost.setState({ confirm: { ...req, resolve } }));
}

/** Mount once (Shell): renders the dialogs requested through `openForm` / `confirmAction`. */
export function DemoHost() {
  const { form, confirm } = useDemoHost();
  return (
    <>
      {form && <FormDialog key={String(Date.now())} req={form} />}
      {confirm && <ConfirmDialog req={confirm} />}
    </>
  );
}

function FormDialog({ req }: { req: FormRequest }) {
  const { tx } = useI18n();
  const [vals, setVals] = useState<Record<string, string>>(() => Object.fromEntries(req.fields.map((f) => [f.key, f.initial ?? (f.type === "select" ? f.options[0]?.value ?? "" : "")])));
  const missing = req.fields.filter((f) => f.required && !vals[f.key]?.trim());
  const close = (v: Record<string, string> | null) => {
    useDemoHost.setState({ form: null });
    req.resolve(v);
  };
  return (
    <Dialog
      open
      onClose={() => close(null)}
      title={req.title}
      footer={
        <>
          <Btn onClick={() => close(null)}>{tx("取消", "Cancel")}</Btn>
          <Btn kind={req.danger ? "danger" : "primary"} disabled={missing.length > 0} onClick={() => close(vals)}>
            {req.submit}
          </Btn>
        </>
      }
    >
      <form
        className="v3-col"
        style={{ gap: 14 }}
        onSubmit={(e) => {
          e.preventDefault();
          if (!missing.length) close(vals);
        }}
      >
        {req.desc && <Muted size={13}>{req.desc}</Muted>}
        {req.fields.map((f, i) => (
          <Field key={f.key} label={<>{f.label}{f.required && <span className="demo-req"> *</span>}</>} hint={f.hint}>
            {f.type === "select" ? (
              <div className="demo-options" role="radiogroup">
                {f.options.map((o) => (
                  <button key={o.value} type="button" role="radio" aria-checked={vals[f.key] === o.value} className={cx("demo-option", vals[f.key] === o.value && "on")} onClick={() => setVals((v) => ({ ...v, [f.key]: o.value }))}>
                    <b>{o.label}</b>
                    {o.hint && <small>{o.hint}</small>}
                  </button>
                ))}
              </div>
            ) : f.type === "textarea" ? (
              <Textarea rows={4} value={vals[f.key]} placeholder={f.placeholder} onChange={(e) => setVals((v) => ({ ...v, [f.key]: e.target.value }))} autoFocus={i === 0} />
            ) : (
              <Input value={vals[f.key]} mono={f.mono} placeholder={f.placeholder} onChange={(e) => setVals((v) => ({ ...v, [f.key]: e.target.value }))} autoFocus={i === 0} />
            )}
          </Field>
        ))}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function ConfirmDialog({ req }: { req: ConfirmRequest }) {
  const { tx } = useI18n();
  const close = (ok: boolean) => {
    useDemoHost.setState({ confirm: null });
    req.resolve(ok);
  };
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === "Enter" && close(true);
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Dialog
      open
      width={440}
      onClose={() => close(false)}
      title={req.title}
      footer={
        <>
          <Btn onClick={() => close(false)}>{tx("取消", "Cancel")}</Btn>
          <Btn kind={req.danger ? "danger" : "primary"} onClick={() => close(true)}>
            {req.confirm}
          </Btn>
        </>
      }
    >
      {req.desc && <div style={{ fontSize: "var(--fs-sm)", lineHeight: 1.65, color: "var(--muted)" }}>{req.desc}</div>}
    </Dialog>
  );
}
