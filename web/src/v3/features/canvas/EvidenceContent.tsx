import type { ReactNode } from "react";
import type { EvidenceBody } from "../../api/types";
import { Badge, Muted, Row } from "../../ui/core";
import { useI18n } from "../../i18n";

type LogSample = { timestamp?: string; level?: string; message?: string; trace_id?: string; truncated?: boolean };
type MaskedLogs = { status: "masked_sample"; provider?: string; samples: LogSample[]; redacted?: boolean; redactions?: Record<string, number>; partial?: boolean; sampled_count?: number; truncated?: boolean; boundary?: string };

/** Real evidence arrives as a kind/content table; returns the parts when it does. */
export function evidenceParts(body: EvidenceBody | undefined): { kind: string; content: string } | null {
  if (!body || body.type !== "table") return null;
  const kind = body.rows.find((r) => r[0] === "kind")?.[1];
  const content = body.rows.find((r) => r[0] === "content")?.[1];
  return kind !== undefined && content !== undefined ? { kind, content } : null;
}

/** Marks the redaction placeholders ([REDACTED:ip], …) so they read as removed, not as log text. */
function withRedactions(text: string): ReactNode[] {
  return text.split(/(\[REDACTED:[a-z_]+\])/g).map((part, i) =>
    /^\[REDACTED:[a-z_]+\]$/.test(part) ? (
      <span key={i} className="ev-redacted" title={part}>
        {part.slice(10, -1)}
      </span>
    ) : (
      part
    ),
  );
}

/**
 * Evidence body as stored by the investigation tools (PA-40): masked log samples are shown line by line with the
 * redactions marked and the boundary note; other kinds show their stored text. Nothing here is executed or followed.
 */
export function EvidenceContent({ body, compact }: { body: EvidenceBody | undefined; compact?: boolean }) {
  const { tx } = useI18n();
  const parts = evidenceParts(body);
  if (!parts) return null;
  let logs: MaskedLogs | null = null;
  if (parts.kind === "logs") {
    try {
      const v = JSON.parse(parts.content) as MaskedLogs;
      if (v && v.status === "masked_sample" && Array.isArray(v.samples)) logs = v;
    } catch {
      /* not a masked sample: show the stored text */
    }
  }
  if (!logs) return <pre className="cv-log ev-text">{compact ? parts.content.slice(0, 600) : parts.content}</pre>;
  const reds = Object.entries(logs.redactions ?? {}).filter(([, n]) => n > 0);
  const shown = compact ? logs.samples.slice(0, 4) : logs.samples;
  return (
    <div className="ev-logs">
      <Row gap={6} wrap>
        <Badge tone="info">{tx("脱敏日志样本", "Masked log sample")}</Badge>
        {logs.provider && <Muted size={11}>{logs.provider}</Muted>}
        {reds.length > 0 && <Muted size={11}>{tx("已抹去：{r}", "Removed: {r}", { r: reds.map(([k, n]) => `${k}×${n}`).join(", ") })}</Muted>}
        {(logs.truncated || logs.partial) && <Badge tone="warn">{tx("不完整", "Partial")}</Badge>}
      </Row>
      {shown.length ? (
        <ol className="cv-log ev-lines">
          {shown.map((l, i) => (
            <li key={i}>
              <span className="muted">{l.timestamp ? new Date(l.timestamp).toLocaleTimeString([], { hour12: false }) : ""}</span>
              {l.level && <b className={`lv-${l.level.toLowerCase()}`}>{l.level}</b>}
              <span>{withRedactions(l.message ?? "")}</span>
              {l.truncated && <Muted size={10.5}>…</Muted>}
            </li>
          ))}
        </ol>
      ) : (
        <Muted size={11.5}>{tx("这段时间没有取到样本；空样本不代表没有错误。", "No samples in this window; an empty sample doesn't mean there were no errors.")}</Muted>
      )}
      {compact && logs.samples.length > shown.length && <Muted size={11}>{tx("还有 {n} 条", "{n} more", { n: logs.samples.length - shown.length })}</Muted>}
      {!compact && logs.boundary && <Muted size={11}>{logs.boundary}</Muted>}
    </div>
  );
}
