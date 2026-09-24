import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AiAvatar, Avatar, Chip, Icon, Kbd, Muted, Sev, Tier, Tile, cx, type IconName } from "./core";
import { useI18n } from "../i18n";
import { useGet } from "../api/client";
import type { MentionCandidate, RefKind } from "../api/types";
import "./composer.css";

export type Attachment = { kind: RefKind | "view" | "time"; id: string; label: string };

const COMMANDS: { name: string; zhName: string; zh: string; en: string; icon: IconName }[] = [
  { name: "/investigate", zhName: "/调查", zh: "发起调查：选择模板并带入当前上下文", en: "Start an investigation with the current context", icon: "branch" },
  { name: "/report", zhName: "/报告", zh: "生成或更新故障报告", en: "Draft or update the incident report", icon: "file" },
  { name: "/silence", zhName: "/静默", zh: "静默告警（需要确认）", en: "Silence alerts (asks for confirmation)", icon: "mute" },
  { name: "/compare", zhName: "/对比", zh: "对比两个时间段、版本或节点", en: "Compare two windows, versions or nodes", icon: "layers" },
  { name: "/runbook", zhName: "/runbook", zh: "查找并按步骤执行 Runbook", en: "Find and follow a runbook", icon: "book" },
  { name: "/branch", zhName: "/分支", zh: "在 Canvas 中派生一个分支", en: "Fork a branch on the canvas", icon: "fork" },
  { name: "/summarize", zhName: "/总结", zh: "总结当前对话，生成要点", en: "Summarize this conversation", icon: "sparkles" },
];
const KIND_ICON: Partial<Record<RefKind, IconName>> = { service: "box", alert: "bell", runbook: "book", artifact: "file", incident: "alert", change: "branch" };
const KIND_LABEL: Partial<Record<RefKind, [string, string]>> = {
  service: ["服务", "Services"],
  alert: ["告警", "Alerts"],
  runbook: ["Runbook", "Runbooks"],
  person: ["人员", "People"],
  artifact: ["产物", "Artifacts"],
  incident: ["事件", "Incidents"],
};

/** Composer with `/` commands and `@` mentions, attachments as chips, ⌘⏎ to send. */
export function Composer({
  placeholder,
  onSend,
  attachments: initial = [],
  big,
  rows = 2,
  autoFocus,
  footer,
  sending,
}: {
  placeholder: string;
  onSend: (text: string, attachments: Attachment[]) => void;
  attachments?: Attachment[];
  big?: boolean;
  rows?: number;
  autoFocus?: boolean;
  footer?: ReactNode;
  sending?: boolean;
}) {
  const { tx, zh } = useI18n();
  const [text, setText] = useState("");
  const [att, setAtt] = useState<Attachment[]>(initial);
  const [sel, setSel] = useState(0);
  const ref = useRef<HTMLTextAreaElement>(null);
  const token = useMemo(() => {
    const m = text.match(/(^|\s)([/@])([^\s]*)$/);
    return m ? { kind: m[2] as "/" | "@", q: m[3] } : null;
  }, [text]);
  const mentions = useGet<{ items: MentionCandidate[] }>(token?.kind === "@" ? "/mentions" : null, { q: token?.q ?? "" }, { staleTime: 10_000 });
  const cmdList = token?.kind === "/" ? COMMANDS.filter((c) => c.name.slice(1).startsWith(token.q.toLowerCase()) || c.zhName.slice(1).startsWith(token.q) || (zh ? c.zh : c.en).includes(token.q)) : [];
  const menItems = token?.kind === "@" ? mentions.data?.items ?? [] : [];
  const count = token?.kind === "/" ? cmdList.length : menItems.length;
  useEffect(() => setSel(0), [token?.kind, token?.q]);

  const replaceToken = (insert: string) => setText((t) => t.replace(/(^|\s)([/@])([^\s]*)$/, `$1${insert} `));
  const choose = (i: number) => {
    if (token?.kind === "/") {
      const c = cmdList[i];
      if (c) replaceToken(zh ? c.zhName : c.name);
    } else if (token?.kind === "@") {
      const m = menItems[i];
      if (m) {
        replaceToken("");
        setAtt((a) => (a.some((x) => x.id === m.id) ? a : [...a, { kind: m.kind, id: m.id, label: m.label }]));
      }
    }
    ref.current?.focus();
  };
  const send = () => {
    if (!text.trim() && !att.length) return;
    onSend(text.trim(), att);
    setText("");
  };
  const onKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (token && count) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setSel((s) => (s + 1) % count);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setSel((s) => (s - 1 + count) % count);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        choose(sel);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setText((t) => t + " ");
        return;
      }
    }
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey || !e.shiftKey)) {
      e.preventDefault();
      send();
    }
  };
  let lastKind: RefKind | null = null;
  return (
    <div className={cx("v3-composer", big && "big")}>
      {token && count > 0 && (
        <div className="composer-menu anim-pop" role="listbox" aria-label={tx("输入建议", "Suggestions")}>
          <div className="menu-head">
            <b>{token.kind === "/" ? tx("命令", "Commands") : tx("提及", "Mentions")}</b>
            <span style={{ flexGrow: 1 }} />
            <Kbd>↑</Kbd>
            <Kbd>↓</Kbd>
            <Muted size={11.5}>{tx("选择", "select")}</Muted>
            <Kbd>⏎</Kbd>
            <Muted size={11.5}>{tx("插入", "insert")}</Muted>
            <Kbd>esc</Kbd>
          </div>
          <ul>
            {token.kind === "/" &&
              cmdList.map((c, i) => (
                <li key={c.name} role="option" aria-selected={i === sel} className={i === sel ? "on" : ""} onMouseDown={(e) => { e.preventDefault(); choose(i); }} onMouseMove={() => setSel(i)}>
                  <Tile icon={c.icon} tone={i === sel ? "accent" : "neutral"} size={24} />
                  <span className="mono" style={{ fontSize: 12.5 }}>{zh ? c.zhName : c.name}</span>
                  <Muted>{zh ? c.zh : c.en}</Muted>
                </li>
              ))}
            {token.kind === "@" &&
              menItems.map((m, i) => {
                const head = m.kind !== lastKind ? KIND_LABEL[m.kind] : undefined;
                lastKind = m.kind;
                return (
                  <li key={m.kind + m.id} style={{ display: "contents" }}>
                    {head && <div className="menu-group">{zh ? head[0] : head[1]}</div>}
                    <div role="option" aria-selected={i === sel} className={cx("opt", i === sel && "on")} onMouseDown={(e) => { e.preventDefault(); choose(i); }} onMouseMove={() => setSel(i)}>
                      {m.kind === "person" ? <Avatar name={m.label} size={22} /> : m.kind === "alert" ? <Sev p="P1" /> : <Tile icon={KIND_ICON[m.kind] ?? "file"} size={22} />}
                      <span style={{ fontSize: 12.5 }}>{m.label}</span>
                      <span style={{ flexGrow: 1 }} />
                      {m.tier && <Tier tier={m.tier} />}
                      {m.meta && <Muted size={11.5}>{m.meta}</Muted>}
                    </div>
                  </li>
                );
              })}
          </ul>
        </div>
      )}
      <textarea
        ref={ref}
        rows={rows}
        value={text}
        autoFocus={autoFocus}
        placeholder={placeholder}
        aria-label={tx("向 AI 下指令", "Message the AI")}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKey}
      />
      <div className="composer-bar">
        {att.map((a) => (
          <Chip key={a.id} icon={a.kind === "view" ? "grid" : a.kind === "time" ? "clock" : "at"} onClick={() => setAtt((x) => x.filter((y) => y.id !== a.id))}>
            {a.label}
            <Icon name="x" size={11} />
          </Chip>
        ))}
        {footer}
        <span style={{ flexGrow: 1 }} />
        <Muted size={11.5}>{tx("/ 命令 · @ 提及 · ⏎ 发送", "/ commands · @ mention · ⏎ send")}</Muted>
        <button type="button" className="send" aria-label={tx("发送", "Send")} onClick={send} disabled={sending || (!text.trim() && !att.length)}>
          {sending ? <span className="v3-spinner spin" /> : <Icon name="up" size={16} />}
        </button>
      </div>
    </div>
  );
}

export function NextSteps({ items }: { items: { icon: IconName; label: string; onClick?: () => void }[] }) {
  return (
    <div className="v3-row" style={{ gap: 6, flexWrap: "wrap" }}>
      {items.map((s) => (
        <button key={s.label} type="button" className="next-step lift" onClick={s.onClick}>
          <Icon name={s.icon} size={12} />
          {s.label}
        </button>
      ))}
    </div>
  );
}

export { AiAvatar };
