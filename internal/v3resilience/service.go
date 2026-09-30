// Package v3resilience owns the scoped service resilience read and human
// request producer. It does not control rollout, traffic, flags or autoscaling.
package v3resilience

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

// Reader must return one current, complete observation for the exact persisted
// application and environment. No reader is installed until all five upstream
// rule families have authoritative scoped adapters.
type Reader interface {
	ReadResilience(context.Context, string, string) (Observation, error)
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
	Reader Reader
	Now    func() time.Time
}

func New(db *pgxpool.Pool, access Access, reader Reader) *Service {
	return &Service{DB: db, Access: access, Reader: reader, Now: time.Now}
}

func (s *Service) Register(api *httpapi.Server) {
	const read = "GET /api/v1/services/{id}/resilience"
	const write = "POST /api/v1/services/{id}/resilience/proposals"
	api.Register(read, "viewer", s.Detail)
	api.MarkV3ObjectRoute(read, "services", "view")
	api.Register(write, "viewer", s.Propose)
	api.MarkV3ObjectRoute(write, "proposals", "change")
}

type target struct {
	ApplicationID string
	EnvironmentID string
	Name          string
	TeamID        string
}

func serviceIdentity(id string) (string, string, bool) {
	if id == "" || len(id) > 1024 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != id {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *Service) selected(r *http.Request) (target, bool) {
	app, env, valid := serviceIdentity(r.PathValue("id"))
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !valid || !ok || scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] != env {
		return target{}, false
	}
	return target{ApplicationID: app, EnvironmentID: env}, true
}

func (s *Service) loadTarget(ctx context.Context, tx pgx.Tx, t target) (target, error) {
	err := tx.QueryRow(ctx, `SELECT a.name,COALESCE(a.owner_team_id,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ve,e`, t.ApplicationID, t.EnvironmentID).Scan(&t.Name, &t.TeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return target{}, store.ErrNotFound
	}
	return t, err
}

func (s *Service) authorize(ctx context.Context, tx pgx.Tx, user core.User, t target, module, action string) error {
	d := rbac.V3Decision{Module: module, Action: action, ApplicationID: t.ApplicationID, EnvironmentID: t.EnvironmentID}
	err := s.Access.AuthorizeWithTx(ctx, tx, user, d)
	if errors.Is(err, store.ErrForbidden) && t.TeamID != "" {
		d.ApplicationID, d.TeamID = "", t.TeamID
		err = s.Access.AuthorizeWithTx(ctx, tx, user, d)
	}
	return err
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Service or investigation not found")
	case errors.Is(err, store.ErrForbidden), errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 403, "forbidden", "Current authorization is required")
	case errors.Is(err, store.ErrConflict):
		httpapi.Error(w, 409, "conflict", "Source or scope changed; refresh and retry")
	default:
		httpapi.Error(w, 503, "resilience_unavailable", "Required resilience source or storage is unavailable")
	}
}

func lockAuthorityTables(ctx context.Context, tx pgx.Tx) error {
	// Serialize grant changes across the short final authorization and
	// response/write window, including new role assignments.
	_, err := tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

func sessionCurrent(ctx context.Context, tx pgx.Tx, token, actorID string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(token), actorID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return store.ErrSessionRevoked
	}
	return err
}
