import { useEffect, useState, lazy, Suspense } from "react";
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
  LogOut,
  ArrowRight,
  Command,
  X,
  FlaskConical,
} from "lucide-react";
import { ApiError, post, request } from "./lib/api";
import type {
  Application,
  Model,
  User,
  Items,
  Investigation,
} from "./lib/types";
import { WorkspaceContext } from "./lib/workspace";
import { Modal, ErrorNotice } from "./components/ui";
const Overview = lazy(() => import("./features/overview/OverviewPage"));
const InvestigationPage = lazy(
  () => import("./features/investigation/InvestigationPage"),
);
const KnowledgePage = lazy(() => import("./features/knowledge/KnowledgePage"));
const SettingsPage = lazy(() => import("./features/settings/SettingsPage"));
const ApprovalPage = lazy(() => import("./features/actions/ApprovalPage"));
const RecoveryPage = lazy(() => import("./features/actions/RecoveryPage"));
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
  const [applicationId, setApplicationId] = useState("");
  const [investigationId, setInvestigationId] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get("investigation"),
  );
  const [toast, setToast] = useState<{ message: string; kind: string } | null>(
    null,
  );
  const [theme, setTheme] = useState(
    () => localStorage.getItem("aiops-theme") ?? "system",
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
  const applications = preview ? [previewApp] : (apps.data?.items ?? []);
  const application =
    applications.find((x) => x.id === applicationId) ?? applications[0] ?? null;
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
    const query = matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      document.documentElement.dataset.theme =
        theme === "system" ? (query.matches ? "dark" : "light") : theme;
    };
    apply();
    query.addEventListener("change", apply);
    localStorage.setItem("aiops-theme", theme);
    return () => query.removeEventListener("change", apply);
  }, [theme]);
  useEffect(() => {
    if (preview && !new URLSearchParams(location.search).has("preview")) {
      const q = new URLSearchParams(location.search);
      q.set("preview", "1");
      navigate({ ...location, search: q.toString() }, { replace: true });
    }
  }, [location, navigate, preview]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        setSearchOpen((v) => !v);
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
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
  const nav = [
    { to: "/", icon: LayoutGrid, label: "应用" },
    { to: "/investigations", icon: Search, label: "调查" },
    { to: "/knowledge", icon: BookOpen, label: "知识" },
    { to: "/integrations", icon: Plug, label: "集成" },
  ];
  return (
    <WorkspaceContext.Provider
      value={{
        preview,
        user,
        applications,
        application,
        setApplication: setApplicationId,
        models: preview ? previewModels : (models.data?.items ?? []),
        investigationId,
        setInvestigationId,
        notify,
      }}
    >
      <div className="app-shell">
        <a className="skip-link" href="#main">
          跳到主要内容
        </a>
        <header className="topbar">
          <NavLink to="/" className="brand" aria-label="AIOps 首页">
            <span className="brand-symbol">
              <Activity size={22} />
            </span>
            AIOps
          </NavLink>
          <span className="header-divider" />
          <div className="workspace-label">
            平台工程 <ChevronDown size={14} />
          </div>
          <span className="header-divider" />
          <label className="app-switch">
            <span className="muted">应用</span>
            <select
              aria-label="选择应用"
              value={application?.id ?? ""}
              onChange={(e) => {
                setApplicationId(e.target.value);
                setInvestigationId(null);
                navigate("/");
              }}
            >
              {applications.map((x) => (
                <option key={x.id} value={x.id}>
                  {preview ? "checkout-api" : x.name}
                </option>
              ))}
            </select>
          </label>
          <span className="environment">
            <i />
            {application?.environment ?? "未选择"}
            <ChevronDown size={13} />
          </span>
          <button
            className="global-search"
            onClick={(event) => {
              event.currentTarget.focus();
              setSearchOpen(true);
            }}
          >
            <Search size={16} />
            <span>搜索调查、证据、Runbook…</span>
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
            aria-label="切换深浅主题"
            title="切换深浅主题"
            onClick={() =>
              setTheme(
                document.documentElement.dataset.theme === "dark"
                  ? "light"
                  : "dark",
              )
            }
          >
            {theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}
          </button>
          <button
            className="avatar"
            title={preview ? "返回登录" : `${user.name} · 退出登录`}
            aria-label={preview ? "返回登录" : "退出登录"}
            onClick={() =>
              preview ? window.location.assign("/") : void logout()
            }
          >
            {user.name.slice(0, 1).toUpperCase()}
          </button>
        </header>
        <aside className="nav-rail" aria-label="主导航">
          {nav.map(({ to, icon: Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              end={to === "/"}
              className={({ isActive }) =>
                `nav-item ${isActive ? "active" : ""}`
              }
            >
              <Icon size={21} />
              <span>{label}</span>
            </NavLink>
          ))}
          <NavLink to="/settings" className="nav-item nav-settings">
            <Settings size={21} />
            <span>设置</span>
          </NavLink>
        </aside>
        <main id="main" className="workspace-main">
          {apps.error && <ErrorNotice error={apps.error} />}
          {models.error && <ErrorNotice error={models.error} />}
          <Suspense
            fallback={<div className="page-loading">正在加载工作区…</div>}
          >
            <Routes>
              <Route path="/" element={<Overview />} />
              <Route path="/investigations/*" element={<InvestigationPage />} />
              <Route
                path="/investigations/:id"
                element={<InvestigationPage />}
              />
              <Route path="/knowledge" element={<KnowledgePage />} />
              <Route path="/integrations" element={<SettingsPage />} />
              <Route path="/settings" element={<SettingsPage />} />
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
      <Modal open={searchOpen} onOpenChange={setSearchOpen} title="搜索工作区">
        <label className="search-input">
          <Search size={18} />
          <input
            placeholder="输入页面或调查名称…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>
        <div className="command-list">
          {[
            ...nav,
            { to: "/settings", icon: Settings, label: "模型与设置" },
            ...(investigations.data?.items ?? []).map((x) => ({
              to: `/investigations/${x.id}`,
              icon: Search,
              label: x.title,
            })),
          ]
            .filter((x) => x.label.toLowerCase().includes(search.toLowerCase()))
            .map((x) => (
              <button
                key={x.to}
                onClick={() => {
                  navigate(x.to);
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
