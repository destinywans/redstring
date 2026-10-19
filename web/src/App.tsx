import { matchesSearch } from "./lib/search";
import { useEffect, useState, useRef, lazy, Suspense } from "react";
import type { FormEvent } from "react";
import {
  NavLink,
  Navigate,
  Route,
  Routes,
  useLocation,
  useNavigate,
} from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  LayoutGrid,
  Search,
  BookOpen,
  Plug,
  Settings,
  ChevronDown,
  ChevronsUpDown,
  Bell,
  Sun,
  Moon,
  ArrowRight,
  Command,
  X,
  FlaskConical,
  Users,
  UsersRound,
  ScrollText,
  Server,
  ChartNoAxesCombined,
  Menu,
  ChevronRight,
} from "./lib/icons";
import { ApiError, post, request } from "./lib/api";
import type {
  Application,
  Model,
  User,
  Items,
  Investigation,
} from "./lib/types";
import { WorkspaceContext } from "./lib/workspace";
import { useRbacAccess, isRbacPath } from "./features/platform/rbac/access";
import { useWorkspaceSelection } from "./lib/selection";
import { useTheme } from "./lib/theme";
import {
  navigation,
  settingsNavigation,
  legacyRedirects,
  isNavigationCurrent,
} from "./lib/navigation";
import { Modal, ErrorNotice } from "./components/ui";
import AccountMenu from "./components/AccountMenu";
import Toaster from "./components/Toaster";
import { useToastStore } from "./lib/toast";
import SettingsWorkspace from "./features/platform/SettingsWorkspace";
import EnvironmentSelector from "./components/EnvironmentSelector";
import {
  EnvironmentScopeContext,
  useEnvironmentScopeState,
} from "./lib/environmentScope";
const ComponentSamplePage = lazy(
  () => import("./features/design-review/ComponentSamplePage"),
);
const DashboardPage = lazy(() => import("./features/workspace/DashboardPage"));
const ApplicationsPage = lazy(
  () => import("./features/workspace/ApplicationsPage"),
);
const ResourceCatalogPage = lazy(
  () => import("./features/resources/ResourceCatalogPage"),
);
const ResourceRelationsPage = lazy(
  () => import("./features/resources/ResourceRelationsPage"),
);
const IncidentsPage = lazy(() => import("./features/workspace/IncidentsPage"));
const Overview = lazy(() => import("./features/overview/OverviewPage"));
const InvestigationPage = lazy(
  () => import("./features/investigation/InvestigationPage"),
);
const KnowledgePage = lazy(() => import("./features/knowledge/KnowledgePage"));
const SettingsPage = lazy(() => import("./features/settings/SettingsPage"));

const ApprovalPage = lazy(() => import("./features/actions/ApprovalPage"));
const RecoveryPage = lazy(() => import("./features/actions/RecoveryPage"));
const ObservabilityPage = lazy(
  () => import("./features/observability/ObservabilityPage"),
);
const TraceObservabilityPage = lazy(
  () => import("./features/observability/TraceObservabilityPage"),
);
const InfrastructurePage = lazy(
  () => import("./features/infrastructure/InfrastructurePage"),
);
const InfrastructureReviewPage = lazy(
  () => import("./features/infrastructure/InfrastructureReviewPage"),
);
function LegacyRedirect({
  to,
}: {
  to: string | ((params: URLSearchParams) => string);
}) {
  const location = useLocation();
  const params = new URLSearchParams(location.search);
  const pathname = typeof to === "string" ? to : to(params);
  params.delete("tab");
  params.delete("mode");
  const search = params.toString();
  return (
    <Navigate
      replace
      to={{ pathname, search: search ? `?${search}` : "", hash: location.hash }}
    />
  );
}
const PlatformPage = lazy(() => import("./features/platform/PlatformPage"));
const ExtensionsPage = lazy(
  () => import("./features/aiextensions/ExtensionsPage"),
);
const MemoryPage = lazy(() => import("./features/memory/MemoryPage"));
const DeliveryPausedPage = lazy(
  () => import("./features/delivery/DeliveryPausedPage"),
);
const AlertsPage = lazy(() => import("./features/alerts/AlertsPage"));
const previewApp: Application = {
  id: "preview-checkout",
  name: "结算服务",
  description: "处理订单支付、金额结算和交易确认的核心服务",
  environment: "生产",
};
const previewModels: Model[] = [
  {
    id: "preview-openai",
    name: "OpenAI 兼容服务",
    provider: "openai",
    available: true,
  },
  { id: "preview-claude", name: "Claude", provider: "claude", available: true },
  { id: "preview-ollama", name: "Ollama", provider: "ollama", available: true },
];
export default function App() {
  const location = useLocation(),
    navigate = useNavigate(),
    client = useQueryClient();
  const pushToast = useToastStore((s) => s.push);
  const [preview] = useState(
    () => new URLSearchParams(window.location.search).get("preview") === "1",
  );
  const [investigationId, setInvestigationId] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get("investigation"),
  );
  const { theme, toggleTheme } = useTheme();
  const [menuOpen, setMenuOpen] = useState(false);
  const [narrow, setNarrow] = useState(
    () => matchMedia("(max-width: 760px)").matches,
  );
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const sidebarRef = useRef<HTMLElement>(null);
  const menuTrigger = useRef<HTMLButtonElement>(null);
  const mainRef = useRef<HTMLElement>(null);
  const currentSection = [...navigation, settingsNavigation].find((section) =>
    section.children.some((item) =>
      isNavigationCurrent(item.to, location.pathname, location.search),
    ),
  );
  const currentDestination = currentSection?.children.find((item) =>
    isNavigationCurrent(item.to, location.pathname, location.search),
  );
  const [helpOpen, setHelpOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false),
    [search, setSearch] = useState("");
  const auth = useQuery({
    queryKey: ["auth"],
    queryFn: () => request<{ user: User }>("/auth/me"),
    enabled: !preview,
  });
  const user = preview
    ? {
        id: "preview-user",
        email: "preview@example.invalid",
        name: "设计预览",
        role: "admin" as const,
      }
    : auth.data?.user;
  const rbacAccess = useRbacAccess(user, preview);
  const profile = useQuery({
    queryKey: ["profile", user?.id],
    queryFn: () => request<{ user: { avatar_url?: string } }>("/profile"),
    enabled: !!user && !preview,
  });
  const apps = useQuery({
    queryKey: ["applications", preview],
    queryFn: () => request<Items<Application>>("/applications"),
    enabled: !!user && !preview,
  });
  const models = useQuery({
    queryKey: ["models", preview],
    queryFn: () => request<Items<Model>>("/models"),
    enabled: !!user && !preview,
  });
  const investigations = useQuery({
    queryKey: ["investigations", preview],
    queryFn: () => request<Items<Investigation>>("/investigations"),
    enabled: !!user && !preview,
  });
  const scopeInvestigationId = location.pathname.startsWith("/investigations")
    ? location.pathname.match(/^\/investigations\/([^/]+)/)?.[1] ||
      investigationId
    : null;
  // Same key as the canvas: one authorized request supplies its immutable scope.
  const scopeInvestigation = useQuery({
    queryKey: ["investigation", scopeInvestigationId],
    queryFn: () =>
      request<Investigation>(`/investigations/${scopeInvestigationId}`),
    enabled: !!user && !preview && !!scopeInvestigationId,
  });
  const applications = preview ? [previewApp] : (apps.data?.items ?? []);
  const environmentScope = useEnvironmentScopeState(
    user?.id,
    applications,
    preview || apps.isSuccess,
  );
  const {
    application,
    environments,
    environment,
    setApplication: setApplicationId,
    setEnvironment: setEnvironmentId,
    setScope,
  } = useWorkspaceSelection(
    applications,
    !!user && (preview || apps.isSuccess),
    preview,
    scopeInvestigation.data
      ? {
          applicationId: scopeInvestigation.data.application_id,
          environmentId: scopeInvestigation.data.environment_id ?? "",
        }
      : undefined,
    environmentScope.key,
  );
  function notify(
    message: string,
    kind: "error" | "success" | "info" = "info",
  ) {
    pushToast(message, kind);
  }
  useEffect(() => {
    const query = matchMedia("(max-width: 760px)");
    const sync = () => {
      setNarrow(query.matches);
      if (!query.matches) setMenuOpen(false);
    };
    query.addEventListener("change", sync);
    return () => query.removeEventListener("change", sync);
  }, []);
  useEffect(() => {
    // The canvas keeps its section collapsed; other investigation pages expand it.
    if (
      currentSection &&
      !currentSection.direct &&
      !location.pathname.startsWith("/investigations")
    )
      setExpanded((value) => ({ ...value, [currentSection.id]: true }));
  }, [currentSection?.id, location.pathname]);
  useEffect(() => {
    setMenuOpen(false);
    document.title = `${currentDestination?.label ?? "工作区"} · AIOps Canvas`;
  }, [location.pathname, location.search, currentDestination?.label]);
  useEffect(() => {
    if (!narrow || !menuOpen) return;
    const sidebar = sidebarRef.current;
    const focusable = () =>
      Array.from(
        sidebar?.querySelectorAll<HTMLElement>(
          "button:not([disabled]),a[href],select:not([disabled])",
        ) ?? [],
      ).filter((element) => element.getClientRects().length > 0);
    focusable()[0]?.focus();
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setMenuOpen(false);
        requestAnimationFrame(() => menuTrigger.current?.focus());
      }
      if (event.key !== "Tab") return;
      const items = focusable(),
        first = items[0],
        last = items[items.length - 1];
      if (
        event.shiftKey &&
        (document.activeElement === first ||
          !sidebar?.contains(document.activeElement))
      ) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, [narrow, menuOpen]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        setMenuOpen(false);
        setSearchOpen((v) => !v);
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
  useEffect(() => {
    if (!user || location.pathname !== "/") return;
    const params = new URLSearchParams(location.search);
    const inv = params.get("investigation");
    if (inv) {
      navigate(
        {
          pathname: `/investigations/${encodeURIComponent(inv)}`,
          search: location.search,
        },
        { replace: true },
      );
      return;
    }
    if (params.get("incident"))
      navigate(
        { pathname: "/incidents", search: location.search },
        { replace: true },
      );
  }, [user?.id, location.pathname, location.search, navigate]);
  async function logout() {
    try {
      await post("/auth/logout");
      client.clear();
      window.location.assign("/");
    } catch (e) {
      notify(String(e), "error");
    }
  }
  if (!preview && auth.isPending)
    return (
      <div className="boot-screen">
        <Activity size={30} />
        <p>正在打开工作区…</p>
      </div>
    );
  if (!user)
    return (
      <Login
        error={
          auth.error instanceof ApiError && auth.error.status === 401
            ? null
            : auth.error
        }
        onLogin={() => client.invalidateQueries({ queryKey: ["auth"] })}
      />
    );
  const sections = navigation
    .filter((section) => !section.admin || user.role === "admin")
    .map((section) => ({
      ...section,
      children: section.children.filter(
        (item) => !item.admin || user.role === "admin",
      ),
    }));
  const nav = [
    ...sections,
    {
      ...settingsNavigation,
      children: settingsNavigation.children.filter(
        (item) =>
          !item.admin ||
          user.role === "admin" ||
          (isRbacPath(item.to) && rbacAccess.canManage("rbac")),
      ),
    },
  ].flatMap((section) =>
    section.children.map((item) => ({ ...item, icon: section.icon })),
  );
  const routeLink = (to: string) => {
    const scoped = environmentScope.withScope(to);
    if (!preview) return scoped;
    const url = new URL(scoped, window.location.origin);
    url.searchParams.set("preview", "1");
    return `${url.pathname}${url.search}`;
  };
  const closeMenu = () => {
    setMenuOpen(false);
    requestAnimationFrame(() => menuTrigger.current?.focus());
  };
  return (
    <WorkspaceContext.Provider
      value={{
        preview,
        user,
        applications,
        application,
        setApplication: setApplicationId,
        environment,
        setEnvironment: setEnvironmentId,
        setScope,
        models: preview ? previewModels : (models.data?.items ?? []),
        investigationId,
        setInvestigationId,
        notify,
      }}
    >
      <EnvironmentScopeContext.Provider value={environmentScope}>
        {currentSection?.id === settingsNavigation.id ? (
          <SettingsWorkspace
            avatarURL={profile.data?.user.avatar_url}
            homeURL={routeLink("/")}
            routeLink={routeLink}
            theme={theme}
            onToggleTheme={toggleTheme}
            onHelp={() => setHelpOpen(true)}
          />
        ) : (
          <div className="app-shell">
            <a className="skip-link" href="#main">
              跳到主要内容
            </a>
            {narrow && menuOpen && (
              <button
                className="sidebar-overlay"
                aria-label="关闭导航"
                tabIndex={-1}
                onClick={closeMenu}
              />
            )}
            <aside
              ref={sidebarRef}
              id="workspace-navigation"
              className={`workspace-sidebar ${menuOpen ? "open" : ""}`}
              inert={narrow && !menuOpen}
              role={narrow && menuOpen ? "dialog" : undefined}
              aria-modal={narrow && menuOpen ? true : undefined}
              aria-label="工作区导航"
            >
              <div className="sidebar-brand-row">
                <NavLink
                  to={routeLink("/")}
                  className="brand"
                  aria-label="AIOps 首页"
                >
                  <span className="brand-symbol">
                    <Activity size={29} />
                  </span>
                  <span>AIOps</span>
                </NavLink>
                <button
                  className="icon-button mobile-nav-close"
                  aria-label="关闭导航"
                  onClick={closeMenu}
                >
                  <X size={20} />
                </button>
              </div>
              <div className="sidebar-scroll">
                <nav className="sidebar-nav" aria-label="主要导航">
                  {sections.map((section) =>
                    section.direct ? (
                      <NavLink
                        key={section.id}
                        to={routeLink(section.children[0].to)}
                        end
                        className={`nav-parent nav-direct ${currentSection?.id === section.id ? "is-current" : ""}`}
                        onClick={() => {
                          if (narrow) {
                            setMenuOpen(false);
                            requestAnimationFrame(() =>
                              mainRef.current?.focus(),
                            );
                          }
                        }}
                      >
                        <section.icon size={18} />
                        <span>{section.label}</span>
                      </NavLink>
                    ) : (
                      <div
                        key={section.id}
                        className={`nav-section ${currentSection?.id === section.id ? "is-current" : ""}`}
                      >
                        <button
                          className="nav-parent"
                          aria-expanded={!!expanded[section.id]}
                          aria-controls={`nav-${section.id}`}
                          onClick={() =>
                            setExpanded((value) => ({
                              ...value,
                              [section.id]: !value[section.id],
                            }))
                          }
                        >
                          <section.icon size={18} />
                          <span>{section.label}</span>
                          <ChevronRight size={12} className="nav-chevron" />
                        </button>
                        <div
                          className="nav-children"
                          id={`nav-${section.id}`}
                          hidden={!expanded[section.id]}
                        >
                          {section.children.map((item) => {
                            const active = isNavigationCurrent(
                              item.to,
                              location.pathname,
                              location.search,
                            );
                            return (
                              <NavLink
                                key={item.to}
                                to={routeLink(item.to)}
                                className={() =>
                                  `nav-child ${active ? "active" : ""}`
                                }
                                aria-current={active ? "page" : false}
                                onClick={() => {
                                  if (narrow) {
                                    setMenuOpen(false);
                                    requestAnimationFrame(() =>
                                      mainRef.current?.focus(),
                                    );
                                  }
                                }}
                              >
                                {item.label}
                              </NavLink>
                            );
                          })}
                        </div>
                      </div>
                    ),
                  )}
                </nav>
              </div>
              <AccountMenu
                avatarURL={profile.data?.user.avatar_url}
                user={user}
                preview={preview}
                settingsURL={routeLink("/profile")}
                onHelp={() => setHelpOpen(true)}
                onNavigate={() => {
                  if (narrow) {
                    setMenuOpen(false);
                    requestAnimationFrame(() => mainRef.current?.focus());
                  }
                }}
                onLogout={() =>
                  preview ? window.location.assign("/") : void logout()
                }
              />
            </aside>
            <header className="topbar" inert={narrow && menuOpen}>
              <button
                ref={menuTrigger}
                className="icon-button mobile-nav-trigger"
                aria-label="打开导航"
                aria-expanded={menuOpen}
                aria-controls="workspace-navigation"
                onClick={() => setMenuOpen(true)}
              >
                <Menu size={21} />
              </button>
              <div className="workspace-breadcrumb">
                <span>{currentSection?.label ?? "工作区"}</span>
                {currentDestination &&
                  currentDestination.label !== currentSection?.label && (
                    <>
                      <ChevronRight size={11} />
                      <strong>{currentDestination.label}</strong>
                    </>
                  )}
              </div>
              <EnvironmentSelector />
              <button
                className="global-search"
                aria-label="搜索工作区"
                title="搜索工作区 · ⌘ K / Ctrl K"
                onClick={(event) => {
                  event.currentTarget.focus();
                  setSearchOpen(true);
                }}
              >
                <Search size={17} />
                <span>搜索</span>
                <kbd>⌘ K</kbd>
              </button>
              {preview && (
                <span
                  className="preview-label"
                  title="示例数据，仅用于查看设计，不连接任何系统"
                >
                  <FlaskConical size={12} />
                  设计预览
                </span>
              )}
              <button
                className="icon-button theme-toggle"
                aria-label={
                  theme === "dark" ? "切换到浅色主题" : "切换到深色主题"
                }
                title={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
                onClick={toggleTheme}
              >
                <Sun className="sun" size={19} />
                <Moon className="moon" size={19} />
              </button>
            </header>
            <main
              id="main"
              ref={mainRef}
              tabIndex={-1}
              className="workspace-main"
              inert={narrow && menuOpen}
            >
              {apps.error && <ErrorNotice error={apps.error} />}
              {models.error && <ErrorNotice error={models.error} />}
              <Suspense
                fallback={<div className="page-loading">正在加载工作区…</div>}
              >
                <Routes>
                  <Route path="/" element={<DashboardPage />} />
                  <Route path="/applications" element={<ApplicationsPage />} />
                  <Route
                    path="/design-review/components"
                    element={<ComponentSamplePage />}
                  />
                  <Route path="/applications/detail" element={<Overview />} />
                  <Route
                    path="/resources/catalog"
                    element={<ResourceCatalogPage />}
                  />
                  <Route
                    path="/resources/relations"
                    element={<ResourceRelationsPage />}
                  />
                  <Route path="/incidents" element={<IncidentsPage />} />
                  <Route
                    path="/alerts"
                    element={<AlertsPage mode="alerts" />}
                  />
                  <Route
                    path="/alerts/sources"
                    element={<AlertsPage mode="sources" />}
                  />
                  <Route
                    path="/infrastructure"
                    element={<InfrastructurePage />}
                  />
                  <Route
                    path="/design-review/infrastructure"
                    element={<InfrastructureReviewPage />}
                  />
                  <Route
                    path="/observability/metrics"
                    element={<ObservabilityPage key="metrics" mode="metrics" />}
                  />
                  <Route
                    path="/observability/logs"
                    element={<ObservabilityPage key="logs" mode="logs" />}
                  />
                  <Route
                    path="/observability/traces"
                    element={<TraceObservabilityPage mode="traces" />}
                  />
                  <Route
                    path="/observability/topology"
                    element={<TraceObservabilityPage mode="topology" />}
                  />
                  <Route
                    path="/investigations/*"
                    element={<InvestigationPage />}
                  />
                  <Route
                    path="/investigations/:id"
                    element={<InvestigationPage />}
                  />
                  <Route path="/ai-extensions/*" element={<ExtensionsPage />} />
                  <Route path="/memory" element={<MemoryPage />} />
                  <Route path="/delivery/*" element={<DeliveryPausedPage />} />
                  <Route path="/knowledge" element={<KnowledgePage />} />
                  <Route
                    path="/integrations/mcp"
                    element={<ExtensionsPage />}
                  />
                  <Route
                    path="/integrations/:tab"
                    element={<SettingsPage />}
                  />
                  <Route
                    path="/automation"
                    element={<PlatformPage tab="automation" />}
                  />
                  {legacyRedirects.map((redirect) => (
                    <Route
                      key={redirect.from}
                      path={redirect.from}
                      element={<LegacyRedirect to={redirect.to} />}
                    />
                  ))}
                  <Route path="/actions" element={<ApprovalPage />} />
                  <Route path="/recovery" element={<RecoveryPage />} />
                  <Route
                    path="*"
                    element={
                      <div className="empty-state">
                        <h2>没有找到这个页面</h2>
                        <NavLink to="/">返回工作区</NavLink>
                      </div>
                    }
                  />
                </Routes>
              </Suspense>
            </main>
          </div>
        )}
        <Modal
          open={helpOpen}
          onOpenChange={setHelpOpen}
          title="帮助与支持"
          description="开始使用 AIOps，了解常用功能与当前版本。"
        >
          <div className="settings-help-links">
            <NavLink to={routeLink("/")} onClick={() => setHelpOpen(false)}>
              <Activity size={20} />
              <span>
                <strong>从工作台开始</strong>
                <small>描述问题，或继续最近的会话。</small>
              </span>
              <ArrowRight size={16} />
            </NavLink>
            <NavLink
              to={routeLink("/knowledge")}
              onClick={() => setHelpOpen(false)}
            >
              <BookOpen size={20} />
              <span>
                <strong>查阅 Runbook</strong>
                <small>查找已有操作步骤与处理经验。</small>
              </span>
              <ArrowRight size={16} />
            </NavLink>
            <a
              href="https://github.com/destinywans/aiops-canvas"
              target="_blank"
              rel="noreferrer"
            >
              <Plug size={20} />
              <span>
                <strong>项目文档与反馈</strong>
                <small>查看使用文档，或到项目仓库报告问题。</small>
              </span>
              <ArrowRight size={16} />
            </a>
          </div>
        </Modal>
        <Toaster />
        <Modal
          open={searchOpen}
          onOpenChange={setSearchOpen}
          title="搜索工作区"
        >
          <label className="search-input">
            <Search size={18} />
            <input
              placeholder="输入页面或会话名称…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
          <div className="command-list">
            {[
              ...nav,
              ...(investigations.data?.items ?? []).map((x) => ({
                to: `/investigations/${x.id}`,
                icon: Search,
                label: x.title,
              })),
            ]
              .filter((x) => matchesSearch(x.label, search))
              .map((x) => (
                <button
                  key={x.to}
                  onClick={() => {
                    navigate(routeLink(x.to));
                    setSearchOpen(false);
                    setSearch("");
                  }}
                >
                  <x.icon size={18} />
                  {x.label}
                  <ArrowRight size={15} />
                </button>
              ))}
          </div>
        </Modal>
      </EnvironmentScopeContext.Provider>
    </WorkspaceContext.Provider>
  );
}
function Login({ error, onLogin }: { error: unknown; onLogin: () => void }) {
  const [email, setEmail] = useState(""),
    [password, setPassword] = useState(""),
    [localError, setLocalError] = useState<unknown>(null),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setLocalError(null);
    try {
      await post("/auth/login", { email, password });
      setPassword("");
      onLogin();
    } catch (e) {
      setLocalError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="login-page">
      <div className="login-intro">
        <div className="brand">
          <span className="brand-symbol">
            <Activity />
          </span>
          AIOps Canvas
        </div>
        <h1>
          让每一次调查，
          <br />
          都有迹可循。
        </h1>
        <p>
          将线索、推理与行动放在同一张画布。
          <br />
          从发现异常，到确认恢复。
        </p>
        <div className="login-graph">
          <div className="card">
            <Activity size={22} />
            <strong>发现异常</strong>
            <small>从真实信号开始</small>
          </div>
          <span>→</span>
          <div className="card accent">
            <Search size={22} />
            <strong>展开调查</strong>
            <small>与你的 AI 一起取证</small>
          </div>
          <span>→</span>
          <div className="card">
            <BookOpen size={22} />
            <strong>积累经验</strong>
            <small>让知识持续生长</small>
          </div>
        </div>
      </div>
      <form className="login-form card" onSubmit={submit}>
        <span className="eyebrow">YOUR OPERATIONS WORKSPACE</span>
        <h2>登录工作区</h2>
        <p className="muted">连接你的应用，继续上一次调查。</p>
        <label>
          邮箱
          <input
            className="field"
            type="email"
            autoComplete="username"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@company.com"
          />
        </label>
        <label>
          密码
          <input
            className="field"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {!!(localError || error) && <ErrorNotice error={localError || error} />}
        <button className="btn btn-primary" disabled={busy}>
          {busy ? "正在登录…" : "进入工作区"}
          <ArrowRight size={16} />
        </button>
        <a className="preview-link" href="/?preview=1">
          <FlaskConical size={16} />
          查看设计预览<span>示例数据</span>
        </a>
        <p className="login-help">
          初始管理员由部署配置创建。
          <br />
          请联系工作区管理员获取账号。
        </p>
      </form>
    </div>
  );
}
