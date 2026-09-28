import { useEffect, useState, type ReactNode } from "react";
import { IconBtn, cx } from "./core";
import { useI18n } from "../i18n";
import { askCover, useAskAi } from "../app/askState";
import "./detail-panel.css";

/** Per-page memory of whether the detail panel is pinned (docked) or floating. */
export function usePinned(page: string, initial = false): [boolean, (v: boolean) => void] {
  const key = `aiops-v3-pin:${page}`;
  const [pinned, setPinned] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem(key);
      return v === null ? initial : v === "1";
    } catch {
      return initial;
    }
  });
  const set = (v: boolean) => {
    setPinned(v);
    try {
      localStorage.setItem(key, v ? "1" : "0");
    } catch {
      /* storage unavailable: keep in memory */
    }
  };
  return [pinned, set];
}

/**
 * Detail panel for spatial pages (topology, canvases, trace waterfall).
 * Floating: a rounded card over the content that never reflows it; Esc, the close button or a click on
 * empty space closes it. Pinned: docked flush to the right edge like the list + detail pages.
 * Render it inside the positioned content area when floating, and as a sibling of it when pinned.
 */
export function DetailPanel({
  pinned,
  onPin,
  onClose,
  header,
  children,
  footer,
  width = 372,
  label,
}: {
  pinned: boolean;
  onPin: (v: boolean) => void;
  onClose: () => void;
  header: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
  label: string;
}) {
  const { tx } = useI18n();
  useEffect(() => {
    if (pinned) return;
    const h = (e: KeyboardEvent) => {
      const t = e.target instanceof Element ? e.target : null;
      if (e.key === "Escape" && !t?.closest("input, textarea, [contenteditable], .ask-card")) onClose();
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [pinned, onClose]);
  return (
    <aside
      className={cx("dp", pinned ? "dp-dock" : "dp-float")}
      style={{ width }}
      aria-label={label}
      onPointerDown={(e) => e.stopPropagation()}
      onClick={(e) => e.stopPropagation()}
    >
      <div className="dp-head">
        <div className="dp-title">{header}</div>
        <IconBtn icon="pin" label={pinned ? tx("取消钉住（改为浮层）", "Unpin (float)") : tx("钉住在右侧", "Pin to the side")} active={pinned} size={26} onClick={() => onPin(!pinned)} />
        <IconBtn icon="x" label={tx("关闭", "Close")} size={26} onClick={onClose} />
      </div>
      <div className="dp-body">{children}</div>
      {footer}
      {!pinned && <div className="dp-hint">{tx("Esc 或点空白处关闭 · 钉住后停靠在右侧", "Esc or click empty space to close · pin to dock")}</div>}
    </aside>
  );
}

/** Width a floating panel covers (card + gap), used to keep the selected item visible. */
export const FLOAT_COVER = 372 + 24;

/** Width floating cards cover on the right: the detail card, plus the Ask AI card when they sit side by side. */
export function useFloatCover() {
  const askOpen = useAskAi((s) => s.isOpen);
  return FLOAT_COVER + askCover(askOpen);
}
