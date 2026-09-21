import { lazy, Suspense } from "react";
import { NavLink, Route, Routes, useLocation } from "react-router-dom";
import {
  Activity,
  ArrowLeft,
  Building2,
  Info,
  LayoutGrid,
  Moon,
  ShieldCheck,
  Sun,
  Users,
  User,
} from "../../lib/icons";
import { useWorkspace } from "../../lib/workspace";
import "./settings-workspace.css";
import { useRbacAccess, isRbacPath } from "./rbac/access";

const Profile = lazy(() =>
  import("./ProfilePage").then((module) => ({ default: module.ProfilePage })),
);
const Overview = lazy(() => import("./WorkspaceOverview"));
const Members = lazy(() =>
  import("./directory/MembersSettingsPage").then((module) => ({
    default: module.MembersSettingsPage,
  })),
);
const Teams = lazy(() =>
  import("./directory/TeamsSettingsPage").then((module) => ({
    default: module.TeamsSettingsPage,
  })),
);
const Roles = lazy(() =>
  import("./directory/RolesSettingsPage").then((module) => ({
    default: module.RolesSettingsPage,
  })),
);
const Permissions = lazy(() =>
  import("./directory/PermissionsSettingsPage").then((module) => ({
    default: module.PermissionsSettingsPage,
  })),
);
const destinations = [
  { to: "/profile", label: "个人资料", icon: User },
  { to: "/platform/settings", label: "平台概览", icon: LayoutGrid },
  { to: "/members", label: "成员管理", icon: Users },
  { to: "/teams", label: "部门管理", icon: Building2 },
  { to: "/platform/roles", label: "角色管理", icon: ShieldCheck },
  { to: "/platform/permissions", label: "权限管理", icon: ShieldCheck },
];

export default function SettingsWorkspace({
  homeURL,
  avatarURL,
  routeLink,
  theme,
  onToggleTheme,
  onHelp,
}: {
  homeURL: string;
  avatarURL?: string;
  routeLink: (to: string) => string;
  theme: string;
  onToggleTheme: () => void;
  onHelp: () => void;
}) {
  const { user, preview } = useWorkspace();
  const access = useRbacAccess(user, preview);
  const location = useLocation();
  const destination = destinations.find(
    (item) => item.to === location.pathname,
  );
  return (
    <div className="settings-workspace">
      <a className="skip-link" href="#settings-main">
        跳到设置内容
      </a>
      <aside className="settings-sidebar" aria-label="平台设置导航">
        <NavLink className="settings-back" to={homeURL}>
          <ArrowLeft size={17} />
          返回工作台
        </NavLink>
        <div className="settings-workspace-identity">
          <span>
            <Activity size={24} />
          </span>
          <div>
            <strong>AIOps</strong>
            <small>平台设置</small>
          </div>
        </div>
        <nav aria-label="设置页面">
          {destinations
            .filter(
              (item) =>
                item.to === "/profile" ||
                user.role === "admin" ||
                (isRbacPath(item.to) && access.canManage("rbac")),
            )
            .map((item) => (
              <NavLink
                key={item.to}
                to={routeLink(item.to)}
                end
                className={({ isActive }) => (isActive ? "active" : "")}
              >
                <item.icon size={18} />
                {item.label}
              </NavLink>
            ))}
        </nav>
        <div className="settings-sidebar-footer">
          <button onClick={onHelp}>
            <Info size={17} />
            帮助与支持
          </button>
          <div className="settings-viewer">
            <span className="avatar">
              {avatarURL ? (
                <img src={avatarURL} alt="" />
              ) : (
                user.name.slice(0, 1).toUpperCase()
              )}
            </span>
            <div>
              <strong>{user.name}</strong>
              <small>{user.role === "admin" ? "管理员" : "成员"}</small>
            </div>
          </div>
        </div>
      </aside>
      <div className="settings-body">
        <header className="settings-topbar">
          <span>设置</span>
          <span aria-hidden="true">/</span>
          <strong>{destination?.label}</strong>
          <button
            className="icon-button theme-toggle"
            onClick={onToggleTheme}
            aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
          >
            <Sun className="sun" size={19} />
            <Moon className="moon" size={19} />
          </button>
        </header>
        <main id="settings-main" tabIndex={-1} className="settings-content">
          {user.role !== "admin" &&
          location.pathname !== "/profile" &&
          !(isRbacPath(location.pathname) && access.canManage("rbac")) ? (
            <div className="settings-denied">
              <ShieldCheck size={32} />
              <h1>
                {access.isPending
                  ? "正在核对管理权限…"
                  : "需要此页面的管理权限"}
              </h1>
              <p>平台设置包含账号与权限资料，请联系管理员。</p>
              <NavLink className="btn" to={homeURL}>
                返回工作台
              </NavLink>
            </div>
          ) : (
            <Suspense
              fallback={<div className="page-loading">正在加载设置…</div>}
            >
              <Routes>
                <Route path="/profile" element={<Profile />} />
                <Route path="/platform/settings" element={<Overview />} />
                <Route path="/members" element={<Members />} />
                <Route path="/teams" element={<Teams />} />
                <Route path="/platform/roles" element={<Roles />} />
                <Route path="/platform/permissions" element={<Permissions />} />
              </Routes>
            </Suspense>
          )}
        </main>
      </div>
    </div>
  );
}
