import { useEffect, useState, useRef, lazy, Suspense } from "react";
import type { FormEvent } from "react";
import {
  NavLink,
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
import { useWorkspaceSelection } from "./lib/selection";
import { useTheme } from "./lib/theme";
import {
  navigation,
  settingsNavigation,
  isNavigationCurrent,
} from "./lib/navigation";
import { Modal, ErrorNotice } from "./components/ui";
import AccountMenu from "./components/AccountMenu";
import EnvironmentSelector from "./components/EnvironmentSelector";
import {
  EnvironmentScopeContext,
  useEnvironmentScopeState,
} from "./lib/environmentScope";
const DashboardPage = lazy(() => import("./features/workspace/DashboardPage"));
const ApplicationsPage = lazy(
  () => import("./features/workspace/ApplicationsPage"),
);
const IncidentsPage = lazy(() => import("./features/workspace/IncidentsPage"));
const Overview = lazy(() => import("./features/overview/OverviewPage"));
const InvestigationPage = lazy(
  () => import("./features/investigation/InvestigationPage"),
);
const KnowledgePage = lazy(() => import("./features/knowledge/KnowledgePage"));
const SettingsPage = lazy(() => import("./features/settings/SettingsPage"));
const MembersPage = lazy(() => import("./features/members/MembersPage"));
const GovernancePage = lazy(() => import("./features/members/GovernancePage"));
const ApprovalPage = lazy(() => import("./features/actions/ApprovalPage"));
const RecoveryPage = lazy(() => import("./features/actions/RecoveryPage"));
const ObservabilityPage = lazy(
  () => import("./features/observability/ObservabilityPage"),
);
const UnavailableObservationPage = lazy(
  () => import("./features/observability/UnavailableObservationPage"),
);
const InfrastructurePage = lazy(
  () => import("./features/infrastructure/InfrastructurePage"),
);
const PlatformPage = lazy(() => import("./features/platform/PlatformPage"));
const ExtensionsPage = lazy(
  () => import("./features/aiextensions/ExtensionsPage"),
);
const MemoryPage = lazy(() => import("./features/memory/MemoryPage"));
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
  const [preview] = useState(
    () => new URLSearchParams(window.location.search).get("preview") === "1",
  );
  const [investigationId, setInvestigationId] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get("investigation"),
  );
  const [toast, setToast] = useState<{ message: string; kind: string } | null>(
    null,
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
  );
  function notify(
    message: string,
    kind: "error" | "success" | "info" = "info",
  ) {
    setToast({ message, kind });
  }
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(null), 6500);
    return () => clearTimeout(timer);
  }, [toast]);
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
    if (
      currentSection &&
      currentSection.id !== "agents" &&
      !currentSection.direct
    )
      setExpanded((value) => ({ ...value, [currentSection.id]: true }));
  }, [currentSection?.id]);
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
    ...(user.role === "admin" ? [settingsNavigation] : []),
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
            <EnvironmentSelector />
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
                          requestAnimationFrame(() => mainRef.current?.focus());
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
                              className={`nav-child ${active ? "active" : ""}`}
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
              user={user}
              preview={preview}
              settingsURL={routeLink("/platform/settings")}
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
            {currentSection?.id === settingsNavigation.id &&
              user.role === "admin" && (
                <nav className="settings-section-nav" aria-label="设置导航">
                  {settingsNavigation.children.map((item) => (
                    <NavLink
                      key={item.to}
                      to={routeLink(item.to)}
                      end
                      className={({ isActive }) => (isActive ? "active" : "")}
                    >
                      {item.label}
                    </NavLink>
                  ))}
                </nav>
              )}
            {apps.error && <ErrorNotice error={apps.error} />}
            {models.error && <ErrorNotice error={models.error} />}
            <Suspense
              fallback={<div className="page-loading">正在加载工作区…</div>}
            >
              <Routes>
                <Route path="/" element={<DashboardPage />} />
                <Route path="/applications" element={<ApplicationsPage />} />
                <Route path="/applications/detail" element={<Overview />} />
                <Route path="/incidents" element={<IncidentsPage />} />
                <Route path="/alerts" element={<AlertsPage mode="alerts" />} />
                <Route
                  path="/alerts/sources"
                  element={<AlertsPage mode="sources" />}
                />
                <Route
                  path="/infrastructure"
                  element={<InfrastructurePage />}
                />
                <Route
                  path="/observability/metrics"
                  element={<ObservabilityPage mode="metrics" />}
                />
                <Route
                  path="/observability/logs"
                  element={<ObservabilityPage mode="logs" />}
                />
                <Route
                  path="/observability/traces"
                  element={<UnavailableObservationPage kind="traces" />}
                />
                <Route
                  path="/observability/topology"
                  element={<UnavailableObservationPage kind="topology" />}
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
                <Route path="/knowledge" element={<KnowledgePage />} />
                <Route path="/integrations" element={<SettingsPage />} />
                <Route path="/members" element={<MembersPage />} />
                <Route path="/teams" element={<GovernancePage tab="teams" />} />
                <Route path="/audit" element={<GovernancePage tab="audit" />} />
                <Route path="/platform/settings" element={<PlatformPage />} />
                <Route
                  path="/platform/roles"
                  element={<PlatformPage tab="roles" />}
                />
                <Route
                  path="/settings"
                  element={<PlatformPage tab="automation" />}
                />
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
        {toast && (
          <div
            className={`toast ${toast.kind}`}
            role={toast.kind === "error" ? "alert" : "status"}
          >
            {toast.message}
            <button
              className="icon-button"
              aria-label="关闭通知"
              onClick={() => setToast(null)}
            >
              <X size={16} />
            </button>
          </div>
        )}
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
              .filter((x) =>
                x.label.toLowerCase().includes(search.toLowerCase()),
              )
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
