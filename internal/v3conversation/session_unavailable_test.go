package v3conversation

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

// PA-59: a session store that cannot be read stops a turn as unavailable
// (503), not as a revoked session (403).
func TestActiveTurnSeparatesStoreFailureFromRevokedSession(t *testing.T) {
	down := errors.New("failed to connect: connection refused")
	for _, tc := range []struct {
		err       error
		forbidden bool
	}{{pgx.ErrNoRows, true}, {down, false}} {
		s := &Service{opts: Options{RefreshSession: func(context.Context, string) (core.User, error) { return core.User{}, tc.err }}}
		err := s.authorizeActiveTurn(context.Background(), "owner", "dev", "token")
		if errors.Is(err, ErrForbidden) != tc.forbidden || !tc.forbidden && !errors.Is(err, down) {
			t.Fatalf("%v: got %v", tc.err, err)
		}
		w := httptest.NewRecorder()
		operationError(w, err)
		if want := map[bool]int{true: 403, false: 503}[tc.forbidden]; w.Code != want {
			t.Fatalf("%v: status %d, want %d", tc.err, w.Code, want)
		}
	}
}
