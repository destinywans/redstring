package v3topology

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
)

// MetricReader must be a source-bound reader that checks the current service,
// connector and query authorization before and after its remote query. The
// production PrometheusReader implements this contract. An operator must
// configure an app/environment-scoped, reviewed topology_v1 recording query;
// a matching catalog key alone cannot prove the expression's data scope.
type MetricReader interface {
	QueryService(context.Context, httpapi.V3EnvironmentScope, core.User, string, string, string,
		connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error)
}

type fullQuery struct {
	Cluster, Around, GroupBy string
	Depth                    int
}

func parseFullQuery(raw string) (fullQuery, bool) {
	var out fullQuery
	q, err := url.ParseQuery(raw)
	if err != nil || len(q) > 4 {
		return out, false
	}
	for key, values := range q {
		if len(values) != 1 || len(values[0]) > 200 ||
			(key != "cluster" && key != "around" && key != "depth" && key != "group_by") {
			return out, false
		}
	}
	out.Cluster, out.Around, out.GroupBy = q.Get("cluster"), q.Get("around"), q.Get("group_by")
	if out.Around == "" || strings.TrimSpace(out.Around) != out.Around ||
		strings.TrimSpace(out.Cluster) != out.Cluster ||
		(out.GroupBy != "" && out.GroupBy != "cluster" && out.GroupBy != "namespace" && out.GroupBy != "zone") {
		return out, false
	}
	out.Depth = 2
	if q.Has("depth") {
		out.Depth, err = strconv.Atoi(q.Get("depth"))
		if err != nil || out.Depth < 1 || out.Depth > 3 {
			return fullQuery{}, false
		}
	}
	return out, true
}

var errTopologyCenterHidden = errors.New("topology center not available")

// Keep per-request object authorization bounded even when a center has many
// hidden neighbors. A larger graph needs a separate paginated contract.
const maxTopologyCandidateIDs = 128

type scopedTopology struct {
	Nodes map[string]node
	Edges []edge
	AppID string
}

// Full projection is deliberately bounded to one application and a small
// selected neighborhood. The existing partial CMDB constructor remains useful
// for broader exploration, but cannot masquerade as the frozen V3 DTO.
func (s *Service) scopedFullTopology(ctx context.Context, user core.User, env string, q fullQuery) (scopedTopology, error) {
	var empty scopedTopology
	centerCandidates, err := s.loadCandidates(ctx, []string{env}, q.Cluster, q.Around, nil, 2)
	if err != nil {
		return empty, err
	}
	centerVisible, err := s.authorizedNodes(ctx, user, centerCandidates, []string{env})
	if err != nil {
		return empty, err
	}
	if _, ok := centerVisible[q.Around]; !ok {
		return empty, errTopologyCenterHidden
	}
	// Read only the selected center's bounded graph component. Unrelated hidden
	// resources in the same cluster cannot make a small visible view return 413.
	ids, err := s.relatedResourceIDs(ctx, q.Around, q.Depth)
	if err != nil {
		return empty, err
	}
	candidates, err := s.loadCandidates(ctx, []string{env}, q.Cluster, "", ids, maxTopologyCandidateIDs+1)
	if err != nil {
		return empty, err
	}
	nodes, err := s.authorizedNodes(ctx, user, candidates, []string{env})
	if err != nil {
		return empty, err
	}
	if _, ok := nodes[q.Around]; !ok {
		return empty, errTopologyCenterHidden
	}
	edges, err := s.loadEdges(ctx, nodes)
	if err != nil {
		return empty, err
	}
	nodes, edges = neighborhood(nodes, edges, q.Around, q.Depth)
	if len(nodes) > 8 || len(edges) > 12 {
		return empty, errTooBroad
	}
	app, err := singleTopologyApplication(nodes, env)
	if err != nil {
		return empty, err
	}
	return scopedTopology{Nodes: nodes, Edges: edges, AppID: app}, nil
}

// The discovery cap protects this request before per-object policy checks. It
// is deliberately distinct from the visible graph's 413 limit: this coarse
// source failure does not disclose a hidden candidate count or graph shape.
func (s *Service) relatedResourceIDs(ctx context.Context, center string, depth int) ([]string, error) {
	seen := map[string]bool{center: true}
	frontier := []string{center}
	for step := 0; step < depth && len(frontier) > 0; step++ {
		rows, err := s.DB.Query(ctx, `SELECT DISTINCT CASE
 WHEN source_resource_id=ANY($1::text[]) THEN target_resource_id ELSE source_resource_id END AS neighbor
FROM cmdb_resource_relations
WHERE NOT is_mock AND (source_resource_id=ANY($1::text[]) OR target_resource_id=ANY($1::text[]))
ORDER BY neighbor LIMIT 129`, frontier)
		if err != nil {
			return nil, err
		}
		next := []string{}
		read := 0
		for rows.Next() {
			read++
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			if !seen[id] {
				seen[id] = true
				next = append(next, id)
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if read > maxTopologyCandidateIDs || len(seen) > maxTopologyCandidateIDs {
			return nil, errTopologyIncomplete
		}
		frontier = next
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

func singleTopologyApplication(nodes map[string]node, env string) (string, error) {
	app := ""
	for _, item := range nodes {
		matching := map[string]bool{}
		for _, link := range item.AppLinks {
			if link.EnvironmentID == env {
				matching[link.ApplicationID] = true
			}
		}
		if len(matching) != 1 {
			return "", errTopologyIncomplete
		}
		for id := range matching {
			if app != "" && app != id {
				return "", errTopologyIncomplete
			}
			app = id
		}
	}
	if app == "" {
		return "", errTopologyIncomplete
	}
	return app, nil
}

func writeFullTopologyError(w http.ResponseWriter, err error) {
	var readErr *v3integrations.PrometheusReadError
	switch {
	case errors.Is(err, errTopologyCenterHidden):
		httpapi.Error(w, 404, "topology_center_not_found", "Center resource is unavailable in this scope")
	case errors.Is(err, errTooBroad):
		httpapi.Error(w, 413, "topology_scope_too_broad", "Select a smaller resource neighborhood")
	case errors.Is(err, store.ErrSessionRevoked), errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "topology_forbidden", "Current topology access is required")
	case errors.As(err, &readErr) && readErr.Code == "forbidden":
		httpapi.Error(w, 403, "topology_forbidden", "Current topology source access is required")
	default:
		httpapi.Error(w, 503, "topology_source_incomplete", "Complete current topology evidence is unavailable")
	}
}

func (s *Service) serveFullTopology(w http.ResponseWriter, r *http.Request, scope httpapi.V3EnvironmentScope) {
	q, ok := parseFullQuery(r.URL.RawQuery)
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, 422, "invalid_topology_query", "Select one environment and a center resource")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	user, env := httpapi.CurrentUser(r), scope.EnvironmentIDs[0]
	graph, err := s.scopedFullTopology(ctx, user, env, q)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	end := now.Truncate(time.Minute)
	window := connectors.TimeWindow{Start: end.Add(-5 * time.Minute), End: end, StepSeconds: 60}
	result, err := s.MetricReader.QueryService(ctx, scope, user, graph.AppID, env, "topology_v1", window)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	obs, err := parseTopologyObservations(result, window, graph.AppID, env, graph.Nodes, graph.Edges, q.Around)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	current, err := s.scopedFullTopology(ctx, user, env, q)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	if !reflect.DeepEqual(current, graph) {
		writeFullTopologyError(w, errTopologyIncomplete)
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		writeFullTopologyError(w, store.ErrSessionRevoked)
		return
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	view, err := projectFullTopology(graph.Nodes, graph.Edges, obs, q.Around, q.GroupBy)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, 200, view)
}
