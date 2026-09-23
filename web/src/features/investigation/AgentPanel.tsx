import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ExternalLink, GitBranch } from "../../lib/icons";
import AgentSteps from "../conversation/AgentSteps";
import {
  formatDuration,
  formatTokens,
  runStepsQuery,
  summarize,
  traceLink,
  type RunStep,
} from "../conversation/steps";
import ContextInspector from "./ContextInspector";
import type { CanvasBranch, CanvasRun } from "./preview";

const runStatus: Record<string, string> = {
  queued: "排队中",
  running: "执行中",
  cancelling: "取消中",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
};

/**
 * Agent view of one branch: which run and attempt produced the context, what
 * the agent called, what it cost, and where the full trace lives.
 */
export default function AgentPanel({
  branch,
  runs,
  preview,
  previewSteps,
  renderEvidence,
}: {
  branch: CanvasBranch;
  runs: CanvasRun[];
  preview: boolean;
  previewSteps?: RunStep[];
  renderEvidence?: (id: string) => ReactNode;
}) {
  const ordered = [...runs].sort((a, b) => b.created_at.localeCompare(a.created_at));
  const [chosen, setChosen] = useState("");
  const run = ordered.find((r) => r.id === chosen) ?? ordered[0];
  const query = useQuery(runStepsQuery(run?.id ?? "", !preview));
  const steps = preview ? (previewSteps ?? []) : (query.data?.items ?? []);
  const totals = summarize(steps);
  const trace = traceLink(
    query.data?.trace_url_template ?? "",
    steps.find((s) => s.trace_id)?.trace_id,
  );
  if (!run && !preview)
    return <p className="muted">这个分支还没有 Agent 运行记录。</p>;
  return (
    <div className="inv-agent-panel">
      <section className="inv-agent-chain" aria-label="状态链">
        <span>
          <GitBranch size={13} />
          {branch.title}
        </span>
        <span>上下文 v{branch.revision}</span>
        <span>运行 {preview ? "预览" : run.id.slice(0, 8)}</span>
        <span>尝试 {preview ? 1 : run.attempt}</span>
      </section>
      {!preview && ordered.length > 1 && (
        <label className="inv-agent-run">
          运行记录
          <select value={run.id} onChange={(e) => setChosen(e.target.value)}>
            {ordered.map((r) => (
              <option key={r.id} value={r.id}>
                {new Date(r.created_at).toLocaleString("zh-CN")} ·{" "}
                {runStatus[r.status] ?? r.status}
              </option>
            ))}
          </select>
        </label>
      )}
      <dl className="inv-agent-stats">
        <div>
          <dt>状态</dt>
          <dd>{preview ? "已完成" : (runStatus[run.status] ?? run.status)}</dd>
        </div>
        <div>
          <dt>工具调用</dt>
          <dd>{totals.tools}</dd>
        </div>
        <div>
          <dt>模型推理</dt>
          <dd>{totals.models}</dd>
        </div>
        <div>
          <dt>Token</dt>
          <dd title="provider 报告的用量；未报告时不估算">
            {totals.tokens == null ? "未报告" : formatTokens(totals.tokens)}
          </dd>
        </div>
        <div>
          <dt>耗时</dt>
          <dd>{totals.duration == null ? "—" : formatDuration(totals.duration)}</dd>
        </div>
      </dl>
      {!preview && run.error && <p className="inv-error">{run.error}</p>}
      <h3>执行步骤</h3>
      {steps.length ? (
        <AgentSteps
          key={run?.id}
          steps={steps}
          traceTemplate={query.data?.trace_url_template}
          renderEvidence={renderEvidence}
          defaultOpen
        />
      ) : (
        <p className="muted">
          {query.isPending && !preview
            ? "正在读取步骤…"
            : "没有步骤记录（该运行早于步骤记录功能，或尚未开始）。"}
        </p>
      )}
      <div className="inv-agent-links">
        {!preview && <ContextInspector runId={run.id} />}
        {trace ? (
          <a className="btn" href={trace} target="_blank" rel="noreferrer">
            完整 Trace <ExternalLink size={13} />
          </a>
        ) : (
          <span className="muted" title="设置 OTEL_EXPORTER_OTLP_ENDPOINT 与 AIOPS_TRACE_UI_URL 后可跳转">
            未配置 Trace 平台
          </span>
        )}
      </div>
    </div>
  );
}
