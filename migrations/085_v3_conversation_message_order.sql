-- Message timestamps use transaction time. A turn's user and assistant rows
-- therefore tie, while random IDs do not encode their order. Drain old V3
-- workers before this migration and the corresponding reader change roll out.
-- Accepted turns have a management_audit row written after the conversation
-- row lock. Its chain_seq preserves the serialized turn order even when an
-- earlier-started transaction acquires that row lock later. Unlinked/imported
-- rows lack this evidence and are placed after audited turns using timestamps.
CREATE SEQUENCE v3_conversation_message_order_seq;
ALTER TABLE v3_conversation_messages ADD COLUMN sort_order bigint;
ALTER SEQUENCE v3_conversation_message_order_seq
    OWNED BY v3_conversation_messages.sort_order;

WITH accepted_audit AS (
    SELECT DISTINCT ON (changes->>'reply_id') changes->>'reply_id' AS reply_id,
           target, chain_seq
    FROM management_audit
    WHERE action = 'v3.conversation.message' AND outcome = 'accepted'
      AND changes ? 'reply_id'
    ORDER BY changes->>'reply_id', chain_seq
), reply_refs AS (
    SELECT r.user_message_id AS message_id, r.id, r.started_at,
           a.chain_seq AS audit_order,
           user_message_id, assistant_message_id
    FROM v3_conversation_replies r
    LEFT JOIN accepted_audit a ON a.reply_id = r.id AND a.target = r.conversation_id
    UNION ALL
    SELECT r.assistant_message_id, r.id, r.started_at,
           a.chain_seq,
           user_message_id, assistant_message_id
    FROM v3_conversation_replies r
    LEFT JOIN accepted_audit a ON a.reply_id = r.id AND a.target = r.conversation_id
), first_reply AS (
    SELECT DISTINCT ON (message_id) *
    FROM reply_refs
    ORDER BY message_id, audit_order NULLS LAST, started_at, id
), ranked AS (
    SELECT m.id,
           row_number() OVER (
               ORDER BY r.audit_order NULLS LAST,
                        COALESCE(r.started_at, m.created_at),
                        COALESCE(r.id, m.id),
                        CASE WHEN m.id = r.user_message_id THEN 0
                             WHEN m.id = r.assistant_message_id THEN 1
                             WHEN m.role = 'user' THEN 0 ELSE 1 END,
                        m.id
           ) AS sort_order
    FROM v3_conversation_messages m
    LEFT JOIN first_reply r ON r.message_id = m.id
)
UPDATE v3_conversation_messages m
SET sort_order = ranked.sort_order
FROM ranked WHERE m.id = ranked.id;

SELECT setval('v3_conversation_message_order_seq',
              COALESCE((SELECT max(sort_order) FROM v3_conversation_messages), 1),
              EXISTS (SELECT 1 FROM v3_conversation_messages));
ALTER TABLE v3_conversation_messages
    ALTER COLUMN sort_order SET DEFAULT nextval('v3_conversation_message_order_seq'),
    ALTER COLUMN sort_order SET NOT NULL;
CREATE UNIQUE INDEX v3_conversation_messages_sort_order
    ON v3_conversation_messages(conversation_id, sort_order);
DROP INDEX v3_conversation_messages_order;
