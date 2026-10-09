import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { create } from "zustand";
import { AiAvatar, Badge, Btn, Dot, Icon, Kbd, LabelChip, Muted, Sev, Strong, Tile, type IconName } from "../ui/core";
import { toast } from "../ui/overlays";
import { useI18n } from "../i18n";
import { API_MODE, api, useGet } from "../api/client";
import type { SearchHit, SearchResult } from "../api/types";
import { usePreferences } from "./preferences";
import { useAskAi } from "./AskAi";
import "./palette.css";

export const useCommandPalette = create<{ open: boolean; setOpen: (v: boolean) => void }>((set) => ({ open: false, setOpen: (open) => set({ open }) }));

type Item = {
  id: string;
  group: "actions" | "results" | "recent" | "ai";
  icon: ReactNode;
  label: ReactNode;
  text: string;
  meta?: ReactNode;
  kbd?: string;
  run: () => void;
  preview?: ReactNode;
};

const KIND_ICON: Record<string, IconName> = {
  service: "box",
  alert: "bell",
  alert_group: "bell",
  incident: "alert",
  investigation: "branch",
  conversation: "message",
  runbook: "book",
  change: "branch",
  proposal: "shield",
  person: "user",
  artifact: "file",
  connector: "plug",
  run: "play",
};

function Highlight({ text, q }: { text: string; q: string }) {
  if (!q) return <>{text}</>;
  const i = text.toLowerCase().indexOf(q.toLowerCase());
  if (i < 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, i)}
      <b style={{ color: "var(--accent-text)", fontWeight: 600 }}>{text.slice(i, i + q.length)}</b>
      {text.slice(i + q.length)}
    </>
  );
}

export function CommandPalette() {
  const { open, setOpen } = useCommandPalette();
  const { tx } = useI18n();
  const nav = useNavigate();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const [duration, setDuration] = useState("1h");
  const listRef = useRef<HTMLDivElement>(null);
  const { setTheme, theme, setLocale, locale } = usePreferences();
  const openAi = useAskAi((s) => s.open);
  const search = useGet<SearchResult>(open ? "/search" : null, { q }, { staleTime: 5000 });

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen(!useCommandPalette.getState().open);
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [setOpen]);
  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
    }
  }, [open]);
  useEffect(() => setSel(0), [q]);

  const close = () => setOpen(false);
  const go = (to: string) => {
    close();
    nav(to);
  };
  const silence = async () => {
    close();
    const res = await api<{ id: string }>("POST", "/silences", {
      body: { matchers: [{ name: "service", op: "=", value: "checkout-api" }], duration, comment: "⌘K" },
    });
    toast(tx("已静默 checkout-api 的 3 条告警 · {d}", "Silenced 3 alerts on checkout-api · {d}", { d: duration }), {
      undo: () => void api("DELETE", `/silences/${res.id}`),
    });
  };

  const items = useMemo<Item[]>(() => {
    const ql = q.trim().toLowerCase();
    const match = (...s: string[]) => !ql || s.some((x) => x.toLowerCase().includes(ql));
    const actions = ([
      {
        id: "a-silence",
        group: "actions",
        icon: <Tile icon="mute" tone="accent" size={24} />,
        label: <Highlight text={tx("静默 checkout-api 的告警", "Silence alerts on checkout-api")} q={q} />,
        text: tx("静默 checkout-api 的告警 silence", "Silence alerts on checkout-api"),
        meta: <Muted size={11.5}>{tx("3 条", "3 alerts")}</Muted>,
        run: silence,
        preview: (
          <div className="pv anim-fade">
            <div className="v3-row" style={{ gap: 10 }}>
              <Tile icon="mute" tone="accent" size={28} />
              <div className="v3-col" style={{ gap: 0 }}>
                <Strong size={13.5}>{tx("静默告警", "Silence alerts")}</Strong>
                <Muted size={11.5}>{tx("写入 Alertmanager · 可随时撤销", "Writes to Alertmanager · undoable")}</Muted>
              </div>
            </div>
            <PvSection title={tx("匹配条件", "Matchers")}>
              <div className="v3-row" style={{ gap: 4, flexWrap: "wrap" }}>
                <LabelChip k="service" v="checkout-api" />
                <LabelChip k="env" v="prod" />
              </div>
            </PvSection>
            <PvSection title={tx("时长", "Duration")}>
              <div className="v3-seg">
                {["30m", "1h", "4h"].map((d) => (
                  <button key={d} type="button" className={d === duration ? "on" : ""} onClick={() => setDuration(d)}>
                    {d}
                  </button>
                ))}
              </div>
            </PvSection>
            <PvSection title={tx("将静默 3 条告警", "Will silence 3 alerts")}>
              {(
                [
                  ["P1", tx("5xx 比例 > 1%", "5xx ratio > 1%")],
                  ["P2", tx("P99 延迟 > 1 s", "P99 latency > 1 s")],
                  ["P3", tx("Pod 重启次数 > 3", "Pod restarts > 3")],
                ] as const
              ).map(([p, t]) => (
                <div key={t} className="v3-row" style={{ gap: 8, fontSize: "var(--fs-sub)" }}>
                  <Sev p={p} />
                  {t}
                </div>
              ))}
            </PvSection>
            <Btn kind="primary" h={32} onClick={silence}>
              {tx("确认静默", "Confirm silence")} <span className="mono" style={{ fontSize: "var(--fs-cap)", opacity: 0.8 }}>⏎</span>
            </Btn>
            <Muted size={11}>{tx("会记录到审计日志。", "Recorded in the audit log.")}</Muted>
          </div>
        ),
      },
      {
        id: "a-investigate",
        group: "actions",
        icon: <Tile icon="branch" tone="accent" size={24} />,
        label: <Highlight text={tx("发起调查：checkout-api 5xx 上升", "Investigate: checkout-api 5xx rising")} q={q} />,
        text: tx("发起调查 checkout-api investigate", "Investigate checkout-api 5xx"),
        meta: <Muted size={11.5}>{tx("模板", "Template")}</Muted>,
        run: () => go("/canvas/INV-231"),
        preview: (
          <div className="pv anim-fade">
            <div className="v3-row" style={{ gap: 10 }}>
              <Tile icon="branch" tone="accent" size={28} />
              <div className="v3-col" style={{ gap: 0 }}>
                <Strong size={13.5}>{tx("发起调查", "Start investigation")}</Strong>
                <Muted size={11.5}>{tx("模板：发布后回归 · 自动带入上下文", "Template: post-release regression")}</Muted>
              </div>
            </div>
            <PvSection title={tx("AI 会先读取", "AI reads first")}>
              {[
                ["bell", tx("2 条触发中的告警", "2 firing alerts")],
                ["branch", tx("最近变更 GitLab #8812 · v2.8.1", "Recent change GitLab #8812 · v2.8.1")],
                ["clock", tx("时间窗口 13:58 – 14:30", "Window 13:58 – 14:30")],
                ["book", tx("Runbook：下游超时与重试风暴", "Runbook: Retry storms")],
              ].map(([i, t]) => (
                <div key={t} className="v3-row" style={{ gap: 6, fontSize: "var(--fs-sub)" }}>
                  <Icon name={i as IconName} size={13} color="var(--muted)" />
                  {t}
                </div>
              ))}
            </PvSection>
            <Btn kind="primary" icon="branch" h={32} onClick={() => go("/canvas/INV-231")}>
              {tx("发起调查", "Start")}
            </Btn>
          </div>
        ),
      },
      {
        id: "a-service",
        group: "actions",
        icon: <Tile icon="box" size={24} />,
        label: <Highlight text={tx("打开服务 checkout-api", "Open service checkout-api")} q={q} />,
        text: "checkout-api service",
        meta: (
          <Badge tone="danger">
            <Dot tone="danger" size={6} />
            {tx("异常", "Degraded")}
          </Badge>
        ),
        run: () => go("/services/checkout-api"),
      },
      {
        id: "a-theme",
        group: "actions",
        icon: <Tile icon={theme === "dark" ? "sun" : "moon"} size={24} />,
        label: tx("切换到{m}主题", "Switch to {m} theme", { m: theme === "dark" ? tx("浅色", "light") : tx("深色", "dark") }),
        text: "theme dark light 主题 深色 浅色",
        run: () => {
          setTheme(theme === "dark" ? "light" : "dark");
          close();
        },
      },
      {
        id: "a-lang",
        group: "actions",
        icon: <Tile icon="globe" size={24} />,
        label: locale === "zh-CN" ? "Switch language to English" : "切换语言为中文",
        text: "language 语言 english 中文",
        run: () => {
          setLocale(locale === "zh-CN" ? "en-US" : "zh-CN");
          close();
        },
      },
      {
        id: "a-env",
        group: "actions",
        icon: <Tile icon="layers" size={24} />,
        label: tx("切换环境：生产 → 预发", "Switch environment: prod → staging"),
        text: "environment 环境 staging 预发",
        kbd: "⌘E",
        run: () => {
          close();
          toast(tx("已切换到预发环境（仅影响你的视图）", "Switched to staging (your view only)"));
        },
      },
    ] as Item[])
      // The silence / investigate / open-service / environment shortcuts act on the sample service and only exist as
      // sample data (the silence even writes); real mode keeps theme and language, the rest comes from search results.
      .filter((a) => API_MODE === "mock" || a.id === "a-theme" || a.id === "a-lang")
      .filter((a) => match(a.text));
    const hits: Item[] = (search.data?.hits ?? []).map((h: SearchHit) => ({
      id: `h-${h.kind}-${h.id}`,
      group: "results",
      icon: <Tile icon={KIND_ICON[h.kind] ?? "file"} size={24} />,
      label: <Highlight text={h.title} q={q} />,
      text: h.title,
      meta: h.subtitle ? <Muted size={11.5}>{h.subtitle}</Muted> : undefined,
      run: () => go(h.href),
    }));
    const recent: Item[] = !ql
      ? (search.data?.recent ?? []).map((h) => ({
          id: `r-${h.id}`,
          group: "recent",
          icon: <Tile icon={KIND_ICON[h.kind] ?? "file"} size={24} />,
          label: h.title,
          text: h.title,
          meta: h.subtitle ? <Muted size={11.5}>{h.subtitle}</Muted> : undefined,
          run: () => go(h.href),
        }))
      : [];
    const ai: Item = {
      id: "ai",
      group: "ai",
      icon: <AiAvatar size={24} />,
      label: q ? tx("问 AI：{q}", "Ask AI: {q}", { q }) : API_MODE === "mock" ? tx("问 AI：checkout-api 现在健康吗？", "Ask AI: is checkout-api healthy now?") : tx("问 AI", "Ask AI"),
      text: q,
      kbd: "Tab",
      run: () => {
        close();
        openAi(q || (API_MODE === "mock" ? tx("checkout-api 现在健康吗？", "Is checkout-api healthy now?") : ""));
      },
    };
    return [...actions, ...hits, ...recent, ai];
  }, [q, search.data, tx, theme, locale, duration]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    listRef.current?.querySelector(`[data-idx="${sel}"]`)?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  if (!open) return null;
  const current = items[sel];
  const groups: [Item["group"], string][] = [
    ["actions", tx("动作", "Actions")],
    ["results", tx("搜索结果", "Results")],
    ["recent", tx("最近", "Recent")],
    ["ai", tx("问 AI", "Ask AI")],
  ];
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSel((s) => Math.min(items.length - 1, s + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSel((s) => Math.max(0, s - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      current?.run();
    } else if (e.key === "Tab") {
      e.preventDefault();
      items.find((i) => i.group === "ai")?.run();
    } else if (e.key === "Escape") close();
  };
  let idx = -1;
  return createPortal(
    <div className="v3">
      <div className="v3-backdrop" onClick={close} aria-hidden="true" />
      <div className="v3-palette" role="dialog" aria-label={tx("动作面板", "Command palette")} onKeyDown={onKey}>
        <div className="pal-input">
          <Icon name="search" size={18} color="var(--muted)" />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={tx("搜索服务、告警、Runbook，或输入一个动作…", "Search services, alerts, runbooks, or type an action…")}
            aria-label={tx("搜索", "Search")}
          />
          {search.isFetching && <span className="v3-spinner spin" style={{ color: "var(--muted)" }} />}
          <Kbd>esc</Kbd>
        </div>
        <div className="pal-body">
          <div className="pal-list" ref={listRef} role="listbox">
            {groups.map(([g, title]) => {
              const gi = items.filter((i) => i.group === g);
              if (!gi.length) return null;
              return (
                <div key={g}>
                  <div className="pal-group">{title}</div>
                  {gi.map((it) => {
                    idx += 1;
                    const i = idx;
                    return (
                      <button key={it.id} type="button" role="option" aria-selected={i === sel} data-idx={i} className={"pal-item" + (i === sel ? " on" : "")} onMouseMove={() => setSel(i)} onClick={it.run}>
                        {it.icon}
                        <span className="ellipsis grow">{it.label}</span>
                        {it.meta}
                        {it.kbd && <Kbd>{it.kbd}</Kbd>}
                        <span className="enter" style={{ opacity: i === sel ? 1 : 0 }}>
                          <Kbd>⏎</Kbd>
                        </span>
                      </button>
                    );
                  })}
                </div>
              );
            })}
          </div>
          <div className="pal-preview">{current?.preview ?? <DefaultPreview item={current} />}</div>
        </div>
        <div className="pal-foot">
          <span className="v3-row" style={{ gap: 4 }}>
            <Kbd>↑</Kbd>
            <Kbd>↓</Kbd>
            {tx("选择", "Select")}
          </span>
          <span className="v3-row" style={{ gap: 4 }}>
            <Kbd>⏎</Kbd>
            {tx("执行", "Run")}
          </span>
          <span className="v3-row" style={{ gap: 4 }}>
            <Kbd>Tab</Kbd>
            {tx("问 AI", "Ask AI")}
          </span>
          <span className="v3-row" style={{ gap: 4 }}>
            <Kbd>⌘K</Kbd>
            {tx("关闭", "Close")}
          </span>
          <span style={{ flexGrow: 1 }} />
          <Icon name="shield" size={12} />
          {tx("动作会记录到审计日志", "Actions are audited")}
        </div>
      </div>
    </div>,
    document.body,
  );
}

function PvSection({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <div className="v3-col" style={{ gap: 6 }}>
      <span style={{ fontSize: "var(--fs-sub)", fontWeight: 600, color: "var(--muted)" }}>{title}</span>
      {children}
    </div>
  );
}

function DefaultPreview({ item }: { item?: Item }) {
  const { tx } = useI18n();
  if (!item) return null;
  return (
    <div className="pv anim-fade" style={{ alignItems: "flex-start" }}>
      {item.icon}
      <Strong size={13.5}>{item.label}</Strong>
      {item.meta}
      <Muted size={11.5}>{tx("按 ⏎ 打开", "Press ⏎ to open")}</Muted>
    </div>
  );
}
