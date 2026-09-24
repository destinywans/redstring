import { API_MODE, api } from "./client";
import type { Message, Part, StreamEvent } from "./types";
import { currentLocale } from "../app/preferences";
import { pick, type L } from "../i18n";

/**
 * Send a user message and stream the assistant reply.
 * Contract: POST /conversations/{id}/messages, then server-sent events on GET /conversations/{id}/events
 * (`message.started`, `part.added`, `text.delta`, `message.completed`).
 */
export async function sendAndStream(conversationId: string, text: string, onEvent: (e: StreamEvent) => void): Promise<() => void> {
  const { message } = await api<{ message: Message; reply_id: string }>("POST", `/conversations/${conversationId}/messages`, { body: { text } });
  onEvent({ type: "message.started", message });
  if (API_MODE === "http") {
    const es = new EventSource(`/api/v1/conversations/${conversationId}/events`);
    es.onmessage = (ev) => {
      const e = JSON.parse(ev.data) as StreamEvent;
      onEvent(e);
      if (e.type === "message.completed") es.close();
    };
    return () => es.close();
  }
  return mockStream(text, onEvent);
}

async function mockStream(text: string, onEvent: (e: StreamEvent) => void) {
  const { cannedReply } = await import("../mock/domains/conversations");
  const r = cannedReply(text);
  const loc = currentLocale();
  const id = `a-${Date.now()}`;
  const timers: ReturnType<typeof setTimeout>[] = [];
  const later = (ms: number, f: () => void) => timers.push(setTimeout(f, ms));
  onEvent({ type: "message.started", message: { id, role: "assistant", author: "AI", created_at: new Date().toISOString(), parts: [], streaming: true } });
  const tools: Part = {
    type: "tool_calls",
    summary: loc === "zh-CN" ? `调用 ${r.tools.length} 个工具` : `${r.tools.length} tool calls`,
    calls: r.tools.map((t) => ({ ...t, status: "running" as const, args: typeof t.args === "string" ? t.args : pick(loc, t.args as L) })),
    tokens: 0,
    duration_s: 0,
  };
  later(300, () => onEvent({ type: "part.added", message_id: id, part: tools }));
  later(1300, () =>
    onEvent({ type: "part.added", message_id: id, part: { ...tools, calls: tools.calls.map((c) => ({ ...c, status: "ok" as const })), tokens: 4800, duration_s: 1.2 } }),
  );
  const full = pick(loc, r.text);
  later(1500, () => onEvent({ type: "part.added", message_id: id, part: { type: "text", markdown: "" } }));
  const chunks = full.match(/.{1,6}/gsu) ?? [];
  chunks.forEach((c, i) => later(1600 + i * 35, () => onEvent({ type: "text.delta", message_id: id, index: 1, delta: c })));
  const end = 1650 + chunks.length * 35;
  later(end, () =>
    onEvent({ type: "part.added", message_id: id, part: { type: "citations", items: r.citations.map((c) => ({ n: c.n, label: pick(loc, c.label) })) } }),
  );
  later(end + 50, () =>
    onEvent({
      type: "message.completed",
      message_id: id,
      run: { id: `run_${id.slice(-4)}`, agent: loc === "zh-CN" ? "调查助手" : "Investigator", version: "v12", model: loc === "zh-CN" ? "默认模型" : "Default model", tokens: 4800, duration_s: end / 1000 },
    }),
  );
  return () => timers.forEach(clearTimeout);
}
