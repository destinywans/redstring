-- PA-46 (2026-10-10): a reply cut at the model's output token limit keeps its
-- answer and completes; the reply records why it ended early. '' is a
-- normal finish. Not executed on the existing business database.
ALTER TABLE v3_conversation_replies
 ADD COLUMN finish_reason text NOT NULL DEFAULT ''
 CONSTRAINT v3_conversation_replies_finish_reason CHECK (finish_reason IN ('', 'max_output_tokens'));
