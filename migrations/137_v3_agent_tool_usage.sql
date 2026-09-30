-- Seven-day observed tool usage comes only from the trusted in-process
-- RecordSpan producer. This index does not declare instrumentation complete
-- or grant any Agent an executable tool.
CREATE INDEX v3_agent_tool_spans_7d
 ON v3_agent_run_spans(tool_id,started_at)
 WHERE kind='tool' AND tool_id IS NOT NULL;
