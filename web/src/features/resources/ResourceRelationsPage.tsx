import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/patterns/ListPage";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { ApiError, request } from "../../lib/api";
import { useEnvironmentScope } from "../../lib/environmentScope";
import {
  ArrowLeft,
  ChevronRight,
  ExternalLink,
  Layers,
  Network,
  RefreshCw,
  Search,
  X,
} from "../../lib/icons";
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
  type ResourceList,
  type ResourceRelation,
} from "./types";
import KnowledgeGraphCanvas, { knowledgeColor } from "./KnowledgeGraphCanvas";
import type {
  KnowledgeGraphResponse,
  KnowledgeNode,
  KnowledgeEdge,
} from "./KnowledgeGraph.types";
import "./resources.css";
import "./KnowledgeGraph.css";

function isRuntime(resource: Resource) {
  return (
    resource.kind === "pod" ||
    resource.kind === "container" ||
    (resource.kind === "host" &&
      String(resource.attributes.resource_type ?? "").toLowerCase() ===
        "container")
  );
}
function searchableResource(resource: Resource, includeRuntime: boolean) {
  return (
    !["event", "kubernetes_event"].includes(resource.kind) &&
    (includeRuntime || !isRuntime(resource))
  );
}
const batchSize = 20;
type Expansion = { pages: KnowledgeGraphResponse[]; anchor: string };

export default function ResourceRelationsPage() {
  const scope = useEnvironmentScope();
  const queryClient = useQueryClient();
  const [params, setParams] = useSearchParams();
  const [search, setSearch] = useState(params.get("q") ?? "");
  const [queryText, setQueryText] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const [selectedId, setSelectedId] = useState("");
  const [detailId, setDetailId] = useState("");
  const [edgeId, setEdgeId] = useState("");
  const [expansions, setExpansions] = useState<Record<string, Expansion>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [anchorId, setAnchorId] = useState("");
  const controller = useRef<AbortController | null>(null);
  const refreshing = useRef(false);
  const refreshCallback = useRef<() => void>(() => {});
  const rootId = params.get("root_id") ?? "";
  const groupId = rootId ? "" : (params.get("group_id") ?? "");
  const direction = params.get("direction") ?? "both";
  const includeRuntime = params.get("include_runtime") === "true";
  const app =
    params.get("application_id") ||
    params.get("application") ||
    params.get("applications") ||
    "";
  const base = new URLSearchParams({ environment_scope: scope.key || "all" });
  if (app) base.set("application_id", app);
  if (includeRuntime) base.set("include_runtime", "true");
  for (const key of ["source_kind", "resource_kind", "kind"])
    for (const value of params.getAll(key)) base.append(key, value);
  const baseKey = base.toString();
  const viewKey = `${baseKey}:${rootId}:${groupId}:${direction}`;
  const overview = useQuery({
    queryKey: ["knowledge-overview", baseKey],
    queryFn: ({ signal }) =>
      request<KnowledgeGraphResponse>(
        `/resource-relations?${baseKey}&mode=overview`,
        { signal },
      ),
    refetchInterval: false,
    refetchOnWindowFocus: false,
  });
  const initialQuery = new URLSearchParams(base);
  initialQuery.set("mode", "neighbors");
  initialQuery.set("limit", String(batchSize));
  if (rootId) initialQuery.set("root_id", rootId);
  if (groupId) initialQuery.set("group_id", groupId);
  initialQuery.set("direction", direction);
  const initialKey = initialQuery.toString();
  const initial = useQuery({
    queryKey: ["knowledge-neighbors", initialKey],
    queryFn: ({ signal }) =>
      request<KnowledgeGraphResponse>(`/resource-relations?${initialKey}`, {
        signal,
      }),
    enabled: !!(rootId || groupId),
    refetchInterval: false,
    refetchOnWindowFocus: false,
  });
  const dataQuery = rootId || groupId ? initial : overview;
  const data = dataQuery.data;
  useEffect(() => {
    controller.current?.abort();
    setExpansions({});
    setSelectedId("");
    setDetailId("");
    setEdgeId("");
    setBusy("");
    setError("");
    setAnchorId("");
    return () => controller.current?.abort();
  }, [viewKey]);
  useEffect(() => {
    const timer = window.setTimeout(() => setQueryText(search.trim()), 250);
    return () => window.clearTimeout(timer);
  }, [search]);
  const searchQuery = new URLSearchParams(base);
  searchQuery.delete("kind");
  searchQuery.delete("resource_kind");
  const searchKinds = params.getAll("resource_kind");
  for (const kind of searchKinds.length
    ? searchKinds
    : includeRuntime
      ? (overview.data?.facets.resource_kinds ?? []).map((item) => item.value)
      : [])
    searchQuery.append("kind", kind);
  searchQuery.set("graph_eligible", "true");
  searchQuery.set("q", queryText);
  searchQuery.set("page_size", "20");
  const searchResults = useQuery({
    queryKey: ["knowledge-resource-search", searchQuery.toString()],
    queryFn: ({ signal }) =>
      request<ResourceList>(`/resources?${searchQuery}`, { signal }),
    enabled: searchOpen && !!queryText,
  });
  const change = (key: string, values: string[]) => {
    const next = new URLSearchParams(params);
    next.delete(key);
    values.filter(Boolean).forEach((value) => next.append(key, value));
    if (key !== "direction" && key !== "include_runtime") {
      next.delete("root_id");
      next.delete("group_id");
    }
    if (
      key === "include_runtime" &&
      !values.length &&
      resources.some(
        (resource) => resource.id === rootId && isRuntime(resource),
      )
    )
      next.delete("root_id");
    next.delete("q");
    setParams(next);
  };
  const navigate = (id: string, group = false) => {
    const next = new URLSearchParams(params);
    next.delete("root_id");
    next.delete("group_id");
    next.delete("q");
    if (id) next.set(group ? "group_id" : "root_id", id);
    setParams(next);
    setSearch("");
    setSearchOpen(false);
  };
  const allPages = useMemo(
    () =>
      [
        data,
        ...Object.values(expansions).flatMap((value) => value.pages),
      ].filter((page): page is KnowledgeGraphResponse => !!page),
    [data, expansions],
  );
  const resources = useMemo(
    () => [
      ...new Map(
        allPages.flatMap((page) => page.nodes).map((node) => [node.id, node]),
      ).values(),
    ],
    [allPages],
  );
  const relations = useMemo(() => {
    const ids = new Set(resources.map((resource) => resource.id));
    return [
      ...new Map(
        allPages
          .flatMap((page) => page.edges)
          .filter(
            (edge) =>
              ids.has(edge.source_resource_id) &&
              ids.has(edge.target_resource_id),
          )
          .map((edge) => [edge.id, edge]),
      ).values(),
    ];
  }, [allPages, resources]);
  const groups = overview.data?.groups ?? [];
  const selected = resources.find((resource) => resource.id === selectedId);
  const chosenEdge = relations.find((edge) => edge.id === edgeId);
  const group = groups.find((item) => item.id === groupId);
  const root = resources.find((item) => item.id === rootId);
  const isOverview = !rootId && !groupId;
  const nodes = useMemo<KnowledgeNode[]>(
    () =>
      isOverview
        ? (data?.groups ?? []).map((group) => ({
            id: group.id,
            name:
              group.namespace ||
              (group.cluster
                ? "集群资源"
                : group.source_kind === "docker"
                  ? "Docker 资源"
                  : group.source_kind === "lima"
                    ? "Lima 主机"
                    : "独立资源"),
            kind: group.resource_kinds[0]?.value ?? "cluster",
            subtitle: group.cluster || group.label,
            group: true,
            count: group.count,
          }))
        : resources.map((resource) => ({
            id: resource.id,
            name: resource.name,
            kind: resource.kind,
            subtitle: resourceKindLabel(resource.kind),
          })),
    [isOverview, data, resources],
  );
  const edges = useMemo<KnowledgeEdge[]>(
    () =>
      isOverview
        ? (data?.group_edges ?? []).map((edge, index) => ({
            id: `aggregate:${index}:${edge.source_group_id}:${edge.target_group_id}:${edge.kind}`,
            source: edge.source_group_id,
            target: edge.target_group_id,
            label: `${relationKinds[edge.kind] ?? edge.kind} · ${edge.count} 条`,
          }))
        : relations.map((edge) => ({
            id: edge.id,
            source: edge.source_resource_id,
            target: edge.target_resource_id,
            label: relationKinds[edge.kind] ?? edge.kind,
            declared: edge.provenance === "declared",
          })),
    [isOverview, data, relations],
  );
  const selectedExpansion =
    expansions[selectedId === rootId ? "base" : selectedId];
  const morePage = expansions["base"]?.pages.at(-1) ?? data;
  const selectedPage =
    selectedExpansion?.pages.at(-1) ??
    (selectedId === rootId ? morePage : undefined);
  const warnings = [
    ...new Set(allPages.flatMap((page) => page.warnings ?? [])),
  ];
  const visibleTypes = [
    ...new Set(nodes.filter((node) => !node.group).map((node) => node.kind)),
  ];

  const loadMore = async (id: string, basePage = false) => {
    if (busy) return;
    if (resources.length >= 300) {
      setError(
        "当前视图已展开 300 项资源。请收起部分关联，或将所选资源设为中心继续查看。",
      );
      return;
    }
    const key = basePage || id === rootId ? "base" : id;
    const last =
      expansions[key]?.pages.at(-1) ??
      (basePage || id === rootId ? data : undefined);
    if (last && last.next_offset === null) return;
    const query = new URLSearchParams(baseKey);
    query.set("mode", "neighbors");
    if (basePage && groupId) query.set("group_id", groupId);
    else query.set("root_id", id || rootId);
    query.set("direction", direction);
    query.set("limit", String(Math.min(batchSize, 300 - resources.length)));
    query.set("offset", String(last?.next_offset ?? 0));
    const visible = resources;
    for (const resource of [
      ...new Map(visible.map((item) => [item.id, item])).values(),
    ])
      query.append("visible_id", resource.id);
    const abort = new AbortController();
    controller.current = abort;
    setBusy(key);
    setError("");
    try {
      const response = await request<KnowledgeGraphResponse>(
        `/resource-relations?${query}`,
        { signal: abort.signal },
      );
      if (abort.signal.aborted) return;
      setAnchorId(id || rootId);
      // The API returns visible nodes to complete cross-page edges. Keep only this batch's owned nodes so collapse can remove its branch.
      const existingIds = new Set(resources.map((resource) => resource.id));
      const batchIds = new Set(
        response.page_node_ids ??
          response.nodes
            .filter((resource) => !existingIds.has(resource.id))
            .map((resource) => resource.id),
      );
      const page = {
        ...response,
        nodes: response.nodes.filter(
          (resource) => batchIds.has(resource.id) || resource.id === id,
        ),
      };
      setExpansions((current) => ({
        ...current,
        [key]: { anchor: id, pages: [...(current[key]?.pages ?? []), page] },
      }));
    } catch (cause) {
      if (!abort.signal.aborted)
        setError(
          cause instanceof Error ? cause.message : "读取关联失败，请重试",
        );
    } finally {
      if (!abort.signal.aborted) setBusy("");
    }
  };
  const collapse = (id: string) => {
    setExpansions((current) => {
      const candidates = { ...current };
      delete candidates[id === rootId ? "base" : id];
      const reachable = new Set((data?.nodes ?? []).map((node) => node.id));
      const keep: Record<string, Expansion> = {};
      if (candidates.base) {
        keep.base = candidates.base;
        candidates.base.pages
          .flatMap((page) => page.nodes)
          .forEach((node) => reachable.add(node.id));
      }
      let changed = true;
      while (changed) {
        changed = false;
        for (const [key, expansion] of Object.entries(candidates)) {
          if (keep[key] || !reachable.has(expansion.anchor)) continue;
          keep[key] = expansion;
          expansion.pages
            .flatMap((page) => page.nodes)
            .forEach((node) => reachable.add(node.id));
          changed = true;
        }
      }
      return keep;
    });
  };
  const refreshAll = async () => {
    if (busy || refreshing.current || dataQuery.isPending) return;
    refreshing.current = true;
    const abort = new AbortController();
    controller.current = abort;
    setBusy("refresh");
    setError("");
    try {
      if (isOverview) {
        const fresh = await request<KnowledgeGraphResponse>(
          `/resource-relations?${baseKey}&mode=overview`,
          { signal: abort.signal },
        );
        if (!abort.signal.aborted)
          queryClient.setQueryData(["knowledge-overview", baseKey], fresh);
      } else {
        const freshBase = await request<KnowledgeGraphResponse>(
          `/resource-relations?${initialKey}`,
          { signal: abort.signal },
        );
        const visible = new Map(freshBase.nodes.map((node) => [node.id, node]));
        const freshExpansions: Record<string, Expansion> = {};
        const pending = new Map(Object.entries(expansions));
        // Only replay branches reachable through freshly confirmed pages. A removed relation must not resurrect its old branch.
        while (pending.size) {
          const entry = [...pending.entries()].find(
            ([key, expansion]) =>
              key === "base" || visible.has(expansion.anchor),
          );
          if (!entry) break;
          const [key, expansion] = entry;
          pending.delete(key);
          const pages: KnowledgeGraphResponse[] = [];
          let missingAnchor = false;
          for (const oldPage of expansion.pages) {
            const query = new URLSearchParams(baseKey);
            query.set("mode", "neighbors");
            if (key === "base" && groupId) query.set("group_id", groupId);
            else
              query.set("root_id", key === "base" ? rootId : expansion.anchor);
            query.set("direction", direction);
            query.set("offset", String(oldPage.offset));
            query.set("limit", String(oldPage.limit));
            for (const id of visible.keys()) query.append("visible_id", id);
            let response: KnowledgeGraphResponse;
            try {
              response = await request<KnowledgeGraphResponse>(
                `/resource-relations?${query}`,
                { signal: abort.signal },
              );
            } catch (cause) {
              if (
                cause instanceof ApiError &&
                cause.status === 404 &&
                key !== "base"
              ) {
                visible.delete(expansion.anchor);
                missingAnchor = true;
                break;
              }
              throw cause;
            }
            const batchIds = new Set(
              response.page_node_ids ??
                response.nodes
                  .filter((node) => !visible.has(node.id))
                  .map((node) => node.id),
            );
            const owned = response.nodes.filter(
              (node) => batchIds.has(node.id) || node.id === expansion.anchor,
            );
            pages.push({ ...response, nodes: owned });
            visible.clear();
            response.nodes.forEach((node) => visible.set(node.id, node));
          }
          if (!missingAnchor)
            freshExpansions[key] = { anchor: expansion.anchor, pages };
        }
        if (!abort.signal.aborted) {
          // Replace every batch together so old expansion snapshots cannot overwrite a refreshed resource.
          const updated = (page: KnowledgeGraphResponse) => ({
            ...page,
            nodes: page.nodes
              .filter((node) => visible.has(node.id))
              .map((node) => visible.get(node.id)!),
          });
          queryClient.setQueryData(
            ["knowledge-neighbors", initialKey],
            updated(freshBase),
          );
          if (selectedId && !visible.has(selectedId)) setSelectedId("");
          setExpansions(
            Object.fromEntries(
              Object.entries(freshExpansions).map(([key, expansion]) => [
                key,
                { ...expansion, pages: expansion.pages.map(updated) },
              ]),
            ),
          );
        }
      }
    } catch (cause) {
      if (!abort.signal.aborted)
        setError(
          `刷新未完成，正在显示上次读取的图谱。${cause instanceof Error ? cause.message : "请稍后重试"}。资源可能已变更，可返回总览重新定位。`,
        );
    } finally {
      refreshing.current = false;
      if (!abort.signal.aborted) setBusy("");
    }
  };
  refreshCallback.current = () => {
    void refreshAll();
  };
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!document.hidden) refreshCallback.current();
    }, 30000);
    return () => window.clearInterval(timer);
  }, []);
  const selectNode = (id: string) => {
    if (isOverview) navigate(id, true);
    else {
      setSelectedId(id);
      setEdgeId("");
    }
  };

  return (
    <section className="resource-page resource-relations-page knowledge-page">
      <PageHeader
        title="资源关系"
        description="从资源出发，探索部署、归属与依赖。"
      />
      <div className="knowledge-filters">
        <FilterBar label="资源关系搜索与筛选">
          <div
            className="knowledge-search"
            onFocus={() => setSearchOpen(true)}
            onBlur={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget))
                setSearchOpen(false);
            }}
          >
            <FilterSearch
              label="搜索关系资源"
              value={search}
              onChange={(value) => {
                setSearch(value);
                setSearchOpen(true);
              }}
              onSubmit={() => {
                setQueryText(search.trim());
                setSearchOpen(true);
              }}
              placeholder="搜索资源名称、中文或拼音，定位关系"
            />
            {searchOpen && search.trim() && (
              <div
                className="knowledge-search-results"
                aria-label="资源搜索结果"
              >
                {searchResults.isPending ? (
                  <p>正在搜索资源…</p>
                ) : searchResults.isError ? (
                  <p role="alert">{searchResults.error.message}</p>
                ) : (
                  <>
                    {(searchResults.data?.items ?? [])
                      .filter((item) =>
                        searchableResource(item, includeRuntime),
                      )
                      .map((item) => (
                        <button key={item.id} onClick={() => navigate(item.id)}>
                          <span className="knowledge-search-icon">
                            <ResourceIcon kind={item.kind} />
                          </span>
                          <span>
                            <span>{item.name}</span>
                            <small>
                              {resourceKindLabel(item.kind)}
                              {item.attributes.cluster
                                ? ` · ${String(item.attributes.cluster)}`
                                : ""}
                              {item.attributes.namespace
                                ? ` / ${String(item.attributes.namespace)}`
                                : ""}
                            </small>
                          </span>
                          <ChevronRight size={15} />
                        </button>
                      ))}
                    {!(searchResults.data?.items ?? []).some((item) =>
                      searchableResource(item, includeRuntime),
                    ) && <p>没有找到匹配资源，请调整关键词或筛选条件。</p>}
                  </>
                )}
              </div>
            )}
          </div>
          <FilterSelect
            id="knowledge-resource-kind"
            label="资源类型"
            options={(overview.data?.facets.resource_kinds ?? []).map(
              (item) => ({ ...item, label: resourceKindLabel(item.value) }),
            )}
            values={params.getAll("resource_kind")}
            onChange={(values) => change("resource_kind", values)}
            multiple
            searchable
            popoverWidth={220}
          />
          <FilterSelect
            id="knowledge-relation-kind"
            label="关系类型"
            options={(overview.data?.facets.relation_kinds ?? []).map(
              (item) => ({
                ...item,
                label: relationKinds[item.value] ?? item.label,
              }),
            )}
            values={params.getAll("kind")}
            onChange={(values) => change("kind", values)}
            multiple
            popoverWidth={200}
          />
          {!isOverview && (
            <FilterSelect
              id="knowledge-direction"
              label="关联方向"
              options={[
                { value: "both", label: "全部方向" },
                { value: "out", label: "出向关系" },
                { value: "in", label: "入向关系" },
              ]}
              values={[direction]}
              onChange={(values) =>
                change("direction", values.length ? values : ["both"])
              }
              includeEmptyOption={false}
            />
          )}
          {
            <label className="knowledge-runtime-toggle">
              <input
                type="checkbox"
                checked={includeRuntime}
                onChange={(event) =>
                  change(
                    "include_runtime",
                    event.target.checked ? ["true"] : [],
                  )
                }
              />
              显示运行实例
            </label>
          }
        </FilterBar>
      </div>
      <div className="knowledge-workspace">
        <KnowledgeGraphCanvas
          nodes={nodes}
          edges={edges}
          rootId={rootId}
          anchorId={anchorId}
          viewKey={viewKey}
          selectedId={selectedId}
          onNode={selectNode}
          onKeyboardNode={setSelectedId}
          onEdge={(id) => {
            if (!isOverview) setEdgeId(id);
          }}
          onCanvas={() => setSelectedId("")}
        >
          {!isOverview && (
            <button
              className="knowledge-icon-button"
              aria-label="返回分组总览"
              title="返回分组总览"
              onClick={() => navigate("")}
            >
              <ArrowLeft size={16} />
            </button>
          )}
          <Layers size={17} />
          <span>
            {isOverview
              ? "分组总览"
              : (root?.name ?? group?.label ?? "资源关联")}
          </span>
          <span className="knowledge-context-count">
            {isOverview
              ? `${nodes.length} 个分组`
              : `${resources.length} 项资源 · ${relations.length} 条关系`}
          </span>
          <button
            className="knowledge-icon-button"
            aria-label="刷新图谱"
            title="刷新当前资源"
            disabled={dataQuery.isFetching || !!busy}
            onClick={() => void refreshAll()}
          >
            <RefreshCw
              size={15}
              className={
                dataQuery.isFetching || busy === "refresh"
                  ? "knowledge-loading"
                  : ""
              }
            />
          </button>
        </KnowledgeGraphCanvas>
        {dataQuery.isPending && (
          <div className="knowledge-empty">
            <Network size={28} />
            <p>正在读取资源关系…</p>
          </div>
        )}
        {dataQuery.isError && (
          <div className="knowledge-empty" role="alert">
            <p>{dataQuery.error.message}</p>
            <button className="btn" onClick={() => void dataQuery.refetch()}>
              重新加载
            </button>
          </div>
        )}
        {!dataQuery.isPending && !dataQuery.isError && !nodes.length && (
          <div className="knowledge-empty">
            <Search size={28} />
            <p>当前范围没有匹配的资源</p>
            <span>调整资源类型、关系类型或全局环境后再试。</span>
          </div>
        )}
        {selected && (
          <div
            className="knowledge-selection"
            role="region"
            aria-label="选中资源操作"
          >
            <span className="knowledge-selection-icon">
              <ResourceIcon kind={selected.kind} />
            </span>
            <div className="knowledge-selection-name">
              <strong>{selected.name}</strong>
              <small>{resourceKindLabel(selected.kind)}</small>
            </div>
            <button
              className="btn"
              disabled={
                !!busy ||
                resources.length >= 300 ||
                selectedPage?.next_offset === null
              }
              onClick={() => void loadMore(selected.id)}
            >
              {busy === selected.id
                ? "读取中…"
                : selectedPage
                  ? selectedPage.remaining > 0
                    ? `继续展开 · ${selectedPage.remaining}`
                    : "已全部展开"
                  : "展开关联"}
            </button>
            {selectedExpansion && (
              <button
                className="btn"
                onClick={() => collapse(selected.id)}
                disabled={!!busy}
              >
                收起关联
              </button>
            )}
            {selected.id !== rootId && (
              <button className="btn" onClick={() => navigate(selected.id)}>
                以此为中心
              </button>
            )}
            <button
              className="btn primary"
              onClick={() => setDetailId(selected.id)}
            >
              详情
            </button>
            <button
              className="knowledge-icon-button"
              aria-label="取消选择"
              onClick={() => setSelectedId("")}
            >
              <X size={15} />
            </button>
          </div>
        )}
        <div className="knowledge-footer">
          <div className="knowledge-legend">
            {isOverview ? (
              <span>
                <i style={{ background: "#7363cf" }} />
                资源分组 · 连线汇总实际关系
              </span>
            ) : (
              visibleTypes.slice(0, 6).map((kind) => (
                <span key={kind}>
                  <i style={{ background: knowledgeColor(kind) }} />
                  {resourceKindLabel(kind)}
                </span>
              ))
            )}
          </div>
          {!isOverview && morePage && morePage.remaining > 0 ? (
            <button
              className="knowledge-more"
              disabled={!!busy || resources.length >= 300}
              onClick={() => void loadMore(rootId, true)}
            >
              {busy === "base"
                ? "正在读取…"
                : `还有 ${morePage.remaining} 项 · 继续展开`}
            </button>
          ) : (
            <span className="knowledge-gesture-hint">
              {isOverview ? "点击分组展开" : "点击资源查看关联"} · 拖动平移 ·
              滚轮缩放
            </span>
          )}
        </div>
      </div>
      <div className="knowledge-announcement" aria-live="polite">
        {selected
          ? `已选中 ${selected.name}，${resourceKindLabel(selected.kind)}`
          : selectedId
            ? `已选中分组 ${nodes.find((node) => node.id === selectedId)?.name ?? ""}，按回车展开`
            : ""}
      </div>
      {resources.length >= 300 && (
        <div className="knowledge-notice" role="status">
          当前视图已展开 300 项资源，收起部分关联或选择新的中心后可继续探索。
        </div>
      )}
      {(error || warnings.length > 0) && (
        <div className="knowledge-notice" role={error ? "alert" : "status"}>
          {error || warnings.join("；")}
        </div>
      )}
      {detailId && (
        <ResourceDrawer
          resourceId={detailId}
          onClose={() => setDetailId("")}
          onSelect={setDetailId}
        />
      )}
      {chosenEdge && (
        <RelationDrawer
          edge={chosenEdge}
          resources={resources}
          onClose={() => setEdgeId("")}
          onSelect={(id) => {
            setEdgeId("");
            setDetailId(id);
          }}
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
