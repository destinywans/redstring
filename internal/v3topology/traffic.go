package v3topology

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
)

const TrafficQueryID = "topology_service_traffic"

var trafficServiceKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,99}$`)

// A node is an actual registered application, keyed by its operator-reviewed
// service label. Unknown peers from trace attributes cannot become chart nodes.
type trafficApplication struct{ ID, Key, Name, Team string }

type trafficView struct {
	ObservedAt time.Time      `json:"observed_at"`
	Source     string         `json:"source"`
	Sankey     topologySankey `json:"sankey"`
}

func (s *Service) authorizedTrafficApplications(ctx context.Context, user core.User, env string) (map[string]trafficApplication, error) {
	rows, err := s.DB.Query(ctx, `SELECT a.id,a.key,a.name,COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$1
 JOIN application_environments e ON e.application_id=a.id AND e.key=$1 AND NOT e.archived
 WHERE NOT a.archived ORDER BY a.id LIMIT 101`, env)
	if err != nil {
		return nil, err
	}
	var candidates []trafficApplication
	for rows.Next() {
		var app trafficApplication
		if err = rows.Scan(&app.ID, &app.Key, &app.Name, &app.Team); err != nil {
			break
		}
		candidates = append(candidates, app)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > 100 {
		return nil, errTopologyIncomplete
	}
	apps := map[string]trafficApplication{}
	for _, app := range candidates {
		if !trafficServiceKey.MatchString(app.Key) {
			continue
		}
		for _, module := range []string{"resources", "services"} {
			err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: env, ApplicationID: app.ID})
			if errors.Is(err, store.ErrForbidden) && app.Team != "" {
				err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: env, TeamID: app.Team})
			}
			if err != nil {
				break
			}
		}
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		apps[app.Key] = app
	}
	return apps, nil
}

func (s *Service) traffic(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil || s.Access == nil || s.TrafficReader == nil {
		writeFullTopologyError(w, errTopologyIncomplete)
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	q := r.URL.Query()
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 || len(q) != 1 || len(q["service"]) != 1 || len(q.Get("service")) > 200 || strings.TrimSpace(q.Get("service")) != q.Get("service") {
		httpapi.Error(w, 422, "invalid_topology_query", "Select one environment and one service")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	env, user := scope.EnvironmentIDs[0], httpapi.CurrentUser(r)
	apps, err := s.authorizedTrafficApplications(ctx, user, env)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	var selected trafficApplication
	for _, app := range apps {
		if q.Get("service") == app.ID {
			selected = app
			break
		}
	}
	if selected.ID == "" {
		writeFullTopologyError(w, errTopologyCenterHidden)
		return
	}
	end := time.Now().UTC().Truncate(time.Second)
	window := connectors.TimeWindow{Start: end.Add(-30 * time.Second), End: end, StepSeconds: 30}
	observed, err := s.TrafficReader.QueryService(ctx, scope, user, selected.ID, env, TrafficQueryID, window)
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	current, err := s.authorizedTrafficApplications(ctx, user, env)
	if err == nil && current[selected.Key] != selected {
		err = store.ErrForbidden
	}
	if err != nil {
		writeFullTopologyError(w, err)
		return
	}
	sankey, err := projectServiceTraffic(observed, end, env, selected, current)
	if err != nil {
		writeFullTopologyError(w, err)
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
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, trafficView{ObservedAt: end, Source: "prometheus", Sankey: sankey})
}

func projectServiceTraffic(data v3integrations.PrometheusMetricEvidence, at time.Time, env string, selected trafficApplication,
	apps map[string]trafficApplication) (topologySankey, error) {
	out := topologySankey{Cols: [][]topologySankeyNode{{}, {}}, Links: []topologySankeyLink{}}
	if data.ApplicationID != selected.ID || data.EnvironmentID != env || data.QueryID != TrafficQueryID || len(data.Series) >= 100 {
		return out, errTopologyIncomplete
	}
	const color = "var(--c3)"
	totals := []map[string]float64{{}, {}}
	seen := map[string]bool{}
	for _, line := range data.Series {
		client, server := line.Labels["client"], line.Labels["server"]
		pair := client + "\x00" + server
		if len(line.Labels) != 2 || client != selected.Key && server != selected.Key || seen[pair] {
			return out, errTopologyIncomplete
		}
		seen[pair] = true
		if apps[client].ID == "" || apps[server].ID == "" || len(line.Samples) == 0 {
			continue
		}
		sample := line.Samples[len(line.Samples)-1]
		if sample.Timestamp != float64(at.Unix()) {
			continue // no current value for this edge
		}
		rate, err := strconv.ParseFloat(sample.Value, 64)
		if err != nil || !finite(rate) || rate < 0 {
			return out, errTopologyIncomplete
		}
		if rate == 0 {
			continue
		}
		totals[0][client] += rate
		totals[1][server] += rate
		out.Links = append(out.Links, topologySankeyLink{From: "src:" + client, To: "dst:" + server, Color: color, Value: rate})
	}
	sort.Slice(out.Links, func(i, j int) bool {
		a, b := out.Links[i], out.Links[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	for column, prefix := range []string{"src:", "dst:"} {
		for key, rate := range totals[column] {
			if !finite(rate) {
				return out, errTopologyIncomplete
			}
			out.Cols[column] = append(out.Cols[column], topologySankeyNode{ID: prefix + key, Label: apps[key].Name, Value: rate, Color: color})
		}
		sort.Slice(out.Cols[column], func(i, j int) bool { return out.Cols[column][i].ID < out.Cols[column][j].ID })
	}
	return out, nil
}
