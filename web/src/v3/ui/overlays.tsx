import { useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { create } from "zustand";
import { Icon, IconBtn, type IconName } from "./core";
import { useI18n } from "../i18n";

/* ---------- Esc / focus helpers ---------- */
function useEscape(open: boolean, onClose: () => void) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);
}

/* ---------- Drawer ---------- */
export function Drawer({
  open,
  onClose,
  width = 420,
  title,
  sub,
  icon,
  right,
  children,
  footer,
  modal = true,
}: {
  open: boolean;
  onClose: () => void;
  width?: number;
  title: ReactNode;
  sub?: ReactNode;
  icon?: ReactNode;
  right?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  modal?: boolean;
}) {
  const { tx } = useI18n();
  useEscape(open, onClose);
  if (!open) return null;
  return createPortal(
    <div className="v3">
      {modal && <div className="v3-backdrop" onClick={onClose} aria-hidden="true" />}
      <aside className="v3-drawer" role="dialog" aria-label={typeof title === "string" ? title : undefined} style={{ width }}>
        <div className="v3-drawer-head">
          {icon}
          <div className="v3-col grow" style={{ gap: 0 }}>
            <b style={{ fontSize: 14, fontWeight: 600 }}>{title}</b>
            {sub && <span className="muted" style={{ fontSize: 11.5 }}>{sub}</span>}
          </div>
          {right}
          <IconBtn icon="x" label={tx("关闭", "Close")} onClick={onClose} />
        </div>
        <div className="v3-drawer-body">{children}</div>
        {footer && <div className="v3-drawer-foot">{footer}</div>}
      </aside>
    </div>,
    document.body,
  );
}

/* ---------- Dialog ---------- */
export function Dialog({ open, onClose, title, children, footer, width = 520 }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; footer?: ReactNode; width?: number }) {
  const { tx } = useI18n();
  useEscape(open, onClose);
  if (!open) return null;
  return createPortal(
    <div className="v3">
      <div className="v3-backdrop" onClick={onClose} aria-hidden="true" />
      <div className="v3-dialog" role="dialog" aria-modal="true" style={{ width: `min(${width}px, calc(100vw - 32px))` }}>
        <div className="v3-drawer-head">
          <b style={{ fontSize: 14, fontWeight: 600, flexGrow: 1 }}>{title}</b>
          <IconBtn icon="x" label={tx("关闭", "Close")} onClick={onClose} />
        </div>
        <div className="v3-dialog-body">{children}</div>
        {footer && (
          <div className="v3-row" style={{ gap: 8, justifyContent: "flex-end", padding: "12px 16px", borderTop: "1px solid var(--border)", background: "var(--sunken)" }}>
            {footer}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

/* ---------- Anchored popover ---------- */
type Place = "bottom-start" | "bottom-end" | "top-start" | "top-end" | "right-start";
function usePosition(anchor: HTMLElement | null, open: boolean, place: Place, pop: HTMLElement | null) {
  const [pos, setPos] = useState<CSSProperties>({ left: -9999, top: -9999 });
  useLayoutEffect(() => {
    if (!open || !anchor) return;
    const update = () => {
      const r = anchor.getBoundingClientRect();
      const pw = pop?.offsetWidth ?? 240;
      const ph = pop?.offsetHeight ?? 200;
      let left = place.endsWith("end") ? r.right - pw : place === "right-start" ? r.right + 8 : r.left;
      let top = place.startsWith("top") ? r.top - ph - 6 : place === "right-start" ? r.top : r.bottom + 6;
      if (top + ph > window.innerHeight - 8) top = Math.max(8, r.top - ph - 6);
      left = Math.max(8, Math.min(left, window.innerWidth - pw - 8));
      setPos({ left, top });
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [anchor, open, place, pop]);
  return pos;
}

/**
 * Fixed-position panel anchored to an element: opens on whichever side (below / above) has more room,
 * spans `span` (defaults to the anchor) and caps its height to the available space.
 */
export function useAnchoredPanel(open: boolean, anchor: HTMLElement | null, span: HTMLElement | null, opts: { minWidth?: number; maxHeight?: number; prefer?: "below" | "above" } = {}) {
  const [style, setStyle] = useState<CSSProperties>({ position: "fixed", left: -9999, top: -9999 });
  const { minWidth = 360, maxHeight = 460, prefer } = opts;
  useLayoutEffect(() => {
    if (!open || !anchor) return;
    const update = () => {
      const a = anchor.getBoundingClientRect();
      const w = (span ?? anchor).getBoundingClientRect();
      const below = window.innerHeight - a.bottom - 12;
      const above = a.top - 12;
      const down = prefer === "below" ? below >= 240 || below >= above : prefer === "above" ? above < 240 && below > above : below >= Math.min(320, above) || below >= above;
      const width = Math.min(Math.max(w.width, minWidth), window.innerWidth - 16);
      const left = Math.max(8, Math.min(w.left, window.innerWidth - width - 8));
      const room = Math.max(160, (down ? below : above) - 8);
      setStyle({ position: "fixed", left, width, maxHeight: Math.min(maxHeight, room), ...(down ? { top: a.bottom + 8 } : { bottom: window.innerHeight - a.top + 8 }) });
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open, anchor, span, minWidth, maxHeight, prefer]);
  return style;
}

export function Popover({ anchor, open, onClose, children, place = "bottom-start", style }: { anchor: HTMLElement | null; open: boolean; onClose: () => void; children: ReactNode; place?: Place; style?: CSSProperties }) {
  const [pop, setPop] = useState<HTMLDivElement | null>(null);
  const pos = usePosition(anchor, open, place, pop);
  useEscape(open, onClose);
  useEffect(() => {
    if (!open) return;
    const h = (e: MouseEvent) => {
      const t = e.target as Node;
      if (pop?.contains(t) || anchor?.contains(t)) return;
      onClose();
    };
    window.addEventListener("mousedown", h);
    return () => window.removeEventListener("mousedown", h);
  }, [open, onClose, pop, anchor]);
  if (!open) return null;
  return createPortal(
    <div className="v3">
      <div ref={setPop} className="v3-popover" style={{ ...pos, ...style }}>
        {children}
      </div>
    </div>,
    document.body,
  );
}

export type MenuItem = { label: ReactNode; icon?: IconName; onSelect?: () => void; danger?: boolean; kbd?: string; sep?: boolean; active?: boolean };
/** Button that opens a small action menu. */
export function MenuButton({ trigger, items, place = "bottom-end" }: { trigger: (props: { onClick: () => void; ref: (el: HTMLElement | null) => void; open: boolean }) => ReactNode; items: MenuItem[]; place?: Place }) {
  const [open, setOpen] = useState(false);
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  return (
    <>
      {trigger({ onClick: () => setOpen((o) => !o), ref: setAnchor, open })}
      <Popover anchor={anchor} open={open} onClose={() => setOpen(false)} place={place}>
        <div className="v3-menu" role="menu">
          {items.map((it, i) =>
            it.sep ? (
              <div key={i} className="sep" />
            ) : (
              <button
                key={i}
                role="menuitem"
                className={it.active ? "on" : ""}
                style={it.danger ? { color: "var(--danger)" } : undefined}
                onClick={() => {
                  setOpen(false);
                  it.onSelect?.();
                }}
              >
                {it.icon && <Icon name={it.icon} size={14} />}
                <span style={{ flexGrow: 1 }}>{it.label}</span>
                {it.active && <Icon name="check" size={13} />}
                {it.kbd && <span className="v3-kbd">{it.kbd}</span>}
              </button>
            ),
          )}
        </div>
      </Popover>
    </>
  );
}

/** Hover (or keyboard focus) preview card with a short open delay, like Linear's peeks. */
export function HoverPeek({ children, content, place = "bottom-start", delay = 280 }: { children: ReactNode; content: () => ReactNode; place?: Place; delay?: number }) {
  const [open, setOpen] = useState(false);
  const [anchor, setAnchor] = useState<HTMLSpanElement | null>(null);
  const [pop, setPop] = useState<HTMLDivElement | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const pos = usePosition(anchor, open, place, pop);
  const show = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setOpen(true), delay);
  }, [delay]);
  const hide = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setOpen(false), 120);
  }, []);
  useEffect(() => () => clearTimeout(timer.current), []);
  return (
    <>
      <span ref={setAnchor} className="v3-peek-host" tabIndex={0} onMouseEnter={show} onMouseLeave={hide} onFocus={show} onBlur={hide}>
        {children}
      </span>
      {open &&
        createPortal(
          <div className="v3">
            <div ref={setPop} className="v3-popover v3-peek" style={pos} onMouseEnter={show} onMouseLeave={hide}>
              {content()}
            </div>
          </div>,
          document.body,
        )}
    </>
  );
}

export function Tooltip({ label, children }: { label: string; children: ReactNode }) {
  const [rect, setRect] = useState<DOMRect | null>(null);
  return (
    <span onMouseEnter={(e) => setRect(e.currentTarget.getBoundingClientRect())} onMouseLeave={() => setRect(null)} style={{ display: "inline-flex" }}>
      {children}
      {rect &&
        createPortal(
          <span className="v3-tooltip" style={{ left: rect.left + rect.width / 2, top: rect.top - 30, transform: "translateX(-50%)" }}>
            {label}
          </span>,
          document.body,
        )}
    </span>
  );
}

/* ---------- toasts with undo ---------- */
type Toast = { id: number; text: ReactNode; undo?: () => void; tone?: "ok" | "danger" };
type ToastStore = { toasts: Toast[]; push: (t: Omit<Toast, "id">) => void; dismiss: (id: number) => void };
let seq = 0;
export const useToasts = create<ToastStore>((set, get) => ({
  toasts: [],
  push: (t) => {
    const id = ++seq;
    set({ toasts: [...get().toasts.slice(-2), { ...t, id }] });
    setTimeout(() => get().dismiss(id), t.undo ? 10000 : 4000);
  },
  dismiss: (id) => set({ toasts: get().toasts.filter((x) => x.id !== id) }),
}));
export const toast = (text: ReactNode, opts: { undo?: () => void; tone?: "ok" | "danger" } = {}) => useToasts.getState().push({ text, ...opts });

export function Toaster() {
  const { toasts, dismiss } = useToasts();
  const { tx } = useI18n();
  return (
    <div className="v3-toasts" role="status" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className="v3-toast">
          <Icon name={t.tone === "danger" ? "alert" : "check"} size={15} />
          <span>{t.text}</span>
          {t.undo && (
            <button
              type="button"
              onClick={() => {
                t.undo?.();
                dismiss(t.id);
              }}
            >
              {tx("撤销", "Undo")}
            </button>
          )}
          <button type="button" className="x" aria-label={tx("关闭", "Close")} onClick={() => dismiss(t.id)}>
            <Icon name="x" size={13} />
          </button>
        </div>
      ))}
    </div>
  );
}
