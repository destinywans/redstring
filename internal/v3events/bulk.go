package v3events

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3proposals"
)

const (
	bulkMaxIDs        = 20
	bulkMaxCandidates = 5000 // Same complete-scan ceiling as GET /alert-groups.
	bulkMaxMembers    = 100
)

var bulkGroupID = regexp.MustCompile(`^group:[0-9a-f]{24}$`)
var errHumanRequired = errors.New("human session required")
var errBulkGroupTooLarge = errors.New("alert group exceeds bulk member limit")
var errBulkNotConnected = errors.New("bulk action source not connected")
var errInvestigationRequired = errors.New("open linked investigation required")

type BulkAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

// BulkSilenceSource is trusted process configuration. ApplicationBindings is
// provider-label -> persisted application ID, never a client-supplied mapping.
type BulkSilenceSource struct {
	Proposals           *v3proposals.Service
	Ledger              *v3alertmanager.DatabaseSilenceLedger
	Client              *v3alertmanager.Client
	ApplicationBindings map[string]map[string]string
	EnvironmentLabels   map[string]string
	IngressSources      []operations.Source
	// GroupIdentityBindings are operator-reviewed assertions about a dedicated
	// label produced upstream and preserved in authenticated webhook labels.
	// The value must be unique to the full local group tuple across every alert
	// on the bound Alertmanager endpoint, including future alerts, and must not
	// be reused while a resulting silence can remain active. A fingerprint
	// field in an inbound envelope is not this label.
	GroupIdentityBindings map[string]map[string]map[string]BulkGroupIdentityBinding // environment -> ingress source -> application
	// AlertRangeCovered is the operator-attested source binding from the
	// oldest active occurrence through now. It must honor context cancellation
	// and use only local/configured storage reads, never network I/O. A
	// nil/empty result fails closed.
	AlertRangeCovered func(context.Context, string, string, time.Time, time.Time) string
}

type BulkGroupIdentityBinding struct {
	LabelName                  string
	ConfigRevision             string    // Reviewed producer/forwarder configuration revision.
	AttestationDigest          string    // SHA-256 of the reviewed uniqueness and propagation contract.
	EndpointBindingFingerprint string    // Must match the current silence proposal endpoint binding.
	Attestation                string    // Exact value: upstream-equality-unique-stable-v1.
	ExpiresAt                  time.Time // Must cover at least the next one-hour silence.
}

type BulkService struct {
	DB      *pgxpool.Pool
	Access  BulkAccess
	Silence *BulkSilenceSource
}

func NewBulk(db *pgxpool.Pool, access BulkAccess, silence *BulkSilenceSource) *BulkService {
	return &BulkService{DB: db, Access: access, Silence: silence}
}

// Register owns the new route only. The composition root must call this after
// V3 environment configuration; no legacy route has this path.
func (b *BulkService) Register(api *httpapi.Server) {
	api.Register("POST /api/v1/alert-groups/bulk", "viewer", b.Bulk)
	api.MarkV3ObjectRoute("POST /api/v1/alert-groups/bulk", "events", "change")
}

type bulkRequest struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

type bulkResult struct {
	ID              string  `json:"id"`
	Outcome         string  `json:"outcome"`
	Matched         int     `json:"matched"`
	Changed         int     `json:"changed"`
	InvestigationID *string `json:"investigation_id"`
	ProposalID      *string `json:"proposal_id"`
	Error           *string `json:"error"`
}

func bulkFailed(id, outcome string, matched int) bulkResult {
	return bulkResult{ID: id, Outcome: outcome, Matched: matched, Error: &outcome}
}

func bulkSucceeded(id, outcome string, matched, changed int) bulkResult {
	return bulkResult{ID: id, Outcome: outcome, Matched: matched, Changed: changed}
}

func validBulkRequest(in bulkRequest) bool {
	if in.Action != "claim" && in.Action != "silence" && in.Action != "investigate" ||
		len(in.IDs) < 1 || len(in.IDs) > bulkMaxIDs {
		return false
	}
	seen := make(map[string]bool, len(in.IDs))
	for _, id := range in.IDs {
		if !bulkGroupID.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

type groupKey struct {
	EnvironmentID string
	ApplicationID string
	SourceID      string
	Fingerprint   string
}

// resolveBulkGroups scans the same complete, bounded persisted candidate set
// used by the read page. A truncation is an error, never a false 404.
func (b *BulkService) resolveBulkGroups(ctx context.Context, env string) (map[string]groupKey, map[string]bool, error) {
	rows, err := b.DB.Query(ctx, `SELECT e.id,i.application_id,i.source_id,i.fingerprint FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 WHERE e.key=$1 ORDER BY i.updated_at DESC,i.id DESC LIMIT 5001`, env)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	groups := make(map[string]groupKey)
	ambiguous := make(map[string]bool)
	count := 0
	for rows.Next() {
		count++
		if count > bulkMaxCandidates {
			return nil, nil, errTooManyIncidents
		}
		var key groupKey
		if err = rows.Scan(&key.EnvironmentID, &key.ApplicationID, &key.SourceID, &key.Fingerprint); err != nil {
			return nil, nil, err
		}
		id := localAlertGroupID(incidentRecord{EnvironmentID: key.EnvironmentID,
			ApplicationID: key.ApplicationID, SourceID: key.SourceID, Fingerprint: key.Fingerprint})
		if old, present := groups[id]; present && old != key {
			ambiguous[id] = true
		}
		groups[id] = key
	}
	return groups, ambiguous, rows.Err()
}

func (b *BulkService) Bulk(w http.ResponseWriter, r *http.Request) {
	if b == nil || b.DB == nil || b.Access == nil {
		httpapi.Error(w, 503, "events_unavailable", "Bulk event storage or policy is unavailable")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 422, "environment_required", "Select one environment")
		return
	}
	if scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, 409, "env_view_only", "Select one environment for bulk actions")
		return
	}
	var in bulkRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !validBulkRequest(in) {
		httpapi.Error(w, 422, "invalid_bulk_request", "Choose 1–20 distinct group IDs and claim, silence, or investigate")
		return
	}
	env := scope.EnvironmentIDs[0]
	groups, ambiguous, err := b.resolveBulkGroups(r.Context(), env)
	if err != nil {
		if errors.Is(err, errTooManyIncidents) {
			httpapi.Error(w, 503, "pagination_required", "Alert groups exceed the bounded candidate scan")
		} else {
			httpapi.Error(w, 503, "events_unavailable", "Alert group source is unavailable")
		}
		return
	}
	results := make([]bulkResult, 0, len(in.IDs))
	status := http.StatusOK
	for _, id := range in.IDs {
		key, found := groups[id]
		var result bulkResult
		switch {
		case !found:
			result = bulkFailed(id, "not_found", 0)
		case ambiguous[id]:
			result = bulkFailed(id, "unavailable", 0)
		default:
			result = b.applyBulkGroup(r, env, id, key, in.Action)
		}
		if result.Error != nil {
			status = http.StatusMultiStatus
		}
		results = append(results, result)
	}
	httpapi.WriteJSON(w, status, map[string]any{"action": in.Action, "results": results})
}

func authorizeBulkGroup(ctx context.Context, tx pgx.Tx, access BulkAccess, user core.User,
	app, team, env string) error {
	return authorizeBulkModule(ctx, tx, access, user, app, team, env, "events", "change")
}

func authorizeBulkModule(ctx context.Context, tx pgx.Tx, access BulkAccess, user core.User,
	app, team, env, module, action string) error {
	decision := rbac.V3Decision{Module: module, Action: action, EnvironmentID: env, ApplicationID: app}
	err := access.AuthorizeWithTx(ctx, tx, user, decision)
	if !errors.Is(err, store.ErrForbidden) || team == "" {
		return err
	}
	decision.ApplicationID = ""
	decision.TeamID = team
	return access.AuthorizeWithTx(ctx, tx, user, decision)
}

func bulkErrorOutcome(err error) string {
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrForbidden):
		return "not_found"
	case errors.Is(err, store.ErrSessionRevoked):
		return "session_revoked"
	case errors.Is(err, errHumanRequired):
		return "human_required"
	case errors.Is(err, store.ErrConflict):
		return "conflict"
	case errors.Is(err, errBulkGroupTooLarge):
		return "group_too_large"
	case errors.Is(err, errInvestigationRequired):
		return "investigation_required"
	case errors.Is(err, errBulkNotConnected), errors.Is(err, store.ErrInvalidReference):
		return "not_connected"
	case errors.Is(err, v3alertmanager.ErrNotConfigured):
		return "not_connected"
	default:
		return "unavailable"
	}
}

// Keep messages out of per-item failures: a hidden group and a missing grant
// must have the same public shape, without leaking application/source details.
func safeBulkError(id string, matched int, err error) bulkResult {
	return bulkFailed(id, bulkErrorOutcome(err), matched)
}

// The user-facing request intentionally has no arbitrary matcher, assignee or
// investigation target. Those values are derived from persisted group members.
