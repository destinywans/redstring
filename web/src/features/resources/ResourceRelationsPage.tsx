import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  ReactFlow,
  Background,
  Controls,
  Handle,
  Position,
  MarkerType,
  type NodeProps,
  type Node,
  type Edge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { request } from "../../lib/api";
import { useEnvironmentScope } from "../../lib/environmentScope";
import { ExternalLink, Network } from "../../lib/icons";
import ResourceDrawer, {
  ResourceFacts,
  ResourceIcon,
  ResourceOverlay,
} from "./ResourceDrawer";
import {
  relationKinds,
  resourceKindLabel,
  resourceTime,
  type Resource,
  type ResourceGraph,
  type ResourceList,
  type ResourceRelation,
} from "./types";
import "./resources.css";

type GraphResourceNode = Node<{ resource: Resource }, "resource">;
function ResourceNode({ data, selected }: NodeProps<GraphResourceNode>) {
  return (
    <div className={`resource-graph-node${selected ? " selected" : ""}`}>
      <Handle type="target" position={Position.Left} />
      <span className="resource-icon">
        <ResourceIcon kind={data.resource.kind} />
      </span>
      <span>
        <strong>{data.resource.name}</strong>
        <small>{resourceKindLabel(data.resource.kind)}</small>
      </span>
      <Handle type="source" position={Position.Right} />
    </div>
  );
}
const nodeTypes = { resource: ResourceNode };
/** Breadth-first columns keep dependencies left-to-right, including cyclic or disconnected data. */
function graphLayout(
  graph: ResourceGraph,
  rootId: string,
): GraphResourceNode[] {
  const ids = new Set(graph.nodes.map((node) => node.id));
  const neighbours = new Map<string, string[]>();
  const indegree = new Map<string, number>();
  for (const edge of graph.edges) {
    if (!ids.has(edge.source_resource_id) || !ids.has(edge.target_resource_id))
      continue;
    neighbours.set(edge.source_resource_id, [
      ...(neighbours.get(edge.source_resource_id) ?? []),
      edge.target_resource_id,
    ]);
    indegree.set(
      edge.target_resource_id,
      (indegree.get(edge.target_resource_id) ?? 0) + 1,
    );
  }
  const rank = new Map<string, number>();
  const roots =
    rootId && ids.has(rootId)
      ? [rootId]
      : graph.nodes
          .filter(
            (node) => node.kind === "application" || !indegree.has(node.id),
          )
          .map((node) => node.id);
  const queue = roots.map((id) => ({ id, level: 0 }));
  while (queue.length) {
    const item = queue.shift()!;
    if (rank.has(item.id)) continue;
    rank.set(item.id, item.level);
    for (const id of neighbours.get(item.id) ?? [])
      queue.push({ id, level: item.level + 1 });
  }
  for (const resource of graph.nodes)
    if (!rank.has(resource.id)) rank.set(resource.id, 0);
  const columns = new Map<number, Resource[]>();
  for (const resource of graph.nodes) {
    const level = rank.get(resource.id)!;
    columns.set(level, [...(columns.get(level) ?? []), resource]);
  }
  const maxRows = Math.max(
    1,
    ...[...columns.values()].map((column) => column.length),
  );
  return [...columns.entries()].flatMap(([level, resources]) =>
    resources.map((resource, index) => ({
      id: resource.id,
      type: "resource" as const,
      data: { resource },
      position: {
        x: level * 310,
        y: (index + (maxRows - resources.length) / 2) * 125,
      },
      ariaLabel: `${resource.name}，${resourceKindLabel(resource.kind)}`,
    })),
  );
}

export default function ResourceRelationsPage() {
  const scope = useEnvironmentScope();
  const [params, setParams] = useSearchParams();
  const [selection, setSelection] = useState<{
    type: "node" | "edge";
    id: string;
  } | null>(null);
  const appFilter =
    params.get("application_id") ||
    params.get("application") ||
    params.get("applications") ||
    "";
  const defaultScope = `${scope.key}:${appFilter}`;
  const [defaultRoot, setDefaultRoot] = useState<{
    scope: string;
    id: string;
  } | null>(null);
  const roots = useQuery({
    queryKey: ["resource-graph-roots", scope.key, appFilter],
    queryFn: ({ signal }) =>
      request<ResourceList>(
        `/resources?kind=application&page_size=100&environment_scope=${encodeURIComponent(scope.key || "all")}${appFilter ? `&applications=${encodeURIComponent(appFilter)}` : ""}`,
        { signal },
      ),
  });
  const searching =
    !!params.get("q") || params.getAll("resource_kind").length > 0;
  const rootId = searching
    ? ""
    : params.get("root_id") ||
      (defaultRoot?.scope === defaultScope ? defaultRoot.id : "");
  const query = new URLSearchParams();
  for (const key of [
    "q",
    "depth",
    "kind",
    "resource_kind",
    "application_id",
    "applications",
  ])
    for (const value of params.getAll(key)) query.append(key, value);
  query.set("environment_scope", scope.key || "all");
  if (appFilter) query.set("application_id", appFilter);
  if (rootId) query.set("root_id", rootId);
  if (!query.has("depth")) query.set("depth", "2");
  const graph = useQuery({
    queryKey: ["resource-relations", query.toString()],
    queryFn: ({ signal }) =>
      request<ResourceGraph>(`/resource-relations?${query}`, { signal }),
    enabled: !!params.get("root_id") || !roots.isPending,
  });
  useEffect(() => {
    if (rootId || searching || !graph.data || params.get("root_id")) return;
    const outgoing = new Set(
      graph.data.edges.map((edge) => edge.source_resource_id),
    );
    const connectedApplication = graph.data.nodes.find(
      (node) => node.kind === "application" && outgoing.has(node.id),
    );
    if (connectedApplication)
      setDefaultRoot({ scope: defaultScope, id: connectedApplication.id });
  }, [graph.data, rootId, searching, params, defaultScope]);
  useEffect(() => setSelection(null), [scope.key, rootId]);
  const change = (key: string, values: string[]) => {
    const next = new URLSearchParams(params);
    next.delete(key);
    if (key === "root_id") {
      next.delete("q");
      next.delete("resource_kind");
    }
    for (const value of values) if (value) next.append(key, value);
    setSelection(null);
    setParams(next, { replace: key === "q" });
  };
  const nodes = useMemo(
    () =>
      graph.data
        ? graphLayout(graph.data, rootId).map((node) => ({
            ...node,
            selected: selection?.type === "node" && selection.id === node.id,
          }))
        : [],
    [graph.data, rootId, selection],
  );
  const edges: Edge[] = useMemo(
    () =>
      graph.data?.edges.map((edge) => ({
        id: edge.id,
        source: edge.source_resource_id,
        target: edge.target_resource_id,
        label: relationKinds[edge.kind] ?? edge.kind,
        type: "smoothstep",
        selected: selection?.type === "edge" && selection.id === edge.id,
        focusable: true,
        ariaLabel: `关系：${relationKinds[edge.kind] ?? edge.kind}`,
        style: {
          stroke: selection?.id === edge.id ? "var(--accent)" : "var(--muted)",
          strokeWidth: selection?.id === edge.id ? 2 : 1.4,
          strokeDasharray: edge.provenance === "declared" ? "6 5" : undefined,
        },
        labelStyle: { fill: "var(--muted)", fontSize: 12 },
        labelBgStyle: { fill: "var(--panel)" },
        markerEnd: {
          type: MarkerType.ArrowClosed,
          color: selection?.id === edge.id ? "var(--accent)" : "var(--muted)",
        },
      })) ?? [],
    [graph.data, selection],
  );
  const chosenEdge =
    selection?.type === "edge"
      ? graph.data?.edges.find((edge) => edge.id === selection.id)
      : undefined;
  const rootOptions = [
    ...new Map(
      [...(roots.data?.items ?? []), ...(graph.data?.nodes ?? [])].map(
        (resource) => [
          resource.id,
          { value: resource.id, label: resource.name },
        ],
      ),
    ).values(),
  ];
  return (
    <section className="resource-page resource-relations-page">
      <header className="resource-page-heading">
        <div>
          <h1>资源关系</h1>
          <p>查看部署、归属与依赖，理解应用和资源之间的关联。</p>
        </div>
        {graph.data?.nodes.some((node) => node.is_mock) && (
          <span className="resource-source is-mock">包含示例数据</span>
        )}
      </header>
      <FilterBar label="资源关系搜索与筛选">
        <FilterSearch
          label="搜索关系资源"
          value={params.get("q") ?? ""}
          onChange={(value) => change("q", value ? [value] : [])}
          onSubmit={() => {}}
          placeholder="搜索资源名称、标签或关键词"
          showSubmit={false}
        />
        <FilterSelect
          id="relation-root"
          label="中心资源"
          emptyLabel="中心资源"
          options={rootOptions}
          values={rootId ? [rootId] : []}
          onChange={(values) => change("root_id", values)}
          searchable
          showLabel={false}
          includeEmptyOption={false}
          popoverWidth={260}
        />
        <FilterSelect
          id="relation-kind"
          label="关系类型"
          emptyLabel="关系类型"
          options={Object.entries(relationKinds).map(([value, label]) => ({
            value,
            label,
          }))}
          values={params.getAll("kind")}
          onChange={(values) => change("kind", values)}
          multiple
          showLabel={false}
          popoverWidth={196}
        />
        <FilterSelect
          id="relation-depth"
          label="关系层级"
          emptyLabel="2 跳"
          options={[1, 2, 3].map((value) => ({
            value: String(value),
            label: `${value} 跳`,
          }))}
          values={[params.get("depth") || "2"]}
          onChange={(values) => change("depth", values)}
          showLabel={false}
          includeEmptyOption={false}
          popoverWidth={140}
        />
        <FilterSelect
          id="relation-resource-kind"
          label="资源类型"
          emptyLabel="资源类型"
          options={
            roots.data?.facets.kinds.map((option) => ({
              ...option,
              label: resourceKindLabel(option.value),
            })) ?? []
          }
          values={params.getAll("resource_kind")}
          onChange={(values) => change("resource_kind", values)}
          multiple
          showLabel={false}
          popoverWidth={208}
        />
      </FilterBar>
      <div className="resource-graph-panel">
        {graph.isPending ? (
          <div className="resource-empty">正在读取资源关系…</div>
        ) : graph.isError ? (
          <div className="resource-error" role="alert">
            {graph.error.message}
            <button className="btn" onClick={() => void graph.refetch()}>
              重新加载
            </button>
          </div>
        ) : !nodes.length ? (
          <div className="resource-empty">
            当前范围没有匹配的资源关系，请调整筛选条件。
          </div>
        ) : (
          <ReactFlow
            key={query.toString()}
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={(changes) => {
              const change = changes.find(
                (item) => item.type === "select" && item.selected,
              );
              if (change && change.type === "select")
                setSelection({ type: "node", id: change.id });
            }}
            onEdgesChange={(changes) => {
              const change = changes.find(
                (item) => item.type === "select" && item.selected,
              );
              if (change && change.type === "select")
                setSelection({ type: "edge", id: change.id });
            }}
            deleteKeyCode={null}
            nodesDraggable={false}
            nodesConnectable={false}
            edgesReconnectable={false}
            fitView
            fitViewOptions={{ padding: 0.22, maxZoom: 1 }}
            minZoom={0.2}
            maxZoom={1.7}
            onNodeClick={(_, node) =>
              setSelection({ type: "node", id: node.id })
            }
            onEdgeClick={(_, edge) =>
              setSelection({ type: "edge", id: edge.id })
            }
            proOptions={{ hideAttribution: false }}
          >
            <Background gap={24} size={0.6} color="var(--border)" />
            <Controls position="bottom-right" showInteractive={false} />
          </ReactFlow>
        )}
        <div className="resource-graph-legend">
          <span>
            <i className="declared" /> 声明关系
          </span>
          <span>
            <i /> 发现关系
          </span>
          <span>
            <i className="selected" /> 已选中
          </span>
        </div>
        {graph.data?.truncated && (
          <p className="resource-graph-limit">
            关系较多，已显示部分资源。请选择中心资源或减少层级。
          </p>
        )}
      </div>
      {selection?.type === "node" && (
        <ResourceDrawer
          resourceId={selection.id}
          onClose={() => setSelection(null)}
          onSelect={(id) => setSelection({ type: "node", id })}
        />
      )}
      {chosenEdge && graph.data && (
        <RelationDrawer
          edge={chosenEdge}
          resources={graph.data.nodes}
          onClose={() => setSelection(null)}
          onSelect={(id) => setSelection({ type: "node", id })}
        />
      )}
    </section>
  );
}
function RelationDrawer({
  edge,
  resources,
  onClose,
  onSelect,
}: {
  edge: ResourceRelation;
  resources: Resource[];
  onClose: () => void;
  onSelect: (id: string) => void;
}) {
  const scope = useEnvironmentScope();
  const source = resources.find(
    (resource) => resource.id === edge.source_resource_id,
  );
  const target = resources.find(
    (resource) => resource.id === edge.target_resource_id,
  );
  return (
    <ResourceOverlay
      title={`${relationKinds[edge.kind] ?? edge.kind}关系`}
      subtitle={`${source?.name ?? "来源资源"} → ${target?.name ?? "目标资源"}`}
      icon={<Network size={22} />}
      onClose={onClose}
    >
      <div className="resource-overlay-body">
        {edge.is_mock && (
          <div className="resource-source-note">示例关系 · 用于界面预览</div>
        )}
        <section className="resource-detail-section">
          <h3>概览</h3>
          <ResourceFacts
            items={[
              [
                "源资源",
                source ? (
                  <button
                    className="resource-text-link"
                    onClick={() => onSelect(source.id)}
                  >
                    {source.name}
                  </button>
                ) : (
                  "—"
                ),
              ],
              [
                "目标资源",
                target ? (
                  <button
                    className="resource-text-link"
                    onClick={() => onSelect(target.id)}
                  >
                    {target.name}
                  </button>
                ) : (
                  "—"
                ),
              ],
              ["关系类型", relationKinds[edge.kind] ?? edge.kind],
              [
                "关系来源",
                edge.provenance === "declared" ? "声明关系" : "发现关系",
              ],
              ["数据来源", edge.is_mock ? "示例数据" : edge.source_kind],
              ["最后更新", resourceTime(edge.updated_at)],
              ["描述", edge.description || "暂无描述"],
            ]}
          />
        </section>
        <section className="resource-detail-section">
          <h3>相关资源</h3>
          {[source, target]
            .filter((resource): resource is Resource => !!resource)
            .map((resource) => (
              <button
                key={resource.id}
                className="resource-related-row resource-related-button"
                onClick={() => onSelect(resource.id)}
              >
                <span className="resource-icon">
                  <ResourceIcon kind={resource.kind} />
                </span>
                <div>
                  <strong>{resource.name}</strong>
                  <p>{resourceKindLabel(resource.kind)}</p>
                </div>
              </button>
            ))}
        </section>
        <Link
          className="resource-shortcut"
          to={scope.withScope(
            `/observability/topology${source?.applications[0] ? `?application=${encodeURIComponent(source.applications[0].id)}` : ""}`,
          )}
        >
          <Network size={17} /> 查看调用拓扑 <ExternalLink size={14} />
        </Link>
        <p className="resource-muted">
          调用拓扑展示观测到的请求流量，与这里的资源依赖分别维护。
        </p>
      </div>
    </ResourceOverlay>
  );
}
