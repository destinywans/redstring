import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Page } from "../../app/Shell";
import { api, useGet, API_MODE } from "../../api/client";
import { sendAndStream } from "../../api/stream";
import type { Artifact, Conversation, Message, Part, StreamEvent } from "../../api/types";
import { AiAvatar, Avatar, Badge, Btn, Col, Divider, Dot, Env, Icon, Muted, Row, Segmented, Skeleton, Spacer, Strong, Tile, TextLink, Mono, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Composer } from "../../ui/Composer";
import { PartView, type PartActions } from "./Parts";
import { ArtifactPanel, ARTIFACT_ICON } from "./ArtifactPanel";
import { useI18n } from "../../i18n";
import "./conversation.css";

function applyEvent(messages: Message[], e: StreamEvent): Message[] {
  switch (e.type) {
    case "message.started":
      return messages.some((m) => m.id === e.message.id) ? messages : [...messages, e.message];
    case "part.added":
      return messages.map((m) => {
        if (m.id !== e.message_id) return m;
        const i = m.parts.findIndex((p) => p.type === e.part.type && p.type === "tool_calls");
        if (i >= 0) return { ...m, parts: m.parts.map((p, j) => (j === i ? e.part : p)) };
        return { ...m, parts: [...m.parts, e.part] };
      });
    case "text.delta":
      return messages.map((m) =>
        m.id !== e.message_id ? m : { ...m, parts: m.parts.map((p, j) => (j === e.index && p.type === "text" ? { ...p, markdown: p.markdown + e.delta } : p)) },
      );
    case "message.completed":
      return messages.map((m) => (m.id === e.message_id ? { ...m, streaming: false, run: e.run } : m));
  }
}

function MessageView({ m, conv, actions, onOpenArtifact }: { m: Message; conv: Conversation; actions: PartActions; onOpenArtifact: (id: string) => void }) {
  const { tx, fmt } = useI18n();
  const user = m.role === "user";
  const lastText = [...m.parts].reverse().find((p) => p.type === "text");
  return (
    <div className={cx("msg anim-rise", user && "user")}>
      {user ? <Avatar name={m.author} size={28} /> : <AiAvatar size={28} />}
      <div className="v3-col grow" style={{ gap: 10 }}>
        <Row gap={8} style={{ fontSize: 12 }}>
          <b style={{ fontWeight: 600 }}>{user ? m.author : "AI"}</b>
          <Muted>
            {fmt.time(m.created_at)}
            {m.run && ` · ${m.run.agent} ${m.run.version} · ${m.run.model}`}
          </Muted>
          {m.streaming && (
            <Badge tone="accent">
              <span className="v3-spinner spin" />
              {tx("生成中", "Generating")}
            </Badge>
          )}
        </Row>
        {m.parts.map((p: Part, i) =>
          p.type === "artifacts" ? (
            <div key={i} className="v3-col" style={{ gap: 8 }}>
              {p.ids.map((id) => {
                const a = conv.artifacts.find((x) => x.id === id);
                if (!a) return null;
                return (
                  <button key={id} type="button" className="art-chip lift" onClick={() => onOpenArtifact(id)}>
                    <Tile icon={ARTIFACT_ICON[a.kind]} tone="accent" size={26} />
                    <span className="v3-col grow" style={{ gap: 0, textAlign: "left" }}>
                      <span style={{ fontSize: 12.5, fontWeight: 500 }}>{a.title}</span>
                      <Muted size={11}>{a.meta}</Muted>
                    </span>
                    <Icon name="panel" size={14} color="var(--muted)" />
                  </button>
                );
              })}
            </div>
          ) : (
            <PartView key={i} part={p} actions={actions} streamingText={m.streaming && p === lastText} />
          ),
        )}
      </div>
    </div>
  );
}

function SidePanel({ conv }: { conv: Conversation }) {
  const { tx } = useI18n();
  const s = conv.side;
  const pct = Math.round((100 * s.context.used) / s.context.budget);
  return (
    <aside className="conv-side">
      <Row gap={8}>
        <Strong size={13}>{tx("本次对话", "This conversation")}</Strong>
        <Spacer />
        <TextLink to="/agents/runs/run_7f3a">{tx("Agent 透视", "Agent view")}</TextLink>
      </Row>
      <Col gap={6}>
        <Muted size={11.5}>{tx("作用域", "Scope")}</Muted>
        <Row gap={6} wrap>
          {conv.scope.service && <Badge>{conv.scope.service}</Badge>}
          {conv.scope.env && <Env env={conv.scope.env} />}
        </Row>
        {conv.scope.inferred && <Muted>{tx("由 AI 从问题推断，你已确认", "Inferred by the AI and confirmed by you")}</Muted>}
      </Col>
      <Col gap={6}>
        <Muted size={11.5}>{tx("本轮用到的连接器", "Connectors used")}</Muted>
        {s.connectors.map((c) => (
          <Row key={c.name} gap={8} style={{ fontSize: 12.5 }}>
            <Dot tone="ok" size={7} />
            <span className="grow">{c.name}</span>
            <Muted>{tx("{n} 次", "{n}×", { n: c.calls })}</Muted>
          </Row>
        ))}
      </Col>
      <Col gap={6}>
        <Muted size={11.5}>{tx("上下文", "Context")}</Muted>
        <div className="ctx-bar">
          {s.context.parts.map((p) => (
            <span key={p.label} title={`${p.label} · ${(p.tokens / 1000).toFixed(1)}k`} style={{ width: `${(100 * p.tokens) / s.context.budget}%`, background: p.color }} />
          ))}
        </div>
        <Row gap={10} wrap style={{ fontSize: 11 }}>
          {s.context.parts.map((p) => (
            <span key={p.label} className="v3-row muted" style={{ gap: 4 }}>
              <i style={{ width: 7, height: 7, borderRadius: 2, background: p.color }} />
              {p.label}
            </span>
          ))}
        </Row>
        <Muted>
          {(s.context.used / 1000).toFixed(1)}k / {s.context.budget / 1000}k tokens · {pct}%
        </Muted>
      </Col>
      <Col gap={6}>
        <Muted size={11.5}>{tx("记忆召回", "Recalled memory")}</Muted>
        {s.recalled.map((r) => (
          <Row key={r.title} gap={8} style={{ fontSize: 12.5 }}>
            <Icon name={r.kind === "runbook" ? "book" : "file"} size={14} color="var(--muted)" />
            <span className="grow">{r.title}</span>
            <Mono>{r.score.toFixed(2)}</Mono>
          </Row>
        ))}
      </Col>
      <Divider />
      <Col gap={8}>
        <Muted size={11.5}>{tx("下一步", "Next")}</Muted>
        <Btn icon="branch" to={`/canvas/${conv.investigation_id ?? "INV-231"}`} style={{ justifyContent: "flex-start" }}>
          {tx("转为 Canvas 调查", "Open as canvas investigation")}
        </Btn>
        <Btn icon="book" style={{ justifyContent: "flex-start" }} onClick={() => toast(tx("已保存为 Runbook 草稿", "Saved as a runbook draft"))}>
          {tx("保存为 Runbook 草稿", "Save as runbook draft")}
        </Btn>
        <Btn icon="clock" style={{ justifyContent: "flex-start" }} onClick={() => toast(tx("已创建定时复查：每天 09:00", "Scheduled a daily check at 09:00"))}>
          {tx("每天 09:00 复查一次", "Re-check daily at 09:00")}
        </Btn>
      </Col>
    </aside>
  );
}

export default function ConversationPage() {
  const { id = "" } = useParams();
  const { tx } = useI18n();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const { data: conv, isLoading, error } = useGet<Conversation>(`/conversations/${id}`);
  const [messages, setMessages] = useState<Message[]>([]);
  const [sending, setSending] = useState(false);
  const stop = useRef<(() => void) | null>(null);
  const bottom = useRef<HTMLDivElement>(null);
  const artifact = params.get("artifact");

  useEffect(() => {
    if (conv) setMessages(conv.messages);
  }, [conv]);
  useEffect(() => () => stop.current?.(), []);
  useEffect(() => {
    bottom.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [messages]);

  const send = useCallback(
    async (text: string) => {
      if (!text || sending) return;
      setSending(true);
      stop.current = await sendAndStream(id, text, (e) => {
        setMessages((ms) => applyEvent(ms, e));
        if (e.type === "message.completed") {
          setSending(false);
          qc.invalidateQueries({ predicate: (q) => q.queryKey[0] === "/conversations" });
        }
      });
    },
    [id, sending, qc],
  );

  useEffect(() => {
    const q = params.get("send");
    if (conv && q) {
      const next = new URLSearchParams(params);
      next.delete("send");
      setParams(next, { replace: true });
      void send(q);
    }
  }, [conv]); // eslint-disable-line react-hooks/exhaustive-deps

  const openArtifact = (aid: string) => {
    const next = new URLSearchParams(params);
    next.set("artifact", aid);
    setParams(next, { replace: true });
  };
  const actions: PartActions = {
    openArtifact: (a) => {
      const match = conv?.artifacts.find((x) => x.kind === a.kind);
      if (match) openArtifact(match.id);
      else toast(tx("已在产物面板中生成：{t}", "Created in the artifact panel: {t}", { t: a.title ?? "" }));
    },
    onStep: (action, label) => {
      if (action === "proposal") nav("/review/PRP-88");
      else if (action === "canvas") nav(`/canvas/${conv?.investigation_id ?? "INV-231"}`);
      else if (action === "ask") void send(label);
      else toast(label);
    },
  };

  if (error) return <Page crumbs={[tx("对话与调查", "Conversations")]}>{String(error)}</Page>;
  return (
    <Page
      flush
      crumbs={[tx("对话与调查", "Conversations"), conv?.title ?? "…"]}
      actions={
        <>
          <Segmented
            label={tx("视图", "View")}
            value="chat"
            onChange={(v) => v === "canvas" && nav(`/canvas/${conv?.investigation_id ?? "INV-231"}`)}
            options={[
              { value: "chat", label: tx("对话", "Chat"), icon: "message" },
              { value: "canvas", label: "Canvas", icon: "branch" },
            ]}
          />
          {conv?.scope.service && (
            <Badge>
              <Icon name="box" size={12} />
              {conv.scope.service}
            </Badge>
          )}
          {conv?.scope.env && <Env env={conv.scope.env} />}
          {conv && conv.artifacts.length > 0 && !artifact && (
            <Btn kind="ghost" icon="panel" onClick={() => openArtifact(conv.artifacts[0].id)}>
              {tx("产物 {n}", "Artifacts {n}", { n: conv.artifacts.length })}
            </Btn>
          )}
          <Btn kind="ghost" onClick={() => { void navigator.clipboard?.writeText(location.href); toast(tx("链接已复制", "Link copied")); }}>
            {tx("分享", "Share")}
          </Btn>
        </>
      }
    >
      <div className="conv-layout">
        <div className={cx("conv-main", artifact && "with-panel")}>
          <div className="conv-scroll">
            <div className="conv-thread">
              {isLoading && (
                <>
                  <Skeleton h={40} />
                  <Skeleton h={180} />
                  <Skeleton h={120} />
                </>
              )}
              {conv && messages.length === 0 && !sending && (
                <div className="v3-col anim-fade" style={{ alignItems: "center", gap: 8, padding: 60 }}>
                  <AiAvatar size={36} />
                  <Strong size={14}>{tx("有什么可以帮你？", "How can I help?")}</Strong>
                  <Muted>{tx("可以直接提问，也可以用 / 命令或 @ 引用服务、告警与 Runbook。", "Ask directly, or use / commands and @ to reference services, alerts and runbooks.")}</Muted>
                </div>
              )}
              {conv && messages.map((m) => <MessageView key={m.id} m={m} conv={conv} actions={actions} onOpenArtifact={openArtifact} />)}
              <div ref={bottom} />
            </div>
          </div>
          <div className="conv-composer">
            <Composer
              placeholder={tx("继续提问，或让 AI 执行一个动作…", "Ask a follow-up, or ask the AI to take an action…")}
              sending={sending}
              attachments={conv?.scope.service ? [{ kind: "service", id: conv.scope.service, label: conv.scope.service }] : []}
              onSend={(t, att) => send(t || att.map((a) => a.label).join(" "))}
            />
            <p className="hint">
              {tx("⏎ 发送 · Shift + ⏎ 换行 · 写操作一律经过人工批准", "⏎ send · Shift + ⏎ new line · every write needs human approval")}
              {API_MODE === "mock" && ` · ${tx("示例数据", "Sample data")}`}
            </p>
          </div>
        </div>
        {conv && artifact ? (
          <ArtifactPanel
            artifacts={conv.artifacts as Artifact[]}
            active={artifact}
            onSelect={openArtifact}
            onClose={() => {
              const next = new URLSearchParams(params);
              next.delete("artifact");
              setParams(next, { replace: true });
            }}
          />
        ) : (
          conv && <SidePanel conv={conv} />
        )}
      </div>
    </Page>
  );
}

/** /conversations/new?q=… creates a conversation and immediately streams the first answer. */
export function NewConversationPage() {
  const [params] = useSearchParams();
  const nav = useNavigate();
  const { tx } = useI18n();
  const q = params.get("q");
  const started = useRef(false);
  useEffect(() => {
    if (!q || started.current) return;
    started.current = true;
    void api<{ id: string }>("POST", "/conversations", { body: { text: q, context: params.get("context") } }).then(({ id }) =>
      nav(`/conversations/${id}?send=${encodeURIComponent(q)}`, { replace: true }),
    );
  }, [q, nav, params]);
  if (q)
    return (
      <Page crumbs={[tx("对话与调查", "Conversations"), tx("新对话", "New")]}>
        <div className="v3-col" style={{ alignItems: "center", padding: 80, gap: 10 }}>
          <span className="v3-spinner spin" style={{ width: 18, height: 18, color: "var(--accent)" }} />
          <Muted>{tx("正在创建对话…", "Creating the conversation…")}</Muted>
        </div>
      </Page>
    );
  return (
    <Page crumbs={[tx("对话与调查", "Conversations"), tx("新对话", "New")]}>
      <div className="v3-col" style={{ alignItems: "center", padding: "80px 24px", gap: 16 }}>
        <AiAvatar size={40} />
        <h1 style={{ fontSize: 20, fontWeight: 600 }}>{tx("开始一个新对话", "Start a new conversation")}</h1>
        <div style={{ width: "min(720px, 100%)" }}>
          <Composer big autoFocus placeholder={tx("描述问题、下指令，或用 / 选择命令…", "Describe a problem, give an instruction, or pick a / command…")} onSend={(t) => nav(`/conversations/new?q=${encodeURIComponent(t)}`)} />
        </div>
      </div>
    </Page>
  );
}
