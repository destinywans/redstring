import { Suspense, useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { AiAvatar, Avatar, Badge, Btn, Dot, Icon, IconBtn, Kbd, Skeleton, Tile, cx } from "../ui/core";
import { MenuButton, Toaster, toast } from "../ui/overlays";
import { useI18n } from "../i18n";
import { NAV, SETTINGS_GROUPS, SETTINGS_NAV, locate, type NavItem } from "./nav";
import { CommandPalette, useCommandPalette } from "./CommandPalette";
import { AskAiCard, useAskAi, useAskOpenClass } from "./AskAi";
import { DemoHost } from "../ui/demo";
import { RollbackDrawerHost } from "../features/events/RollbackProposal";
import { BrandMark } from "../ui/BrandMark";
import { PageBoundary } from "../ui/PageBoundary";
import { LoadError } from "../ui/unknown";
import { API_MODE, ApiError, DEMO_ABOUT_URL, useGet } from "../api/client";
import type { Me, WatchedService } from "../api/types";
import { EnvSwitch, useInstanceLoader } from "./EnvSwitch";
import NoAccessPage, { signOutToLogin } from "../features/auth/NoAccessPage";
import { useEnv } from "./environment";
import { Gated, useNeed } from "../ui/permission";
import "./shell.css";

function Section({ item, active, activeChild }: { item: NavItem; active: boolean; activeChild?: string }) {
  const { tl } = useI18n();
  const [open, setOpen] = useState(active);
  useEffect(() => {
    if (active) setOpen(true);
  }, [active]);
  const hasChildren = item.children.length > 0;
  return (
    <div className="nav-section">
      <div className={cx("nav-item", active && !hasChildren && "active", active && hasChildren && "parent")}>
        {hasChildren ? (
          // Sections with children only expand / collapse; the user picks a child explicitly.
          <button type="button" className="nav-link nav-toggle" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
            <Icon name={item.icon} size={16} />
            <span className="grow">{tl(item.label)}</span>
            {item.count ? <span className="nav-count">{item.count}</span> : null}
            <Icon name="down" size={13} className="nav-chevron" style={{ transform: open ? "none" : "rotate(-90deg)" }} />
          </button>
        ) : (
          <Link to={item.to} className="nav-link">
            <Icon name={item.icon} size={16} />
            <span className="grow">{tl(item.label)}</span>
            {item.count ? <span className="nav-count">{item.count}</span> : null}
          </Link>
        )}
      </div>
      {hasChildren && (
        <div className={cx("nav-children", open && "open")}>
          <div>
            {item.children.map((c) => (
              <NavLink key={c.id} to={c.to} className={() => cx("nav-child", activeChild === c.id && "active")}>
                {tl(c.label)}
              </NavLink>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function Sidebar() {
  const { pathname } = useLocation();
  const { tx } = useI18n();
  const where = locate(pathname);
  const open = useCommandPalette((s) => s.setOpen);
  const me = useGet<Me>("/me");
  const watched = useGet<{ items: WatchedService[] }>("/me/watched-services");
  const newChatBlocked = useNeed(["conversation", "change"]);
  // The workspace menu names the real workspace (it was always the sample's "Trading platform", PA-224); without
  // workspace.view, or before it loads, it just says "current workspace".
  const noWorkspace = useNeed(["workspace", "view"]);
  const workspace = useGet<{ name: string }>(noWorkspace ? null : "/workspace", undefined, { staleTime: 5 * 60_000 });
  const navTo = useNavigate();
  return (
    <nav className="v3-sidebar" aria-label={tx("主导航", "Main navigation")}>
      <div className="brand-row">
        <BrandMark size={20} state="intro" />
        <span className="grow brand-name">Redstring</span>
        <MenuButton
          place="bottom-end"
          trigger={({ onClick, ref }) => (
            <span ref={ref}>
              <IconBtn icon="down" label={tx("切换工作区", "Switch workspace")} size={24} onClick={onClick} />
            </span>
          )}
          items={[
            { label: workspace.data?.name ? tx("{n} · 当前工作区", "{n} · current workspace", { n: workspace.data.name }) : tx("当前工作区", "Current workspace"), icon: "check", active: true },
            { label: tx("工作区设置", "Workspace settings"), icon: "sliders", onSelect: () => navTo("/settings/workspace") },
            { label: tx("成员与权限", "Members & access"), icon: "users", onSelect: () => navTo("/settings/members") },
          ]}
        />
      </div>
      <div className="v3-row" style={{ gap: 6 }}>
        <button type="button" className="search-btn" onClick={() => open(true)}>
          <Icon name="search" size={14} />
          <span className="grow" style={{ textAlign: "left" }}>
            {tx("搜索或跳转", "Search or jump")}
          </span>
          <Kbd>⌘K</Kbd>
        </button>
        {newChatBlocked ? (
          <Gated reason={newChatBlocked}>
            <button type="button" className="new-btn" aria-label={tx("新建对话", "New conversation")}>
              <Icon name="plus" size={15} />
            </button>
          </Gated>
        ) : (
          <Link to="/conversations/new" className="new-btn" aria-label={tx("新建对话", "New conversation")} title={tx("新建对话", "New conversation")}>
            <Icon name="plus" size={15} />
          </Link>
        )}
      </div>
      <div className="nav-list">
        {NAV.map((s) => (
          <Section key={s.id} item={s} active={where.section?.id === s.id} activeChild={where.section?.id === s.id ? where.child?.id : undefined} />
        ))}
      </div>
      <div className="nav-list">
        <h2 className="nav-head">{tx("关注的服务", "Watched services")}</h2>
        {watched.data?.items.map((s) => (
          <Link key={s.id} to={`/services/${s.id}`} className="watched">
            {/* The server sends status null when it has no current status evidence: a neutral dot that says so, not no dot (PA-222). */}
            {s.status ? (
              <Dot tone={s.status} size={7} pulse={s.status === "danger"} />
            ) : (
              <span title={tx("状态未知：现在拿不到这个服务的状态证据", "Status unknown: no current status evidence for this service")} style={{ display: "inline-flex" }}>
                <Dot tone="neutral" size={7} />
              </span>
            )}
            <span className="ellipsis grow">{s.name}</span>
            {s.signal && (
              <span className="tnum" style={{ fontSize: "var(--fs-sub)", color: `var(--${s.status === "ok" ? "muted" : s.status})` }}>
                {s.signal}
              </span>
            )}
          </Link>
        ))}
      </div>
      <span style={{ flexGrow: 1 }} />
      <AccountCard me={me.data} />
    </nav>
  );
}

/** Last page outside settings, so "Back to app" returns where the user left. */
let lastAppPath = "/";

/** In settings the app sidebar is replaced by the settings menu (Linear / Notion style). */
function SettingsSidebar() {
  const { tx, tl } = useI18n();
  const { pathname } = useLocation();
  const me = useGet<Me>("/me");
  return (
    <nav className="v3-sidebar settings-sidebar" aria-label={tx("设置导航", "Settings navigation")}>
      <Link to={lastAppPath} className="settings-back">
        <Icon name="left" size={15} />
        {tx("返回应用", "Back to app")}
      </Link>
      {SETTINGS_GROUPS.map((g) => (
        <div key={g.id} className="nav-list">
          <h2 className="nav-head">{g.id === "workspace" && me.data?.team ? `${tl(g.label)} · ${me.data.team}` : tl(g.label)}</h2>
          {g.items.map((it) => {
            const active = pathname === it.to || pathname.startsWith(it.to + "/");
            return (
              <Link key={it.id} to={it.to} className={cx("nav-item nav-link settings-item", active && "active")} aria-current={active ? "page" : undefined}>
                <Icon name={it.icon} size={15} />
                <span className="grow">{tl(it.label)}</span>
              </Link>
            );
          })}
        </div>
      ))}
      <span style={{ flexGrow: 1 }} />
      <AccountCard me={me.data} />
    </nav>
  );
}

function AccountCard({ me }: { me?: Me }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const signOut = () => {
    if (API_MODE === "mock") toast(tx("示意模式：不会真正退出登录", "Sample mode: you are not actually signed out"));
    else
      signOutToLogin();
  };
  return (
    <MenuButton
      place="top-start"
      items={[
        { label: tx("设置", "Settings"), icon: "sliders", onSelect: () => nav("/settings/profile") },
        { sep: true, label: "" },
        { label: tx("退出登录", "Sign out"), icon: "logout", danger: true, onSelect: signOut },
      ]}
      trigger={({ onClick, ref }) => (
        <button type="button" ref={ref} onClick={onClick} className="account">
          <Avatar name={me?.name ?? "?"} size={26} accent />
          <span className="v3-col grow" style={{ gap: 0, lineHeight: 1.3, textAlign: "left" }}>
            <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{me?.name ?? "…"}</span>
            <span className="muted ellipsis" style={{ fontSize: "var(--fs-sub)" }}>
              {me ? [me.title, me.team].filter(Boolean).join(" · ") : ""}
            </span>
          </span>
          {me?.oncall?.active && (
            <Badge tone="ok">
              <Dot tone="ok" size={6} />
              {tx("值班", "On call")}
            </Badge>
          )}
        </button>
      )}
    />
  );
}

/** Top bar used by every page: breadcrumbs on the left, page actions + Ask AI on the right. */
/** A breadcrumb is plain text/node, or an explicit `{ label, to }` for levels that are not menu items (a service, a proposal…). */
export type Crumb = ReactNode | { label: ReactNode; to: string };
const isLinkCrumb = (c: Crumb): c is { label: ReactNode; to: string } => typeof c === "object" && c !== null && "to" in c && "label" in c;

/** Labels used in page titles that differ from the menu wording. */
const CRUMB_ALIASES: Record<string, string> = { 全部对话: "/conversations", "All conversations": "/conversations", 事件收件箱: "/inbox", Inbox: "/inbox", 工作区通用: "/settings/workspace", Workspace: "/settings/workspace" };

/** Resolve where a breadcrumb level goes: explicit link, alias, or the matching menu section / item. */
function useCrumbHref() {
  const { tl } = useI18n();
  const { pathname } = useLocation();
  const where = locate(pathname);
  return (c: Crumb, i: number): string | undefined => {
    if (isLinkCrumb(c)) return c.to;
    if (typeof c !== "string") return undefined;
    if (CRUMB_ALIASES[c]) return CRUMB_ALIASES[c];
    const sections = [...NAV, SETTINGS_NAV];
    const section = sections.find((sct) => tl(sct.label) === c);
    if (section) return section.to;
    const pool = where.section ? [where.section, ...sections.filter((x) => x !== where.section)] : sections;
    for (const sct of pool) {
      const child = sct.children.find((ch) => tl(ch.label) === c);
      if (child) return child.to;
    }
    if (i === 0 && where.section) return where.section.to;
    if (i === 1 && where.child) return where.child.to;
    return undefined;
  };
}

/**
 * `error` is the page's main read failure (pass it only while there is no data): the body then
 * explains the missing or failing source instead of an empty table or an endless skeleton.
 */
export function Page({ crumbs, title, actions, children, ai = true, bodyClass, flush, error, errorSource }: { crumbs?: Crumb[]; title?: ReactNode; actions?: ReactNode; children: ReactNode; ai?: boolean; bodyClass?: string; flush?: boolean; error?: unknown; errorSource?: string }) {
  const { tx, tl } = useI18n();
  const toggleAi = useAskAi((s) => s.toggle);
  const aiOpen = useAskAi((s) => s.isOpen);
  const hrefOf = useCrumbHref();
  const prodInstance = useEnv((s) => s.instance?.tier === "production");
  const { pathname } = useLocation();
  useEffect(() => {
    const t = crumbs?.map((c) => (isLinkCrumb(c) ? c.label : c)).filter((c) => typeof c === "string").slice(-1)[0] ?? (typeof title === "string" ? title : "");
    document.title = t ? `${t} · Redstring` : "Redstring";
  }, [crumbs, title]);
  return (
    <main className="v3-page">
      <header className={cx("v3-topbar", prodInstance && !pathname.startsWith("/settings") && "env-prod")}>
        {title ?? (
          <nav className="crumbs" aria-label={tx("面包屑", "Breadcrumbs")}>
            {crumbs?.map((c, i) => {
              const last = i === crumbs.length - 1;
              const label = isLinkCrumb(c) ? c.label : c;
              const href = last ? undefined : hrefOf(c, i);
              return (
                <span key={i} className={last ? "cur" : ""}>
                  {i > 0 && <Icon name="right" size={12} color="var(--faint)" />}
                  {href && href !== pathname ? (
                    <Link to={href} className="crumb-link">
                      {label}
                    </Link>
                  ) : (
                    label
                  )}
                </span>
              );
            })}
          </nav>
        )}
        {API_MODE === "mock" &&
          (DEMO_ABOUT_URL ? (
            <a
              className="mock-badge mock-badge-link"
              href={tl(DEMO_ABOUT_URL)}
              target="_blank"
              rel="noopener"
              title={tx("这是 Redstring 的示例数据演示，不连后端、不调用模型。点击在新标签页打开项目介绍。", "This is a Redstring demo on sample data: no backend, no model calls. Opens the project page in a new tab.")}
              aria-label={tx("示意数据：在新标签页打开 Redstring 项目介绍", "Sample data: open the Redstring project page in a new tab")}
            >
              {tx("示意数据", "Sample data")}
              <Icon name="arrow-up-right" size={11} />
            </a>
          ) : (
            <span className="mock-badge" title={tx("页面数据来自前端 mock，等待后端按 API 契约提供", "Page data is served by frontend mocks until the backend implements the API contract")}>
              {tx("示意数据", "Sample data")}
            </span>
          ))}
        <span style={{ flexGrow: 1 }} />
        {actions}
        <EnvSwitch />
        {ai && (
          <Btn kind="ghost" onClick={toggleAi} className={cx("ask-toggle", aiOpen && "on")} aria-pressed={aiOpen} style={{ color: "var(--accent-text)" }}>
            <Icon name="sparkles" size={14} />
            {tx("问 AI", "Ask AI")}
            <Kbd>⌘J</Kbd>
          </Btn>
        )}
      </header>
      <div className={cx("v3-page-body", flush && "flush", bodyClass)}>{error ? <LoadError error={error} source={errorSource} /> : children}</div>
    </main>
  );
}

export function Shell() {
  useInstanceLoader();
  // A signed-in account without any environment gets 403 on /me and on every page: show that instead of the shell.
  const meCheck = useGet<Me>("/me");
  const noAccess = API_MODE !== "mock" && meCheck.error instanceof ApiError && meCheck.error.status === 403 && !meCheck.data;
  const { pathname } = useLocation();
  const { tx } = useI18n();
  const toggleAi = useAskAi((s) => s.toggle);
  const askClass = useAskOpenClass();
  const inSettings = pathname.startsWith("/settings");
  useEffect(() => {
    if (!inSettings) lastAppPath = pathname + location.search;
  }, [pathname, inSettings]);
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "j") {
        e.preventDefault();
        toggleAi();
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [toggleAi]);
  if (noAccess) return <NoAccessPage />;
  return (
    <div className={cx("v3 v3-shell", askClass)}>
      <a href="#v3-main" className="sr-only">
        {tx("跳到主要内容", "Skip to content")}
      </a>
      {inSettings ? <SettingsSidebar /> : <Sidebar />}
      <div id="v3-main" className="v3-main" key={pathname.split("/").slice(0, 2).join("/")}>
        <PageBoundary resetKey={pathname}>
          {/* Pages load lazily; suspend here so the sidebar stays while a page's code arrives. */}
          <Suspense fallback={<PageLoading />}>
            <Outlet />
          </Suspense>
        </PageBoundary>
      </div>
      <CommandPalette />
      {!inSettings && <AskAiCard />}
      <DemoHost />
      <RollbackDrawerHost />
      <Toaster />
    </div>
  );
}

export { AiAvatar, Tile };

/** Content-area placeholder while a page's code loads; the shell around it stays interactive. */
export function PageLoading() {
  return (
    <main className="v3-page" data-loading="">
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
