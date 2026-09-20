import ScopeFilter from "../../components/ScopeFilter";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import ReactMarkdown from "react-markdown";
import {
  ArrowRight,
  BookOpen,
  Check,
  ChevronDown,
  ChevronUp,
  FileText,
  Folder,
  Info,
  LoaderCircle,
  Plus,
  Quote,
  RefreshCw,
  Search,
  X,
} from "../../lib/icons";
import { request, post } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { useDraftStore } from "../../lib/drafts";
import {
  previewDocuments,
  previewHits,
  previewDraft,
  type KnowledgeDocument,
  type KnowledgeHit,
  type KnowledgeDraft,
} from "./fixtures";
import "./knowledge.css";
import KnowledgeGovernance, {
  TemplateFields,
  metadataFromForm,
} from "./KnowledgeGovernance";

function KnowledgeModal({
  title,
  children,
  onClose,
}: {
  title: string;
  children: React.ReactNode;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
    return () => ref.current?.close();
  }, []);
  return (
    <dialog ref={ref} className="knowledge-modal" onCancel={onClose}>
      <header>
        <h2>{title}</h2>
        <button
          className="icon-button"
          onClick={onClose}
          aria-label="关闭对话框"
        >
          <X size={18} />
        </button>
      </header>
      {children}
    </dialog>
  );
}
const statusLabel = (s: string) =>
  s === "ready" || s === "indexed"
    ? "已索引"
    : s === "failed"
      ? "索引失败"
      : s === "indexing" || s === "building"
        ? "索引中"
        : s === "unindexed"
          ? "待索引"
          : s;
export default function KnowledgePage() {
  const { preview, application, environment, investigationId, user, notify } =
    useWorkspace();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [selectedId, setSelectedId] = useState("");
  const [filter, setFilter] = useState("");
  const [query, setQuery] = useState(preview ? "发布后连接超时" : "");
  const [hits, setHits] = useState<KnowledgeHit[] | null>(
    preview ? previewHits : null,
  );
  const [searching, setSearching] = useState(false);
  const [retrievalNotice, setRetrievalNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [modal, setModal] = useState<
    "import" | "draft" | "citation" | "new-draft" | null
  >(null);
  const [selectedDraft, setSelectedDraft] = useState<KnowledgeDraft | null>(
    null,
  );
  const [hitDetail, setHitDetail] = useState<KnowledgeHit | null>(null);
  const [relatedCollapsed, setRelatedCollapsed] = useState(false);
  const [draftCollapsed, setDraftCollapsed] = useState(false);
  const [libraryCollapsed, setLibraryCollapsed] = useState(false);
  const [citationBranch, setCitationBranch] = useState("");
  const [citations, setCitations] = useState<string[]>([]);
  const docs = useQuery({
    queryKey: ["knowledge", application?.id, preview],
    enabled: !!application,
    queryFn: async () =>
      preview
        ? { items: previewDocuments }
        : request<{ items: KnowledgeDocument[] | null }>(
            `/knowledge?application_id=${encodeURIComponent(application!.id)}`,
          ),
  });
  const detail = useQuery({
    queryKey: ["knowledge-document", selectedId, preview],
    enabled: !!selectedId && !preview,
    queryFn: () => request<KnowledgeDocument>(`/knowledge/${selectedId}`),
  });
  const investigation = useQuery({
    queryKey: ["investigation", investigationId, preview],
    enabled: !!investigationId && !preview,
    queryFn: () =>
      request<{
        application_id: string;
        status: string;
        branches: { id: string; title: string }[];
        evidence: { id: string; title: string }[];
      }>(`/investigations/${investigationId}`),
  });
  const drafts = useQuery({
    queryKey: ["knowledge-drafts", investigationId, preview],
    enabled: !!investigationId || preview,
    queryFn: async () =>
      preview
        ? { items: [previewDraft] }
        : request<{ items: KnowledgeDraft[] | null }>(
            `/investigations/${investigationId}/knowledge-drafts`,
          ),
  });
  useEffect(() => {
    const items = docs.data?.items ?? [];
    if (!items.some((d) => d.id === selectedId))
      setSelectedId(items[0]?.id ?? "");
  }, [docs.data]);
  useEffect(() => {
    setHits(preview ? previewHits : null);
    setHitDetail(null);
    setModal(null);
  }, [application?.id, environment?.id, preview]);
  useEffect(() => {
    setCitationBranch("");
  }, [investigationId]);
  const documents = docs.data?.items ?? [];
  const selected = preview
    ? documents.find((d) => d.id === selectedId)
    : detail.data?.id === selectedId &&
        (!detail.data.application_id ||
          detail.data.application_id === application?.id ||
          detail.data.metadata?.visibility === "workspace" ||
          detail.data.metadata?.shared_application_ids?.includes(
            application?.id ?? "",
          ))
      ? detail.data
      : documents.find((d) => d.id === selectedId);
  const canWrite = user.role !== "viewer";
  const groups = preview
    ? [
        {
          name: "应用交付",
          items: [documents[1], documents[2], documents[0]].filter(Boolean),
        },
        { name: "数据库", items: documents.slice(3, 6) },
        { name: "Kubernetes", items: documents.slice(6) },
      ]
    : [{ name: application?.name ?? "应用知识", items: documents }];
  const error = (e: unknown) =>
    notify(e instanceof Error ? e.message : "操作失败，请稍后重试", "error");
  async function search(e: FormEvent) {
    e.preventDefault();
    if (!query.trim() || !application) return;
    setSearching(true);
    try {
      const result = preview
        ? {
            items: previewHits.filter(
              (h) =>
                `${h.title}${h.content}`.includes(query.trim()) ||
                query.includes("连接"),
            ),
          }
        : await post<{
            items: KnowledgeHit[] | null;
            mode?: string;
            degraded?: boolean;
            reason?: string;
          }>("/knowledge/search", {
            application_id: application.id,
            environment_id: environment?.id,
            query,
            limit: 5,
          });
      setHits(result.items ?? []);
      setRetrievalNotice(
        "degraded" in result && result.degraded
          ? `向量检索暂不可用，已使用关键词召回。${result.reason || ""}`
          : "mode" in result && result.mode === "hybrid"
            ? "关键词与语义融合召回"
            : "",
      );
    } catch (e) {
      error(e);
    } finally {
      setSearching(false);
    }
  }
  async function reindex() {
    if (!selected) return;
    if (preview) {
      notify("演示模式不会重建真实索引", "info");
      return;
    }
    setBusy(true);
    try {
      await post(`/knowledge/${selected.id}/reindex`, {});
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["knowledge"] }),
        qc.invalidateQueries({ queryKey: ["knowledge-document", selected.id] }),
      ]);
      notify("索引已重建", "success");
    } catch (e) {
      error(e);
    } finally {
      setBusy(false);
    }
  }
  async function importDocument(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!application) return;
    if (preview) {
      notify("演示模式不会导入真实文档", "info");
      return;
    }
    const data = new FormData(e.currentTarget);
    setBusy(true);
    try {
      const d = await post<KnowledgeDocument>("/knowledge", {
        application_id: application.id,
        title: data.get("title"),
        body: data.get("body"),
        source: data.get("source"),
        version: 1,
        metadata: metadataFromForm(data),
      });
      await qc.invalidateQueries({ queryKey: ["knowledge"] });
      setSelectedId(d.id);
      setModal(null);
      notify(
        d.status === "failed"
          ? "原文已保存，索引失败，可稍后重试"
          : "文档已保存",
        "success",
      );
    } catch (e) {
      error(e);
    } finally {
      setBusy(false);
    }
  }
  async function createDraft(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (preview) {
      notify("演示模式不会创建真实经验草稿", "info");
      return;
    }
    if (
      !investigationId ||
      investigation.data?.application_id !== application?.id
    ) {
      notify("请先选择当前应用的调查", "error");
      return;
    }
    const data = new FormData(e.currentTarget);
    const evidenceIds = data.getAll("evidence_id");
    if (!evidenceIds.length) {
      notify("至少选择一项来源证据", "error");
      return;
    }
    setBusy(true);
    try {
      await post(`/investigations/${investigationId}/knowledge-drafts`, {
        title: data.get("title"),
        body: data.get("body"),
        evidence_ids: evidenceIds,
      });
      await qc.invalidateQueries({ queryKey: ["knowledge-drafts"] });
      setModal(null);
      notify("经验草稿已保存，人工确认后才进入检索", "success");
    } catch (e) {
      error(e);
    } finally {
      setBusy(false);
    }
  }
  async function confirmDraft() {
    if (!selectedDraft) return;
    if (preview) {
      notify("演示模式不会发布真实经验", "info");
      return;
    }
    setBusy(true);
    try {
      const d = await post<KnowledgeDocument>(
        `/knowledge-drafts/${selectedDraft.id}/confirm`,
        {},
      );
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["knowledge"] }),
        qc.invalidateQueries({ queryKey: ["knowledge-drafts"] }),
      ]);
      setSelectedId(d.id);
      setModal(null);
      notify("经验原文已确认保存；索引状态可在文档中查看", "success");
    } catch (e) {
      error(e);
    } finally {
      setBusy(false);
    }
  }
  function cite() {
    if (!selected) return;
    if (
      !preview &&
      (!investigation.data ||
        investigation.data.application_id !== application?.id ||
        (selected.application_id &&
          selected.application_id !== investigation.data.application_id &&
          selected.metadata?.visibility !== "workspace" &&
          !selected.metadata?.shared_application_ids?.includes(
            investigation.data.application_id,
          )))
    ) {
      notify("知识与当前调查不属于同一应用，请重新选择调查。", "error");
      return;
    }
    const branch =
      investigation.data?.branches?.find((b) => b.id === citationBranch)?.id ||
      investigation.data?.branches?.[0]?.id;
    if (!investigationId && !preview) {
      notify("请先在调查工作区选择一项调查", "info");
      return;
    }
    const payload = {
      investigationId: investigationId ?? "preview-investigation",
      branchId: branch,
      documentId: selected.id,
      title: selected.title,
      version: selected.version,
      body: selected.body,
    };
    useDraftStore.getState().setCitation(payload);
    setCitations((s) => [...new Set([...s, selected.title])]);
    setModal(null);
    notify("引用已放入调查草稿，确认发送后才会调用模型", "success");
    navigate(`/investigations/${payload.investigationId}`);
  }
  return (
    <div className="knowledge-page">
      <ScopeFilter single />
      <div className="page-heading">
        <div>
          <h1>Runbook</h1>
          <p className="muted">沉淀运维经验，支持 AIOps 的分析与决策</p>
        </div>
        {preview && <span className="badge">演示资料</span>}
      </div>
      <div
        className={`knowledge-grid ${libraryCollapsed ? "library-is-collapsed" : ""}`}
      >
        <aside className="card knowledge-library">
          <header>
            <h2>{libraryCollapsed ? <BookOpen size={19} /> : "知识库"}</h2>
            <div>
              {!libraryCollapsed && canWrite && (
                <button
                  className="icon-button"
                  title="导入 Runbook"
                  aria-label="导入 Runbook"
                  onClick={() => setModal("import")}
                >
                  <Plus size={18} />
                </button>
              )}
              <button
                className="icon-button"
                onClick={() => setLibraryCollapsed(!libraryCollapsed)}
                aria-label={libraryCollapsed ? "展开知识库" : "折叠知识库"}
              >
                {libraryCollapsed ? (
                  <ChevronDown size={16} />
                ) : (
                  <ChevronUp size={16} />
                )}
              </button>
            </div>
          </header>
          {!libraryCollapsed && (
            <>
              <label className="knowledge-search">
                <Search size={16} />
                <input
                  aria-label="搜索文档"
                  placeholder="搜索文档…"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                />
              </label>
              {docs.isLoading && <p className="muted">加载知识库…</p>}
              {docs.isError && (
                <p className="knowledge-error" role="alert">
                  知识库读取失败，请重试。
                  <button className="btn" onClick={() => docs.refetch()}>
                    重试
                  </button>
                </p>
              )}
              {groups.map((g) => (
                <details key={g.name} open className="knowledge-tree">
                  <summary>
                    <Folder size={17} />
                    {g.name}
                  </summary>
                  {g.items
                    .filter((d) => d.title.includes(filter))
                    .map((d) => (
                      <button
                        key={d.id}
                        className={selectedId === d.id ? "selected" : ""}
                        onClick={() => {
                          setSelectedId(d.id);
                          setHitDetail(null);
                        }}
                      >
                        <FileText size={17} />
                        <span>{d.title}</span>
                      </button>
                    ))}
                </details>
              ))}
              {!docs.isLoading && !documents.length && (
                <p className="muted knowledge-empty">
                  还没有 Runbook。导入一份操作手册开始积累。
                </p>
              )}
            </>
          )}
        </aside>
        <main className="card knowledge-reader">
          {selected ? (
            <>
              <div className="knowledge-document-top">
                <div>
                  <h2>{selected.title}</h2>
                  <div className="knowledge-tags">
                    <span
                      className={`badge ${selected.status === "failed" ? "knowledge-warning" : ""}`}
                    >
                      v{selected.version} · {statusLabel(selected.status)}
                    </span>
                    <span className="badge">{application?.name ?? "应用"}</span>
                    {(environment?.name || application?.environment) && (
                      <span className="badge">
                        {environment?.name || application?.environment}
                      </span>
                    )}
                  </div>
                </div>
                <button
                  className="btn btn-primary"
                  disabled={!canWrite}
                  onClick={() => setModal("citation")}
                >
                  <Quote size={16} />
                  引用到当前调查
                </button>
              </div>
              <KnowledgeGovernance document={selected} canWrite={canWrite} />
              <p className="knowledge-meta">
                来源 {selected.source || "手动导入"}{" "}
                <span>版本 v{selected.version}</span>
                <span>
                  {new Date(selected.created_at).toLocaleDateString("zh-CN")}
                </span>
              </p>
              {selected.error && (
                <div className="knowledge-notice knowledge-warning">
                  <Info size={16} />
                  <span>原文已保存，索引暂不可用：{selected.error}</span>
                  {canWrite && (
                    <button className="btn" onClick={reindex} disabled={busy}>
                      重试索引
                    </button>
                  )}
                </div>
              )}
              {detail.isError && (
                <p role="alert" className="knowledge-error">
                  文档详情未能刷新，当前展示列表中已保存的原文。
                </p>
              )}
              <article className="knowledge-markdown">
                <ReactMarkdown>{selected.body}</ReactMarkdown>
              </article>
              <footer className="knowledge-document-footer">
                <span>
                  索引代次 {selected.generation_id || "待建立"}
                  {selected.embedding_model
                    ? ` · ${selected.embedding_model}`
                    : ""}
                  {selected.dimensions ? ` · ${selected.dimensions} 维` : ""}
                </span>
                {canWrite && (
                  <button
                    className="btn btn-ghost"
                    onClick={reindex}
                    disabled={busy}
                  >
                    <RefreshCw size={14} />
                    重建索引
                  </button>
                )}
              </footer>
            </>
          ) : (
            <div className="empty-state">
              <BookOpen size={32} />
              <h2>让经验成为下一次调查的起点</h2>
              <p>选择或导入一份 Runbook，查看可追溯的原文与版本。</p>
              {canWrite && (
                <button
                  className="btn btn-primary"
                  onClick={() => setModal("import")}
                >
                  导入 Runbook
                </button>
              )}
            </div>
          )}
        </main>
        <aside className="knowledge-related">
          <section className="card">
            <header>
              <h2>相关经验</h2>
              <button
                className="icon-button"
                aria-label={relatedCollapsed ? "展开相关经验" : "折叠相关经验"}
                onClick={() => setRelatedCollapsed(!relatedCollapsed)}
              >
                {relatedCollapsed ? (
                  <ChevronDown size={16} />
                ) : (
                  <ChevronUp size={16} />
                )}
              </button>
            </header>
            {!relatedCollapsed && (
              <>
                <form className="knowledge-search" onSubmit={search}>
                  <Search size={16} />
                  <input
                    aria-label="检索相关经验"
                    placeholder="描述问题并按 Enter 检索…"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                  />
                  <button
                    aria-label="开始检索"
                    className="icon-button"
                    disabled={searching || !query.trim()}
                  >
                    {searching ? (
                      <LoaderCircle size={16} className="spin" />
                    ) : (
                      <ArrowRight size={16} />
                    )}
                  </button>
                </form>
                {retrievalNotice && (
                  <p className="management-notice">{retrievalNotice}</p>
                )}
                <div className="knowledge-results">
                  {hits?.map((h) => (
                    <button
                      className="knowledge-result"
                      key={`${h.document_id}-${h.chunk}`}
                      onClick={() => {
                        if (documents.some((d) => d.id === h.document_id)) {
                          setSelectedId(h.document_id);
                          setHitDetail(null);
                        } else setHitDetail(h);
                      }}
                    >
                      <div>
                        <strong>{h.title}</strong>
                        <span className="badge">{h.source || "知识"}</span>
                      </div>
                      <p>{h.content}</p>
                      <footer>
                        <span>
                          v{h.version} · 检索得分 {h.similarity.toFixed(3)}
                        </span>
                        <span>
                          查看引用 <ArrowRight size={13} />
                        </span>
                      </footer>
                    </button>
                  ))}
                  {hits?.length === 0 && (
                    <p className="muted">没有找到相关资料。尝试调整关键词。</p>
                  )}
                  {hits === null && (
                    <p className="muted knowledge-empty">
                      按应用范围检索 Runbook
                      和已确认经验，结果保留原文、版本与引用。
                    </p>
                  )}
                </div>
              </>
            )}
          </section>
          <section className="card knowledge-drafts">
            <header>
              <h2>
                <Info size={18} />
                待沉淀经验
              </h2>
              {canWrite && (investigationId || preview) && (
                <button
                  className="icon-button"
                  aria-label="创建经验草稿"
                  onClick={() => setModal("new-draft")}
                >
                  <Plus size={16} />
                </button>
              )}
              <button
                className="icon-button"
                aria-label={draftCollapsed ? "展开经验草稿" : "折叠经验草稿"}
                onClick={() => setDraftCollapsed(!draftCollapsed)}
              >
                {draftCollapsed ? (
                  <ChevronDown size={16} />
                ) : (
                  <ChevronUp size={16} />
                )}
              </button>
            </header>
            {!draftCollapsed && (
              <>
                {canWrite &&
                  !preview &&
                  investigation.data?.status === "closed" && (
                    <button
                      className="btn"
                      disabled={busy}
                      onClick={async () => {
                        setBusy(true);
                        try {
                          await post(
                            `/investigations/${investigationId}/knowledge-drafts/generate`,
                            {},
                          );
                          await qc.invalidateQueries({
                            queryKey: ["knowledge-drafts"],
                          });
                          notify(
                            "经验草稿已生成，请检查事实和来源后确认",
                            "success",
                          );
                        } catch (e) {
                          error(e);
                        } finally {
                          setBusy(false);
                        }
                      }}
                    >
                      {busy ? "正在生成…" : "让 AI 生成经验草稿"}
                    </button>
                  )}
                {drafts.isError && (
                  <p className="knowledge-error">草稿读取失败。</p>
                )}
                {(drafts.data?.items ?? []).map((d) => (
                  <button
                    className="knowledge-result"
                    key={d.id}
                    onClick={() => {
                      setSelectedDraft(d);
                      setModal("draft");
                    }}
                  >
                    <div>
                      <strong>{d.title}</strong>
                      <span className="badge">
                        {d.document_id ? "已确认" : "草稿"}
                      </span>
                    </div>
                    <p>{d.body}</p>
                    <footer>
                      <span>{d.evidence_ids.length} 项来源证据</span>
                      <span>
                        查看草稿 <ArrowRight size={13} />
                      </span>
                    </footer>
                  </button>
                ))}
                {!drafts.data?.items?.length && (
                  <p className="muted knowledge-empty">
                    {investigationId
                      ? "当前调查尚无经验草稿。可在调查结束后确认经验。"
                      : "选择一项调查后查看经验草稿。"}
                  </p>
                )}
              </>
            )}
          </section>
        </aside>
      </div>
      <div className="card knowledge-citations">
        <FileText size={18} />
        <strong>本次已放入调查草稿 · {citations.length} 项</strong>
        <span className="muted">
          {citations.join("、") || "引用前可审阅原文，发送由你确认"}
        </span>
      </div>
      {modal === "import" && (
        <KnowledgeModal title="新建 Runbook" onClose={() => setModal(null)}>
          <form onSubmit={importDocument} className="knowledge-form">
            <p className="muted">
              选择模板或从空白开始。保存后由维护人审阅发布，才会用于知识检索。
            </p>
            <label>
              标题
              <input className="field" name="title" required maxLength={200} />
            </label>
            <label>
              来源
              <input
                className="field"
                name="source"
                placeholder="文档链接或运维团队"
                maxLength={2048}
              />
            </label>
            <TemplateFields />
            <footer>
              <button
                type="button"
                className="btn"
                onClick={() => setModal(null)}
              >
                取消
              </button>
              <button
                className="btn btn-primary"
                disabled={busy || !application}
              >
                {busy ? "保存中…" : "保存草稿"}
              </button>
            </footer>
          </form>
        </KnowledgeModal>
      )}
      {modal === "new-draft" && (
        <KnowledgeModal title="创建经验草稿" onClose={() => setModal(null)}>
          <form className="knowledge-form" onSubmit={createDraft}>
            <p className="muted">
              保留真实调查证据与经验原文；草稿不会自动进入知识检索。
            </p>
            <label>
              标题
              <input className="field" name="title" required maxLength={200} />
            </label>
            <label>
              经验正文
              <textarea className="field" name="body" required rows={8} />
            </label>
            <fieldset className="knowledge-evidence-choices">
              <legend>来源证据（至少选择一项）</legend>
              {(preview
                ? [{ id: "preview-evidence", title: "连接池变更 · 演示证据" }]
                : (investigation.data?.evidence ?? [])
              ).map((e) => (
                <label key={e.id}>
                  <input type="checkbox" name="evidence_id" value={e.id} />
                  {e.title}
                </label>
              ))}
              {!preview && !investigation.data?.evidence?.length && (
                <p className="muted">
                  当前调查还没有证据，取证后可保存经验草稿。
                </p>
              )}
            </fieldset>
            <footer>
              <button
                type="button"
                className="btn"
                onClick={() => setModal(null)}
              >
                取消
              </button>
              <button
                className="btn btn-primary"
                disabled={
                  busy || (!preview && !investigation.data?.evidence?.length)
                }
              >
                保存草稿
              </button>
            </footer>
          </form>
        </KnowledgeModal>
      )}
      {modal === "draft" && selectedDraft && (
        <KnowledgeModal
          title={selectedDraft.title}
          onClose={() => setModal(null)}
        >
          <article className="knowledge-markdown">
            <ReactMarkdown>{selectedDraft.body}</ReactMarkdown>
          </article>
          <p className="muted">
            来源证据：{selectedDraft.evidence_ids.join("、")}
          </p>
          <div className="knowledge-notice">
            <Info size={16} />
            <span>调查关闭后，由操作员人工确认，才会加入可检索知识库。</span>
          </div>
          <button
            className="btn btn-primary"
            onClick={confirmDraft}
            disabled={
              busy ||
              !canWrite ||
              !!selectedDraft.document_id ||
              (!preview && investigation.data?.status !== "closed")
            }
          >
            <Check size={16} />
            {selectedDraft.document_id ? "已确认发布" : "审核并发布"}
          </button>
        </KnowledgeModal>
      )}
      {modal === "citation" && selected && (
        <KnowledgeModal title="引用到调查草稿" onClose={() => setModal(null)}>
          <p>
            将「{selected.title}」v{selected.version}{" "}
            的原文与来源加入所选分支的待发送草稿。
          </p>
          {!preview && investigation.data?.branches && (
            <label className="knowledge-form">
              调查分支
              <select
                className="field"
                value={citationBranch || investigation.data.branches[0]?.id}
                onChange={(e) => setCitationBranch(e.target.value)}
              >
                {investigation.data.branches.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.title}
                  </option>
                ))}
              </select>
            </label>
          )}
          <p className="muted">此操作不会启动模型，也不会直接执行 Runbook。</p>
          <button
            className="btn btn-primary"
            onClick={cite}
            disabled={
              !preview &&
              (!investigationId ||
                !investigation.data?.branches?.length ||
                investigation.data.application_id !== application?.id)
            }
          >
            放入草稿并查看
          </button>
        </KnowledgeModal>
      )}
      {hitDetail && (
        <KnowledgeModal
          title={hitDetail.title}
          onClose={() => setHitDetail(null)}
        >
          <span className="badge">
            检索片段 · v{hitDetail.version} · 代次 {hitDetail.generation_id}
          </span>
          <article className="knowledge-markdown">
            <ReactMarkdown>{hitDetail.content}</ReactMarkdown>
          </article>
          <p className="muted">
            来源：{hitDetail.source} · 文档 {hitDetail.document_id}
          </p>
          <button
            className="btn"
            onClick={() => {
              if (
                preview &&
                !documents.some((d) => d.id === hitDetail.document_id)
              ) {
                notify("此演示仅包含引用片段，未提供完整原文", "info");
                return;
              }
              setSelectedId(hitDetail.document_id);
              setHitDetail(null);
            }}
          >
            查看完整文档
          </button>
        </KnowledgeModal>
      )}
    </div>
  );
}
