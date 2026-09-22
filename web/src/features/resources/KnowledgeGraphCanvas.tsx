import { useEffect, useRef, useState } from "react";
import { Graph, type GraphData } from "@antv/g6";
import { renderToStaticMarkup } from "react-dom/server";
import { ResourceIcon } from "./ResourceDrawer";
import { Layers, Maximize2, Minus, Plus, Target } from "../../lib/icons";
import type { KnowledgeEdge, KnowledgeNode } from "./KnowledgeGraph.types";

const palette = [
  "#7363cf",
  "#438eae",
  "#518d78",
  "#b28343",
  "#9571a9",
  "#5684bd",
];
export function knowledgeColor(kind: string) {
  const kinds = [
    "cluster",
    "node",
    "namespace",
    "deployment",
    "host",
    "network",
    "storage",
    "mysql",
    "redis",
    "kafka",
  ];
  return palette[Math.max(0, kinds.indexOf(kind)) % palette.length];
}
function iconSource(node: KnowledgeNode) {
  const icon = node.group ? (
    <Layers size={25} color={knowledgeColor(node.kind)} />
  ) : (
    <ResourceIcon kind={node.kind} />
  );
  const svg = renderToStaticMarkup(icon).replace(
    /currentColor/g,
    knowledgeColor(node.kind),
  );
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
}

function groupLabelLine(value: string) {
  let units = 0;
  let text = "";
  for (const char of value) {
    units += /[\u0000-\u007f]/.test(char) ? 0.55 : 1;
    if (units > 12) return `${text}…`;
    text += char;
  }
  return text;
}

/** A viewport-shaped lattice keeps labels readable. Focused views fill its central rings first;
 * later batches only claim vacant cells around their anchor, never moving an existing node. */
function placeNodes(
  nodes: KnowledgeNode[],
  positions: Map<string, [number, number]>,
  rootId: string,
  anchorId: string,
  width: number,
  height: number,
) {
  const active = new Set(nodes.map((node) => node.id));
  for (const id of positions.keys()) if (!active.has(id)) positions.delete(id);
  const columnGap = 180;
  const rowGap = nodes.some((node) => node.group) ? 126 : 108;
  if (!positions.size && nodes.length) {
    let columns = Math.max(
      1,
      Math.min(
        7,
        Math.round(
          Math.sqrt(
            (nodes.length * Math.max(1, width / height) * rowGap) / columnGap,
          ),
        ),
      ),
    );
    let rows = Math.ceil(nodes.length / columns);
    if (rootId) {
      if (columns % 2 === 0) columns++;
      rows = Math.ceil(nodes.length / columns);
      if (rows % 2 === 0) rows++;
    }
    const cells: [number, number][] = [];
    for (let row = 0; row < rows; row++)
      for (let column = 0; column < columns; column++) {
        cells.push([
          (column - (columns - 1) / 2) * columnGap,
          (row - (rows - 1) / 2) * rowGap,
        ]);
      }
    if (rootId) {
      positions.set(rootId, [0, 0]);
      cells.sort(
        (a, b) =>
          Math.hypot(a[0] / columnGap, a[1] / rowGap) -
          Math.hypot(b[0] / columnGap, b[1] / rowGap),
      );
    }
    for (const node of nodes) {
      if (positions.has(node.id)) continue;
      let cell = cells.shift()!;
      if (rootId && cell[0] === 0 && cell[1] === 0) cell = cells.shift()!;
      positions.set(node.id, cell);
    }
    return;
  }
  const anchor = positions.get(anchorId) ?? positions.get(rootId) ?? [0, 0];
  const candidates: [number, number][] = [];
  for (let ring = 1; ring <= 20; ring++) {
    for (let y = -ring; y <= ring; y++)
      for (let x = -ring; x <= ring; x++) {
        if (Math.max(Math.abs(x), Math.abs(y)) === ring)
          candidates.push([anchor[0] + x * columnGap, anchor[1] + y * rowGap]);
      }
  }
  for (const node of nodes.filter((node) => !positions.has(node.id))) {
    const index = candidates.findIndex(([x, y]) =>
      [...positions.values()].every(
        ([px, py]) => Math.abs(px - x) >= 170 || Math.abs(py - y) >= rowGap - 8,
      ),
    );
    const point = candidates.splice(Math.max(0, index), 1)[0];
    positions.set(node.id, point);
  }
}

export default function KnowledgeGraphCanvas({
  nodes,
  edges,
  rootId,
  anchorId,
  viewKey,
  selectedId,
  onNode,
  onKeyboardNode,
  onEdge,
  onCanvas,
  children,
}: {
  nodes: KnowledgeNode[];
  edges: KnowledgeEdge[];
  rootId: string;
  anchorId: string;
  viewKey: string;
  selectedId: string;
  onNode: (id: string) => void;
  onKeyboardNode: (id: string) => void;
  onEdge: (id: string) => void;
  onCanvas: () => void;
  children?: React.ReactNode;
}) {
  const container = useRef<HTMLDivElement>(null);
  const graph = useRef<Graph | null>(null);
  const positions = useRef(new Map<string, [number, number]>());
  const queue = useRef(Promise.resolve());
  const callbacks = useRef({
    onNode,
    onEdge,
    onCanvas,
    nodes,
    edges,
    selectedId,
  });
  callbacks.current = { onNode, onEdge, onCanvas, nodes, edges, selectedId };
  const [zoom, setZoom] = useState(100);
  const [error, setError] = useState("");
  const [fullscreen, setFullscreen] = useState(false);
  const [dark, setDark] = useState(
    document.documentElement.classList.contains("dark") ||
      document.documentElement.dataset.theme === "dark",
  );
  const activeKey = useRef("");
  const needsFit = useRef(true);

  useEffect(() => {
    const observer = new MutationObserver(() =>
      setDark(
        document.documentElement.classList.contains("dark") ||
          document.documentElement.dataset.theme === "dark",
      ),
    );
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class", "data-theme"],
    });
    const updateFullscreen = () => setFullscreen(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", updateFullscreen);
    return () => {
      observer.disconnect();
      document.removeEventListener("fullscreenchange", updateFullscreen);
    };
  }, []);
  useEffect(() => {
    if (!container.current) return;
    const instance = new Graph({
      container: container.current,
      animation: false,
      autoResize: true,
      padding: [26, 26, 30, 26],
      zoomRange: [0.25, 2],
      node: {
        type: "circle",
        state: {
          selected: {
            lineWidth: 3,
            stroke: "#7864db",
            halo: true,
            haloStroke: "#7864db",
            haloLineWidth: 8,
            haloStrokeOpacity: 0.12,
          },
          active: { lineWidth: 2.5 },
          dimmed: { opacity: 0.22 },
        },
      },
      edge: {
        type: "quadratic",
        style: {
          endArrow: true,
          endArrowSize: 6,
          lineWidth: 1.2,
          label: false,
          curveOffset: 14,
        },
        state: {
          active: { stroke: "#8874d7", lineWidth: 2, label: true },
          dimmed: { opacity: 0.12 },
        },
      },
      behaviors: ["drag-canvas", "zoom-canvas", "drag-element"],
      plugins: [
        {
          type: "tooltip",
          trigger: "hover",
          getContent: async (_event: unknown, items: { id?: string }[]) => {
            const id = items[0]?.id;
            const node = callbacks.current.nodes.find((item) => item.id === id);
            const edge = callbacks.current.edges.find((item) => item.id === id);
            const content = document.createElement("div");
            content.className = "knowledge-tooltip";
            content.textContent = node
              ? `${node.name} · ${node.subtitle}${node.group ? ` · ${node.count} 项资源` : ""}`
              : (edge?.label ?? "");
            return content;
          },
        },
      ],
    });
    graph.current = instance;
    const idFrom = (event: unknown) =>
      (event as { target?: { id?: string } }).target?.id ?? "";
    const highlight = (id: string) => {
      const current = callbacks.current;
      const adjacent = new Set([id]);
      const connected = current.edges.filter(
        (edge) => edge.source === id || edge.target === id,
      );
      connected.forEach((edge) => {
        adjacent.add(edge.source);
        adjacent.add(edge.target);
      });
      const states: Record<string, string[]> = {};
      for (const node of current.nodes)
        states[node.id] = [
          node.id === current.selectedId ? "selected" : "",
          id ? (adjacent.has(node.id) ? "active" : "dimmed") : "",
        ].filter(Boolean);
      for (const edge of current.edges)
        states[edge.id] = id
          ? [connected.includes(edge) ? "active" : "dimmed"]
          : [];
      void instance.setElementState(states, false).catch(() => {});
    };
    instance.on("node:click", (event) =>
      callbacks.current.onNode(idFrom(event)),
    );
    instance.on("edge:click", (event) =>
      callbacks.current.onEdge(idFrom(event)),
    );
    instance.on("canvas:click", () => callbacks.current.onCanvas());
    instance.on("node:pointerenter", (event) => highlight(idFrom(event)));
    instance.on("node:pointerleave", () =>
      highlight(callbacks.current.selectedId),
    );
    instance.on("edge:pointerenter", (event) => {
      const id = idFrom(event);
      if (id)
        void instance.setElementState(id, ["active"], false).catch(() => {});
    });
    instance.on("edge:pointerleave", () =>
      highlight(callbacks.current.selectedId),
    );
    instance.on("aftertransform", () =>
      setZoom(Math.round(instance.getZoom() * 100)),
    );
    return () => {
      graph.current = null;
      instance.destroy();
    };
  }, []);

  useEffect(() => {
    const instance = graph.current;
    if (!instance) return;
    let cancelled = false;
    queue.current = queue.current
      .catch(() => {})
      .then(async () => {
        if (cancelled || graph.current !== instance) return;
        const reset = activeKey.current !== viewKey;
        if (reset) {
          positions.current.clear();
          activeKey.current = viewKey;
          needsFit.current = true;
        } else
          for (const node of instance.getNodeData()) {
            const [x, y] = instance.getElementPosition(node.id);
            positions.current.set(node.id, [x, y]);
          }
        placeNodes(
          nodes,
          positions.current,
          rootId,
          anchorId,
          container.current?.clientWidth ?? 1000,
          container.current?.clientHeight ?? 510,
        );
        const data: GraphData = {
          nodes: nodes.map((node) => ({
            id: node.id,
            style: {
              x: positions.current.get(node.id)![0],
              y: positions.current.get(node.id)![1],
              size: node.id === rootId ? 66 : node.group ? 60 : 46,
              fill: dark ? "#252737" : "#ffffff",
              stroke: knowledgeColor(node.kind),
              lineWidth: 1.5,
              iconSrc: iconSource(node),
              iconWidth: node.group ? 25 : 21,
              iconHeight: node.group ? 25 : 21,
              labelText: node.group
                ? `${groupLabelLine(node.name)}\n${groupLabelLine(node.subtitle)}\n${node.count} 项资源`
                : node.name,
              labelPlacement: "bottom",
              labelOffsetY: 9,
              labelFill: dark ? "#e0e2ee" : "#394253",
              labelFontFamily: "MiSans, sans-serif",
              labelFontSize: 13,
              labelFontWeight: 400,
              labelMaxWidth: 170,
              labelWordWrap: true,
              labelMaxLines: node.group ? 3 : 2,
              cursor: "pointer",
            },
          })),
          edges: edges.map((edge) => ({
            id: edge.id,
            source: edge.source,
            target: edge.target,
            style: {
              stroke: dark ? "#62677e" : "#c1c7d6",
              lineDash: edge.declared ? [5, 4] : undefined,
              labelText: edge.label,
              labelFontSize: 12,
              labelFill: dark ? "#e0e2ee" : "#5f587c",
              labelBackground: true,
              labelBackgroundFill: dark ? "#252737" : "#ffffff",
              labelPadding: [3, 5],
              cursor: "pointer",
            },
          })),
        };
        instance.setData(data);
        await instance.render();
        if (needsFit.current && nodes.length) {
          await instance.fitView({ when: "always", direction: "both" }, false);
          if (instance.getZoom() < 0.85) await instance.zoomTo(0.85, false);
          needsFit.current = false;
        }
        if (!cancelled) {
          setError("");
          setZoom(Math.round(instance.getZoom() * 100));
        }
      })
      .catch((cause) => {
        if (!cancelled)
          setError(cause instanceof Error ? cause.message : "图谱暂时无法显示");
      });
    return () => {
      cancelled = true;
    };
  }, [nodes, edges, rootId, anchorId, viewKey, dark]);

  useEffect(() => {
    const instance = graph.current;
    if (!instance) return;
    void queue.current
      .then(async () => {
        if (graph.current !== instance) return;
        const neighbors = new Set([selectedId]);
        edges
          .filter(
            (edge) => edge.source === selectedId || edge.target === selectedId,
          )
          .forEach((edge) => {
            neighbors.add(edge.source);
            neighbors.add(edge.target);
          });
        const states: Record<string, string[]> = {};
        nodes.forEach((node) => {
          states[node.id] = selectedId
            ? [
                node.id === selectedId
                  ? "selected"
                  : neighbors.has(node.id)
                    ? "active"
                    : "dimmed",
              ]
            : [];
        });
        edges.forEach((edge) => {
          states[edge.id] = selectedId
            ? [
                edge.source === selectedId || edge.target === selectedId
                  ? "active"
                  : "dimmed",
              ]
            : [];
        });
        await instance.setElementState(states, false);
      })
      .catch(() => {});
  }, [selectedId, nodes, edges]);

  const changeZoom = (amount: number) => {
    const instance = graph.current;
    if (instance)
      void instance.zoomTo(
        Math.max(0.25, Math.min(2, instance.getZoom() + amount)),
        false,
      );
  };
  const toggleFullscreen = async () => {
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else await document.documentElement.requestFullscreen();
    } catch {
      setError("当前浏览器无法进入全屏，请使用浏览器的全屏功能。");
    }
  };
  return (
    <section
      className={`knowledge-graph${fullscreen ? " is-fullscreen" : ""}`}
      aria-label="资源知识图谱"
    >
      <div className="knowledge-toolbar">
        <div className="knowledge-toolbar-context">{children}</div>
        <div className="knowledge-tools">
          <button
            className="knowledge-icon-button"
            title="缩小"
            aria-label="缩小图谱"
            onClick={() => changeZoom(-0.15)}
            disabled={zoom <= 25}
          >
            <Minus size={16} />
          </button>
          <button
            className="knowledge-zoom"
            title="恢复 100%"
            onClick={() => void graph.current?.zoomTo(1, false)}
          >
            {zoom}%
          </button>
          <button
            className="knowledge-icon-button"
            title="放大"
            aria-label="放大图谱"
            onClick={() => changeZoom(0.15)}
            disabled={zoom >= 200}
          >
            <Plus size={16} />
          </button>
          <span className="knowledge-tool-separator" />
          <button
            className="knowledge-tool-button"
            onClick={() =>
              void graph.current?.fitView({ when: "always" }, false)
            }
          >
            <Target size={16} />
            适应画布
          </button>
          <button
            className="knowledge-tool-button"
            onClick={() => void toggleFullscreen()}
          >
            <Maximize2 size={16} />
            {fullscreen ? "退出全屏" : "全屏"}
          </button>
        </div>
      </div>
      <div
        className="knowledge-canvas"
        ref={container}
        tabIndex={0}
        role="group"
        aria-label="图谱画布，使用方向键选择资源，回车打开，拖动平移或滚轮缩放"
        onKeyDown={(event) => {
          if (!nodes.length) return;
          const index = nodes.findIndex((node) => node.id === selectedId);
          if (
            ["ArrowRight", "ArrowDown", "ArrowLeft", "ArrowUp"].includes(
              event.key,
            )
          ) {
            event.preventDefault();
            const step =
              event.key === "ArrowRight" || event.key === "ArrowDown" ? 1 : -1;
            onKeyboardNode(
              nodes[(index + step + nodes.length) % nodes.length].id,
            );
          } else if (event.key === "Escape") onCanvas();
          else if (event.key === "Enter" && selectedId) {
            event.preventDefault();
            onNode(selectedId);
          }
        }}
      />
      {error && (
        <div className="knowledge-canvas-error" role="alert">
          {error}
        </div>
      )}
    </section>
  );
}
