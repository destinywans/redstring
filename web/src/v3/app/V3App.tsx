import { lazy, Suspense, type ComponentType } from "react";
import { Navigate, Route, Routes, useParams } from "react-router-dom";
import { Shell, Page } from "./Shell";
import { useApplyPreferences } from "./theme";
import { Skeleton } from "../ui/core";
import "../design/tokens.css";
import "../design/base.css";
import "../ui/ui.css";

const p = (load: () => Promise<Record<string, unknown>>, name = "default") =>
  lazy(async () => ({ default: (await load())[name] as ComponentType }));

const Home = p(() => import("../features/home/HomePage"));
const Preferences = p(() => import("../features/settings/PreferencesPage"));
const Pending = p(() => import("../features/Pending"));
const Conversations = p(() => import("../features/conversation/ConversationsPage"));
const Conversation = p(() => import("../features/conversation/ConversationPage"));
const Templates = p(() => import("../features/conversation/TemplatesPage"));
const Inbox = p(() => import("../features/events/InboxPage"));
const Incidents = p(() => import("../features/events/IncidentsPage"));
const Alerts = p(() => import("../features/events/AlertsPage"));
const Rules = p(() => import("../features/events/RulesPage"));
const Routing = p(() => import("../features/events/RoutingPage"));
const Silences = p(() => import("../features/events/SilencesPage"));
const Review = p(() => import("../features/events/ReviewPage"));
const Execution = p(() => import("../features/events/ExecutionPage"));
const Runbooks = p(() => import("../features/knowledge/RunbooksPage"));
const RunbookEditor = p(() => import("../features/knowledge/RunbookEditorPage"));
const Postmortems = p(() => import("../features/knowledge/PostmortemsPage"));
const Memory = p(() => import("../features/knowledge/MemoryPage"));
const Sources = p(() => import("../features/knowledge/SourcesPage"));
const Canvas = p(() => import("../features/canvas/CanvasPage"));
const NewRule = p(() => import("../features/events/NewRulePage"));
const NewRunbook = p(() => import("../features/knowledge/NewRunbookPage"));
const NewPostmortem = p(() => import("../features/knowledge/NewPostmortemPage"));
const NewSource = p(() => import("../features/knowledge/NewSourcePage"));
const DocCanvas = p(() => import("../features/canvas/DocCanvasPage"));
const Canvases = p(() => import("../features/canvas/CanvasesPage"));
const NewConversation = p(() => import("../features/conversation/ConversationPage"), "NewConversationPage");

/** Investigation canvases keep the evidence/write-back view; other kinds (plan, review, …) use the generic canvas. */
function CanvasRoute() {
  const { id = "" } = useParams();
  return id.startsWith("INV-") ? <Canvas /> : <DocCanvas />;
}

function Loading() {
  return (
    <main className="v3-page">
      <header className="v3-topbar">
        <Skeleton w={180} h={12} />
      </header>
      <div className="v3-content">
        <Skeleton h={90} />
        <Skeleton h={220} />
        <Skeleton h={160} />
      </div>
    </main>
  );
}

export const ROUTES: [string, ComponentType][] = [
  ["services", p(() => import("../features/observe/ServicesPage"))],
  ["services/new", p(() => import("../features/observe/NewServicePage"))],
  ["services/:id", p(() => import("../features/observe/ServiceDetailPage"))],
  ["services/:id/resilience", p(() => import("../features/observe/ServiceDetailPage"), "ResiliencePage")],
  ["resources", p(() => import("../features/observe/ResourcesPage"))],
  ["metrics", p(() => import("../features/observe/MetricsPage"))],
  ["traces", p(() => import("../features/observe/TracesPage"))],
  ["traces/:id", p(() => import("../features/observe/TraceDetailPage"))],
  ["logs", p(() => import("../features/observe/LogsPage"))],
  ["changes", p(() => import("../features/observe/ChangesPage"))],
  ["integrations", p(() => import("../features/integrations/ConnectorsPage"))],
  ["integrations/new", p(() => import("../features/integrations/AddConnectorPage"))],
  ["integrations/sync", p(() => import("../features/integrations/ConfigSyncPage"))],
  ["integrations/models", p(() => import("../features/integrations/ModelsPage"))],
  ["integrations/mcp", p(() => import("../features/integrations/McpPage"))],
  ["integrations/ingest", p(() => import("../features/integrations/IngestPage"))],
  ["integrations/ingest/push", p(() => import("../features/integrations/IngestPage"))],
  ["integrations/ingest/stack", p(() => import("../features/integrations/IngestPage"))],
  ["integrations/mapping", p(() => import("../features/integrations/MappingPage"))],
  ["integrations/coverage", p(() => import("../features/integrations/CoveragePage"))],
  ["integrations/webhooks", p(() => import("../features/integrations/WebhooksPage"))],
  ["agents/lifecycle", p(() => import("../features/agents/LifecyclePage"))],
  ["agents/prompts", p(() => import("../features/agents/PromptsPage"))],
  ["agents/tools", p(() => import("../features/agents/ToolsPage"))],
  ["agents/evals", p(() => import("../features/agents/EvalsPage"))],
  ["agents/evals/:id", p(() => import("../features/agents/EvalCasePage"))],
  ["agents/runs", p(() => import("../features/agents/RunsPage"))],
  ["agents/runs/:id", p(() => import("../features/agents/RunDetailPage"))],
  ["agents/guardrails", p(() => import("../features/agents/GuardrailsPage"))],
  ["agents/:id", p(() => import("../features/agents/AgentPage"))],
  ["settings/profile", p(() => import("../features/settings/ProfilePage"))],
  ["settings/notifications", p(() => import("../features/settings/ProfilePage"), "NotificationsPage")],
  ["settings/account-security", p(() => import("../features/settings/ProfilePage"), "AccountSecurityPage")],
  ["settings/ai", p(() => import("../features/settings/AiPrefsPage"))],
  ["settings/workspace", p(() => import("../features/settings/WorkspacePage"))],
  ["settings/members", p(() => import("../features/settings/MembersPage"))],
  ["settings/roles", p(() => import("../features/settings/RolesPage"))],
  ["settings/security", p(() => import("../features/settings/SecurityPage"))],
  ["settings/audit", p(() => import("../features/settings/AuditPage"))],
];

export default function V3App() {
  useApplyPreferences();
  return (
    <Suspense fallback={<Loading />}>
      <Routes>
        <Route element={<Shell />}>
          <Route index element={<Home />} />
          <Route path="settings" element={<Navigate to="/settings/profile" replace />} />
          <Route path="settings/preferences" element={<Preferences />} />
          <Route path="conversations" element={<Conversations />} />
          <Route path="conversations/new" element={<NewConversation />} />
          <Route path="conversations/:id" element={<Conversation />} />
          <Route path="canvases" element={<Canvases />} />
          <Route path="canvas/:id" element={<CanvasRoute />} />
          <Route path="templates" element={<Templates />} />
          <Route path="inbox" element={<Inbox />} />
          <Route path="incidents" element={<Incidents />} />
          <Route path="incidents/:id" element={<Incidents />} />
          <Route path="alerts" element={<Alerts />} />
          <Route path="alert-rules" element={<Rules />} />
          <Route path="alert-rules/new" element={<NewRule />} />
          <Route path="routing" element={<Routing />} />
          <Route path="silences" element={<Silences />} />
          <Route path="review/:id" element={<Review />} />
          <Route path="executions/:id" element={<Execution />} />
          <Route path="runbooks" element={<Runbooks />} />
          <Route path="runbooks/new" element={<NewRunbook />} />
          <Route path="runbooks/:id" element={<RunbookEditor />} />
          <Route path="postmortems" element={<Postmortems />} />
          <Route path="postmortems/new" element={<NewPostmortem />} />
          <Route path="memory" element={<Memory />} />
          <Route path="knowledge-sources" element={<Sources />} />
          <Route path="knowledge-sources/new" element={<NewSource />} />
          {ROUTES.map(([path, C]) => (
            <Route key={path} path={path} element={<C />} />
          ))}
          <Route path="*" element={<Pending />} />
        </Route>
      </Routes>
    </Suspense>
  );
}

export { Page };
