package rbac

// V3ListGrantsCTE makes one actor's effective grants available to list queries
// so that hidden objects do not consume result limits. The argument must be a
// trusted SQL placeholder, and the actor ID must be supplied as a bound value.
// Callers still recheck current authority before returning the result.
func V3ListGrantsCTE(actorArg string) string {
	return `list_grants AS MATERIALIZED (
 SELECT module,action,scope_kind,scope_id FROM v3_effective_role_grants WHERE user_id=` + actorArg + `
)`
}

// V3ListScopePredicate mirrors the environment and module decisions of the V3
// policy for app-scoped candidates. The caller joins a current application to
// its selected environment and passes only trusted, static SQL expressions.
// An empty team expression omits owner-team fallback.
func V3ListScopePredicate(envExpr, appExpr, teamExpr, module, action string) string {
	team := ""
	if teamExpr != "" {
		team = `
   OR (g.scope_kind='team' AND g.scope_id=` + teamExpr + ` AND ` + teamExpr + ` NOT IN ('','*'))`
	}
	// Approve needs verified step-up evidence and is never admitted by this
	// ordinary list predicate. Invalid internal operation names fail closed.
	if !v3ModuleCode.MatchString(module) || !v3Actions[action] || action == "approve" {
		return "FALSE"
	}
	return `EXISTS (
 SELECT 1 FROM list_grants g WHERE g.module='environment' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=` + envExpr + `))
 ) AND EXISTS (
 SELECT 1 FROM list_grants g WHERE g.module='` + module + `' AND g.action='` + action + `'
 AND (
      (g.scope_kind='global' AND g.scope_id='*')
   OR (g.scope_kind='environment' AND g.scope_id=` + envExpr + `)
   OR (g.scope_kind='application' AND g.scope_id=` + appExpr + ` AND ` + appExpr + ` NOT IN ('','*'))` + team + `
 )
 )`
}
