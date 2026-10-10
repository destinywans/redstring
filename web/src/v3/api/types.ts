/**
 * API contract types for the v3 frontend. The Go API must return these shapes under /api/v1.
 * Human-readable text is returned in the request language (Accept-Language: zh-CN | en-US).
 * Timestamps are RFC 3339 strings; durations are seconds; percentages are 0–100 unless named *_ratio.
 * See docs/api/v3-contract.md for endpoints, parameters and implementation status.
 */

export type Tone = "neutral" | "accent" | "ok" | "warn" | "danger" | "info";
export type Severity = "P1" | "P2" | "P3" | "P4";
export type EnvName = "prod" | "staging" | "test";
export type Tier = "T0" | "T1" | "T2" | "T3";
/** Why a value is unknown (V3UnknownReason). */
export type UnknownReason = "not_connected" | "not_configured" | "no_authorized_target" | "not_generated" | "unavailable" | "stale";
/**
 * V3Unavailable: field path (dot-separated; `[]` marks a field of every array item) -> reason.
 * A key present means that field is null in the body; `{}` means everything is proven.
 */
export type Unavailable = Record<string, UnknownReason>;

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
  /** null = unknown: no authoritative on-call source, or it failed. Never means "not on call". */
  oncall: { active: boolean; until?: string; schedule?: string } | null;
  oncall_state: "verified" | "not_configured" | "unavailable";
  roles: string[];
  mfa: { totp: boolean; passkeys: number };
  preferences: Preferences;
  /** Effective grants per selected environment: environment → module → action → reach; a missing action is not granted (PA-64). */
  permissions?: Record<string, Record<string, Record<string, "all" | "scoped" | "conditional">>>;
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
  /** null until generated; oncall inside is null without an on-call source. */
  briefing: { summary: string; generated_at: string; citations: { n: number; label: string; href: string }[]; oncall: { name: string; until: string; next: string } | null } | null;
  suggestions: { id: string; text: string; tone?: Tone; icon: string }[];
  kpis: Kpi[];
  /** alert_state: is the incident's alert still firing (null when the source didn't say). Resolved items stay listed (D7). */
  attention: { id: string; severity: Severity; service: string; env: EnvName; symptom: string; meta: string; ai: "done" | "running" | null; owner?: string; href: string; alert_state?: "firing" | "resolved" | null }[];
  /** Distinct visible services whose alerts are still firing; null without events.view (PA-58). */
  firing_services?: number | null;
  todos: { id: string; kind: "approval" | "report" | "runbook" | "access"; title: string; meta: string; action: string; href: string }[];
  services: { id: string; name: string; tier: Tier | null; status: "ok" | "warn" | "danger" | null; budget_pct: number | null; p99: string | null; error_rate: string | null; series: number[] | null }[];
  /** labels are UTC HH:MM (compatibility); bucket i starts at start + i * step_seconds — label it in the viewer's zone (PA-25). */
  alert_trend: { labels: string[]; start?: string; step_seconds?: number; counts: [number, number, number, number][]; summary: string } | null;
  /** Per-section evidence state from the server, e.g. { alert_trend: "no_authorized_target", oncall: "not_connected" }. */
  availability?: Record<string, string>;
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
  run?: { id: string; agent: string; version: string; model: string; model_id?: string; reasoning?: string; reasoning_label?: string; brand?: string; tokens: number; duration_s: number };
  attachments?: Upload[];
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
    /** used / budget are null when the model reported no input usage or has no configured input budget. */
    context: { used: number | null; budget: number | null; parts: { label: string; tokens: number; color: string }[] };
    recalled: { kind: "runbook" | "postmortem"; title: string; score: number }[];
  };
};
/** Server-sent events on GET /conversations/{id}/events while a reply streams. */
export type StreamEvent =
  | { type: "message.started"; message: Message }
  | { type: "part.added"; message_id: string; part: Part }
  | { type: "text.delta"; message_id: string; index: number; delta: string }
  | { type: "message.completed"; message_id: string; run: Message["run"] };

/* ---------------- canvas investigations ---------------- */
export type LoopStep = { key: "anomaly" | "evidence" | "proposal" | "approval" | "execution" | "verify"; label: string; detail: string; state: "done" | "active" | "pending" | "error"; href?: string };
export type EvidenceBody =
  | { type: "chart"; unit: string; values: number[]; marker?: number }
  | { type: "log"; lines: string[] }
  | { type: "diff"; minus: string; plus: string }
  | { type: "table"; rows: string[][] };
export type CanvasNode = {
  id: string;
  kind: "root" | "branch" | "evidence";
  parent?: string;
  title: string;
  subtitle: string;
  status: "active" | "running" | "ruled_out" | "confirmed";
  x: number;
  y: number;
  w: number;
  summary?: string;
  confidence?: "high" | "medium" | "low";
  evidence_refs?: string[];
  tools?: string;
  series?: number[];
  writeback?: { to: string; version: string };
  evidence?: EvidenceBody & { source: string; color: string };
  context: { version: string; inherits?: string; tokens: number; messages: number; excludes: string[] };
  versions: { v: string; text: string; at: string }[];
};
export type EvidenceTrayItem = { id: string; kind: "chart" | "log" | "table" | "diff"; title: string; source: string; body: EvidenceBody };
export type CanvasGraph = {
  investigation: { id: string; title: string; status: ConversationStatus; incident_id?: string; env: EnvName; participants: string[]; conversation_id?: string };
  loop: LoopStep[];
  nodes: CanvasNode[];
  tray: EvidenceTrayItem[];
};
export type WritebackPreview = {
  from: { id: string; title: string; version: string };
  to: { id: string; title: string; version: string; next_version: string; hops: number };
  conclusions: { id: string; text: string; evidence: string[]; recommended: boolean; tokens?: number }[];
  timeline: { v: string; text: string; meta: string; isNew?: boolean }[];
  checks: { tone: "ok" | "warn"; text: string }[];
  budget: { before: number; after: number; limit: number };
};

/* ---------------- investigation templates ---------------- */
export type InvestigationTemplate = {
  id: string;
  name: string;
  owner: "official" | "team";
  description: string;
  branches: { name: string; step: string; tools: string[] }[];
  tools: string[];
  uses: string;
  version: string;
  maintainer: string;
  triggers: { manual: boolean; auto_triage: boolean; matchers: { name: string; value: string }[] };
  runbooks: string[];
  budget: { turns: number; tools: number; minutes: number };
};

/* ---------------- events: inbox, alerts, incidents ---------------- */
export type InboxItem = { id: string; kind: "alert" | "approval" | "mention" | "runbook" | "report" | "access" | "agent"; tone: Tone; title: string; sub: string; time: string; unread: boolean; day: "today" | "yesterday"; href: string; alert_id?: string };
/** Sample data carries time/text/done; the real queue reports id/status/created_at/attempts (local enqueue, not delivery). */
export type AlertNotification = { channel: string; time?: string; text?: string; done?: boolean; id?: string; status?: "pending" | "sending" | "retrying" | "failed" | "sent"; created_at?: string; attempts?: number };
/**
 * GET /alerts/{id} (V3AlertDetail). Without the producers, source, rule, triage, series, labels, runbook and the
 * grouping group/inhibited/change are null and `unavailable` lists them. Claim needs `revision` as expected_revision.
 */
export type AlertDetail = {
  id: string;
  severity: Severity;
  title: string;
  state: "firing" | "pending" | "resolved";
  source: string | null;
  env: string;
  since: string;
  rule: string | null;
  triage: { summary: string; confidence: "high" | "medium" | "low"; agent: string; duration_s: number; tools: number; investigation_id: string; proposal_id?: string } | null;
  series: { labels: string[]; values: number[]; threshold: number; release_at?: number } | null;
  labels: { k: string; v: string }[] | null;
  summary: string;
  runbook: { id: string; title: string } | null;
  notifications: AlertNotification[];
  grouping: { group: string | null; inhibited: string | null; incident: { id: string; label?: string }; change: { id: string; label: string } | null };
  revision?: string;
  investigation_id?: string;
  assignee_id?: string;
  unavailable?: string[];
};
/** Real instances carry no current value, series or pending progress yet (null = unknown, never zero). */
export type AlertInstance = { id: string; value: string | null; state: "firing" | "pending"; duration: string; series: number[] | null; pending?: { pct: number; text: string } | null };
/** Real groups are local persisted incidents: service is the application ID, owner/inhibited/notify.channel may be null. */
export type AlertGroup = {
  id: string;
  severity: Severity;
  labels: { k: string; v: string }[];
  service: string;
  count: number;
  state: "firing" | "pending" | "silenced" | "resolved";
  since: string;
  notify: { channel?: string | null; text: string; status?: string | null };
  badges: { text: string; tone: Tone }[];
  owner?: string | null;
  inhibited?: boolean | null;
  instances: AlertInstance[];
  environment_id?: string;
  unavailable?: string[];
};
/** inhibition is null and timeline entries may lack done (unknown) when the source can't prove them. */
export type AlertGroupPolicy = {
  group_by: string[];
  route: string;
  timing: { key: "group_wait" | "group_interval" | "repeat_interval"; label: string; value: string; help: string }[];
  timeline: { time: string; channel: string; text: string; done?: boolean | null }[];
  inhibition: string[] | null;
  unavailable?: string[];
};
/** Peeks are partial: null means unknown (never "healthy", "no owner" or "off call"). */
export type ServicePeek = { id: string; name: string; tier: Tier | null; team: string | null; env: string; status: "ok" | "warn" | "danger" | null; kpis: { k: string; v: string; tone?: Tone }[]; owner: string | null; oncall: string | null; change: string | null };
export type PersonPeek = { id: string; name: string; role: string | null; oncall: string | null; load: string | null };
/**
 * GET /incidents/{id}: sample data is fully filled; the real record has duration, commander, stage and every impact
 * field null unless a human confirmed them, timeline entries are manual notes with RFC 3339 times, and human-confirmed
 * cases (`record_kind: "case"`) carry `status_revision` for POST /incidents/{id}/status.
 */
export type Incident = {
  id: string;
  severity: Severity;
  title: string;
  status: "active" | "mitigated" | "resolved";
  duration: string | null;
  commander: string | null;
  stage: number | null;
  timeline: { id?: string; time: string; actor: { kind: "brand" | "ai" | "person" | "human" | "machine"; name: string }; text: string; sub?: string }[];
  impact: { peak: string | null; budget_used: string | null; budget_left: number | null; note: string | null; series: number[] | null };
  roles: { role: string; name: string; ai?: boolean }[];
  links: { kind: RefKind | string; label: string; href: string }[];
  record_kind?: "case";
  status_revision?: number;
  investigation_id?: string | null;
  partial?: boolean;
  unavailable?: string[];
};

/* ---------------- alert rules, routing, silences ---------------- */
export type AlertRuleSummary = { id: string; folder: string; name: string; state: "firing" | "pending" | "ok"; schedule?: string };
/**
 * List-endpoint emptiness (PA-09): items null = source not configured (see unavailable); [] with
 * empty_reason "no_authorized_target" = nothing visible to this user; [] with null = genuinely none.
 */
export type EmptyReason = "no_authorized_target" | null;
export type AlertRuleList = { items: AlertRuleSummary[] | null; counts: { all: number; firing: number; scheduled: number } | null; empty_reason: EmptyReason; unavailable: Unavailable };
export type AlertRule = AlertRuleSummary & {
  datasource: string;
  query_ref: string;
  expr: string;
  preview: { values: number[]; threshold: number };
  condition: { op: ">" | "<"; value: number; unit: string; for_minutes: number; recover_minutes: number };
  evaluation: { mode: "continuous" | "cron"; interval: string; cron?: string; no_data: string };
  labels: { k: string; v: string }[];
  annotation: string;
  runbook: string;
  route_path: string[];
  ai_triage: { enabled: boolean; template: string };
  last_eval: string;
};
/** Page shape of a route node (sample data uses it directly; the real tree is normalized into it). hits null = unknown. */
export type RouteNode = {
  id: string;
  title: string;
  matchers: { k: string; v: string; op?: string; redacted?: boolean }[];
  receivers: { channel: string; target: string }[];
  timing: [string, string, string];
  hits: number | null;
  continue?: boolean;
  children: RouteNode[];
};
/** GET /notification-routes node: matcher values and receiver names are redacted to opaque refs. */
export type RouteNodeRead = {
  id: string;
  title: string;
  matchers: { k: string; op: string; value_redacted: boolean; value?: string }[];
  receiver_refs: string[];
  group_by: string[];
  group_wait: string;
  group_interval: string;
  repeat_interval: string;
  continue: boolean;
  conditional: boolean;
  children?: RouteNodeRead[];
};
/** starts_at / ends_at are RFC 3339 (render the window from them); remaining.pct is the remaining share. */
export type Silence = { id: string; matchers: { k: string; v: string }[]; state: "active" | "planned" | "expired"; window: string; starts_at?: string; ends_at?: string; remaining?: { pct: number; text: string }; affects: number; creator: string; reason: string };
/** GET /silences: items/counts null without Alertmanager (unavailable says why); [] + empty_reason when nothing is visible. */
export type SilenceList = { items: Silence[] | null; counts: { active: number; planned: number; expired: number } | null; empty_reason: EmptyReason; unavailable: Unavailable };
export type Inhibition = { id: string; source: { k: string; v: string }[]; target: { k: string; v: string }[]; equal: string[]; active: boolean; stat: string };

/* ---------------- proposals & executions ---------------- */
export type Proposal = {
  id: string;
  title: string;
  status: "pending" | "approved" | "rejected" | "executed";
  risk: "low" | "medium" | "high";
  impact: string;
  reversible: boolean;
  proposed_by: string;
  proposed_at: string;
  investigation_id: string;
  target: string;
  diff: { path: string; lines: string[] };
  method: string;
  evidence: { n: number; icon: string; title: string; desc: string; series?: number[] }[];
  verification: string[];
  verification_note: string;
  checklist: string[];
  requirement: string;
  discussion: { author: string; ai?: boolean; time: string; text: string }[];
  /** Null until the second approval; status/upstream_id stay null (not_generated) until the executor records them. */
  execution?: { execution_id: string; approved_at: string; action_kind: string; status: string | null; upstream_id: string | null; unavailable: Unavailable } | null;
};
export type Execution = {
  id: string;
  proposal_id: string;
  status: "queued" | "executing" | "verifying" | "succeeded" | "failed";
  steps: { time: string; icon: string; tone: Tone; title: string; desc: string }[];
  observation: { values: number[]; threshold: number; labels: string[]; band: [number, number]; marker: number; checks: { text: string; value: string }[] };
  report: { title: string; severity: Severity; meta: string; summary: string; impact: string; timeline: { time: string; text: string }[]; actions: { text: string; owner: string; due: string; done: boolean }[]; refs: string };
};

/**
 * GET /executions/{id} in real mode (V3ExecutionDetail). Steps are persisted events; the observation is a recovery
 * summary without a series; the report is the closed report without severity/impact/timeline/actions (null).
 */
export type ExecutionDetail = {
  id: string;
  plan_id: string;
  investigation_id: string;
  application_id: string;
  environment_id: string;
  proposal_id: null;
  status: "queued" | "executing" | "verifying" | "succeeded" | "failed" | "needs_attention";
  status_source: string;
  attempt: number;
  error: string;
  created_at: string;
  updated_at: string;
  approval: { decision: "pending" | "approved" | "rejected" | "revoked"; approved_by: string | null; digest_match: boolean };
  steps: { seq: number; type: string; time: string }[];
  steps_truncated: boolean;
  observation: { recovered: boolean | null; started_at: string | null; finished_at: string | null; samples: number | null; requests: number | null; reason: string | null } | null;
  report: { title: string; summary: string; closed_by: string; approved_by: string; closed_at: string; published: boolean; published_by: string | null; published_at: string | null } | null;
  partial: boolean;
  missing: string[];
};

/* ---------------- knowledge & memory ---------------- */
export type RunbookSummary = { id: string; title: string; tags: string[]; scope: string; owner: string; version: string; status: "published" | "review" | "draft"; refs_30d: number | null; resolve_rate?: number | null; last_verified: string | null; stale: boolean | null };
export type RunbookSuggestion = { id: string; icon: string; tone: Tone; title: string; body: string; actions: { label: string; kind: "primary" | "secondary" | "ghost"; href?: string }[] };
export type RunbookStep = { n: number; kind: "query" | "check" | "action" | "verify"; title: string; body?: string; connectors?: string[]; code?: string; readonly?: boolean; suggestion?: { source: string; minus: string; plus: string } };
export type Runbook = {
  id: string;
  title: string;
  version: string;
  base: string;
  owner: string;
  scope: string;
  tags: string[];
  when: string;
  steps: RunbookStep[];
  reviewers: { name: string; state: "pending" | "optional" | "approved" }[];
  versions: { v: string; who: string; when: string }[];
  usage: { refs: number; resolve_rate: number; weekly: number[]; highlight: number };
  incidents: { id: string; title: string }[];
};
export type Postmortem = { id: string; title: string; severity: Severity | null; date: string; duration: string | null; cause: string; actions: string; status: "draft" | "published"; incident_id?: string };
export type PostmortemSummary = {
  kpis: Kpi[];
  items: Postmortem[];
  /** null = no trustworthy source yet (see unknown_fields); never an empty "all clear". */
  causes: { label: string; pct: number; color: string }[] | null;
  impact_by_service: { label: string; value: number }[] | null;
  followups: { text: string; owner: string; due: string; tone: Tone }[] | null;
  unknown_fields?: string[];
};
export type MemoryLayer = { id: "working" | "service" | "episodic" | "semantic"; name: string; count: string | number; desc?: string; description?: string };
/** Real facts carry `revision`/`state`; confidence, recalls and expiry are null until measured. */
export type MemoryFact = { id: string; fact: string; source: string; confidence: "high" | "medium" | "low" | null; confirmed: string | null; recalls: number | null; expires: string | null; revision?: number; state?: string };
export type MemoryConflictSide = { id?: string; text: string; source: string };
export type MemoryView = {
  layers: MemoryLayer[];
  services: { id: string; name?: string; key?: string; count: number }[];
  service: string | null;
  conflict?: { id?: string; a: MemoryConflictSide; b: MemoryConflictSide } | null;
  facts: MemoryFact[];
  policy: string | null;
  partial?: boolean;
  unavailable_fields?: string[];
};
export type MemoryPolicy = { ttl_days: 30 | 90 | 180; proposers: "human" | "human_ai"; revision: number };
export type KnowledgeSourceMock = { id: string; brand?: string; name: string; kind: string; sync: string; docs: number; chunks: string; last: string; status: "connected" | "error"; error?: string };
export type KnowledgeSourceEvidence = {
  id: string;
  name: string;
  kind: string;
  environment_id: string;
  application_id?: string;
  team_id?: string;
  schedule: string;
  chunk_strategy: string;
  registration_status: string;
  last_read: { id: string; observed_at: string; observed_count: number } | null;
  keyword_content: { persisted_document_count: number; version: number; read_id: string } | null;
  connection_status: string | null;
  sync_status: string | null;
  document_count: number | null;
  chunk_count: number | null;
  last_sync_at: string | null;
  index_status: string | null;
  error: string | null;
};
export type KnowledgeSource = KnowledgeSourceMock | KnowledgeSourceEvidence;
export type RetrievalHit = { brand?: string; title: string; snippet: string; keyword: number; vector: number; score: number };

/* ---------------- resources & observability ---------------- */
export type Health = "ok" | "warn" | "danger";
/** Catalog row (V3ServiceSummary): values without current evidence are null and `unavailable[path]` says why (PA-05). */
export type ServiceSummary = {
  id: string;
  name: string;
  desc: string;
  tier: Tier | null;
  team: string | null;
  oncall: string | null;
  status: Health | null;
  slo: { target: string | null; budget_left: number | null };
  release: { brand: string; version: string; when: string } | null;
  alerts: number | null;
  connectors: string[] | null;
  /** Paths: tier, team, oncall, status, slo.target, slo.budget_left, release, alerts, connectors. */
  unavailable: Unavailable;
};
/** Counts only services whose input is known; `unknown` services were left out. `value` is null (+ reason) only when all are unknown. */
export type ServiceCatalogKpi = { id: string; label: string; value: string | null; tone?: Tone; series: number[]; unknown: number; reason?: UnknownReason; sub?: string };
export type ServiceCatalog = { kpis: ServiceCatalogKpi[]; items: ServiceSummary[]; facets: { key: string; title: string; items: { label: string; count: number; on: boolean }[] }[] };
export type ServiceDependency = { brand?: string; name: string; status: Health | null; meta: string; unavailable: Unavailable };
export type ServiceBurn = { window: string; value: string; tone: "ok" | "warn" | "danger"; pct: number };
export type ServiceDetail = {
  id: string;
  name: string;
  tier: Tier | null;
  team: string | null;
  /** Any environment key of the instance, e.g. prod, staging, test or dev. */
  env: string;
  status: Health | null;
  desc: string;
  deeplinks: { brand: string; name: string; url: string }[];
  ai: { text: string; investigation: string; proposal?: string; meta: string } | null;
  signals: Kpi[] | null;
  slo: { availability: string | null; target: string | null; budget_left: number | null; burn: ServiceBurn[] | null; note: string };
  changes: { brand: string; title: string; meta: string; tag?: string }[] | null;
  deps: { upstream: ServiceDependency[]; downstream: ServiceDependency[] } | null;
  /** The contract counts changes, deps and incidents; the other keys only appear in sample data. */
  tabs: { changes: number | null; deps: number | null; incidents: number | null; resilience?: number; runbooks?: number };
  /** Paths: tier, team, status, ai, signals, slo.availability, slo.target, slo.budget_left, slo.burn, changes, deps, tabs.deps, tabs.incidents. */
  unavailable: Unavailable;
};
/** GET/PUT /services/{id}/slo: null with not_configured before the first save. */
export type ServiceSloSettings = { target: "99.5" | "99.9" | "99.95" | "99.99" | null; window: "7d" | "30d" | null; revision: number; unavailable: Unavailable };
export type Resilience = {
  synced: string;
  rollout: {
    title: string;
    status: string;
    stable: string;
    steps: { weight: string; note: string; state: "done" | "current" | "todo" }[];
    split: { stable: number; canary: number };
    analysis: { name: string; value: string; threshold: string; ok: boolean }[];
    stable_series: number[];
    canary_series: number[];
    next: string;
    source: string;
  };
  breakers: { brand: string; upstream: string; mechanism: string; state: "closed" | "open" | "half_open" | "none"; threshold: string; ejected: string }[];
  breaker_timeline: { from: number; to: number; state: "closed" | "open" | "half_open" }[];
  breaker_note: string;
  limits: { brand: string; route: string; mechanism: string; limit: string; used: string; pct: number; rejected: number }[];
  limit_note: string;
  flags: { brand: string; key: string; desc: string; on: boolean; tag: { text: string; tone: Tone }; action: string }[];
  hpa: { min: number; max: number; current: number; desired: number; replicas: number[]; desired_series: number[]; cpu: number[]; target: number; note: string };
  ai: string[];
};
/** Real topology carries no layout, health or impact yet: status is null (unknown) and the page lays nodes out itself. */
export type TopologyNode = { id: string; name: string; brand?: string; sub: string; status: Health | null; group?: string; x: number; y: number; impacted?: boolean; kind?: string; cluster?: string; environment_ids?: string[] };
export type Topology = {
  groups: { id: string; label: string; zone: string; x: number; y: number; w: number; h: number }[];
  nodes: TopologyNode[];
  edges: { id: string; from: string; to: string; label?: string; tone: "ok" | "warn" | "danger" | null; fast?: boolean; width: number; path?: string; lx?: number; ly?: number; kind?: string }[];
  blast: string | null;
  tree: { depth: number; kind: string; name: string; brand?: string; status: Health | null; replicas: string; cpu?: number; mem?: number; alerts?: number; open?: boolean; hit?: boolean }[];
  sankey: null | { cols: { id: string; label: string; value: number; color: string }[][]; links: { from: string; to: string; value: number; color: string }[] };
  detail: null | { id: string; name: string; meta: string; brand: string; conns: { used: number; max: number }; from_service: { used: number; max: number; service: string }; wait: number[]; health: { k: string; v: string }[]; owner: string; dependents: string; source: string };
  unavailable_fields?: string[];
};
/**
 * GET /metrics/dashboard. Markers are null when no platform record falls in the window (not_generated); every metric
 * block is null without a reviewed Prometheus source and query (not_configured) or when the panels don't add up (unavailable).
 */
export type MetricsDashboard = {
  window: { labels: string[]; ticks: string[]; release_at: number | null; rollback_at: number | null; incident: [number, number] | null; incident_label: string | null };
  kpis: Kpi[] | null;
  requests: { ok: number[]; client: number[]; server: number[] } | null;
  slo: { remaining: number; availability: string; delta: string; burn: { window: string; value: string; tone: Tone; pct: number }[]; note: string } | null;
  /** heat/buckets are null when only the percentile panels are proven (PA-41). */
  latency: { p50: number[]; p95: number[]; p99: number[]; heat: number[][] | null; buckets: string[] | null } | null;
  endpoints: { label: string; value: number; tone?: Tone }[] | null;
  instances: { label: string; values: number[]; tone?: Tone }[] | null;
  unavailable: Unavailable;
};
export type TraceSummary = { id: string; op: string; duration_ms: number; spans: number; error: boolean; time: string };
export type Span = { id: string; depth: number; service: string; op: string; start: number; end: number; status: "ok" | "error"; label?: string; attrs?: { k: string; v: string }[]; error?: string };
export type TraceSearch = { points: { t: number; ms: number; error: boolean; id?: string }[]; release_at: number; items: TraceSummary[] };
export type TraceDetail = { id: string; op: string; duration_ms: number; spans: Span[]; time: string };
export type LogLine = { time: string; level: "ERROR" | "WARN" | "INFO"; service: string; message: string; trace?: string };
export type LogPattern = { template: string; level: LogLine["level"]; count: string; trend: number[]; new?: boolean };
/**
 * GET /logs: proven lines; counts, histogram and patterns are null with reasons when unproven, and `items` is
 * null too when no log source is bound at all (every key not_configured).
 */
export type LogSearch = { total: string | null; errors: string | null; histogram: number[][] | null; items: LogLine[] | null; patterns: LogPattern[] | null; unavailable: Unavailable };
export type Pipeline = { id: string; brand: string; ref: string; service: string; title: string; trigger: string; status: "success" | "failed" | "running" | "canceled"; stages: ("ok" | "danger" | "accent" | "neutral")[]; duration: string; time: string };
/** GET /delivery/runs item (native delivery run), used to pick a run for the changes view. */
export type DeliveryRunSummary = { id: string; pipeline_id?: string; application_id?: string; environment_id?: string; provider?: string; status?: string; version?: number; created_at?: string; updated_at?: string; snapshot?: { name?: string }; projection?: { status?: string; commit_sha?: string; message?: string } };
export type ChangesView = {
  kpis: Kpi[];
  sources: { brand: string; name: string; count: number; on: boolean }[];
  lanes: { service: string; marks: { at: number; tone: "ok" | "danger"; label?: string }[]; alerts: [number, number][] }[];
  axis: string[];
  pipelines: Pipeline[];
  detail: {
    id: string;
    title: string;
    meta: string;
    brand: string;
    stages: { name: string; jobs: { name: string; state: "ok" | "running"; brand?: string }[] }[];
    diff: { path: string; minus: string; plus: string };
    risk: { title: string; meta: string; text: string };
    alert: { text: string; incident: string };
  };
};

/* ---------------- integrations ---------------- */
export type ConnectorStatus = "ok" | "unverified" | "error";
export type ConnectorCategory = "model" | "observe" | "delivery" | "infra" | "im" | "knowledge" | "agent_obs" | "security";
export type TagChip = { label: string; tone?: Tone; mono?: boolean };
export type Connector = { id: string; brand: string; name: string; kind: string; category: ConnectorCategory; status: ConnectorStatus; tags: TagChip[]; meta: string };
export type ConnectorList = { items: Connector[]; counts: Record<ConnectorStatus, number>; categories: { id: ConnectorCategory | "all"; label: string; count: number }[] };
export type ConnectorDetail = Connector & {
  scope: string;
  address: string;
  auth: { brand?: string; text: string };
  checked: string;
  services: string[];
  queries: string[];
  tools: { name: string; desc: string; enabled: boolean }[];
  deeplink?: string;
};
export type ConnectorType = {
  brand: string;
  name: string;
  kind: string;
  version: string;
  auth_methods: { id: string; label: string; desc: string }[];
  scopes: { id: string; label: string; required: boolean }[];
  tools: { name: string; desc: string }[];
  webhook_url: string;
};

export type McpServer = {
  id: string;
  brand: string;
  name: string;
  desc: string;
  transport: string;
  caps: string;
  trust: { label: string; tone: Tone };
  apps: { label: string; tone?: Tone }[];
  status: "ok" | "pending" | "error";
  last: string;
};
export type McpList = { kpis: Kpi[]; items: McpServer[]; pending_change?: { server: string; title: string; tools: TagChip[]; note: string } };
export type McpDetail = McpServer & {
  version: string;
  counts: { tools: number; resources: number; prompts: number };
  tools: { name: string; desc: string; write: boolean; enabled: boolean; calls: string }[];
  resources: { uri: string; desc: string }[];
  prompts: { name: string; desc: string }[];
  calls: { time: string; agent: string; tool: string; ms: number; ok: boolean }[];
  trust_info: { readonly_check: string; scope: string[]; limit: string; auth: string };
};

export type ContractKind = "metric" | "log" | "trace" | "resource" | "change" | "alert";
export type PullDraft = {
  id: string;
  name: string;
  step: number;
  request: { method: string; host: string; path: string; params: { k: string; v: string; variable: boolean }[]; auth: string; rate: string; paging: string };
  sample: { status: number; meta: string; body: string; highlights: { text: string; color: string }[] };
  contract: ContractKind;
  contract_version: string;
  fields: { field: string; required: boolean; source: string; transform?: string; color: string; state: "ok" | "warn" | "none" }[];
  preview: { rows: { service: string; name: string; time: string; value: string; labels: Record<string, string>; bad?: boolean }[]; series: number[]; checks: TagChip[] };
};
/** GET /ingest/pull (PA-20): items null without a Pull configuration (not_configured). */
export type PullListItem = { id: string; name: string; application_id: string; environment_id: string; connector_id: string; connector_name: string | null; query_id: string; state: "ready" | "source_unavailable"; href: string };
export type PullList = { items: PullListItem[] | null; empty_reason: EmptyReason; unavailable: Unavailable };
export type PushEventType = { id: string; icon: string; name: string; schema: string; today: string; rejected: string; tone: Tone };
export type PushView = {
  types: PushEventType[];
  endpoint: { method: string; path: string; host: string; headers: { k: string; v: string; note: string }[]; example: string; responses: { code: string; tone: Tone; text: string }[] };
  deliveries: { time: string; source: string; key: string; code: string; tone: Tone; result: string; ms: string }[];
  stack: { enabled: boolean; components: { brand: string; name: string; role: string }[]; helm: string; compose: string; estimate: string };
};

/** A reviewed GitLab code candidate staged by POST /service-mappings/discover; accepting sends it back exactly. */
export type MappingCandidate = { id: string; version: string; service_id: string; service: string; environment_id: string; system_id: "code"; connector_id: string; target_id: string; external_id: string; binding_revision: number; expires_at: string };
export type MappingCell = { state: "ok" | "suggested" | "missing"; value?: string; brand?: string; candidate?: MappingCandidate };
/** Per-row save snapshot for PUT /service-mappings (see docs/examples/v3-service-mapping-save.md); absent rows aren't editable. */
export type MappingSave = { service_id: string; service_environment_id: string; binding_revision: number; bindings: { system_id: string; kind: string; connector_id: string; resource: string }[]; editable: boolean; unavailable_reason?: string };
export type ServiceMapping = {
  systems: { id: string; brand: string; label: string }[];
  rows: { service: string; cells: Record<string, MappingCell>; save?: MappingSave }[];
  suggestions: number;
};

export type CoverageCell = { state: "ok" | "partial" | "missing" | "na" | "pull"; brand?: string; text?: string; href?: string };
export type CoverageView = {
  kpis: Kpi[];
  types: { label: string; pct: number; tone: Tone }[];
  columns: { id: string; label: string }[];
  rows: { service: string; tier: string; cells: Record<string, CoverageCell>; owner?: string; score: number }[];
  impact: { answers: string; flagged: string; delta: string; lowered: string; by_type: { label: string; value: number; tone?: Tone }[]; eval_failures: number };
  fixes: { text: string; impact: string; action: string; href: string }[];
};

export type WebhookView = {
  inbound: { id: string; brand: string; name: string; url: string; verify: string; count: string; last: string; status: "ok" | "error" }[];
  deliveries: { time: string; event: string; code: string; ms: string }[];
  tokens: { name: string; scopes: string[]; used: string; expires: string }[];
  mcp: { enabled: boolean; url: string; tools: string[]; clients: string };
  outbound: { event: string; target: string; brand?: string }[];
};

/* ---------------- agent console ---------------- */
export type AgentLevel = "L0" | "L1" | "L2" | "L3";
/** score / runs_7d / success / avg_tokens are null until evaluation and usage producers exist (unknown, not zero). */
export type AgentSummary = { id: string; icon: string; name: string; desc: string; version: string; level: AgentLevel; score: number | null; runs_7d: string | null; success: string | null; avg_tokens: string | null; status: "stable" | "canary" | "paused" | (string & {}) };
export type Lifecycle = {
  /** null when no stage aggregate or to-do producer exists yet (see `missing`). */
  stages: { id: string; name: string; value: string; sub: string; tone: Tone }[] | null;
  agents: AgentSummary[];
  todos: { icon: string; tone: Tone; title: string; body: string; action: string; href: string }[] | null;
  missing?: string[];
};
/** Real versions carry configured_traffic; observed traffic, score, success and tokens are null until measured. */
export type AgentVersion = { version: string; status: "canary" | "stable" | "retired" | "draft"; traffic: string | null; configured_traffic?: string; score: string | null; success: string | null; tokens: string | null; delta?: string; created: string };
export type AgentDetail = AgentSummary & {
  owner: string;
  account: string | null;
  summary: string;
  versions: AgentVersion[];
  /** current / gates / tokens are null until a routing and evaluation producer exists. */
  release: { from: string; to: string; current: number | null; target: number; configured_canary_percent?: number; gates: { label: string; value: string; pass: boolean }[] | null; tokens: { base: number[]; canary: number[] } | null; missing?: string[] };
  diff: { key: string; before?: string; after: string; added?: boolean }[] | null;
  config: { icon: string; key: string; value: string }[] | null;
};
export type PromptItem = { id: string; kind: "prompt" | "skill"; name: string; version: string; status?: string };
export type PromptDetail = {
  id: string;
  name: string;
  version: string;
  status?: string;
  variables: string[];
  used_by: string;
  lines: { text: string; added?: boolean }[];
  notes: string;
};
export type PromptTrial = { version: string; score: string; tokens: string; duration: string; text: string; good: boolean };
export type ToolRow = { id: string; brand?: string; name: string; kind: "read" | "write"; risk: "low" | "medium" | "high"; approval: string; agents: string; envs: string; calls: string; error: string };
export type ToolsView = {
  items: ToolRow[];
  levels: { code: AgentLevel; name: string; desc: string }[];
  env_limits: { env: string; max: AgentLevel; note: string }[];
  rules: string[];
};
/** Before a run has results, dataset/metrics/radar/cases/datasets/evaluators are null (see missing). */
export type EvalExperiment = {
  id: string;
  title: string;
  dataset: string | null;
  meta: string;
  status?: string;
  baseline_version?: string;
  candidate_version?: string;
  gate_failed?: string;
  metrics: { name: string; a: string; b: string; delta: string; gate: string; pass: boolean | null }[] | null;
  radar: { axes: string[]; a: number[]; b: number[] } | null;
  cases: { id: string; title: string; expected: string; a: boolean; b: boolean; score: string; duration: string }[] | null;
  datasets: { name: string; count: number; desc: string; tone: Tone }[] | null;
  evaluators: { name: string; kind: string }[] | null;
  missing?: string[];
};
/** GET /evals (PA-20): persisted experiments; execution_status / finished_at are null until a run is recorded. */
export type EvalListItem = { id: string; title: string; agent_id: string; agent_name: string; application_id: string; environment_id: string; baseline_version: string; candidate_version: string; status: "pending" | "running" | "passed" | "failed" | "invalid"; execution_status: string | null; created_at: string; finished_at: string | null; href: string };
export type EvalList = { items: EvalListItem[]; total: number; page: number; page_size: number; empty_reason: EmptyReason; unavailable: Unavailable };
type EvalCaseAlert = { sev: string; title: string; labels: { k: string; v: string }[] };
type EvalCaseEvidence = { brand: string; title: string; sub: string }[];
type EvalCaseExpected = { cause: string; advice: string };
/**
 * GET /eval-cases/{id}. Without a trusted snapshot the alert / evidence / expected / output / baseline / auto blocks are
 * null (see `missing`); `human_input` may still carry the registered input. Labels post `next` + `expected_revision`.
 */
export type EvalCase = {
  id: string;
  title: string;
  queue: string;
  alert: EvalCaseAlert | null;
  evidence: EvalCaseEvidence | null;
  expected: EvalCaseExpected | null;
  output: { version: string; meta: string; tools: string[]; markdown: string } | null;
  baseline: { version: string; score: string; text: string } | null;
  auto: { score: string; note: string; rules: string[] } | null;
  revision?: number;
  labels?: { label: string; note: string; actor: string; time: string }[];
  human_input?: { title: string; input: string; alert: EvalCaseAlert; evidence: EvalCaseEvidence; expected: EvalCaseExpected; checks: string[] } | null;
  missing?: string[];
};
/** Sample rows carry display strings; real rows carry counts, `duration_ms`, `started_at` and vote totals (null when unreported). */
export type RunFeedback = { icon: string; tone: Tone; text: string } | { up: number; down: number; my_vote: string | null };
export type RunRow = {
  id: string;
  agent: string;
  version: string;
  trigger: string;
  scope: string;
  status: "success" | "failed" | "blocked" | "running" | (string & {});
  steps: string | number | null;
  tokens: string | number | null;
  duration?: string | null;
  duration_ms?: number | null;
  started_at?: string;
  feedback?: RunFeedback | null;
};
/**
 * trend/durations/counts are null until runs are recorded (see `missing`). Sample `durations` are histogram buckets;
 * real ones are the sorted durations in ms, and real trends name their window start and bucket size.
 */
export type RunsView = {
  kpis: Kpi[];
  trend: { ok: number[]; failed: number[]; blocked: number[]; window_start_at?: string; interval_minutes?: number } | null;
  durations: number[] | null;
  items: RunRow[];
  counts: Record<"all" | "failed" | "blocked" | "negative", number> | null;
  truncated?: boolean;
  missing?: string[];
};
/**
 * GET /agent-runs/{id}. `start`, `dur` and `total` are milliseconds. Steps are null without recorded spans, `total`
 * only exists for finished runs, and context / budget / isolated / memory have no producer yet (null, see `missing`).
 */
export type RunDetail = {
  id: string;
  chain: { label: string; value: string; icon: string; to?: string }[];
  stats: { k: string; v: string }[];
  steps: { id: string; icon: string; name: string; start: number; dur: number | null; meta: string; kind: "model" | "tool" | string; call?: string; result?: string[]; connector?: string }[] | null;
  total: number | null;
  context: { name: string; tokens: string; pct: number; color: string }[] | null;
  budget: string | null;
  isolated: string[] | null;
  memory: { icon: string; name: string; desc: string; hits: { text: string; score?: string }[] }[] | null;
  status?: "running" | "success" | "failed" | "blocked";
  partial?: boolean;
  missing?: string[];
};
export type GuardPolicy = { id: string; icon: string; name: string; desc: string; stat: string; enabled: boolean; forced?: boolean };
/** Real event times are RFC3339; `detail` is the reason code there. */
export type GuardEvent = { id: string; time: string; agent: string; run: string; rule: string; action: "blocked" | "masked" | (string & {}); detail: string; target_kind?: string };
export type GuardrailsView = {
  policies: GuardPolicy[];
  trend: number[][];
  /** UTC days (YYYY-MM-DD) for the real trend columns. */
  trend_days?: string[];
  events: GuardEvent[];
  counts: Record<"all" | "blocked" | "masked", number>;
  /** Real events don't keep the source text, snippet, masked text or verdict (null); detail is null with no events. */
  detail: { id: string; title: string; meta: string; source: string | null; snippet: string | null; masked: string | null; verdict: string | null; run?: string } | null;
};

/* ---------------- settings ---------------- */
export type NotifyChannel = "feishu" | "email" | "sms" | "phone";
/**
 * GET /me/profile (V3ProfileView). Identity-evidence fields are null without a trusted identity source;
 * `unavailable` names them (e.g. `contacts[].verified`, `contacts.feishu`, `security.factors`, `security.sessions[].device`).
 * PUT sends the GET body back; only name, display_name, title, timezone and notifications may change.
 */
export type ProfileView = {
  name: string;
  display_name: string;
  department: string;
  title: string;
  timezone: string;
  contacts: { kind: "email" | "phone" | "feishu"; label: string; value: string; verified: boolean | null; note?: string }[];
  notifications: { event: string; label: string; channels: Record<NotifyChannel, boolean> }[];
  security: {
    password_changed: string;
    mfa_enabled: boolean | null;
    org_rule: string | null;
    factors: { kind: "passkey" | "totp" | "recovery"; name: string; sub: string }[] | null;
    sessions: { id: string; device: string | null; sub: string; current?: boolean }[];
    tokens: string;
  };
  unavailable: Unavailable;
};
export type NotificationSettings = { quiet: { enabled: boolean; from: string; to: string }; digest: "off" | "daily" | "weekly"; subscriptions: { id: string; label: string; sub: string; on: boolean }[] };
export type OrgNode = { id: string; name: string; count: number; depth: number; kind: "org" | "team" | "group" };
export type Member = {
  id: string;
  name: string;
  email: string;
  department: string;
  roles: string[];
  phone: string;
  mfa: "passkey" | "totp" | "none" | "na";
  source: "feishu" | "ldap" | "system" | "invite";
  status: "active" | "disabled";
  last_login: string;
  agent?: boolean;
};
export type MembersView = { org: OrgNode[]; groups: OrgNode[]; items: Member[]; counts: Record<"all" | "admin" | "no_mfa" | "disabled", number>; mfa_grace_days: number };
export type Role = { id: string; name: string; members: number; kind: "system" | "business" | "machine" };
export type RoleDetail = Role & {
  desc: string;
  member_names: string[];
  scope: string[];
  actions: string[];
  matrix: { resource: string; grants: (0 | 1 | 2)[] }[];
  notes: string;
};
export type SecurityView = {
  sso: { brand: string; name: string; sub: string; enabled: boolean; default?: boolean }[];
  scim: boolean;
  mfa_policy: "all" | "privileged" | "none";
  mfa_methods: { id: string; label: string; enabled: boolean; note?: string }[];
  approval_passkey: boolean;
  session: { idle: string; max: string; ip_allow: string; new_device_notice: boolean };
  vault: { name: string; sub: string; connected: boolean };
  token_ttl: string;
  password_policy: string;
  /** `{}` on a proven view; PUT may echo it only when empty. */
  unavailable?: Unavailable;
};
/** GET /security without a configured authority: every field null (not_configured) and no ETag, so it can't be saved. */
export type SecurityUnknownView = { [K in Exclude<keyof SecurityView, "unavailable">]: null } & { unavailable: Unavailable };
export type SecurityRead = SecurityView | SecurityUnknownView;
/** result "pending" = a lifecycle step whose outcome is a later row; null = unclassifiable (see unavailable.result). */
export type AuditEntry = { id: string; time: string; actor: { kind: "user" | "agent" | "system"; name: string }; action: string; target: string; result: "ok" | "pending" | "blocked" | "failed" | null; source: string; unavailable?: Unavailable };
export type AuditView = {
  items: AuditEntry[];
  counts: Record<"all" | "user" | "agent" | "system", number>;
  volume: number[][];
};
export type AuditDetail = { id: string; title: string; time: string; actor_kind: AuditEntry["actor"]["kind"]; actor_name: string; actor: { k: string; v: string }[]; links: { k: string; v: string; to: string }[]; payload: string; chain_ok: boolean };

/* ---------------- composer: models, uploads, mention previews, commands (v4) ---------------- */
export type ReasoningLevel = { id: string; label: string; hint?: string };
export type ModelInfo = {
  id: string;
  name: string;
  provider: string;
  brand: string;
  connector: string;
  api: "responses" | "chat_completions" | "messages";
  reasoning: { kind: "levels" | "toggle" | "none"; levels: ReasoningLevel[]; default?: string };
  tools: boolean;
  vision: boolean;
  context_k: number;
  cost: string;
  note?: string;
  available: boolean;
  unavailable_reason?: string;
};
export type ModelCatalog = { items: ModelInfo[]; default: ModelChoice };
export type ModelChoice = { model: string; reasoning?: string };
export type UploadKind = "file" | "image" | "link" | "paste";
export type Upload = { id: string; kind: UploadKind; name: string; size: number; mime: string; url?: string };
export type MentionPreview = {
  kind: RefKind;
  id: string;
  title: string;
  subtitle: string;
  tier?: Tier;
  status?: Tone;
  status_label?: string;
  metrics: { label: string; value: string; tone?: Tone; series: number[] }[];
  change?: { brand: string; text: string; meta: string };
  related: { label: string; tone?: Tone }[];
  href?: string;
};
export type CommandItem = {
  id: string;
  group: "command" | "skill" | "runbook" | "template";
  name: string;
  desc: string;
  icon: string;
  badge?: string;
  creates?: "canvas" | "report" | "proposal";
  params?: { name: string; example: string; required: boolean }[];
  usage?: string;
};
export type ModelAdminView = {
  providers: { brand: string; name: string; api: string; models: number; status: "ok" | "unverified" | "error" }[];
  items: (ModelInfo & { used_by: string; scope: string; status: "ok" | "unverified"; is_default?: boolean })[];
  detail: { model: string; api_chain: { api: string; note: string }[]; mapping: { level: string; param: string; is_default?: boolean }[]; checks: string[]; roles: string; agents: string; limits: string };
};

/* ---------------- v4: generic canvases ---------------- */
export type TileTone = Tone | "t1" | "dev";
export type CanvasKind = "investigation" | "plan" | "review" | "analysis" | "custom";
export type CanvasStatus = "draft" | "active" | "waiting_approval" | "done" | "resolved";
export type CanvasThumb = { nodes: [number, number, number, number, string][]; edges: string[] };
export type CanvasSummary = {
  id: string;
  title: string;
  kind: CanvasKind;
  status: CanvasStatus;
  meta: string;
  participants: string[];
  updated_at: string;
  href: string;
  investigation_id?: string;
  services: string[];
  thumb: CanvasThumb;
};
export type CanvasTemplateOption = { id: string; kind: CanvasKind; name: string; desc: string; nodes: string; is_default?: boolean };
export type CanvasBlock =
  | { type: "kv"; items: [string, string][] }
  | { type: "tags"; items: string[] }
  | { type: "text"; text: string }
  | { type: "bars"; values: number[]; highlight?: number; color?: string }
  | { type: "rows"; items: { badge?: string; badge_tone?: Tone; dot?: Tone; text: string; value?: string; state?: string; state_tone?: Tone }[] }
  | { type: "steps"; items: { text: string; sub: string; state: "done" | "gate" | "todo" }[] }
  | { type: "callout"; tone: Tone; icon?: string; text: string };
export type CanvasDocNode = { id: string; x: number; y: number; w: number; icon: string; tone: TileTone; title: string; sub: string; badge?: { text: string; tone: Tone }; blocks: CanvasBlock[] };
export type CanvasDocEdge = { from: string; to: string; tone?: Tone; dash?: boolean; live?: boolean };
export type CanvasDocArtifact = { id: string; x: number; y: number; icon?: string; brand?: string; ext?: string; tone?: Tone; title: string; sub: string; href?: string };
export type CanvasTurn = { id: string; role: "user" | "assistant"; author: string; text: string; model?: string; reasoning_label?: string; brand?: string };
export type CanvasDoc = {
  id: string;
  title: string;
  kind: CanvasKind;
  status: CanvasStatus;
  env?: EnvName;
  participants: string[];
  note: string;
  proposal?: { id: string; href: string };
  nodes: CanvasDocNode[];
  edges: CanvasDocEdge[];
  artifacts: CanvasDocArtifact[];
  threads: Record<string, { context: string; turns: CanvasTurn[] }>;
};

/* ---------------- v4: traces (application + agent) ---------------- */
export type TraceFacet = { key: string; label: string; items: { value: string; count: number; color?: string; tone?: Tone }[] };
export type TraceRow = { id: string; time: string; op: string; services: string[]; duration_ms: number; spans: number; errors: number; tokens?: string; cost?: string; agent?: string; model?: string; href: string };
/**
 * GET /traces. A complete list has every field; the observed list (`partial: true`) only proves the closed traces it
 * found, so total, facets, histogram, scatter and view counts are null with reasons, and items is null too without a
 * trace source binding.
 */
export type TraceList = {
  scope: "app" | "agent";
  total: number | null;
  took: string;
  colors: Record<string, string>;
  facets: TraceFacet[] | null;
  views: { id: string; label: string; count?: number | null; query: string }[];
  histogram: { counts: [number, number][]; axis: string[] } | null;
  scatter: { x: number; y: number; error: boolean; id?: string }[] | null;
  items: TraceRow[] | null;
  partial?: boolean;
  unavailable: Unavailable;
};
export type TraceSpan = {
  id: string;
  depth: number;
  service: string;
  op: string;
  start: number;
  end: number;
  status: "ok" | "error";
  critical?: boolean;
  label?: string;
  attrs?: { k: string; v: string; hot?: boolean }[];
  events?: { at: string; name: string; text: string }[];
  logs?: { time: string; level: "ERROR" | "WARN" | "INFO"; message: string }[];
  links?: { kind: "metric" | "change" | "runbook"; label: string; href: string; brand?: string }[];
};
export type TraceView = {
  id: string;
  trace_id: string;
  op: string;
  duration_ms: number;
  time: string;
  version: string;
  colors: Record<string, string>;
  spans: TraceSpan[];
  flame_note: string;
  graph: { nodes: { id: string; x: number; y: number; sub: string; danger?: boolean }[]; edges: { from: string; to: string; label: string; danger?: boolean }[] };
};
export type AgentSpanKind = "agent" | "chain" | "llm" | "tool" | "retriever" | "guardrail" | "output";
export type AgentSpan = {
  id: string;
  depth: number;
  kind: AgentSpanKind;
  name: string;
  meta: string;
  /** Milliseconds from the run start; dur is null when the span recorded none. */
  start: number;
  dur: number | null;
  llm?: {
    model: string;
    effort: string;
    api: string;
    max_tokens: number;
    first_token: string;
    cost: string;
    status: string;
    usage: { cached: number; input: number; reasoning: number; output: number };
    messages: { role: string; tone: Tone | "t0"; text?: string; tokens: string; folded?: boolean; json?: unknown }[];
    output?: string;
    params: Record<string, unknown>;
    scores: { name: string; value: string; source: string; tone: Tone }[];
  };
  tool?: { brand: string; connector: string; budget: string; args: unknown; result: unknown; evidence: string };
  guard?: { target: string; rule: string; result: string };
};
/** GET /agent-runs/{id}/trace. Times are milliseconds; scores, session and turns have no producer yet and spans are null without records. */
export type AgentTrace = {
  id: string;
  agent: string;
  status: "running" | "success" | "failed" | "blocked";
  stats: { k: string; v: string }[];
  scores: { label: string; tone: Tone; icon?: string }[] | null;
  session: string | null;
  turns: { n: number; brand: string; model: string; meta: string; current?: boolean; switched?: boolean; run_id?: string }[] | null;
  total: number | null;
  spans: AgentSpan[] | null;
  partial?: boolean;
  missing?: string[];
};

/* ---------------- v4: instance & environments ---------------- */
/** A platform instance covers one isolation boundary: production runs alone; non-production environments share one. */
export type InstanceTier = "production" | "nonprod";
export type EnvironmentInfo = { id: string; label: string; kind: "standing" | "feature"; tone: "prod" | "stg" | "dev"; clusters: string[]; note?: string };
export type InstanceInfo = {
  tier: InstanceTier;
  name: string;
  domain: string;
  environments: EnvironmentInfo[];
  default_env: string;
  /** The other instance, reachable only as a link (separate domain and sign-in). */
  other?: { label: string; url: string; domain: string };
};
export type LowerSource = { id: string; name: string; brand: string; env: string; env_label: string; purpose: string; credential: string };
export type SyncItem = { id: string; kind: "runbook" | "alert_rule" | "agent_version" | "template"; kind_label: string; title: string; mr: string; author: string; meta: string; status: "pending" | "imported" | "exported" | "merged" };
