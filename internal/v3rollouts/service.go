package v3rollouts

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3kube"
	"github.com/destinywans/redstring/internal/v3proposals"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Access is the transactional V3 policy.
type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

// MetricReader evaluates reviewed Prometheus queries at one instant for
// verification.
type MetricReader interface {
	QueryForExecutor(ctx context.Context, app, env, connectorID, resourceID, queryID string,
		at time.Time) ([]connectors.MetricSeries, error)
}

// Service holds the rollback dependencies. Credentials is the approved
// connector credential resolver (reads); Secrets reads the executor's write
// credential named by a write binding and is never given to readers.
type Service struct {
	DB            *pgxpool.Pool
	Tier          string
	Access        Access
	Registry      *v3telemetry.Resolver
	Credentials   v3integrations.CredentialResolver
	Secrets       v3integrations.CredentialResolver
	WriteBindings []WriteBinding
	Proposals     *v3proposals.Service
	Metrics       MetricReader
	Now           func() time.Time
	// RevisionTimes says when each revision took effect (PA-94); the
	// composition root reads it from the change service. Nil leaves the
	// ReplicaSet-creation rule.
	RevisionTimes func(ctx context.Context, user core.User, app, env string) (map[string]RevisionTime, error)

	// Executor timing; zero values use the defaults below.
	PollInterval   time.Duration
	SampleInterval time.Duration
	Settle         time.Duration
	RolloutTimeout time.Duration

	workerID string
	running  atomic.Bool
	beat     atomic.Int64

	credMu      sync.Mutex
	credChecked map[string]credentialCheck
}

// credentialCheck caches whether a write credential was readable, so the
// readiness check does not read Vault on every request.
type credentialCheck struct {
	ok bool
	at time.Time
}

const credentialCheckTTL = 15 * time.Second

const (
	defaultPollInterval   = 3 * time.Second
	defaultSampleInterval = 15 * time.Second
	defaultSettle         = 60 * time.Second
	defaultRolloutTimeout = 10 * time.Minute
	maxSampleAge          = 60.0
)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func or(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// minimumObservation is the tier rule: production observes at least 300 s,
// nonprod at least 180 s; a longer catalog value wins.
func (s *Service) minimumObservation(catalogSeconds int) int {
	floor := 180
	if s.Tier == "production" {
		floor = 300
	}
	if catalogSeconds > floor {
		return catalogSeconds
	}
	return floor
}

var (
	errNotFound  = errors.New("not found")
	errForbidden = store.ErrForbidden
)

// decodeServiceID splits base64url(application "\0" environment).
func decodeServiceID(id string) (string, string, bool) {
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

// ServiceID encodes the service ID of an application and environment.
func ServiceID(app, env string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(app + "\x00" + env))
}

// ownerTeamTx reads the application's owner team, requiring the application
// to be active and mapped to the environment. Writers lock the application
// row; read-only transactions cannot.
func ownerTeamTx(ctx context.Context, tx pgx.Tx, app, env string) (string, error) {
	return ownerTeam(ctx, tx, app, env, " FOR SHARE OF a")
}

func ownerTeamReadTx(ctx context.Context, tx pgx.Tx, app, env string) (string, error) {
	return ownerTeam(ctx, tx, app, env, "")
}

func ownerTeam(ctx context.Context, tx pgx.Tx, app, env, lock string) (string, error) {
	var team string
	err := tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived`+lock, app, env).Scan(&team)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return team, err
}

// authorizeTx checks every module/action on the application, falling back to
// the owner team, like the other V3 domains.
func (s *Service) authorizeTx(ctx context.Context, tx pgx.Tx, user core.User, app, env, team string, checks ...[2]string) error {
	for _, c := range checks {
		decision := rbac.V3Decision{Module: c[0], Action: c[1], EnvironmentID: env, ApplicationID: app}
		err := s.Access.AuthorizeWithTx(ctx, tx, user, decision)
		if errors.Is(err, store.ErrForbidden) && team != "" {
			decision.ApplicationID, decision.TeamID = "", team
			err = s.Access.AuthorizeWithTx(ctx, tx, user, decision)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// readClient builds a client with the connector's approved read credential
// and the CA stored next to it.
func (s *Service) readClient(ctx context.Context, env string, w v3telemetry.Workload) (*v3kube.Client, error) {
	if s.Credentials == nil || w.AuthMethod != "bearer" || w.CredentialRef == "" {
		return nil, v3integrations.ErrCredentialUnavailable
	}
	use := v3integrations.WithCredentialUse(ctx, v3integrations.CredentialUse{Tier: s.Tier, EnvironmentID: env,
		ConnectorID: w.ConnectorID, Provider: v3telemetry.WorkloadProvider, Endpoint: w.Endpoint})
	token, err := s.Credentials.Resolve(use, w.CredentialRef)
	if err != nil || token == "" {
		return nil, v3integrations.ErrCredentialUnavailable
	}
	return v3kube.New(w.Endpoint, token, s.clusterCA(use, w.CredentialRef))
}

// clusterCA reads the optional "ca" field next to the connector credential.
func (s *Service) clusterCA(ctx context.Context, reference string) string {
	if siblings, ok := s.Credentials.(v3integrations.SiblingResolver); ok {
		if pem, err := siblings.ResolveSibling(ctx, reference, "ca"); err == nil {
			return pem
		}
	}
	return ""
}

// writeClient builds the executor's client from a write binding. The write
// token comes from Secrets (the raw store: the binding is its approval); the
// CA is the connector's.
func (s *Service) writeClient(ctx context.Context, env string, w v3telemetry.Workload, b WriteBinding) (*v3kube.Client, error) {
	if s.Secrets == nil {
		return nil, v3integrations.ErrCredentialUnavailable
	}
	token, err := s.Secrets.Resolve(ctx, b.CredentialRef)
	if err != nil || token == "" {
		return nil, v3integrations.ErrCredentialUnavailable
	}
	use := v3integrations.WithCredentialUse(ctx, v3integrations.CredentialUse{Tier: s.Tier, EnvironmentID: env,
		ConnectorID: w.ConnectorID, Provider: v3telemetry.WorkloadProvider, Endpoint: w.Endpoint})
	return v3kube.New(w.Endpoint, token, s.clusterCA(use, w.CredentialRef))
}

// bindingFingerprint covers what a proposal was built from: the connector
// configuration, the target, the reviewed scope and the write binding.
func bindingFingerprint(w v3telemetry.Workload, b WriteBinding) string {
	policy := ""
	if w.Scope.Rollback != nil {
		p := *w.Scope.Rollback
		v := p.Verification
		policy = digestOf(boolString(p.Allowed), itoa(p.ObservationSeconds), v.ConnectorID, v.ResourceID,
			v.RequestRateQueryID, v.ErrorRatioQueryID, v.SampleAgeQueryID, ftoa(v.MaxErrorRatio), ftoa(v.MinRequestRate))
	}
	return digestOf(w.TargetID, w.ConnectorID, w.Endpoint, w.AuthMethod, w.CredentialRef, w.ResourceID,
		w.Scope.Namespace, w.Scope.Deployment, policy, b.fingerprint())
}

// Ready reports that the executor loop is running and recently polled, and
// that the environment has write bindings whose write credentials are
// readable (PA-08 approval gate). An emptied Vault therefore blocks approval
// instead of failing the execution after it.
func (s *Service) Ready(ctx context.Context, env string) error {
	if s == nil || !s.running.Load() || time.Since(time.Unix(0, s.beat.Load())) > 75*time.Second {
		return errExecutorNotReady
	}
	found := false
	for _, b := range s.WriteBindings {
		if b.Tier != s.Tier || b.Environment != env {
			continue
		}
		found = true
		if !s.writeCredentialReadable(ctx, b.CredentialRef) {
			return errExecutorNotReady
		}
	}
	if !found {
		return errExecutorNotReady
	}
	return nil
}

func (s *Service) writeCredentialReadable(ctx context.Context, reference string) bool {
	now := s.now()
	s.credMu.Lock()
	check, cached := s.credChecked[reference]
	s.credMu.Unlock()
	if cached && now.Sub(check.at) < credentialCheckTTL && !now.Before(check.at) {
		return check.ok
	}
	ok := false
	if s.Secrets != nil {
		token, err := s.Secrets.Resolve(ctx, reference)
		ok = err == nil && token != ""
	}
	s.credMu.Lock()
	if s.credChecked == nil {
		s.credChecked = map[string]credentialCheck{}
	}
	s.credChecked[reference] = credentialCheck{ok: ok, at: now}
	s.credMu.Unlock()
	return ok
}

var errExecutorNotReady = errors.New("rollback executor not ready")
