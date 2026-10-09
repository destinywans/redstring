package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

type sessionLookupRepository struct {
	store.Repository
	err error
}

func (r sessionLookupRepository) Session(context.Context, string) (core.User, error) {
	if r.err != nil {
		return core.User{}, r.err
	}
	return core.User{ID: "u1", Role: "viewer"}, nil
}

var errStoreDown = errors.New("failed to connect: connection refused")

// PA-59: only a missing, expired or revoked session signs the caller out; a
// session store that cannot be read is 503, both at the route gate and in the
// V3 environment middleware.
func TestSessionLookupFailureIsNotSignOut(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{pgx.ErrNoRows, 401, "unauthorized"},
		{fmt.Errorf("lookup: %w", store.ErrSessionRevoked), 401, "unauthorized"},
		{errStoreDown, 503, "session_store_unavailable"},
		{context.DeadlineExceeded, 503, "session_store_unavailable"},
	}
	for _, tc := range cases {
		s := New(sessionLookupRepository{err: tc.err}, nil)
		s.Register("GET /api/v1/session-probe", "viewer", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		r := httptest.NewRequest(http.MethodGet, "/api/v1/session-probe", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "token"})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("route gate, %v: %d %s", tc.err, w.Code, w.Body.String())
		}

		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		lookup := func(context.Context, string) (core.User, error) { return core.User{}, tc.err }
		h, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
			testV3EnvironmentAuthorizer{"dev": true}, lookup, next)
		if err != nil {
			t.Fatal(err)
		}
		r = httptest.NewRequest(http.MethodGet, "/api/v1/resources", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "token"})
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("environment middleware, %v: %d %s", tc.err, w.Code, w.Body.String())
		}
	}
	s := New(sessionLookupRepository{}, nil)
	s.Register("GET /api/v1/session-probe", "viewer", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session-probe", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "token"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("live session: %d %s", w.Code, w.Body.String())
	}
}
