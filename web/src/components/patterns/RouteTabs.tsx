import { NavLink, useLocation } from "react-router-dom";
import "./list-page.css";

/** Page-level tabs that are real routes, so each tab has its own URL. */
export function RouteTabs({
  label,
  tabs,
}: {
  label: string;
  tabs: readonly { to: string; label: string }[];
}) {
  const { search } = useLocation();
  return (
    <nav className="ui-route-tabs" aria-label={label}>
      {tabs.map((tab) => (
        <NavLink
          key={tab.to}
          to={{ pathname: tab.to, search }}
          end
          className={({ isActive }) => (isActive ? "active" : "")}
        >
          {tab.label}
        </NavLink>
      ))}
    </nav>
  );
}
