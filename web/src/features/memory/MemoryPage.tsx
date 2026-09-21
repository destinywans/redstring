import Pagination from "../../components/Pagination";
import SelectField from "../../components/SelectField";
import { useState, useEffect, useRef } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import ReactMarkdown from "react-markdown";
import { request, put, post } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { usePageScope } from "../../components/ScopeFilter";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { Button, ErrorNotice, Modal } from "../../components/ui";
import { BookOpen, GitBranch, X } from "../../lib/icons";
import type { KnowledgeMetadata } from "../knowledge/KnowledgeGovernance";
import "../workspace/workspace.css";
import "./memory.css";
type Doc = {
  id: string;
  title: string;
  body: string;
  source: string;
  application_id: string;
  version: number;
  lifecycle: string;
  status: string;
  metadata: KnowledgeMetadata;
  investigation_id?: string;
  generation_id: number;
  embedding_provider?: string;
  embedding_model?: string;
  dimensions?: number;
  created_at: string;
  content_hash: string;
};
type Context = {
  id: string;
  title: string;
  investigation_id: string;
  investigation_title: string;
  application_id: string;
  environment_id: string;
  parent_id?: string;
  revision: number;
  message_count: number;
  model_id: string;
  created_at: string;
  provenance: Record<string, unknown>;
};
type Items<T> = { items: T[]; next_cursor?: string; total?: number };
const lifecycle = (s: string) =>
  ({
    draft: "草稿",
    review: "待审阅",
    published: "已发布",
    archived: "已归档",
  })[s] ?? s;
export default function MemoryPage() {
  const { preview, user, applications, notify } = useWorkspace(),
    scope = usePageScope({ globalEnvironmentOnly: true }),
    client = useQueryClient();
  const [params, setParams] = useSearchParams(),
    [q, setQ] = useState(params.get("q") ?? ""),
    [editBase, setEditBase] = useState<Doc | null>(null),
    [body, setBody] = useState(""),
    [title, setTitle] = useState(""),
    [archiveBase, setArchiveBase] = useState<Doc | null>(null),
    [revision, setRevision] = useState<number | null>(null);
  const mode = params.get("type") === "contexts" ? "contexts" : "documents",
    id = params.get("item") ?? "";
  const memoryTabsRef = useRef<HTMLDivElement>(null);
  const committedQuery = params.get("q") ?? "";
  useEffect(() => setQ(committedQuery), [committedQuery]);
  // The global environment is now the only read scope for this page. Remove
  // legacy page overrides from shared URLs so they cannot remain invisible.
  useEffect(() => {
    if (
      !["environment", "environments", "environment_filter", "scope"].some(
        (key) => params.has(key),
      )
    )
      return;
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        if (next.get("scope") === "all") {
          next.delete("application");
          next.delete("applications");
        }
        for (const key of [
          "environment",
          "environments",
          "environment_filter",
          "scope",
        ])
          next.delete(key);
        return next;
      },
      { replace: true },
    );
  }, [params, setParams]);
  const filters = new URLSearchParams(scope.query);
  for (const k of mode === "documents" ? ["q", "kind", "lifecycle"] : ["q"])
    if (params.get(k)) filters.set(k, params.get(k)!);
  const [pageSize, setPageSize] = useState(20);
  const [position, setPosition] = useState({ key: "", page: 1 });
  const pagingKey = `${mode}:${filters.toString()}:${pageSize}`;
  const list = useInfiniteQuery({
    queryKey: ["memory", mode, filters.toString(), pageSize],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      request<Items<Doc | Context>>(
        `/memory/${mode}?${filters}&limit=${pageSize}&cursor=${encodeURIComponent(pageParam)}`,
      ),
    getNextPageParam: (p) => p.next_cursor || undefined,
    enabled: !preview,
  });
  const detail = useQuery({
    queryKey: ["memory", "document", id],
    queryFn: () => request<Doc>(`/knowledge/${id}`),
    enabled: !!id && mode === "documents" && !preview,
  });
  const references = useQuery({
    queryKey: ["memory", "references", id],
    queryFn: () =>
      request<
        Items<{
          investigation_id: string;
          branch_id: string;
          title: string;
          version: number;
          created_at: string;
          generation_id: number;
        }>
      >(`/memory/documents/${id}/references`),
    enabled: !!id && mode === "documents" && !preview,
  });
  const history = useQuery({
    queryKey: ["memory", "versions", mode, id],
    queryFn: () =>
      request<
        Items<{ version?: number; revision?: number; created_at: string }>
      >(
        mode === "documents"
          ? `/knowledge/${id}/revisions`
          : `/branches/${id}/revisions`,
      ),
    enabled: !!id && !preview,
  });
  useEffect(() => {
    setRevision(null);
    setEditBase(null);
    setArchiveBase(null);
  }, [id, mode]);
  const previousScope = useRef(scope.query);
  useEffect(() => {
    if (previousScope.current === scope.query) return;
    previousScope.current = scope.query;
    setEditBase(null);
    setArchiveBase(null);
    setRevision(null);
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        next.delete("item");
        return next;
      },
      { replace: true },
    );
  }, [scope.query, setParams]);
  const contextRevision = revision ?? history.data?.items[0]?.revision;
  const contextDetail = useQuery({
    queryKey: ["memory", "context", id, contextRevision],
    queryFn: () =>
      request<{
        messages: {
          id: string;
          role: string;
          content: string;
          model_id?: string;
        }[];
        provenance: Record<string, unknown>;
        evidence: { id: string; title: string; source: string }[];
      }>(`/branches/${id}/revisions/${contextRevision}`),
    enabled:
      !!id && mode === "contexts" && contextRevision !== undefined && !preview,
  });
  const historicalDoc = useQuery({
    queryKey: ["memory", "historical-document", id, revision],
    queryFn: () => request<Doc>(`/knowledge/${id}/revisions/${revision}`),
    enabled: !!id && mode === "documents" && revision !== null && !preview,
  });
  const d = revision === null ? detail.data : historicalDoc.data;
  const save = useMutation({
    mutationFn: () => {
      if (!editBase) throw new Error("请重新打开修订窗口");
      return put(`/knowledge/${editBase.id}`, {
        expected_version: editBase.version,
        title,
        body,
        source: editBase.source,
        metadata: editBase.metadata,
      });
    },
    onSuccess: () => {
      setEditBase(null);
      setRevision(null);
      notify("已生成新的草稿版本，审阅发布后参与召回");
      void client.invalidateQueries({ queryKey: ["memory"] });
      void client.invalidateQueries({ queryKey: ["knowledge"] });
    },
  });
  const change = useMutation({
    mutationFn: () => {
      if (!archiveBase) throw new Error("请重新打开归档确认窗口");
      return post(`/knowledge/${archiveBase.id}/transition`, {
        expected_version: archiveBase.version,
        state: "archived",
      });
    },
    onSuccess: () => {
      setArchiveBase(null);
      notify("状态已更新并记录审计");
      void client.invalidateQueries({ queryKey: ["memory"] });
    },
  });
  const total = list.data?.pages[0]?.total;
  const requestedPage = position.key === pagingKey ? position.page : 1;
  const maximumPage =
    typeof total === "number" && Number.isFinite(total)
      ? Math.max(1, Math.ceil(total / pageSize))
      : Infinity;
  // Commit resets, including a refresh that removed the current cursor page.
  // Returning to an earlier filter must not revive its old page position.
  const pageUnavailable =
    requestedPage > 1 &&
    list.isSuccess &&
    (!list.data.pages[requestedPage - 1] ||
      (!list.data.pages[requestedPage - 1].items.length &&
        !list.data.pages[requestedPage - 1].next_cursor));
  const page =
    requestedPage > maximumPage || pageUnavailable ? 1 : requestedPage;
  if (position.key !== pagingKey || position.page !== page)
    setPosition({ key: pagingKey, page: page });
  const currentPage = list.data?.pages[page - 1];
  const rows = currentPage?.items ?? [];
  async function changePage(next: number) {
    if (list.isFetching || next < 1) return;
    if (next > (list.data?.pages.length ?? 0)) {
      if (!currentPage?.next_cursor) return;
      const result = await list.fetchNextPage();
      if (result.isError || !result.data?.pages[next - 1]) return;
    }
    setPosition({ key: pagingKey, page: next });
    setParams((current) => {
      const params = new URLSearchParams(current);
      params.delete("item");
      return params;
    });
  }
  const ctx =
    mode === "contexts"
      ? (rows.find((r) => r.id === id) as Context | undefined)
      : undefined;
  function update(key: string, value: string) {
    setParams((p) => {
      const next = new URLSearchParams(p);
      value ? next.set(key, value) : next.delete(key);
      next.delete("item");
      if (key === "type" && value === "contexts") {
        next.delete("kind");
        next.delete("lifecycle");
      }
      return next;
    });
    setRevision(null);
  }
  const appliedFilters = [
    ...(committedQuery
      ? [
          {
            key: "q",
            label: `关键词：${committedQuery}`,
            remove: () => update("q", ""),
          },
        ]
      : []),
    ...scope.applicationIds.map((appId) => ({
      key: `application:${appId}`,
      label: `应用：${applications.find((app) => app.id === appId)?.name ?? "不可用应用"}`,
      remove: () =>
        scope.update(
          scope.applicationIds.filter((value) => value !== appId),
          [],
        ),
    })),
    ...(mode === "documents" && params.get("kind")
      ? [
          {
            key: "kind",
            label: `类型：${params.get("kind") === "experience" ? "调查经验" : params.get("kind") === "runbook" ? "Runbook" : params.get("kind")}`,
            remove: () => update("kind", ""),
          },
        ]
      : []),
    ...(mode === "documents" && params.get("lifecycle")
      ? [
          {
            key: "lifecycle",
            label: `状态：${lifecycle(params.get("lifecycle")!)}`,
            remove: () => update("lifecycle", ""),
          },
        ]
      : []),
  ];
  function clearFilters() {
    setQ("");
    setParams((current) => {
      const next = new URLSearchParams(current);
      for (const key of [
        "q",
        "kind",
        "lifecycle",
        "application",
        "applications",
        "item",
      ])
        next.delete(key);
      return next;
    });
  }
  const canWrite =
    !!detail.data &&
    user.role !== "viewer" &&
    (!!detail.data.application_id || user.role === "admin") &&
    revision === null;
  return (
    <div className="management-page memory-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">MEMORY</span>
          <h1>记忆与上下文</h1>
          <p>
            查看已保存的知识、调查分支和引用来源。模型切换继续使用这些持久记录。
          </p>
        </div>
        <Link className="btn" to="/knowledge">
          维护 Runbook 与经验 <BookOpen size={16} />
        </Link>
      </header>
      <div
        ref={memoryTabsRef}
        className="memory-switch"
        role="tablist"
        aria-label="记忆分类"
        onKeyDown={(event) => {
          if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key))
            return;
          const tabs = Array.from(
            memoryTabsRef.current?.querySelectorAll<HTMLButtonElement>(
              '[role="tab"]',
            ) ?? [],
          );
          const current = tabs.indexOf(
            document.activeElement as HTMLButtonElement,
          );
          if (current < 0 || !tabs.length) return;
          event.preventDefault();
          const next =
            event.key === "Home"
              ? 0
              : event.key === "End"
                ? tabs.length - 1
                : (current +
                    (event.key === "ArrowRight" ? 1 : -1) +
                    tabs.length) %
                  tabs.length;
          tabs[next].focus();
          update("type", next === 0 ? "documents" : "contexts");
        }}
      >
        <button
          type="button"
          role="tab"
          id="memory-documents-tab"
          aria-controls="memory-results"
          aria-selected={mode === "documents"}
          tabIndex={mode === "documents" ? 0 : -1}
          onClick={() => update("type", "documents")}
        >
          长期知识与经验
        </button>
        <button
          type="button"
          role="tab"
          id="memory-contexts-tab"
          aria-controls="memory-results"
          aria-selected={mode === "contexts"}
          tabIndex={mode === "contexts" ? 0 : -1}
          onClick={() => update("type", "contexts")}
        >
          会话上下文
        </button>
      </div>
      <FilterBar label="记忆搜索与筛选">
        <FilterSearch
          value={q}
          onChange={setQ}
          onSubmit={() => update("q", q.trim())}
          label="搜索记忆"
          placeholder="搜索标题、内容或来源"
        />
        <FilterSelect
          id="memory-applications"
          label="应用"
          multiple
          searchable
          values={scope.applicationIds}
          options={applications
            .filter((app) => !app.archived)
            .map((app) => ({ value: app.id, label: app.name }))}
          onChange={(values) => scope.update(values, [])}
        />
        {mode === "documents" && (
          <>
            <FilterSelect
              id="memory-kind"
              label="类型"
              values={params.get("kind") ? [params.get("kind")!] : []}
              options={[
                { value: "experience", label: "调查经验" },
                { value: "runbook", label: "Runbook" },
              ]}
              onChange={(values) => update("kind", values[0] ?? "")}
            />
            <FilterSelect
              id="memory-lifecycle"
              label="状态"
              values={params.get("lifecycle") ? [params.get("lifecycle")!] : []}
              options={["draft", "review", "published", "archived"].map(
                (value) => ({ value, label: lifecycle(value) }),
              )}
              onChange={(values) => update("lifecycle", values[0] ?? "")}
            />
          </>
        )}
      </FilterBar>
      <div className="memory-filter-summary" aria-label="已选筛选条件">
        <div className="memory-filter-chips">
          {appliedFilters.map((filter) => (
            <button
              key={filter.key}
              className="filter-chip"
              type="button"
              onClick={filter.remove}
              title={filter.label}
              aria-label={`移除${filter.label}`}
            >
              <span>{filter.label}</span>
              <X size={13} />
            </button>
          ))}
          {!!appliedFilters.length && (
            <button
              type="button"
              className="filter-clear"
              onClick={clearFilters}
            >
              清除筛选
            </button>
          )}
        </div>
      </div>
      {preview && <p className="management-notice">预览模式未读取实际记忆。</p>}
      {list.error && <ErrorNotice error={list.error} />}
      <div
        className="memory-columns"
        id="memory-results"
        role="tabpanel"
        tabIndex={0}
        aria-labelledby={
          mode === "documents" ? "memory-documents-tab" : "memory-contexts-tab"
        }
      >
        <section className="card memory-list">
          {!rows.length ? (
            <p className="management-empty">
              {list.isPending && !preview
                ? "正在读取记忆…"
                : "当前范围暂无记录"}
            </p>
          ) : (
            rows.map((r) => {
              const doc = r as Doc,
                c = r as Context;
              return (
                <button
                  className={id === r.id ? "active" : ""}
                  key={r.id}
                  onClick={() => {
                    setParams((p) => {
                      p.set("item", r.id);
                      return p;
                    });
                    setRevision(mode === "contexts" ? c.revision : null);
                  }}
                >
                  <div className="memory-title">
                    {mode === "documents" ? (
                      <BookOpen size={18} />
                    ) : (
                      <GitBranch size={18} />
                    )}
                    <strong>{r.title}</strong>
                  </div>
                  <small>
                    {applications.find((a) => a.id === r.application_id)
                      ?.name ??
                      (r.application_id || "工作区共享")}
                    {mode === "documents"
                      ? ` · v${doc.version} · ${lifecycle(doc.lifecycle)}`
                      : ` · ${c.message_count} 条消息 · v${c.revision}`}
                  </small>
                  <p>
                    {mode === "documents"
                      ? (doc.investigation_id ? "调查经验 · " : "") +
                        (doc.metadata.origin === "agent"
                          ? "Agent 起草，人工确认"
                          : doc.metadata.origin === "human"
                            ? "人工维护"
                            : "历史来源")
                      : c.investigation_title}
                  </p>
                </button>
              );
            })
          )}
          <Pagination
            page={page}
            pageSize={pageSize}
            total={total}
            hasNext={!!currentPage?.next_cursor}
            loading={list.isFetching}
            onPageChange={(next) => void changePage(next)}
            onPageSizeChange={(size) => {
              setPageSize(size);
              setPosition({ key: "", page: 1 });
            }}
            ariaLabel="记忆列表分页"
          />
        </section>
        <section className="card memory-detail">
          {!id ? (
            <div className="management-empty">
              选择一条记录，查看内容、版本和来源
            </div>
          ) : (
            <>
              <header className="memory-detail-heading">
                <h2>{d?.title ?? ctx?.title ?? "上下文记录"}</h2>
                <SelectField
                  className="field"
                  aria-label="历史版本"
                  value={
                    mode === "contexts"
                      ? (contextRevision ?? "")
                      : (revision ?? "")
                  }
                  onChange={(e) =>
                    setRevision(
                      e.target.value === "" ? null : Number(e.target.value),
                    )
                  }
                >
                  {mode === "documents" && <option value="">当前版本</option>}
                  {history.data?.items.map((v) => (
                    <option
                      key={v.version ?? v.revision}
                      value={v.version ?? v.revision}
                    >
                      v{v.version ?? v.revision} ·{" "}
                      {new Date(v.created_at).toLocaleDateString()}
                    </option>
                  ))}
                </SelectField>
              </header>
              {history.error && <ErrorNotice error={history.error} />}
              {mode === "documents" ? (
                <>
                  {detail.error && <ErrorNotice error={detail.error} />}{" "}
                  {historicalDoc.error && (
                    <ErrorNotice error={historicalDoc.error} />
                  )}{" "}
                  {d && (
                    <>
                      <dl className="memory-facts">
                        <dt>适用范围</dt>
                        <dd>
                          {d.metadata.visibility === "workspace"
                            ? "工作区共享"
                            : (applications.find(
                                (a) => a.id === d.application_id,
                              )?.name ?? d.application_id)}
                        </dd>
                        <dt>维护人</dt>
                        <dd>{d.metadata.maintainer_id || "未知"}</dd>
                        <dt>原始来源</dt>
                        <dd>{d.source || "未记录"}</dd>
                        <dt>创建来源</dt>
                        <dd>
                          {d.metadata.origin || "未知"}
                          {d.metadata.generation_model
                            ? ` · ${d.metadata.generation_model}`
                            : ""}
                        </dd>
                        <dt>索引</dt>
                        <dd>
                          {d.status} ·{" "}
                          {d.embedding_model || "当前版本尚无向量索引"}
                          {d.dimensions ? ` · ${d.dimensions} 维` : ""}
                        </dd>
                        <dt>版本 / 内容摘要</dt>
                        <dd>
                          v{d.version} · {d.content_hash}
                        </dd>
                      </dl>
                      <div className="memory-content">
                        <ReactMarkdown>{d.body}</ReactMarkdown>
                      </div>
                      {canWrite && (
                        <div className="memory-actions">
                          <Button
                            variant="outline"
                            onClick={() => {
                              setTitle(d.title);
                              setBody(d.body);
                              save.reset();
                              setEditBase(structuredClone(d));
                            }}
                          >
                            修订为新版本
                          </Button>
                          {detail.data?.lifecycle !== "archived" && (
                            <Button
                              variant="outline"
                              onClick={() => {
                                change.reset();
                                setArchiveBase(structuredClone(d));
                              }}
                            >
                              归档并停止召回
                            </Button>
                          )}
                        </div>
                      )}
                      <h3>已记录的召回来源</h3>
                      <p className="muted">
                        展示最近的检索快照；命中不等于模型采纳，也不代表完整召回次数。
                      </p>
                      {references.error && (
                        <ErrorNotice error={references.error} />
                      )}{" "}
                      {!references.data?.items.length ? (
                        <p className="muted">暂无可见的引用快照</p>
                      ) : (
                        references.data.items.map((v, i) => (
                          <Link
                            className="memory-reference"
                            key={`${v.investigation_id}-${i}`}
                            to={`/investigations/${v.investigation_id}?branch=${v.branch_id}`}
                          >
                            {v.title}
                            <small>
                              文档 v{v.version} · 索引代次 {v.generation_id} ·{" "}
                              {new Date(v.created_at).toLocaleString()}
                            </small>
                          </Link>
                        ))
                      )}
                      {d.investigation_id && (
                        <Link
                          className="btn"
                          to={`/investigations/${d.investigation_id}`}
                        >
                          查看经验来源调查 →
                        </Link>
                      )}
                    </>
                  )}
                </>
              ) : (
                <>
                  {ctx && (
                    <dl className="memory-facts">
                      <dt>当前模型</dt>
                      <dd>{ctx.model_id || "尚未选择"}</dd>
                      <dt>父分支</dt>
                      <dd>{ctx.parent_id || "根分支"}</dd>
                      <dt>所属调查</dt>
                      <dd>
                        <Link to={`/investigations/${ctx.investigation_id}`}>
                          {ctx.investigation_title} →
                        </Link>
                      </dd>
                    </dl>
                  )}
                  <p className="management-notice">
                    上下文版本为历史快照。继续对话、派生或合并会创建新版本。
                  </p>
                  {contextDetail.error && (
                    <ErrorNotice error={contextDetail.error} />
                  )}
                  <details>
                    <summary>版本来源</summary>
                    <pre>
                      {JSON.stringify(
                        contextDetail.data?.provenance ?? ctx?.provenance,
                        null,
                        2,
                      )}
                    </pre>
                  </details>
                  {contextDetail.data?.messages.map((m, i) => (
                    <article className="memory-message" key={m.id ?? i}>
                      <small>
                        {m.role} {m.model_id}
                      </small>
                      <ReactMarkdown>{m.content}</ReactMarkdown>
                    </article>
                  ))}
                  {contextDetail.data?.messages.length === 0 && (
                    <p className="muted">初始快照，暂无消息。</p>
                  )}
                  <details>
                    <summary>
                      引用证据（{contextDetail.data?.evidence.length ?? 0}）
                    </summary>
                    {contextDetail.data?.evidence.map((e) => (
                      <p key={e.id}>
                        {e.title} · {e.source}
                      </p>
                    ))}
                  </details>
                </>
              )}
            </>
          )}
        </section>
      </div>
      <Modal
        open={!!editBase}
        onOpenChange={(v) => {
          if (!v && !save.isPending) setEditBase(null);
        }}
        title="修订记忆"
        description="保留旧版本。保存后成为草稿，审阅发布后才参与知识召回。"
      >
        <label>
          标题
          <input
            className="field"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
        </label>
        <label>
          内容
          <textarea
            className="field"
            rows={14}
            value={body}
            onChange={(e) => setBody(e.target.value)}
          />
        </label>
        <p className="muted">
          基于 v{editBase?.version}{" "}
          修订。若版本冲突，请保留草稿并关闭窗口，重新查看最新内容后再修订。
        </p>
        {save.error && <ErrorNotice error={save.error} />}
        <Button
          disabled={save.isPending || !title.trim() || !body.trim()}
          onClick={() => save.mutate()}
        >
          保存草稿版本
        </Button>
      </Modal>
      <Modal
        open={!!archiveBase}
        onOpenChange={(v) => {
          if (!v && !change.isPending) setArchiveBase(null);
        }}
        title="归档这条记忆？"
        description="归档后不再参与新的知识召回，历史版本与已经形成的调查证据仍保留。"
      >
        <p className="muted">
          确认归档「{archiveBase?.title}」v{archiveBase?.version}
          ；内容发生变化时本次确认将失效。
        </p>
        {change.error && <ErrorNotice error={change.error} />}
        <Button disabled={change.isPending} onClick={() => change.mutate()}>
          确认归档
        </Button>
      </Modal>
    </div>
  );
}
