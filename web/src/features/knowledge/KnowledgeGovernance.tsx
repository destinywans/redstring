import SelectField from "../../components/SelectField";
import { memberLabel } from "../../lib/members";
import { useState, useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { request, post, put } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { Modal, ErrorNotice } from "../../components/ui";
import type { KnowledgeDocument } from "./fixtures";
import type { User, Team } from "../../lib/types";
import ReactMarkdown from "react-markdown";
import "../workspace/workspace.css";
export type KnowledgeMetadata = {
  maintainer_id: string;
  team_id?: string;
  origin?: string;
  actor_id?: string;
  generation_run_id?: string;
  generation_model?: string;
  evidence_ids?: string[];
  environment_ids?: string[];
  resource_categories?: string[];
  tags?: string[];
  shared_application_ids?: string[];
  visibility?: string;
};
const life = (s?: string) =>
  ({
    draft: "草稿",
    review: "待审阅",
    published: "已发布",
    archived: "已归档",
  })[s ?? ""] ?? "历史文档";
export function MetadataFields({ metadata }: { metadata?: KnowledgeMetadata }) {
  const { user, preview, application, applications } = useWorkspace();
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => request<{ items: User[] }>("/users"),
    enabled: !preview && user.role === "admin",
  });
  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => request<{ items: Team[] }>("/teams"),
    enabled: !preview && user.role === "admin",
  });
  return (
    <>
      <div className="management-form-grid">
        <label>
          维护人
          {user.role === "admin" ? (
            <SelectField
              className="field"
              name="maintainer_id"
              required
              defaultValue={metadata?.maintainer_id || user.id}
            >
              <option value={user.id}>{user.name}</option>
              {users.data?.items
                .filter((u) => u.id !== user.id)
                .map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.name}
                  </option>
                ))}
            </SelectField>
          ) : (
            <input
              name="maintainer_id"
              className="field"
              required
              defaultValue={metadata?.maintainer_id || user.id}
            />
          )}
        </label>
        <label>
          所属团队
          <SelectField
            name="team_id"
            className="field"
            defaultValue={metadata?.team_id || application?.owner_team_id || ""}
          >
            <option value="">未指定</option>
            {(teams.data?.items ?? []).map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
            {user.role !== "admin" &&
              (metadata?.team_id || application?.owner_team_id) && (
                <option value={metadata?.team_id || application?.owner_team_id}>
                  {metadata?.team_id || application?.owner_team_id}
                </option>
              )}
          </SelectField>
        </label>
      </div>
      <label>
        资源分类（逗号分隔）
        <input
          name="resource_categories"
          className="field"
          defaultValue={metadata?.resource_categories?.join(", ")}
          placeholder="应用, PostgreSQL, Redis, Kubernetes"
        />
      </label>
      <label>
        问题标签（逗号分隔）
        <input
          name="tags"
          className="field"
          defaultValue={metadata?.tags?.join(", ")}
          placeholder="连接超时, 发布回滚"
        />
      </label>
      <fieldset>
        <legend>适用环境（不选表示应用内所有环境）</legend>
        {application?.environments?.map((e) => (
          <label className="checkbox-label" key={e.id}>
            <input
              name="environment_ids"
              type="checkbox"
              value={e.id}
              defaultChecked={metadata?.environment_ids?.includes(e.id)}
            />
            {e.name}
          </label>
        ))}
      </fieldset>
      <label>
        共享范围
        <SelectField
          className="field"
          name="visibility"
          defaultValue={metadata?.visibility || "application"}
        >
          <option value="application">当前应用与明确共享的应用</option>
          {user.role === "admin" && (
            <option value="workspace">工作区公开（管理员发布）</option>
          )}
        </SelectField>
      </label>
      <fieldset>
        <legend>明确共享给其他应用</legend>
        {applications
          .filter((a) => a.id !== application?.id)
          .map((a) => (
            <label className="checkbox-label" key={a.id}>
              <input
                name="shared_application_ids"
                type="checkbox"
                value={a.id}
                defaultChecked={metadata?.shared_application_ids?.includes(
                  a.id,
                )}
              />
              {a.name}
            </label>
          ))}
      </fieldset>
    </>
  );
}
export function metadataFromForm(
  f: FormData,
  previous?: KnowledgeMetadata,
): KnowledgeMetadata {
  const split = (name: string) =>
    String(f.get(name) || "")
      .split(/[,，]/)
      .map((x) => x.trim())
      .filter(Boolean);
  return {
    ...previous,
    maintainer_id: String(f.get("maintainer_id") || ""),
    team_id: String(f.get("team_id") || ""),
    resource_categories: split("resource_categories"),
    tags: split("tags"),
    environment_ids: f.getAll("environment_ids").map(String),
    shared_application_ids: f.getAll("shared_application_ids").map(String),
    visibility: String(f.get("visibility") || "application"),
  };
}
export function TemplateFields() {
  const { preview } = useWorkspace();
  const [template, setTemplate] = useState("blank"),
    [body, setBody] = useState("");
  const q = useQuery({
    queryKey: ["knowledge-templates"],
    queryFn: () =>
      request<{ items: { id: string; title: string; body: string }[] }>(
        "/knowledge/templates",
      ),
    enabled: !preview,
  });
  return (
    <>
      {q.error && <ErrorNotice error={q.error} />}
      <label>
        创建方式
        <SelectField
          className="field"
          value={template}
          onChange={(e) => {
            setTemplate(e.target.value);
            setBody(
              (q.data?.items ?? []).find((t) => t.id === e.target.value)
                ?.body ?? "",
            );
          }}
        >
          <option value="blank">空白 Runbook</option>
          {q.data?.items
            .filter((t) => t.id !== "blank")
            .map((t) => (
              <option key={t.id} value={t.id}>
                {t.title}
              </option>
            ))}
        </SelectField>
      </label>
      <label>
        正文（Markdown）
        <textarea
          className="field"
          name="body"
          value={body}
          onChange={(e) => setBody(e.target.value)}
          required
          rows={12}
        />
      </label>
      <MetadataFields />
    </>
  );
}
export default function KnowledgeGovernance({
  document: d,
  canWrite,
}: {
  document: KnowledgeDocument;
  canWrite: boolean;
}) {
  const { preview, user, notify } = useWorkspace(),
    qc = useQueryClient();
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => request<{ items: User[] | null }>("/users"),
    enabled: !preview && user.role === "admin",
  });
  const [edit, setEdit] = useState(false),
    [history, setHistory] = useState(false),
    [version, setVersion] = useState<number | null>(null),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    setEdit(false);
    setHistory(false);
    setVersion(null);
  }, [d.id]);
  const revisions = useQuery({
    queryKey: ["knowledge-revisions", d.id],
    queryFn: () =>
      request<{ items: KnowledgeDocument[] }>(`/knowledge/${d.id}/revisions`),
    enabled: history && !preview,
  });
  async function refresh() {
    await Promise.all([
      qc.invalidateQueries({ queryKey: ["knowledge"] }),
      qc.invalidateQueries({ queryKey: ["knowledge-document", d.id] }),
      qc.invalidateQueries({ queryKey: ["knowledge-revisions", d.id] }),
    ]);
  }
  async function transition(state: string) {
    if (preview) {
      notify("设计预览不变更文档状态");
      return;
    }
    setBusy(true);
    try {
      await post(`/knowledge/${d.id}/transition`, {
        expected_version: d.version,
        state,
      });
      await refresh();
      notify("文档状态已更新", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  async function save(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (preview) return;
    const f = new FormData(e.currentTarget);
    setBusy(true);
    try {
      await put(`/knowledge/${d.id}`, {
        expected_version: d.version,
        title: f.get("title"),
        body: f.get("body"),
        source: f.get("source"),
        metadata: metadataFromForm(f, d.metadata),
      });
      await refresh();
      setEdit(false);
      notify("新修订已保存为草稿", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  async function exportDocument() {
    if (preview) return;
    try {
      const data = await request(`/knowledge/${d.id}/export`),
        url = URL.createObjectURL(
          new Blob([JSON.stringify(data, null, 2)], {
            type: "application/json",
          }),
        ),
        a = document.createElement("a");
      a.href = url;
      a.download = `runbook-${d.id}.json`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      notify(String(e), "error");
    }
  }
  const revision = (revisions.data?.items ?? []).find(
    (r) => r.version === version,
  );
  return (
    <div className="governance-strip">
      <div className="management-meta">
        <span className="badge">{life(d.lifecycle)}</span>
        <span className="badge">
          {d.metadata?.origin === "agent"
            ? "Agent 生成"
            : d.metadata?.origin === "human"
              ? "人工创建"
              : "历史来源未记录"}
        </span>
        <span className="muted">
          维护人{" "}
          {memberLabel(
            d.metadata?.maintainer_id,
            user,
            user.role === "admin" ? (users.data?.items ?? []) : [],
          )}
        </span>
        {d.metadata?.resource_categories?.map((c) => (
          <span className="badge" key={c}>
            {c}
          </span>
        ))}
        {d.metadata?.tags?.map((c) => (
          <span className="badge" key={c}>
            {c}
          </span>
        ))}
      </div>
      {d.metadata?.generation_run_id && (
        <p className="muted">
          来源任务 {d.metadata.generation_run_id} ·{" "}
          {d.metadata.generation_model}
        </p>
      )}
      <div className="management-actions">
        {canWrite && (
          <button className="btn" onClick={() => setEdit(true)}>
            编辑新修订
          </button>
        )}
        {canWrite && (!d.lifecycle || d.lifecycle === "draft") && (
          <button
            className="btn"
            disabled={busy}
            onClick={() => void transition("review")}
          >
            提交审阅
          </button>
        )}
        {canWrite && d.lifecycle === "review" && (
          <button
            className="btn btn-primary"
            disabled={busy}
            onClick={() => void transition("published")}
          >
            确认并发布
          </button>
        )}
        {canWrite && d.lifecycle !== "archived" && (
          <button
            className="btn"
            disabled={busy}
            onClick={() => void transition("archived")}
          >
            归档
          </button>
        )}
        {canWrite && d.lifecycle === "archived" && (
          <button
            className="btn"
            disabled={busy}
            onClick={() => void transition("draft")}
          >
            恢复为草稿
          </button>
        )}
        <button className="btn" onClick={() => setHistory(true)}>
          修订历史
        </button>
        <button
          className="btn"
          disabled={preview}
          onClick={() => void exportDocument()}
        >
          导出原文与修订
        </button>
      </div>
      {d.lifecycle !== "published" && (
        <small className="muted">只有已发布的修订可以进入知识召回。</small>
      )}
      <Modal
        open={edit}
        onOpenChange={setEdit}
        title="编辑 Runbook · 创建新修订"
      >
        <form className="management-form" onSubmit={save}>
          <p className="muted">
            以 v{d.version} 为基础保存；原修订与历史引用保持不变。
          </p>
          <label>
            标题
            <input
              className="field"
              name="title"
              required
              defaultValue={d.title}
            />
          </label>
          <label>
            来源
            <input className="field" name="source" defaultValue={d.source} />
          </label>
          <label>
            正文
            <textarea
              className="field"
              name="body"
              rows={12}
              required
              defaultValue={d.body}
            />
          </label>
          <MetadataFields metadata={d.metadata} />
          <footer>
            <button className="btn btn-primary" disabled={busy}>
              保存为新草稿
            </button>
          </footer>
        </form>
      </Modal>
      <Modal open={history} onOpenChange={setHistory} title="修订历史">
        {revisions.error && <ErrorNotice error={revisions.error} />}
        <div className="management-actions">
          {(revisions.data?.items ?? []).map((r) => (
            <button
              key={r.version}
              className="btn"
              onClick={() => setVersion(r.version)}
            >
              v{r.version} · {life(r.lifecycle)}
            </button>
          ))}
        </div>
        {revision ? (
          <article className="knowledge-markdown">
            <h3>{revision.title}</h3>
            <ReactMarkdown>{revision.body}</ReactMarkdown>
          </article>
        ) : (
          <p className="management-empty">选择修订查看原文。</p>
        )}
      </Modal>
    </div>
  );
}
