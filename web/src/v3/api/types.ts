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
export type AlertDetail = {
  id: string;
  severity: Severity;
  title: string;
  state: "firing" | "pending" | "resolved";
  source: string;
  env: EnvName;
  since: string;
  rule: string;
  triage?: { summary: string; confidence: "high" | "medium" | "low"; agent: string; duration_s: number; tools: number; investigation_id: string; proposal_id?: string };
  series: { labels: string[]; values: number[]; threshold: number; release_at?: number };
  labels: { k: string; v: string }[];
  summary: string;
  runbook?: { id: string; title: string };
  notifications: { time: string; channel: string; text: string; done: boolean }[];
  grouping: { group: string; inhibited: string; incident: { id: string; label: string }; change: { id: string; label: string } };
};
export type AlertInstance = { id: string; value: string; state: "firing" | "pending"; duration: string; series: number[]; pending?: { pct: number; text: string } };
export type AlertGroup = {
  id: string;
  severity: Severity;
  labels: { k: string; v: string }[];
  service: string;
  count: number;
  state: "firing" | "pending" | "silenced" | "resolved";
  since: string;
  notify: { channel?: string; text: string };
  badges: { text: string; tone: Tone }[];
  owner?: string;
  inhibited?: boolean;
  instances: AlertInstance[];
};
export type AlertGroupPolicy = {
  group_by: string[];
  route: string;
  timing: { key: "group_wait" | "group_interval" | "repeat_interval"; label: string; value: string; help: string }[];
  timeline: { time: string; channel: string; text: string; done: boolean }[];
  inhibition: string[];
};
export type ServicePeek = { id: string; name: string; tier: Tier; team: string; env: EnvName; status: "ok" | "warn" | "danger"; kpis: { k: string; v: string; tone?: Tone }[]; owner: string; oncall: string; change: string };
export type PersonPeek = { id: string; name: string; role: string; oncall?: string; load: string };
export type Incident = {
  id: string;
  severity: Severity;
  title: string;
  status: "active" | "mitigated" | "resolved";
  duration: string;
  commander: string;
  stage: number;
  timeline: { time: string; actor: { kind: "brand" | "ai" | "person"; name: string }; text: string; sub?: string }[];
  impact: { peak: string; budget_used: string; budget_left: number; note: string; series: number[] };
  roles: { role: string; name: string; ai?: boolean }[];
  links: { kind: RefKind; label: string; href: string }[];
};

/* ---------------- alert rules, routing, silences ---------------- */
export type AlertRuleSummary = { id: string; folder: string; name: string; state: "firing" | "pending" | "ok"; schedule?: string };
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
export type RouteNode = {
  id: string;
  title: string;
  matchers: { k: string; v: string }[];
  receivers: { channel: string; target: string }[];
  timing: [string, string, string];
  hits: number;
  continue?: boolean;
  children: RouteNode[];
};
export type Silence = { id: string; matchers: { k: string; v: string }[]; state: "active" | "planned" | "expired"; window: string; remaining?: { pct: number; text: string }; affects: number; creator: string; reason: string };
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
};
export type Execution = {
  id: string;
  proposal_id: string;
  status: "queued" | "executing" | "verifying" | "succeeded" | "failed";
  steps: { time: string; icon: string; tone: Tone; title: string; desc: string }[];
  observation: { values: number[]; threshold: number; labels: string[]; band: [number, number]; marker: number; checks: { text: string; value: string }[] };
  report: { title: string; severity: Severity; meta: string; summary: string; impact: string; timeline: { time: string; text: string }[]; actions: { text: string; owner: string; due: string; done: boolean }[]; refs: string };
};

/* ---------------- knowledge & memory ---------------- */
export type RunbookSummary = { id: string; title: string; tags: string[]; scope: string; owner: string; version: string; status: "published" | "review" | "draft"; refs_30d: number; resolve_rate?: number; last_verified: string; stale: boolean };
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
export type Postmortem = { id: string; title: string; severity: Severity; date: string; duration: string; cause: string; actions: string; status: "draft" | "published" };
export type PostmortemSummary = {
  kpis: Kpi[];
  items: Postmortem[];
  causes: { label: string; pct: number; color: string }[];
  impact_by_service: { label: string; value: number }[];
  followups: { text: string; owner: string; due: string; tone: Tone }[];
};
export type MemoryLayer = { id: "working" | "service" | "episodic" | "semantic"; name: string; count: string; desc: string };
export type MemoryFact = { id: string; fact: string; source: string; confidence: "high" | "medium" | "low"; confirmed: string | null; recalls: number; expires: string };
export type MemoryView = {
  layers: MemoryLayer[];
  services: { id: string; count: number }[];
  service: string;
  conflict?: { a: { text: string; source: string }; b: { text: string; source: string } };
  facts: MemoryFact[];
  policy: string;
};
export type KnowledgeSource = { id: string; brand?: string; name: string; kind: string; sync: string; docs: number; chunks: string; last: string; status: "connected" | "error"; error?: string };
export type RetrievalHit = { brand?: string; title: string; snippet: string; keyword: number; vector: number; score: number };

/* ---------------- resources & observability ---------------- */
export type Health = "ok" | "warn" | "danger";
export type ServiceSummary = {
  id: string;
  name: string;
  desc: string;
  tier: Tier;
  team: string;
  oncall: string;
  status: Health;
  slo: { target: string; budget_left: number };
  release: { brand: string; version: string; when: string };
  alerts: number;
  connectors: string[];
};
export type ServiceCatalog = { kpis: Kpi[]; items: ServiceSummary[]; facets: { key: string; title: string; items: { label: string; count: number; on: boolean }[] }[] };
export type ServiceDetail = {
  id: string;
  name: string;
  tier: Tier;
  team: string;
  env: EnvName;
  status: Health;
  desc: string;
  deeplinks: { brand: string; name: string; url: string }[];
  ai: { text: string; investigation: string; proposal?: string; meta: string };
  signals: Kpi[];
  slo: { availability: string; target: string; budget_left: number; burn: { window: string; value: string; tone: Tone; pct: number }[]; note: string };
  changes: { brand: string; title: string; meta: string; tag?: string }[];
  deps: { upstream: { brand?: string; name: string; status: Health; meta: string }[]; downstream: { brand?: string; name: string; status: Health; meta: string }[] };
  tabs: Record<string, number>;
};
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
export type TopologyNode = { id: string; name: string; brand?: string; sub: string; status: Health; group: string; x: number; y: number; impacted: boolean };
export type Topology = {
  groups: { id: string; label: string; zone: string; x: number; y: number; w: number; h: number }[];
  nodes: TopologyNode[];
  edges: { id: string; from: string; to: string; label?: string; tone: "ok" | "warn" | "danger"; fast?: boolean; width: number; path: string; lx: number; ly: number }[];
  blast: string;
  tree: { depth: number; kind: string; name: string; brand?: string; status: Health; replicas: string; cpu?: number; mem?: number; alerts?: number; open?: boolean; hit?: boolean }[];
  sankey: { cols: { id: string; label: string; value: number; color: string }[][]; links: { from: string; to: string; value: number; color: string }[] };
  detail: { id: string; name: string; meta: string; brand: string; conns: { used: number; max: number }; from_service: { used: number; max: number; service: string }; wait: number[]; health: { k: string; v: string }[]; owner: string; dependents: string; source: string };
};
export type MetricsDashboard = {
  window: { labels: string[]; ticks: string[]; release_at: number; rollback_at: number; incident: [number, number]; incident_label: string };
  kpis: Kpi[];
  requests: { ok: number[]; client: number[]; server: number[] };
  slo: { remaining: number; availability: string; delta: string; burn: { window: string; value: string; tone: Tone; pct: number }[]; note: string };
  latency: { p50: number[]; p95: number[]; p99: number[]; heat: number[][]; buckets: string[] };
  endpoints: { label: string; value: number; tone?: Tone }[];
  instances: { label: string; values: number[]; tone?: Tone }[];
};
export type TraceSummary = { id: string; op: string; duration_ms: number; spans: number; error: boolean; time: string };
export type Span = { id: string; depth: number; service: string; op: string; start: number; end: number; status: "ok" | "error"; label?: string; attrs?: { k: string; v: string }[]; error?: string };
export type TraceSearch = { points: { t: number; ms: number; error: boolean; id?: string }[]; release_at: number; items: TraceSummary[] };
export type TraceDetail = { id: string; op: string; duration_ms: number; spans: Span[]; time: string };
export type LogLine = { time: string; level: "ERROR" | "WARN" | "INFO"; service: string; message: string; trace?: string };
export type LogSearch = { total: string; errors: string; histogram: number[][]; items: LogLine[]; patterns: { template: string; level: LogLine["level"]; count: string; trend: number[]; new?: boolean }[] };
export type Pipeline = { id: string; brand: string; ref: string; service: string; title: string; trigger: string; status: "success" | "failed" | "running" | "canceled"; stages: ("ok" | "danger" | "accent" | "neutral")[]; duration: string; time: string };
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
export type PushEventType = { id: string; icon: string; name: string; schema: string; today: string; rejected: string; tone: Tone };
export type PushView = {
  types: PushEventType[];
  endpoint: { method: string; path: string; host: string; headers: { k: string; v: string; note: string }[]; example: string; responses: { code: string; tone: Tone; text: string }[] };
  deliveries: { time: string; source: string; key: string; code: string; tone: Tone; result: string; ms: string }[];
  stack: { enabled: boolean; components: { brand: string; name: string; role: string }[]; helm: string; compose: string; estimate: string };
};

export type MappingCell = { state: "ok" | "suggested" | "missing"; value?: string; brand?: string };
export type ServiceMapping = {
  systems: { id: string; brand: string; label: string }[];
  rows: { service: string; cells: Record<string, MappingCell> }[];
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
export type AgentSummary = { id: string; icon: string; name: string; desc: string; version: string; level: AgentLevel; score: number; runs_7d: string; success: string; avg_tokens: string; status: "stable" | "canary" | "paused" };
export type Lifecycle = {
  stages: { id: string; name: string; value: string; sub: string; tone: Tone }[];
  agents: AgentSummary[];
  todos: { icon: string; tone: Tone; title: string; body: string; action: string; href: string }[];
};
export type AgentVersion = { version: string; status: "canary" | "stable" | "retired"; traffic: string; score: string; success: string; tokens: string; delta?: string; created: string };
export type AgentDetail = AgentSummary & {
  owner: string;
  account: string;
  summary: string;
  versions: AgentVersion[];
  release: { from: string; to: string; current: number; target: number; gates: { label: string; value: string; pass: boolean }[]; tokens: { base: number[]; canary: number[] } };
  diff: { key: string; before?: string; after: string; added?: boolean }[];
  config: { icon: string; key: string; value: string }[];
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
export type EvalExperiment = {
  id: string;
  title: string;
  dataset: string;
  meta: string;
  gate_failed?: string;
  metrics: { name: string; a: string; b: string; delta: string; gate: string; pass: boolean | null }[];
  radar: { axes: string[]; a: number[]; b: number[] };
  cases: { id: string; title: string; expected: string; a: boolean; b: boolean; score: string; duration: string }[];
  datasets: { name: string; count: number; desc: string; tone: Tone }[];
  evaluators: { name: string; kind: string }[];
};
export type EvalCase = {
  id: string;
  title: string;
  queue: string;
  alert: { sev: string; title: string; labels: { k: string; v: string }[] };
  evidence: { brand: string; title: string; sub: string }[];
  expected: { cause: string; advice: string };
  output: { version: string; meta: string; tools: string[]; markdown: string };
  baseline: { version: string; score: string; text: string };
  auto: { score: string; note: string; rules: string[] };
};
export type RunRow = { id: string; agent: string; version: string; trigger: string; scope: string; status: "success" | "failed" | "blocked"; steps: string; tokens: string; duration: string; feedback?: { icon: string; tone: Tone; text: string } };
export type RunsView = { kpis: Kpi[]; trend: { ok: number[]; failed: number[]; blocked: number[] }; durations: number[]; items: RunRow[]; counts: Record<"all" | "failed" | "blocked" | "negative", number> };
export type RunDetail = {
  id: string;
  chain: { label: string; value: string; icon: string; to?: string }[];
  stats: { k: string; v: string }[];
  steps: { id: string; icon: string; name: string; start: number; dur: number; meta: string; kind: "model" | "tool"; call?: string; result?: string[]; connector?: string }[];
  total: number;
  context: { name: string; tokens: string; pct: number; color: string }[];
  budget: string;
  isolated: string[];
  memory: { icon: string; name: string; desc: string; hits: { text: string; score?: string }[] }[];
};
export type GuardPolicy = { id: string; icon: string; name: string; desc: string; stat: string; enabled: boolean; forced?: boolean };
export type GuardEvent = { id: string; time: string; agent: string; run: string; rule: string; action: "blocked" | "masked"; detail: string };
export type GuardrailsView = {
  policies: GuardPolicy[];
  trend: number[][];
  events: GuardEvent[];
  counts: Record<"all" | "blocked" | "masked", number>;
  detail: { id: string; title: string; meta: string; source: string; snippet: string; masked: string; verdict: string };
};
