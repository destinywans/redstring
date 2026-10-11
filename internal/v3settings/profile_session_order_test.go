package v3settings

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/store"
)

// PA-69: the session list marks the current session on every item, puts it
// first and orders the rest by newest sign-in, with the sign-in time shown.
func TestProfileSessionsMarkCurrentAndNewestFirst(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-session-order-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 3)
	for i, age := range []string{"3 hours", "1 hour", "2 hours"} {
		_, token, err := s.Login(ctx, "one@example.invalid", password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(ctx, `UPDATE sessions SET issued_at=now()-$2::interval WHERE token_hash=$1`, store.Hash(token), age); err != nil {
			t.Fatal(err)
		}
		tokens[i] = token
	}
	request := profileTestAPI(t, New(s, nil), tokens[0])
	w := request(http.MethodGet, "/api/v1/me/profile", "")
	var view struct {
		Security struct {
			Sessions []map[string]json.RawMessage `json:"sessions"`
		} `json:"security"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Security.Sessions) != 3 {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	want := []string{store.Hash(tokens[0]), store.Hash(tokens[1]), store.Hash(tokens[2])}
	for i, session := range view.Security.Sessions {
		var id, signedIn string
		var current bool
		_ = json.Unmarshal(session["id"], &id)
		_ = json.Unmarshal(session["signed_in_at"], &signedIn)
		if _, present := session["current"]; !present || json.Unmarshal(session["current"], &current) != nil ||
			id != want[i] || current != (i == 0) || !validRFC3339UTC(signedIn) {
			t.Fatalf("session %d: %s", i, w.Body.String())
		}
	}
}

// validRFC3339UTC reports whether value is an RFC 3339 instant in UTC (PA-83).
func validRFC3339UTC(value string) bool {
	at, err := time.Parse(time.RFC3339, value)
	return err == nil && strings.HasSuffix(value, "Z") && at.Location() == time.UTC
}
