// Package v3observe adapts persisted CMDB data and registered telemetry to the
// V3 read contracts without manufacturing health, service levels or log counts.
package v3observe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Telemetry interface {
	QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error)
	QueryTracesBatchID(context.Context, []string, connectors.TimeWindow, int, string) (map[string]connectors.TraceEvidence, error)
}

type Service struct {
	Store     *store.Store
	Access    Access
	Telemetry Telemetry
}

type observationTarget struct {
	ApplicationID string
	EnvironmentID string
	Resource      string
	Provider      string
}

func New(st *store.Store, access Access, telemetry Telemetry) *Service {
	return &Service{Store: st, Access: access, Telemetry: telemetry}
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/services/discover", "viewer", s.discover)
	api.Register("GET /api/v1/logs", "viewer", s.logs)
	api.Register("GET /api/v1/traces", "viewer", s.traces)
	api.Register("GET /api/v1/traces/{id}", "viewer", s.traceDetail)
}

func (s *Service) allowedEnvironments(w http.ResponseWriter, r *http.Request, module string) ([]string, bool) {
	if s.Store == nil || s.Access == nil {
		httpapi.Error(w, 503, "observe_unavailable", "Observation storage or policy is not configured")
		return nil, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	if module == "" {
		return scope.EnvironmentIDs, true
	}
	allowed := make([]string, 0, len(scope.EnvironmentIDs))
	for _, id := range scope.EnvironmentIDs {
		if err := s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{Module: module, Action: "view", EnvironmentID: id}); err == nil {
			allowed = append(allowed, id)
		} else if !errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 503, "observe_unavailable", "Observation policy is unavailable")
			return nil, false
		}
	}
	if len(allowed) == 0 {
		httpapi.Error(w, 403, "forbidden", "This observation is outside your authorized scope")
		return nil, false
	}
	return allowed, true
}

type discoveredService struct {
	ID        string   `json:"id"`
	Namespace string   `json:"namespace"`
	Kind      string   `json:"kind"`
	Replicas  *int     `json:"replicas"`
	Image     string   `json:"image"`
	OwnerHint string   `json:"owner_hint"`
	TierHint  string   `json:"tier_hint"`
	Matched   []string `json:"matched"`
	Missing   []string `json:"missing"`
}

// discover returns fresh, unregistered workload records. No provider match is
// asserted without an application binding and configured connector identity.
func (s *Service) discover(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.allowedEnvironments(w, r, "services")
	if !ok {
		return
	}
	cluster := strings.TrimSpace(r.URL.Query().Get("cluster"))
	if len(cluster) > 200 {
		httpapi.Error(w, 422, "invalid_cluster", "Cluster filter is too long")
		return
	}
	rows, err := s.Store.DB.Query(r.Context(), `SELECT r.name,r.attributes,COALESCE(t.name,'')
 FROM cmdb_resources r
 JOIN cmdb_sync_sources src ON src.id=r.source_id AND src.kind=r.source_kind
 LEFT JOIN teams t ON t.id=r.owner_team_id
 WHERE r.kind='deployment' AND r.source_kind='kubernetes' AND NOT r.is_mock
 AND r.observed_at >= now()-interval '5 minutes'
 AND src.last_success_at IS NOT NULL AND src.environment_key=ANY($1::text[])
 AND r.attributes->>'environment_key'=src.environment_key
 AND ($2='' OR r.attributes->>'cluster'=$2)
 AND NOT EXISTS (SELECT 1 FROM cmdb_resource_applications l WHERE l.resource_id=r.id)
 ORDER BY r.name,r.id LIMIT 501`, envs, cluster)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Resource catalog unavailable")
		return
	}
	defer rows.Close()
	items := []discoveredService{}
	discoveredCluster := ""
	multipleClusters := false
	for rows.Next() {
		var name, team string
		var raw []byte
		if err = rows.Scan(&name, &raw, &team); err != nil {
			break
		}
		var attrs struct {
			Namespace    string            `json:"namespace"`
			ResourceType string            `json:"resource_type"`
			Cluster      string            `json:"cluster"`
			Images       []string          `json:"images"`
			Replicas     *int              `json:"replicas"`
			Labels       map[string]string `json:"labels"`
		}
		if err = json.Unmarshal(raw, &attrs); err != nil {
			break
		}
		if cluster == "" {
			if len(items) == 0 {
				discoveredCluster = attrs.Cluster
			} else if discoveredCluster != attrs.Cluster {
				multipleClusters = true
			}
		}
		image := ""
		if len(attrs.Images) > 0 {
			image = attrs.Images[0]
		}
		owner := team
		if owner == "" {
			owner = attrs.Labels["team"]
		}
		items = append(items, discoveredService{ID: name, Namespace: attrs.Namespace, Kind: attrs.ResourceType,
			Replicas: attrs.Replicas, Image: image, OwnerHint: owner, Matched: []string{}, Missing: []string{}})
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Resource catalog unavailable")
		return
	}
	if len(items) > 500 {
		httpapi.Error(w, 422, "result_limit", "Narrow the cluster or environment filter")
		return
	}
	if cluster == "" && !multipleClusters {
		cluster = discoveredCluster
	}
	registeredRows, err := s.Store.DB.Query(r.Context(), `SELECT DISTINCT a.id,e.environment_id FROM applications a
 JOIN v3_application_environments e ON e.application_id=a.id
 WHERE NOT a.archived AND e.environment_id=ANY($1::text[])`, envs)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Service registry unavailable")
		return
	}
	registeredIDs := map[string]bool{}
	for registeredRows.Next() {
		var id, env string
		if err = registeredRows.Scan(&id, &env); err != nil {
			break
		}
		decisionErr := s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: env, ApplicationID: id})
		if decisionErr == nil {
			registeredIDs[id] = true
		} else if !errors.Is(decisionErr, store.ErrForbidden) {
			err = decisionErr
			break
		}
	}
	if err == nil {
		err = registeredRows.Err()
	}
	registeredRows.Close()
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Service registry unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"cluster": cluster, "items": items, "registered": len(registeredIDs)})
}

// observationTargets use only persisted application/environment bindings. An
// arbitrary query string never becomes a provider URL or an unbounded scope.
func (s *Service) observationTargets(ctx context.Context, user core.User, environments []string, module, service string, providers []string) ([]observationTarget, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT a.id,ve.environment_id,b.kind,b.resource
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, resource text) ON b.kind=ANY($3::text[])
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[]) AND ($2='' OR a.id=$2 OR a.key=$2)
 AND b.resource<>'' ORDER BY a.id,ve.environment_id,b.kind LIMIT 101`, environments, service, providers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []observationTarget{}
	seen := map[string]bool{}
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > 100 {
			return nil, errors.New("observation scope exceeds bound")
		}
		var target observationTarget
		if err = rows.Scan(&target.ApplicationID, &target.EnvironmentID, &target.Provider, &target.Resource); err != nil {
			return nil, err
		}
		if err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: target.EnvironmentID, ApplicationID: target.ApplicationID}); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				continue
			}
			return nil, err
		}
		key := target.ApplicationID + "\x00" + target.EnvironmentID
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, target)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > 100 {
		return nil, errors.New("observation scope exceeds bound")
	}
	return result, nil
}

func observationWindow() connectors.TimeWindow {
	now := time.Now().UTC()
	return connectors.TimeWindow{Start: now.Add(-15 * time.Minute), End: now}
}
