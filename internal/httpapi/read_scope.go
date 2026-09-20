package httpapi

import (
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"net/url"
	"sort"
	"strings"
)

// ParseReadScope accepts repeated or comma-separated IDs. Plural parameters
// take precedence over legacy singular parameters, never silently broaden an
// explicitly empty/malformed filter to the entire workspace.
func ParseReadScope(q url.Values) (core.ReadScope, error) {
	parse := func(plural, singular string) ([]string, error) {
		values, exists := q[plural]
		if !exists {
			if v := q.Get(singular); v != "" {
				values = []string{v}
			}
		}
		if len(values) == 0 {
			return []string{}, nil
		}
		set := map[string]bool{}
		for _, raw := range values {
			for _, id := range strings.Split(raw, ",") {
				id = strings.TrimSpace(id)
				if id == "" || len(id) > 200 {
					return nil, store.ErrInvalidReference
				}
				set[id] = true
			}
		}
		if len(set) > 50 {
			return nil, store.ErrInvalidReference
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return ids, nil
	}
	apps, err := parse("applications", "application_id")
	if err != nil {
		return core.ReadScope{}, err
	}
	envs, err := parse("environments", "environment_id")
	return core.ReadScope{ApplicationIDs: apps, EnvironmentIDs: envs}, err
}

func scopeApplicationMatches(scope core.ReadScope, app core.Application) bool {
	if len(scope.EnvironmentIDs) == 0 {
		return scope.Matches(app.ID, "")
	}
	for _, env := range app.Environments {
		if scope.Matches(app.ID, env.ID) {
			return true
		}
	}
	return false
}
