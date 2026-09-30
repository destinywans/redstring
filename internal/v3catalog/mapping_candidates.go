package v3catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// Only an administrator-enrolled GitLab project is currently a precise,
// independently verifiable source for the code column. A connector name,
// Kubernetes label, or deployment name is not an external object identity.
type mappingTarget struct {
	appID, key, teamID, env, envID    string
	bindings                          []byte
	revision                          int64
	targetID, connectorID, externalID string
	boundAt                           time.Time
	ready                             bool
}

type mappingCandidate struct {
	id, serviceKey, sourceID, connectorID, externalID string
	boundAt, discoveredAt, expiresAt                  time.Time
	revision                                          int64
}

type mappingSuggestion struct {
	appID, key, teamID, connectorID, externalID string
	review                                      mappingCandidateReview
}

type mappingCandidateReview struct {
	ID              string    `json:"id"`
	Version         string    `json:"version"`
	ServiceID       string    `json:"service_id"`
	Service         string    `json:"service"`
	EnvironmentID   string    `json:"environment_id"`
	SystemID        string    `json:"system_id"`
	ConnectorID     string    `json:"connector_id"`
	TargetID        string    `json:"target_id"`
	ExternalID      string    `json:"external_id"`
	BindingRevision int64     `json:"binding_revision"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func mappingVersion(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

// The matrix only renders a candidate while its exact source and binding
// revision remain current and it is the sole GitLab project for that service
// in the selected environment. The caller supplies only authorized app IDs.
func (s *Service) currentMappingSuggestions(ctx context.Context, env string, appIDs []string) ([]mappingSuggestion, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT m.application_id,m.service_key,COALESCE(a.owner_team_id,''),m.connector_id,m.external_id,
 m.id,m.discovered_at,m.environment_id,m.system_id,m.source_id,m.binding_revision,m.expires_at
 FROM v3_service_mapping_candidates m
 JOIN v3_connector_targets t ON t.id=m.source_id AND t.application_id=m.application_id AND t.environment_id=m.environment_id
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 JOIN applications a ON a.id=m.application_id AND NOT a.archived AND a.key=m.service_key
 JOIN application_environments e ON e.application_id=a.id AND e.key=m.environment_id AND NOT e.archived
 WHERE m.environment_id=$1 AND m.application_id=ANY($2::text[]) AND m.system_id='code'
 AND m.source_kind='explicit_target' AND m.accepted_at IS NULL AND m.expires_at>now()
 AND m.source_bound_at=t.bound_at AND m.connector_id=t.connector_id AND m.external_id=t.external_id
 AND m.binding_revision=e.binding_revision AND t.target_kind='project' AND c.provider='gitlab'
 AND `+eligibleMappingTarget+`
 AND NOT EXISTS (SELECT 1 FROM v3_connector_targets other
 JOIN v3_connectors other_connector ON other_connector.id=other.connector_id AND other_connector.environment_id=other.environment_id
 WHERE other.application_id=t.application_id AND other.environment_id=t.environment_id
 AND other.target_kind='project' AND other_connector.provider='gitlab' AND other.id<>t.id)
 ORDER BY m.application_id,m.id LIMIT 1001`, env, appIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []mappingSuggestion{}
	for rows.Next() {
		var item mappingSuggestion
		var discoveredAt time.Time
		if err := rows.Scan(&item.appID, &item.key, &item.teamID, &item.connectorID, &item.externalID,
			&item.review.ID, &discoveredAt, &item.review.EnvironmentID, &item.review.SystemID,
			&item.review.TargetID, &item.review.BindingRevision, &item.review.ExpiresAt); err != nil {
			return nil, err
		}
		item.review.Version = mappingVersion(discoveredAt)
		item.review.ServiceID, item.review.Service = item.appID, item.key
		item.review.ConnectorID, item.review.ExternalID = item.connectorID, item.externalID
		if validMappingProject(item.externalID) {
			items = append(items, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > 1000 {
		return nil, errors.New("mapping candidate scope exceeds 1000")
	}
	return items, nil
}

func validMappingProject(id string) bool {
	if len(id) == 0 || len(id) > 19 || id[0] == '0' {
		return false
	}
	for _, digit := range id {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0
}

func mappingBindings(raw []byte) ([]core.Binding, bool, error) {
	var bindings []core.Binding
	if err := json.Unmarshal(raw, &bindings); err != nil {
		return nil, false, err
	}
	for _, binding := range bindings {
		if mappingKind(binding.Kind) == "code" {
			return bindings, true, nil
		}
	}
	return bindings, false, nil
}

// The platform environment writer only validates its legacy connector kinds.
// A V3 mapping may add exactly the GitLab project verified by the candidate
// source; every other binding must still pass that unchanged runtime policy.
// Callers must derive expected from the enrolled target, never request input.
func (s *Service) mappingBindingSupported(bindings []core.Binding, expected core.Binding) bool {
	if s.MappingBindingValidator == nil || expected.Kind != "gitlab" ||
		expected.ConnectorID == "" || !validMappingProject(expected.Resource) {
		return false
	}
	legacy := make([]core.Binding, 0, len(bindings))
	matched := false
	for _, binding := range bindings {
		if mappingKind(binding.Kind) == "code" {
			if matched || binding != expected {
				return false
			}
			matched = true
			continue
		}
		legacy = append(legacy, binding)
	}
	return matched && s.MappingBindingValidator(legacy) == nil
}

func authorizeMappingService(ctx context.Context, access transactionalAccess, tx pgx.Tx, user core.User, app, env, team, action string) error {
	err := access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "services", Action: action, EnvironmentID: env, ApplicationID: app})
	if errors.Is(err, store.ErrForbidden) && team != "" {
		err = access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "services", Action: action, EnvironmentID: env, TeamID: team})
	}
	return err
}

func authorizeMappingConnector(ctx context.Context, access transactionalAccess, tx pgx.Tx, user core.User, env, action string) error {
	return access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "connectors", Action: action, EnvironmentID: env})
}

// Hold the authenticated session and account through commit. A logout or
// account disable that commits while a mapping writer waits on another lock
// must invalidate the write; a later revocation waits for this transaction.
func lockMappingWriteSession(ctx context.Context, tx pgx.Tx, r *http.Request, userID string) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return store.ErrForbidden
	}
	var currentUserID string
	err = tx.QueryRow(ctx, `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(cookie.Value), userID).Scan(&currentUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	return err
}

// Activation records point-in-time verification of every enrolled GitLab
// project. A generic probe only checks /api/v4/user, so recent connector
// health does not assert current project access. Activation cannot be renewed
// while enabled; keep its evidence historical and require it after target
// binding. The 053 migration left older connectors enabled without it.
const eligibleMappingTarget = `c.enabled AND c.status='ok'
 AND c.last_checked_at BETWEEN now()-interval '15 minutes' AND now()+interval '1 minute'
 AND EXISTS (SELECT 1 FROM management_audit activation
 WHERE activation.action='connector.activated' AND activation.target=c.id
 AND activation.outcome='success' AND activation.changes->>'environment'=t.environment_id
 AND activation.created_at>=t.bound_at)`

func mappingTargetBatch(ctx context.Context, tx pgx.Tx, env, after string) ([]mappingTarget, error) {
	rows, err := tx.Query(ctx, `SELECT a.id,a.key,COALESCE(a.owner_team_id,''),t.environment_id,e.id,e.bindings,e.binding_revision,
 t.id,t.connector_id,t.external_id,t.bound_at,COALESCE(`+eligibleMappingTarget+`,false)
 FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 JOIN applications a ON a.id=t.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=t.environment_id
 JOIN application_environments e ON e.application_id=a.id AND e.key=t.environment_id AND NOT e.archived
 WHERE t.environment_id=$1 AND t.id>$2 AND t.target_kind='project' AND c.provider='gitlab'
 ORDER BY t.id LIMIT 128`, env, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batch := make([]mappingTarget, 0, 128)
	for rows.Next() {
		var item mappingTarget
		if err := rows.Scan(&item.appID, &item.key, &item.teamID, &item.env, &item.envID, &item.bindings,
			&item.revision, &item.targetID, &item.connectorID, &item.externalID, &item.boundAt, &item.ready); err != nil {
			return nil, err
		}
		batch = append(batch, item)
	}
	return batch, rows.Err()
}

// mappingDiscover stores a short-lived review snapshot. The external target
// was explicitly bound by an administrator; discovery itself never edits a
// service binding or treats connector health as proven.
func (s *Service) mappingDiscover(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "change")
	if !ok {
		return
	}
	if r.ContentLength > 0 {
		httpapi.Error(w, 422, "invalid_body", "Discovery takes no request body")
		return
	}
	access, ok := s.transactionAccess()
	if !ok {
		httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return
	}
	defer tx.Rollback(ctx)
	user := httpapi.CurrentUser(r)
	for _, action := range []string{"view", "manage"} {
		if err := authorizeMappingConnector(ctx, access, tx, user, envs[0], action); err != nil {
			policyError(w, err)
			return
		}
	}
	// Expired rows are filtered from reads and acceptance. Do not clean them
	// here: a global delete would lock candidate rows before service rows and
	// invert the lock order used by acceptance and target writers.
	groups := map[string][]mappingTarget{}
	seen := 0
	configured := false
	after := ""
	for {
		batch, err := mappingTargetBatch(ctx, tx, envs[0], after)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping source unavailable")
			return
		}
		for _, item := range batch {
			after = item.targetID
			if item.key == "" || len(item.key) > 295 {
				continue
			}
			visible := true
			for _, action := range []string{"view", "change"} {
				err := authorizeMappingService(ctx, access, tx, user, item.appID, item.env, item.teamID, action)
				if errors.Is(err, store.ErrForbidden) {
					visible = false
					break
				}
				if err != nil {
					httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
					return
				}
			}
			if !visible {
				continue
			}
			configured = true
			_, mapped, err := mappingBindings(item.bindings)
			if err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Stored mapping is invalid")
				return
			}
			if mapped {
				continue
			}
			seen++
			if seen > 500 {
				httpapi.Error(w, 422, "result_limit", "Narrow the mapping source")
				return
			}
			groups[item.appID] = append(groups[item.appID], item)
		}
		if len(batch) < 128 {
			break
		}
	}
	candidates := []mappingCandidateReview{}
	appIDs := make([]string, 0, len(groups))
	for appID := range groups {
		appIDs = append(appIDs, appID)
	}
	// Acquire every service lock before touching any candidate row. Acceptance
	// and target writes use service-first locking, including multi-service
	// batches. Re-read service state after each lock because the initial scan
	// may have raced an acceptance or target edit.
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		var key, teamID string
		var raw []byte
		var revision int64
		err := tx.QueryRow(ctx, `SELECT a.key,COALESCE(a.owner_team_id,''),e.bindings,e.binding_revision
	 FROM applications a
	 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
	 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
	 WHERE a.id=$1 AND NOT a.archived FOR UPDATE OF a,ve,e`, appID, envs[0]).Scan(&key, &teamID, &raw, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			delete(groups, appID)
			continue
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		for _, action := range []string{"view", "change"} {
			err = authorizeMappingService(ctx, access, tx, user, appID, envs[0], teamID, action)
			if errors.Is(err, store.ErrForbidden) {
				delete(groups, appID)
				break
			}
			if err != nil {
				httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
				return
			}
		}
		if len(groups[appID]) == 0 || key == "" || len(key) > 295 {
			continue
		}
		_, mapped, err := mappingBindings(raw)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Stored mapping is invalid")
			return
		}
		if mapped {
			delete(groups, appID)
			continue
		}
		for i := range groups[appID] {
			groups[appID][i].key = key
			groups[appID][i].teamID = teamID
			groups[appID][i].bindings = raw
			groups[appID][i].revision = revision
		}
	}
	// The caller may lose connector access while waiting for service locks.
	// Recheck the source authority before any candidate write or response.
	for _, action := range []string{"view", "manage"} {
		if err := authorizeMappingConnector(ctx, access, tx, user, envs[0], action); err != nil {
			policyError(w, err)
			return
		}
	}
	for _, appID := range appIDs {
		group := groups[appID]
		if len(group) != 1 || !group[0].ready || !validMappingProject(group[0].externalID) {
			continue
		}
		item := group[0]
		// Target writers lock the service first, so this fresh read cannot be
		// invalidated by a concurrent binding edit while our service lock lives.
		var liveTargets []mappingTarget
		rows, err := tx.Query(ctx, `SELECT t.id,t.connector_id,t.external_id,t.bound_at,
		 COALESCE(`+eligibleMappingTarget+`,false)
		 FROM v3_connector_targets t
		 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
		 WHERE t.application_id=$1 AND t.environment_id=$2
		 AND t.target_kind='project' AND c.provider='gitlab'
		 ORDER BY t.id LIMIT 2`, appID, envs[0])
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping source unavailable")
			return
		}
		for rows.Next() {
			var live mappingTarget
			if err = rows.Scan(&live.targetID, &live.connectorID, &live.externalID, &live.boundAt, &live.ready); err != nil {
				break
			}
			liveTargets = append(liveTargets, live)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping source unavailable")
			return
		}
		if len(liveTargets) != 1 || !liveTargets[0].ready || !validMappingProject(liveTargets[0].externalID) {
			continue
		}
		item.targetID, item.connectorID, item.externalID, item.boundAt =
			liveTargets[0].targetID, liveTargets[0].connectorID, liveTargets[0].externalID, liveTargets[0].boundAt
		bindings, _, err := mappingBindings(item.bindings)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Stored mapping is invalid")
			return
		}
		expected := core.Binding{Kind: "gitlab", ConnectorID: item.connectorID, Resource: item.externalID}
		supported := s.mappingBindingSupported(append(bindings, expected), expected)
		// A previous candidate from a removed target cannot make today's
		// unique reviewed target ambiguous at accept time.
		if _, err := tx.Exec(ctx, `DELETE FROM v3_service_mapping_candidates
 WHERE application_id=$1 AND environment_id=$2 AND system_id='code'
 AND accepted_at IS NULL AND source_id<>$3`, item.appID, item.env, item.targetID); err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		var id string
		var discoveredAt, expiresAt time.Time
		err = tx.QueryRow(ctx, `INSERT INTO v3_service_mapping_candidates
 (id,application_id,environment_id,service_key,system_id,source_kind,source_id,source_bound_at,
 connector_id,external_id,binding_revision,expires_at)
 VALUES($1,$2,$3,$4,'code','explicit_target',$5,$6,$7,$8,$9,now()+interval '15 minutes')
 ON CONFLICT (application_id,environment_id,system_id,source_kind,source_id) WHERE accepted_at IS NULL
 DO UPDATE SET service_key=EXCLUDED.service_key,source_bound_at=EXCLUDED.source_bound_at,
 connector_id=EXCLUDED.connector_id,external_id=EXCLUDED.external_id,
	 binding_revision=EXCLUDED.binding_revision,
	 discovered_at=GREATEST(clock_timestamp(),v3_service_mapping_candidates.discovered_at+interval '1 microsecond'),
	 expires_at=EXCLUDED.expires_at
	 RETURNING id,discovered_at,expires_at`, store.ID(), item.appID, item.env, item.key, item.targetID, item.boundAt,
			item.connectorID, item.externalID, item.revision).Scan(&id, &discoveredAt, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		if supported {
			candidates = append(candidates, mappingCandidateReview{ID: id, Version: mappingVersion(discoveredAt),
				ServiceID: item.appID, Service: item.key, EnvironmentID: item.env, SystemID: "code",
				ConnectorID: item.connectorID, TargetID: item.targetID, ExternalID: item.externalID,
				BindingRevision: item.revision, ExpiresAt: expiresAt})
		}
	}
	if err := lockMappingWriteSession(ctx, tx, r, user.ID); err != nil {
		policyError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return
	}
	sourceState := "not_configured"
	if configured {
		sourceState = "configured"
	}
	httpapi.WriteJSON(w, 200, map[string]any{"suggestions": len(candidates), "source_state": sourceState, "candidates": candidates})
}

type mappingAcceptInput struct {
	Candidates []mappingAcceptCandidate `json:"candidates"`
}

type mappingAcceptCandidate struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	ServiceID       string `json:"service_id"`
	EnvironmentID   string `json:"environment_id"`
	SystemID        string `json:"system_id"`
	BindingRevision int64  `json:"binding_revision"`
}

type mappingAcceptItem struct {
	CandidateID     string       `json:"candidate_id"`
	ServiceID       string       `json:"service_id"`
	EnvironmentID   string       `json:"environment_id"`
	SystemID        string       `json:"system_id"`
	ConnectorID     string       `json:"connector_id"`
	TargetID        string       `json:"target_id"`
	ExternalID      string       `json:"external_id"`
	BindingRevision int64        `json:"binding_revision"`
	Binding         core.Binding `json:"binding"`
	Status          string       `json:"status"`
}

func parseMappingCandidates(candidates []mappingAcceptCandidate, env string) ([]mappingAcceptCandidate, error) {
	if len(candidates) == 0 || len(candidates) > 500 {
		return nil, errors.New("provide 1 to 500 mapping candidates")
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		at, err := time.Parse(time.RFC3339Nano, candidate.Version)
		if candidate.ID == "" || candidate.ServiceID == "" || candidate.EnvironmentID != env ||
			candidate.SystemID != "code" || candidate.BindingRevision < 1 || err != nil ||
			mappingVersion(at) != candidate.Version {
			return nil, errors.New("invalid mapping candidate")
		}
		if seen[candidate.ID] || seen[candidate.ServiceID] {
			return nil, errors.New("duplicate mapping candidate or service")
		}
		seen[candidate.ID], seen[candidate.ServiceID] = true, true
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ServiceID < candidates[j].ServiceID })
	return candidates, nil
}

func (s *Service) mappingAccept(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "change")
	if !ok {
		return
	}
	var input mappingAcceptInput
	if !httpapi.Decode(w, r, &input) {
		return
	}
	candidatesInput, err := parseMappingCandidates(input.Candidates, envs[0])
	if err != nil {
		httpapi.Error(w, 422, "invalid_candidates", err.Error())
		return
	}
	access, ok := s.transactionAccess()
	if !ok {
		httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return
	}
	defer tx.Rollback(ctx)
	user := httpapi.CurrentUser(r)
	accepted := 0
	items := []mappingAcceptItem{}
	var earliestExpiry time.Time
	// Object routes defer authorization to the handler. Check every requested
	// service and the connector before taking any write lock; an unauthorized
	// batch must not hold rows belonging to other services. Recheck below
	// after locking so policy or object changes cannot authorize stale work.
	for _, action := range []string{"view", "manage"} {
		if err := authorizeMappingConnector(ctx, access, tx, user, envs[0], action); err != nil {
			policyError(w, err)
			return
		}
	}
	for _, selected := range candidatesInput {
		var appID, teamID string
		err := tx.QueryRow(ctx, `SELECT a.id,COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
	 WHERE a.id=$1 AND NOT a.archived`, selected.ServiceID, envs[0]).Scan(&appID, &teamID)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Service mapping not found")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		for _, action := range []string{"view", "change"} {
			if err := authorizeMappingService(ctx, access, tx, user, appID, envs[0], teamID, action); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					httpapi.Error(w, 404, "not_found", "Service mapping not found")
				} else {
					policyError(w, err)
				}
				return
			}
		}
	}
	// Every target writer locks its service before the connector. Acquire all
	// requested service rows in one stable order before locking any shared
	// connector, including when an "accept all" request spans many services.
	for _, selected := range candidatesInput {
		var appID string
		err := tx.QueryRow(ctx, `SELECT a.id FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
	 WHERE a.id=$1 AND NOT a.archived FOR UPDATE OF a,ve,e`, selected.ServiceID, envs[0]).Scan(&appID)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Service mapping not found")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
	}
	for _, selected := range candidatesInput {
		var appID, serviceKey, teamID, envID string
		var raw []byte
		var revision int64
		err := tx.QueryRow(ctx, `SELECT a.id,a.key,COALESCE(a.owner_team_id,''),e.id,e.bindings,e.binding_revision
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
	 WHERE a.id=$1 AND NOT a.archived FOR UPDATE OF a,ve,e`, selected.ServiceID, envs[0]).Scan(&appID, &serviceKey, &teamID, &envID, &raw, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Service mapping not found")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		for _, action := range []string{"view", "change"} {
			if err := authorizeMappingService(ctx, access, tx, user, appID, envs[0], teamID, action); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					httpapi.Error(w, 404, "not_found", "Service mapping not found")
				} else {
					policyError(w, err)
				}
				return
			}
		}
		for _, action := range []string{"view", "manage"} {
			if err := authorizeMappingConnector(ctx, access, tx, user, envs[0], action); err != nil {
				policyError(w, err)
				return
			}
		}
		bindings, mapped, err := mappingBindings(raw)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Stored mapping is invalid")
			return
		}
		if mapped {
			item, alreadyAccepted, err := existingAcceptedMapping(ctx, tx, selected, serviceKey, revision, bindings)
			if err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Mapping candidate unavailable")
				return
			}
			if alreadyAccepted {
				items = append(items, item)
				continue
			}
			httpapi.Error(w, 409, "stale_mapping", "Mapping or accepted candidate changed")
			return
		}
		var candidate mappingCandidate
		err = tx.QueryRow(ctx, `SELECT id,service_key,source_id,source_bound_at,connector_id,external_id,binding_revision,discovered_at,expires_at
 FROM v3_service_mapping_candidates
 WHERE id=$1 AND application_id=$2 AND environment_id=$3 AND system_id='code'
	 AND source_kind='explicit_target' AND accepted_at IS NULL AND expires_at>clock_timestamp()
	 FOR UPDATE`, selected.ID, appID, envs[0]).Scan(&candidate.id, &candidate.serviceKey,
			&candidate.sourceID, &candidate.boundAt, &candidate.connectorID, &candidate.externalID,
			&candidate.revision, &candidate.discoveredAt, &candidate.expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 409, "candidate_unavailable", "Mapping candidate expired or changed")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping candidates unavailable")
			return
		}
		if candidate.serviceKey != serviceKey || candidate.revision != revision ||
			candidate.revision != selected.BindingRevision || mappingVersion(candidate.discoveredAt) != selected.Version {
			httpapi.Error(w, 409, "stale_mapping", "Mapping changed after discovery")
			return
		}
		if earliestExpiry.IsZero() || candidate.expiresAt.Before(earliestExpiry) {
			earliestExpiry = candidate.expiresAt
		}
		var sourceApp, sourceEnv, sourceConnector, sourceKind, sourceExternal, provider string
		var sourceBound time.Time
		var ready bool
		err = tx.QueryRow(ctx, `SELECT t.application_id,t.environment_id,t.connector_id,t.target_kind,t.external_id,
	 t.bound_at,c.provider,COALESCE(`+eligibleMappingTarget+`,false)
 FROM v3_connector_targets t JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.id=$1 FOR UPDATE OF t,c`, candidate.sourceID).Scan(&sourceApp, &sourceEnv, &sourceConnector,
			&sourceKind, &sourceExternal, &sourceBound, &provider, &ready)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 409, "source_unavailable", "Mapping source disappeared")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping source unavailable")
			return
		}
		if sourceApp != appID || sourceEnv != envs[0] || sourceConnector != candidate.connectorID || sourceKind != "project" ||
			sourceExternal != candidate.externalID || !sourceBound.Equal(candidate.boundAt) || provider != "gitlab" || !ready || !validMappingProject(sourceExternal) {
			httpapi.Error(w, 409, "stale_mapping", "Mapping source changed after discovery")
			return
		}
		var sourceIDs []string
		var rows pgx.Rows
		rows, err = tx.Query(ctx, `SELECT t.id FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.target_kind='project' AND c.provider='gitlab'
 ORDER BY t.id LIMIT 2 FOR SHARE OF t,c`, appID, envs[0])
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping source unavailable")
			return
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			sourceIDs = append(sourceIDs, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping source unavailable")
			return
		}
		if len(sourceIDs) != 1 || sourceIDs[0] != candidate.sourceID {
			httpapi.Error(w, 409, "ambiguous_mapping", "Multiple GitLab projects match this service")
			return
		}
		expected := core.Binding{Kind: "gitlab", ConnectorID: candidate.connectorID, Resource: candidate.externalID}
		bindings = append(bindings, expected)
		if !s.mappingBindingSupported(bindings, expected) {
			httpapi.Error(w, 409, "mapping_binding_unavailable", "This mapping is not supported by the current environment")
			return
		}
		serialized, err := json.Marshal(bindings)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping could not be serialized")
			return
		}
		var nextRevision int64
		err = tx.QueryRow(ctx, `UPDATE application_environments
 SET bindings=$1,binding_revision=binding_revision+1
 WHERE id=$2 AND application_id=$3 AND key=$4 AND binding_revision=$5
 RETURNING binding_revision`, serialized, envID, appID, envs[0], revision).Scan(&nextRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 409, "stale_mapping", "Mapping changed after discovery")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE v3_service_mapping_candidates
 SET accepted_at=now(),accepted_by=$2 WHERE id=$1 AND accepted_at IS NULL`, candidate.id, user.ID); err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping candidate unavailable")
			return
		}
		changes, _ := json.Marshal(map[string]any{"environment": envs[0], "candidate_id": candidate.id,
			"source_kind": "explicit_target", "source_id": candidate.sourceID,
			"connector_id": candidate.connectorID, "project_id": candidate.externalID,
			"binding_revision_before": revision, "binding_revision_after": nextRevision})
		if _, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.service_mapping.accepted',$3,'success',$4)`, user.ID, appID, envID, changes); err != nil {
			httpapi.Error(w, 503, "audit_unavailable", "Mapping audit unavailable")
			return
		}
		accepted++
		items = append(items, mappingAcceptItem{CandidateID: candidate.id, ServiceID: appID,
			EnvironmentID: envs[0], SystemID: "code", ConnectorID: candidate.connectorID,
			TargetID: candidate.sourceID, ExternalID: candidate.externalID, BindingRevision: nextRevision,
			Binding: expected, Status: "accepted"})
	}
	if err := lockMappingWriteSession(ctx, tx, r, user.ID); err != nil {
		policyError(w, err)
		return
	}
	if !earliestExpiry.IsZero() {
		var currentTime time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&currentTime); err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		if !currentTime.Before(earliestExpiry) {
			httpapi.Error(w, 409, "candidate_unavailable", "Mapping candidate expired or changed")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"accepted": accepted, "items": items})
}

func existingAcceptedMapping(ctx context.Context, tx pgx.Tx, selected mappingAcceptCandidate, key string, revision int64, bindings []core.Binding) (mappingAcceptItem, bool, error) {
	var empty mappingAcceptItem
	var code *core.Binding
	for _, binding := range bindings {
		if mappingKind(binding.Kind) != "code" {
			continue
		}
		if code != nil {
			return empty, false, nil
		}
		copy := binding
		code = &copy
	}
	if code == nil || code.Kind != "gitlab" || code.Resource == "" {
		return empty, false, nil
	}
	var candidate mappingCandidate
	err := tx.QueryRow(ctx, `SELECT id,service_key,source_id,source_bound_at,connector_id,external_id,binding_revision,discovered_at
	 FROM v3_service_mapping_candidates
	 WHERE id=$1 AND application_id=$2 AND environment_id=$3 AND system_id='code'
	 AND source_kind='explicit_target' AND accepted_at IS NOT NULL`,
		selected.ID, selected.ServiceID, selected.EnvironmentID).Scan(&candidate.id, &candidate.serviceKey,
		&candidate.sourceID, &candidate.boundAt, &candidate.connectorID, &candidate.externalID,
		&candidate.revision, &candidate.discoveredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	if candidate.serviceKey != key || candidate.revision != selected.BindingRevision ||
		mappingVersion(candidate.discoveredAt) != selected.Version || revision != candidate.revision+1 ||
		*code != (core.Binding{Kind: "gitlab", ConnectorID: candidate.connectorID, Resource: candidate.externalID}) {
		return empty, false, nil
	}
	return mappingAcceptItem{CandidateID: candidate.id, ServiceID: selected.ServiceID,
		EnvironmentID: selected.EnvironmentID, SystemID: "code", ConnectorID: candidate.connectorID,
		TargetID: candidate.sourceID, ExternalID: candidate.externalID, BindingRevision: revision,
		Binding: *code, Status: "already_accepted"}, true, nil
}
