package v3settings

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
)

type notificationChannels struct {
	Feishu bool `json:"feishu"`
	Email  bool `json:"email"`
	SMS    bool `json:"sms"`
	Phone  bool `json:"phone"`
}
type profileNotification struct {
	Event    string               `json:"event"`
	Label    string               `json:"label"`
	Channels notificationChannels `json:"channels"`
}
type profileContact struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Verified *bool  `json:"verified"`
	Note     string `json:"note,omitempty"`
}

// SignedInAt and ExpiresAt are the times inside Sub, in RFC 3339 (PA-83);
// SignedInAt is null for a session that predates the sign-in record.
type profileSession struct {
	ID         string  `json:"id"`
	Device     *string `json:"device"`
	Sub        string  `json:"sub"`
	SignedInAt *string `json:"signed_in_at"`
	ExpiresAt  string  `json:"expires_at"`
	// Current marks the session making this request; always present (PA-69).
	Current bool `json:"current"`
}
type profileFactor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Sub  string `json:"sub"`
}

// A null MFA state, organization rule, factor list or session device is
// unknown; profileView.Unavailable names each one with its reason.
type profileSecurity struct {
	PasswordChanged string           `json:"password_changed"`
	MFAEnabled      *bool            `json:"mfa_enabled"`
	OrgRule         *string          `json:"org_rule"`
	Factors         []profileFactor  `json:"factors"`
	Sessions        []profileSession `json:"sessions"`
	Tokens          string           `json:"tokens"`
}
type profileView struct {
	Name          string                `json:"name"`
	DisplayName   string                `json:"display_name"`
	Department    string                `json:"department"`
	Title         string                `json:"title"`
	Timezone      string                `json:"timezone"`
	Contacts      []profileContact      `json:"contacts"`
	Notifications []profileNotification `json:"notifications"`
	Security      profileSecurity       `json:"security"`
	// Unavailable maps each unknown field path to its reason. A PUT may echo
	// it; the server ignores it and compares the read-only fields themselves.
	Unavailable map[string]string `json:"unavailable"`
}

func localize(r *http.Request, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		return en
	}
	return zh
}

func currentSessionHash(r *http.Request) string {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return ""
	}
	return store.Hash(cookie.Value)
}

func validNotifications(items []profileNotification) bool {
	if len(items) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.Event == "" || seen[item.Event] || len(item.Event) > 64 || utf8.RuneCountInString(item.Label) > 120 {
			return false
		}
		seen[item.Event] = true
	}
	return true
}

func (s *Service) putProfile(r *http.Request, input profileView) (profileView, error) {
	return s.profileLive(r, &input)
}

func equalContacts(a, b []profileContact) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Label != b[i].Label || a[i].Value != b[i].Value || a[i].Note != b[i].Note {
			return false
		}
		av, bv := a[i].Verified, b[i].Verified
		if (av == nil) != (bv == nil) || (av != nil && *av != *bv) {
			return false
		}
	}
	return true
}

func (s *Service) endSession(ctx context.Context, userID, currentHash, targetHash string) error {
	if len(targetHash) != 64 || targetHash == currentHash {
		return errInvalid
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND token_hash=$2`, userID, targetHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrNotFound
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.session.revoked',$2,'success','{}'::jsonb)`, userID, targetHash)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) registerProfile(api *httpapi.Server) {
	api.Register("GET /api/v1/me/profile", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.profileLive(r, nil)
		respondProfile(w, v, err)
	})
	api.Register("PUT /api/v1/me/profile", "viewer", func(w http.ResponseWriter, r *http.Request) {
		input, ok := decodeProfile(w, r)
		if !ok {
			return
		}
		v, err := s.putProfile(r, input)
		respondProfile(w, v, err)
	})
	api.Register("DELETE /api/v1/me/sessions/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie("aiops_session")
		if e != nil {
			httpapi.Error(w, 401, "unauthorized", "Login required")
			return
		}
		e = s.endSession(r.Context(), httpapi.CurrentUser(r).ID, store.Hash(cookie.Value), r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
