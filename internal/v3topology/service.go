// Package v3topology exposes an authorized CMDB graph and a bounded V3 view
// when complete source-bound observations are configured.
package v3topology

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Service struct {
	DB           *pgxpool.Pool
	Access       Access
	MetricReader MetricReader
	Now          func() time.Time
}

func New(db *pgxpool.Pool, access Access) *Service {
	return &Service{DB: db, Access: access, Now: time.Now}
}

// NewWithMetrics enables the complete, source-bound V3 projection. The
// existing constructor retains its explicitly partial CMDB response until the
// composition root configures a reviewed topology_v1 Prometheus query.
func NewWithMetrics(db *pgxpool.Pool, access Access, reader MetricReader) *Service {
	s := New(db, access)
	s.MetricReader = reader
	return s
}

// GET /topology checks each persisted resource before projecting it. The route
// must be marked object-scoped: a route-level module decision has no resource
// identity and would incorrectly suppress application/team grants.
func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/topology", "viewer", s.topology)
	api.MarkV3ObjectRoute("GET /api/v1/topology", "resources", "view")
}

type appLink struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
	TeamID        string `json:"team_id"`
}

type candidate struct {
	ID, Name, Kind, Cluster, Namespace, Zone, EnvironmentID, TeamID string
	SourceKind, SourceID, ExternalID, Owner                         string
	ObservedAt                                                      *time.Time
	UpdatedAt                                                       time.Time
	Links                                                           []appLink
	FallbackEligible                                                bool
}

type node struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	Cluster        string     `json:"cluster,omitempty"`
	EnvironmentIDs []string   `json:"environment_ids"`
	Namespace      string     `json:"-"`
	Zone           string     `json:"-"`
	SourceKind     string     `json:"-"`
	SourceID       string     `json:"-"`
	ExternalID     string     `json:"-"`
	Owner          string     `json:"-"`
	ObservedAt     *time.Time `json:"-"`
	UpdatedAt      time.Time  `json:"-"`
	AppLinks       []appLink  `json:"-"`
}

type edge struct {
	ID             string    `json:"id"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	Kind           string    `json:"kind"`
	Provenance     string    `json:"provenance"`
	EnvironmentIDs []string  `json:"environment_ids"`
	UpdatedAt      time.Time `json:"-"`
}

type projection struct {
	CompleteV3DTO     bool     `json:"complete_v3_dto"`
	Source            string   `json:"source"`
	Nodes             []node   `json:"nodes"`
	Edges             []edge   `json:"edges"`
	UnavailableFields []string `json:"unavailable_fields"`
	// Collection says whether inventory collectors are current (PA-61); a
	// stale collector's resources are left out, so an empty graph is not
	// "no resources" when it says stale or never.
	Collection []collectionStatus `json:"collection"`
}

var missing = []string{"groups", "nodes.status", "nodes.x", "nodes.y", "nodes.impacted", "edges.tone", "edges.path", "blast", "tree", "sankey", "detail", "complete_resource_coverage"}

func (s *Service) topology(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "topology_unavailable", "Topology storage or policy is unavailable")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	if s.MetricReader != nil {
		s.serveFullTopology(w, r, scope)
		return
	}
	q := r.URL.Query()
	for _, key := range []string{"cluster", "around", "depth", "group_by"} {
		if len(q[key]) > 1 || len(q.Get(key)) > 200 {
			httpapi.Error(w, 422, "invalid_topology_query", "Invalid topology filter")
			return
		}
	}
	if q.Get("group_by") != "" {
		httpapi.Error(w, 422, "unsupported_topology_grouping", "Grouped topology is not available from the current resource index")
		return
	}
	depth := 2
	if q.Get("depth") != "" {
		var err error
		depth, err = strconv.Atoi(q.Get("depth"))
		if err != nil || depth < 1 || depth > 3 {
			httpapi.Error(w, 422, "invalid_topology_query", "Depth must be from 1 to 3")
			return
		}
	}
	candidates, err := s.loadCandidates(r.Context(), scope.EnvironmentIDs, strings.TrimSpace(q.Get("cluster")), "", nil, 1000)
	if err != nil {
		httpapi.Error(w, 503, "topology_unavailable", "Resource index unavailable")
		return
	}
	nodes, err := s.authorizedNodes(r.Context(), httpapi.CurrentUser(r), candidates, scope.EnvironmentIDs)
	if err != nil {
		httpapi.Error(w, 503, "authorization_unavailable", "Resource authorization unavailable")
		return
	}
	collection, err := collectionStatuses(r.Context(), s.DB, httpapi.CurrentUser(r).ID, scope.EnvironmentIDs)
	if err != nil {
		httpapi.Error(w, 503, "topology_unavailable", "Collector state unavailable")
		return
	}
	if writeEmptyIfNoVisible(w, nodes, collection) {
		return
	}
	if len(nodes) > 300 && q.Get("around") == "" {
		httpapi.Error(w, 422, "topology_scope_too_broad", "Choose a center resource or narrow the cluster")
		return
	}
	edges, err := s.loadEdges(r.Context(), nodes)
	if err != nil {
		if errors.Is(err, errTooBroad) {
			httpapi.Error(w, 422, "topology_scope_too_broad", "Choose a center resource or narrow the cluster")
		} else {
			httpapi.Error(w, 503, "topology_unavailable", "Resource relationships unavailable")
		}
		return
	}
	if around := strings.TrimSpace(q.Get("around")); around != "" {
		if _, exists := nodes[around]; !exists {
			httpapi.Error(w, 404, "topology_center_not_found", "Center resource is unavailable in this scope")
			return
		}
		nodes, edges = neighborhood(nodes, edges, around, depth)
	}
	if len(nodes) > 300 || len(edges) > 600 {
		httpapi.Error(w, 422, "topology_scope_too_broad", "Choose a center resource or narrow the cluster")
		return
	}
	out := projection{Source: "cmdb_resource_relations", Nodes: make([]node, 0, len(nodes)), Edges: edges, UnavailableFields: missing,
		Collection: collection}
	for _, item := range nodes {
		out.Nodes = append(out.Nodes, item)
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	httpapi.WriteJSON(w, 200, out)
}

// A hidden cluster and an absent cluster produce the same empty projection.
// The caller must not inspect the number of pre-authorization candidates.
func writeEmptyIfNoVisible(w http.ResponseWriter, nodes map[string]node, collection []collectionStatus) bool {
	if len(nodes) != 0 {
		return false
	}
	httpapi.WriteJSON(w, 200, projection{
		Source: "cmdb_resource_relations", Nodes: []node{}, Edges: []edge{}, UnavailableFields: missing,
		Collection: collection,
	})
	return true
}

var errTooBroad = errors.New("topology scope exceeds bounded read")

// A current application link is authoritative for a linked resource. An
// unlinked resource can use its collector environment only when its source is
// a recognized inventory collector, with a matching persisted source record.
// Manually edited attributes cannot create an additional environment scope.
const resourceQuery = `SELECT r.id,r.name,r.kind,COALESCE(r.attributes->>'cluster',''),
 COALESCE(r.attributes->>'namespace',''),COALESCE(r.attributes->>'zone',''),
 COALESCE(r.attributes->>'environment_key',''),COALESCE(r.owner_team_id,''),
 r.source_kind,r.source_id,r.external_id,COALESCE(ou.name,ot.name,'Unassigned'),
 r.observed_at,r.updated_at,links.items,
 r.source_kind IN ('kubernetes','docker','lima') AND r.observed_at IS NOT NULL
 AND EXISTS (SELECT 1 FROM cmdb_sync_sources src WHERE src.id=r.source_id AND src.kind=r.source_kind
  AND src.last_success_at IS NOT NULL AND src.environment_key<>''
  AND src.environment_key=r.attributes->>'environment_key')
 AND NOT EXISTS (SELECT 1 FROM cmdb_resource_applications l WHERE l.resource_id=r.id)
FROM cmdb_resources r
LEFT JOIN users ou ON ou.id=r.owner_user_id
LEFT JOIN teams ot ON ot.id=r.owner_team_id
LEFT JOIN LATERAL (
 SELECT COALESCE(jsonb_agg(jsonb_build_object('application_id',a.id,
  'environment_id',ve.environment_id,'team_id',COALESCE(a.owner_team_id,''))
  ORDER BY a.id,ve.environment_id), '[]'::jsonb) items
 FROM cmdb_resource_applications l
 JOIN applications a ON a.id=l.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=l.environment_id AND e.application_id=a.id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=e.key
 WHERE l.resource_id=r.id AND ve.environment_id=ANY($1::text[])
 AND (r.source_kind='manual' OR EXISTS (SELECT 1 FROM cmdb_sync_sources src
  WHERE src.id=r.source_id AND src.kind=r.source_kind AND src.environment_key=ve.environment_id))
) links ON true
WHERE NOT r.is_mock AND r.kind NOT IN ('event','pod')
 -- A collector record is an observation, not a timeless CMDB declaration.
 -- Both the object and its source must have fresh successful evidence.
 AND (r.source_kind='manual' OR (
  r.source_kind IN ('kubernetes','docker','lima')
  AND r.attributes->>'inventory_presence'='present'
  AND r.observed_at BETWEEN now()-interval '5 minutes' AND now()+interval '1 minute'
  AND EXISTS (SELECT 1 FROM cmdb_sync_sources src WHERE src.id=r.source_id AND src.kind=r.source_kind
   AND src.last_success_at BETWEEN now()-interval '5 minutes' AND now()+interval '1 minute')))
 AND (links.items<>'[]'::jsonb OR (
  r.source_kind IN ('kubernetes','docker','lima') AND r.observed_at IS NOT NULL
  AND EXISTS (SELECT 1 FROM cmdb_sync_sources src WHERE src.id=r.source_id AND src.kind=r.source_kind
   AND src.last_success_at IS NOT NULL AND src.environment_key=ANY($1::text[])
   AND src.environment_key=r.attributes->>'environment_key')
  AND NOT EXISTS (SELECT 1 FROM cmdb_resource_applications l WHERE l.resource_id=r.id)
  AND r.attributes->>'environment_key'=ANY($1::text[])))
 AND ($2='' OR r.attributes->>'cluster'=$2)
 AND ($3='' OR r.id=$3)
 AND ($5::text[] IS NULL OR r.id=ANY($5::text[]))
-- The response is explicitly partial: never expose a count or limit error
-- based on pre-authorization candidates, which would reveal hidden clusters.
ORDER BY r.id LIMIT $4`

func (s *Service) loadCandidates(ctx context.Context, envs []string, cluster, around string, ids []string, limit int) ([]candidate, error) {
	rows, err := s.DB.Query(ctx, resourceQuery, envs, cluster, around, limit, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []candidate{}
	for rows.Next() {
		var item candidate
		var links []byte
		if err = rows.Scan(&item.ID, &item.Name, &item.Kind, &item.Cluster, &item.Namespace,
			&item.Zone, &item.EnvironmentID, &item.TeamID, &item.SourceKind, &item.SourceID,
			&item.ExternalID, &item.Owner, &item.ObservedAt, &item.UpdatedAt, &links,
			&item.FallbackEligible); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(links, &item.Links); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) authorizedNodes(ctx context.Context, user core.User, candidates []candidate, selected []string) (map[string]node, error) {
	out := make(map[string]node, len(candidates))
	for _, item := range candidates {
		authorized := []string{}
		for _, env := range selected {
			if !eligibleEnvironment(item, env) {
				continue
			}
			allowed, err := s.canView(ctx, user, item, env)
			if err != nil {
				return nil, err
			}
			if allowed {
				authorized = append(authorized, env)
			}
		}
		if len(authorized) > 0 {
			sort.Strings(authorized)
			out[item.ID] = node{ID: item.ID, Name: item.Name, Kind: item.Kind, Cluster: item.Cluster,
				EnvironmentIDs: authorized, Namespace: item.Namespace, Zone: item.Zone,
				SourceKind: item.SourceKind, SourceID: item.SourceID, ExternalID: item.ExternalID,
				Owner: item.Owner, ObservedAt: item.ObservedAt, UpdatedAt: item.UpdatedAt,
				AppLinks: item.Links}
		}
	}
	return out, nil
}

func hasLink(links []appLink, env string) bool {
	for _, link := range links {
		if link.EnvironmentID == env {
			return true
		}
	}
	return false
}

func eligibleEnvironment(item candidate, env string) bool {
	return hasLink(item.Links, env) || item.FallbackEligible && len(item.Links) == 0 && item.EnvironmentID == env
}

func (s *Service) canView(ctx context.Context, user core.User, item candidate, env string) (bool, error) {
	decisions := []rbac.V3Decision{}
	for _, link := range item.Links {
		if link.EnvironmentID != env {
			continue
		}
		decisions = append(decisions, rbac.V3Decision{Module: "resources", Action: "view", EnvironmentID: env, ApplicationID: link.ApplicationID})
		if link.TeamID != "" {
			decisions = append(decisions, rbac.V3Decision{Module: "resources", Action: "view", EnvironmentID: env, TeamID: link.TeamID})
		}
	}
	// A resource's own scoped team or a global/environment grant can authorize
	// an unlinked infrastructure item; inferred topology edges cannot grant it.
	if len(item.Links) == 0 && eligibleEnvironment(item, env) {
		decisions = append(decisions, rbac.V3Decision{Module: "resources", Action: "view", EnvironmentID: env, TeamID: item.TeamID})
	}
	for _, decision := range decisions {
		err := s.Access.Authorize(ctx, user, decision)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, store.ErrForbidden) {
			return false, err
		}
	}
	return false, nil
}

func (s *Service) loadEdges(ctx context.Context, nodes map[string]node) ([]edge, error) {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return []edge{}, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT rel.id,rel.source_resource_id,rel.target_resource_id,
 rel.kind,rel.provenance,rel.updated_at,rel.source_kind,rel.source_id,
 COALESCE(src.environment_key,'')
FROM cmdb_resource_relations rel
LEFT JOIN cmdb_sync_sources src ON src.id=rel.source_id AND src.kind=rel.source_kind
WHERE NOT rel.is_mock AND rel.source_resource_id=ANY($1::text[])
 AND rel.target_resource_id=ANY($1::text[])
 AND (rel.provenance='declared' OR (
  rel.provenance='discovered' AND src.last_success_at
   BETWEEN now()-interval '5 minutes' AND now()+interval '1 minute'
  AND src.environment_key<>''))
ORDER BY rel.id LIMIT 2001`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []edge{}
	read := 0
	for rows.Next() {
		read++
		if read > 2000 {
			return nil, errTooBroad
		}
		var item edge
		var sourceKind, sourceID, sourceEnv string
		if err = rows.Scan(&item.ID, &item.From, &item.To, &item.Kind, &item.Provenance,
			&item.UpdatedAt, &sourceKind, &sourceID, &sourceEnv); err != nil {
			return nil, err
		}
		item.EnvironmentIDs = intersection(nodes[item.From].EnvironmentIDs, nodes[item.To].EnvironmentIDs)
		if item.Provenance == "discovered" {
			// A relation observed in another collector/environment cannot gain
			// this graph's scope merely because its endpoints are visible.
			if sourceKind != nodes[item.From].SourceKind || sourceKind != nodes[item.To].SourceKind ||
				sourceID != nodes[item.From].SourceID || sourceID != nodes[item.To].SourceID {
				continue
			}
			if !containsEnvironment(item.EnvironmentIDs, sourceEnv) {
				continue
			}
			item.EnvironmentIDs = []string{sourceEnv}
		}
		if len(item.EnvironmentIDs) > 0 {
			out = append(out, item)
		}
	}
	return out, rows.Err()
}

func containsEnvironment(environments []string, wanted string) bool {
	for _, id := range environments {
		if id == wanted {
			return true
		}
	}
	return false
}

func intersection(a, b []string) []string {
	out := []string{}
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

func neighborhood(nodes map[string]node, edges []edge, center string, depth int) (map[string]node, []edge) {
	seen := map[string]bool{center: true}
	frontier := []string{center}
	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		next := []string{}
		for _, link := range edges {
			for _, from := range frontier {
				to := ""
				if link.From == from {
					to = link.To
				} else if link.To == from {
					to = link.From
				}
				if to != "" && !seen[to] {
					seen[to] = true
					next = append(next, to)
				}
			}
		}
		frontier = next
	}
	selected := map[string]node{}
	for id := range seen {
		selected[id] = nodes[id]
	}
	links := []edge{}
	for _, link := range edges {
		if seen[link.From] && seen[link.To] {
			links = append(links, link)
		}
	}
	return selected, links
}
