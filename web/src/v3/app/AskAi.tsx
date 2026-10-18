import { useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { ApiError, api } from "../api/client";
import { applyEvent, sendAndStream } from "../api/stream";
import type { Message, ModelChoice } from "../api/types";
import { AiAvatar, Brand, IconBtn, Muted, Strong, cx } from "../ui/core";
import { BrandMark } from "../ui/BrandMark";
import { Composer, NextSteps, type SendExtra } from "../ui/Composer";
import { PartView } from "../features/conversation/Parts";
import { useI18n } from "../i18n";
import { Gated, useNeed } from "../ui/permission";
import { toast } from "../ui/overlays";
import { useAskAi } from "./askState";
import { locate } from "./nav";
import "./ask-ai.css";

export { useAskAi } from "./askState";

/**
 * "Ask AI" floating card available on every page. It carries the current view as context and answers in
 * place, without dimming the page, so the user can keep looking at what they ask about.
 * Same card treatment as floating detail panels; wide screens place a detail card left of it.
 */
export function AskAiCard() {
  const { isOpen, setOpen, draft } = useAskAi();
  const { tx, tl } = useI18n();
  const askBlocked = useNeed(["conversation", "change"]);
  const nav = useNavigate();
  const { pathname } = useLocation();
  const where = locate(pathname);
  const view = where.child ? tl(where.child.label) : where.section ? tl(where.section.label) : tx("当前页面", "This page");
  const [convId, setConvId] = useState<string | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [sending, setSending] = useState(false);
  const [turn, setTurn] = useState<ModelChoice | null>(null);
  const [epoch, setEpoch] = useState(0);
  const stop = useRef<(() => void) | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const card = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!isOpen) return;
    const h = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || document.querySelector(".v3-popover, .v3-dialog")) return;
      const inside = e.target instanceof Element && !!e.target.closest(".ask-card");
      // Esc outside the card first closes a floating detail card, if one is open.
      if (inside || !document.querySelector(".dp-float")) setOpen(false);
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [isOpen, setOpen]);

  useEffect(() => {
    scroller.current?.scrollTo({ top: scroller.current.scrollHeight });
  }, [messages]);

  const reset = () => {
    stop.current?.();
    stop.current = null;
    setConvId(null);
    setMessages([]);
    setSending(false);
    setTurn(null);
    setEpoch((n) => n + 1);
  };

  const ask = async (text: string, x: SendExtra = { files: [], choice: null }) => {
    // The suggestions below go through here too, so they honour the same grant as the composer (PA-225).
    if (!text.trim() || sending || askBlocked) return;
    if (draft) useAskAi.setState({ draft: "" });
    setSending(true);
    setTurn(x.choice);
    // A rejected request or a stream that gave up says so instead of "generating" forever (PA-225).
    const fail = (msg: string) => {
      stop.current = null;
      setSending(false);
      setTurn(null);
      setMessages((ms) => ms.map((m) => (m.streaming ? { ...m, streaming: false } : m)));
      toast(msg, { tone: "danger" });
    };
    try {
      let id = convId;
      if (!id) {
        id = (await api<{ id: string }>("POST", "/conversations", { body: { text, context: pathname, model: x.choice?.model } })).id;
        setConvId(id);
      }
      stop.current = await sendAndStream(
        id,
        text,
        (e) => {
          setMessages((ms) => applyEvent(ms, e));
          if (e.type === "message.completed") {
            setSending(false);
            setTurn(null);
            stop.current = null;
          }
        },
        { choice: x.choice, attachments: x.files, onError: () => fail(tx("回复中断了，请稍后重试", "The reply was interrupted; try again later")) },
      );
    } catch (e) {
      fail(e instanceof ApiError ? e.message : tx("这条消息没有发出去，请稍后重试", "The message wasn't sent; try again later"));
    }
  };

  const openFull = () => {
    if (!convId) return;
    setOpen(false);
    nav(`/conversations/${convId}`);
  };

  if (!isOpen) return null;
  return (
    <aside ref={card} className="ask-card" aria-label={tx("问 AI", "Ask AI")}>
      <header className="ask-head">
        <AiAvatar size={28} />
        <div className="v3-col grow" style={{ gap: 0, minWidth: 0 }}>
          <Strong>{tx("问 AI", "Ask AI")}</Strong>
          <Muted size={11.5} style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {tx("会带上当前视图：{v}", "Includes the current view: {v}", { v: view })}
          </Muted>
        </div>
        <IconBtn icon="external" label={convId ? tx("在对话中打开", "Open as conversation") : tx("提问后可在对话中打开", "Ask something first to open it as a conversation")} size={28} disabled={!convId} onClick={openFull} />
        <IconBtn icon="edit" label={tx("新问题", "New question")} size={28} disabled={!messages.length} onClick={reset} />
        <IconBtn icon="x" label={tx("关闭 (Esc)", "Close (Esc)")} size={28} onClick={() => setOpen(false)} />
      </header>
      <div className="ask-body" ref={scroller}>
        {messages.length === 0 ? (
          <div className="v3-col" style={{ gap: 12 }}>
            <Strong>{tx("试试这样问", "Try asking")}</Strong>
            {/* A fieldset disables every suggestion at once; Gated names the missing grant. */}
            <Gated reason={askBlocked}>
              <fieldset style={{ border: 0, padding: 0, margin: 0, minWidth: 0 }}>
            <NextSteps
              items={[
                { icon: "activity", label: tx("解释这页的异常", "Explain what looks wrong here"), onClick: () => void ask(tx("解释这页的异常", "Explain what looks wrong here")) },
                { icon: "file", label: tx("总结最近 1 小时", "Summarize the last hour"), onClick: () => void ask(tx("总结最近 1 小时", "Summarize the last hour")) },
                { icon: "branch", label: tx("发起调查", "Start an investigation"), onClick: () => void ask(tx("基于当前视图发起调查", "Investigate based on this view")) },
              ]}
            />
              </fieldset>
            </Gated>
            <Muted size={11.5}>{tx("AI 只读取证；任何写操作都会生成提案，经人工批准后执行。", "The AI only reads. Any write becomes a proposal that a person approves.")}</Muted>
          </div>
        ) : (
          messages.map((m) =>
            m.role === "user" ? (
              <div key={m.id} className="ask-me anim-rise">
                {m.parts.map((p, i) => (p.type === "text" ? <span key={i}>{p.markdown}</span> : null))}
              </div>
            ) : (
              <div key={m.id} className="ask-ai anim-rise">
                <div className="ask-by">
                  AI
                  {m.run && (
                    <span className="v3-row" style={{ gap: 4 }}>
                      · {m.run.brand && <Brand name={m.run.brand} size={13} />}
                      {m.run.model}
                      {m.run.reasoning_label && ` · ${m.run.reasoning_label}`}
                    </span>
                  )}
                  {m.streaming && <BrandMark size={14} state="thinking" label={tx("生成中", "Generating")} />}
                </div>
                {m.parts.map((p, i) => (
                  <PartView key={i} part={p} streamingText={m.streaming && i === m.parts.length - 1} streaming={m.streaming} actions={{ onStep: () => openFull() }} />
                ))}
              </div>
            ),
          )
        )}
      </div>
      <div className="ask-foot">
        <Composer
          key={`${draft}:${epoch}:${view}`}
          compact
          blocked={askBlocked}
          placeholder={draft || (messages.length ? tx("继续追问…", "Follow up…") : tx("问一个问题…", "Ask a question…"))}
          autoFocus
          sending={sending}
          turn={turn}
          onStop={() => {
            stop.current?.();
            stop.current = null;
            setSending(false);
            setMessages((ms) => ms.map((m) => (m.streaming ? { ...m, streaming: false } : m)));
          }}
          attachments={[{ kind: "view", id: "view", label: view }]}
          onSend={(t, _att, x) => void ask(t || draft, x)}
        />
      </div>
    </aside>
  );
}

/** Shell class while the card is open, so floating detail cards can sit beside it on wide screens. */
export function useAskOpenClass() {
  return cx(useAskAi((s) => s.isOpen) && "ask-open");
}
