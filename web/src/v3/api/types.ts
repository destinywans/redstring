/**
 * API contract types for the v3 frontend. The Go API must return these shapes under /api/v1.
 * Human-readable text is returned in the request language (Accept-Language: zh-CN | en-US).
 * Timestamps are RFC 3339 strings; durations are seconds; percentages are 0–100 unless named *_ratio.
 * See docs/api/v3-contract.md for endpoints, parameters and implementation status.
 */

export type Tone = "neutral" | "accent" | "ok" | "warn" | "danger" | "info";
export type Severity = "P1" | "P2" | "P3" | "P4";
export type EnvName = "prod" | "staging" | "test";
export type Tier = "T0" | "T1" | "T2";

export type Page<T> = { items: T[]; total: number; page: number; page_size: number };
export type Items<T> = { items: T[] };
export type ApiErrorBody = { error: { code: string; message: string; details?: Record<string, unknown> } };

/** Reference to another object, used by chips, citations and links. */
export type Ref = { kind: RefKind; id: string; label: string };
export type RefKind =
  | "service"
  | "alert"
  | "alert_group"
  | "incident"
  | "investigation"
  | "conversation"
  | "runbook"
  | "change"
  | "proposal"
  | "person"
  | "artifact"
  | "connector"
  | "run";

/* ---------------- identity ---------------- */
export type Person = { id: string; name: string; title?: string; team?: string };
export type Me = Person & {
  email: string;
  phone: string;
  title: string;
  team: string;
  department: string;
  avatar_url?: string;
  timezone: string;
  oncall: { active: boolean; until?: string; schedule?: string };
  roles: string[];
  mfa: { totp: boolean; passkeys: number };
  preferences: Preferences;
};
export type Preferences = { theme: "light" | "dark" | "system"; locale: "zh-CN" | "en-US"; density: "comfortable" | "compact"; reduce_motion: boolean };
export type WatchedService = { id: string; name: string; status: "ok" | "warn" | "danger"; signal?: string };

/* ---------------- global search / command palette ---------------- */
export type SearchHit = { kind: RefKind; id: string; title: string; subtitle?: string; status?: Tone; href: string };
export type SearchResult = { query: string; hits: SearchHit[]; recent: SearchHit[] };
export type MentionCandidate = { kind: RefKind; id: string; label: string; meta?: string; status?: Tone; tier?: Tier };

/* ---------------- shared widgets ---------------- */
export type Kpi = { id: string; label: string; value: string; unit?: string; delta?: string; delta_good?: boolean; tone?: Tone; sub?: string; series: number[] };
export type Activity = { id: string; time: string; kind: "ai" | "change" | "alert" | "runbook" | "approval" | "system"; tone: Tone; text: string; href?: string };
export type RecentItem = { id: string; kind: "investigation" | "conversation" | "artifact"; title: string; meta: string; href: string };

/* ---------------- home ---------------- */
export type HomeSummary = {
  greeting: { name: string; summary: string };
  suggestions: { id: string; text: string; tone?: Tone; icon: string }[];
  kpis: Kpi[];
  attention: { id: string; severity: Severity; service: string; env: EnvName; symptom: string; meta: string; ai: "done" | "running" | null; owner?: string; href: string }[];
  todos: { id: string; kind: "approval" | "report" | "runbook" | "access"; title: string; meta: string; action: string; href: string }[];
  services: { id: string; name: string; tier: Tier; status: "ok" | "warn" | "danger"; budget_pct: number; p99: string; error_rate: string; series: number[] }[];
  alert_trend: { labels: string[]; counts: [number, number, number, number][]; summary: string };
  activity: Activity[];
  recent: RecentItem[];
};

/* ---------------- conversations & rich message parts ---------------- */
export type ConversationStatus = "active" | "waiting_approval" | "resolved" | "archived";
export type ConversationSource = "manual" | "alert_triage" | "schedule" | "im";
export type ConversationSummary = {
  id: string;
  title: string;
  mode: "chat" | "canvas";
  status: ConversationStatus;
  source: ConversationSource;
  scope: string;
  incident_id?: string;
  participants: string[];
  size: string;
  updated_at: string;
  tokens: number;
};
export type ConversationList = Page<ConversationSummary> & { stats: Kpi[]; counts: Record<"mine" | "team" | "triage" | "archived", number> };

export type ToolCall = { id: string; connector: string; tool: string; args: string; duration_ms: number; status: "ok" | "error" | "running" };
export type ChartSeries = { name: string; values: number[] };
export type Part =
  | { type: "text"; markdown: string }
  | { type: "tool_calls"; summary: string; calls: ToolCall[]; tokens: number; duration_s: number }
  | { type: "chart"; title: string; unit: string; labels: string[]; series: ChartSeries[]; threshold?: number; markers?: { at: number; label: string; tone: Tone }[]; source?: string; deep_link?: string }
  | { type: "table"; title: string; columns: string[]; rows: (string | number)[][]; filter?: string }
  | { type: "image"; title: string; caption: string; alt: string; diagram: "call_graph" }
  | { type: "files"; files: { id: string; name: string; kind: "md" | "csv" | "yaml" | "log" | "pdf"; size: string; preview: string }[] }
  | { type: "diff"; path: string; from: string; to: string; left: string[]; right: string[] }
  | { type: "topology"; caption: string; href: string }
  | { type: "proposal"; id: string; title: string; risk: "low" | "medium" | "high"; reversible: boolean; target: string; detail: string }
  | { type: "form"; title: string; fields: { id: string; label: string; kind: "radio" | "select" | "check"; options?: string[]; value?: string | boolean }[] }
  | { type: "tasks"; title: string; items: { label: string; state: "done" | "running" | "todo"; meta?: string }[] }
  | { type: "citations"; items: { n: number; label: string; ref?: Ref }[] }
  | { type: "data_gap"; text: string; href: string }
  | { type: "next_steps"; items: { icon: string; label: string; action: string }[] }
  | { type: "status"; text: string; detail?: string }
  | { type: "artifacts"; ids: string[] };
export type Message = {
  id: string;
  role: "user" | "assistant";
  author: string;
  created_at: string;
  parts: Part[];
  run?: { id: string; agent: string; version: string; model: string; tokens: number; duration_s: number };
  streaming?: boolean;
};
export type Artifact = {
  id: string;
  kind: "report" | "chart" | "table" | "diff";
  title: string;
  meta: string;
  version: number;
  updated_at: string;
  content:
    | { kind: "report"; markdown: string; editing_note?: string; annotations: { author: string; text: string }[] }
    | { kind: "chart"; panels: { title: string; unit: string; peak: string; series: ChartSeries; threshold?: number }[]; labels: string[]; markers: { at: number; label: string; tone: Tone }[] }
    | { kind: "table"; columns: string[]; rows: (string | number)[][] }
    | { kind: "diff"; path: string; from: string; to: string; left: string[]; right: string[] };
};
export type Conversation = {
  id: string;
  title: string;
  mode: "chat" | "canvas";
  status: ConversationStatus;
  scope: { service?: string; env?: EnvName; inferred: boolean };
  investigation_id?: string;
  messages: Message[];
  artifacts: Artifact[];
  side: {
    connectors: { name: string; calls: number }[];
    context: { used: number; budget: number; parts: { label: string; tokens: number; color: string }[] };
    recalled: { kind: "runbook" | "postmortem"; title: string; score: number }[];
  };
};
/** Server-sent events on GET /conversations/{id}/events while a reply streams. */
export type StreamEvent =
  | { type: "message.started"; message: Message }
  | { type: "part.added"; message_id: string; part: Part }
  | { type: "text.delta"; message_id: string; index: number; delta: string }
  | { type: "message.completed"; message_id: string; run: Message["run"] };
