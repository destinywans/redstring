import { lazy, Suspense, type ComponentType } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
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
const NewConversation = p(() => import("../features/conversation/ConversationPage"), "NewConversationPage");

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
  ["services/:id", p(() => import("../features/observe/ServiceDetailPage"))],
  ["services/:id/resilience", p(() => import("../features/observe/ServiceDetailPage"), "ResiliencePage")],
  ["resources", p(() => import("../features/observe/ResourcesPage"))],
  ["metrics", p(() => import("../features/observe/MetricsPage"))],
  ["traces", p(() => import("../features/observe/TracesPage"))],
  ["logs", p(() => import("../features/observe/LogsPage"))],
  ["changes", p(() => import("../features/observe/ChangesPage"))],
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
          <Route path="canvas/:id" element={<Canvas />} />
          <Route path="templates" element={<Templates />} />
          <Route path="inbox" element={<Inbox />} />
          <Route path="incidents" element={<Incidents />} />
          <Route path="incidents/:id" element={<Incidents />} />
          <Route path="alerts" element={<Alerts />} />
          <Route path="alert-rules" element={<Rules />} />
          <Route path="routing" element={<Routing />} />
          <Route path="silences" element={<Silences />} />
          <Route path="review/:id" element={<Review />} />
          <Route path="executions/:id" element={<Execution />} />
          <Route path="runbooks" element={<Runbooks />} />
          <Route path="runbooks/:id" element={<RunbookEditor />} />
          <Route path="postmortems" element={<Postmortems />} />
          <Route path="memory" element={<Memory />} />
          <Route path="knowledge-sources" element={<Sources />} />
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
