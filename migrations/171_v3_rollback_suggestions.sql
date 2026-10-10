-- PA-72: the AI rollback suggestion of an investigation reply. It is set only
-- with the completed reply, is a JSON object, and never changes afterwards.
-- Executed only on isolated databases and the acceptance instances.
ALTER TABLE v3_investigation_node_replies ADD COLUMN rollback_suggestion jsonb;
ALTER TABLE v3_investigation_node_replies ADD CONSTRAINT v3_investigation_reply_suggestion_shape
 CHECK (rollback_suggestion IS NULL OR (state='completed' AND jsonb_typeof(rollback_suggestion)='object'
   AND octet_length(rollback_suggestion::text)<=16384));
CREATE INDEX v3_investigation_reply_suggestions ON v3_investigation_node_replies(branch_id,completed_at DESC,id DESC)
 WHERE rollback_suggestion IS NOT NULL;

CREATE FUNCTION v3_investigation_reply_suggestion_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.rollback_suggestion IS DISTINCT FROM NEW.rollback_suggestion AND OLD.state='completed' THEN
   RAISE EXCEPTION 'v3 investigation reply suggestions are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_investigation_reply_suggestion_immutable BEFORE UPDATE ON v3_investigation_node_replies
 FOR EACH ROW EXECUTE FUNCTION v3_investigation_reply_suggestion_immutable();
