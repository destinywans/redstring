package v3settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
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
	Verified bool   `json:"verified"`
}
type profileSession struct {
	ID      string `json:"id"`
	Device  string `json:"device"`
	Sub     string `json:"sub"`
	Current bool   `json:"current,omitempty"`
}
type profileFactor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Sub  string `json:"sub"`
}
type profileSecurity struct {
	PasswordChanged string           `json:"password_changed"`
	MFAEnabled      bool             `json:"mfa_enabled"`
	OrgRule         string           `json:"org_rule"`
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
}

func localize(r *http.Request, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		return en
	}
	return zh
}

func (s *Service) getProfile(r *http.Request) (profileView, error) {
	ctx := r.Context()
	id := httpapi.CurrentUser(r).ID
	v := profileView{Contacts: []profileContact{}, Notifications: []profileNotification{}}
	v.Security.Factors = []profileFactor{}
	v.Security.Sessions = []profileSession{}
	var email, phone *string
	err := s.Store.DB.QueryRow(ctx, `SELECT u.name,u.v3_display_name,u.department,u.job_title,u.v3_timezone,u.email,u.phone FROM users u WHERE u.id=$1 AND NOT u.disabled AND u.deleted_at IS NULL`, id).Scan(&v.Name, &v.DisplayName, &v.Department, &v.Title, &v.Timezone, &email, &phone)
	if err != nil {
		return v, err
	}
	if email != nil && *email != "" {
		v.Contacts = append(v.Contacts, profileContact{Kind: "email", Label: localize(r, "邮箱", "Email"), Value: *email})
	}
	if phone != nil && *phone != "" {
		v.Contacts = append(v.Contacts, profileContact{Kind: "phone", Label: localize(r, "手机号", "Phone"), Value: *phone})
	}
	var raw []byte
	err = s.Store.DB.QueryRow(ctx, `SELECT profile_notifications FROM v3_user_settings WHERE user_id=$1`, id).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &v.Notifications); err != nil {
			return v, err
		}
	}
	var changed *time.Time
	err = s.Store.DB.QueryRow(ctx, `SELECT max(created_at) FROM management_audit WHERE actor_id=$1 AND action='profile.password.updated' AND outcome='success'`, id).Scan(&changed)
	if err != nil {
		return v, err
	}
	v.Security.PasswordChanged = localize(r, "密码修改时间未记录", "Password change time not recorded")
	if changed != nil {
		v.Security.PasswordChanged = changed.UTC().Format(time.RFC3339)
	}
	v.Security.OrgRule = localize(r, "二次验证尚未接入", "Two-factor authentication is not configured")
	v.Security.Tokens = localize(r, "个人令牌尚未接入", "Personal tokens are not configured")
	current := r.URL.Query().Get("current_session")
	if cookie, e := r.Cookie("aiops_session"); e == nil {
		current = store.Hash(cookie.Value)
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT token_hash,expires_at FROM sessions WHERE user_id=$1 AND expires_at>now() ORDER BY expires_at DESC,token_hash LIMIT 100`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid string
		var expires time.Time
		if err = rows.Scan(&sid, &expires); err != nil {
			return v, err
		}
		v.Security.Sessions = append(v.Security.Sessions, profileSession{ID: sid, Device: localize(r, "浏览器会话", "Browser session"), Sub: expires.UTC().Format(time.RFC3339), Current: sid == current})
	}
	return v, rows.Err()
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
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 120 || utf8.RuneCountInString(input.DisplayName) > 120 || utf8.RuneCountInString(input.Title) > 120 || !validTimezone(input.Timezone) || !validNotifications(input.Notifications) {
		return profileView{}, errInvalid
	}
	current, err := s.getProfile(r)
	if err != nil {
		return profileView{}, err
	}
	// Department, contacts and security are controlled by identity providers and
	// dedicated account flows. A profile PUT cannot mutate them by substitution.
	if input.Department != current.Department || !equalContacts(input.Contacts, current.Contacts) {
		return profileView{}, errInvalid
	}
	id := httpapi.CurrentUser(r).ID
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		return profileView{}, err
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `UPDATE users SET name=$2,v3_display_name=$3,job_title=$4,v3_timezone=$5 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL`, id, input.Name, input.DisplayName, input.Title, input.Timezone)
	if err != nil {
		return profileView{}, err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_user_settings(user_id,profile_notifications) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET profile_notifications=EXCLUDED.profile_notifications,updated_at=now()`, id, mustJSON(input.Notifications))
	if err != nil {
		return profileView{}, err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.profile.updated',$1,'success',$2)`, id, mustJSON(map[string]any{"name": input.Name, "display_name": input.DisplayName, "title": input.Title, "timezone": input.Timezone, "notification_events": len(input.Notifications)}))
	if err != nil {
		return profileView{}, err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return profileView{}, err
	}
	return s.getProfile(r)
}

func equalContacts(a, b []profileContact) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
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
	// ProfileView requires an authoritative MFA state. Until factor enrollment
	// is connected, do not report the Go zero value (false) as proof of no MFA.
	api.Register("GET /api/v1/me/profile", "viewer", func(w http.ResponseWriter, r *http.Request) {
		respond(w, nil, errIdentityUnavailable)
	})
	api.Register("PUT /api/v1/me/profile", "viewer", func(w http.ResponseWriter, r *http.Request) {
		// No partial profile write followed by a response with fabricated
		// security fields. The session-revocation route remains independent.
		respond(w, nil, errIdentityUnavailable)
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
