import { useId, type CSSProperties } from "react";
import { cx } from "./core";
import "./brand.css";

/**
 * Redstring mark: a red lightning bolt threaded through two clue dots, ending on the red root-cause dot.
 * Geometry is on a 64 grid and matches scripts/brand/assets.py, which writes the favicon and README files.
 */
type Pt = readonly [number, number];
const S: Pt = [46, 5];
const A: Pt = [16, 35];
const B: Pt = [48, 27];
const E: Pt = [18, 57];
const CLUES: Pt[] = [
  [31, 20],
  [33, 42],
];

type Optical = { w: number; gap: number; r: number; rr: number };
// Smaller marks get a heavier line, bigger dots and no gaps so they stay legible.
const LARGE: Optical = { w: 3.6, gap: 1.4, r: 3.8, rr: 4.4 };
const MEDIUM: Optical = { w: 4.2, gap: 1.4, r: 3.8, rr: 4.4 };
const SMALL: Optical = { w: 5.2, gap: 0, r: 4.6, rr: 5.3 };
const optical = (size: number) => (size <= 24 ? SMALL : size < 48 ? MEDIUM : LARGE);

const pt = (q: Pt) => `${+q[0].toFixed(2)} ${+q[1].toFixed(2)}`;
const dist = (a: Pt, b: Pt) => Math.hypot(b[0] - a[0], b[1] - a[1]);
/** Point `d` units from `at`, measured along the direction from → to. */
const along = (from: Pt, to: Pt, at: Pt, d: number): Pt => {
  const l = dist(from, to);
  return [at[0] + ((to[0] - from[0]) / l) * d, at[1] + ((to[1] - from[1]) / l) * d];
};
const FULL = `M${pt(S)}L${pt(A)}L${pt(B)}L${pt(E)}`;
const LEN = dist(S, A) + dist(A, B) + dist(B, E);
// How far along the line each dot sits; the intro pops them in this order.
const AT = [dist(S, CLUES[0]) / LEN, (dist(S, A) + dist(A, B) + dist(B, CLUES[1])) / LEN, 1];

// The thread stops short of every dot so it reads as passing through it.
function threadPath(o: Optical) {
  if (!o.gap) return FULL;
  const g = o.r + o.gap;
  return (
    `M${pt(S)}L${pt(along(S, A, CLUES[0], -g))}` +
    `M${pt(along(S, A, CLUES[0], g))}L${pt(A)}L${pt(B)}L${pt(along(B, E, CLUES[1], -g))}` +
    `M${pt(along(B, E, CLUES[1], g))}L${pt(along(B, E, E, -(o.rr + o.gap)))}`
  );
}

/** Colours come from the parent: `.rs-thread` in the mark, plain white inside masks. */
function Thread({ o }: { o: Optical }) {
  return (
    <>
      <path d={threadPath(o)} fill="none" strokeWidth={o.w} strokeLinecap={o.gap ? "butt" : "round"} strokeLinejoin="miter" strokeMiterlimit={12} />
      {o.gap > 0 && <circle cx={S[0]} cy={S[1]} r={o.w / 2} />}
    </>
  );
}

/**
 * - static: the plain mark.
 * - intro: drawn once on mount, then a short flash; for app start.
 * - loading: dimmed line with a current running along it.
 * - thinking: the dots light up in turn while the AI works.
 */
export type BrandMarkState = "static" | "intro" | "loading" | "thinking";

export function BrandMark({ size = 20, state = "static", label }: { size?: number; state?: BrandMarkState; label?: string }) {
  const id = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const o = optical(size);
  const reveal = `rs-reveal-${id}`;
  const shape = `rs-shape-${id}`;
  return (
    <svg
      className={cx("rs-mark", state !== "static" && `rs-${state}`)}
      width={size}
      height={size}
      viewBox="0 0 64 64"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      style={{ "--rs-len": LEN.toFixed(1) } as CSSProperties}
    >
      <defs>
        {state === "intro" && (
          <mask id={reveal} maskUnits="userSpaceOnUse" x={-8} y={-8} width={80} height={80}>
            <path className="rs-draw" d={FULL} fill="none" stroke="#fff" strokeWidth={o.w + 5} strokeLinecap="round" strokeLinejoin="miter" strokeMiterlimit={12} />
          </mask>
        )}
        {state === "loading" && o.gap > 0 && (
          <mask id={shape} maskUnits="userSpaceOnUse" x={-8} y={-8} width={80} height={80}>
            <g stroke="#fff" fill="#fff">
              <Thread o={o} />
            </g>
          </mask>
        )}
      </defs>
      <g className="rs-body">
        <g className="rs-thread" mask={state === "intro" ? `url(#${reveal})` : undefined}>
          <Thread o={o} />
        </g>
        {state === "loading" && (
          <path className="rs-glint" d={FULL} fill="none" strokeWidth={o.w} strokeLinejoin="miter" strokeMiterlimit={12} mask={o.gap ? `url(#${shape})` : undefined} />
        )}
        {[...CLUES, E].map((q, i) => (
          <circle key={i} className={cx("rs-dot", i === 2 && "rs-cause")} style={{ "--rs-at": AT[i].toFixed(3), "--rs-i": i } as CSSProperties} cx={q[0]} cy={q[1]} r={i === 2 ? o.rr : o.r} />
        ))}
      </g>
    </svg>
  );
}
