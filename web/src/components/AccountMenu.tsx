import { useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronUp, LogOut, Settings, Info } from "../lib/icons";
import type { User } from "../lib/types";

export default function AccountMenu({
  user,
  settingsURL,
  preview,
  onNavigate,
  onLogout,
  onHelp,
}: {
  user: User;
  settingsURL: string;
  preview: boolean;
  onNavigate: () => void;
  onLogout: () => void;
  onHelp: () => void;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null),
    trigger = useRef<HTMLButtonElement>(null),
    menu = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    const outside = (e: PointerEvent) => {
      if (e.target instanceof Node && !root.current?.contains(e.target))
        setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  return (
    <div ref={root} className="sidebar-account">
      <button
        ref={trigger}
        className="account-trigger"
        aria-label={`用户菜单：${user.name}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === "ArrowUp" || e.key === "ArrowDown") {
            e.preventDefault();
            setOpen(true);
          }
        }}
      >
        <span className="avatar" aria-hidden="true">
          {user.name.slice(0, 1).toUpperCase()}
        </span>
        <span className="account-identity">
          <strong>{user.name}</strong>
          <small>
            {user.role === "admin"
              ? "管理员"
              : user.role === "operator"
                ? "操作员"
                : "只读成员"}
          </small>
        </span>
        <ChevronUp size={14} />
      </button>
      {open && (
        <div
          ref={menu}
          id={id}
          className="account-menu"
          role="menu"
          aria-label="用户选项"
          onBlur={(e) => {
            if (
              !e.currentTarget.contains(e.relatedTarget as Node) &&
              e.relatedTarget !== trigger.current
            )
              setOpen(false);
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              setOpen(false);
              trigger.current?.focus();
            }
            const items = Array.from(
              menu.current?.querySelectorAll<HTMLElement>(
                '[role="menuitem"]',
              ) ?? [],
            );
            const current = items.indexOf(
              document.activeElement as HTMLElement,
            );
            if (["ArrowUp", "ArrowDown", "Home", "End"].includes(e.key)) {
              e.preventDefault();
              const n =
                e.key === "Home"
                  ? 0
                  : e.key === "End"
                    ? items.length - 1
                    : (current +
                        (e.key === "ArrowDown" ? 1 : -1) +
                        items.length) %
                      items.length;
              items[n]?.focus();
            }
          }}
        >
          {user.role === "admin" && (
            <Link
              role="menuitem"
              to={settingsURL}
              onClick={() => {
                setOpen(false);
                onNavigate();
              }}
            >
              <Settings size={17} />
              设置
            </Link>
          )}
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false);
              trigger.current?.focus();
              onHelp();
            }}
          >
            <Info size={17} />
            帮助与支持
          </button>
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false);
              onLogout();
            }}
          >
            <LogOut size={17} />
            {preview ? "返回登录" : "退出登录"}
          </button>
        </div>
      )}
    </div>
  );
}
