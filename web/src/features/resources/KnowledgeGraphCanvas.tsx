import { useEffect, useRef, useState } from "react";
import { Graph, type GraphData } from "@antv/g6";
import { renderToStaticMarkup } from "react-dom/server";
import { ResourceIcon } from "./ResourceDrawer";
import { Layers, Maximize2, Minus, Plus, Target } from "../../lib/icons";
import type { KnowledgeEdge, KnowledgeNode } from "./KnowledgeGraph.types";
import { placeKnowledgeNodes } from "./KnowledgeGraph.layout";

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
  const hoveredNode = useRef("");
  const hoveredEdge = useRef("");
  const updateHighlight = useRef<() => void>(() => {});
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
        style: { opacity: 1 },
        state: {
          selected: {
            opacity: 1,
            lineWidth: 3,
            stroke: "#7864db",
            halo: true,
            haloStroke: "#7864db",
            haloLineWidth: 8,
            haloStrokeOpacity: 0.12,
          },
          active: { opacity: 1, lineWidth: 2.5 },
        },
      },
      edge: {
        type: "quadratic",
        style: {
          opacity: 1,
          endArrow: true,
          endArrowSize: 6,
          lineWidth: 1.2,
          label: false,
          curveOffset: 14,
        },
        state: {
          related: { opacity: 1, stroke: "#8874d7", lineWidth: 1.9 },
          active: {
            opacity: 1,
            stroke: "#7864db",
            lineWidth: 2.2,
            label: true,
          },
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
    // Emphasize the inspected relationship without washing out the rest of the map.
    // Read the latest refs when applying state, including after asynchronous redraws.
    const highlight = () => {
      if (graph.current !== instance) return;
      const current = callbacks.current;
      const ids = new Set(current.nodes.map((node) => node.id));
      const focus = ids.has(hoveredNode.current)
        ? hoveredNode.current
        : ids.has(current.selectedId)
          ? current.selectedId
          : "";
      const edgeFocus = current.edges.find(
        (edge) => edge.id === hoveredEdge.current,
      );
      const adjacent = new Set(focus ? [focus] : []);
      const connected = new Set<string>();
      current.edges.forEach((edge) => {
        if (
          (focus && (edge.source === focus || edge.target === focus)) ||
          edge === edgeFocus
        ) {
          adjacent.add(edge.source);
          adjacent.add(edge.target);
          connected.add(edge.id);
        }
      });
      const states: Record<string, string[]> = {};
      for (const node of current.nodes) {
        states[node.id] = [
          adjacent.has(node.id) ? "active" : "",
          node.id === current.selectedId ? "selected" : "",
        ].filter(Boolean);
      }
      for (const edge of current.edges) {
        states[edge.id] =
          edge === edgeFocus
            ? ["active"]
            : connected.has(edge.id)
              ? ["related"]
              : [];
      }
      void instance.setElementState(states, false).catch(() => {});
    };
    updateHighlight.current = highlight;
    instance.on("node:click", (event) =>
      callbacks.current.onNode(idFrom(event)),
    );
    instance.on("edge:click", (event) =>
      callbacks.current.onEdge(idFrom(event)),
    );
    instance.on("canvas:click", () => callbacks.current.onCanvas());
    instance.on("node:pointerenter", (event) => {
      hoveredNode.current = idFrom(event);
      hoveredEdge.current = "";
      highlight();
    });
    instance.on("node:pointerleave", () => {
      hoveredNode.current = "";
      highlight();
    });
    instance.on("edge:pointerenter", (event) => {
      hoveredEdge.current = idFrom(event);
      hoveredNode.current = "";
      highlight();
    });
    instance.on("edge:pointerleave", () => {
      hoveredEdge.current = "";
      highlight();
    });
    instance.on("canvas:pointerenter", () => {
      hoveredNode.current = "";
      hoveredEdge.current = "";
      highlight();
    });
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
          hoveredNode.current = "";
          hoveredEdge.current = "";
          activeKey.current = viewKey;
          needsFit.current = true;
        } else
          for (const node of instance.getNodeData()) {
            const [x, y] = instance.getElementPosition(node.id);
            positions.current.set(node.id, [x, y]);
          }
        placeKnowledgeNodes(
          nodes,
          edges,
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
              labelFontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans CJK SC", "Noto Sans SC", sans-serif',
              labelFontSize: 14,
              labelFontWeight: 400,
              labelBackground: true,
              labelBackgroundFill: dark ? "#252737" : "#ffffff",
              labelPadding: [2, 3],
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
        updateHighlight.current();
        if (needsFit.current && nodes.length) {
          await instance.fitView({ when: "always", direction: "both" }, false);
          // Keep the complete network visible initially. A forced lower zoom
          // bound clipped the outer branches of radial layouts.
          if (instance.getZoom() > 1) await instance.zoomTo(1, false);
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
    void queue.current.then(() => updateHighlight.current()).catch(() => {});
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
        onPointerLeave={() => {
          hoveredNode.current = "";
          hoveredEdge.current = "";
          updateHighlight.current();
        }}
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
