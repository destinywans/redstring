package v3settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// ProfileEvidenceSource is a trusted authority for fields that the account
// database cannot prove. Its answer must cover the exact account, environment,
// stored contacts and active session IDs requested here. Session IDs are hashes;
// the raw login cookie is never sent to the source.
type ProfileEvidenceSource interface {
	Snapshot(context.Context, ProfileEvidenceRequest) (ProfileEvidenceSnapshot, error)
}

type ProfileEvidenceContact struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type ProfileEvidenceRequest struct {
	InstanceID       string                   `json:"instance_id"`
	UserID           string                   `json:"user_id"`
	EnvironmentID    string                   `json:"environment_id"`
	CurrentSessionID string                   `json:"current_session_id"`
	Contacts         []ProfileEvidenceContact `json:"contacts"`
	SessionIDs       []string                 `json:"session_ids"`
}

type ProfileVerifiedContact struct {
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Label    string `json:"label,omitempty"`
	Note     string `json:"note,omitempty"`
	Verified bool   `json:"verified"`
}

type ProfileSessionEvidence struct {
	ID          string `json:"id"`
	Device      string `json:"device"`
	LoginMethod string `json:"login_method"`
}

type ProfileFactorEvidence struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Sub  string `json:"sub"`
}

type ProfileEvidenceSnapshot struct {
	InstanceID    string                   `json:"instance_id"`
	UserID        string                   `json:"user_id"`
	EnvironmentID string                   `json:"environment_id"`
	Complete      bool                     `json:"complete"`
	ObservedAt    time.Time                `json:"observed_at"`
	ValidUntil    time.Time                `json:"valid_until"`
	Contacts      []ProfileVerifiedContact `json:"contacts"`
	MFAEnabled    bool                     `json:"mfa_enabled"`
	OrgRule       string                   `json:"org_rule"`
	Factors       []ProfileFactorEvidence  `json:"factors"`
	Sessions      []ProfileSessionEvidence `json:"sessions"`
}

func validProfileEvidence(req ProfileEvidenceRequest, answer ProfileEvidenceSnapshot, now time.Time) bool {
	if req.InstanceID == "" || req.UserID == "" || req.EnvironmentID == "" || req.CurrentSessionID == "" ||
		!answer.Complete || answer.InstanceID != req.InstanceID || answer.UserID != req.UserID || answer.EnvironmentID != req.EnvironmentID ||
		answer.ObservedAt.IsZero() || answer.ValidUntil.IsZero() || answer.ObservedAt.Before(now.Add(-60*time.Second)) ||
		answer.ObservedAt.After(now.Add(10*time.Second)) || !answer.ValidUntil.After(now) || !answer.ValidUntil.After(answer.ObservedAt) ||
		answer.ValidUntil.After(answer.ObservedAt.Add(5*time.Minute)) ||
		answer.Contacts == nil || answer.Factors == nil || answer.Sessions == nil ||
		strings.TrimSpace(answer.OrgRule) == "" || utf8.RuneCountInString(answer.OrgRule) > 500 ||
		len(answer.Contacts) < len(req.Contacts) || len(answer.Contacts) > len(req.Contacts)+1 ||
		len(answer.Sessions) != len(req.SessionIDs) || len(answer.Factors) > 20 {
		return false
	}
	contacts := make(map[string]bool, len(req.Contacts))
	for _, contact := range req.Contacts {
		if contact.Kind != "email" && contact.Kind != "phone" || contact.Value == "" || utf8.RuneCountInString(contact.Value) > 320 {
			return false
		}
		contacts[contact.Kind+"\x00"+contact.Value] = false
	}
	for _, contact := range answer.Contacts {
		key := contact.Kind + "\x00" + contact.Value
		seen, ok := contacts[key]
		if utf8.RuneCountInString(contact.Note) > 120 || utf8.RuneCountInString(contact.Label) > 120 {
			return false
		}
		if contact.Kind == "feishu" && !ok && strings.TrimSpace(contact.Value) != "" &&
			utf8.RuneCountInString(contact.Value) <= 320 {
			contacts[key] = true
			continue
		}
		if !ok || seen {
			return false
		}
		contacts[key] = true
	}
	for _, seen := range contacts {
		if !seen {
			return false
		}
	}
	sessions := make(map[string]bool, len(req.SessionIDs))
	for _, id := range req.SessionIDs {
		if _, exists := sessions[id]; len(id) != 64 || exists {
			return false
		}
		sessions[id] = false
	}
	for _, session := range answer.Sessions {
		seen, ok := sessions[session.ID]
		if !ok || seen || strings.TrimSpace(session.Device) == "" || utf8.RuneCountInString(session.Device) > 120 ||
			(session.LoginMethod != "password" && session.LoginMethod != "oidc" && session.LoginMethod != "passkey") {
			return false
		}
		sessions[session.ID] = true
	}
	for _, seen := range sessions {
		if !seen {
			return false
		}
	}
	activeFactor := false
	totpFactor := false
	recoveryFactor := false
	factorKeys := make(map[string]bool, len(answer.Factors))
	for _, factor := range answer.Factors {
		key := factor.Kind + "\x00" + factor.Name + "\x00" + factor.Sub
		if factor.Kind != "totp" && factor.Kind != "passkey" && factor.Kind != "recovery" ||
			strings.TrimSpace(factor.Name) == "" || strings.TrimSpace(factor.Sub) == "" ||
			utf8.RuneCountInString(factor.Name) > 120 || utf8.RuneCountInString(factor.Sub) > 240 || factorKeys[key] {
			return false
		}
		factorKeys[key] = true
		if factor.Kind == "totp" || factor.Kind == "passkey" {
			activeFactor = true
		}
		if factor.Kind == "totp" {
			totpFactor = true
		}
		if factor.Kind == "recovery" {
			recoveryFactor = true
		}
	}
	return answer.MFAEnabled == activeFactor && (!recoveryFactor || totpFactor)
}

type profileQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type profileAccount struct {
	Name, DisplayName, Department, Title, Timezone string
	Email, Phone                                   *string
}

func readProfileAccount(ctx context.Context, q profileQuerier, userID string) (profileAccount, error) {
	var a profileAccount
	err := q.QueryRow(ctx, `SELECT name,v3_display_name,department,job_title,v3_timezone,email,phone
 FROM users WHERE id=$1 AND v3_identity_kind='human' AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo'`, userID).
		Scan(&a.Name, &a.DisplayName, &a.Department, &a.Title, &a.Timezone, &a.Email, &a.Phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return profileAccount{}, store.ErrForbidden
	}
	return a, err
}

type profileSessionRow struct {
	ID        string
	ExpiresAt time.Time
	IssuedAt  *time.Time
}

func readProfileSessions(ctx context.Context, q profileQuerier, userID string, locked bool) ([]profileSessionRow, error) {
	// Newest sign-in first (PA-69); sessions predating issued_at sort last.
	query := `SELECT token_hash,expires_at,issued_at FROM sessions WHERE user_id=$1 AND expires_at>clock_timestamp()
 ORDER BY issued_at DESC NULLS LAST,token_hash LIMIT 101`
	if locked {
		query += ` FOR SHARE`
	}
	rows, err := q.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]profileSessionRow, 0)
	for rows.Next() {
		var row profileSessionRow
		if err := rows.Scan(&row.ID, &row.ExpiresAt, &row.IssuedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > 100 {
		return nil, errIdentityUnavailable
	}
	return result, nil
}

func profileContacts(a profileAccount) []ProfileEvidenceContact {
	contacts := make([]ProfileEvidenceContact, 0, 2)
	if a.Email != nil && *a.Email != "" {
		contacts = append(contacts, ProfileEvidenceContact{Kind: "email", Value: *a.Email})
	}
	if a.Phone != nil && *a.Phone != "" {
		contacts = append(contacts, ProfileEvidenceContact{Kind: "phone", Value: *a.Phone})
	}
	return contacts
}

func profileSessionIDs(rows []profileSessionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func profileIncludesSession(rows []profileSessionRow, currentID string) bool {
	for _, row := range rows {
		if row.ID == currentID {
			return true
		}
	}
	return false
}

// profileLive serves the account fields this instance proves and adds the
// identity evidence fields only when a configured source currently proves
// them. Unknown fields are null and named in Unavailable; they never become
// false, empty or a placeholder text.
func (s *Service) profileLive(r *http.Request, input *profileView) (profileView, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		return profileView{}, errInvalid
	}
	userID, currentID := httpapi.CurrentUser(r).ID, currentSessionHash(r)
	if userID == "" || currentID == "" {
		return profileView{}, errMeUnauthorized
	}
	account, err := readProfileAccount(r.Context(), s.Store.DB, userID)
	if err != nil {
		return profileView{}, err
	}
	sessionRows, err := readProfileSessions(r.Context(), s.Store.DB, userID, false)
	if err != nil {
		return profileView{}, err
	}
	if !profileIncludesSession(sessionRows, currentID) {
		return profileView{}, errMeUnauthorized
	}
	request := ProfileEvidenceRequest{InstanceID: s.ProfileInstanceID, UserID: userID,
		EnvironmentID: scope.EnvironmentIDs[0], CurrentSessionID: currentID,
		Contacts: profileContacts(account), SessionIDs: profileSessionIDs(sessionRows)}
	answer, reason, err := s.profileEvidence(r.Context(), request)
	if err != nil {
		return profileView{}, err
	}
	return s.profileTransaction(r.Context(), r, request, answer, reason, input)
}

// profileEvidence returns a currently valid snapshot, or nil and the reason
// it is unknown. Only a cancelled request is an error.
func (s *Service) profileEvidence(ctx context.Context, request ProfileEvidenceRequest) (*ProfileEvidenceSnapshot, string, error) {
	// The snapshot's factors are cross-checked against local enrollment, so
	// it needs the same instance and passkey relying party as /me.
	if s.ProfileEvidence == nil || s.ProfileInstanceID == "" ||
		s.ProfileInstanceID != s.MeEvidence.MFAInstanceID || s.MeEvidence.PasskeyRPID == "" {
		return nil, "not_configured", nil
	}
	answer, err := s.ProfileEvidence.Snapshot(ctx, request)
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err != nil || !validProfileEvidence(request, answer, time.Now()) {
		return nil, "unavailable", nil
	}
	return &answer, "", nil
}

func (s *Service) profileTransaction(ctx context.Context, r *http.Request, request ProfileEvidenceRequest, answer *ProfileEvidenceSnapshot, reason string, input *profileView) (profileView, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return profileView{}, err
	}
	defer tx.Rollback(ctx)
	for _, key := range []int64{752018992, 752018995, 752018998} {
		lock := `SELECT pg_advisory_xact_lock_shared($1)`
		if input != nil {
			lock = `SELECT pg_advisory_xact_lock($1)`
		}
		if _, err := tx.Exec(ctx, lock, key); err != nil {
			return profileView{}, err
		}
	}
	// Account before session matches password changes, disable, OIDC binding and
	// login. The locks keep those changes from invalidating this transaction.
	accountLock := `SELECT id FROM users WHERE id=$1 AND v3_identity_kind='human' AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`
	if input != nil {
		accountLock = `SELECT id FROM users WHERE id=$1 AND v3_identity_kind='human' AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR UPDATE`
	}
	var activeID string
	if err := tx.QueryRow(ctx, accountLock, request.UserID).Scan(&activeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return profileView{}, store.ErrForbidden
		}
		return profileView{}, err
	}
	var currentExpires time.Time
	if err := tx.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`,
		request.CurrentSessionID, request.UserID).Scan(&currentExpires); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return profileView{}, errMeUnauthorized
		}
		return profileView{}, err
	}
	var permitted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$1 AND g.module='environment' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=$2)))`,
		request.UserID, request.EnvironmentID).Scan(&permitted); err != nil {
		return profileView{}, err
	}
	if !permitted {
		return profileView{}, store.ErrForbidden
	}
	account, err := readProfileAccount(ctx, tx, request.UserID)
	if err != nil {
		return profileView{}, err
	}
	sessions, err := readProfileSessions(ctx, tx, request.UserID, true)
	if err != nil {
		return profileView{}, err
	}
	// A snapshot that no longer covers the locked contacts and sessions, or
	// whose validity ended, is stale: its fields fall back to unknown.
	if answer != nil && (!reflect.DeepEqual(profileContacts(account), request.Contacts) ||
		!reflect.DeepEqual(profileSessionIDs(sessions), request.SessionIDs) ||
		!validProfileEvidence(request, *answer, time.Now())) {
		answer, reason = nil, "stale"
	}
	// /me reads these registrations from this same instance and is the MFA
	// authority. A remote factor snapshot may add verified descriptions, but
	// cannot contradict local enrollment or the two account pages would
	// assert opposite MFA states.
	var mfaEnabled *bool
	if s.MeEvidence.MFAInstanceID != "" && s.MeEvidence.PasskeyRPID != "" {
		var localTOTP bool
		var localPasskeys int
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_mfa_totp WHERE instance_id=$1 AND user_id=$2 AND state='active'),
 (SELECT count(*) FROM v3_passkey_credentials WHERE instance_id=$1 AND rp_id=$3 AND user_id=$2 AND revoked_at IS NULL)`,
			s.MeEvidence.MFAInstanceID, request.UserID, s.MeEvidence.PasskeyRPID).Scan(&localTOTP, &localPasskeys); err != nil {
			return profileView{}, err
		}
		enabled := localTOTP || localPasskeys > 0
		mfaEnabled = &enabled
		if answer != nil {
			factorTOTP, factorPasskeys := 0, 0
			for _, factor := range answer.Factors {
				switch factor.Kind {
				case "totp":
					factorTOTP++
				case "passkey":
					factorPasskeys++
				}
			}
			if (localTOTP && factorTOTP != 1) || (!localTOTP && factorTOTP != 0) || factorPasskeys != localPasskeys {
				answer, reason = nil, "unavailable"
			}
		}
	}
	v, err := profileViewFromRows(ctx, r, tx, request, account, sessions, answer, reason, mfaEnabled)
	if err != nil {
		return profileView{}, err
	}
	if input != nil {
		// Read-only fields, including unknown ones, must equal the current
		// view; the echoed unavailable map itself is ignored.
		if !validEditableProfile(*input) || input.Department != v.Department ||
			!equalContacts(input.Contacts, v.Contacts) || !reflect.DeepEqual(input.Security, v.Security) {
			return profileView{}, errInvalid
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET name=$2,v3_display_name=$3,job_title=$4,v3_timezone=$5 WHERE id=$1`,
			request.UserID, input.Name, input.DisplayName, input.Title, input.Timezone); err != nil {
			return profileView{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,profile_notifications) VALUES($1,$2)
 ON CONFLICT(user_id) DO UPDATE SET profile_notifications=EXCLUDED.profile_notifications,updated_at=now()`,
			request.UserID, mustJSON(input.Notifications)); err != nil {
			return profileView{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.profile.updated',$1,'success',$2)`, request.UserID,
			mustJSON(map[string]any{"fields": []string{"name", "display_name", "title", "timezone", "notifications"},
				"notification_events": len(input.Notifications)})); err != nil {
			return profileView{}, err
		}
		v.Name, v.DisplayName, v.Title, v.Timezone, v.Notifications =
			input.Name, input.DisplayName, input.Title, input.Timezone, input.Notifications
	}
	if !time.Now().Before(currentExpires) {
		return profileView{}, errMeUnauthorized
	}
	if err := tx.Commit(ctx); err != nil {
		return profileView{}, err
	}
	// Evidence that expired while the transaction ran is not returned.
	if answer != nil && !validProfileEvidence(request, *answer, time.Now()) {
		v.withoutEvidence(r, account, sessions, request.CurrentSessionID, "stale")
	}
	return v, nil
}

// withoutEvidence replaces every evidence-derived field with its unknown
// form: stored contacts without verification, no IdP-linked contact, no
// organization rule or factor list, and sessions without a device.
func (v *profileView) withoutEvidence(r *http.Request, a profileAccount, sessions []profileSessionRow, currentID, reason string) {
	v.Contacts = make([]profileContact, 0, 2)
	for _, contact := range profileContacts(a) {
		label := localize(r, "邮箱", "Email")
		if contact.Kind == "phone" {
			label = localize(r, "手机号", "Phone")
		}
		v.Contacts = append(v.Contacts, profileContact{Kind: contact.Kind, Label: label, Value: contact.Value})
	}
	v.Security.OrgRule, v.Security.Factors = nil, nil
	v.Security.Sessions = make([]profileSession, 0, len(sessions))
	for _, row := range sessions {
		v.Security.Sessions = append(v.Security.Sessions, profileSession{ID: row.ID,
			Sub:        sessionSignedIn(r, row) + localize(r, "到期时间 ", "Expires ") + row.ExpiresAt.UTC().Format(time.RFC3339),
			SignedInAt: sessionSignedInAt(row), ExpiresAt: row.ExpiresAt.UTC().Format(time.RFC3339),
			Current: row.ID == currentID})
	}
	currentSessionFirst(v.Security.Sessions)
	if len(v.Contacts) > 0 {
		v.Unavailable["contacts[].verified"] = reason
	}
	v.Unavailable["contacts.feishu"] = reason
	v.Unavailable["security.org_rule"] = reason
	v.Unavailable["security.factors"] = reason
	v.Unavailable["security.sessions[].device"] = reason
}

func profileViewFromRows(ctx context.Context, r *http.Request, tx pgx.Tx, request ProfileEvidenceRequest, a profileAccount, sessions []profileSessionRow, answer *ProfileEvidenceSnapshot, reason string, mfaEnabled *bool) (profileView, error) {
	v := profileView{Name: a.Name, DisplayName: a.DisplayName, Department: a.Department, Title: a.Title,
		Timezone: a.Timezone, Contacts: []profileContact{}, Notifications: []profileNotification{},
		Unavailable: map[string]string{}}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT profile_notifications FROM v3_user_settings WHERE user_id=$1`, request.UserID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return profileView{}, err
	}
	if len(raw) > 0 {
		if !profileNoDuplicateKeys(raw) || !profileArrayFieldsPresent(raw, "event", "label", "channels") ||
			!profileNotificationChannelsPresent(raw) || strictProfileJSON(raw, &v.Notifications) != nil ||
			v.Notifications == nil || !validNotifications(v.Notifications) {
			return profileView{}, errIdentityUnavailable
		}
	}
	var changed *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(created_at) FROM management_audit WHERE actor_id=$1 AND action='profile.password.updated' AND outcome='success'`,
		request.UserID).Scan(&changed); err != nil {
		return profileView{}, err
	}
	v.Security.PasswordChanged = localize(r, "密码修改时间未记录", "Password change time not recorded")
	if changed != nil {
		v.Security.PasswordChanged = changed.UTC().Format(time.RFC3339)
	}
	v.Security.MFAEnabled = mfaEnabled
	if mfaEnabled == nil {
		v.Unavailable["security.mfa_enabled"] = "not_configured"
	}
	v.Security.Tokens = localize(r, "个人令牌尚未接入", "Personal tokens are not configured")
	if answer == nil {
		v.withoutEvidence(r, a, sessions, request.CurrentSessionID, reason)
		if !validEditableProfile(v) {
			return profileView{}, errIdentityUnavailable
		}
		return v, nil
	}
	for _, expected := range request.Contacts {
		for _, actual := range answer.Contacts {
			if expected.Kind == actual.Kind && expected.Value == actual.Value {
				label := localize(r, "邮箱", "Email")
				if expected.Kind == "phone" {
					label = localize(r, "手机号", "Phone")
				}
				verified := actual.Verified
				v.Contacts = append(v.Contacts, profileContact{Kind: actual.Kind, Label: label, Value: actual.Value,
					Verified: &verified, Note: actual.Note})
				break
			}
		}
	}
	for _, actual := range answer.Contacts {
		if actual.Kind == "feishu" {
			verified := actual.Verified
			v.Contacts = append(v.Contacts, profileContact{Kind: "feishu", Label: localize(r, "飞书", "Feishu"),
				Value: actual.Value, Verified: &verified, Note: actual.Note})
		}
	}
	orgRule := answer.OrgRule
	v.Security.OrgRule = &orgRule
	v.Security.Factors = make([]profileFactor, 0, len(answer.Factors))
	for _, factor := range answer.Factors {
		v.Security.Factors = append(v.Security.Factors, profileFactor{Kind: factor.Kind, Name: factor.Name, Sub: factor.Sub})
	}
	v.Security.Sessions = make([]profileSession, 0, len(sessions))
	for _, row := range sessions {
		for _, evidence := range answer.Sessions {
			if row.ID == evidence.ID {
				method := evidence.LoginMethod
				switch method {
				case "password":
					method = localize(r, "密码登录", "Password login")
				case "oidc":
					method = localize(r, "单点登录", "Single sign-on")
				case "passkey":
					method = localize(r, "通行密钥登录", "Passkey login")
				}
				device := evidence.Device
				v.Security.Sessions = append(v.Security.Sessions, profileSession{ID: row.ID, Device: &device,
					Sub: method + " · " + sessionSignedIn(r, row) + localize(r, "到期时间 ", "Expires ") +
						row.ExpiresAt.UTC().Format(time.RFC3339),
					SignedInAt: sessionSignedInAt(row), ExpiresAt: row.ExpiresAt.UTC().Format(time.RFC3339),
					Current: row.ID == request.CurrentSessionID})
				break
			}
		}
	}
	currentSessionFirst(v.Security.Sessions)
	if !validEditableProfile(v) {
		return profileView{}, errIdentityUnavailable
	}
	return v, nil
}

// sessionSignedIn is the sign-in time of a session, the closest recorded
// activity: requests do not record a last-active time (PA-69).
func sessionSignedIn(r *http.Request, row profileSessionRow) string {
	if row.IssuedAt == nil {
		return ""
	}
	return localize(r, "登录于 ", "Signed in ") + row.IssuedAt.UTC().Format(time.RFC3339) + " · "
}

// sessionSignedInAt is the sign-in time in RFC 3339, null when not recorded.
func sessionSignedInAt(row profileSessionRow) *string {
	if row.IssuedAt == nil {
		return nil
	}
	at := row.IssuedAt.UTC().Format(time.RFC3339)
	return &at
}

// currentSessionFirst moves the requesting session to the top, keeping the
// newest-sign-in order of the others.
func currentSessionFirst(sessions []profileSession) {
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Current && !sessions[j].Current })
}

func validEditableProfile(v profileView) bool {
	return strings.TrimSpace(v.Name) != "" && utf8.RuneCountInString(v.Name) <= 120 &&
		utf8.RuneCountInString(v.DisplayName) <= 120 && utf8.RuneCountInString(v.Title) <= 120 &&
		validTimezone(v.Timezone) && v.Notifications != nil && validNotifications(v.Notifications)
}

func decodeProfile(w http.ResponseWriter, r *http.Request) (profileView, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var raw json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil || decoder.Decode(new(any)) != io.EOF ||
		len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || !profileNoDuplicateKeys(raw) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Expected one complete profile object")
		return profileView{}, false
	}
	// The body is the full view from GET. Unknown fields stay null and must
	// still match; the echoed unavailable map is optional and ignored.
	fields, ok := profileObject(raw, []string{"name", "display_name", "department", "title", "timezone", "contacts", "notifications", "security"},
		nil, []string{"unavailable"})
	var securityFields map[string]json.RawMessage
	if ok {
		securityFields, ok = profileObject(fields["security"], []string{"password_changed", "mfa_enabled", "org_rule", "factors", "sessions", "tokens"},
			[]string{"mfa_enabled", "org_rule", "factors"}, nil)
	}
	if ok {
		if unavailable, present := fields["unavailable"]; present {
			var reasons map[string]string
			ok = json.Unmarshal(unavailable, &reasons) == nil && reasons != nil && len(reasons) <= 32
		}
	}
	if !ok || !profileItems(fields["contacts"], []string{"kind", "label", "value", "verified"}, []string{"verified"}) ||
		!profileArrayFieldsPresent(fields["notifications"], "event", "label", "channels") ||
		!(isJSONNull(securityFields["factors"]) || profileArrayFieldsPresent(securityFields["factors"], "kind", "name", "sub")) ||
		// signed_in_at and expires_at (PA-83) may be echoed or left out.
		!profileItems(securityFields["sessions"], []string{"id", "device", "sub"}, []string{"device", "signed_in_at"}, "signed_in_at", "expires_at") ||
		!profileNotificationChannelsPresent(fields["notifications"]) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Missing, duplicate, or invalid profile field")
		return profileView{}, false
	}
	var v profileView
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&v); err != nil || v.Contacts == nil ||
		v.Security.Sessions == nil || v.Notifications == nil || !validEditableProfile(v) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Invalid profile value")
		return profileView{}, false
	}
	return v, true
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// profileObject requires every listed key, admits only the optional extras,
// and allows null only for the nullable keys.
func profileObject(raw []byte, required, nullable, optional []string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, name := range optional {
		allowed[name] = true
	}
	for _, name := range required {
		allowed[name] = true
		if _, present := fields[name]; !present {
			return nil, false
		}
	}
	nulls := map[string]bool{}
	for _, name := range nullable {
		nulls[name] = true
	}
	for name, value := range fields {
		if !allowed[name] || isJSONNull(value) && !nulls[name] {
			return nil, false
		}
	}
	return fields, true
}

// profileItems is profileArrayFieldsPresent for arrays whose listed nullable
// keys must be present but may hold an explicit null.
// profileItems checks an array of objects with the required fields; note,
// current and the extra fields may be left out, and only nullable ones may be
// null.
func profileItems(raw json.RawMessage, required, nullable []string, extra ...string) bool {
	var items []json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || json.Unmarshal(raw, &items) != nil || items == nil {
		return false
	}
	for _, item := range items {
		fields, ok := profileObject(item, required, nullable, append([]string{"note", "current"}, extra...))
		if !ok {
			return false
		}
		for _, name := range []string{"note", "current"} {
			if value, present := fields[name]; present && isJSONNull(value) {
				return false
			}
		}
	}
	return true
}

func profileNotificationChannelsPresent(raw json.RawMessage) bool {
	var rows []map[string]json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || rows == nil {
		return false
	}
	for _, row := range rows {
		if _, err := requiredWorkspaceFields(row["channels"], []string{"feishu", "email", "sms", "phone"}); err != nil {
			return false
		}
	}
	return true
}

func respondProfile(w http.ResponseWriter, v profileView, err error) {
	if err == nil {
		w.Header().Set("Cache-Control", "private, no-store")
		httpapi.WriteJSON(w, http.StatusOK, v)
		return
	}
	switch {
	case errors.Is(err, errMeUnauthorized):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Current environment permission required")
	case errors.Is(err, errInvalid):
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_settings", "Read-only profile fields changed or editable values invalid")
	case errors.Is(err, errIdentityUnavailable):
		httpapi.Error(w, http.StatusServiceUnavailable, "identity_evidence_unavailable", "Complete current profile evidence is not configured or available")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "settings_unavailable", "Settings storage unavailable")
	}
}
