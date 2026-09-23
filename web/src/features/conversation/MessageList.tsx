import { useEffect, useRef, type ReactNode } from "react";
import { User } from "../../lib/icons";
import RichMessage, { type Citation } from "./RichMessage";
import type { RunStep } from "./steps";

export type ListMessage = {
  id: string;
  role: string;
  content: string;
  created_at: string;
  model_id?: string;
  run_id?: string;
  evidence_ids?: string[];
  streaming?: boolean;
};

const clock = (value: string) =>
  new Date(value).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
  });

/** Chronological conversation that follows new output unless the reader scrolled up. */
export default function MessageList({
  messages,
  citationsFor,
  stepsFor,
  onCite,
  renderEvidence,
  live = true,
  className,
}: {
  messages: ListMessage[];
  citationsFor?: (m: ListMessage) => Citation[];
  stepsFor?: (m: ListMessage) => RunStep[] | undefined;
  onCite?: (id: string) => void;
  renderEvidence?: (id: string) => ReactNode;
  live?: boolean;
  className?: string;
}) {
  // The list may sit in any scrolling ancestor, so follow output through an
  // end sentinel, and only while the reader is already at the bottom.
  const end = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const tail = messages.at(-1);
  useEffect(() => {
    const el = end.current;
    if (!el) return;
    const observer = new IntersectionObserver(
      ([entry]) => {
        pinned.current = entry.isIntersecting;
      },
      { rootMargin: "0px 0px 64px 0px" },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (pinned.current) end.current?.scrollIntoView({ block: "nearest" });
  }, [messages.length, tail?.content]);
  return (
    <div
      className={`cv-list ${className ?? ""}`}
      role="log"
      aria-live="polite"
    >
      {messages.map((m) => {
        const assistant = m.role !== "user";
        return (
          <article key={m.id} className={`cv-row ${assistant ? "is-assistant" : "is-user"}`}>
            <span className="cv-avatar" aria-hidden="true">
              {assistant ? "AI" : <User size={14} />}
            </span>
            <div className="cv-row-body">
              <header>
                <b>{assistant ? "AI" : "你"}</b>
                <time dateTime={m.created_at}>{clock(m.created_at)}</time>
                {assistant && m.model_id && <span>{m.model_id}</span>}
              </header>
              <RichMessage
                role={assistant ? "assistant" : "user"}
                content={m.content}
                runId={m.run_id}
                steps={stepsFor?.(m)}
                citations={citationsFor?.(m)}
                onCite={onCite}
                renderEvidence={renderEvidence}
                streaming={m.streaming}
                live={live}
              />
            </div>
          </article>
        );
      })}
      <div ref={end} className="cv-list-end" aria-hidden="true" />
    </div>
  );
}
