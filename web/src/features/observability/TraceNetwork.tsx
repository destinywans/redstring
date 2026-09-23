import { useId, useMemo, useRef, useState } from "react";
import { Maximize2, Minus, Plus, RotateCcw } from "../../lib/icons";

export type TraceEdge = {
  from: string;
  to: string;
  count: number;
  errors: number;
  traces: Set<string>;
};
export type TraceService = {
  name: string;
  spans: number;
  errors: number;
  traces: number;
};

/** A bounded, directed network built only from visible parent/child spans. */
export default function TraceNetwork({
  services,
  edges,
  center,
  onCenter,
  onService,
  onEdge,
}: {
  services: TraceService[];
  edges: TraceEdge[];
  center: string;
  onCenter: (service: string) => void;
  onService: (service: string) => void;
  onEdge: (edge: TraceEdge) => void;
}) {
  const marker = useId().replace(/:/g, "");
  const container = useRef<HTMLDivElement>(null);
  const drag = useRef<{ x: number; y: number; dx: number; dy: number } | null>(
    null,
  );
  const [view, setView] = useState({ x: 0, y: 0, scale: 1 });
  const [fullscreen, setFullscreen] = useState(false);
  const effectiveCenter = services.some((s) => s.name === center)
    ? center
    : services[0]?.name;
  const positions = useMemo(() => {
    const result = new Map<string, { x: number; y: number }>();
    if (!effectiveCenter) return result;
    result.set(effectiveCenter, { x: 500, y: 275 });
    const others = services.filter((s) => s.name !== effectiveCenter);
    // Angles follow a circle around the selected service, never a flat node grid.
    others.forEach((s, i) => {
      const angle =
        -Math.PI / 2 +
        (i * Math.PI * 2) / Math.max(1, others.length) +
        (others.length === 2 ? Math.PI / 3 : 0);
      result.set(s.name, {
        x: 500 + Math.cos(angle) * 305,
        y: 275 + Math.sin(angle) * 175,
      });
    });
    return result;
  }, [services, effectiveCenter]);
  const zoom = (factor: number) =>
    setView((v) => ({
      ...v,
      scale: Math.max(0.5, Math.min(2.5, v.scale * factor)),
    }));
  return (
    <div
      ref={container}
      className={`trace-network ${fullscreen ? "trace-network--expanded" : ""}`}
    >
      <header className="trace-network-toolbar">
        <div>
          <span className="trace-network-dot" />
          服务调用网络{" "}
          <span className="muted">
            {services.length} 个服务 · {edges.length} 组关系
          </span>
        </div>
        <div className="trace-network-tools" aria-label="拓扑画布工具">
          <button
            className="btn btn-ghost"
            onClick={() => zoom(1 / 1.2)}
            aria-label="缩小拓扑"
            title="缩小"
          >
            <Minus size={16} />
          </button>
          <span className="trace-network-zoom">
            {Math.round(view.scale * 100)}%
          </span>
          <button
            className="btn btn-ghost"
            onClick={() => zoom(1.2)}
            aria-label="放大拓扑"
            title="放大"
          >
            <Plus size={16} />
          </button>
          <button
            className="btn btn-ghost"
            onClick={() => setView({ x: 0, y: 0, scale: 1 })}
            title="适应画布"
          >
            <RotateCcw size={16} />
            适应画布
          </button>
          <button
            className="btn btn-ghost"
            aria-pressed={fullscreen}
            onClick={() => setFullscreen((v) => !v)}
          >
            <Maximize2 size={16} />
            {fullscreen ? "收起" : "展开"}
          </button>
        </div>
      </header>
      <svg
        className="trace-network-canvas"
        viewBox="0 0 1000 550"
        role="group"
        aria-label="服务调用拓扑，点击服务查看详情，拖动空白区域平移"
        onPointerDown={(event) => {
          if (
            event.button !== 0 ||
            (event.target as Element).closest('[role="button"]')
          )
            return;
          const rect = event.currentTarget.getBoundingClientRect();
          drag.current = {
            x: event.clientX,
            y: event.clientY,
            dx: view.x,
            dy: view.y,
          };
          event.currentTarget.setPointerCapture(event.pointerId);
          event.currentTarget.dataset.ratio = String(1000 / rect.width);
        }}
        onPointerMove={(event) => {
          if (!drag.current) return;
          const ratio = Number(event.currentTarget.dataset.ratio || 1);
          setView((v) => ({
            ...v,
            x: drag.current!.dx + (event.clientX - drag.current!.x) * ratio,
            y: drag.current!.dy + (event.clientY - drag.current!.y) * ratio,
          }));
        }}
        onPointerUp={() => {
          drag.current = null;
        }}
        onPointerCancel={() => {
          drag.current = null;
        }}
      >
        <defs>
          <marker
            id={marker}
            markerWidth="8"
            markerHeight="8"
            refX="7"
            refY="4"
            orient="auto"
          >
            <path d="M 0 0 L 8 4 L 0 8 z" fill="var(--muted)" />
          </marker>
        </defs>
        <g
          transform={`translate(${view.x} ${view.y}) translate(500 275) scale(${view.scale}) translate(-500 -275)`}
        >
          <circle cx="500" cy="275" r="190" className="trace-network-orbit" />
          {edges.map((edge) => {
            const from = positions.get(edge.from),
              to = positions.get(edge.to);
            if (!from || !to) return null;
            const dx = to.x - from.x,
              dy = to.y - from.y,
              length = Math.max(1, Math.hypot(dx, dy));
            const x1 = from.x + (dx / length) * 43,
              y1 = from.y + (dy / length) * 43;
            const x2 = to.x - (dx / length) * 49,
              y2 = to.y - (dy / length) * 49;
            const reciprocal = edges.some(
              (e) => e.from === edge.to && e.to === edge.from,
            );
            const bend = reciprocal ? 38 : 0;
            const mx = (x1 + x2) / 2 - (dy / length) * bend,
              my = (y1 + y2) / 2 + (dx / length) * bend;
            const path = `M ${x1} ${y1} Q ${mx} ${my} ${x2} ${y2}`;
            return (
              <g
                key={`${edge.from}:${edge.to}`}
                className={`trace-network-edge ${edge.errors ? "has-error" : ""}`}
                role="button"
                tabIndex={0}
                aria-label={`${edge.from} 调用 ${edge.to}，样本中 ${edge.count} 个调用片段，${edge.errors} 个错误片段`}
                onClick={() => onEdge(edge)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    onEdge(edge);
                  }
                }}
              >
                <path className="trace-network-edge-hit" d={path} />
                <path
                  className="trace-network-edge-line"
                  d={path}
                  markerEnd={`url(#${marker})`}
                />
                <rect
                  x={mx - 44}
                  y={my - 13}
                  width="88"
                  height="25"
                  rx="6"
                  className="trace-network-edge-label"
                />
                <text x={mx} y={my + 4} textAnchor="middle">
                  {edge.count} 次片段调用
                </text>
              </g>
            );
          })}
          {services.map((service) => {
            const point = positions.get(service.name)!;
            return (
              <g
                key={service.name}
                transform={`translate(${point.x} ${point.y})`}
                role="button"
                tabIndex={0}
                className={`trace-network-node ${service.name === effectiveCenter ? "is-center" : ""} ${service.errors ? "has-error" : ""}`}
                aria-label={`${service.name}，${service.traces} 条链路样本，查看服务详情`}
                onClick={() => onService(service.name)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    onService(service.name);
                  }
                }}
              >
                <circle className="trace-network-node-halo" r="47" />
                <circle className="trace-network-node-disc" r="37" />
                <path
                  d="M-12 -13 L0 -20 L12 -13 L12 3 L0 10 L-12 3 Z M-12 -13 L0 -6 L12 -13 M0 -6 L0 10"
                  className="trace-network-node-symbol"
                />
                <text
                  y="58"
                  textAnchor="middle"
                  className="trace-network-node-title"
                >
                  {service.name.length > 28
                    ? service.name.slice(0, 25) + "…"
                    : service.name}
                </text>
                <text
                  y="78"
                  textAnchor="middle"
                  className="trace-network-node-meta"
                >
                  {service.traces} 条样本
                  {service.errors ? ` · ${service.errors} 个错误片段` : ""}
                </text>
                <title>{service.name}</title>
              </g>
            );
          })}
        </g>
      </svg>
      <footer className="trace-network-footer">
        <span>连线表示样本中的真实调用方向 · 点击连线查看请求</span>
        <label>
          中心服务
          <select
            aria-label="中心服务"
            value={effectiveCenter || ""}
            onChange={(e) => {
              onCenter(e.target.value);
              setView({ x: 0, y: 0, scale: 1 });
            }}
          >
            {services.map((s) => (
              <option key={s.name} value={s.name}>
                {s.name}
              </option>
            ))}
          </select>
        </label>
      </footer>
    </div>
  );
}
