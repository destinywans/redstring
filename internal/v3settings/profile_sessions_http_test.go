package v3settings

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestProfileSessionRevocationRegisteredHTTP(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-session-test-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id IN ('one','two')`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	_, activeToken, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, olderToken, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := s.Login(ctx, "two@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(s, nil, true)
	New(s, nil).Register(api)
	api.MarkV3Route("DELETE /api/v1/me/sessions/{id}", "", "")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, allowPreferencesEnvironment{}, s.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(token, environment, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+id, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	olderID, activeID, otherID := store.Hash(olderToken), store.Hash(activeToken), store.Hash(otherToken)
	if w := request("", "dev", olderID); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous delete: status=%d", w.Code)
	}
	if w := request(activeToken, "all", olderID); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"env_view_only"`) {
		t.Fatalf("all-scope delete: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(activeToken, "dev", "not-a-session-id"); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid session id: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(activeToken, "dev", activeID); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("current session delete: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(activeToken, "dev", otherID); w.Code != http.StatusNotFound {
		t.Fatalf("other account session delete: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(activeToken, "dev", olderID); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("own older session delete: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := s.Session(ctx, olderToken); err == nil {
		t.Fatal("revoked session still authenticates")
	}
	if _, err := s.Session(ctx, activeToken); err != nil {
		t.Fatalf("active session lost: %v", err)
	}
	if _, err := s.Session(ctx, otherToken); err != nil {
		t.Fatalf("other account session lost: %v", err)
	}
	if w := request(activeToken, "dev", olderID); w.Code != http.StatusNotFound {
		t.Fatalf("repeat delete: status=%d body=%s", w.Code, w.Body.String())
	}
	var auditCount int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.session.revoked' AND target=$1 AND outcome='success'`, olderID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("successful audit count=%d err=%v", auditCount, err)
	}
	// Session revocation is account-bound. Neither the request nor the audit
	// record contains the raw bearer token.
	var rawTokenRecorded bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management_audit WHERE changes::text LIKE '%' || $1 || '%')`, olderToken).Scan(&rawTokenRecorded); err != nil || rawTokenRecorded {
		t.Fatalf("raw session token recorded=%v err=%v", rawTokenRecorded, err)
	}
}

func TestProfileCurrentSessionCannotBeClaimedByQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me/profile?current_session=forged", nil)
	if got := currentSessionHash(r); got != "" {
		t.Fatalf("query claimed a current session: %q", got)
	}
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "actual-token"})
	if got := currentSessionHash(r); got != store.Hash("actual-token") {
		t.Fatalf("current session is not bound to the authenticated cookie")
	}
}
