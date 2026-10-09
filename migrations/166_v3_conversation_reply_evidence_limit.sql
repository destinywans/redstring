-- PA-52 (2026-10-10): a reply the model gave after its evidence-tool limit
-- closed the tools completes with finish_reason evidence_limit instead of
-- being canceled. Not executed on the existing business database.
ALTER TABLE v3_conversation_replies DROP CONSTRAINT v3_conversation_replies_finish_reason;
ALTER TABLE v3_conversation_replies
 ADD CONSTRAINT v3_conversation_replies_finish_reason CHECK (finish_reason IN ('', 'max_output_tokens', 'evidence_limit'));
