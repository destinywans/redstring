package core

import "context"

// ReadScope is a list/query filter, never an authorization grant. Empty lists
// mean all visible resources; callers must still apply application ACLs.
type ReadScope struct {
	ApplicationIDs []string
	EnvironmentIDs []string
}
type readScopeKey struct{}

func WithReadScope(ctx context.Context, scope ReadScope) context.Context {
	return context.WithValue(ctx, readScopeKey{}, scope)
}
func ScopeFromContext(ctx context.Context) ReadScope {
	v, _ := ctx.Value(readScopeKey{}).(ReadScope)
	return v
}
func (s ReadScope) Matches(app, env string) bool {
	return scopeContains(s.ApplicationIDs, app) && scopeContains(s.EnvironmentIDs, env)
}
func scopeContains(ids []string, id string) bool {
	if len(ids) == 0 {
		return true
	}
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
