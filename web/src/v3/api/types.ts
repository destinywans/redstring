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
/** at is RFC 3339; environment_label is the instance's label for the environment, or its ID (PA-83). */
export type RecentItem = { id: string; kind: "investigation" | "conversation" | "artifact"; title: string; href: string; at: string; environment_id?: string; environment_label?: string };

/* ---------------- home ---------------- */
export type HomeSummary = {
  greeting: { name: string; summary: string };
  /** null until generated; oncall inside is null without an on-call source. */
  briefing: { summary: string; generated_at: string; citations: { n: number; label: string; href: string }[]; oncall: { name: string; until: string; next: string } | null } | null;
  suggestions: { id: string; text: string; tone?: Tone; icon: string }[];
  kpis: Kpi[];
  /** alert_state: is the incident's alert still firing (null when the source didn't say). Resolved items stay listed (D7). */
  attention: { id: string; severity: Severity; service: string; env: EnvName; symptom: string; meta: string; ai: "done" | "running" | null; owner?: string; href: string; alert_state?: "firing" | "resolved" | null; /** PA-83: the time inside meta (alert start while firing, else last update). */ at?: string }[];
  /** Distinct visible services whose alerts are still firing; null without events.view (PA-58). */
  firing_services?: number | null;
  todos: { id: string; kind: "approval" | "report" | "runbook" | "access"; title: string; meta: string; action: string; href: string }[];
  services: { id: string; name: string; tier: Tier | null; status: "ok" | "warn" | "danger" | null; budget_pct: number | null; p99: string | null; error_rate: string | null; series: number[] | null }[];
  /** labels are UTC HH:MM (compatibility); bucket i starts at start + i * step_seconds — label it in the viewer's zone (PA-25). */
  alert_trend: { start?: string; step_seconds?: number; counts: [number, number, number, number][]; summary: string } | null;
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
  /** code: reply_failed / reply_interrupted (failed), evidence_limit / output_truncated / canvas_history_too_large (finished under a limit); none for evidence steps. */
  | { type: "status"; text: string; detail?: string; code?: string }
  /** AI rollback suggestion as a message part (sample conversations); the card only opens the proposal drawer. */
  | ({ type: "rollback_suggestion" } & RollbackSuggestion)
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
  /** Real nodes: the start of the branch's latest completed AI reply (≤ 280 chars, "…" when cut), null without one (PA-75). */
  summary?: string | null;
  confidence?: "high" | "medium" | "low";
  evidence_refs?: string[];
  tools?: string;
  series?: number[];
  writeback?: { to: string; version: string };
  /** The branch's most recent AI rollback suggestion, null when there is none (PA-72, docs/api/v3-rollback.md). */
  rollback_suggestion?: RollbackSuggestion | null;
  /** Conclusions written back into this node, with the nodes they came from (PA-62). */
  writebacks?: { received: number; sources: string[] };
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
/**
 * GET /investigations/{id}/nodes/{node}/replies (PA-75): one branch's AI replies, newest first, cursor-paged. answer is
 * only set when completed; a running attempt whose lease ran out is failed/lease_expired. evidence_ids are in reading
 * order (the numbers a rollback suggestion cites).
 */
export type InvestigationReply = {
  id: string;
  text: string;
  author: string | null;
  answer: string | null;
  state: "running" | "completed" | "failed" | (string & {});
  failure_code: string | null;
  model: { id: string; name: string; provider: string };
  reasoning: string | null;
  created_at: string;
  completed_at: string | null;
  evidence_ids: string[];
  rollback_suggestion: RollbackSuggestion | null;
};
export type InvestigationReplies = { investigation_id: string; node_id: string; replies: InvestigationReply[]; has_more: boolean; next_cursor: string | null };
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
/** `at` is RFC 3339; the day is judged locally (PA-83). An `assignment` links (`href`) to the assigned object and says what it is in `assignment_kind` (PA-84). */
export type InboxItem = { id: string; kind: "alert" | "approval" | "mention" | "runbook" | "report" | "access" | "agent" | "assignment"; tone: Tone; title: string; sub: string; at: string; unread: boolean; href: string; alert_id?: string; assignment_kind?: "alert" | "incident" | "proposal" | null };
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
  /** The persisted environment row; the environment key ("prod") is what service catalog IDs carry. */
  environment_id?: string;
  environment_key?: string;
  unavailable?: string[];
};
/**
 * GET /alert-groups/{id}/policy. group_by, route, timing, receivers and policy_source are null when the Alertmanager
 * route can't be read; `unavailable` then names those fields and the cause (policy_source, policy_source_binding,
 * occurrence_labels, route_ambiguous). inhibition is null and timeline entries may lack done (unknown).
 */
export type AlertGroupPolicy = {
  group_by: string[] | null;
  route: string | null;
  timing: { key: "group_wait" | "group_interval" | "repeat_interval"; label: string; value: string; help: string }[] | null;
  timeline: { id?: string; time: string; channel: string; text: string; done?: boolean | null }[];
  inhibition: string[] | null;
  receivers?: string[] | null;
  policy_source?: string | null;
  local_grouping?: string[];
  partial?: boolean;
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
export type AlertRuleSummary = {
  id: string;
  folder: string;
  name: string;
  state: "firing" | "pending" | "ok";
  schedule?: string;
  /** D11 §11.6: where the rule is in its GitOps lifecycle (separate from whether it fires); ids are null without a disable in progress. */
  lifecycle?: AlertRuleLifecycle;
};
export type AlertRuleLifecycle = { state: "active" | "disable_pending_approval" | "disabling" | "needs_attention"; proposal_id: string | null; execution_id: string | null };
/** D11 §11.6: an enable or disable not in effect yet (only proposals the viewer may see; newest first, at most 50). */
export type AlertRulePending = {
  proposal_id: string;
  execution_id: string | null;
  operation: "enable" | "disable";
  rule_name: string;
  group: string;
  application_id: string;
  environment_id: string;
  /** failed and rejected only within 7 days. */
  state: "pending_approval" | "executing" | "needs_attention" | "failed" | "rejected";
  updated_at: string;
  /** The review page while pending approval, the execution page once it runs. */
  href: string;
};
/**
 * List-endpoint emptiness (PA-09): items null = source not configured (see unavailable); [] with
 * empty_reason "no_authorized_target" = nothing visible to this user; [] with null = genuinely none.
 */
export type EmptyReason = "no_authorized_target" | null;
/** pending is null (and unavailable says "pending" / "items[].lifecycle") when the GitOps view can't be read. */
export type AlertRuleList = { items: AlertRuleSummary[] | null; counts: { all: number; firing: number; scheduled: number } | null; empty_reason: EmptyReason; unavailable: Unavailable; pending?: AlertRulePending[] | null };
/**
 * GET /alert-rules/{id} (V3AlertRuleDetail). The real rule comes from its bound Prometheus file: query_ref, preview,
 * condition, evaluation, runbook, route_path and ai_triage have no trusted source and are null (listed in
 * `unavailable`, a list of field names); the sample fills them.
 */
export type AlertRule = AlertRuleSummary & {
  datasource: string;
  query_ref: string | null;
  expr: string;
  for_seconds?: number;
  preview: { values: number[]; threshold: number } | null;
  condition: { op: ">" | "<"; value: number; unit: string; for_minutes: number; recover_minutes: number } | null;
  evaluation: { mode: "continuous" | "cron"; interval: string; cron?: string; no_data: string } | null;
  evaluation_interval_seconds?: number | null;
  evaluation_ms?: number | null;
  labels: { k: string; v: string }[];
  annotations?: Record<string, string>;
  annotation: string | null;
  runbook: string | null;
  route_path: string[] | null;
  ai_triage: { enabled: boolean; template: string } | null;
  /** When Prometheus last evaluated the rule. */
  last_eval: string | null;
  partial?: boolean;
  unavailable?: string[];
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
export type Silence = { id: string; matchers: { k: string; v: string }[]; state: "active" | "planned" | "expired"; starts_at: string; ends_at: string; remaining?: { pct: number; text: string }; affects: number; creator: string; reason: string };
/** GET /silences: items/counts null without Alertmanager (unavailable says why); [] + empty_reason when nothing is visible. */
export type SilenceList = { items: Silence[] | null; counts: { active: number; planned: number; expired: number } | null; empty_reason: EmptyReason; unavailable: Unavailable };
export type Inhibition = { id: string; source: { k: string; v: string }[]; target: { k: string; v: string }[]; equal: string[]; active: boolean; stat: string };

/* ---------------- proposals & executions ---------------- */
/** How a proposal was decided (PA-102). expired: approved, but the approval lapsed before any executor took it up. */
export type ProposalDecision = {
  state: "approved" | "rejected" | "expired";
  decided_at: string;
  /** The two counted votes, in vote order; empty for a rejection. */
  approvers: { id: string; name: string; method: "passkey"; at: string }[];
  rejected_by: { id: string; name: string; at: string } | null;
  /** An approval withdrawn after it was given (its execution is refused). */
  revoked: { id: string; name: string; at: string } | null;
};
export type Proposal = {
  id: string;
  title: string;
  status: "pending" | "approved" | "rejected" | "executed";
  risk: "low" | "medium" | "high";
  impact: string;
  reversible: boolean;
  proposed_by: string;
  /** The proposer's display name when the server has one. */
  proposed_by_name?: string;
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
  /** PA-102: from the approval records; null while pending (also with one of two votes). */
  decision?: ProposalDecision | null;
  /** e.g. kubernetes.deployment.rollback; null for display-only proposals (docs/api/v3-rollback.md §3). */
  action_kind?: string | null;
  rollback?: ProposalRollback | null;
  /** D11 §11.4: set for prometheus.alert_rule.enable / .disable, null otherwise. */
  alert_rule?: ProposalAlertRule | null;
};

/* ---------------- Alert rule enable / disable through GitOps (D11 §11.4–11.5) ---------------- */
export const ALERT_RULE_KINDS = ["prometheus.alert_rule.enable", "prometheus.alert_rule.disable"];
export type ProposalAlertRule = {
  operation: "enable" | "disable";
  rule: { name: string; group: string; expr: string; for: string; severity: string; labels: Record<string, string>; annotations: Record<string, string> };
  /** connector_id, project_path and web_url are null when the deployment binding doesn't name them. */
  repository: { connector_id: string | null; project_id: string; project_path: string | null; web_url: string | null; target_branch: string; path: string; base_sha: string };
  /** The whole file's difference recorded with the proposal; op " " is an unchanged line. Null when none was recorded (older proposals). */
  change: { lines: { op: "+" | "-" | " "; text: string }[]; content_sha256: string } | null;
  prometheus: { group: string; file: string; readback_timeout_seconds: number };
  /** fast_forward by the executor (file_pull), or a person merging the MR in GitLab. */
  merge: { method: "fast_forward" | "merge_request"; by: "executor" | "human" };
};
export type AlertRuleExecState = "reserved" | "committing" | "merging" | "merged" | "loading" | "enabled" | "disabled" | "failed" | "needs_attention" | "uncertain";
export type AlertRuleFailureCode = "stale_base" | "branch_conflict" | "mr_closed" | "protection_changed" | "merge_refused" | "credential_rejected" | "approval_invalid" | "plan_changed" | "source_changed" | "load_timeout" | "rule_health_error";
/** Fields of steps not reached yet are null. */
export type ExecutionAlertRule = {
  proposal_id: string;
  operation: "enable" | "disable";
  state: AlertRuleExecState;
  /**
   * Every recorded step in order, retries included (committing → uncertain → committing). PA-90: `reason` is the
   * failure code of needs_attention / failed, what left uncertain unknown (prepare_unknown, merge_unknown,
   * merge_unavailable, reconciliation_unavailable), "manual" for a person's recheck; null otherwise.
   */
  steps: { state: AlertRuleExecState | "pending_review" | "recheck"; at: string; reason?: string | null }[];
  repository: { project_id: string; project_path: string; source_branch: string; target_branch: string; path: string; commit_sha: string | null; mr_iid: number | null; mr_url: string | null; merged_sha: string | null; merged_at: string | null };
  readback: {
    state: "pending" | "loaded" | "absent" | "mismatch" | "error" | "timeout";
    checked_at: string | null;
    first_seen_at: string | null;
    health: string | null;
    last_evaluation_at: string | null;
    last_error: string | null;
    /** PA-90: Prometheus's own reload status read on a load timeout, or why it couldn't be read. */
    reload: { last_successful: boolean | null; last_success_at: string | null; unavailable?: "not_found" | "ambiguous" | "source_unavailable" | "not_connected" | null } | null;
  };
  failure: { code: AlertRuleFailureCode | (string & {}); message: string } | null;
  /** Not in the contract (the recheck returns it); the sample sets it. */
  observed_rule_id?: string | null;
};
/** GET /executions/{id} for an alert rule (V3AlertRuleExecutionDetail). The rule's name comes from the proposal. */
export type AlertRuleExecutionDetail = {
  id: string;
  kind: "prometheus.alert_rule.enable" | "prometheus.alert_rule.disable";
  proposal_id: string;
  investigation_id: string;
  application_id: string;
  environment_id: string;
  approved_at: string;
  approvers: { name: string; verified_at: string }[];
  created_at: string;
  updated_at: string;
  alert_rule: ExecutionAlertRule;
};

/* ---------------- Kubernetes rollback (docs/api/v3-rollback.md, PA-08) ---------------- */
/**
 * AI rollback suggestion (PA-72). service_id is the encoded service ID for /services/{id}/rollback-candidates; cite n
 * is evidence_ids[n-1]. Only target_revision and reasons come from the model (server-checked); the rest from evidence.
 */
export type RollbackSuggestion = {
  service_id: string;
  workload: string;
  from_revision: string;
  target_revision: string;
  target_created_at?: string | null;
  target_change_cause?: string | null;
  reasons: { text: string; cites?: number[] }[];
  reply_id?: string;
  evidence_ids?: string[];
  suggested_at?: string;
};
export const ROLLBACK_KIND = "kubernetes.deployment.rollback";
/** One container-level difference; `to` null means the field goes away. Secret/ConfigMap values are never shown (value_hidden). */
export type RollbackChange = { container: string; field: "image" | "env" | "resources" | "command" | "other" | (string & {}); name: string | null; from: string | null; to: string | null; value_hidden: boolean };
export type RollbackContainer = { name: string; image: string };
export type RollbackVerificationPlan = { observation_seconds: number; request_rate_query_id: string; error_ratio_query_id: string; sample_age_query_id: string; max_error_ratio: number; min_request_rate: number };
export type RollbackBlockedReason = "workload_not_configured" | "rollback_not_allowed" | "write_not_approved" | "deployment_paused" | "credential_unavailable" | "workload_unavailable" | "executor_not_ready";
export type RollbackCandidate = { revision: string; created_at: string | null; change_cause: string | null; current: boolean; containers: RollbackContainer[]; changes: RollbackChange[]; template_digest: string };
export type RollbackCandidates = {
  service_id: string;
  application_id: string;
  environment_id: string;
  workload: { connector_id: string; namespace: string; deployment: string; uid: string; paused: boolean; current_revision: string; current_template_digest: string; replicas: { desired: number; updated: number; available: number } } | null;
  verification: RollbackVerificationPlan | null;
  /** Newest first; empty whenever blocked_reason is set. */
  candidates: RollbackCandidate[];
  blocked_reason: RollbackBlockedReason | null;
  observed_at: string;
};
export type ProposalRollback = {
  service_id: string;
  application_id: string;
  environment_id: string;
  namespace: string;
  deployment: string;
  from_revision: string;
  target_revision: string;
  containers: RollbackContainer[];
  changes: RollbackChange[];
  verification: RollbackVerificationPlan;
  reason: string;
};
export type RolloutStatus = "reserved" | "dispatching" | "unknown" | "applied" | "rolling_out" | "verifying" | "succeeded" | "failed" | "needs_attention";
export type RolloutStatusReason =
  | "drift"
  | "approval_invalid"
  | "authorization_changed"
  | "binding_changed"
  | "write_forbidden"
  | "write_unavailable"
  | "reconcile_unknown"
  | "rollout_timeout"
  | "progress_deadline_exceeded"
  | "verification_failed"
  | "verification_unavailable"
  | "target_changed";
/** GET /executions/{id} for a rollback (told apart by `kind`). rollout/verification are null before their stage. */
export type RolloutExecutionDetail = {
  id: string;
  kind: typeof ROLLBACK_KIND;
  proposal_id: string;
  investigation_id: string;
  application_id: string;
  environment_id: string;
  status: RolloutStatus;
  status_reason: RolloutStatusReason | null;
  approved_at: string;
  approvers: { name: string; verified_at: string }[];
  created_at: string;
  updated_at: string;
  target: { namespace: string; deployment: string; from_revision: string; target_revision: string };
  timeline: { state: string; at: string }[];
  /** current_revision is the new number Kubernetes assigns on rollback; success is told by status, not by comparing numbers. */
  rollout: { desired: number; updated: number; available: number; current_revision: string | null; observed_at: string } | null;
  verification: {
    plan: RollbackVerificationPlan;
    started_at: string | null;
    finished_at: string | null;
    result: "pending" | "passed" | "failed" | "unavailable";
    samples: { at: string; request_rate: number | null; error_ratio: number | null; sample_age_seconds: number | null; ok: boolean }[];
  } | null;
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
  /** Real records leave these null until a trustworthy source exists (listed in unknown_fields). */
  usage: { refs: number | null; resolve_rate: number | null; weekly: number[] | null; highlight: number | null };
  incidents: { id: string; title: string }[] | null;
  unknown_fields?: string[];
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
export type MemoryLayer = { id: "working" | "service" | "episodic" | "semantic" | "workspace"; name: string; count: string | number; desc?: string; description?: string; /** Real layers list the facts they hold. */ evidence?: { fact_ids?: string[] } };
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
  /** PA-83: `at` is the time embedded in text and meta. */
  ai: { text: string; investigation: string; proposal?: string; meta: string; at?: string } | null;
  signals: Kpi[] | null;
  slo: { availability: string | null; target: string | null; budget_left: number | null; burn: ServiceBurn[] | null; note: string };
  changes: { brand: string; title: string; meta: string; tag?: string; /** PA-83: the time inside meta. */ at?: string }[] | null;
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
/** One inventory collector kind per environment (PA-61); resources of collectors past stale_after_seconds are left out. */
export type TopologyCollection = {
  environment_id: string;
  source_kind: "kubernetes" | "docker" | "lima" | (string & {});
  state: "fresh" | "partial" | "stale" | "never" | (string & {});
  sources: number;
  fresh_sources: number;
  last_success_at: string | null;
  oldest_success_at: string | null;
  stale_after_seconds: number;
};
export type Topology = {
  groups: { id: string; label: string; zone: string; x: number; y: number; w: number; h: number }[];
  nodes: TopologyNode[];
  edges: { id: string; from: string; to: string; label?: string; tone: "ok" | "warn" | "danger" | null; fast?: boolean; width: number; path?: string; lx?: number; ly?: number; kind?: string }[];
  blast: string | null;
  tree: { depth: number; kind: string; name: string; brand?: string; status: Health | null; replicas: string; cpu?: number; mem?: number; alerts?: number; open?: boolean; hit?: boolean }[];
  sankey: null | { cols: { id: string; label: string; value: number; color: string }[][]; links: { from: string; to: string; value: number; color: string }[] };
  detail: null | { id: string; name: string; meta: string; brand: string; conns: { used: number; max: number }; from_service: { used: number; max: number; service: string }; wait: number[]; health: { k: string; v: string }[]; owner: string; dependents: string; source: string };
  unavailable_fields?: string[];
  collection?: TopologyCollection[];
};
/**
 * GET /metrics/dashboard. Markers are null when no platform record falls in the window (not_generated); every metric
 * block is null without a reviewed Prometheus source and query (not_configured) or when the panels don't add up (unavailable).
 */
export type MetricsDashboard = {
  window: {
    /** The instant of each point and of each axis tick (RFC 3339; PA-83). */
    label_times: string[];
    tick_times: string[];
    release_at: number | null;
    rollback_at: number | null;
    incident: [number, number] | null;
    /** PA-83: the incident span (end = window end while it's open; both null without an incident). */
    incident_start_at?: string | null;
    incident_end_at?: string | null;
  };
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
/** GitLab user as the change reads give it. */
export type GitLabUser = { username: string } | null;
/** An alert whose start fell near a change: only time adjacency, never a cause (basis is always time_adjacent). */
export type ChangeAdjacentAlert = { incident_id: string; offset_seconds: number; basis: "time_adjacent" };
export type GitLabDeployment = {
  id: string;
  deployment_id: number;
  iid: number;
  status: "created" | "running" | "success" | "failed" | "canceled" | "skipped" | "blocked";
  environment: string;
  ref: string;
  sha: string;
  short_sha: string;
  pipeline_id: number | null;
  job: { id: number; name: string } | null;
  user: GitLabUser;
  created_at: string;
  finished_at: string | null;
  web_url: string | null;
  merge_request_iid: number | null;
  /** The cluster revision whose change-cause names this pipeline; display only, not proof. */
  kubernetes_revision: string | null;
  adjacent_alerts: ChangeAdjacentAlert[] | null;
};
export type GitLabPipeline = { id: string; pipeline_id: number; status: string; ref: string; sha: string; short_sha: string; source: string; created_at: string; updated_at: string; web_url: string | null; merge_request_iid: number | null; deployment_ids: number[] };
export type GitLabMergeRequest = {
  id: string;
  iid: number;
  title: string;
  state: "opened" | "closed" | "merged" | "locked";
  source_branch: string;
  target_branch: string;
  author: GitLabUser;
  merged_by: GitLabUser;
  created_at: string;
  merged_at: string | null;
  closed_at: string | null;
  sha: string | null;
  merge_commit_sha: string | null;
  squash_commit_sha: string | null;
  web_url: string | null;
  pipeline_ids: number[];
  deployment_ids: number[];
};
/**
 * GET /changes?service_id=&range=24h|7d&sources=gitlab (D11 §11.1, V3GitLabChangesView). A list is null when its
 * connector tool is off (its KPIs are then missing and `unavailable["kpis.<id>"]` says not_configured).
 */
/** D11 §11.3: GET /incidents/{id}/changes — GitLab deployments and cluster revisions around an incident's start. */
export type IncidentGitLabDeployment = {
  kind: "gitlab_deployment";
  /** gitlab:deployment:<project>:<deployment id> */
  id: string;
  at: string;
  /** Change time minus the incident start; negative when the change came first. */
  offset_seconds: number;
  basis: "time_adjacent";
  status: "created" | "running" | "success" | "failed" | "canceled" | "skipped" | "blocked";
  sha: string;
  short_sha: string;
  ref: string;
  /** /changes?service_id=…&range=24h&focus=<id> */
  href: string;
  pipeline_id: number | null;
  merge_request: { iid: number; title: string } | null;
  kubernetes_revision: string | null;
  web_url: string | null;
};
export type IncidentKubernetesRevision = {
  kind: "kubernetes_revision";
  id: string;
  at: string;
  offset_seconds: number;
  basis: "time_adjacent";
  revision: string;
  change_cause: string | null;
  /** The window's GitLab deployment whose pipeline the change cause names; null = not deployed through GitLab (or unknown when GitLab isn't readable). */
  gitlab_deployment_id: number | null;
  /** What `at` comes from (PA-94). */
  time_basis: Exclude<RevisionTimeBasis, "unknown">;
  /** The ReplicaSet's creation; for a reused ReplicaSet not when the revision took effect. */
  created_at: string;
  replicaset_reused: boolean;
};
/** How a Kubernetes revision's time is known (PA-94). */
export type RevisionTimeBasis = "replicaset_created" | "gitlab_deployment" | "rollback_execution" | "unknown";
export type RevisionTimeUnknownReason =
  | "replicaset_reused"
  | "change_cause_stale"
  | "gitlab_deployment_not_found"
  | "gitlab_deployment_unfinished"
  | "gitlab_unavailable"
  | "rollback_execution_not_found"
  | "rollback_ledger_unavailable"
  | "out_of_order";
/** A revision of unknown time that took effect between not_before and not_after, an interval meeting the window: shown apart, "time unknown · between X and Y". */
export type IncidentUntimedRevision = {
  kind: "kubernetes_revision";
  id: string;
  revision: string;
  change_cause: string | null;
  time_unknown_reason: RevisionTimeUnknownReason;
  not_before: string;
  not_after: string;
  created_at: string;
  replicaset_reused: boolean;
};
/** A Kubernetes revision of the change view: `at` null means its time is unknown, bounded by not_before..not_after (PA-94). */
export type KubernetesRevision = {
  revision: string;
  /** The ReplicaSet's creation; for a reused ReplicaSet not when the revision took effect. */
  created_at: string;
  change_cause: string | null;
  gitlab_pipeline_id: number | null;
  replicaset_reused: boolean;
  at: string | null;
  time_basis: RevisionTimeBasis;
  time_unknown_reason: RevisionTimeUnknownReason | null;
  not_before: string | null;
  not_after: string | null;
};
export type IncidentChanges = {
  incident_id: string;
  service_id: string;
  environment_id: string;
  alert_started_at: string;
  window: { start_at: string; end_at: string };
  items: (IncidentGitLabDeployment | IncidentKubernetesRevision)[];
  /** Revisions of unknown time that may fall in the window; not on the timeline (PA-94). */
  untimed_revisions: IncidentUntimedRevision[];
  coverage: { gitlab: "complete" | "not_configured" | "no_authorized_target" | "unavailable"; kubernetes: "history_limited" | "not_configured" | "no_authorized_target" | "unavailable" };
  note: "time_adjacent_not_causal";
  unavailable: Unavailable;
};
export type GitLabChangesView = {
  service_id: string;
  environment_id: string;
  source: "gitlab";
  window: { start_at: string; end_at: string };
  project: { connector_id: string; project_id: string; path: string; web_url: string; deploy_environment: string; refs: string[] };
  kpis: Kpi[];
  series_start_at: string;
  series_step_seconds: number;
  deployments: GitLabDeployment[] | null;
  pipelines: GitLabPipeline[] | null;
  merge_requests: GitLabMergeRequest[] | null;
  /** null without an alert coverage statement (coverage.alerts unknown) or permission (not_authorized). */
  alerts: { incident_id: string; title: string; href: string; started_at: string; ended_at: string | null }[] | null;
  kubernetes_revisions: KubernetesRevision[] | null;
  coverage: { gitlab: "complete"; alerts: "complete" | "unknown" | "not_authorized"; kubernetes: "history_limited" | "not_configured" | "no_authorized_target" | "unavailable" };
  captured_at: string;
  unavailable: Unavailable;
};
/** GET /changes/pipelines/{pipeline_id}?service_id= (D11 §11.2, V3GitLabPipelineDetail); deployments come from the list row. */
export type GitLabPipelineDetail = {
  id: string;
  pipeline_id: number;
  status: string;
  ref: string;
  sha: string;
  source: string;
  user: GitLabUser;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  duration_seconds: number | null;
  web_url: string | null;
  stages: { name: string; status: string; jobs: { id: number; name: string; status: string; manual: boolean; started_at: string | null; finished_at: string | null; duration_seconds: number | null; web_url: string | null }[] }[];
  commit: { sha: string; title: string; authored_at: string; parent_ids: string[]; stats: { files: number | null; additions: number; deletions: number }; paths: string[] | null } | null;
  merge_request_iid: number | null;
  adjacent_alerts: ChangeAdjacentAlert[] | null;
  unavailable: Unavailable;
};

/* ---------------- integrations ---------------- */
export type ConnectorStatus = "ok" | "unverified" | "error";
export type ConnectorCategory = "model" | "observe" | "delivery" | "infra" | "im" | "knowledge" | "agent_obs" | "security";
export type TagChip = { label: string; tone?: Tone; mono?: boolean };
export type Connector = { id: string; brand: string; name: string; kind: string; category: ConnectorCategory; status: ConnectorStatus; tags: TagChip[]; meta: string; /** The last connection test (RFC 3339; null when never tested); meta carries no time (PA-83). */ checked_at?: string | null };
export type ConnectorList = { items: Connector[]; counts: Record<ConnectorStatus, number>; categories: { id: ConnectorCategory | "all"; label: string; count: number }[] };
export type ConnectorDetail = Connector & {
  scope: string;
  address: string;
  auth: { brand?: string; text: string };
  services: string[];
  queries: string[];
  tools: { name: string; desc: string; enabled: boolean }[];
  deeplink?: string;
  /**
   * Kubernetes connectors (PA-08 design): registered workload targets with the catalog's rollback setting and the
   * deployer's write approval, both read-only here. Not in the backend contract yet; absent fields hide the section.
   */
  workloads?: { key: string; namespace: string; deployment: string; service: string | null; rollback: { allowed: boolean; observation_seconds: number | null } | null; write_approved: boolean | null }[];
  capabilities?: string[];
  /** Registry stage 2 step 7: tools project the saved switches; false says they gate data reads only, not agent execution. */
  agent_execution_available?: boolean;
  /** D11 design (GitLab): how writes reach the provider, if at all. Not in the contract yet; absent hides the row. */
  write_path?: string;
  /**
   * D11 design (GitLab): the catalog's GitLab deploy environment and branch per service environment, reviewed by the
   * deployer and read-only. Not in the contract yet; absent hides the section.
   */
  deploy_environments?: { service: string; environment: string; project_id: string; deploy_environment: string; refs: string[] }[];
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
  sample: { status: number; meta: string; body: string; highlights: { text: string; color: string }[]; /** PA-83: the read time inside meta. */ captured_at?: string | null };
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
  /** "not_configured" until a Push source is configured for this instance. */
  availability?: string;
  types: PushEventType[];
  endpoint: { method: string; path: string; host: string; headers: { k: string; v: string; note: string }[]; example: string; responses: { code: string; tone: Tone; text: string }[] };
  deliveries: { at: string; source: string; key: string; code: string; tone: Tone; result: string; ms: string }[];
  stack: { enabled: boolean; components: { brand: string; name: string; role: string }[]; helm: string; compose: string; estimate: string };
};

/** A reviewed GitLab code candidate staged by POST /service-mappings/discover; accepting sends it back exactly. */
export type MappingCandidate = {
  id: string;
  version: string;
  service_id: string;
  service: string;
  environment_id: string;
  system_id: "code";
  connector_id: string;
  target_id: string;
  /** The GitLab numeric project ID. */
  external_id: string;
  binding_revision: number;
  expires_at: string;
  /** D11 design extras the contract doesn't send yet (the sample fills them): project path, a one-line hint, the suggested one. */
  project_path?: string;
  hint?: string;
  recommended?: boolean;
};
export type MappingCell = {
  state: "ok" | "suggested" | "missing";
  /** Code column (D20): the confirmed GitLab numeric project ID. */
  value?: string;
  brand?: string;
  candidate?: MappingCandidate;
  /**
   * D11 design, code column: every candidate when a service has more than one GitLab project, and who confirmed the
   * current one. Not in the contract yet (real candidates only exist for a single project); absent fields are hidden.
   */
  candidates?: MappingCandidate[];
  confirmed?: { by: string; at: string; revision: number; project_path?: string };
};
/** Per-row save snapshot for PUT /service-mappings (see docs/examples/v3-service-mapping-save.md); absent rows aren't editable. */
export type MappingSave = { service_id: string; service_environment_id: string; binding_revision: number; bindings: { system_id: string; kind: string; connector_id: string; resource: string }[]; editable: boolean; unavailable_reason?: string };
/** One mapping system in GET /services/{id}/external-targets/options (PA-19 stage 2, docs/api/v3-contract.md). */
export type MappingOptionSystem = {
  system_id: "metrics" | "logs" | "traces" | "code" | "deploy" | "dash" | "workload" | (string & {});
  editable: boolean;
  /** Since P4 every system is on the registry (registry_not_enabled / registry_cutover are no longer returned). */
  unavailable_reason: "system_not_supported" | "ambiguous_targets" | "forbidden" | null | (string & {});
  /** target_id null = a legacy binding. */
  current: { target_id: string | null; connector_id: string; resource_id: string } | null;
  /** Connectors of this environment for the system; not ready ones list why. resources are reviewed catalog IDs (code: bound GitLab projects). */
  options: { connector_id: string; name: string; provider: string; ready: boolean; not_ready: ("connector_disabled" | "tool_disabled" | "options_invalid" | "credential_missing" | "credential_not_approved" | (string & {}))[]; resources: string[] }[];
};
/** {id} is the encoded service ID (application + NUL + environment key). */
export type ExternalTargetOptions = { service_id: string; environment_id: string; systems: MappingOptionSystem[] };
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
  /** The last delivery: last_at (RFC 3339) and last_code, both null when unknown (PA-83). */
  inbound: { id: string; brand: string; name: string; url: string; verify: string; count: string; last_at: string | null; last_code: number | string | null; status: "ok" | "error" }[];
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
  /** The UTC midnight each trend bucket starts at (buckets are UTC days; PA-83). */
  trend_starts_at?: string[];
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
    /** sub is the login method (empty when unknown); signed_in_at (null when not recorded) and expires_at are RFC 3339 (PA-83). */
    sessions: { id: string; device: string | null; sub: string; current?: boolean; signed_in_at?: string | null; expires_at?: string }[];
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
/** counts.no_mfa, mfa_grace_days and the directory source are null / listed in unavailable_fields until their sources exist. */
export type MembersView = {
  org: OrgNode[];
  groups: OrgNode[];
  items: Member[];
  counts: Record<"all" | "admin" | "disabled", number> & { no_mfa: number | null };
  mfa_grace_days: number | null;
  unavailable_fields?: string[];
};
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
  detail: { model: string; api_chain: { api: string; note: string }[]; mapping: { level: string; param: string; is_default?: boolean }[]; checks: string[]; roles: string; agents: string; limits: string; /** PA-83: the last check (null when none in this process); not shown yet. */ checked_at?: string | null };
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
  /** `axis_times` are the bucket starts (RFC 3339; PA-83). */
  histogram: { counts: [number, number][]; axis_times: string[] } | null;
  scatter: { x: number; y: number; error: boolean; id?: string }[] | null;
  items: TraceRow[] | null;
  partial?: boolean;
  /**
   * PA-78: why the list is partial. "search_budget": the search went newest-first and stopped at its budget, so
   * only [window_start_at, window_end_at) of the range is covered; "search_unattested": the deployment doesn't
   * attest search completeness (no counts or charts); "not_configured": no Tempo source.
   */
  partial_reason?: "search_budget" | "search_unattested" | "incomplete_traces" | "not_configured" | (string & {}) | null;
  /** PA-87: traces left out for missing spans (no parent, several roots or a cycle); incomplete_traces when search is declared complete. */
  excluded_incomplete?: number;
  window_start_at?: string | null;
  window_end_at?: string | null;
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
/**
 * status verifying (kind unknown, reason not_ready): GitLab is still processing a just-merged MR (PA-100).
 * status unverifiable (kind unknown): this candidate could not be verified — reason gitlab_unavailable (refresh to retry)
 * or untrusted (it cannot be imported) (PA-103). Nothing is read from either; the others list as usual.
 */
export type SyncItem = {
  id: string;
  kind: "runbook" | "alert_rule" | "agent_version" | "template" | "unknown";
  kind_label: string;
  title: string;
  mr: string;
  author: string;
  meta: string;
  status: "pending" | "imported" | "exported" | "merged" | "verifying" | "unverifiable";
  reason?: "not_ready" | "gitlab_unavailable" | "untrusted";
};
