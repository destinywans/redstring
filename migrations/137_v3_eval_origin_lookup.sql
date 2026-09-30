-- Bound immutable evaluation and case-origin audit lookups by target.
CREATE INDEX management_audit_v3_eval_origin
 ON management_audit(action,target,id)
 WHERE action IN ('v3.agent.eval.created','v3.agent.eval.case.registered')
 AND outcome='recorded';
