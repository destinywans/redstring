-- V3 conversations are independent of investigations and their application FK.
CREATE TABLE v3_conversations (
    id text PRIMARY KEY,
    owner_id text NOT NULL REFERENCES users(id),
    environment_id text NOT NULL,
    title text NOT NULL,
    context_ref text NOT NULL DEFAULT '',
    default_model_id text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','waiting_approval','resolved','archived')),
    active_reply_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_conversations_owner_updated ON v3_conversations(owner_id, environment_id, updated_at DESC);

CREATE TABLE v3_conversation_messages (
    id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES v3_conversations(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('user','assistant')),
    author text NOT NULL,
    parts jsonb NOT NULL DEFAULT '[]'::jsonb,
    attachments jsonb NOT NULL DEFAULT '[]'::jsonb,
    run jsonb,
    state text NOT NULL DEFAULT 'completed' CHECK (state IN ('streaming','completed','failed')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_conversation_messages_order ON v3_conversation_messages(conversation_id, created_at, id);

CREATE TABLE v3_conversation_replies (
    id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES v3_conversations(id) ON DELETE CASCADE,
    user_message_id text NOT NULL REFERENCES v3_conversation_messages(id),
    assistant_message_id text NOT NULL REFERENCES v3_conversation_messages(id),
    model_id text NOT NULL,
    reasoning text NOT NULL DEFAULT '',
    state text NOT NULL CHECK (state IN ('queued','running','completed','failed')),
    error text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX v3_conversation_replies_latest ON v3_conversation_replies(conversation_id, started_at DESC, id DESC);
CREATE UNIQUE INDEX v3_conversation_one_active_reply ON v3_conversation_replies(conversation_id) WHERE state IN ('queued','running');
ALTER TABLE v3_conversations ADD CONSTRAINT v3_conversations_active_reply_fk
    FOREIGN KEY (active_reply_id) REFERENCES v3_conversation_replies(id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE v3_conversation_events (
    seq bigserial PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES v3_conversations(id) ON DELETE CASCADE,
    reply_id text NOT NULL REFERENCES v3_conversation_replies(id) ON DELETE CASCADE,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_conversation_events_replay ON v3_conversation_events(reply_id, seq);
