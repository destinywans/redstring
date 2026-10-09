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
	Verified *bool  `json:"verified"`
	Note     string `json:"note,omitempty"`
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
	MFAEnabled      *bool            `json:"mfa_enabled"`
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
	v.Security.OrgRule = localize(r, "组织 MFA 策略状态不可用", "Organization MFA policy status unavailable")
	v.Security.Tokens = localize(r, "个人令牌尚未接入", "Personal tokens are not configured")
	current := currentSessionHash(r)
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
		v.Security.Sessions = append(v.Security.Sessions, profileSession{ID: sid, Device: localize(r, "设备信息未记录", "Device not recorded"), Sub: localize(r, "到期时间 ", "Expires ") + expires.UTC().Format(time.RFC3339), Current: sid == current})
	}
	if err := rows.Err(); err != nil {
		return v, err
	}
	// The persisted user and session rows do not establish contact verification,
	// usable MFA factors, or a session's login method/device. Their nil values are
	// explicitly unknown. ProfileView requires booleans, so this projection must
	// not be served or used as the basis of a partial PUT until the evidence and
	// permission checks are connected.
	return v, errIdentityUnavailable
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
		if s.ProfileEvidence == nil || s.ProfileInstanceID == "" {
			respondProfile(w, profileView{}, errIdentityUnavailable)
			return
		}
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
