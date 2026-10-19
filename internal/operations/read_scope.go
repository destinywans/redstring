package operations

import (
	"context"
	"github.com/destinywans/redstring/internal/core"
)

// Empty arrays, not SQL NULL, preserve the legacy all-visible query behavior.
func readScopeArrays(ctx context.Context) core.ReadScope {
	s := core.ScopeFromContext(ctx)
	if s.ApplicationIDs == nil {
		s.ApplicationIDs = []string{}
	}
	if s.EnvironmentIDs == nil {
		s.EnvironmentIDs = []string{}
	}
	return s
}
