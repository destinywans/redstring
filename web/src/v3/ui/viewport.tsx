import { useCallback, useEffect, useState, type CSSProperties, type RefObject } from "react";
import { IconBtn } from "./core";
import { useI18n } from "../i18n";
import "./viewport.css";

export type View = { x: number; y: number; k: number };
export type Bounds = { minX: number; minY: number; maxX: number; maxY: number };
export const MIN_ZOOM = 0.4;
export const MAX_ZOOM = 1.8;
export const clampK = (k: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, k));

/** Pan / zoom state for a spatial page: Ctrl/⌘ + wheel zooms at the cursor, a plain wheel pans. */
export function useViewport(area: RefObject<HTMLElement | null>, enabled = true) {
  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 });
  const zoom = useCallback(
    (factor: number, cx?: number, cy?: number) => {
      setView((v) => {
        const k = clampK(v.k * factor);
        const r = area.current?.getBoundingClientRect();
        const px = cx !== undefined && r ? cx - r.left : (r?.width ?? 800) / 2;
        const py = cy !== undefined && r ? cy - r.top : (r?.height ?? 600) / 2;
        return { k, x: px - ((px - v.x) * k) / v.k, y: py - ((py - v.y) * k) / v.k };
      });
    },
    [area],
  );
  /** Scale the content to fit the visible area (never above 100%) and centre it. */
  const fitTo = useCallback(
    (b: Bounds, pad: { x?: number; top?: number; bottom?: number } = {}) => {
      const el = area.current;
      if (!el) return;
      const px = pad.x ?? 40;
      const top = pad.top ?? 40;
      const bottom = pad.bottom ?? 40;
      const w = el.clientWidth;
      const h = el.clientHeight;
      const bw = Math.max(1, b.maxX - b.minX);
      const bh = Math.max(1, b.maxY - b.minY);
      const k = clampK(Math.min((w - 2 * px) / bw, (h - top - bottom) / bh, 1));
      setView({ k, x: (w - bw * k) / 2 - b.minX * k, y: top + (h - top - bottom - bh * k) / 2 - b.minY * k });
    },
    [area],
  );
  useEffect(() => {
    const el = area.current;
    if (!el || !enabled) return;
    const h = (e: WheelEvent) => {
      e.preventDefault();
      if (e.ctrlKey || e.metaKey) zoom(Math.exp(-e.deltaY * 0.01), e.clientX, e.clientY);
      else setView((v) => ({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY }));
    };
    el.addEventListener("wheel", h, { passive: false });
    return () => el.removeEventListener("wheel", h);
  }, [area, zoom, enabled]);
  return { view, setView, zoom, fitTo };
}

/** Bottom-left zoom control shared by the topology and the canvases: − · % (reset to 100%) · + · fit. */
export function ZoomControl({ k, onZoom, onReset, onFit, style }: { k: number; onZoom: (factor: number) => void; onReset: () => void; onFit: () => void; style?: CSSProperties }) {
  const { tx } = useI18n();
  return (
    <div className="vp-zoom" style={style} onPointerDown={(e) => e.stopPropagation()} onClick={(e) => e.stopPropagation()}>
      <IconBtn icon="zoomout" label={tx("缩小", "Zoom out")} size={26} onClick={() => onZoom(1 / 1.18)} />
      <button type="button" className="tnum" onClick={onReset} title={tx("恢复到 100%", "Reset to 100%")}>
        {Math.round(k * 100)}%
      </button>
      <IconBtn icon="zoomin" label={tx("放大", "Zoom in")} size={26} onClick={() => onZoom(1.18)} />
      <IconBtn icon="fit" label={tx("适应画布", "Fit to view")} size={26} onClick={onFit} />
    </div>
  );
}
