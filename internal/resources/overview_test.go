package resources

import (
	"fmt"
	"net/url"
	"testing"
	"time"
)

func overviewFixture(id, kind, source string, now time.Time) Resource {
	return Resource{ID: id, Kind: kind, Name: id, SourceKind: "kubernetes", SourceID: source, ObservedAt: &now, Attributes: map[string]any{"status": "Ready", "sync_status": "current", "sync_last_success_at": now.Format(time.RFC3339), "metrics_status": "current", "metrics_observed_at": now.Format(time.RFC3339), "cpu_percent": 20.0, "memory_percent": 40.0, "capacity": map[string]any{"cpu": "2", "memory": "3Gi"}}}
}
func TestOverviewUsesOnlyVisibleFilteredRows(t *testing.T) {
	now := time.Now().UTC()
	cluster := overviewFixture("cluster", "cluster", "s", now)
	cluster.Attributes["nodes_count"] = 9999
	visible := overviewFixture("node", "node", "s", now)
	visible.Applications = []ApplicationLink{{ID: "a"}}
	other := overviewFixture("other", "node", "other", now)
	other.Applications = []ApplicationLink{{ID: "b"}}
	mock := overviewFixture("mock", "node", "s", now)
	mock.IsMock = true
	mock.SourceKind = "mock"
	got := buildOverview([]Resource{cluster, visible, other, mock}, url.Values{"application_id": {"a"}}, now)
	if got.Stats.NodesTotal != 1 || len(got.Sources) != 1 || got.Sources[0].ID != "s" || len(got.Clusters) != 0 {
		t.Fatalf("scope leaked: %+v", got.Stats)
	}
	got = buildOverview([]Resource{cluster, visible}, url.Values{}, now)
	if got.Clusters[0].NodesTotal != 1 {
		t.Fatal("unfiltered source node count leaked")
	}
	if len(got.Metrics.History) != 31 || got.Metrics.History[0].CPU != nil {
		t.Fatal("missing samples must be null")
	}
}
func TestOverviewDeduplicatesVMAndExcludesChildReplicas(t *testing.T) {
	now := time.Now().UTC()
	cluster := overviewFixture("cluster", "cluster", "s", now)
	node := overviewFixture("node", "node", "s", now)
	host := overviewFixture("vm", "host", "lima", now)
	host.SourceKind = "lima"
	host.Attributes["metric_cluster"] = "s"
	host.Attributes["metric_node"] = "node"
	host.Attributes["cpu_percent"] = 100.0
	workload := overviewFixture("api", "deployment", "s", now)
	workload.Attributes["workload_type"] = "Deployment"
	workload.Attributes["replicas"] = 3.0
	workload.Attributes["ready_replicas"] = 2.0
	child := overviewFixture("rs", "deployment", "s", now)
	child.Attributes["workload_type"] = "ReplicaSet"
	child.Attributes["owner_controller_kind"] = "Deployment"
	child.Attributes["replicas"] = 3.0
	child.Attributes["ready_replicas"] = 2.0
	job := overviewFixture("job", "deployment", "s", now)
	job.Attributes["workload_type"] = "Job"
	job.Attributes["replicas"] = 1.0
	got := buildOverview([]Resource{cluster, node, host, workload, child, job}, url.Values{}, now)
	if got.Stats.HostsTotal != 0 || len(got.Nodes) != 1 || got.Metrics.CPUPercent == nil || *got.Metrics.CPUPercent != 20 {
		t.Fatal("VM alias double counted")
	}
	if got.Clusters[0].DesiredReplicas != 3 || got.Clusters[0].ReadyReplicas != 2 {
		t.Fatal("child or Job replicas double counted")
	}
}
func TestOverviewSeparatesStaleSourceAndMetricsFromRuntimeAnomalies(t *testing.T) {
	now := time.Now().UTC()
	r := overviewFixture("node", "node", "s", now)
	r.Attributes["sync_status"] = "disconnected"
	r.Attributes["status"] = "NotReady"
	r.Attributes["metrics_observed_at"] = now.Add(95 * 365 * 24 * time.Hour).Format(time.RFC3339)
	got := buildOverview([]Resource{r}, url.Values{}, now)
	if got.Stats.AnomaliesTotal != 0 || got.Stats.StaleResourcesTotal != 1 || got.Metrics.Stale != 1 || got.Nodes[0].CPUPercent != nil {
		t.Fatal("stale/future state was treated as fresh or runtime failure")
	}
}

func TestOverviewMiddlewareCoverageDoesNotEnterNodeCPU(t *testing.T) {
	now := time.Now().UTC()
	database := overviewFixture("db", "mysql", "local-docker", now)
	database.SourceKind = "docker"
	database.Attributes["metric_cluster"] = "local-docker"
	database.Attributes["metric_node"] = "container-id"
	got := buildOverview([]Resource{database}, url.Values{}, now)
	if got.Metrics.Eligible != 1 || got.Metrics.Current != 1 || got.Sources[0].MetricsCurrent != 1 {
		t.Fatal("mapped middleware coverage missing")
	}
	if len(got.Nodes) != 0 || got.Metrics.CPUPercent != nil {
		t.Fatal("middleware container was counted as Kubernetes Node")
	}
}

func TestOverviewGroupsAggregateBeforeTruncationAndRespectUnknownCapacity(t *testing.T) {
	now := time.Now().UTC()
	rows := []Resource{}
	for i := 0; i < 205; i++ {
		r := overviewFixture(fmt.Sprintf("node-%03d", i), "node", "s", now)
		rows = append(rows, r)
	}
	host := overviewFixture("host", "host", "independent", now)
	host.Attributes["capacity"] = map[string]any{}
	rows = append(rows, host)
	got := buildOverview(rows, url.Values{}, now)
	if len(got.Nodes) != 200 || !got.NodesTruncated || got.ComputeGroups.Node.Total != 205 || got.ComputeGroups.Node.CapacityCPU == nil || *got.ComputeGroups.Node.CapacityCPU != 410 || got.ComputeGroups.Node.CPUCovered != 205 {
		t.Fatal("group totals were based on truncated chart")
	}
	if got.ComputeGroups.Host.Total != 1 || got.ComputeGroups.Host.CapacityCPU != nil || got.ComputeGroups.Host.CPUPercent != nil {
		t.Fatal("unknown host capacity invented or mixed with Node")
	}
}

func TestOverviewLatestBucketWaitsForCurrentMetricCoverage(t *testing.T) {
	now := time.Now().UTC().Truncate(30 * time.Second)
	first := overviewFixture("first", "node", "a", now)
	second := overviewFixture("second", "node", "b", now)
	first.Attributes["history"] = []map[string]any{{"time": now.Add(-30 * time.Second).Format(time.RFC3339), "cpu": 20.0, "memory": 30.0}, {"time": now.Format(time.RFC3339), "cpu": 20.0, "memory": 87.0}}
	second.Attributes["history"] = []map[string]any{{"time": now.Add(-30 * time.Second).Format(time.RFC3339), "cpu": 40.0, "memory": 50.0}, {"time": now.Format(time.RFC3339), "cpu": 40.0}}
	got := buildOverview([]Resource{first, second}, url.Values{}, now)
	latest := got.Metrics.History[30]
	previous := got.Metrics.History[29]
	if latest.CPU == nil || *latest.CPU != 30 || latest.Memory != nil || latest.CPUSamples == nil || *latest.CPUSamples != 2 || latest.MemorySamples == nil || *latest.MemorySamples != 1 {
		t.Fatal("incomplete latest bucket rendered or independent CPU lost")
	}
	if previous.Memory == nil || *previous.Memory != 40 || previous.MemorySamples == nil || *previous.MemorySamples != 2 {
		t.Fatal("complete historical weighted samples changed")
	}
	second.Attributes["history"].([]map[string]any)[1]["memory"] = 33.0
	got = buildOverview([]Resource{first, second}, url.Values{}, now)
	if got.Metrics.History[30].Memory == nil || *got.Metrics.History[30].Memory != 60 {
		t.Fatal("complete latest bucket did not become visible")
	}
}
