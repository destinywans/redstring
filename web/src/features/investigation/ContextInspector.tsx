import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError, request } from "../../lib/api";
import { Modal, ErrorNotice } from "../../components/ui";
import { useWorkspace } from "../../lib/workspace";
import "../workspace/workspace.css";
type Manifest = {
  run_id: string;
  attempt_id: string;
  model_id: string;
  context_revision: number;
  input_budget: number;
  estimated_tokens: number;
  estimation: string;
  summary: string;
  summary_source_ids: string[];
  recent_message_ids: string[];
  evidence_ids: string[];
  messages: { role: string; content: string }[];
  created_at: string;
  summary_calls?: number;
};
export default function ContextInspector({ runId }: { runId: string }) {
  const { preview } = useWorkspace();
  const [open, setOpen] = useState(false);
  const q = useQuery({
    queryKey: ["run-context", runId],
    queryFn: () => request<Manifest>(`/runs/${runId}/context`),
    enabled: open && !preview,
    retry: false,
  });
  const m = q.data;
  return (
    <>
      <button className="btn btn-ghost" onClick={() => setOpen(true)}>
        本轮上下文
      </button>
      <Modal open={open} onOpenChange={setOpen} title="本轮实际上下文">
        <p className="muted">
          模型切换沿用已提交的消息和证据。摘要是派生信息，原始历史始终保留。
        </p>
        {q.error instanceof ApiError && q.error.status === 404 ? (
          <p className="management-empty">
            任务尚未准备上下文，开始执行后可在这里查看。
          </p>
        ) : q.error ? (
          <ErrorNotice error={q.error} />
        ) : null}
        {preview && (
          <p className="management-empty">设计预览没有真实模型输入。</p>
        )}
        {m && (
          <>
            <div className="management-meta">
              <span className="badge">{m.model_id}</span>
              <span className="badge">上下文 v{m.context_revision}</span>
              <span className="badge">
                {m.estimated_tokens.toLocaleString()} /{" "}
                {m.input_budget.toLocaleString()} 输入预算
              </span>
            </div>
            <p className="muted">
              预算使用保守 UTF-8 字节估计，并非提供商实际 token 用量。摘要调用：
              {m.summary_calls ?? 0}。
            </p>
            <h3 className="management-section">历史摘要</h3>
            <p style={{ whiteSpace: "pre-wrap" }}>
              {m.summary || "本轮无需摘要，历史在预算内完整传入。"}
            </p>
            <details className="management-section">
              <summary>
                来源记录 · 摘要覆盖 {m.summary_source_ids?.length ?? 0} 条 /
                最近消息 {m.recent_message_ids?.length ?? 0} 条 / 证据{" "}
                {m.evidence_ids?.length ?? 0} 条
              </summary>
              <p className="muted">
                摘要来源：{m.summary_source_ids?.join(" · ") || "无"}
              </p>
              <p className="muted">
                最近消息：{m.recent_message_ids?.join(" · ") || "无"}
              </p>
              <p className="muted">
                证据：{m.evidence_ids?.join(" · ") || "无"}
              </p>
            </details>
            <h3 className="management-section">提交给模型的消息</h3>
            {m.messages.map((msg, i) => (
              <details key={i} className="management-section">
                <summary>
                  {i + 1}. {msg.role} · {msg.content.length} 字符
                </summary>
                <div
                  style={{
                    whiteSpace: "pre-wrap",
                    overflowWrap: "anywhere",
                    maxHeight: 350,
                    overflow: "auto",
                  }}
                >
                  {msg.content}
                </div>
              </details>
            ))}
          </>
        )}
      </Modal>
    </>
  );
}
