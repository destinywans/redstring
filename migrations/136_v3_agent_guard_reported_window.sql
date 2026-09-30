-- Guardrail summaries use the full seven-day set of revision-backed reports,
-- independent of the bounded event page. Historical rows without a trusted
-- decision time are excluded from action-time counts.
CREATE INDEX v3_agent_guard_events_decision_window
 ON v3_agent_guard_events(observed_at DESC,policy_id,action)
 WHERE observed_at IS NOT NULL AND policy_revision IS NOT NULL;

CREATE INDEX v3_agent_guard_events_recent_reports
 ON v3_agent_guard_events(created_at DESC,id DESC);
