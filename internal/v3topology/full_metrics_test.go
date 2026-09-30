package v3topology

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

func topologyMetricFixture() (v3integrations.PrometheusMetricEvidence, connectors.TimeWindow, map[string]node, []edge) {
	end := time.Now().UTC().Truncate(time.Minute)
	window := connectors.TimeWindow{Start: end.Add(-5 * time.Minute), End: end, StepSeconds: 60}
	nodes := map[string]node{
		"work":  {ID: "work", Kind: "deployment", ExternalID: "uid-work", SourceKind: "kubernetes", SourceID: "cluster-1"},
		"cache": {ID: "cache", Kind: "redis", ExternalID: "uid-cache", SourceKind: "manual", SourceID: "inventory-1"},
	}
	edges := []edge{{ID: "relation-1", From: "work", To: "cache", UpdatedAt: end.Add(-time.Minute)}}
	result := v3integrations.PrometheusMetricEvidence{
		ApplicationID: "app-1", EnvironmentID: "env-1", BindingID: "binding-1", ConnectorID: "connector-1",
		ResourceID: "metric-source-1", QueryID: "topology_v1", CapturedAt: end.Add(time.Second),
	}
	add := func(field, unit, value string, extra map[string]string) {
		labels := map[string]string{"schema": "topology_v1", "application_id": "app-1", "environment_id": "env-1", "field": field, "unit": unit}
		for k, v := range extra {
			labels[k] = v
		}
		series := connectors.MetricSeries{Labels: labels}
		for i := 0; i < 6; i++ {
			series.Samples = append(series.Samples, connectors.MetricSample{
				Timestamp: float64(window.Start.Add(time.Duration(i) * time.Minute).Unix()), Value: value,
			})
		}
		result.Series = append(result.Series, series)
	}
	for _, id := range []string{"work", "cache"} {
		n := nodes[id]
		identity := map[string]string{"resource_id": id, "external_uid": n.ExternalID, "source_kind": n.SourceKind, "source_id": n.SourceID}
		add("node_health", "state", "0", identity)
		add("node_impacted", "bool", "0", identity)
		if id == "work" {
			add("node_replicas_ready", "count", "2", identity)
			add("node_replicas_desired", "count", "3", identity)
			add("node_cpu_pct", "percent", "45.5", identity)
		}
		if id == "cache" {
			for field, value := range map[string]string{"center_connections_used": "12", "center_connections_max": "100", "center_wait_p95_ms": "4.25"} {
				unit := "count"
				if field == "center_wait_p95_ms" {
					unit = "milliseconds"
				}
				add(field, unit, value, identity)
			}
			from := map[string]string{"resource_id": id, "external_uid": n.ExternalID, "source_kind": n.SourceKind, "source_id": n.SourceID, "caller_id": "work"}
			add("center_from_used", "count", "8", from)
			add("center_from_max", "count", "50", from)
		}
	}
	relation := map[string]string{"relation_id": edges[0].ID, "from_resource_id": "work", "to_resource_id": "cache", "relation_revision": edges[0].UpdatedAt.Format(time.RFC3339Nano)}
	add("edge_rps", "requests_per_second", "17.5", relation)
	add("edge_error_pct", "percent", "0.5", relation)
	return result, window, nodes, edges
}

func TestParseTopologyObservationsComplete(t *testing.T) {
	result, window, nodes, edges := topologyMetricFixture()
	out, err := parseTopologyObservations(result, window, "app-1", "env-1", nodes, edges, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Nodes) != 2 || len(out.Edges) != 1 || out.Nodes["work"].Status != "ok" ||
		out.Nodes["work"].Ready == nil || *out.Nodes["work"].Ready != 2 ||
		out.Edges["relation-1"].RPS != 17.5 || out.Center.FromID != "work" ||
		out.Center.FromUsed != 8 || len(out.Center.Wait) != 6 || out.Center.Wait[5] != 4.25 {
		t.Fatalf("unexpected topology observation: %+v", out)
	}
}

func TestParseTopologyObservationsRejectsIncompleteOrMismatchedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		change func(*v3integrations.PrometheusMetricEvidence, *connectors.TimeWindow, map[string]node, []edge)
	}{
		{"cross environment series", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series[0].Labels["environment_id"] = "env-2"
		}},
		{"cross environment envelope", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.EnvironmentID = "env-2"
		}},
		{"stale relation revision", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series[len(r.Series)-1].Labels["relation_revision"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
		}},
		{"mismatched source", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series[0].Labels["source_id"] = "cluster-2"
		}},
		{"extra label", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series[0].Labels["unknown"] = "x"
		}},
		{"missing sample", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series[0].Samples = r.Series[0].Samples[:5]
		}},
		{"unaligned sample", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series[0].Samples[3].Timestamp += 1
		}},
		{"missing coverage", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series = r.Series[:len(r.Series)-1]
		}},
		{"duplicate series", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.Series = append(r.Series, r.Series[0])
		}},
		{"invalid integer", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			for i := range r.Series {
				if r.Series[i].Labels["field"] == "node_replicas_ready" {
					r.Series[i].Samples[5].Value = "2.5"
				}
			}
		}},
		{"historical used exceeds max", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			for i := range r.Series {
				if r.Series[i].Labels["field"] == "center_connections_used" {
					r.Series[i].Samples[2].Value = "101"
				}
			}
		}},
		{"caller is not connected", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			for i := range r.Series {
				if r.Series[i].Labels["field"] == "center_from_used" {
					r.Series[i].Labels["caller_id"] = "outsider"
				}
			}
		}},
		{"wrong query", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			r.QueryID = "different"
		}},
		{"stale window", func(r *v3integrations.PrometheusMetricEvidence, w *connectors.TimeWindow, _ map[string]node, _ []edge) {
			w.End = time.Now().Add(-3 * time.Minute)
			w.Start = w.End.Add(-5 * time.Minute)
		}},
		{"too many series", func(r *v3integrations.PrometheusMetricEvidence, _ *connectors.TimeWindow, _ map[string]node, _ []edge) {
			for len(r.Series) <= 99 {
				i := len(r.Series)
				r.Series = append(r.Series, connectors.MetricSeries{Labels: map[string]string{"n": strconv.Itoa(i)}})
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, window, nodes, edges := topologyMetricFixture()
			tc.change(&result, &window, nodes, edges)
			_, err := parseTopologyObservations(result, window, "app-1", "env-1", nodes, edges, "cache")
			if !errors.Is(err, errTopologyIncomplete) {
				t.Fatalf("expected incomplete evidence, got %v", err)
			}
		})
	}
}
