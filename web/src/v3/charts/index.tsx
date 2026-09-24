import { useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import "./charts.css";

export const C = Array.from({ length: 8 }, (_, i) => `var(--c${i + 1})`);
export const CS = Array.from({ length: 8 }, (_, i) => `var(--c${i + 1}s)`);
export const HEAT = Array.from({ length: 5 }, (_, i) => `var(--h${i + 1})`);
export const TONE: Record<string, string> = {
  ok: "var(--ok)",
  warn: "var(--warn)",
  danger: "var(--danger)",
  accent: "var(--accent)",
  info: "var(--info)",
  neutral: "var(--faint)",
};
const tc = (c: string) => TONE[c] ?? c;

/** Measure the container so charts stay fluid in responsive layouts. */
export function useWidth<T extends HTMLElement>(fixed?: number) {
  const ref = useRef<T>(null);
  const [w, setW] = useState(fixed ?? 0);
  useLayoutEffect(() => {
    if (fixed || !ref.current) return;
    const el = ref.current;
    const ro = new ResizeObserver(([e]) => setW(Math.floor(e.contentRect.width)));
    ro.observe(el);
    setW(Math.floor(el.getBoundingClientRect().width));
    return () => ro.disconnect();
  }, [fixed]);
  return [ref, w] as const;
}

const pts = (vals: number[], w: number, h: number, max: number, min = 0) =>
  vals.map((v, i) => [vals.length === 1 ? w / 2 : (w * i) / (vals.length - 1), h - (h * (v - min)) / (max - min || 1)] as const);
const line = (p: readonly (readonly [number, number])[]) => p.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
const Grid = ({ w, h, n }: { w: number; h: number; n: number }) => (
  <>
    {Array.from({ length: Math.max(0, n - 1) }, (_, k) => (
      <line key={k} x1={0} x2={w} y1={(h * (k + 1)) / n} y2={(h * (k + 1)) / n} className="ch-grid" />
    ))}
  </>
);

export type Series = { name?: string; values: number[]; color?: string; fill?: string; dashed?: boolean };
export type Marker = { at: number; label: string; tone?: string };

/** Line/area chart with threshold, markers, bands, and a hover crosshair + tooltip. */
export function Lines({
  series,
  height = 120,
  width,
  max,
  threshold,
  markers = [],
  bands = [],
  grid = 4,
  labels,
  format = (v: number) => String(Math.round(v * 100) / 100),
  draw = true,
  hover = true,
  axis,
}: {
  series: Series[];
  height?: number;
  width?: number;
  max?: number;
  threshold?: number;
  markers?: Marker[];
  bands?: { from: number; to: number; color?: string }[];
  grid?: number;
  labels?: string[];
  format?: (v: number) => string;
  draw?: boolean;
  hover?: boolean;
  axis?: string[];
}) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const [hi, setHi] = useState<number | null>(null);
  const n = series[0]?.values.length ?? 0;
  const ymax = max ?? Math.max(1e-9, ...series.flatMap((s) => s.values)) * 1.15;
  const xat = (i: number) => (n <= 1 ? 0 : (w * i) / (n - 1));
  return (
    <div className="ch-wrap" ref={ref} style={{ height: height + (axis ? 18 : 0), width: width ?? "100%" }}>
      {w > 0 && (
        <div style={{ position: "relative", height }}>
          <svg width={w} height={height} className="ch-svg"
            onMouseMove={hover ? (e) => {
              const r = e.currentTarget.getBoundingClientRect();
              setHi(Math.max(0, Math.min(n - 1, Math.round(((e.clientX - r.left) / w) * (n - 1)))));
            } : undefined}
            onMouseLeave={() => setHi(null)}>
            <Grid w={w} h={height} n={grid} />
            {bands.map((b, i) => (
              <rect key={i} x={xat(b.from)} y={0} width={xat(b.to) - xat(b.from)} height={height} style={{ fill: b.color ?? "var(--accent-soft)" }} />
            ))}
            {series.map((s, k) => {
              if (!s.fill) return null;
              const p = pts(s.values, w, height, ymax);
              return <polygon key={"f" + k} points={`${line(p)} ${w},${height} 0,${height}`} style={{ fill: s.fill }} className={draw ? "anim-fade" : undefined} />;
            })}
            {threshold !== undefined && (
              <line x1={0} x2={w} y1={height * (1 - threshold / ymax)} y2={height * (1 - threshold / ymax)} className="ch-thr" />
            )}
            {series.map((s, k) => (
              <polyline
                key={k}
                points={line(pts(s.values, w, height, ymax))}
                pathLength={draw ? 1 : undefined}
                className={"ch-line" + (draw ? " ch-draw" : "") + (s.dashed ? " dashed" : "")}
                style={{ stroke: s.color ?? C[k % 8] }}
              />
            ))}
            {markers.map((m, i) => (
              <line key={i} x1={xat(m.at)} x2={xat(m.at)} y1={0} y2={height} className="ch-marker" style={{ stroke: tc(m.tone ?? "warn") }} />
            ))}
            {hi !== null && (
              <g>
                <line x1={xat(hi)} x2={xat(hi)} y1={0} y2={height} className="ch-cross" />
                {series.map((s, k) => (
                  <circle key={k} cx={xat(hi)} cy={height - (height * s.values[hi]) / ymax} r={3.5} style={{ fill: "var(--panel)", stroke: s.color ?? C[k % 8], strokeWidth: 2 }} />
                ))}
              </g>
            )}
          </svg>
          {markers.map((m, i) => (
            <span key={i} className="ch-mlabel" style={{ left: xat(m.at), borderColor: tc(m.tone ?? "warn"), color: tc(m.tone ?? "warn") }}>
              {m.label}
            </span>
          ))}
          {hi !== null && (
            <div className="ch-tip" style={{ left: Math.min(xat(hi) + 10, w - 140), top: 8 }}>
              {labels?.[hi] && <b>{labels[hi]}</b>}
              {series.map((s, k) => (
                <span key={k}>
                  <i style={{ background: s.color ?? C[k % 8] }} />
                  {s.name ?? `#${k + 1}`}
                  <em>{format(s.values[hi])}</em>
                </span>
              ))}
            </div>
          )}
        </div>
      )}
      {axis && <Axis labels={axis} />}
    </div>
  );
}

export function Axis({ labels, style }: { labels: string[]; style?: CSSProperties }) {
  return (
    <div className="ch-axis" style={style}>
      {labels.map((l, i) => (
        <span key={i}>{l}</span>
      ))}
    </div>
  );
}

export function Sparkline({ values, color = "var(--accent)", fill, width = 64, height = 18 }: { values: number[]; color?: string; fill?: string; width?: number; height?: number }) {
  const ymax = Math.max(...values) * 1.1 || 1;
  const p = pts(values, width - 4, height - 4, ymax).map(([x, y]) => [x + 2, y + 2] as const);
  const soft = fill ?? (color.includes("danger") ? "var(--danger-soft)" : color.includes("warn") ? "var(--warn-soft)" : color.includes("ok") ? "var(--ok-soft)" : "var(--accent-soft)");
  return (
    <svg width={width} height={height} className="ch-svg" aria-hidden="true">
      <polygon points={`${line(p)} ${p[p.length - 1][0]},${height} ${p[0][0]},${height}`} style={{ fill: soft }} />
      <polyline points={line(p)} className="ch-line thin" style={{ stroke: color }} />
    </svg>
  );
}

export function StackedArea({ series, colors = C, height = 120, width, labels, names, axis }: { series: number[][]; colors?: string[]; height?: number; width?: number; labels?: string[]; names?: string[]; axis?: string[] }) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const [hi, setHi] = useState<number | null>(null);
  const n = series[0]?.length ?? 0;
  const tops = Array.from({ length: n }, (_, i) => series.reduce((a, s) => a + s[i], 0));
  const ymax = Math.max(...tops) * 1.1 || 1;
  let base = Array(n).fill(0) as number[];
  const polys = series.map((s) => {
    const top = s.map((v, i) => base[i] + v);
    const up = pts(top, w, height, ymax);
    const low = pts(base, w, height, ymax);
    base = top;
    return `${line(up)} ${line([...low].reverse())}`;
  });
  const xat = (i: number) => (w * i) / Math.max(1, n - 1);
  return (
    <div ref={ref} className="ch-wrap" style={{ width: width ?? "100%" }}>
      {w > 0 && (
        <div style={{ position: "relative", height }}>
          <svg width={w} height={height} className="ch-svg"
            onMouseMove={(e) => {
              const r = e.currentTarget.getBoundingClientRect();
              setHi(Math.max(0, Math.min(n - 1, Math.round(((e.clientX - r.left) / w) * (n - 1)))));
            }}
            onMouseLeave={() => setHi(null)}>
            <Grid w={w} h={height} n={4} />
            {polys.map((p, k) => (
              <polygon key={k} points={p} className="anim-fade" style={{ fill: colors[k % colors.length], opacity: 0.85 }} />
            ))}
            {hi !== null && <line x1={xat(hi)} x2={xat(hi)} y1={0} y2={height} className="ch-cross" />}
          </svg>
          {hi !== null && (
            <div className="ch-tip" style={{ left: Math.min(xat(hi) + 10, w - 140), top: 8 }}>
              {labels?.[hi] && <b>{labels[hi]}</b>}
              {series.map((s, k) => (
                <span key={k}>
                  <i style={{ background: colors[k % colors.length] }} />
                  {names?.[k] ?? `#${k + 1}`}
                  <em>{Math.round(s[hi] * 10) / 10}</em>
                </span>
              ))}
            </div>
          )}
        </div>
      )}
      {axis && <Axis labels={axis} />}
    </div>
  );
}

export function StackedBars({ data, colors, height = 80, width, gap = 3, names, labels }: { data: number[][]; colors: string[]; height?: number; width?: number; gap?: number; names?: string[]; labels?: string[] }) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const [hi, setHi] = useState<number | null>(null);
  const m = Math.max(...data.map((d) => d.reduce((a, b) => a + b, 0))) || 1;
  const bw = (w - gap * (data.length - 1)) / data.length;
  return (
    <div ref={ref} className="ch-wrap" style={{ width: width ?? "100%", height, position: "relative" }}>
      {w > 0 && (
        <svg width={w} height={height} className="ch-svg" onMouseLeave={() => setHi(null)}>
          {data.map((parts, i) => {
            let y = height;
            return (
              <g key={i} onMouseEnter={() => setHi(i)} opacity={hi === null || hi === i ? 1 : 0.55} style={{ transition: "opacity 120ms" }}>
                <rect x={i * (bw + gap)} y={0} width={bw} height={height} fill="transparent" />
                {parts.map((v, j) => {
                  const bh = (height * v) / m;
                  if (bh <= 0) return null;
                  y -= bh;
                  return <rect key={j} className="ch-grow" x={i * (bw + gap)} y={y} width={bw} height={bh} rx={1.5} style={{ fill: colors[j % colors.length], animationDelay: `${i * 12}ms` }} />;
                })}
              </g>
            );
          })}
        </svg>
      )}
      {hi !== null && names && (
        <div className="ch-tip" style={{ left: Math.min(hi * (bw + gap) + bw + 6, w - 130), top: 0 }}>
          {labels?.[hi] && <b>{labels[hi]}</b>}
          {data[hi].map((v, j) => (
            <span key={j}>
              <i style={{ background: colors[j % colors.length] }} />
              {names[j]}
              <em>{v}</em>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

export function VBars({ values, color = "var(--c1)", highlight, hiColor = "var(--danger)", height = 80, width, gap = 3 }: { values: number[]; color?: string; highlight?: Set<number>; hiColor?: string; height?: number; width?: number; gap?: number }) {
  return <VBarsInner values={values} color={color} highlight={highlight} hiColor={hiColor} height={height} width={width} gap={gap} />;
}
function VBarsInner({ values, color, highlight, hiColor, height, width, gap }: { values: number[]; color: string; highlight?: Set<number>; hiColor: string; height: number; width?: number; gap: number }) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const m = Math.max(...values) || 1;
  const bw = (w - gap * (values.length - 1)) / values.length;
  return (
    <div ref={ref} className="ch-wrap" style={{ width: width ?? "100%", height }}>
      {w > 0 && (
        <svg width={w} height={height} className="ch-svg">
          <Grid w={w} h={height} n={3} />
          {values.map((v, i) => {
            const bh = Math.max(1.5, (height * v) / m);
            return (
              <rect key={i} className="ch-grow" x={i * (bw + gap)} y={height - bh} width={bw} height={bh} rx={2} style={{ fill: highlight?.has(i) ? hiColor : color, animationDelay: `${i * 10}ms` }}>
                <title>{v}</title>
              </rect>
            );
          })}
        </svg>
      )}
    </div>
  );
}

export function HBars({ items, labelWidth = 110, unit = "", color = "var(--c1)" }: { items: { label: ReactNode; value: number; tone?: string }[]; labelWidth?: number; unit?: string; color?: string }) {
  const m = Math.max(...items.map((i) => i.value)) || 1;
  return (
    <div className="ch-hbars">
      {items.map((it, i) => (
        <div key={i} className="row">
          <span className="ellipsis" style={{ width: labelWidth }}>
            {it.label}
          </span>
          <span className="track">
            <span className="fill" style={{ width: `${(100 * it.value) / m}%`, background: it.tone ? tc(it.tone) : color, animationDelay: `${i * 40}ms` }} />
          </span>
          <span className="val tnum">
            {it.value.toLocaleString()}
            {unit}
          </span>
        </div>
      ))}
    </div>
  );
}

export function Heatmap({ matrix, height = 140, width, palette = HEAT, rowLabels, gap = 1.5 }: { matrix: number[][]; height?: number; width?: number; palette?: string[]; rowLabels?: string[]; gap?: number }) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const rows = matrix.length;
  const cols = matrix[0]?.length ?? 0;
  const lw = rowLabels ? 56 : 0;
  const cw = (w - lw - gap * (cols - 1)) / cols;
  const ch = (height - gap * (rows - 1)) / rows;
  return (
    <div ref={ref} className="ch-wrap" style={{ width: width ?? "100%", height, display: "flex" }}>
      {rowLabels && (
        <div className="ch-rowlabels" style={{ width: lw - 6, height }}>
          {rowLabels.map((l) => (
            <span key={l}>{l}</span>
          ))}
        </div>
      )}
      {w > 0 && (
        <svg width={w - lw} height={height} className="ch-svg">
          {matrix.map((r, ri) =>
            r.map((v, ci) => (
              <rect key={ri + "-" + ci} x={ci * (cw + gap)} y={ri * (ch + gap)} width={Math.max(0, cw)} height={ch} rx={1.5} style={{ fill: palette[Math.min(palette.length - 1, Math.floor(v * palette.length))] }}>
                <title>{Math.round(v * 100)}%</title>
              </rect>
            )),
          )}
        </svg>
      )}
    </div>
  );
}

export function Gauge({ pct, size = 150, tone = "ok", label, sub }: { pct: number; size?: number; tone?: string; label: ReactNode; sub?: ReactNode }) {
  const r = size / 2 - 10;
  const half = Math.PI * r;
  const val = half * Math.max(0, Math.min(1, pct));
  return (
    <div className="ch-gauge" style={{ width: size }}>
      <svg width={size} height={size / 2 + 10} className="ch-svg">
        <path d={`M 10 ${size / 2} A ${r} ${r} 0 0 1 ${size - 10} ${size / 2}`} className="ch-gauge-track" />
        <path d={`M 10 ${size / 2} A ${r} ${r} 0 0 1 ${size - 10} ${size / 2}`} className="ch-gauge-val" style={{ stroke: tc(tone), strokeDasharray: `${val} ${half * 2}` }} />
      </svg>
      <div className="lbl" style={{ top: size / 2 - 22 }}>
        <b style={{ color: tc(tone) }}>{label}</b>
        {sub && <span>{sub}</span>}
      </div>
    </div>
  );
}

export function Donut({ parts, size = 120, thick = 16, center, sub }: { parts: { value: number; color: string; label?: string }[]; size?: number; thick?: number; center?: ReactNode; sub?: ReactNode }) {
  const r = (size - thick) / 2;
  const circ = 2 * Math.PI * r;
  const total = parts.reduce((a, p) => a + p.value, 0) || 1;
  let off = 0;
  return (
    <div className="ch-donut" style={{ width: size, height: size }}>
      <svg width={size} height={size} className="ch-svg">
        <circle cx={size / 2} cy={size / 2} r={r} style={{ fill: "none", stroke: "var(--sunken)", strokeWidth: thick }} />
        {parts.map((p, i) => {
          const seg = (circ * p.value) / total;
          const el = (
            <circle key={i} cx={size / 2} cy={size / 2} r={r} transform={`rotate(-90 ${size / 2} ${size / 2})`} className="ch-donut-seg"
              style={{ fill: "none", stroke: p.color, strokeWidth: thick, strokeDasharray: `${Math.max(0, seg - 1.5)} ${circ}`, strokeDashoffset: -off }}>
              {p.label && <title>{`${p.label} · ${p.value}`}</title>}
            </circle>
          );
          off += seg;
          return el;
        })}
      </svg>
      <div className="lbl">
        <b>{center}</b>
        {sub && <span>{sub}</span>}
      </div>
    </div>
  );
}

export function BoxPlot({ stats, height = 120, width, max }: { stats: { label: string; min: number; q1: number; med: number; q3: number; max: number }[]; height?: number; width?: number; max: number }) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const slot = w / stats.length;
  const y = (v: number) => height - (height * v) / max;
  return (
    <div ref={ref} className="ch-wrap" style={{ width: width ?? "100%" }}>
      {w > 0 && (
        <svg width={w} height={height} className="ch-svg">
          <Grid w={w} h={height} n={4} />
          {stats.map((s, i) => {
            const cx = slot * i + slot / 2;
            const bw = slot * 0.42;
            return (
              <g key={i}>
                <line x1={cx} x2={cx} y1={y(s.max)} y2={y(s.min)} style={{ stroke: "var(--muted)", strokeWidth: 1.5 }} />
                <rect x={cx - bw / 2} y={y(s.q3)} width={bw} height={y(s.q1) - y(s.q3)} rx={2} style={{ fill: "var(--c1s)", stroke: "var(--c1)", strokeWidth: 1.5 }} />
                <line x1={cx - bw / 2} x2={cx + bw / 2} y1={y(s.med)} y2={y(s.med)} style={{ stroke: "var(--c1)", strokeWidth: 2 }} />
              </g>
            );
          })}
        </svg>
      )}
      <Axis labels={stats.map((s) => s.label)} style={{ justifyContent: "space-around" }} />
    </div>
  );
}

export type SankeyNode = { id: string; label: string; value: number; color: string };
export function Sankey({ cols, links, height = 400, width }: { cols: SankeyNode[][]; links: { from: string; to: string; value: number; color: string }[]; height?: number; width?: number }) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const nodeW = 10;
  const layout = useMemo(() => {
    if (!w) return null;
    const pos: Record<string, { x: number; y: number; h: number; out: number; inn: number }> = {};
    const xgap = (w - nodeW) / (cols.length - 1);
    const total = Math.max(...cols.map((c) => c.reduce((a, n) => a + n.value, 0)));
    const maxN = Math.max(...cols.map((c) => c.length));
    const scale = (height - 10 * (maxN - 1)) / total;
    cols.forEach((col, ci) => {
      let y = 0;
      col.forEach((n) => {
        const h = n.value * scale;
        pos[n.id] = { x: ci * xgap, y, h, out: 0, inn: 0 };
        y += h + 10;
      });
    });
    const paths = links.map((l) => {
      const s = pos[l.from];
      const d = pos[l.to];
      const th = l.value * scale;
      const y0 = s.y + s.out + th / 2;
      const y1 = d.y + d.inn + th / 2;
      s.out += th;
      d.inn += th;
      const x0 = s.x + nodeW;
      const x1 = d.x;
      const mx = (x0 + x1) / 2;
      return { d: `M${x0} ${y0} C ${mx} ${y0}, ${mx} ${y1}, ${x1} ${y1}`, th, color: l.color, key: l.from + l.to, value: l.value };
    });
    return { pos, paths };
  }, [w, cols, links, height]);
  const [hi, setHi] = useState<string | null>(null);
  return (
    <div ref={ref} className="ch-wrap" style={{ width: width ?? "100%", height, position: "relative" }}>
      {layout && (
        <>
          <svg width={w} height={height} className="ch-svg">
            {layout.paths.map((p) => (
              <path key={p.key} d={p.d} onMouseEnter={() => setHi(p.key)} onMouseLeave={() => setHi(null)}
                style={{ fill: "none", stroke: p.color, strokeWidth: p.th, strokeOpacity: hi === null ? 0.42 : hi === p.key ? 0.7 : 0.15, transition: "stroke-opacity 150ms" }}>
                <title>{p.value}</title>
              </path>
            ))}
            {cols.flat().map((n) => (
              <rect key={n.id} x={layout.pos[n.id].x} y={layout.pos[n.id].y} width={nodeW} height={layout.pos[n.id].h} rx={2} style={{ fill: n.color }} />
            ))}
          </svg>
          {cols.map((col, ci) =>
            col.map((n) => {
              const p = layout.pos[n.id];
              const last = ci === cols.length - 1;
              return (
                <span key={n.id} className="ch-sankey-label" style={last ? { right: w - p.x + 6, top: p.y + p.h / 2 } : { left: p.x + nodeW + 6, top: p.y + p.h / 2 }}>
                  {n.label}
                </span>
              );
            }),
          )}
        </>
      )}
    </div>
  );
}

export function Treemap({ items, height = 120, width, colors = C }: { items: { label: string; value: number }[]; height?: number; width?: number; colors?: string[] }) {
  const [ref, w] = useWidth<HTMLDivElement>(width);
  const rects: { x: number; y: number; w: number; h: number; label: string; value: number; k: number }[] = [];
  const lay = (its: typeof items, x: number, y: number, ww: number, hh: number, vertical: boolean, k0: number) => {
    if (!its.length) return;
    if (its.length === 1) {
      rects.push({ x, y, w: ww, h: hh, label: its[0].label, value: its[0].value, k: k0 });
      return;
    }
    const total = its.reduce((a, i) => a + i.value, 0);
    const first = its[0].value / total;
    if (vertical) {
      rects.push({ x, y, w: ww * first, h: hh, label: its[0].label, value: its[0].value, k: k0 });
      lay(its.slice(1), x + ww * first, y, ww * (1 - first), hh, !vertical, k0 + 1);
    } else {
      rects.push({ x, y, w: ww, h: hh * first, label: its[0].label, value: its[0].value, k: k0 });
      lay(its.slice(1), x, y + hh * first, ww, hh * (1 - first), !vertical, k0 + 1);
    }
  };
  if (w) lay(items, 0, 0, w, height, true, 0);
  return (
    <div ref={ref} className="ch-wrap" style={{ width: width ?? "100%", height, position: "relative" }}>
      {rects.map((r) => (
        <div key={r.label} className="ch-tm" title={`${r.label} · ${r.value}`}
          style={{ left: r.x, top: r.y, width: Math.max(0, r.w - 2), height: Math.max(0, r.h - 2), background: colors[r.k % colors.length] }}>
          {r.w > 60 && r.h > 22 && (
            <>
              <b>{r.label}</b>
              <span>{r.value}</span>
            </>
          )}
        </div>
      ))}
    </div>
  );
}

export function Radar({ axes, series, size = 220, max = 1 }: { axes: string[]; series: { values: number[]; color: string; fill?: string; name?: string }[]; size?: number; max?: number }) {
  const cx = size / 2;
  const r = size / 2 - 28;
  const ang = (i: number) => -Math.PI / 2 + (2 * Math.PI * i) / axes.length;
  const poly = (f: (i: number) => number) => axes.map((_, i) => `${cx + r * f(i) * Math.cos(ang(i))},${cx + r * f(i) * Math.sin(ang(i))}`).join(" ");
  return (
    <div className="ch-radar" style={{ width: size, height: size }}>
      <svg width={size} height={size} className="ch-svg">
        {[0.25, 0.5, 0.75, 1].map((k) => (
          <polygon key={k} points={poly(() => k)} style={{ fill: "none", stroke: "var(--border)" }} />
        ))}
        {axes.map((_, i) => (
          <line key={i} x1={cx} y1={cx} x2={cx + r * Math.cos(ang(i))} y2={cx + r * Math.sin(ang(i))} style={{ stroke: "var(--border)" }} />
        ))}
        {series.map((s, k) => (
          <polygon key={k} points={poly((i) => s.values[i] / max)} className="anim-fade" style={{ fill: s.fill ?? "none", stroke: s.color, strokeWidth: 2, strokeLinejoin: "round" }} />
        ))}
      </svg>
      {axes.map((a, i) => (
        <span key={a} className="ch-radar-label" style={{ left: cx + (r + 16) * Math.cos(ang(i)), top: cx + (r + 12) * Math.sin(ang(i)) }}>
          {a}
        </span>
      ))}
    </div>
  );
}

export function Swimlane({ rows, labelWidth = 120 }: { rows: { label: ReactNode; segs: { from: number; to: number; tone: string; text?: string }[] }[]; labelWidth?: number }) {
  return (
    <div className="ch-swim">
      {rows.map((r, i) => (
        <div key={i} className="row">
          {labelWidth > 0 && <span className="ellipsis" style={{ width: labelWidth }}>{r.label}</span>}
          <span className="lane">
            {r.segs.map((s, j) => (
              <span key={j} className="seg" title={s.text} style={{ left: `${s.from}%`, width: `${s.to - s.from}%`, background: tc(s.tone) }}>
                {s.text}
              </span>
            ))}
          </span>
        </div>
      ))}
    </div>
  );
}

export function SmallMultiples({ items, cols = 6, height = 42 }: { items: { label: string; values: number[]; tone?: string; value?: string }[]; cols?: number; height?: number }) {
  return (
    <div className="ch-multiples" style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}>
      {items.map((it) => {
        const color = it.tone ? tc(it.tone) : "var(--c1)";
        const fill = it.tone === "danger" ? "var(--danger-soft)" : it.tone === "warn" ? "var(--warn-soft)" : "var(--c1s)";
        return (
          <div key={it.label} className="cell">
            <div className="head">
              <span className="muted">{it.label}</span>
              <b className="tnum" style={{ color }}>
                {it.value ?? Math.round(it.values[it.values.length - 1])}
              </b>
            </div>
            <Lines series={[{ values: it.values, color, fill }]} height={height} grid={1} hover={false} draw={false} />
          </div>
        );
      })}
    </div>
  );
}

/** Deterministic demo series (same generator as the design canvas). */
export function series(n: number, base: number, amp: number, period = 12, seed = 1, spike?: [number, number]) {
  return Array.from({ length: n }, (_, i) => {
    let v = base + amp * (0.5 + 0.5 * Math.sin((2 * Math.PI * i) / period - Math.PI / 2));
    v += (base * 0.08 * ((((i + 1) * 37 + seed * 17) % 11) - 5)) / 5;
    if (spike && i >= spike[0]) v *= spike[1];
    return Math.round(Math.max(0, v) * 1000) / 1000;
  });
}
