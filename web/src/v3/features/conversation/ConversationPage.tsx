import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Page } from "../../app/Shell";
import { api, useGet, API_MODE, ApiError } from "../../api/client";
import { applyEvent, newConversationUrl, sendAndStream, takeAttachments } from "../../api/stream";
import { formatSize } from "../../api/upload";
import type { Artifact, Conversation, Message, ModelChoice, Part, StreamEvent, Upload } from "../../api/types";
import { AiAvatar, Avatar, Badge, Brand, Btn, Col, Divider, Dot, Env, Icon, Muted, Row, Segmented, Skeleton, Spacer, Strong, Tile, TextLink, Mono, cx } from "../../ui/core";
import { BrandMark } from "../../ui/BrandMark";
import { toast } from "../../ui/overlays";
import { UnknownValue } from "../../ui/unknown";
import { Composer } from "../../ui/Composer";
import { useModelCatalog, describeChoice } from "../../ui/ModelPicker";
import { PartView, type PartActions } from "./Parts";
import { ArtifactPanel, ARTIFACT_ICON } from "./ArtifactPanel";
import { useI18n } from "../../i18n";
import { Gated, useNeed } from "../../ui/permission";
import { useRollbackDrawer } from "../events/RollbackProposal";
import { useNames } from "../../ui/names";
import "./conversation.css";

function MessageView({ m, conv, actions, onOpenArtifact }: { m: Message; conv: Conversation; actions: PartActions; onOpenArtifact: (id: string) => void }) {
  const { tx, fmt } = useI18n();
  const user = m.role === "user";
  const lastText = [...m.parts].reverse().find((p) => p.type === "text");
  return (
    <div className={cx("msg anim-rise", user && "user")}>
      {user ? <Avatar name={m.author} size={28} /> : <AiAvatar size={28} />}
      <div className="v3-col grow" style={{ gap: 10 }}>
        <Row gap={8} style={{ fontSize: "var(--fs-sub)" }}>
          <b style={{ fontWeight: 600 }}>{user ? m.author : "AI"}</b>
          <Muted>
            {fmt.time(m.created_at)}
            {m.run && ` · ${m.run.agent} ${m.run.version}`}
          </Muted>
          {m.run && (
            <span className="v3-row muted" style={{ gap: 4, fontSize: "var(--fs-sub)" }} title={tx("本轮使用的模型与推理强度", "Model and reasoning effort used for this turn")}>
              {m.run.brand && <Brand name={m.run.brand} size={14} />}
              {m.run.model}
              {m.run.reasoning_label && ` · ${m.run.reasoning_label}`}
            </span>
          )}
          {m.streaming && (
            <span className="rs-working">
              <BrandMark size={16} state="thinking" />
              {tx("生成中", "Generating")}
            </span>
          )}
        </Row>
        {m.attachments && m.attachments.length > 0 && <UploadChips files={m.attachments} />}
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
                      <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{a.title}</span>
                      <Muted size={11}>{a.meta}</Muted>
                    </span>
                    <Icon name="panel" size={14} color="var(--muted)" />
                  </button>
                );
              })}
            </div>
          ) : (
            <PartView key={i} part={p} actions={actions} streamingText={m.streaming && p === lastText} streaming={m.streaming} />
          ),
        )}
      </div>
    </div>
  );
}

function UploadChips({ files }: { files: Upload[] }) {
  const { tx } = useI18n();
  return (
    <div className="v3-row" style={{ gap: 6, flexWrap: "wrap" }}>
      {files.map((f) => (
        <span key={f.id} className="att-file">
          <span className={cx("att-ext", f.kind)}>{f.kind === "link" ? "URL" : f.kind === "paste" ? "TXT" : (f.name.split(".").pop() ?? "").slice(0, 4).toUpperCase()}</span>
          <span className="v3-col" style={{ gap: 0, minWidth: 0 }}>
            <span className="ellipsis" style={{ fontSize: "var(--fs-sub)", fontWeight: 500 }}>{f.name}</span>
            <span className="muted" style={{ fontSize: "var(--fs-cap)" }}>{f.kind === "link" ? tx("链接", "Link") : formatSize(f.size)}</span>
          </span>
        </span>
      ))}
    </div>
  );
}

/** Shown between assistant turns whose model or effort differ. */
function TurnDivider({ run }: { run: NonNullable<Message["run"]> }) {
  const { tx } = useI18n();
  const label = run.reasoning_label ? `${run.model} · ${run.reasoning_label}` : run.model;
  return (
    <div className="turn-divider" role="separator">
      {run.brand && <Brand name={run.brand} size={14} />}
      {tx("本轮起切换为 {m}", "Switched to {m} from this turn", { m: label })}
    </div>
  );
}

const fmtTokens = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n));

/**
 * Where "open as canvas" goes: the linked investigation, or (real mode) a new investigation canvas scoped to the
 * conversation's service. Only sample data falls back to the sample investigation.
 */
const canvasHref = (conv?: Conversation) =>
  conv?.investigation_id
    ? `/canvas/${conv.investigation_id}`
    : API_MODE === "mock"
      ? "/canvas/INV-231"
      : `/canvases?new=investigation${conv?.scope.service ? `&service=${encodeURIComponent(conv.scope.service)}` : ""}`;

function SidePanel({ conv, service }: { conv: Conversation; service: string | null }) {
  const { tx } = useI18n();
  const canvasBlocked = useNeed(["canvas", "change"]);
  const s = conv.side;
  const { used, budget } = s.context;
  const pct = used !== null && budget ? Math.round((100 * used) / budget) : null;
  // The server labels the measured part in Chinese only.
  const partLabel = (l: string) => (l === "模型输入" ? tx("模型输入", "Model input") : l);
  return (
    <aside className="conv-side">
      <Row gap={8}>
        <Strong size={13}>{tx("本次对话", "This conversation")}</Strong>
        <Spacer />
        {/* Chat turns don't record agent runs yet, so only the sample data has a run to open. */}
        {API_MODE === "mock" && <TextLink to="/agents/runs/run_7f3a">{tx("Agent 透视", "Agent view")}</TextLink>}
      </Row>
      <Col gap={6}>
        <Muted size={11.5}>{tx("作用域", "Scope")}</Muted>
        <Row gap={6} wrap>
          {service && <Badge>{service}</Badge>}
          {conv.scope.env && <Env env={conv.scope.env} />}
        </Row>
        {conv.scope.inferred && <Muted>{tx("由 AI 从问题推断，你已确认", "Inferred by the AI and confirmed by you")}</Muted>}
      </Col>
      <Col gap={6}>
        <Muted size={11.5}>{tx("本轮用到的连接器", "Connectors used")}</Muted>
        {s.connectors.map((c) => (
          <Row key={c.name} gap={8} style={{ fontSize: "var(--fs-sm)" }}>
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
            <span key={p.label} title={`${partLabel(p.label)} · ${fmtTokens(p.tokens)}`} style={{ width: budget ? `${(100 * p.tokens) / budget}%` : 0, background: p.color }} />
          ))}
        </div>
        <Row gap={10} wrap style={{ fontSize: "var(--fs-cap)" }}>
          {s.context.parts.map((p) => (
            <span key={p.label} className="v3-row muted" style={{ gap: 4 }}>
              <i style={{ width: 7, height: 7, borderRadius: 2, background: p.color }} />
              {partLabel(p.label)}
            </span>
          ))}
        </Row>
        <Muted>
          {used !== null ? fmtTokens(used) : <UnknownValue reason="not_generated" label={tx("已用 —", "used —")} />} / {budget ? fmtTokens(budget) : <UnknownValue reason="not_tracked" label={tx("预算 —", "budget —")} />} tokens
          {pct !== null && ` · ${pct}%`}
        </Muted>
      </Col>
      <Col gap={6}>
        <Muted size={11.5}>{tx("记忆召回", "Recalled memory")}</Muted>
        {s.recalled.map((r) => (
          <Row key={r.title} gap={8} style={{ fontSize: "var(--fs-sm)" }}>
            <Icon name={r.kind === "runbook" ? "book" : "file"} size={14} color="var(--muted)" />
            <span className="grow">{r.title}</span>
            <Mono>{r.score.toFixed(2)}</Mono>
          </Row>
        ))}
      </Col>
      <Divider />
      <Col gap={8}>
        <Muted size={11.5}>{tx("下一步", "Next")}</Muted>
        {/* Without an investigation this opens the new-investigation dialog, which needs canvas.change (PA-228). */}
        <Gated reason={conv.investigation_id || API_MODE === "mock" ? null : canvasBlocked}>
          <Btn icon="branch" to={canvasHref(conv)} style={{ justifyContent: "flex-start" }}>
            {tx("转为画布", "Open as canvas")}
          </Btn>
        </Gated>
        {/* No backend writes a runbook draft from a chat or schedules a re-check yet: sample data only. */}
        {API_MODE === "mock" && (
          <>
            <Btn icon="book" style={{ justifyContent: "flex-start" }} onClick={() => toast(tx("已保存为 Runbook 草稿", "Saved as a runbook draft"))}>
              {tx("保存为 Runbook 草稿", "Save as runbook draft")}
            </Btn>
            <Btn icon="clock" style={{ justifyContent: "flex-start" }} onClick={() => toast(tx("已创建定时复查：每天 09:00", "Scheduled a daily check at 09:00"))}>
              {tx("每天 09:00 复查一次", "Re-check daily at 09:00")}
            </Btn>
          </>
        )}
      </Col>
    </aside>
  );
}

export default function ConversationPage() {
  const { id = "" } = useParams();
  const { tx } = useI18n();
  const chatBlocked = useNeed(["conversation", "change"]);
  const nav = useNavigate();
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const { data: conv, isLoading, error } = useGet<Conversation>(`/conversations/${id}`);
  // The scope names its service by catalog or application ID; show the catalog name when there is one (PA-166).
  const names = useNames();
  const service = conv?.scope.service ? (names.service(conv.scope.service) ?? conv.scope.service) : null;
  const [messages, setMessages] = useState<Message[]>([]);
  const [sending, setSending] = useState(false);
  const [choice, setChoice] = useState<ModelChoice | null>(() => (params.get("model") ? { model: params.get("model")!, reasoning: params.get("reasoning") ?? undefined } : null));
  const [turn, setTurn] = useState<ModelChoice | null>(null);
  const { data: catalog } = useModelCatalog();
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
    async (text: string, files: Upload[] = [], pick: ModelChoice | null = choice) => {
      if ((!text && !files.length) || sending) return;
      const c = pick ?? catalog?.default ?? null;
      setSending(true);
      setTurn(c);
      // PA-225: a rejected send (422, 413, 503…) or a stream that gave up says so and frees the composer.
      const fail = (text: string) => {
        stop.current = null;
        setSending(false);
        setTurn(null);
        setMessages((ms) => ms.map((m) => (m.streaming ? { ...m, streaming: false } : m)));
        toast(text, { tone: "danger" });
      };
      try {
        stop.current = await sendAndStream(
          id,
          text,
          (e) => {
            setMessages((ms) => applyEvent(ms, e));
            if (e.type === "message.completed") {
              setSending(false);
              setTurn(null);
              stop.current = null;
              // Refresh the list and this conversation (side panel: connectors, context meter).
              qc.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === "string" && (q.queryKey[0] as string).startsWith("/conversations") });
            }
          },
          { choice: c, attachments: files, onError: () => fail(tx("回复中断了，请稍后重试", "The reply was interrupted; try again later")) },
        );
      } catch (e) {
        fail(e instanceof ApiError ? e.message : tx("这条消息没有发出去，请稍后重试", "The message wasn't sent; try again later"));
      }
    },
    [id, sending, qc, choice, catalog],
  );
  const stopTurn = () => {
    stop.current?.();
    stop.current = null;
    setSending(false);
    setTurn(null);
    setMessages((ms) => ms.map((m) => (m.streaming ? { ...m, streaming: false } : m)));
  };

  useEffect(() => {
    const q = params.get("send");
    if (conv && q) {
      const next = new URLSearchParams(params);
      const files = takeAttachments(next.get("files"));
      for (const k of ["send", "files", "model", "reasoning"]) next.delete(k);
      setParams(next, { replace: true });
      void send(q, files);
    }
  }, [conv]); // eslint-disable-line react-hooks/exhaustive-deps

  const openRollback = useRollbackDrawer((x) => x.open);
  const openArtifact = (aid: string) => {
    const next = new URLSearchParams(params);
    next.set("artifact", aid);
    setParams(next, { replace: true });
  };
  const actions: PartActions = {
    openArtifact: (a) => {
      const match = conv?.artifacts.find((x) => x.kind === a.kind);
      if (match) openArtifact(match.id);
      else if (API_MODE === "mock") toast(tx("已在产物面板中生成：{t}", "Created in the artifact panel: {t}", { t: a.title ?? "" }));
      else toast(tx("这条结果还没有保存为产物", "This result hasn't been saved as an artifact"));
    },
    onStep: (action, label) => {
      if (action === "proposal") {
        // Open the proposal this conversation actually produced; the sample data has its own.
        const proposal = [...messages].reverse().flatMap((m) => m.parts).find((p): p is Extract<Part, { type: "proposal" }> => p.type === "proposal");
        if (proposal) nav(`/review/${proposal.id}`);
        else if (API_MODE === "mock") nav("/review/PRP-88");
        else toast(tx("这次对话还没有生成提案", "This conversation hasn't produced a proposal yet"));
      } else if (action === "canvas") nav(canvasHref(conv));
      else if (action === "ask") void send(label);
      else toast(label);
    },
    // Rollback suggestions only appear in conversations linked to an investigation (approved design).
    ...(conv?.investigation_id ? { openRollback: (r: { serviceId: string; preselect: string }) => openRollback({ investigationId: conv.investigation_id!, serviceId: r.serviceId, preselect: r.preselect || undefined }) } : {}),
  };

  if (error) return <Page crumbs={[tx("对话与画布", "Conversations & canvases")]} error={error} errorSource={tx("对话", "conversation")}>{null}</Page>;
  return (
    <Page
      flush
      crumbs={[tx("对话与画布", "Conversations & canvases"), conv?.title ?? "…"]}
      actions={
        <>
          <Segmented
            label={tx("视图", "View")}
            value="chat"
            onChange={(v) => v === "canvas" && nav(canvasHref(conv))}
            options={[
              { value: "chat", label: tx("对话", "Chat"), icon: "message" },
              { value: "canvas", label: tx("画布", "Canvas"), icon: "branch" },
            ]}
          />
          {service && (
            <Badge>
              <Icon name="box" size={12} />
              {service}
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
              {conv &&
                messages.map((m, i) => {
                  const prev = messages.slice(0, i).reverse().find((x) => x.role === "assistant" && x.run);
                  const switched = m.role === "assistant" && m.run && prev?.run && (prev.run.model_id !== m.run.model_id || prev.run.reasoning !== m.run.reasoning);
                  return (
                    <div key={m.id} className="v3-col" style={{ gap: 18 }}>
                      {switched && m.run && <TurnDivider run={m.run} />}
                      <MessageView m={m} conv={conv} actions={actions} onOpenArtifact={openArtifact} />
                    </div>
                  );
                })}
              <div ref={bottom} />
            </div>
          </div>
          <div className="conv-composer">
            <Composer
              blocked={chatBlocked}
              placeholder={tx("继续提问，或让 AI 执行一个动作…", "Ask a follow-up, or ask the AI to take an action…")}
              sending={sending}
              onStop={stopTurn}
              turn={turn}
              choice={choice}
              onChoiceChange={setChoice}
              attachments={conv?.scope.service && service ? [{ kind: "service", id: conv.scope.service, label: service }] : []}
              onSend={(t, att, x) => send(t || att.map((a) => a.label).join(" "), x.files, x.choice)}
            />
            <p className="hint">
              {tx("⏎ 发送 · Shift + ⏎ 换行 · 每轮使用一个模型，切换从下一轮生效 · 写操作一律经过人工批准", "⏎ send · Shift + ⏎ new line · one model per turn, switches apply next turn · every write needs human approval")}
              {turn && choice && describeChoice(catalog, turn)?.label !== describeChoice(catalog, choice)?.label && ` · ${tx("本轮：{m}", "This turn: {m}", { m: describeChoice(catalog, turn)?.label ?? "" })}`}
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
          conv && <SidePanel conv={conv} service={service} />
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
  const chatBlocked = useNeed(["conversation", "change"]);
  const [failed, setFailed] = useState<string | null>(null);
  const q = params.get("q");
  const started = useRef(false);
  useEffect(() => {
    // Without conversation·change the question stays unsent; the composer below says which permission is missing.
    if (!q || started.current || chatBlocked) return;
    started.current = true;
    const carry = new URLSearchParams({ send: q });
    for (const k of ["model", "reasoning", "files"]) if (params.get(k)) carry.set(k, params.get(k)!);
    api<{ id: string }>("POST", "/conversations", { body: { text: q, context: params.get("context"), model: params.get("model") } })
      .then(({ id }) => nav(`/conversations/${id}?${carry}`, { replace: true }))
      .catch((e: unknown) => setFailed(e instanceof Error ? e.message : tx("没能创建对话，请稍后重试", "Couldn't create the conversation; try again later")));
  }, [q, nav, params, chatBlocked, tx]);
  if (q && !chatBlocked && !failed)
    return (
      <Page crumbs={[tx("对话与画布", "Conversations & canvases"), tx("新对话", "New")]}>
        <div className="v3-col" style={{ alignItems: "center", padding: 80, gap: 10 }}>
          <span className="v3-spinner spin" style={{ width: 18, height: 18, color: "var(--accent)" }} />
          <Muted>{tx("正在创建对话…", "Creating the conversation…")}</Muted>
        </div>
      </Page>
    );
  return (
    <Page crumbs={[tx("对话与画布", "Conversations & canvases"), tx("新对话", "New")]}>
      <div className="v3-col" style={{ alignItems: "center", padding: "80px 24px", gap: 16 }}>
        <AiAvatar size={40} />
        <h1 style={{ fontSize: 20, fontWeight: 600 }}>{tx("开始一个新对话", "Start a new conversation")}</h1>
        {failed && (
          <div className="unk-banner warn" role="alert" style={{ width: "min(720px, 100%)" }}>
            <span className="grow">{failed}</span>
          </div>
        )}
        <div style={{ width: "min(720px, 100%)" }}>
          <Composer big autoFocus minRows={4} blocked={chatBlocked} placeholder={tx("描述问题、下指令，或用 / 选择命令…", "Describe a problem, give an instruction, or pick a / command…")} onSend={(t, att, x) => nav(newConversationUrl(t, att, x))} />
        </div>
      </div>
    </Page>
  );
}

