import { API_MODE, api } from "./client";
import type { Message, ModelChoice, Part, StreamEvent, Upload } from "./types";
import { currentLocale } from "../app/preferences";
import { pick, type L } from "../i18n";

/** `onError`: the reply stream closed for good before the reply completed. */
export type SendOptions = { choice?: ModelChoice | null; attachments?: Upload[]; onError?: () => void };

/** Hands attachments picked on one page (e.g. the home composer) to the conversation that sends them. */
const pending = new Map<string, Upload[]>();
export function stashAttachments(files: Upload[]) {
  if (!files.length) return null;
  const key = `h${Date.now().toString(36)}`;
  pending.set(key, files);
  return key;
}
export function takeAttachments(key: string | null) {
  if (!key) return [];
  const f = pending.get(key) ?? [];
  pending.delete(key);
  return f;
}

/**
 * Send a user message and stream the assistant reply.
 * Contract: POST /conversations/{id}/messages `{ text, model?, reasoning?, attachments? }`, then server-sent events on
 * GET /conversations/{id}/events (`message.started`, `part.added`, `text.delta`, `message.completed`).
 * One model per turn: the model/reasoning sent here apply to this turn only; the run in `message.completed` records them.
 */
export async function sendAndStream(conversationId: string, text: string, onEvent: (e: StreamEvent) => void, opts: SendOptions = {}): Promise<() => void> {
  const body = { text, model: opts.choice?.model, reasoning: opts.choice?.reasoning, attachments: opts.attachments };
  const { message } = await api<{ message: Message; reply_id: string }>("POST", `/conversations/${conversationId}/messages`, { body });
  onEvent({ type: "message.started", message });
  if (API_MODE === "http") {
    const es = new EventSource(`/api/v1/conversations/${conversationId}/events`);
    es.onmessage = (ev) => {
      const e = JSON.parse(ev.data) as StreamEvent;
      onEvent(e);
      if (e.type === "message.completed") es.close();
    };
    // EventSource retries on its own; CLOSED means it gave up, so the reply will never complete (PA-225).
    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) opts.onError?.();
    };
    return () => es.close();
  }
  let reply: Message[] = [];
  return mockStream(
    text,
    (e) => {
      reply = applyEvent(reply, e);
      onEvent(e);
      if (e.type === "message.completed") {
        const done = reply.find((m) => m.id === e.message_id);
        if (done) void import("../mock/domains/conversations").then((m) => m.saveReply(conversationId, { ...done, streaming: false, run: e.run }));
      }
    },
    opts.choice,
  );
}

async function mockStream(text: string, onEvent: (e: StreamEvent) => void, choice?: ModelChoice | null) {
  const { cannedReply } = await import("../mock/domains/conversations");
  const { MODELS } = await import("../mock/domains/composer");
  const def = { model: "claude:claude-opus-5-5", reasoning: "high" };
  const c = choice ?? def;
  const m = MODELS.find((x) => x.id === c.model) ?? MODELS[0];
  const lvl = (m.reasoning.levels as { id: string; label: L }[]).find((x) => x.id === c.reasoning);
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
      run: { id: `run_${id.slice(-4)}`, agent: loc === "zh-CN" ? "调查助手" : "Investigator", version: "v12", model: typeof m.name === "string" ? m.name : pick(loc, m.name as L), model_id: m.id, reasoning: lvl?.id, reasoning_label: lvl ? (m.reasoning.kind === "toggle" ? `${loc === "zh-CN" ? "思考" : "Thinking"} ${pick(loc, lvl.label)}` : pick(loc, lvl.label)) : undefined, brand: m.brand, tokens: 4800, duration_s: end / 1000 },
    }),
  );
  return () => timers.forEach(clearTimeout);
}

/** URL that starts a conversation from any composer, carrying the model choice and attachments. */
export function newConversationUrl(text: string, refs: { kind: string; id: string; label: string }[], x: { files: Upload[]; choice: ModelChoice | null }) {
  const q = new URLSearchParams({ q: text || refs.map((a) => a.label).join(" ") || x.files.map((f) => f.name).join(" ") });
  if (refs.length) q.set("context", refs.map((a) => `${a.kind}:${a.id}`).join(","));
  if (x.choice) {
    q.set("model", x.choice.model);
    if (x.choice.reasoning) q.set("reasoning", x.choice.reasoning);
  }
  const key = stashAttachments(x.files);
  if (key) q.set("files", key);
  return `/conversations/new?${q}`;
}

/** Folds a stream event into a message list (conversation page and the Ask AI card). */
export function applyEvent(messages: Message[], e: StreamEvent): Message[] {
  switch (e.type) {
    case "message.started":
      return messages.some((m) => m.id === e.message.id) ? messages : [...messages, e.message];
    case "part.added":
      return messages.map((m) => {
        if (m.id !== e.message_id) return m;
        const i = m.parts.findIndex((p) => p.type === e.part.type && p.type === "tool_calls");
        if (i >= 0) return { ...m, parts: m.parts.map((p, j) => (j === i ? e.part : p)) };
        return { ...m, parts: [...m.parts, e.part] };
      });
    case "text.delta":
      return messages.map((m) =>
        m.id !== e.message_id ? m : { ...m, parts: m.parts.map((p, j) => (j === e.index && p.type === "text" ? { ...p, markdown: p.markdown + e.delta } : p)) },
      );
    case "message.completed":
      return messages.map((m) => (m.id === e.message_id ? { ...m, streaming: false, run: e.run } : m));
  }
}
