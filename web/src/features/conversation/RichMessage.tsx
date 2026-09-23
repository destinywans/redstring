import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Quote } from "../../lib/icons";
import AgentSteps from "./AgentSteps";
import { runStepsQuery, type RunStep } from "./steps";
import "./conversation.css";

export type Citation = { id: string; title: string; kind?: string };

const markdown: Components = {
  a: ({ node: _node, ...props }) => (
    <a {...props} target="_blank" rel="noreferrer" />
  ),
  table: ({ node: _node, ...props }) => (
    <div className="cv-table-wrap">
      <table {...props} />
    </div>
  ),
};

/** Markdown body shared by chat bubbles and canvas cards. */
export function MarkdownBody({
  content,
  className,
}: {
  content: string;
  className?: string;
}) {
  return (
    <div className={`cv-markdown ${className ?? ""}`}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdown}>
        {content}
      </ReactMarkdown>
    </div>
  );
}

/**
 * One assistant or user message, rendered the same in chat and on the canvas:
 * agent steps (live while streaming), markdown, then evidence citations.
 */
export default function RichMessage({
  content,
  role,
  runId,
  steps: providedSteps,
  citations = [],
  onCite,
  renderEvidence,
  compact = false,
  streaming = false,
  live = true,
}: {
  content: string;
  role: "user" | "assistant";
  runId?: string;
  /** Pre-supplied steps (design preview); otherwise loaded by runId. */
  steps?: RunStep[];
  citations?: Citation[];
  onCite?: (id: string) => void;
  renderEvidence?: (id: string) => ReactNode;
  compact?: boolean;
  streaming?: boolean;
  /** False where the backend is not reachable (design preview). */
  live?: boolean;
}) {
  const query = useQuery(
    runStepsQuery(runId ?? "", live && role === "assistant" && !providedSteps),
  );
  const steps = providedSteps ?? query.data?.items ?? [];
  return (
    <div className={`cv-message is-${role} ${compact ? "is-compact" : ""}`}>
      {role === "assistant" && (
        <AgentSteps
          steps={steps}
          traceTemplate={query.data?.trace_url_template}
          renderEvidence={renderEvidence}
        />
      )}
      {role === "user" ? (
        <p className="cv-plain">{content}</p>
      ) : (
        <MarkdownBody content={content} />
      )}
      {streaming && <span className="cv-cursor" aria-hidden="true" />}
      {citations.length > 0 && (
        <div className="cv-citations" aria-label="引用的证据">
          {citations.map((c, i) => (
            <button
              key={c.id}
              type="button"
              className="cv-citation"
              title={c.title}
              onClick={() => onCite?.(c.id)}
            >
              <Quote size={11} />
              <sup>{i + 1}</sup>
              <span>{c.title}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
