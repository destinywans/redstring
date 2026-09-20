import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { request } from "../../lib/api";
import { Modal, ErrorNotice } from "../../components/ui";
import TelemetryResult, {
  type TelemetryResultData,
} from "../overview/TelemetryResult";
import { type AutomationRun, statusName, timeLabel } from "./operations";

function EvidenceContent({ content }: { content: string }) {
  let parsed: unknown;
  try {
    parsed = JSON.parse(content);
  } catch {
    /* Plain-text evidence is retained as text. */
  }
  if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
    const record = parsed as Record<string, unknown>;
    if (Array.isArray(record.series) || Array.isArray(record.entries))
      return <TelemetryResult result={record as TelemetryResultData} />;
  }
  return <p className="automation-evidence-content">{content}</p>;
}
export function AutomationResult({ run }: { run: AutomationRun }) {
  const result = run.result;
  if (!result?.state && !result?.summary && !result?.evidence?.length)
    return null;
  return (
    <details className="automation-result">
      <summary>
        查看{run.kind === "patrol" ? "巡检" : "任务"}结论与证据（
        {result.evidence?.length ?? 0} 项）
      </summary>
      {result.state && (
        <span className="badge">{statusName(result.state)}</span>
      )}
      {result.summary && (
        <p className="automation-evidence-content">{result.summary}</p>
      )}
      {result.state === "clear" && (
        <p className="muted">
          仅表示本次检查范围内未发现异常，不代表全部服务健康。
        </p>
      )}
      {result.state === "suspected" && (
        <p className="muted">
          这是 AI 提出的疑似异常，需要结合证据进一步调查。
        </p>
      )}
      {(result.evidence ?? []).map((e, index) => (
        <details className="automation-evidence" key={`${e.kind}-${index}`}>
          <summary>{e.title || `证据 ${index + 1}`}</summary>
          <small className="muted">
            {e.kind} · 来源 {e.source || "未记录"}
          </small>
          <EvidenceContent content={e.content} />
        </details>
      ))}
    </details>
  );
}
export function IncidentEvidence({
  incidentId,
  environmentId,
}: {
  incidentId: string;
  environmentId: string;
}) {
  const [open, setOpen] = useState(false);
  const q = useQuery({
    queryKey: ["automation-runs", environmentId],
    queryFn: () =>
      request<{ items: AutomationRun[] }>(
        `/automation/runs?environment_id=${encodeURIComponent(environmentId)}`,
      ),
    enabled: open,
  });
  const runs = (q.data?.items ?? []).filter(
    (r) => r.incident_id === incidentId,
  );
  return (
    <>
      <button className="btn" onClick={() => setOpen(true)}>
        查看巡检证据
      </button>
      <Modal open={open} onOpenChange={setOpen} title="异常来源 · 自动巡检">
        {q.error && <ErrorNotice error={q.error} />}
        {runs.map((r) => (
          <section key={r.id} className="management-section">
            <h3>
              {timeLabel(r.created_at)} · {statusName(r.status)}
            </h3>
            {r.error && <p className="management-notice">{r.error}</p>}
            <AutomationResult run={r} />
          </section>
        ))}
        {!runs.length && !q.error && (
          <p className="management-empty">
            {q.isPending
              ? "正在读取巡检记录…"
              : "近期记录中未找到对应巡检证据。"}
          </p>
        )}
      </Modal>
    </>
  );
}
