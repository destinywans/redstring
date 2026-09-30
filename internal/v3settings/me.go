package v3settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var ErrOncallUnavailable = errors.New("authoritative on-call evidence unavailable")
var errMeEvidenceUnavailable = errors.New("current account MFA evidence unavailable")
var errMeUnauthorized = errors.New("current login session is unavailable")

// OncallStatus describes one verified answer for the requested account and
// environments. Inactive is meaningful only when the source covers every
// requested environment and has a fresh observation.
type OncallStatus struct {
	Active          bool   `json:"active"`
	Until           string `json:"until,omitempty"`
	Schedule        string `json:"schedule,omitempty"`
	observedAt      time.Time
	validUntil      time.Time
	assignmentUntil time.Time
}

func (v OncallStatus) validAt(now time.Time) bool {
	return !v.observedAt.IsZero() && !v.validUntil.IsZero() &&
		!v.observedAt.Before(now.Add(-60*time.Second)) && !v.observedAt.After(now.Add(10*time.Second)) &&
		v.validUntil.After(now) && !v.validUntil.After(v.observedAt.Add(5*time.Minute)) &&
		(!v.Active || v.Schedule != "" && v.assignmentUntil.After(now))
}

type OncallSource interface {
	Current(context.Context, string, []string) (OncallStatus, error)
}

// MeEvidence binds self-account projections to the same instance namespace and
// relying-party ID used by the existing TOTP and passkey services.
type MeEvidence struct {
	MFAInstanceID string
	PasskeyRPID   string
	Oncall        OncallSource
}

type httpOncallSource struct {
	endpoint string
	token    string
	client   *http.Client
	now      func() time.Time
}

// NewOncallHTTPSource accepts only an explicit HTTPS authority. The bearer
// token is loaded from a protected file by startup code and never logged.
func NewOncallHTTPSource(endpoint, token string, client *http.Client) (OncallSource, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || strings.TrimSpace(token) == "" {
		return nil, ErrOncallUnavailable
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	if bounded.Timeout == 0 || bounded.Timeout > 3*time.Second {
		bounded.Timeout = 3 * time.Second
	}
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &httpOncallSource{endpoint: endpoint, token: strings.TrimSpace(token), client: &bounded, now: time.Now}, nil
}

// LoadOncallTokenFile reads a protected bearer token at startup. Its error
// never includes the path, token or file contents.
func LoadOncallTokenFile(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrOncallUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 4096 {
		return "", ErrOncallUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrOncallUnavailable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return "", ErrOncallUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return "", ErrOncallUnavailable
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return "", ErrOncallUnavailable
	}
	return token, nil
}

type oncallAssignment struct {
	EnvironmentID string `json:"environment_id"`
	Schedule      string `json:"schedule"`
	Until         string `json:"until"`
}

type oncallAnswer struct {
	UserID         string             `json:"user_id"`
	EnvironmentIDs []string           `json:"environment_ids"`
	ObservedAt     string             `json:"observed_at"`
	ValidUntil     string             `json:"valid_until"`
	Assignments    []oncallAssignment `json:"assignments"`
}

func (s *httpOncallSource) Current(ctx context.Context, userID string, environmentIDs []string) (OncallStatus, error) {
	if userID == "" || len(environmentIDs) == 0 || len(environmentIDs) > 32 {
		return OncallStatus{}, ErrOncallUnavailable
	}
	requestBody, _ := json.Marshal(map[string]any{"user_id": userID, "environment_ids": environmentIDs})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return OncallStatus{}, ErrOncallUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return OncallStatus{}, ErrOncallUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 65536 {
		return OncallStatus{}, ErrOncallUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return OncallStatus{}, ErrOncallUnavailable
	}
	var answer oncallAnswer
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil || decoder.Decode(new(any)) != io.EOF {
		return OncallStatus{}, ErrOncallUnavailable
	}
	if answer.UserID != userID || len(answer.EnvironmentIDs) != len(environmentIDs) || len(answer.Assignments) > 32 {
		return OncallStatus{}, ErrOncallUnavailable
	}
	coverage := make(map[string]bool, len(environmentIDs))
	for _, id := range environmentIDs {
		if id == "" || coverage[id] {
			return OncallStatus{}, ErrOncallUnavailable
		}
		coverage[id] = false
	}
	for _, id := range answer.EnvironmentIDs {
		if _, ok := coverage[id]; !ok || coverage[id] {
			return OncallStatus{}, ErrOncallUnavailable
		}
		coverage[id] = true
	}
	for _, seen := range coverage {
		if !seen {
			return OncallStatus{}, ErrOncallUnavailable
		}
	}
	observedAt, errObserved := time.Parse(time.RFC3339Nano, answer.ObservedAt)
	validUntil, errValid := time.Parse(time.RFC3339Nano, answer.ValidUntil)
	now := s.now()
	if errObserved != nil || errValid != nil || observedAt.Before(now.Add(-60*time.Second)) || observedAt.After(now.Add(10*time.Second)) || !validUntil.After(now) || validUntil.After(observedAt.Add(5*time.Minute)) {
		return OncallStatus{}, ErrOncallUnavailable
	}
	result := OncallStatus{observedAt: observedAt, validUntil: validUntil}
	for _, assignment := range answer.Assignments {
		until, err := time.Parse(time.RFC3339Nano, assignment.Until)
		if err != nil || !coverage[assignment.EnvironmentID] || strings.TrimSpace(assignment.Schedule) == "" || !until.After(now) || result.Active {
			return OncallStatus{}, ErrOncallUnavailable
		}
		result = OncallStatus{Active: true, Until: until.UTC().Format(time.RFC3339), Schedule: assignment.Schedule,
			observedAt: observedAt, validUntil: validUntil, assignmentUntil: until}
	}
	return result, nil
}

type meMFA struct {
	TOTP     bool `json:"totp"`
	Passkeys int  `json:"passkeys"`
}

type meView struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Email       string       `json:"email"`
	Phone       string       `json:"phone"`
	Title       string       `json:"title"`
	Team        string       `json:"team"`
	Department  string       `json:"department"`
	Timezone    string       `json:"timezone"`
	Oncall      OncallStatus `json:"oncall"`
	Roles       []string     `json:"roles"`
	MFA         meMFA        `json:"mfa"`
	Preferences Preferences  `json:"preferences"`
}

type mePatch struct {
	Name        *string
	Title       *string
	Timezone    *string
	Preferences preferencesPatch
	hasPrefs    bool
}

func decodeMePatch(w http.ResponseWriter, r *http.Request) (mePatch, bool) {
	var fields map[string]json.RawMessage
	if !httpapi.Decode(w, r, &fields) {
		return mePatch{}, false
	}
	if len(fields) == 0 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Expected editable account fields")
		return mePatch{}, false
	}
	var patch mePatch
	for key, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Account fields cannot be null")
			return mePatch{}, false
		}
		var target **string
		switch key {
		case "name":
			target = &patch.Name
		case "title":
			target = &patch.Title
		case "timezone":
			target = &patch.Timezone
		case "preferences":
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) != nil || len(nested) == 0 {
				httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Expected preference fields")
				return mePatch{}, false
			}
			for name, value := range nested {
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Preferences cannot be null")
					return mePatch{}, false
				}
				var err error
				switch name {
				case "theme":
					err = json.Unmarshal(value, &patch.Preferences.Theme)
				case "locale":
					err = json.Unmarshal(value, &patch.Preferences.Locale)
				case "density":
					err = json.Unmarshal(value, &patch.Preferences.Density)
				case "reduce_motion":
					err = json.Unmarshal(value, &patch.Preferences.ReduceMotion)
				default:
					err = errInvalid
				}
				if err != nil {
					httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Invalid preference field")
					return mePatch{}, false
				}
			}
			patch.hasPrefs = true
			continue
		default:
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Field is not self-editable")
			return mePatch{}, false
		}
		if err := json.Unmarshal(raw, target); err != nil || *target == nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Invalid account field type")
			return mePatch{}, false
		}
	}
	if patch.Name != nil && (strings.TrimSpace(*patch.Name) == "" || utf8.RuneCountInString(*patch.Name) > 120) ||
		patch.Title != nil && utf8.RuneCountInString(*patch.Title) > 120 ||
		patch.Timezone != nil && !validTimezone(*patch.Timezone) ||
		patch.Preferences.Theme != nil && *patch.Preferences.Theme != "light" && *patch.Preferences.Theme != "dark" && *patch.Preferences.Theme != "system" ||
		patch.Preferences.Locale != nil && *patch.Preferences.Locale != "zh-CN" && *patch.Preferences.Locale != "en-US" ||
		patch.Preferences.Density != nil && *patch.Preferences.Density != "comfortable" && *patch.Preferences.Density != "compact" {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Invalid account value")
		return mePatch{}, false
	}
	return patch, true
}

func (s *Service) meEvidence(r *http.Request) (OncallStatus, httpapi.V3EnvironmentScope, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		return OncallStatus{}, scope, store.ErrForbidden
	}
	if s.MeEvidence.MFAInstanceID == "" || s.MeEvidence.PasskeyRPID == "" {
		return OncallStatus{}, scope, errMeEvidenceUnavailable
	}
	if s.MeEvidence.Oncall == nil {
		return OncallStatus{}, scope, ErrOncallUnavailable
	}
	oncall, err := s.MeEvidence.Oncall.Current(r.Context(), httpapi.CurrentUser(r).ID, scope.EnvironmentIDs)
	if err == nil && !oncall.validAt(time.Now()) {
		err = ErrOncallUnavailable
	}
	return oncall, scope, err
}

func (s *Service) meTransaction(ctx context.Context, user core.User, sessionHash string, scope httpapi.V3EnvironmentScope, oncall OncallStatus, patch *mePatch) (meView, error) {
	if user.ID == "" || sessionHash == "" {
		return meView{}, store.ErrForbidden
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return meView{}, err
	}
	defer tx.Rollback(ctx)
	for _, key := range []int64{752018992, 752018995, 752018998} {
		lock := "SELECT pg_advisory_xact_lock_shared($1)"
		if patch != nil {
			lock = "SELECT pg_advisory_xact_lock($1)"
		}
		if _, err := tx.Exec(ctx, lock, key); err != nil {
			return meView{}, err
		}
	}
	// OIDC bind/unbind takes the account row before revoking its sessions.
	// Follow that order so a concurrent identity change cannot hold the user
	// while this transaction holds the session and waits for the user.
	accountLock := `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`
	if patch != nil {
		accountLock = `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR UPDATE`
	}
	var lockedUser string
	if err := tx.QueryRow(ctx, accountLock, user.ID).Scan(&lockedUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return meView{}, store.ErrForbidden
		}
		return meView{}, err
	}
	// Hold the session row through the reply or update. A logout or remote
	// revocation that gets the row first makes this fail; one that queues after
	// this read cannot invalidate authorization halfway through the transaction.
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`, sessionHash, user.ID).Scan(&expiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return meView{}, errMeUnauthorized
		}
		return meView{}, err
	}
	for _, environmentID := range scope.EnvironmentIDs {
		var permitted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1 AND g.module='environment' AND g.action='view' AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=$2)))`, user.ID, environmentID).Scan(&permitted); err != nil {
			return meView{}, err
		}
		if !permitted {
			return meView{}, store.ErrForbidden
		}
	}
	if patch != nil {
		if _, err := tx.Exec(ctx, `UPDATE users SET name=COALESCE($2,name),job_title=COALESCE($3,job_title),v3_timezone=COALESCE($4,v3_timezone) WHERE id=$1`, user.ID, patch.Name, patch.Title, patch.Timezone); err != nil {
			return meView{}, err
		}
		if patch.hasPrefs {
			if _, err := tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,theme,locale,density,reduce_motion)
 VALUES($1,COALESCE($2,'system'),COALESCE($3,'zh-CN'),COALESCE($4,'comfortable'),COALESCE($5,false))
 ON CONFLICT(user_id) DO UPDATE SET theme=COALESCE($2,v3_user_settings.theme),
 locale=COALESCE($3,v3_user_settings.locale),density=COALESCE($4,v3_user_settings.density),
 reduce_motion=COALESCE($5,v3_user_settings.reduce_motion),updated_at=now()`, user.ID,
				patch.Preferences.Theme, patch.Preferences.Locale, patch.Preferences.Density, patch.Preferences.ReduceMotion); err != nil {
				return meView{}, err
			}
		}
	}
	view, err := s.projectMe(ctx, tx, user.ID, oncall)
	if err != nil {
		return meView{}, err
	}
	if patch != nil {
		if !oncall.validAt(time.Now()) {
			return meView{}, ErrOncallUnavailable
		}
		changes := map[string]any{}
		if patch.Name != nil {
			changes["name"] = *patch.Name
		}
		if patch.Title != nil {
			changes["title"] = *patch.Title
		}
		if patch.Timezone != nil {
			changes["timezone"] = *patch.Timezone
		}
		if patch.hasPrefs {
			prefs := map[string]any{}
			if patch.Preferences.Theme != nil {
				prefs["theme"] = *patch.Preferences.Theme
			}
			if patch.Preferences.Locale != nil {
				prefs["locale"] = *patch.Preferences.Locale
			}
			if patch.Preferences.Density != nil {
				prefs["density"] = *patch.Preferences.Density
			}
			if patch.Preferences.ReduceMotion != nil {
				prefs["reduce_motion"] = *patch.Preferences.ReduceMotion
			}
			changes["preferences"] = prefs
		}
		if _, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.me.updated',$1,'success',$2)`, user.ID, mustJSON(changes)); err != nil {
			return meView{}, err
		}
	}
	if !time.Now().Before(expiresAt) {
		return meView{}, errMeUnauthorized
	}
	if !oncall.validAt(time.Now()) {
		return meView{}, ErrOncallUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return meView{}, err
	}
	if patch == nil && !time.Now().Before(expiresAt) {
		return meView{}, errMeUnauthorized
	}
	if patch == nil && !oncall.validAt(time.Now()) {
		return meView{}, ErrOncallUnavailable
	}
	return view, nil
}

func (s *Service) projectMe(ctx context.Context, tx pgx.Tx, userID string, oncall OncallStatus) (meView, error) {
	v := meView{Oncall: oncall, Roles: []string{}}
	var phone *string
	err := tx.QueryRow(ctx, `SELECT u.id,u.name,u.email,u.phone,u.job_title,COALESCE(t.name,''),u.department,u.v3_timezone,
 COALESCE(p.theme,'system'),COALESCE(p.locale,'zh-CN'),COALESCE(p.density,'comfortable'),COALESCE(p.reduce_motion,false)
 FROM users u LEFT JOIN teams t ON t.id=u.department_id LEFT JOIN v3_user_settings p ON p.user_id=u.id
 WHERE u.id=$1 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo' FOR SHARE OF u`, userID).
		Scan(&v.ID, &v.Name, &v.Email, &phone, &v.Title, &v.Team, &v.Department, &v.Timezone,
			&v.Preferences.Theme, &v.Preferences.Locale, &v.Preferences.Density, &v.Preferences.ReduceMotion)
	if errors.Is(err, pgx.ErrNoRows) {
		return meView{}, store.ErrForbidden
	}
	if err != nil {
		return meView{}, err
	}
	if phone != nil {
		v.Phone = *phone
	}
	if !validTimezone(v.Timezone) || v.Preferences.Theme != "system" && v.Preferences.Theme != "dark" && v.Preferences.Theme != "light" ||
		v.Preferences.Locale != "zh-CN" && v.Preferences.Locale != "en-US" ||
		v.Preferences.Density != "comfortable" && v.Preferences.Density != "compact" {
		return meView{}, errMeEvidenceUnavailable
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT a.role_id FROM (
 SELECT role_id FROM rbac_user_roles WHERE user_id=$1
 UNION SELECT dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id WHERE u.id=$1
 ) a JOIN rbac_roles r ON r.id=a.role_id ORDER BY a.role_id`, userID)
	if err != nil {
		return meView{}, err
	}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			rows.Close()
			return meView{}, err
		}
		v.Roles = append(v.Roles, role)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return meView{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_mfa_totp WHERE instance_id=$1 AND user_id=$2 AND state='active'),
 (SELECT count(*) FROM v3_passkey_credentials WHERE instance_id=$1 AND rp_id=$3 AND user_id=$2 AND revoked_at IS NULL)`,
		s.MeEvidence.MFAInstanceID, userID, s.MeEvidence.PasskeyRPID).Scan(&v.MFA.TOTP, &v.MFA.Passkeys); err != nil {
		return meView{}, err
	}
	return v, nil
}

func respondMe(w http.ResponseWriter, v meView, err error) {
	if errors.Is(err, errMeUnauthorized) {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
		return
	}
	if errors.Is(err, ErrOncallUnavailable) {
		httpapi.Error(w, http.StatusServiceUnavailable, "oncall_unavailable", "Authoritative on-call status is not configured or current")
		return
	}
	if errors.Is(err, errMeEvidenceUnavailable) {
		httpapi.Error(w, http.StatusServiceUnavailable, "identity_evidence_unavailable", "Current identity evidence is not configured")
		return
	}
	respond(w, v, err)
}

func (s *Service) getMeHTTP(w http.ResponseWriter, r *http.Request) {
	oncall, scope, err := s.meEvidence(r)
	if err != nil {
		respondMe(w, meView{}, err)
		return
	}
	v, err := s.meTransaction(r.Context(), httpapi.CurrentUser(r), currentSessionHash(r), scope, oncall, nil)
	respondMe(w, v, err)
}

func (s *Service) patchMeHTTP(w http.ResponseWriter, r *http.Request) {
	patch, ok := decodeMePatch(w, r)
	if !ok {
		return
	}
	oncall, scope, err := s.meEvidence(r)
	if err != nil {
		respondMe(w, meView{}, err)
		return
	}
	if scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_environment", "Select one environment")
		return
	}
	v, err := s.meTransaction(r.Context(), httpapi.CurrentUser(r), currentSessionHash(r), scope, oncall, &patch)
	respondMe(w, v, err)
}
