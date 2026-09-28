// Package v3catalog registers service metadata against the existing application
// and CMDB inventory. It does not infer health, SLO attainment or releases.
package v3catalog

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Service struct {
	Store                  *store.Store
	Access                 Access
	InstanceEnvironmentIDs []string
}

// The configured instance environment set is independent of the selected
// request environment. Without it, reuse of an existing identity fails closed.
func New(st *store.Store, access Access, instanceEnvironmentIDs ...string) *Service {
	return &Service{Store: st, Access: access, InstanceEnvironmentIDs: append([]string(nil), instanceEnvironmentIDs...)}
}

// Register exposes catalog reads with explicit unavailable evidence fields.
// The discovery route belongs to v3observe. The read responses are a bounded
// subset of the frontend V3 DTO until live health, release and SLO evidence is
// available; callers must not mark them as the complete V3 contract.
func (s *Service) Register(api *httpapi.Server) {
	api.Register("POST /api/v1/services", "admin", s.register)
	api.MarkV3Route("POST /api/v1/services", "services", "change")
	api.Register("GET /api/v1/services", "viewer", s.services)
	api.Register("GET /api/v1/services/{id}", "viewer", s.service)
	api.Register("GET /api/v1/service-mappings", "viewer", s.mappings)
	api.MarkV3ObjectRoute("GET /api/v1/service-mappings", "services", "view")
}

type registration struct {
	Items []registerItem `json:"items"`
}

type registerItem struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Tier      string `json:"tier"`
	Owner     string `json:"owner"`
	SLO       string `json:"slo"`
	P99       string `json:"p99"`
}

var serviceName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,126}[A-Za-z0-9])?$`)
var namespaceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)

func validateItems(items []registerItem) ([]registerItem, error) {
	if len(items) == 0 || len(items) > 50 {
		return nil, errors.New("choose 1 to 50 services")
	}
	seen := map[string]bool{}
	for i := range items {
		v := &items[i]
		v.ID, v.Namespace, v.Tier, v.Owner = strings.TrimSpace(v.ID), strings.TrimSpace(v.Namespace), strings.TrimSpace(v.Tier), strings.TrimSpace(v.Owner)
		v.SLO, v.P99 = strings.TrimSpace(v.SLO), strings.TrimSpace(v.P99)
		if !serviceName.MatchString(v.ID) || (v.Namespace != "" && !namespaceName.MatchString(v.Namespace)) || len(v.Owner) > 200 {
			return nil, errors.New("invalid service name, namespace or owner")
		}
		if seen[strings.ToLower(v.ID)] {
			return nil, errors.New("duplicate service in request")
		}
		seen[strings.ToLower(v.ID)] = true
		if v.Tier != "" && v.Tier != "T0" && v.Tier != "T1" && v.Tier != "T2" && v.Tier != "T3" {
			return nil, errors.New("invalid service tier")
		}
		if v.SLO != "" {
			n, err := strconv.ParseFloat(v.SLO, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 100 {
				return nil, errors.New("SLO must be a percentage from 0 to 100")
			}
		}
		if v.P99 != "" {
			n, err := strconv.Atoi(v.P99)
			if err != nil || n <= 0 || n > 3600000 {
				return nil, errors.New("P99 target must be milliseconds from 1 to 3600000")
			}
		}
	}
	return items, nil
}

func (s *Service) scope(w http.ResponseWriter, r *http.Request, action string) ([]string, bool) {
	if s == nil || s.Store == nil || s.Access == nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Catalog storage or policy is unavailable")
		return nil, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	if action != "view" && scope.All {
		httpapi.Error(w, 409, "env_view_only", "All non-production environments are read-only")
		return nil, false
	}
	if action != "view" && len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Choose one environment")
		return nil, false
	}
	return scope.EnvironmentIDs, true
}

func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "change")
	if !ok {
		return
	}
	user := httpapi.CurrentUser(r)
	if err := s.Access.Authorize(r.Context(), user, rbac.V3Decision{Module: "services", Action: "change", EnvironmentID: envs[0]}); err != nil {
		policyError(w, err)
		return
	}
	var input registration
	if !httpapi.Decode(w, r, &input) {
		return
	}
	items, err := validateItems(input.Items)
	if err != nil {
		httpapi.Error(w, 422, "invalid_service", err.Error())
		return
	}
	// Every item holds its key lock until commit; a stable acquisition order
	// prevents two overlapping batches from deadlocking in reverse order.
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].ID) < strings.ToLower(items[j].ID) })
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Catalog transaction unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	for _, item := range items {
		// Serialize registrations of the same logical key, including case
		// variants. Existing application identity and owner-team ACL are kept.
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended(lower($1),0))`, item.ID); err != nil {
			writeError(w, err)
			return
		}
		var appID, ownerTeamID string
		var archived bool
		matches, queryErr := tx.Query(r.Context(), `SELECT id,COALESCE(owner_team_id,''),archived
 FROM applications WHERE lower(key)=lower($1) LIMIT 2 FOR UPDATE`, item.ID)
		if queryErr != nil {
			writeError(w, queryErr)
			return
		}
		found := 0
		for matches.Next() {
			found++
			if queryErr = matches.Scan(&appID, &ownerTeamID, &archived); queryErr != nil {
				break
			}
		}
		if queryErr == nil {
			queryErr = matches.Err()
		}
		matches.Close()
		if queryErr != nil {
			writeError(w, queryErr)
			return
		}
		if found > 1 || (found == 1 && archived) {
			httpapi.Error(w, 409, "service_exists", "Service identity is ambiguous or archived")
			return
		}
		if found == 1 {
			if err = s.authorizeExisting(r.Context(), tx, user, appID, ownerTeamID); err != nil {
				policyError(w, err)
				return
			}
			var registered bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS (
 SELECT 1 FROM v3_application_environments
 WHERE application_id=$1 AND environment_id=$2
 )`, appID, envs[0]).Scan(&registered); err != nil {
				writeError(w, err)
				return
			}
			if registered {
				httpapi.Error(w, 409, "service_exists", "Service is already registered in this environment")
				return
			}
		} else {
			appID = store.ID()
		}
		envID := store.ID()
		// A manual service may have no CMDB resource. A discovered service may
		// link only a fresh, present and unclaimed workload with a verified UID.
		// Resolve before writing the application so a stale discovery cannot be
		// silently turned into a metadata-only registration.
		resourceID, resourceTeamID, e := s.registrationWorkload(r.Context(), tx, item, envs[0])
		if e != nil {
			var matchErr *workloadMatchError
			if errors.As(e, &matchErr) {
				httpapi.Error(w, 409, matchErr.code, matchErr.message)
			} else {
				httpapi.Error(w, 503, "catalog_unavailable", "Workload inventory unavailable")
			}
			return
		}
		if resourceID != "" {
			if e = s.authorizeResourceLink(r.Context(), tx, user, envs[0], resourceTeamID); e != nil {
				policyError(w, e)
				return
			}
		}
		var tier, slo any
		if item.Tier != "" {
			tier = item.Tier
		}
		if item.SLO != "" {
			slo = item.SLO
		}
		var p99 any
		if item.P99 != "" {
			p99, _ = strconv.Atoi(item.P99)
		}
		if found == 0 {
			_, err = tx.Exec(r.Context(), `INSERT INTO applications(id,name,description,environment,key)
 VALUES($1,$2,'',$3,$4)`, appID, item.ID, envs[0], item.ID)
			if err != nil {
				writeError(w, err)
				return
			}
		}
		var existingEnvID string
		var envArchived bool
		err = tx.QueryRow(r.Context(), `SELECT id,archived FROM application_environments
 WHERE application_id=$1 AND key=$2 FOR UPDATE`, appID, envs[0]).Scan(&existingEnvID, &envArchived)
		switch {
		case err == nil && envArchived:
			httpapi.Error(w, 409, "service_exists", "Service environment is archived")
			return
		case err == nil:
			envID = existingEnvID
		case errors.Is(err, pgx.ErrNoRows):
			_, err = tx.Exec(r.Context(), `INSERT INTO application_environments(id,application_id,key,name)
 VALUES($1,$2,$3,$3)`, envID, appID, envs[0])
			if err != nil {
				writeError(w, err)
				return
			}
		default:
			writeError(w, err)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,$2)`, appID, envs[0])
		if err != nil {
			writeError(w, err)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_service_environment_profiles
 (application_id,environment_id,tier,slo_target_percent,owner_label,p99_target_ms,namespace)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, appID, envs[0], tier, slo, item.Owner, p99, item.Namespace)
		if err != nil {
			writeError(w, err)
			return
		}
		if resourceID != "" {
			_, err = tx.Exec(r.Context(), `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id,provenance)
	 VALUES($1,$2,$3,'manual')
	 ON CONFLICT (resource_id,application_id,environment_id) DO UPDATE SET provenance='manual'`, resourceID, appID, envID)
			if err != nil {
				writeError(w, err)
				return
			}
		}
		changes, _ := json.Marshal(map[string]any{"environment": envs[0], "namespace": item.Namespace, "resource_linked": resourceID != "", "tier": item.Tier, "existing_service": found == 1})
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$2,'success',$4)`, user.ID, appID, serviceAuditAction(found == 1), changes)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]int{"created": len(items)})
}

func serviceAuditAction(existing bool) string {
	if existing {
		return "v3.service.environment_linked"
	}
	return "v3.service.created"
}

type workloadMatchError struct {
	code, message string
}

func (e *workloadMatchError) Error() string { return e.message }

// registrationWorkload uses the discovery route's source, presence, UID and
// freshness rules. The current POST DTO contains only name and namespace, so
// every trusted namesake in an environment must be unique, even across
// clusters. Locking the chosen row makes a concurrent inventory refresh wait
// until the registration transaction commits.
func (s *Service) registrationWorkload(ctx context.Context, tx pgx.Tx, item registerItem, env string) (string, string, error) {
	// Inventory refreshes lock their source before updating resource rows.
	// Take the same order explicitly; a joined FOR UPDATE/FOR SHARE query does
	// not guarantee which table's row lock is acquired first.
	sources, err := tx.Query(ctx, `SELECT id FROM cmdb_sync_sources
 WHERE kind='kubernetes' AND environment_key=$1 ORDER BY id FOR SHARE`, env)
	if err != nil {
		return "", "", err
	}
	for sources.Next() {
		var id string
		if err = sources.Scan(&id); err != nil {
			break
		}
	}
	if err == nil {
		err = sources.Err()
	}
	sources.Close()
	if err != nil {
		return "", "", err
	}
	rows, err := tx.Query(ctx, `SELECT r.id,COALESCE(r.owner_team_id,''),r.attributes->>'namespace'
 FROM cmdb_resources r
 JOIN cmdb_sync_sources src ON src.id=r.source_id AND src.kind=r.source_kind
 WHERE r.kind='deployment' AND r.source_kind='kubernetes' AND NOT r.is_mock
 AND lower(r.name)=lower($1)
 AND r.observed_at BETWEEN now()-interval '5 minutes' AND now()+interval '1 minute'
 AND src.last_success_at BETWEEN now()-interval '5 minutes' AND now()+interval '1 minute'
 AND src.environment_key=$2 AND src.environment_key<>''
 AND r.attributes->>'environment_key'=src.environment_key
 AND r.attributes->>'inventory_presence'='present'
 AND r.external_id<>'' AND r.attributes->>'uid'=r.external_id
 AND r.attributes->>'resource_type' IN ('Deployment','StatefulSet','DaemonSet')
 AND r.attributes->>'namespace'<>'' AND r.attributes->>'cluster'<>''
 AND NOT EXISTS (SELECT 1 FROM cmdb_resource_applications l WHERE l.resource_id=r.id)
 ORDER BY r.id LIMIT 2 FOR UPDATE OF r`, item.ID, env)
	if err != nil {
		return "", "", err
	}
	type match struct{ id, team, namespace string }
	var matches []match
	for rows.Next() {
		var m match
		if err = rows.Scan(&m.id, &m.team, &m.namespace); err != nil {
			break
		}
		matches = append(matches, m)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", "", err
	}
	if len(matches) > 1 {
		return "", "", &workloadMatchError{"ambiguous_workload", "Multiple current workloads share this service name; registration needs a resource identity"}
	}
	if len(matches) == 1 && matches[0].namespace != item.Namespace {
		return "", "", &workloadMatchError{"workload_mismatch", "A current workload has this service name in another namespace"}
	}

	// Include stale, missing, malformed-source and already-linked namesakes.
	// An invalid candidate is not proof of a link, but it must not be silently
	// converted into a new unrelated service either. A completely absent name
	// remains a valid manual, metadata-only registration.
	rows, err = tx.Query(ctx, `SELECT r.id FROM cmdb_resources r
 LEFT JOIN cmdb_sync_sources src ON src.id=r.source_id AND src.kind=r.source_kind
 WHERE r.kind='deployment' AND r.source_kind='kubernetes'
 AND lower(r.name)=lower($1)
 AND ($2='' OR r.attributes->>'namespace'=$2)
 AND (src.environment_key=$3 OR r.attributes->>'environment_key'=$3)
 ORDER BY r.id LIMIT 2 FOR UPDATE OF r`, item.ID, item.Namespace, env)
	if err != nil {
		return "", "", err
	}
	var raw []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		raw = append(raw, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", "", err
	}
	if len(matches) == 0 {
		if len(raw) > 0 {
			return "", "", &workloadMatchError{"workload_unavailable", "A matching workload exists but is not current, present or available for registration"}
		}
		return "", "", nil
	}
	if len(raw) != 1 || raw[0] != matches[0].id {
		return "", "", &workloadMatchError{"ambiguous_workload", "Multiple workload identities match this service name and namespace"}
	}
	return matches[0].id, matches[0].team, nil
}

func (s *Service) authorizeResourceLink(ctx context.Context, tx pgx.Tx, user core.User, env, team string) error {
	decision := rbac.V3Decision{Module: "resources", Action: "view", EnvironmentID: env, TeamID: team}
	access, ok := s.transactionAccess()
	if !ok {
		return store.ErrForbidden
	}
	return access.AuthorizeWithTx(ctx, tx, user, decision)
}

type transactionalAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

func (s *Service) transactionAccess() (transactionalAccess, bool) {
	access, ok := s.Access.(transactionalAccess)
	return access, ok
}

// Changing a service in the target environment is insufficient to claim an
// unrelated existing identity. A pre-existing application/team grant in one
// of that service's mapped environments must also authorize the caller.
func (s *Service) authorizeExisting(ctx context.Context, tx pgx.Tx, user core.User, appID, teamID string) error {
	if len(s.InstanceEnvironmentIDs) == 0 {
		return store.ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT environment_id FROM v3_application_environments
 WHERE application_id=$1 AND environment_id=ANY($2::text[])
 ORDER BY environment_id`, appID, s.InstanceEnvironmentIDs)
	if err != nil {
		return err
	}
	var oldEnvs []string
	for rows.Next() {
		var env string
		if err = rows.Scan(&env); err != nil {
			break
		}
		oldEnvs = append(oldEnvs, env)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	return s.authorizeExistingGrants(ctx, tx, user, appID, teamID, oldEnvs)
}

func (s *Service) authorizeExistingGrants(ctx context.Context, tx pgx.Tx, user core.User, appID, teamID string, oldEnvs []string) error {
	if len(s.InstanceEnvironmentIDs) == 0 {
		return store.ErrForbidden
	}
	access, ok := s.transactionAccess()
	if !ok {
		return store.ErrForbidden
	}
	for _, env := range oldEnvs {
		if !s.instanceEnvironment(env) {
			continue
		}
		err := access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "services", Action: "change", EnvironmentID: env, ApplicationID: appID})
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrForbidden) {
			return err
		}
		if teamID != "" {
			err = access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "services", Action: "change", EnvironmentID: env, TeamID: teamID})
			if err == nil {
				return nil
			}
			if !errors.Is(err, store.ErrForbidden) {
				return err
			}
		}
	}
	// A legacy application without a V3 mapping inside this instance cannot
	// be adopted merely because the caller has a global grant elsewhere.
	return store.ErrForbidden
}

func (s *Service) instanceEnvironment(env string) bool {
	for _, configured := range s.InstanceEnvironmentIDs {
		if configured == env {
			return true
		}
	}
	return false
}

func policyError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Service access denied")
		return
	}
	httpapi.Error(w, 503, "authorization_unavailable", "Service policy unavailable")
}

func writeError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		httpapi.Error(w, 409, "service_exists", "Service name already exists")
		return
	}
	httpapi.Error(w, 503, "catalog_unavailable", "Service catalog unavailable")
}
