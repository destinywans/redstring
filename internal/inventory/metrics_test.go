package inventory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsKeepsMemoryWhenCPUFailsAndRejectsFutureSamples(t *testing.T) {
	now := time.Now().UTC().Truncate(30 * time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if strings.Contains(query, `job="aiops-vm-nodes"`) {
			t.Error("hardcoded job filter")
		}
		if strings.Contains(query, "node_cpu_seconds_total") {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{map[string]any{"metric": map[string]string{"node": "worker"}, "values": [][]any{{float64(now.Unix()), "37.2"}, {float64(now.Add(95 * 365 * 24 * time.Hour).Unix()), "99.0"}}}}}})
	}))
	defer server.Close()
	snapshot := Snapshot{SourceID: "cluster", SourceKind: "kubernetes", Resources: []Record{{Kind: "node", Name: "worker", Attributes: map[string]any{}}}}
	enrichMetrics(context.Background(), &snapshot, server.URL)
	attrs := snapshot.Resources[0].Attributes
	if attrs["metrics_status"] != "partial" || attrs["memory_percent"] != 37.2 || attrs["cpu_percent"] != nil {
		t.Fatalf("partial sample lost: %#v", attrs)
	}
	if attrs["metrics_cpu_status"] != "error" {
		t.Fatal("CPU failure not preserved")
	}
}
func TestMetricsSupportsExplicitContainerGaugeCapacityAndUnsupportedHosts(t *testing.T) {
	now := time.Now().UTC().Truncate(30 * time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		value := "25"
		if strings.Contains(query, "capacity_cores") {
			value = "4"
		}
		if strings.Contains(query, "capacity_bytes") {
			value = "8589934592"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{map[string]any{"metric": map[string]string{"node": "kind-control", "metric_scope": "container"}, "values": [][]any{{float64(now.Unix()), value}}}}}})
	}))
	defer server.Close()
	snapshot := Snapshot{SourceID: "kind", SourceKind: "kubernetes", Resources: []Record{{Kind: "node", Name: "kind-control", Attributes: map[string]any{}}, {Kind: "host", Name: "unmapped", Attributes: map[string]any{}}}}
	enrichMetrics(context.Background(), &snapshot, server.URL)
	attrs := snapshot.Resources[0].Attributes
	if attrs["metrics_status"] != "current" || attrs["metrics_scope"] != "container" || attrs["metrics_capacity_cpu"] != 4.0 || attrs["metrics_capacity_memory_bytes"] != 8589934592.0 {
		t.Fatalf("container denominator lost: %#v", attrs)
	}
	if snapshot.Resources[1].Attributes["metrics_status"] != "unsupported" {
		t.Fatal("unmapped host treated as missing exporter")
	}
}

func TestMetricsAllowsMappedMiddlewareWithoutInventingUnmappedValues(t *testing.T) {
	now := time.Now().UTC().Truncate(30 * time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{map[string]any{"metric": map[string]string{"node": "container-id"}, "values": [][]any{{float64(now.Unix()), "12"}}}}}})
	}))
	defer server.Close()
	snapshot := Snapshot{SourceID: "local-docker", SourceKind: "docker", Resources: []Record{{Kind: "mysql", Name: "db", Attributes: map[string]any{"metric_cluster": "local-docker", "metric_node": "container-id"}}, {Kind: "redis", Name: "unmapped", Attributes: map[string]any{}}}}
	enrichMetrics(context.Background(), &snapshot, server.URL)
	if snapshot.Resources[0].Attributes["cpu_percent"] != 12.0 || snapshot.Resources[0].Attributes["metrics_status"] != "current" {
		t.Fatal("explicit middleware mapping ignored")
	}
	if snapshot.Resources[1].Attributes["cpu_percent"] != nil {
		t.Fatal("unmapped middleware has invented usage")
	}
}

func TestMetricsToleratesBoundedExporterTimestampLead(t *testing.T) {
	now := time.Now().UTC().Truncate(30 * time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		// Real demo samples reported collection time ~0.6s after scrape/evaluation
		// start. Its series disappeared under the previous strict age >= 0 guard.
		if !strings.Contains(query, "timestamp_seconds") || !strings.Contains(query, ">= -5") || !strings.Contains(query, "<= 90") {
			t.Error("missing bounded exporter clock guard")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{map[string]any{"metric": map[string]string{"node": "aiops-canvas-demo-control-plane", "metric_scope": "container"}, "values": [][]any{{float64(now.Unix()), "4.12"}}}}}})
	}))
	defer server.Close()
	snapshot := Snapshot{SourceID: "aiops-canvas-demo", SourceKind: "kubernetes", Resources: []Record{{Kind: "node", Name: "aiops-canvas-demo-control-plane", Attributes: map[string]any{}}}}
	enrichMetrics(context.Background(), &snapshot, server.URL)
	if snapshot.Resources[0].Attributes["metrics_status"] != "current" {
		t.Fatal("legitimate exporter collection-time lead removed metrics")
	}
	if _, _, ok := metricSample([]any{float64(now.Add(95 * 365 * 24 * time.Hour).Unix()), "4.12"}, now, true); ok {
		t.Fatal("unbounded future sample accepted")
	}
}
