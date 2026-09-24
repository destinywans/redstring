import {
  forwardRef,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type HTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
} from "react";
import { Link } from "react-router-dom";
import { ICONS, type IconName } from "./iconPaths";
import { useI18n } from "../i18n";

export type Tone =
  | "neutral"
  | "accent"
  | "ok"
  | "warn"
  | "danger"
  | "info"
  | "p2"
  | "p4"
  | "prod"
  | "stg"
  | "dev"
  | "t0"
  | "t1";

export const toneColor = (t: Tone) =>
  t === "neutral" ? "var(--faint)" : t === "accent" ? "var(--accent)" : `var(--${t})`;

const cx = (...c: (string | false | null | undefined)[]) => c.filter(Boolean).join(" ");
export { cx };

/* ---------- layout ---------- */
type StackProps = HTMLAttributes<HTMLDivElement> & { gap?: number; align?: CSSProperties["alignItems"]; wrap?: boolean; grow?: boolean };
export function Row({ gap = 8, align = "center", wrap, grow, style, className, ...rest }: StackProps) {
  return <div className={cx("v3-row", grow && "grow", className)} style={{ gap, alignItems: align, flexWrap: wrap ? "wrap" : undefined, ...style }} {...rest} />;
}
export function Col({ gap = 8, align, grow, style, className, ...rest }: StackProps) {
  return <div className={cx("v3-col", grow && "grow", className)} style={{ gap, alignItems: align, ...style }} {...rest} />;
}
export const Spacer = () => <span style={{ flexGrow: 1 }} />;
export function Grid({ cols, gap = 12, style, ...rest }: HTMLAttributes<HTMLDivElement> & { cols: number | string; gap?: number }) {
  return (
    <div
      style={{ display: "grid", gridTemplateColumns: typeof cols === "number" ? `repeat(${cols}, minmax(0, 1fr))` : cols, gap, ...style }}
      {...rest}
    />
  );
}

/* ---------- text ---------- */
export const Muted = ({ children, size, style }: { children: ReactNode; size?: number; style?: CSSProperties }) => (
  <span className="muted" style={{ fontSize: size ?? 12, ...style }}>
    {children}
  </span>
);
export const Strong = ({ children, size = 12.5, style }: { children: ReactNode; size?: number; style?: CSSProperties }) => (
  <b style={{ fontSize: size, fontWeight: 600, ...style }}>{children}</b>
);
export const Mono = ({ children, color, size = 12, style }: { children: ReactNode; color?: string; size?: number; style?: CSSProperties }) => (
  <span className="mono" style={{ fontSize: size, color: color ?? "var(--muted)", ...style }}>
    {children}
  </span>
);
export const Num = ({ children, color, size, style }: { children: ReactNode; color?: string; size?: number; style?: CSSProperties }) => (
  <span className="tnum" style={{ color: color ?? "var(--text)", fontSize: size, fontWeight: 500, ...style }}>
    {children}
  </span>
);
export const Ellipsis = ({ children, style, title }: { children: ReactNode; style?: CSSProperties; title?: string }) => (
  <span className="ellipsis" style={style} title={title}>
    {children}
  </span>
);

/* ---------- icons ---------- */
export function Icon({ name, size = 16, color, style, className }: { name: IconName; size?: number; color?: string; style?: CSSProperties; className?: string }) {
  return (
    <svg
      className={cx("v3-i", className)}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      aria-hidden="true"
      style={{ color: color ?? "currentColor", ...style }}
      dangerouslySetInnerHTML={{ __html: ICONS[name] }}
    />
  );
}
export type { IconName };

const brandUrls = import.meta.glob("../assets/brands/*.svg", { eager: true, query: "?url", import: "default" }) as Record<string, string>;
const BRANDS: Record<string, string> = Object.fromEntries(
  Object.entries(brandUrls).map(([p, url]) => [p.split("/").pop()!.replace(".svg", ""), url]),
);
/** Vendors without a redistributable official mark render as a dashed monogram placeholder. */
const PLACEHOLDER: Record<string, string> = { feishu: "飞书", wecom: "企微", phoenix: "Px", tempo: "Tp", loki: "Lk", apisix: "AX", unleash: "Un", sentinel: "St", sonarqube: "Sq" };

export function Brand({ name, size = 28, title }: { name: string; size?: number; title?: string }) {
  if (!BRANDS[name]) {
    return (
      <span className="v3-brand placeholder" title={title ?? name} style={{ width: size, height: size, fontSize: Math.max(9, size * 0.34) }}>
        {PLACEHOLDER[name] ?? name.slice(0, 2)}
      </span>
    );
  }
  return (
    <span className="v3-brand" title={title ?? name} style={{ width: size, height: size }}>
      <img src={BRANDS[name]} alt={title ?? name} style={{ width: size * 0.62, height: size * 0.62 }} />
    </span>
  );
}

/* ---------- badges ---------- */
export function Badge({ children, tone = "neutral", mono, style, title }: { children: ReactNode; tone?: Tone; mono?: boolean; style?: CSSProperties; title?: string }) {
  return (
    <span className={cx("v3-badge", `t-${tone}`, mono && "mono")} style={style} title={title}>
      {children}
    </span>
  );
}
export const Dot = ({ tone = "neutral", size = 8, pulse }: { tone?: Tone; size?: number; pulse?: boolean }) => (
  <span className={cx("v3-dot", pulse && "pulse")} style={{ width: size, height: size, background: toneColor(tone) }} />
);
export function Tile({ icon, tone = "neutral", size = 28 }: { icon: IconName; tone?: Tone; size?: number }) {
  return (
    <span className={cx("v3-tile", `t-${tone}`)} style={{ width: size, height: size }} aria-hidden="true">
      <Icon name={icon} size={Math.min(15, size * 0.55)} />
    </span>
  );
}
export const Kbd = ({ children }: { children: ReactNode }) => <kbd className="v3-kbd">{children}</kbd>;

export type Severity = "P1" | "P2" | "P3" | "P4";
const SEV: Record<Severity, Tone> = { P1: "danger", P2: "p2", P3: "warn", P4: "p4" };
export const Sev = ({ p }: { p: Severity }) => (
  <Badge tone={SEV[p]} mono>
    {p}
  </Badge>
);
export type EnvName = "prod" | "staging" | "test";
export function Env({ env }: { env: EnvName }) {
  const { tx } = useI18n();
  const map = { prod: ["prod", tx("生产", "Prod")], staging: ["stg", tx("预发", "Staging")], test: ["dev", tx("测试", "Test")] } as const;
  const [tone, label] = map[env];
  return <Badge tone={tone as Tone}>{label}</Badge>;
}
export const Tier = ({ tier }: { tier: string }) => (
  <Badge tone={tier === "T0" ? "t0" : tier === "T1" ? "t1" : "neutral"} mono>
    {tier}
  </Badge>
);
export function State({ tone, children, pulse }: { tone: Tone; children: ReactNode; pulse?: boolean }) {
  return (
    <Badge tone={tone}>
      <Dot tone={tone} size={6} pulse={pulse} />
      {children}
    </Badge>
  );
}
export function Delta({ value, good }: { value: string; good: boolean }) {
  const up = value.startsWith("+");
  const down = value.startsWith("-") || value.startsWith("−");
  return (
    <span className="v3-delta tnum" style={{ color: good ? "var(--ok)" : "var(--danger)" }}>
      {up && <span className="arrow">▲</span>}
      {down && <span className="arrow">▼</span>}
      {value.replace(/^[+\-−]/, "")}
    </span>
  );
}
export function LabelChip({ k, v }: { k: string; v: string }) {
  return (
    <span className="v3-labelchip mono">
      <span className="k">{k}</span>
      <span className="v">{v}</span>
    </span>
  );
}

/* ---------- avatars ---------- */
const AVATAR = ["#5B5BD6", "#0F7B80", "#B42347", "#A34A1A", "#0B6BCB", "#6E3FC9", "#1E7A4C", "#6B5A00"];
export function Avatar({ name, size = 24, accent, title }: { name: string; size?: number; accent?: boolean; title?: string }) {
  const ch = name.slice(0, 1).toUpperCase();
  const bg = accent ? "var(--accent-soft)" : AVATAR[[...name].reduce((a, c) => a + c.charCodeAt(0), 0) % AVATAR.length];
  return (
    <span className="v3-avatar" title={title ?? name} aria-hidden="true" style={{ width: size, height: size, background: bg, color: accent ? "var(--accent-text)" : "#fff", fontSize: Math.max(10, size * 0.44) }}>
      {ch}
    </span>
  );
}
export function AiAvatar({ size = 24 }: { size?: number }) {
  return (
    <span className="v3-ai-avatar" style={{ width: size, height: size }} aria-hidden="true">
      <Icon name="sparkles" size={Math.min(14, size * 0.6)} />
    </span>
  );
}
export function AvatarGroup({ names, size = 22 }: { names: string[]; size?: number }) {
  return (
    <span className="v3-avatars">
      {names.map((n, i) => (
        <span key={n + i} className="item">
          {n === "AI" ? <AiAvatar size={size} /> : <Avatar name={n} size={size} accent={i === 0} />}
        </span>
      ))}
    </span>
  );
}

/* ---------- buttons ---------- */
type BtnKind = "primary" | "secondary" | "ghost" | "danger" | "soft";
type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & { kind?: BtnKind; icon?: IconName; to?: string; h?: number; loading?: boolean; iconRight?: IconName };
export const Btn = forwardRef<HTMLButtonElement, BtnProps>(function Btn({ kind = "secondary", icon, to, h = 28, loading, iconRight, children, className, style, ...rest }, ref) {
  const inner = (
    <>
      {loading ? <span className="v3-spinner spin" /> : icon && <Icon name={icon} size={14} />}
      {children}
      {iconRight && <Icon name={iconRight} size={13} />}
    </>
  );
  const cls = cx("v3-btn", `k-${kind}`, className);
  const st = { height: h, ...style };
  if (to)
    return (
      <Link to={to} className={cls} style={st}>
        {inner}
      </Link>
    );
  return (
    <button ref={ref} type="button" className={cls} style={st} disabled={loading || rest.disabled} {...rest}>
      {inner}
    </button>
  );
});
export function IconBtn({ icon, label, size = 28, color, onClick, to, active, style }: { icon: IconName; label: string; size?: number; color?: string; onClick?: () => void; to?: string; active?: boolean; style?: CSSProperties }) {
  const cls = cx("v3-iconbtn", active && "active");
  const st = { width: size, height: size, color, ...style };
  if (to)
    return (
      <Link to={to} className={cls} aria-label={label} title={label} style={st}>
        <Icon name={icon} size={16} />
      </Link>
    );
  return (
    <button type="button" className={cls} aria-label={label} title={label} style={st} onClick={onClick}>
      <Icon name={icon} size={16} />
    </button>
  );
}
export function Chip({ children, icon, caret, active, onClick, style }: { children: ReactNode; icon?: IconName; caret?: boolean; active?: boolean; onClick?: () => void; style?: CSSProperties }) {
  return (
    <button type="button" className={cx("v3-chip", active && "active")} onClick={onClick} style={style}>
      {icon && <Icon name={icon} size={13} />}
      {children}
      {caret && <Icon name="down" size={12} />}
    </button>
  );
}
export function TextLink({ children, to, icon = "right", onClick }: { children: ReactNode; to?: string; icon?: IconName | null; onClick?: () => void }) {
  const inner = (
    <>
      {children}
      {icon && <Icon name={icon} size={12} />}
    </>
  );
  if (to)
    return (
      <Link className="v3-link" to={to}>
        {inner}
      </Link>
    );
  return (
    <button type="button" className="v3-link" onClick={onClick}>
      {inner}
    </button>
  );
}

/* ---------- surfaces ---------- */
export function Card({ children, style, className, ...rest }: HTMLAttributes<HTMLElement>) {
  return (
    <section className={cx("v3-card", className)} style={style} {...rest}>
      {children}
    </section>
  );
}
export function CardHead({ title, count, right, icon }: { title: ReactNode; count?: number | string; right?: ReactNode; icon?: IconName }) {
  return (
    <header className="v3-card-head">
      {icon && <Icon name={icon} size={15} color="var(--muted)" />}
      <h2>{title}</h2>
      {count !== undefined && <Badge>{count}</Badge>}
      <Spacer />
      {right}
    </header>
  );
}
export function Panel({ title, right, count, icon, children, style, bodyStyle, className }: { title: ReactNode; right?: ReactNode; count?: number | string; icon?: IconName; children: ReactNode; style?: CSSProperties; bodyStyle?: CSSProperties; className?: string }) {
  return (
    <Card className={cx("v3-panel", className)} style={style}>
      <CardHead title={title} right={right} count={count} icon={icon} />
      <div className="v3-panel-body" style={bodyStyle}>
        {children}
      </div>
    </Card>
  );
}
export function Section({ title, right, children, style }: { title: ReactNode; right?: ReactNode; children: ReactNode; style?: CSSProperties }) {
  return (
    <section className="v3-section" style={style}>
      <div className="v3-section-label">
        <h3>{title}</h3>
        <Spacer />
        {right}
      </div>
      {children}
    </section>
  );
}
export function KV({ k, children, kw = 64 }: { k: ReactNode; children: ReactNode; kw?: number }) {
  return (
    <div className="v3-kv">
      <span className="k" style={{ width: kw }}>
        {k}
      </span>
      <span className="v">{children}</span>
    </div>
  );
}
export function Legend({ items }: { items: [string, string][] }) {
  return (
    <span className="v3-legend">
      {items.map(([l, c]) => (
        <span key={l}>
          <i style={{ background: c }} />
          {l}
        </span>
      ))}
    </span>
  );
}
export function Code({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <pre className="v3-code" style={style}>
      {children}
    </pre>
  );
}
export function Bar({ pct, tone = "accent", width = 80, height = 6, color }: { pct: number; tone?: Tone; width?: number | string; height?: number; color?: string }) {
  return (
    <span className="v3-bar" style={{ width, height }}>
      <span style={{ width: `${Math.max(0, Math.min(100, pct))}%`, background: color ?? toneColor(tone) }} />
    </span>
  );
}
export function Sup({ n, onClick }: { n: number; onClick?: () => void }) {
  return (
    <button type="button" className="v3-sup" onClick={onClick} aria-label={`citation ${n}`}>
      {n}
    </button>
  );
}
export function Cite({ n, children, onClick }: { n: number; children: ReactNode; onClick?: () => void }) {
  return (
    <button type="button" className="v3-cite" onClick={onClick}>
      <span className="n">{n}</span>
      {children}
    </button>
  );
}
export function Skeleton({ w = "100%", h = 14, style }: { w?: number | string; h?: number; style?: CSSProperties }) {
  return <span className="skeleton" style={{ display: "block", width: w, height: h, ...style }} />;
}
export function Empty({ icon = "inbox", title, desc, action }: { icon?: IconName; title: ReactNode; desc?: ReactNode; action?: ReactNode }) {
  return (
    <div className="v3-empty anim-fade">
      <Tile icon={icon} size={36} />
      <b>{title}</b>
      {desc && <Muted>{desc}</Muted>}
      {action}
    </div>
  );
}
export function Divider({ vertical }: { vertical?: boolean }) {
  return <span className={vertical ? "v3-vdivider" : "v3-divider"} aria-hidden="true" />;
}

/* ---------- form ---------- */
export function Field({ label, hint, children, style }: { label: ReactNode; hint?: ReactNode; children: ReactNode; style?: CSSProperties }) {
  return (
    <label className="v3-field" style={style}>
      <span className="label">{label}</span>
      {children}
      {hint && <span className="hint">{hint}</span>}
    </label>
  );
}
type InputProps = InputHTMLAttributes<HTMLInputElement> & { icon?: IconName; right?: ReactNode; mono?: boolean; width?: number | string };
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input({ icon, right, mono, width, style, className, ...rest }, ref) {
  return (
    <span className={cx("v3-input", mono && "mono", className)} style={{ width, ...style }}>
      {icon && <Icon name={icon} size={14} color="var(--muted)" />}
      <input ref={ref} {...rest} />
      {right}
    </span>
  );
});
export function SearchBox({ placeholder, value, onChange, width = 240, autoFocus }: { placeholder: string; value?: string; onChange?: (v: string) => void; width?: number | string; autoFocus?: boolean }) {
  return (
    <Input
      icon="search"
      width={width}
      placeholder={placeholder}
      aria-label={placeholder}
      value={value}
      autoFocus={autoFocus}
      onChange={(e) => onChange?.(e.target.value)}
      right={value ? <IconBtn icon="x" label="clear" size={20} onClick={() => onChange?.("")} /> : undefined}
    />
  );
}
export function Textarea(props: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className="v3-textarea" {...props} />;
}
export function Toggle({ on, onChange, label, disabled }: { on: boolean; onChange?: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button type="button" role="switch" aria-checked={on} aria-label={label} className={cx("v3-toggle", on && "on")} disabled={disabled} onClick={() => onChange?.(!on)}>
      <span />
    </button>
  );
}
export function Check({ checked, onChange, label, sub }: { checked: boolean; onChange?: (v: boolean) => void; label?: ReactNode; sub?: ReactNode }) {
  return (
    <label className="v3-check">
      <input type="checkbox" checked={checked} onChange={(e) => onChange?.(e.target.checked)} />
      {label && (
        <span className="v3-col" style={{ gap: 0 }}>
          <span>{label}</span>
          {sub && <Muted size={11.5}>{sub}</Muted>}
        </span>
      )}
    </label>
  );
}
export function Radio({ checked, onChange, label, sub, name }: { checked: boolean; onChange?: () => void; label: ReactNode; sub?: ReactNode; name: string }) {
  return (
    <label className="v3-check">
      <input type="radio" name={name} checked={checked} onChange={() => onChange?.()} />
      <span className="v3-col" style={{ gap: 0 }}>
        <span>{label}</span>
        {sub && <Muted size={11.5}>{sub}</Muted>}
      </span>
    </label>
  );
}
export function Segmented<T extends string>({ value, options, onChange, label, size = "sm" }: { value: T; options: { value: T; label: ReactNode; icon?: IconName }[]; onChange: (v: T) => void; label: string; size?: "sm" | "md" }) {
  return (
    <div role="radiogroup" aria-label={label} className={cx("v3-seg", size)}>
      {options.map((o) => (
        <button key={o.value} type="button" role="radio" aria-checked={o.value === value} className={o.value === value ? "on" : ""} onClick={() => onChange(o.value)}>
          {o.icon && <Icon name={o.icon} size={13} />}
          {o.label}
        </button>
      ))}
    </div>
  );
}
export function Pills<T extends string>({ value, options, onChange }: { value: T; options: { value: T; label: ReactNode; count?: number }[]; onChange: (v: T) => void }) {
  return (
    <div className="v3-pills">
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} className={o.value === value ? "on" : ""} onClick={() => onChange(o.value)}>
          {o.label}
          {o.count !== undefined && <span className="count">{o.count}</span>}
        </button>
      ))}
    </div>
  );
}
export function Tabs<T extends string>({ value, options, onChange, pad = "0 24px" }: { value: T; options: { value: T; label: ReactNode; count?: number | string; icon?: IconName }[]; onChange: (v: T) => void; pad?: string }) {
  return (
    <div className="v3-tabs" role="tablist" style={{ padding: pad }}>
      {options.map((o) => (
        <button key={o.value} type="button" role="tab" aria-selected={o.value === value} className={o.value === value ? "on" : ""} onClick={() => onChange(o.value)}>
          {o.icon && <Icon name={o.icon} size={13} />}
          {o.label}
          {o.count !== undefined && <span className="count">{o.count}</span>}
        </button>
      ))}
    </div>
  );
}
export function Stepper({ steps, active }: { steps: ReactNode[]; active: number }) {
  return (
    <div className="v3-stepper">
      {steps.map((s, i) => (
        <span key={i} className="v3-row" style={{ gap: 10, flexGrow: i < steps.length - 1 ? 1 : 0 }}>
          <span className={cx("step", i < active && "done", i === active && "on")}>
            <span className="mk">{i < active ? <Icon name="check" size={12} /> : i + 1}</span>
            {s}
          </span>
          {i < steps.length - 1 && <span className={cx("line", i < active && "done")} />}
        </span>
      ))}
    </div>
  );
}
