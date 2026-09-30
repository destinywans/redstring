-- Freeze the exact media bytes accepted for a conversation turn. A reply and
-- later history replay must not depend on mutable or deleted upload rows.
-- This table is private model input; public message APIs expose only Upload
-- references and never select its content.
CREATE TABLE v3_conversation_media_snapshot (
    message_id text NOT NULL REFERENCES v3_conversation_messages(id) ON DELETE CASCADE,
    position smallint NOT NULL CHECK (position BETWEEN 0 AND 19),
    upload_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('image','pdf')),
    name text NOT NULL,
    mime text NOT NULL,
    content bytea NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 8388608),
    PRIMARY KEY (message_id,position),
    UNIQUE (message_id,upload_id)
);
