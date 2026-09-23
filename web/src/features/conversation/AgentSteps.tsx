import { useState, type ReactNode } from "react";
import {
  Check,
  ChevronRight,
  Database,
  ExternalLink,
  LoaderCircle,
  Sparkles,
  TriangleAlert,
} from "../../lib/icons";
import {
  formatDuration,
  formatTokens,
  stepDuration,
  stepLabel,
  summarize,
  traceLink,
  type RunStep,
} from "./steps";

function StepStatus({ step }: { step: RunStep }) {
  if (step.status === "running")
    return <LoaderCircle size={13} className="cv-spin" aria-label="进行中" />;
  if (step.status === "failed")
    return <TriangleAlert size={13} aria-label="失败" />;
  return <Check size={13} aria-label="完成" />;
}

function StepRow({
  step,
  traceTemplate,
  renderEvidence,
}: {
  step: RunStep;
  traceTemplate: string;
  renderEvidence?: (id: string) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const duration = stepDuration(step);
  const evidence = step.evidence_id ? renderEvidence?.(step.evidence_id) : null;
  const detail =
    step.kind === "tool"
      ? step.input || "（无查询参数）"
      : step.tool_calls?.length
        ? `请求工具：${step.tool_calls.join("、")}`
        : step.output
          ? "生成回答"
          : "";
  const expandable = !!(step.error || step.output || evidence || step.input);
  return (
    <li className={`cv-step is-${step.status} is-${step.kind}`}>
      <button
        type="button"
        className="cv-step-head"
        aria-expanded={expandable ? open : undefined}
        disabled={!expandable}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="cv-step-icon">
          {step.kind === "tool" ? <Database size={13} /> : <Sparkles size={13} />}
        </span>
        <span className="cv-step-name">{stepLabel(step)}</span>
        <span className="cv-step-detail">{detail}</span>
        <span className="cv-step-meta">
          {step.input_tokens != null && (
            <span title="provider 报告的输入 / 输出 token">
              {formatTokens(step.input_tokens)}→
              {formatTokens(step.output_tokens ?? 0)}
            </span>
          )}
          {duration != null && <span>{formatDuration(duration)}</span>}
          <StepStatus step={step} />
        </span>
      </button>
      {open && (
        <div className="cv-step-body">
          {step.kind === "tool" && step.input && (
            <p>
              <b>查询</b>
              <code>{step.input}</code>
            </p>
          )}
          {step.error && <p className="cv-step-error">{step.error}</p>}
          {step.kind === "tool" && step.output && (
            <p>
              <b>结果</b>
              {step.output}
            </p>
          )}
          {evidence}
          {traceLink(traceTemplate, step.trace_id) && (
            <a
              className="cv-step-trace"
              href={traceLink(traceTemplate, step.trace_id)}
              target="_blank"
              rel="noreferrer"
            >
              在 Trace 平台查看 <ExternalLink size={12} />
            </a>
          )}
        </div>
      )}
    </li>
  );
}

/**
 * What the agent did for one answer: model calls and read-only tool calls,
 * collapsed to a one-line summary. Tool results expand into their evidence.
 */
export default function AgentSteps({
  steps,
  traceTemplate = "",
  renderEvidence,
  defaultOpen = false,
}: {
  steps: RunStep[];
  traceTemplate?: string;
  renderEvidence?: (id: string) => ReactNode;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  if (!steps.length) return null;
  const s = summarize(steps);
  const expanded = open || s.running;
  return (
    <div className={`cv-steps ${expanded ? "is-open" : ""}`}>
      <button
        type="button"
        className="cv-steps-summary"
        aria-expanded={expanded}
        onClick={() => setOpen((v) => !v)}
      >
        <ChevronRight size={13} className="cv-steps-chevron" />
        {s.running ? (
          <span className="cv-steps-live">
            <LoaderCircle size={13} className="cv-spin" />
            正在执行 · 第 {steps.length} 步
          </span>
        ) : (
          <span>
            {s.tools ? `调用 ${s.tools} 个工具` : "直接回答"} · {s.models}{" "}
            次推理
          </span>
        )}
        {s.failed > 0 && <span className="cv-steps-failed">{s.failed} 步失败</span>}
        <span className="cv-steps-meta">
          {s.tokens != null && <span>{formatTokens(s.tokens)} tokens</span>}
          {s.duration != null && <span>{formatDuration(s.duration)}</span>}
        </span>
      </button>
      {expanded && (
        <ol className="cv-step-list">
          {steps.map((step) => (
            <StepRow
              key={step.id}
              step={step}
              traceTemplate={traceTemplate}
              renderEvidence={renderEvidence}
            />
          ))}
        </ol>
      )}
    </div>
  );
}
