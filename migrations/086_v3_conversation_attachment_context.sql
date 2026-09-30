-- Preserve the exact bounded, model-only text snapshot used for a user turn.
-- Client-visible parts and attachment references remain separate.
ALTER TABLE v3_conversation_messages
    ADD COLUMN attachment_context text NOT NULL DEFAULT ''
    CHECK (octet_length(attachment_context) <= 32768);
