package v3topology

import (
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/v3integrations"
)

// errTopologyIncomplete intentionally carries no upstream labels or identifiers.
var errTopologyIncomplete = errors.New("topology evidence incomplete")

type topologyObservation struct {
	Nodes      map[string]nodeObservation
	Edges      map[string]edgeObservation
	Center     centerObservation
	CapturedAt time.Time
}

type nodeObservation struct {
	Status         string
	Impacted       bool
	CPU, Mem       *float64
	Ready, Desired *int
}

type edgeObservation struct {
	RPS, ErrorPct float64
}

type centerObservation struct {
	Used, Max, FromUsed, FromMax int
	FromID                       string
	Wait                         []float64
}

type observationSeries struct {
	field  string
	value  float64
	values []float64
	labels map[string]string
}

// parseTopologyObservations accepts only complete, version-bound observations
// for the exact authorized graph supplied by the caller. Prometheus's successful
// matrix response alone does not prove full coverage or resource identity.
func parseTopologyObservations(result v3integrations.PrometheusMetricEvidence, window connectors.TimeWindow,
	app, env string, nodes map[string]node, edges []edge, center string) (topologyObservation, error) {
	bad := func() (topologyObservation, error) { return topologyObservation{}, errTopologyIncomplete }
	now := time.Now()
	if app == "" || env == "" || center == "" || len(nodes) == 0 || len(result.Series) == 0 || len(result.Series) > 99 || result.Empty ||
		result.ApplicationID != app || result.EnvironmentID != env || result.QueryID != "topology_v1" ||
		result.BindingID == "" || result.ConnectorID == "" || result.ResourceID == "" || result.CapturedAt.IsZero() ||
		result.CapturedAt.Before(window.End) || result.CapturedAt.After(now.Add(time.Minute)) ||
		window.Start.IsZero() || window.End.IsZero() || window.StepSeconds != 60 ||
		window.End.Sub(window.Start) != 5*time.Minute || window.End.Before(now.Add(-2*time.Minute)) ||
		window.End.After(now.Add(time.Minute)) {
		return bad()
	}
	if _, ok := nodes[center]; !ok {
		return bad()
	}
	edgeByID := make(map[string]edge, len(edges))
	incoming := make(map[string]bool)
	for _, e := range edges {
		if e.ID == "" || e.From == "" || e.To == "" || e.UpdatedAt.IsZero() || edgeByID[e.ID].ID != "" {
			return bad()
		}
		if _, ok := nodes[e.From]; !ok {
			return bad()
		}
		if _, ok := nodes[e.To]; !ok {
			return bad()
		}
		edgeByID[e.ID] = e
		if e.To == center {
			incoming[e.From] = true
		}
	}
	seen := make(map[string]observationSeries, len(result.Series))
	for _, series := range result.Series {
		labels := series.Labels
		field, unit := labels["field"], labels["unit"]
		if labels["schema"] != "topology_v1" || labels["application_id"] != app || labels["environment_id"] != env ||
			field == "" || unit == "" || len(series.Samples) != 6 {
			return bad()
		}
		kind, expectedUnit, ok := topologyField(field)
		if !ok || unit != expectedUnit {
			return bad()
		}
		extra := []string(nil)
		key := ""
		switch kind {
		case "node", "center":
			id := labels["resource_id"]
			n, found := nodes[id]
			if !found || id == "" || n.ExternalID == "" || n.SourceKind == "" || n.SourceID == "" ||
				labels["external_uid"] != n.ExternalID || labels["source_kind"] != n.SourceKind || labels["source_id"] != n.SourceID {
				return bad()
			}
			if kind == "center" && id != center {
				return bad()
			}
			extra = []string{"resource_id", "external_uid", "source_kind", "source_id"}
			if field == "center_from_used" || field == "center_from_max" {
				if !incoming[labels["caller_id"]] || labels["caller_id"] == "" {
					return bad()
				}
				extra = append(extra, "caller_id")
				key = field + ":" + id + ":" + labels["caller_id"]
			} else {
				key = field + ":" + id
			}
		case "edge":
			id := labels["relation_id"]
			e, found := edgeByID[id]
			revision, parseErr := time.Parse(time.RFC3339Nano, labels["relation_revision"])
			if !found || id == "" || labels["from_resource_id"] != e.From || labels["to_resource_id"] != e.To ||
				parseErr != nil || !revision.Equal(e.UpdatedAt) {
				return bad()
			}
			extra = []string{"relation_id", "from_resource_id", "to_resource_id", "relation_revision"}
			key = field + ":" + id
		default:
			return bad()
		}
		if !exactTopologyLabels(labels, extra) {
			return bad()
		}
		if _, duplicate := seen[key]; duplicate {
			return bad()
		}
		values := make([]float64, 6)
		for i, sample := range series.Samples {
			want := float64(window.Start.Add(time.Duration(i)*time.Minute).UnixNano()) / 1e9
			v, parseErr := strconv.ParseFloat(sample.Value, 64)
			if math.IsNaN(sample.Timestamp) || math.IsInf(sample.Timestamp, 0) || math.Abs(sample.Timestamp-want) > 0.000001 ||
				parseErr != nil || math.IsNaN(v) || math.IsInf(v, 0) || !validTopologyValue(field, v) {
				return bad()
			}
			values[i] = v
		}
		seen[key] = observationSeries{field: field, value: values[5], values: values, labels: labels}
	}
	out := topologyObservation{Nodes: make(map[string]nodeObservation, len(nodes)), Edges: make(map[string]edgeObservation, len(edges)), CapturedAt: result.CapturedAt}
	for id, n := range nodes {
		if id == "" || n.ID != id || n.ExternalID == "" || n.SourceKind == "" || n.SourceID == "" {
			return bad()
		}
		health, okHealth := seen["node_health:"+id]
		impact, okImpact := seen["node_impacted:"+id]
		if !okHealth || !okImpact {
			return bad()
		}
		ob := nodeObservation{Impacted: impact.value == 1}
		switch health.value {
		case 0:
			ob.Status = "ok"
		case 1:
			ob.Status = "warn"
		case 2:
			ob.Status = "danger"
		default:
			return bad()
		}
		if metric, ok := seen["node_cpu_pct:"+id]; ok {
			v := metric.value
			ob.CPU = &v
		}
		if metric, ok := seen["node_mem_pct:"+id]; ok {
			v := metric.value
			ob.Mem = &v
		}
		ready, hasReady := seen["node_replicas_ready:"+id]
		desired, hasDesired := seen["node_replicas_desired:"+id]
		if hasReady != hasDesired || (n.Kind == "deployment" && !hasReady) || (hasReady && !seriesWithin(ready, desired, false)) {
			return bad()
		}
		if hasReady {
			r, d := int(ready.value), int(desired.value)
			ob.Ready, ob.Desired = &r, &d
		}
		out.Nodes[id] = ob
	}
	for _, e := range edges {
		rps, okRPS := seen["edge_rps:"+e.ID]
		errorPct, okError := seen["edge_error_pct:"+e.ID]
		if !okRPS || !okError {
			return bad()
		}
		out.Edges[e.ID] = edgeObservation{RPS: rps.value, ErrorPct: errorPct.value}
	}
	used, okUsed := seen["center_connections_used:"+center]
	max, okMax := seen["center_connections_max:"+center]
	wait, okWait := seen["center_wait_p95_ms:"+center]
	if !okUsed || !okMax || !okWait || !seriesWithin(used, max, true) {
		return bad()
	}
	out.Center.Used, out.Center.Max, out.Center.Wait = int(used.value), int(max.value), wait.values
	var caller string
	for _, metric := range seen {
		if metric.field == "center_from_used" && metric.labels["resource_id"] == center {
			if caller != "" {
				return bad()
			}
			caller = metric.labels["caller_id"]
			fromMax, found := seen["center_from_max:"+center+":"+caller]
			if !found || !seriesWithin(metric, fromMax, true) {
				return bad()
			}
			out.Center.FromID, out.Center.FromUsed, out.Center.FromMax = caller, int(metric.value), int(fromMax.value)
		}
	}
	if caller == "" {
		return bad()
	}
	for key, metric := range seen {
		if metric.field == "center_from_max" && key != "center_from_max:"+center+":"+caller {
			return bad()
		}
	}
	return out, nil
}

func seriesWithin(used, max observationSeries, positiveMax bool) bool {
	for i := 0; i < 6; i++ {
		if (positiveMax && max.values[i] <= 0) || used.values[i] > max.values[i] {
			return false
		}
	}
	return true
}

func topologyField(field string) (kind, unit string, ok bool) {
	switch field {
	case "node_health":
		return "node", "state", true
	case "node_impacted":
		return "node", "bool", true
	case "node_cpu_pct", "node_mem_pct":
		return "node", "percent", true
	case "node_replicas_ready", "node_replicas_desired":
		return "node", "count", true
	case "edge_rps":
		return "edge", "requests_per_second", true
	case "edge_error_pct":
		return "edge", "percent", true
	case "center_connections_used", "center_connections_max", "center_from_used", "center_from_max":
		return "center", "count", true
	case "center_wait_p95_ms":
		return "center", "milliseconds", true
	default:
		return "", "", false
	}
}

func exactTopologyLabels(labels map[string]string, extra []string) bool {
	if len(labels) != 5+len(extra) {
		return false
	}
	for _, key := range []string{"schema", "application_id", "environment_id", "field", "unit"} {
		if labels[key] == "" {
			return false
		}
	}
	for _, key := range extra {
		if labels[key] == "" {
			return false
		}
	}
	return true
}

func validTopologyValue(field string, v float64) bool {
	if v < 0 || v > 1e9 {
		return false
	}
	switch field {
	case "node_health":
		return v == 0 || v == 1 || v == 2
	case "node_impacted":
		return v == 0 || v == 1
	case "node_cpu_pct", "node_mem_pct", "edge_error_pct":
		return v <= 100
	case "node_replicas_ready", "node_replicas_desired", "center_connections_used", "center_connections_max", "center_from_used", "center_from_max":
		return math.Trunc(v) == v && v <= float64(math.MaxInt32)
	default:
		return true
	}
}
