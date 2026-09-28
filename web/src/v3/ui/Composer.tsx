import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { AiAvatar, Avatar, Badge, Brand, Icon, Kbd, Muted, Sev, Spacer, Tier, Tile, cx, type IconName } from "./core";
import { Popover, toast, useAnchoredPanel } from "./overlays";
import { Sparkline } from "../charts";
import { ModelPicker } from "./ModelPicker";
import { useI18n } from "../i18n";
import { useGet } from "../api/client";
import { addLink, addPaste, formatSize, uploadFile } from "../api/upload";
import type { CommandItem, MentionCandidate, MentionPreview, ModelChoice, RefKind, Upload, UploadKind } from "../api/types";
import "./composer.css";

export type Attachment = { kind: RefKind | "view" | "time" | "skill" | "template"; id: string; label: string };
export type SendExtra = { files: Upload[]; choice: ModelChoice | null };

type FileAtt = { key: string; kind: UploadKind; name: string; size: number; progress: number; upload?: Upload; error?: string };

const KIND_ICON: Partial<Record<Attachment["kind"], IconName>> = {
  service: "box", alert: "bell", runbook: "book", artifact: "file", incident: "alert", change: "branch", investigation: "branch", skill: "book", template: "grid", view: "grid", time: "clock",
};
const MENTION_TABS: { key: RefKind | "all"; zh: string; en: string }[] = [
  { key: "all", zh: "全部", en: "All" }, { key: "service", zh: "服务", en: "Services" }, { key: "alert", zh: "告警", en: "Alerts" }, { key: "incident", zh: "事件", en: "Incidents" },
  { key: "runbook", zh: "Runbook", en: "Runbooks" }, { key: "investigation", zh: "画布", en: "Canvases" }, { key: "person", zh: "人员", en: "People" }, { key: "artifact", zh: "产物", en: "Artifacts" },
];
const CMD_TABS: { key: CommandItem["group"] | "all"; zh: string; en: string }[] = [
  { key: "all", zh: "全部", en: "All" }, { key: "command", zh: "命令", en: "Commands" }, { key: "skill", zh: "Skills", en: "Skills" }, { key: "runbook", zh: "Runbook", en: "Runbooks" }, { key: "template", zh: "模板", en: "Templates" },
];
const LONG_PASTE = 2000;
const extOf = (name: string, kind: UploadKind) => (kind === "link" ? "URL" : kind === "paste" ? "TXT" : (name.split(".").pop() ?? "FILE").slice(0, 4).toUpperCase());

function MentionIcon({ m }: { m: MentionCandidate }) {
  if (m.kind === "person") return <Avatar name={m.label} size={24} />;
  if (m.kind === "alert") return <Sev p="P1" />;
  return <Tile icon={KIND_ICON[m.kind] ?? "file"} tone={m.kind === "incident" ? "danger" : m.kind === "runbook" ? "t1" : "neutral"} size={24} />;
}

function Preview({ m }: { m: MentionCandidate }) {
  const { tx } = useI18n();
  const { data: p } = useGet<MentionPreview>(`/mentions/${m.kind}/${encodeURIComponent(m.id)}/preview`, undefined, { staleTime: 60_000 });
  return (
    <div className="cm-preview anim-fade" key={m.id}>
      <div className="v3-row" style={{ gap: 10 }}>
        <MentionIcon m={m} />
        <div className="v3-col grow" style={{ gap: 1, minWidth: 0 }}>
          <div className="v3-row" style={{ gap: 6 }}>
            <b style={{ fontSize: "var(--fs-lg)" }}>{p?.title ?? m.label}</b>
            {p?.tier && <Tier tier={p.tier} />}
            {p?.status_label && <Badge tone={p.status}>{p.status_label}</Badge>}
          </div>
          <Muted size={11.5}>{p?.subtitle ?? m.meta}</Muted>
        </div>
      </div>
      {p && p.metrics.length > 0 && (
        <div className="cm-metrics">
          {p.metrics.map((x) => (
            <div key={x.label}>
              <Muted size={11}>{x.label}</Muted>
              <div className="v3-row" style={{ gap: 6 }}>
                <b style={{ color: x.tone ? `var(--${x.tone})` : undefined }}>{x.value}</b>
                <Spacer />
                <Sparkline values={x.series} width={64} height={18} color={x.tone ? `var(--${x.tone})` : undefined} />
              </div>
            </div>
          ))}
        </div>
      )}
      {p?.change && (
        <div className="v3-col" style={{ gap: 4 }}>
          <Muted size={11.5}>{tx("最近变更", "Recent change")}</Muted>
          <div className="v3-row" style={{ gap: 8, fontSize: "var(--fs-sm)" }}>
            <Brand name={p.change.brand} size={20} />
            <span className="grow ellipsis">{p.change.text}</span>
            <Muted size={11.5}>{p.change.meta}</Muted>
          </div>
        </div>
      )}
      {p && p.related.length > 0 && (
        <div className="v3-row" style={{ gap: 6, flexWrap: "wrap" }}>
          {p.related.map((r) => (
            <Badge key={r.label} tone={r.tone}>
              {r.label}
            </Badge>
          ))}
        </div>
      )}
      <Spacer />
      <Muted size={11.5}>{tx("⏎ 插入为上下文 · AI 会读取它的最新状态", "⏎ insert as context · the AI reads its latest state")}</Muted>
    </div>
  );
}

function CommandPreview({ c }: { c: CommandItem }) {
  const { tx } = useI18n();
  return (
    <div className="cm-preview anim-fade" key={c.id}>
      <div className="v3-row" style={{ gap: 8 }}>
        <b className="mono" style={{ fontSize: "var(--fs-lg)" }}>{c.name}</b>
        {c.creates === "canvas" && <Badge tone="info">{tx("创建画布", "Creates a canvas")}</Badge>}
        {c.badge && <Badge tone="accent">{c.badge}</Badge>}
      </div>
      <p style={{ fontSize: "var(--fs-sm)", lineHeight: 1.6 }}>{c.desc}</p>
      {c.params && (
        <table className="cm-params">
          <tbody>
            {c.params.map((p) => (
              <tr key={p.name}>
                <td>{p.name}</td>
                <td>{p.example}</td>
                <td>{p.required ? <Badge tone="warn">{tx("必填", "Required")}</Badge> : <Badge>{tx("可选", "Optional")}</Badge>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {c.usage && <pre className="v3-code">{c.usage}</pre>}
      {c.creates === "canvas" && <Muted size={11.5}>{tx("执行类步骤会生成提案，需要人工批准。", "Execution steps become proposals that need human approval.")}</Muted>}
    </div>
  );
}

/**
 * v4 composer: auto-growing text (minRows → maxRows, then scrolls), `@` context and `/` command panels
 * that open toward the side with room and span `spanRef`, a "+" attachment menu with drag-and-drop and
 * long-paste folding, an environment slot and the per-turn model picker.
 */
export function Composer({
  placeholder,
  onSend,
  attachments: initial = [],
  big,
  rows,
  minRows,
  maxRows = 12,
  autoFocus,
  footer,
  env,
  sending,
  onStop,
  model = true,
  turn,
  choice: controlled,
  onChoiceChange,
  viewAttachment,
  spanRef,
  compact,
}: {
  placeholder: string;
  onSend: (text: string, attachments: Attachment[], extra: SendExtra) => void;
  attachments?: Attachment[];
  big?: boolean;
  /** @deprecated use minRows */
  rows?: number;
  minRows?: number;
  maxRows?: number;
  autoFocus?: boolean;
  footer?: ReactNode;
  env?: ReactNode;
  sending?: boolean;
  onStop?: () => void;
  model?: boolean;
  /** Model of the reply that is currently streaming; a different selection is shown as "next turn". */
  turn?: ModelChoice | null;
  choice?: ModelChoice | null;
  onChoiceChange?: (c: ModelChoice) => void;
  /** Offered in the "+" menu as "reference the current view". */
  viewAttachment?: Attachment;
  /** Element whose width the @ and / panels span (e.g. the whole home hero). */
  spanRef?: RefObject<HTMLElement | null>;
  /** Narrow hosts (Ask AI card): @ and / show icons only, the model chip drops the vendor prefix. */
  compact?: boolean;
}) {
  const { tx, zh } = useI18n();
  const [text, setText] = useState("");
  const [att, setAtt] = useState<Attachment[]>(initial);
  const [files, setFiles] = useState<FileAtt[]>([]);
  const [sel, setSel] = useState(0);
  const [tab, setTab] = useState(0);
  const [plus, setPlus] = useState(false);
  const [linkMode, setLinkMode] = useState(false);
  const [link, setLink] = useState("");
  const [dragging, setDragging] = useState(false);
  const [ownChoice, setOwnChoice] = useState<ModelChoice | null>(null);
  const [closedFor, setClosedFor] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);
  const box = useRef<HTMLDivElement>(null);
  const plusBtn = useRef<HTMLButtonElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const imageInput = useRef<HTMLInputElement>(null);
  const choice = controlled !== undefined ? controlled : ownChoice;
  const setChoice = (c: ModelChoice) => (onChoiceChange ? onChoiceChange(c) : setOwnChoice(c));
  const lo = minRows ?? rows ?? (big ? 3 : 2);

  const rawToken = useMemo(() => {
    const m = text.match(/(^|\s)([/@])([^\s]*)$/);
    return m ? { kind: m[2] as "/" | "@", q: m[3] } : null;
  }, [text]);
  const token = rawToken && closedFor !== `${rawToken.kind}${rawToken.q}` ? rawToken : null;

  const mentions = useGet<{ items: MentionCandidate[] }>(token?.kind === "@" ? "/mentions" : null, { q: token?.q ?? "" }, { staleTime: 10_000 });
  const commands = useGet<{ items: CommandItem[] }>(token?.kind === "/" ? "/commands" : null, undefined, { staleTime: 5 * 60_000 });
  const menList = useMemo(() => {
    const k = MENTION_TABS[tab]?.key ?? "all";
    return (mentions.data?.items ?? []).filter((m) => k === "all" || m.kind === k || (k === "alert" && m.kind === "alert_group"));
  }, [mentions.data, tab]);
  const cmdList = useMemo(() => {
    const k = CMD_TABS[tab]?.key ?? "all";
    const q = (token?.q ?? "").toLowerCase();
    return (commands.data?.items ?? []).filter((c) => (k === "all" || c.group === k) && (!q || `${c.name} ${c.desc} ${c.id}`.toLowerCase().includes(q)));
  }, [commands.data, tab, token?.q]);
  const count = token?.kind === "/" ? cmdList.length : token?.kind === "@" ? menList.length : 0;
  const open = !!token;
  useEffect(() => setSel(0), [token?.kind, token?.q, tab]);
  useEffect(() => setTab(0), [token?.kind]);

  // Auto-grow between minRows and maxRows, then scroll inside the box.
  useLayoutEffect(() => {
    const ta = ref.current;
    if (!ta) return;
    const lh = parseFloat(getComputedStyle(ta).lineHeight) || 21;
    ta.style.height = "auto";
    const h = Math.min(Math.max(ta.scrollHeight, lo * lh), maxRows * lh);
    ta.style.height = `${h}px`;
    ta.style.overflowY = ta.scrollHeight > maxRows * lh + 1 ? "auto" : "hidden";
  }, [text, lo, maxRows]);

  const panelStyle = useAnchoredPanel(open, box.current, spanRef?.current ?? box.current, { maxHeight: 440 });
  const wide = typeof panelStyle.width === "number" && panelStyle.width >= 640;

  const replaceToken = (insert: string) => setText((t) => t.replace(/(^|\s)([/@])([^\s]*)$/, `$1${insert}`));
  const addRef = (a: Attachment) => setAtt((x) => (x.some((y) => y.id === a.id) ? x : [...x, a]));
  const choose = (i: number) => {
    if (token?.kind === "/") {
      const c = cmdList[i];
      if (!c) return;
      if (c.group === "command") replaceToken(`${c.name} `);
      else {
        replaceToken("");
        addRef({ kind: c.group === "runbook" ? "runbook" : c.group === "skill" ? "skill" : "template", id: c.id, label: c.name });
      }
    } else if (token?.kind === "@") {
      const m = menList[i];
      if (!m) return;
      replaceToken("");
      addRef({ kind: m.kind, id: m.id, label: m.label });
    }
    ref.current?.focus();
  };
  const trigger = (ch: "@" | "/") => {
    setClosedFor(null);
    setText((t) => (t && !/\s$/.test(t) ? `${t} ${ch}` : `${t}${ch}`));
    ref.current?.focus();
  };

  const startUpload = (file: File, kind: UploadKind) => {
    const key = `${file.name}-${Date.now()}-${Math.random()}`;
    setFiles((f) => [...f, { key, kind, name: file.name, size: file.size, progress: 0 }]);
    uploadFile(file, kind, (p) => setFiles((f) => f.map((x) => (x.key === key ? { ...x, progress: p } : x))))
      .then((u) => setFiles((f) => f.map((x) => (x.key === key ? { ...x, progress: 100, upload: u } : x))))
      .catch((e: Error) => {
        setFiles((f) => f.map((x) => (x.key === key ? { ...x, error: e.message === "too_large" ? tx("超过 20 MB", "Over 20 MB") : tx("上传失败", "Upload failed") } : x)));
      });
  };
  const addFiles = (list: FileList | File[] | null) => {
    for (const f of Array.from(list ?? [])) startUpload(f, f.type.startsWith("image/") ? "image" : "file");
  };
  const addLinkAtt = async () => {
    const url = link.trim();
    if (!url) return;
    setLink("");
    setLinkMode(false);
    setPlus(false);
    const key = `link-${Date.now()}`;
    setFiles((f) => [...f, { key, kind: "link", name: url, size: 0, progress: 50 }]);
    const u = await addLink(url);
    setFiles((f) => f.map((x) => (x.key === key ? { ...x, progress: 100, name: u.name, upload: u } : x)));
  };
  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const imgs = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith("image/"));
    if (imgs.length) {
      e.preventDefault();
      imgs.forEach((f) => startUpload(f, "image"));
      return;
    }
    const t = e.clipboardData.getData("text");
    if (t.length > LONG_PASTE) {
      e.preventDefault();
      const key = `paste-${Date.now()}`;
      const lines = t.split("\n").length;
      const name = tx("粘贴的文本 · {n} 行", "Pasted text · {n} lines", { n: lines });
      setFiles((f) => [...f, { key, kind: "paste", name, size: new Blob([t]).size, progress: 60 }]);
      void addPaste(t).then((u) => setFiles((f) => f.map((x) => (x.key === key ? { ...x, progress: 100, upload: { ...u, name } } : x))));
      toast(tx("超长粘贴已折叠为附件", "Long paste folded into an attachment"));
    }
  };

  const uploading = files.some((f) => !f.upload && !f.error);
  const canSend = !sending && !uploading && (!!text.trim() || att.length > 0 || files.some((f) => f.upload));
  const send = () => {
    if (!canSend) return;
    onSend(text.trim(), att, { files: files.filter((f) => f.upload).map((f) => f.upload!), choice });
    setText("");
    setFiles([]);
  };
  const onKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (token && count) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        setSel((s) => (s + (e.key === "ArrowDown" ? 1 : count - 1)) % count);
        return;
      }
      if (e.key === "Enter") {
        e.preventDefault();
        choose(sel);
        return;
      }
      if (e.key === "Tab") {
        e.preventDefault();
        const n = token.kind === "@" ? MENTION_TABS.length : CMD_TABS.length;
        setTab((t) => (t + (e.shiftKey ? n - 1 : 1)) % n);
        return;
      }
    }
    if (token && e.key === "Escape") {
      e.preventDefault();
      setClosedFor(`${token.kind}${token.q}`);
      return;
    }
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      send();
    }
  };

  const tabs = token?.kind === "@" ? MENTION_TABS : CMD_TABS;
  let lastGroup = "";
  const panel = open && (
    <div className="v3">
      <div className="composer-menu anim-pop" style={panelStyle} role="listbox" aria-label={tx("输入建议", "Suggestions")} onMouseDown={(e) => e.preventDefault()}>
        <div className="menu-head">
          {tabs.map((t, i) => (
            <button key={t.key} type="button" className={cx("cm-tab", i === tab && "on")} onClick={() => setTab(i)}>
              {zh ? t.zh : t.en}
            </button>
          ))}
          <span style={{ flexGrow: 1 }} />
          <Kbd>↑↓</Kbd>
          <Muted size={11.5}>{tx("选择", "select")}</Muted>
          <Kbd>⏎</Kbd>
          <Muted size={11.5}>{tx("插入", "insert")}</Muted>
          <Kbd>Tab</Kbd>
          <Muted size={11.5}>{tx("切换类型", "type")}</Muted>
          <Kbd>esc</Kbd>
        </div>
        <div className={cx("cm-body", wide && "wide")}>
          <ul className="cm-list">
            {count === 0 && <li className="cm-empty">{(token?.kind === "@" ? mentions.isLoading : commands.isLoading) ? tx("正在搜索…", "Searching…") : tx("没有匹配的结果", "No matches")}</li>}
            {token?.kind === "@" &&
              menList.map((m, i) => {
                const g = MENTION_TABS.find((t) => t.key === m.kind || (t.key === "alert" && m.kind === "alert_group"));
                const head = g && g.key !== lastGroup ? (zh ? g.zh : g.en) : null;
                if (g) lastGroup = g.key;
                return (
                  <li key={m.kind + m.id}>
                    {head && MENTION_TABS[tab].key === "all" && <div className="menu-group">{head}</div>}
                    <div role="option" aria-selected={i === sel} className={cx("opt", i === sel && "on")} onMouseDown={() => choose(i)} onMouseMove={() => setSel(i)}>
                      <MentionIcon m={m} />
                      <span className="grow ellipsis" style={{ fontSize: "var(--fs-sm)", fontWeight: i === sel ? 600 : 400 }}>{m.label}</span>
                      {m.tier && <Tier tier={m.tier} />}
                      {m.meta && <Muted size={11.5}>{m.meta}</Muted>}
                    </div>
                  </li>
                );
              })}
            {token?.kind === "/" &&
              cmdList.map((c, i) => {
                const g = CMD_TABS.find((t) => t.key === c.group);
                const head = g && g.key !== lastGroup ? (zh ? g.zh : g.en) : null;
                if (g) lastGroup = g.key;
                return (
                  <li key={c.id}>
                    {head && CMD_TABS[tab].key === "all" && <div className="menu-group">{head}</div>}
                    <div role="option" aria-selected={i === sel} className={cx("opt", i === sel && "on")} onMouseDown={() => choose(i)} onMouseMove={() => setSel(i)}>
                      <Tile icon={c.icon as IconName} tone={i === sel ? "accent" : c.group === "skill" || c.group === "runbook" ? "t1" : "neutral"} size={24} />
                      <span className="v3-col grow" style={{ gap: 0, minWidth: 0 }}>
                        <span className={cx(c.group === "command" && "mono")} style={{ fontSize: "var(--fs-sm)", fontWeight: i === sel ? 600 : 400 }}>{c.name}</span>
                        <span className="muted ellipsis" style={{ fontSize: "var(--fs-cap)" }}>{c.desc}</span>
                      </span>
                      {c.badge && <Badge tone="accent">{c.badge}</Badge>}
                    </div>
                  </li>
                );
              })}
          </ul>
          {wide && token?.kind === "@" && menList[sel] && <Preview m={menList[sel]} />}
          {wide && token?.kind === "/" && cmdList[sel] && <CommandPreview c={cmdList[sel]} />}
        </div>
      </div>
    </div>
  );

  return (
    <div
      ref={box}
      className={cx("v3-composer", big && "big", compact && "compact", dragging && "dragging")}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes("Files")) {
          e.preventDefault();
          setDragging(true);
        }
      }}
      onDragLeave={(e) => {
        if (!box.current?.contains(e.relatedTarget as Node)) setDragging(false);
      }}
      onDrop={(e) => {
        if (!e.dataTransfer.files.length) return;
        e.preventDefault();
        setDragging(false);
        addFiles(e.dataTransfer.files);
      }}
    >
      {(att.length > 0 || files.length > 0) && (
        <div className="composer-atts">
          {files.map((f) => (
            <span key={f.key} className={cx("att-file", f.error && "error")}>
              <span className={cx("att-ext", f.kind)}>{extOf(f.name, f.kind)}</span>
              <span className="v3-col" style={{ gap: 0, minWidth: 0 }}>
                <span className="ellipsis" style={{ fontSize: "var(--fs-sub)", fontWeight: 500 }}>{f.name}</span>
                <span className="muted" style={{ fontSize: "var(--fs-cap)" }}>
                  {f.error ?? (f.upload ? (f.kind === "link" ? tx("链接 · 发送时解析", "Link · resolved on send") : formatSize(f.size)) : tx("上传中 {p}%", "Uploading {p}%", { p: f.progress }))}
                </span>
              </span>
              <button type="button" aria-label={tx("移除 {n}", "Remove {n}", { n: f.name })} onClick={() => setFiles((x) => x.filter((y) => y.key !== f.key))}>
                <Icon name="x" size={12} />
              </button>
              {!f.upload && !f.error && <i className="att-progress" style={{ width: `${f.progress}%` }} />}
            </span>
          ))}
          {att.map((a) => (
            <span key={a.kind + a.id} className="att-ref">
              <Icon name={KIND_ICON[a.kind] ?? "at"} size={12} />
              {a.label}
              <button type="button" aria-label={tx("移除 {n}", "Remove {n}", { n: a.label })} onClick={() => setAtt((x) => x.filter((y) => y !== a))}>
                <Icon name="x" size={11} />
              </button>
            </span>
          ))}
        </div>
      )}
      <textarea
        ref={ref}
        rows={lo}
        value={text}
        autoFocus={autoFocus}
        placeholder={placeholder}
        aria-label={tx("向 AI 下指令", "Message the AI")}
        onChange={(e) => {
          setText(e.target.value);
          setClosedFor(null);
        }}
        onKeyDown={onKey}
        onPaste={onPaste}
      />
      <div className="composer-bar">
        <button ref={plusBtn} type="button" className={cx("cm-plus", plus && "on")} aria-label={tx("添加附件", "Add attachment")} aria-expanded={plus} onClick={() => setPlus((p) => !p)}>
          <Icon name="plus" size={15} />
        </button>
        <button type="button" className={cx("cm-trigger", token?.kind === "@" && "on")} onClick={() => trigger("@")} aria-label={tx("上下文", "Context")} title={compact ? tx("上下文 @", "Context @") : undefined}>
          <Icon name="at" size={13} />
          {!compact && tx("上下文", "Context")}
        </button>
        <button type="button" className={cx("cm-trigger", token?.kind === "/" && "on")} onClick={() => trigger("/")} aria-label={tx("命令", "Commands")} title={compact ? tx("命令 /", "Commands /") : undefined}>
          <Icon name="slash" size={13} />
          {!compact && tx("命令", "Commands")}
        </button>
        {footer}
        <span style={{ flexGrow: 1 }} />
        {env}
        {model && <ModelPicker value={choice} onChange={setChoice} turn={sending ? turn : null} compact={compact} />}
        {sending && onStop ? (
          <button type="button" className="send stop" aria-label={tx("停止生成", "Stop")} onClick={onStop}>
            <span />
          </button>
        ) : (
          <button type="button" className="send" aria-label={tx("发送", "Send")} onClick={send} disabled={!canSend}>
            {sending ? <span className="v3-spinner spin" /> : <Icon name="up" size={16} />}
          </button>
        )}
      </div>
      {dragging && (
        <div className="cm-drop anim-fade">
          <Icon name="upload" size={22} />
          <b>{tx("松开以添加附件", "Drop to attach")}</b>
          <Muted size={11.5}>{tx("日志、配置、截图、CSV、PDF、Markdown · 单个 ≤ 20 MB", "Logs, configs, screenshots, CSV, PDF, Markdown · up to 20 MB each")}</Muted>
        </div>
      )}
      <input ref={fileInput} type="file" multiple hidden accept=".log,.txt,.yaml,.yml,.json,.csv,.pdf,.md,.conf,.toml,.png,.jpg,.jpeg" onChange={(e) => { addFiles(e.target.files); e.target.value = ""; setPlus(false); }} />
      <input ref={imageInput} type="file" multiple hidden accept="image/*" onChange={(e) => { addFiles(e.target.files); e.target.value = ""; setPlus(false); }} />
      <Popover anchor={plusBtn.current} open={plus} onClose={() => { setPlus(false); setLinkMode(false); }} place="bottom-start" style={{ width: 340, padding: 0 }}>
        <div className="cm-plusmenu">
          {linkMode ? (
            <form className="cm-linkform" onSubmit={(e) => { e.preventDefault(); void addLinkAtt(); }}>
              <Icon name="link" size={14} />
              <input autoFocus value={link} onChange={(e) => setLink(e.target.value)} placeholder="https://grafana.example.internal/d/…" aria-label={tx("链接", "Link")} />
              <button type="submit" className="v3-link">{tx("添加", "Add")}</button>
            </form>
          ) : (
            <>
              <PlusItem icon="upload" tone="accent" title={tx("上传文件", "Upload files")} sub={tx("日志、配置、CSV、PDF、Markdown · 单个 ≤ 20 MB", "Logs, configs, CSV, PDF, Markdown · ≤ 20 MB each")} k="⌘U" onClick={() => fileInput.current?.click()} />
              <PlusItem icon="file" title={tx("图片或截图", "Image or screenshot")} sub={tx("也可以直接粘贴", "Or paste it directly")} k="⌘V" onClick={() => imageInput.current?.click()} />
              <PlusItem icon="link" title={tx("从链接添加", "Add from link")} sub={tx("自动识别 Grafana、Kibana、GitLab 链接", "Grafana, Kibana and GitLab links are recognised")} onClick={() => setLinkMode(true)} />
              <PlusItem
                icon="grid"
                title={tx("引用当前视图", "Reference current view")}
                sub={viewAttachment ? viewAttachment.label : tx("在指标、链路、日志页面可用", "Available on metrics, traces and logs")}
                disabled={!viewAttachment}
                onClick={() => { if (viewAttachment) addRef(viewAttachment); setPlus(false); }}
              />
            </>
          )}
          <div className="cm-plusnote">
            <Icon name="lock" size={13} />
            <span>{tx("附件只作为数据：不会执行其中的指令，预览在沙箱中进行，上传写入审计。", "Attachments are data only: never executed, previewed in a sandbox, uploads audited.")}</span>
          </div>
        </div>
      </Popover>
      {panel && createPortal(panel, document.body)}
    </div>
  );
}

function PlusItem({ icon, tone = "neutral", title, sub, k, disabled, onClick }: { icon: IconName; tone?: "accent" | "neutral"; title: string; sub: string; k?: string; disabled?: boolean; onClick: () => void }) {
  return (
    <button type="button" className="cm-plusitem" disabled={disabled} onClick={onClick}>
      <Tile icon={icon} tone={tone} size={28} />
      <span className="v3-col grow" style={{ gap: 0, minWidth: 0 }}>
        <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{title}</span>
        <span className="muted" style={{ fontSize: "var(--fs-sub)" }}>{sub}</span>
      </span>
      {k && <Kbd>{k}</Kbd>}
    </button>
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
