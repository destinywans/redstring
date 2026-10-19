package v3admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errInvitationUnavailable = errors.New("member invitation delivery unavailable")
var errInvitationInvalid = errors.New("member invitation code is invalid or expired")
var errInvitationConflict = errors.New("member already exists")
var errInvitationLimited = errors.New("member invitation rate limit reached")
var errInvitationRoleUnsafe = errors.New("viewer role has unsafe grants")

// An accepted SMTP relay handoff is delivery evidence for this API. It is not
// proof that the final mailbox accepted or displayed the message.
type InvitationMessage struct {
	Email     string
	Code      string
	EntryURL  string
	ExpiresAt time.Time
}

type InvitationSender interface {
	SendInvitation(context.Context, InvitationMessage) error
}

type invitationAttempt struct {
	Window time.Time
	Count  int
}

// InvitationService is configured explicitly by the composition root. The
// login entry is HTTPS and contains no bearer code in its URL.
type InvitationService struct {
	Sender   InvitationSender
	EntryURL string
	mu       sync.Mutex
	attempts map[string]invitationAttempt
}

func NewInvitationService(sender InvitationSender, entryURL string) (*InvitationService, error) {
	u, err := url.Parse(entryURL)
	if sender == nil || err != nil || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errInvitationUnavailable
	}
	return &InvitationService{Sender: sender, EntryURL: u.String(), attempts: map[string]invitationAttempt{}}, nil
}

// This local limit supplements the 256-bit one-time code. Multi-instance
// deployments must also limit anonymous requests at their trusted ingress.
func (v *InvitationService) allowAccept(remote string) bool {
	if v == nil {
		return false
	}
	key := remote
	if host, _, err := net.SplitHostPort(remote); err == nil && host != "" {
		key = host
	}
	now := time.Now()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.attempts == nil {
		v.attempts = map[string]invitationAttempt{}
	}
	if len(v.attempts) > 4096 {
		for k, old := range v.attempts {
			if now.Sub(old.Window) >= time.Minute {
				delete(v.attempts, k)
			}
		}
		if len(v.attempts) > 4096 {
			return false
		}
	}
	entry := v.attempts[key]
	if now.Sub(entry.Window) >= time.Minute {
		entry = invitationAttempt{Window: now}
	}
	entry.Count++
	v.attempts[key] = entry
	return entry.Count <= 10
}

type invitationInput struct {
	Email string
	Role  string
}

type invitationAcceptance struct {
	Code     string
	Name     string
	Password string
}

// Decode one flat object without allowing duplicate keys, nulls, trailing
// JSON, unknown fields, or an unbounded request body.
func decodeInvitationObject(w http.ResponseWriter, r *http.Request, allowed map[string]bool) (map[string]string, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("expected object")
	}
	values := map[string]string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok || !allowed[key] {
			return nil, errors.New("unknown field")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("duplicate field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || string(raw) == "null" {
			return nil, errors.New("invalid field")
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return nil, errors.New("invalid field")
		}
		values[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("incomplete object")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return values, nil
}

func readInvitationInput(w http.ResponseWriter, r *http.Request) (invitationInput, error) {
	var out invitationInput
	values, err := decodeInvitationObject(w, r, map[string]bool{"email": true, "role": true})
	if err != nil {
		return out, err
	}
	out.Email = strings.ToLower(strings.TrimSpace(values["email"]))
	parsed, err := mail.ParseAddress(out.Email)
	if err != nil || parsed.Address != out.Email || len(out.Email) > 320 ||
		strings.ContainsAny(out.Email, "\r\n") {
		return out, errors.New("invalid email")
	}
	out.Role, _ = values["role"]
	if _, supplied := values["role"]; !supplied {
		out.Role = "viewer"
	}
	// The frozen UI offers read-only invitations. Grant assignment needs a
	// separate reviewed action; a caller cannot request administrator access.
	if out.Role != "viewer" {
		return out, errors.New("invitation role must be viewer")
	}
	return out, nil
}

func readInvitationAcceptance(w http.ResponseWriter, r *http.Request) (invitationAcceptance, error) {
	var out invitationAcceptance
	values, err := decodeInvitationObject(w, r, map[string]bool{"code": true, "name": true, "password": true})
	if err != nil {
		return out, err
	}
	out.Code = values["code"]
	raw, err := base64.RawURLEncoding.DecodeString(out.Code)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != out.Code {
		return out, errors.New("invalid code")
	}
	out.Name = strings.TrimSpace(values["name"])
	if out.Name == "" || utf8.RuneCountInString(out.Name) > 120 ||
		strings.IndexFunc(out.Name, unicode.IsControl) >= 0 {
		return out, errors.New("invalid name")
	}
	out.Password = values["password"]
	if len(out.Password) < 12 || len(out.Password) > 1024 {
		return out, errors.New("invalid password")
	}
	return out, nil
}

func invitationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Login required")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Insufficient permission")
	case errors.Is(err, errInvitationConflict):
		httpapi.Error(w, 409, "invitation_conflict", "An account already uses this email")
	case errors.Is(err, errInvitationInvalid):
		httpapi.Error(w, 409, "invitation_invalid", "Invitation code is invalid or expired")
	case errors.Is(err, errInvitationLimited):
		httpapi.Error(w, 429, "invitation_rate_limited", "Try again later")
	case errors.Is(err, errInvitationRoleUnsafe):
		httpapi.Error(w, 409, "invitation_role_unsafe", "Read-only viewer role must be reviewed before inviting")
	default:
		httpapi.Error(w, 503, "invitation_unavailable", "Invitation delivery or account storage is unavailable")
	}
}

func (s *Service) inviteMember(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok || len(envs) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment")
		return
	}
	input, err := readInvitationInput(w, r)
	if err != nil {
		httpapi.Error(w, 422, "invalid_invitation", "Expected an email and optional viewer role")
		return
	}
	actor := httpapi.CurrentUser(r)
	token := roleSessionToken(r)
	id, code, expiry, err := s.prepareInvitation(r.Context(), actor, token, envs[0], input)
	if err != nil {
		invitationError(w, err)
		return
	}
	message := InvitationMessage{Email: input.Email, Code: code,
		EntryURL: s.Invitations.EntryURL, ExpiresAt: expiry}
	if err := s.Invitations.Sender.SendInvitation(r.Context(), message); err != nil {
		s.abandonInvitation(id)
		invitationError(w, errInvitationUnavailable)
		return
	}
	if err := s.confirmInvitationDelivery(r.Context(), actor, token, envs[0], id); err != nil {
		invitationError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Service) authorizeInvitation(ctx context.Context, tx pgx.Tx, actor core.User, token, environment string) error {
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return err
	}
	if err := (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(ctx, tx, actor,
		rbac.V3Decision{Module: "members", Action: "manage", EnvironmentID: environment}); err != nil {
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1
 AND v3_identity_kind='human' AND source<>'settings_demo')
 AND EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1 AND module='members'
 AND action='manage' AND scope_kind='global' AND scope_id='*')
 AND EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1 AND module='environment'
 AND action='view' AND scope_kind='global' AND scope_id='*')`, actor.ID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return store.ErrForbidden
	}
	return nil
}

// Viewer is editable in the role UI. Inspect the actual role before sending
// a code and again when consuming it; the identity and matrix locks serialize
// this check with the existing role and permission writers. The one
// environment grant allowed is the instance-wide environment.view the
// built-in viewer carries since PA-55 (decision D6).
func safeInvitationViewer(ctx context.Context, tx pgx.Tx) (bool, error) {
	var safe bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM rbac_roles WHERE id='viewer' AND builtin AND v3_kind='system')
 AND NOT EXISTS(SELECT 1 FROM v3_role_grants
  WHERE role_id='viewer' AND
   ((module='environment' AND NOT (scope_kind='global' AND scope_id='*'))
    OR action<>'view' OR scope_kind IN ('application','team')))
 AND NOT EXISTS(SELECT 1 FROM rbac_role_permissions rp
  JOIN rbac_permission_resources pr ON pr.permission_id=rp.permission_id
  WHERE rp.role_id='viewer' AND
   (pr.action<>'view' OR pr.scope_type NOT IN ('resource','legacy_application')))`).Scan(&safe)
	return safe, err
}

func safeAcceptedMember(ctx context.Context, tx pgx.Tx, accountID string) (bool, error) {
	var safe bool
	err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*)=1 FROM rbac_user_roles WHERE user_id=$1)
 AND EXISTS(SELECT 1 FROM rbac_user_roles WHERE user_id=$1
  AND role_id='viewer' AND origin='legacy')
 AND EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='viewer'
  AND department_id IS NULL AND v3_identity_kind='human' AND source='local')
 AND NOT EXISTS(SELECT 1 FROM team_members WHERE user_id=$1)
 AND NOT EXISTS(SELECT 1 FROM application_grants
  WHERE subject_type='user' AND subject_id=$1)
 AND NOT EXISTS(SELECT 1 FROM v3_effective_role_grants
  WHERE user_id=$1 AND
   ((module='environment' AND NOT (scope_kind='global' AND scope_id='*'))
    OR action<>'view' OR scope_kind IN ('application','team')))
 AND NOT EXISTS(SELECT 1 FROM rbac_effective_permissions
  WHERE user_id=$1 AND
   (action<>'view' OR scope_type NOT IN ('resource','legacy_application')))`, accountID).Scan(&safe)
	return safe, err
}

func (s *Service) prepareInvitation(ctx context.Context, actor core.User, token, environment string, input invitationInput) (id, code string, expires time.Time, err error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer tx.Rollback(ctx)
	if err = memberIdentityLocks(ctx, tx, true); err != nil {
		return "", "", time.Time{}, err
	}
	if err = s.authorizeInvitation(ctx, tx, actor, token, environment); err != nil {
		return "", "", time.Time{}, err
	}
	if s.Invitations == nil || s.Invitations.Sender == nil {
		return "", "", time.Time{}, errInvitationUnavailable
	}
	safe, err := safeInvitationViewer(ctx, tx)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if !safe {
		return "", "", time.Time{}, errInvitationRoleUnsafe
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, input.Email).Scan(&exists); err != nil {
		return "", "", time.Time{}, err
	}
	if exists {
		return "", "", time.Time{}, errInvitationConflict
	}
	var recentActor, recentEmail int
	if err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM v3_member_invitations WHERE inviter_id=$1
  AND created_at>clock_timestamp()-interval '1 hour'),
 (SELECT count(*) FROM v3_member_invitations WHERE email=$2
  AND created_at>clock_timestamp()-interval '1 day')`, actor.ID, input.Email).Scan(&recentActor, &recentEmail); err != nil {
		return "", "", time.Time{}, err
	}
	if recentActor >= 20 || recentEmail >= 5 {
		return "", "", time.Time{}, errInvitationLimited
	}
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", "", time.Time{}, err
	}
	code = base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256(raw[:])
	id = store.ID()
	// A crashed or uncertain sender leaves a pending code unusable. Once its
	// bounded delivery window passes it can be superseded safely.
	if _, err = tx.Exec(ctx, `UPDATE v3_member_invitations SET status='superseded'
 WHERE email=$1 AND status='pending'
 AND created_at<clock_timestamp()-interval '2 minutes'`, input.Email); err != nil {
		return "", "", time.Time{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_member_invitations
 WHERE email=$1 AND status IN ('pending','delivered') AND expires_at>clock_timestamp())`,
		input.Email).Scan(&exists); err != nil {
		return "", "", time.Time{}, err
	}
	if exists {
		return "", "", time.Time{}, errInvitationConflict
	}
	// An expired delivered code may be replaced; it is never accepted after
	// its expiry and no second relay handoff is concurrent with this insert.
	if _, err = tx.Exec(ctx, `UPDATE v3_member_invitations SET status='superseded'
 WHERE email=$1 AND status='delivered' AND expires_at<=clock_timestamp()`, input.Email); err != nil {
		return "", "", time.Time{}, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO v3_member_invitations
 (id,email,role,environment_id,inviter_id,token_hash,status,expires_at)
 VALUES($1,$2,'viewer',$3,$4,$5,'pending',clock_timestamp()+interval '24 hours')
 RETURNING expires_at`, id, input.Email, environment, actor.ID, hash[:]).Scan(&expires); err != nil {
		return "", "", time.Time{}, err
	}
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return "", "", time.Time{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", time.Time{}, err
	}
	return id, code, expires, nil
}

func (s *Service) abandonInvitation(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A failed cleanup is safe: pending codes cannot be accepted and a
	// subsequent request may supersede the orphan after two minutes.
	_, _ = s.Store.DB.Exec(ctx, `UPDATE v3_member_invitations SET status='superseded'
 WHERE id=$1 AND status='pending'`, id)
}

func (s *Service) confirmInvitationDelivery(ctx context.Context, actor core.User, token, environment, id string) error {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = memberIdentityLocks(ctx, tx, true); err != nil {
		return err
	}
	if err = s.authorizeInvitation(ctx, tx, actor, token, environment); err != nil {
		return err
	}
	var changed string
	err = tx.QueryRow(ctx, `UPDATE v3_member_invitations SET status='delivered',delivered_at=clock_timestamp()
 WHERE id=$1 AND inviter_id=$2 AND environment_id=$3 AND status='pending'
 AND expires_at>clock_timestamp() RETURNING id`, id, actor.ID, environment).Scan(&changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvitationInvalid
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.member.invitation.relay_accepted',$2,'success',jsonb_build_object('environment',$3::text))`,
		actor.ID, id, environment); err != nil {
		return err
	}
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) acceptMemberInvitation(w http.ResponseWriter, r *http.Request) {
	if s.Invitations == nil {
		invitationError(w, errInvitationUnavailable)
		return
	}
	if !s.Invitations.allowAccept(r.RemoteAddr) {
		invitationError(w, errInvitationLimited)
		return
	}
	input, err := readInvitationAcceptance(w, r)
	if err != nil {
		httpapi.Error(w, 422, "invalid_invitation_acceptance", "Expected a valid code, name and password of at least 12 bytes")
		return
	}
	if err = s.acceptInvitation(r.Context(), input); err != nil {
		invitationError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"ok": true, "access": "pending_scope_grant"})
}

func (s *Service) acceptInvitation(ctx context.Context, input invitationAcceptance) error {
	raw, _ := base64.RawURLEncoding.DecodeString(input.Code)
	hash := sha256.Sum256(raw)
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = memberIdentityLocks(ctx, tx, true); err != nil {
		return err
	}
	var id, email, role, status string
	var fresh bool
	err = tx.QueryRow(ctx, `SELECT id,email,role,status,expires_at>clock_timestamp()
 FROM v3_member_invitations WHERE token_hash=$1 FOR UPDATE`, hash[:]).
		Scan(&id, &email, &role, &status, &fresh)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvitationInvalid
	}
	if err != nil {
		return err
	}
	if status != "delivered" || !fresh || role != "viewer" {
		return errInvitationInvalid
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, email).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errInvitationConflict
	}
	roleSafe, err := safeInvitationViewer(ctx, tx)
	if err != nil {
		return err
	}
	if !roleSafe {
		return errInvitationRoleUnsafe
	}
	var timezone, locale string
	if err = tx.QueryRow(ctx, `SELECT timezone,language FROM workspace_settings
 WHERE id='default' FOR SHARE`).Scan(&timezone, &locale); err != nil {
		return errInvitationUnavailable
	}
	if _, err = time.LoadLocation(timezone); err != nil || locale != "zh-CN" && locale != "en-US" {
		return errInvitationUnavailable
	}
	accountID := store.ID()
	if _, err = tx.Exec(ctx, `INSERT INTO users
 (id,email,name,role,password_hash,source,disabled,v3_timezone)
 VALUES($1,$2,$3,'viewer',$4,'local',false,$5)`,
		accountID, email, input.Name, store.PasswordHash(input.Password), timezone); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return errInvitationConflict
		}
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,locale) VALUES($1,$2)`, accountID, locale); err != nil {
		return err
	}
	accountSafe, err := safeAcceptedMember(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if !accountSafe {
		return errInvitationRoleUnsafe
	}
	var consumed string
	err = tx.QueryRow(ctx, `UPDATE v3_member_invitations SET status='accepted',account_id=$2,
 accepted_at=clock_timestamp() WHERE id=$1 AND status='delivered'
 AND expires_at>clock_timestamp() RETURNING id`, id, accountID).Scan(&consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvitationInvalid
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.member.invitation.accepted',$1,'success',jsonb_build_object('invitation_id',$2::text,
 'access','pending_scope_grant'))`, accountID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
