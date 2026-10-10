-- PA-19 P4 (approved 2026-10-10): a V3 instance no longer runs the legacy
-- rollback flow (internal/actions). Every legacy plan that was approved but
-- never executed expires, with the reason legacy_plan_expired_registry_cutover:
-- - without an execution, its approval becomes "expired", so it can neither
--   be enqueued nor approved or revoked again;
-- - with an execution still queued, the execution becomes "failed" and gets
--   an execution.failed event. Its approval stays "approved": a plan has one
--   execution, so it cannot run again, and GET /executions/{id} keeps the
--   decision values of its contract.
-- A rollback needs a new V3 proposal. Each expired plan is audited
-- (legacy_action_plan.expired). Irreversible.
--
-- Only a database bound to a V3 instance is changed: the legacy mode keeps
-- its flow, and its database has no v3_instance_boundary row. The legacy
-- action tables belong to internal/actions and may not exist yet. A legacy
-- database bound to V3 after this migration keeps its approvals, but a V3
-- instance neither serves the legacy action routes nor runs their worker.
DO $$
BEGIN
 IF to_regclass('v3_instance_boundary') IS NULL OR to_regclass('approvals') IS NULL OR to_regclass('executions') IS NULL THEN
  RETURN;
 END IF;
 IF NOT EXISTS (SELECT 1 FROM v3_instance_boundary) THEN
  RETURN;
 END IF;
 WITH expiring AS (
  SELECT a.plan_id, p.investigation_id, i.application_id, x.id AS execution_id
  FROM approvals a
  JOIN action_plans p ON p.id = a.plan_id
  JOIN investigations i ON i.id = p.investigation_id
  LEFT JOIN executions x ON x.plan_id = a.plan_id
  WHERE a.decision = 'approved' AND (x.id IS NULL OR x.status = 'queued')
 ), expired AS (
  UPDATE approvals a SET decision = 'expired', reason = 'legacy_plan_expired_registry_cutover'
  FROM expiring e WHERE a.plan_id = e.plan_id AND e.execution_id IS NULL
  RETURNING a.plan_id
 ), failed AS (
  UPDATE executions x SET status = 'failed', error = 'legacy_plan_expired_registry_cutover', updated_at = now()
  FROM expiring e WHERE x.id = e.execution_id
  RETURNING x.id
 ), failed_events AS (
  INSERT INTO events(investigation_id, type, payload)
  SELECT e.investigation_id, 'execution.failed',
   jsonb_build_object('execution_id', e.execution_id, 'error', 'legacy_plan_expired_registry_cutover')
  FROM expiring e WHERE e.execution_id IS NOT NULL
  RETURNING seq
 )
 INSERT INTO management_audit(actor_id, actor_kind, application_id, action, target, outcome, changes)
 SELECT '', 'system', e.application_id, 'legacy_action_plan.expired', e.plan_id, 'success',
  jsonb_build_object('reason', 'legacy_plan_expired_registry_cutover', 'execution_id', e.execution_id,
   'previous_execution_status', CASE WHEN e.execution_id IS NULL THEN 'none' ELSE 'queued' END)
 FROM expiring e;
END $$;
