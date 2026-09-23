import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { request } from "../../lib/api";

/** One visible model call or read-only tool call (GET /runs/{id}/steps). */
export type RunStep = {
  id: string;
  run_id: string;
  attempt_id: string;
  seq: number;
  kind: "model" | "tool";
  name: string;
  status: "running" | "succeeded" | "failed";
  input?: string;
  output?: string;
  error?: string;
  evidence_id?: string;
  input_tokens?: number;
  output_tokens?: number;
  tool_calls?: string[];
  trace_id?: string;
  started_at: string;
  finished_at?: string;
};
export type RunSteps = { items: RunStep[]; trace_url_template: string };

export const runStepsQuery = (runId: string, enabled = true) =>
  queryOptions({
    queryKey: ["run-steps", runId],
    queryFn: () => request<RunSteps>(`/runs/${runId}/steps`),
    enabled: enabled && !!runId,
    staleTime: Infinity,
  });

/** Applies a live attempt.step event; the same id arrives running, then final. */
export function applyStepEvent(client: QueryClient, step: RunStep) {
  client.setQueryData<RunSteps>(["run-steps", step.run_id], (prev) => {
    const items = (prev?.items ?? []).filter((s) => s.id !== step.id);
    items.push(step);
    items.sort(
      (a, b) =>
        a.started_at.localeCompare(b.started_at) || a.seq - b.seq,
    );
    return { items, trace_url_template: prev?.trace_url_template ?? "" };
  });
}

const toolLabels: Record<string, string> = {
  query_metrics: "查询指标",
  query_logs: "查询日志",
  search_runbooks: "检索 Runbook",
  inspect_deployment: "查看 Deployment",
};
export function stepLabel(step: RunStep) {
  if (step.kind === "model") return "模型推理";
  if (step.name.startsWith("mcp_")) return `MCP · ${step.name.slice(4)}`;
  return toolLabels[step.name] ?? step.name;
}
export function stepDuration(step: RunStep) {
  if (!step.finished_at) return null;
  return new Date(step.finished_at).getTime() - new Date(step.started_at).getTime();
}
export const formatDuration = (ms: number) =>
  ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
export const formatTokens = (n: number) =>
  n < 1000 ? `${n}` : `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;

/** Totals shown on a collapsed step card. Tokens only count reported usage. */
export function summarize(steps: RunStep[]) {
  const tools = steps.filter((s) => s.kind === "tool");
  const models = steps.filter((s) => s.kind === "model");
  const reported = models.filter((s) => s.input_tokens != null);
  const tokens = reported.reduce(
    (n, s) => n + (s.input_tokens ?? 0) + (s.output_tokens ?? 0),
    0,
  );
  const times = steps.map((s) => new Date(s.started_at).getTime());
  const ends = steps
    .filter((s) => s.finished_at)
    .map((s) => new Date(s.finished_at!).getTime());
  return {
    tools: tools.length,
    models: models.length,
    failed: steps.filter((s) => s.status === "failed").length,
    running: steps.some((s) => s.status === "running"),
    tokens: reported.length ? tokens : null,
    duration:
      times.length && ends.length ? Math.max(...ends) - Math.min(...times) : null,
  };
}
export const traceLink = (template: string, traceId?: string) =>
  template && traceId ? template.replace("{trace_id}", traceId) : "";
