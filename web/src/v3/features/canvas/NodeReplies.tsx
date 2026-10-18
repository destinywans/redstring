import { useEffect, useState, type Ref } from "react";
import { ApiError, api, useGet } from "../../api/client";
import type { CanvasNode, InvestigationReplies, InvestigationReply, RollbackSuggestion } from "../../api/types";
import { AiAvatar, Avatar, Brand, Btn, Col, Icon, Muted, Row, Skeleton } from "../../ui/core";
import { Markdown } from "../../ui/Markdown";
import { useI18n } from "../../i18n";
import { RollbackSuggestionCard } from "../events/RollbackSuggestion";
import "../conversation/parts.css";

const PAGE = 20;

/**
 * A branch's own conversation in the inspector (PA-75): the persisted replies, oldest first, with a way to load
 * earlier pages. Each rollback suggestion sits right under the reply that produced it (approved PA-72 design).
 * When the replies can't be read, it says so instead of showing an empty thread.
 */
export function NodeReplies({
  investigationId,
  n,
  cardRef,
  onOpenRollback,
  onCiteEvidence,
}: {
  investigationId: string;
  n: CanvasNode;
  /** Attached to the card of the node's current suggestion, so the node hint can scroll to it. */
  cardRef: Ref<HTMLDivElement>;
  onOpenRollback: (r: { serviceId: string; preselect: string }) => void;
  /** An evidence ID a citation points at (cite n is evidence_ids[n-1]); undefined when it can't be resolved. */
  onCiteEvidence: (evidenceId: string | undefined) => void;
}) {
  const { tx, fmt } = useI18n();
  const path = `/investigations/${investigationId}/nodes/${n.id}/replies`;
  const first = useGet<InvestigationReplies>(path, { page_size: PAGE }, { retry: false });
  const [older, setOlder] = useState<InvestigationReply[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [olderErr, setOlderErr] = useState<string | null>(null);
  // A fresh first page (new node, new reply) resets the earlier pages.
  useEffect(() => {
    setOlder([]);
    setCursor(first.data?.next_cursor ?? null);
    setMore(!!first.data?.has_more);
    setOlderErr(null);
  }, [first.data]);
  const loadEarlier = async () => {
    if (!cursor) return;
    setLoading(true);
    setOlderErr(null);
    try {
      const r = await api<InvestigationReplies>("GET", path, { query: { page_size: PAGE, cursor } });
      setOlder((o) => [...o, ...r.replies]);
      setCursor(r.next_cursor);
      setMore(r.has_more);
    } catch (e) {
      setOlderErr(e instanceof ApiError ? e.message : tx("没能读取更早的回复", "Couldn't load earlier replies"));
    } finally {
      setLoading(false);
    }
  };

  const suggestionCard = (s: RollbackSuggestion, isCurrent: boolean, evidence: string[]) => (
    <RollbackSuggestionCard
      key={`s-${s.reply_id ?? "node"}`}
      ref={isCurrent ? cardRef : undefined}
      s={s}
      onOpen={onOpenRollback}
      onCite={(c) => onCiteEvidence((s.evidence_ids ?? evidence)[c - 1])}
    />
  );

  if (first.isLoading) return <Skeleton h={120} />;
  if (!first.data) {
    // Can't read the replies (older server, error): keep the summary and say the history is unavailable.
    return (
      <Col gap={12}>
        {n.summary && (
          <div className="cv-msg">
            <Markdown text={n.summary} />
          </div>
        )}
        {n.context.messages > 0 || n.rollback_suggestion ? (
          <div className="cv-msg muted" role="note">
            <Row gap={6} align="flex-start">
              <Icon name="info" size={13} style={{ marginTop: 3 }} />
              <span>{tx("这个节点的历史回复暂时无法读取（共 {n} 条消息）。继续追问后，新的回复会显示在这里。", "This node's earlier replies can't be read yet ({n} messages). New replies show here after you ask a follow-up.", { n: n.context.messages })}</span>
            </Row>
          </div>
        ) : (
          <Muted>{tx("还没有对话。在节点上追问后，回复会显示在这里。", "No conversation yet. Ask on the node and replies show here.")}</Muted>
        )}
        {n.rollback_suggestion && suggestionCard(n.rollback_suggestion, true, [])}
      </Col>
    );
  }
  const replies = [...first.data.replies, ...older].reverse();
  const current = n.rollback_suggestion;
  const isCurrent = (r: InvestigationReply) => !!current && !!r.rollback_suggestion && (r.rollback_suggestion.reply_id ?? r.id) === (current.reply_id ?? "");
  const currentShown = replies.some(isCurrent);
  const failure = (code: string | null) =>
    (
      ({
        lease_expired: tx("回复中断：执行超时，没有完成。", "Interrupted: the attempt ran out of time before it finished."),
        model_unavailable: tx("模型回复失败。", "The model didn't reply."),
        evidence_unavailable: tx("取证失败，没能读到证据。", "Evidence couldn't be read."),
        // PA-166: the codes a finished reply can still fail to save with, in words.
        evidence_access_revoked: tx("回复完成前，你读取其中证据的权限被收回了，结果没有保存。", "Your access to some of its evidence was revoked before it finished, so the result wasn't saved."),
        context_version_conflict: tx("回复期间分支上下文有了更新，结果没有保存，可以重新提问。", "The branch context changed while it ran, so the result wasn't saved; ask again."),
        reply_state_conflict: tx("这条回复的状态被其他操作改变了，结果没有保存。", "Another action changed this reply's state, so the result wasn't saved."),
        node_context_too_large: tx("分支上下文超过上限，没能生成回复。", "The branch context is over the limit, so no reply was made."),
      }) as Record<string, string>
    )[code ?? ""] ?? tx("回复没有完成，原因未知。", "The reply didn't finish; the reason is unknown.");
  const when = (iso: string | null) => (iso ? fmt.dateTime(iso) : "");
  return (
    <Col gap={12}>
      {more && (
        <Btn h={26} kind="ghost" icon="up" loading={loading} onClick={() => void loadEarlier()} style={{ alignSelf: "center" }}>
          {tx("加载更早的回复", "Load earlier replies")}
        </Btn>
      )}
      {olderErr && <Muted size={11.5}>{olderErr}</Muted>}
      {!replies.length && n.status !== "running" && <Muted>{tx("还没有对话。在节点上追问后，回复会显示在这里。", "No conversation yet. Ask on the node and replies show here.")}</Muted>}
      {replies.map((r) => (
        <Col key={r.id} gap={8}>
          {/* The question, in the generic canvas turn layout. */}
          <Row gap={8} align="flex-start">
            <Avatar name={r.author ?? tx("成员", "Member")} size={22} accent />
            <div className="v3-col" style={{ gap: 2, minWidth: 0 }}>
              <Muted size={11}>
                {r.author ?? tx("成员", "Member")} · {when(r.created_at)}
              </Muted>
              <p style={{ fontSize: "var(--fs-sm)", lineHeight: 1.6, margin: 0 }}>{r.text}</p>
            </div>
          </Row>
          <Row gap={8} align="flex-start">
            <AiAvatar size={22} />
            <div className="v3-col" style={{ gap: 4, minWidth: 0, flex: 1 }}>
              <Row gap={5}>
                {r.model.provider && <Brand name={r.model.provider === "anthropic" ? "claude" : r.model.provider} size={16} />}
                <Muted size={11}>
                  {r.reasoning ? `${r.model.name} · ${r.reasoning}` : r.model.name}
                  {r.completed_at && ` · ${when(r.completed_at)}`}
                </Muted>
              </Row>
              {r.state === "completed" && r.answer ? (
                <div className="cv-msg">
                  <Markdown text={r.answer} />
                </div>
              ) : r.state === "running" ? (
                <Row gap={8} className="part-status">
                  <span className="v3-spinner spin" />
                  <span>{tx("AI 正在回复…", "The AI is replying…")}</span>
                </Row>
              ) : (
                <Row gap={8} className="part-status danger">
                  <Icon name="x" size={13} />
                  <span>{failure(r.failure_code)}</span>
                </Row>
              )}
            </div>
          </Row>
          {r.rollback_suggestion && suggestionCard(r.rollback_suggestion, isCurrent(r), r.evidence_ids)}
        </Col>
      ))}
      {/* The node's current suggestion came from a reply not loaded yet (older page): still show it, last. */}
      {current && !currentShown && suggestionCard(current, true, [])}
      {n.status === "running" && !replies.some((r) => r.state === "running") && (
        <Row gap={8} className="part-status">
          <span className="v3-spinner spin" />
          <span>{tx("AI 正在回复…", "The AI is replying…")}</span>
        </Row>
      )}
    </Col>
  );
}
