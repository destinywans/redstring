// Package v3tokens issues environment-bound personal API tokens. No common
// Bearer authentication path consumes them until the V3 composition root
// wires the transaction verifier into object-level authorization.
package v3tokens

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

const (
	maxActiveTokens = 50
	listLimit       = 100
)

var (
	ErrInvalidInput = errors.New("invalid API token request")
	ErrInvalidToken = errors.New("invalid or expired API token")
	ErrForbidden    = errors.New("API token action forbidden")
	ErrTokenLimit   = errors.New("active API token limit reached")
	ErrUnavailable  = errors.New("API token storage or policy unavailable")
	tokenID         = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// These are deliberately read-only V3 matrix cells. A mock UI scope such as
// changes:write is never accepted as authority.
var allowedScopes = map[string]string{
	"services:view":   "services",
	"events:view":     "events",
	"connectors:view": "connectors",
	"resources:view":  "resources",
	"logs:view":       "logs",
	"traces:view":     "traces",
}

var allowedTTL = map[int64]bool{
	3600: true, 86400: true, 604800: true, 2592000: true,
}

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
}

func New(db *pgxpool.Pool, access Access) *Service {
	return &Service{DB: db, Access: access}
}

// Actor is supplied by the cookie-authenticated V3 handler, then rechecked
// against the same raw session and persisted human identity in each action.
type Actor struct {
	User          core.User
	SessionToken  string
	EnvironmentID string
}

type IssueInput struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	TTL    int64    `json:"ttl"` // seconds; one of the four fixed durations.
}

type IssueResult struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	EnvironmentID   string    `json:"environment_id"`
	Scopes          []string  `json:"scopes"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	SecretShownOnce string    `json:"secret_shown_once"`
}

type TokenView struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	EnvironmentID string     `json:"environment_id"`
	Scopes        []string   `json:"scopes"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
}

type TokenList struct {
	Items   []TokenView `json:"items"`
	HasMore bool        `json:"has_more"`
}

func validName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name ||
		!utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 || len(name) > 200 {
		return false
	}
	for _, ch := range name {
		if unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch) {
			return false
		}
	}
	return true
}

func validateIssue(in IssueInput) ([]string, error) {
	if !validName(in.Name) || !allowedTTL[in.TTL] || len(in.Scopes) == 0 || len(in.Scopes) > len(allowedScopes) {
		return nil, ErrInvalidInput
	}
	seen := make(map[string]bool, len(in.Scopes))
	for _, scope := range in.Scopes {
		if allowedScopes[scope] == "" || seen[scope] {
			return nil, ErrInvalidInput
		}
		seen[scope] = true
	}
	result := append([]string(nil), in.Scopes...)
	sort.Strings(result)
	return result, nil
}

func validActor(a Actor) bool {
	return a.User.ID != "" && !a.User.Disabled && a.SessionToken != "" &&
		a.EnvironmentID != "" && a.EnvironmentID != "all" &&
		strings.TrimSpace(a.EnvironmentID) == a.EnvironmentID
}

// Governance writers use the exclusive form of these advisory keys. Taking
// the shared form first, then locking grant tables, the user and session
// prevents a role/account revoke from racing the issuance commit.
func lockGovernance(ctx context.Context, tx pgx.Tx) error {
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,
	 v3_role_grants,rbac_roles IN SHARE MODE`)
	return err
}

func currentHuman(ctx context.Context, tx pgx.Tx, a Actor) error {
	if !validActor(a) {
		return store.ErrSessionRevoked
	}
	if err := store.LockActiveSession(ctx, tx, a.SessionToken, a.User.ID); err != nil {
		return err
	}
	var kind, source string
	err := tx.QueryRow(ctx, `SELECT v3_identity_kind,source FROM users WHERE id=$1`, a.User.ID).Scan(&kind, &source)
	if err != nil {
		return err
	}
	if kind != "human" || source == "settings_demo" {
		return ErrForbidden
	}
	return nil
}

func sessionStillActive(ctx context.Context, tx pgx.Tx, a Actor) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
	 WHERE token_hash=$1 AND user_id=$2`, store.Hash(a.SessionToken), a.User.ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return store.ErrSessionRevoked
	}
	return err
}

func (s *Service) ready() bool {
	return s != nil && s.DB != nil && s.Access != nil
}
