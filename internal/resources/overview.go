package resources

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

// Overview contains only the caller's visible, filtered resources. Counts are
// aggregated before bounded chart/attention output; nil values mean no sample.
type Overview struct {
	ComputeGroups      ComputeGroups     `json:"compute_groups"`
	GeneratedAt        time.Time         `json:"generated_at"`
	WindowSeconds      int               `json:"window_seconds"`
	Stats              OverviewStats     `json:"stats"`
	Distribution       []KindCount       `json:"distribution"`
	WorkloadTypes      []WorkloadCount   `json:"workload_types"`
	Clusters           []ClusterOverview `json:"clusters"`
	Nodes              []NodeOverview    `json:"nodes"`
	NodesTruncated     bool              `json:"nodes_truncated"`
	Sources            []SourceOverview  `json:"sources"`
	Anomalies          []ResourceAnomaly `json:"anomalies"`
	AnomaliesTruncated bool              `json:"anomalies_truncated"`
	Metrics            MetricOverview    `json:"metrics"`
}
type ComputeGroups struct {
	Node ComputeGroup `json:"node"`
	Host ComputeGroup `json:"host"`
}
type ComputeGroup struct {
	Total               int      `json:"total"`
	CapacityCPU         *float64 `json:"capacity_cpu"`
	CapacityMemoryBytes *float64 `json:"capacity_memory_bytes"`
	CPUPercent          *float64 `json:"cpu_percent"`
	MemoryPercent       *float64 `json:"memory_percent"`
	CPUCovered          int      `json:"cpu_covered"`
	MemoryCovered       int      `json:"memory_covered"`
	ContainerScoped     int      `json:"container_scoped"`
}
type OverviewStats struct {
	ResourcesTotal        int `json:"resources_total"`
	ClustersTotal         int `json:"clusters_total"`
	NodesTotal            int `json:"nodes_total"`
	HostsTotal            int `json:"hosts_total"`
	WorkloadsTotal        int `json:"workloads_total"`
	RuntimePodsTotal      int `json:"runtime_pods_total"`
	AnomaliesTotal        int `json:"anomalies_total"`
	StaleResourcesTotal   int `json:"stale_resources_total"`
	MissingResourcesTotal int `json:"missing_resources_total"`
}
type KindCount struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Count int    `json:"count"`
}
type WorkloadCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type ClusterOverview struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	SourceID        string     `json:"source_id"`
	NodesTotal      int        `json:"nodes_total"`
	NodesReady      int        `json:"nodes_ready"`
	NodesUnknown    int        `json:"nodes_unknown"`
	WorkloadsTotal  int        `json:"workloads_total"`
	DesiredReplicas int        `json:"desired_replicas"`
	ReadyReplicas   int        `json:"ready_replicas"`
	SyncStatus      string     `json:"sync_status"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
}
type MetricPoint struct {
	CPUSamples    *int      `json:"cpu_samples,omitempty"`
	MemorySamples *int      `json:"memory_samples,omitempty"`
	Time          time.Time `json:"time"`
	CPU           *float64  `json:"cpu"`
	Memory        *float64  `json:"memory"`
}
type NodeOverview struct {
	MetricsScope        string        `json:"metrics_scope"`
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Kind                string        `json:"kind"`
	SourceID            string        `json:"source_id"`
	MetricsStatus       string        `json:"metrics_status"`
	CPUPercent          *float64      `json:"cpu_percent"`
	MemoryPercent       *float64      `json:"memory_percent"`
	History             []MetricPoint `json:"history"`
	CapacityCPU         *float64      `json:"capacity_cpu"`
	CapacityMemoryBytes *float64      `json:"capacity_memory_bytes"`
}
type SourceOverview struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	SyncStatus      string     `json:"sync_status"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	ResourcesCount  int        `json:"resources_count"`
	MetricsEligible int        `json:"metrics_eligible"`
	MetricsCurrent  int        `json:"metrics_current"`
}
type ResourceAnomaly struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Status string `json:"status"`
}
type MetricOverview struct {
	Eligible      int           `json:"eligible"`
	Current       int           `json:"current"`
	Partial       int           `json:"partial"`
	Unavailable   int           `json:"unavailable"`
	Unsupported   int           `json:"unsupported"`
	Stale         int           `json:"stale"`
	CPUPercent    *float64      `json:"cpu_percent"`
	MemoryPercent *float64      `json:"memory_percent"`
	History       []MetricPoint `json:"history"`
}

func (s *Service) registerOverview(api *httpapi.Server, load func(http.ResponseWriter, *http.Request) ([]Resource, bool)) {
	api.Register("GET /api/v1/resources/overview", "viewer", func(w http.ResponseWriter, r *http.Request) {
		rows, ok := load(w, r)
		if !ok {
			return
		}
		httpapi.WriteJSON(w, 200, buildOverview(rows, r.URL.Query(), time.Now().UTC()))
	})
}
func textAttribute(r Resource, key string) string { v, _ := r.Attributes[key].(string); return v }
func numeric(v any) *float64 {
	var value float64
	switch x := v.(type) {
	case float64:
		value = x
	case int:
		value = float64(x)
	case int64:
		value = float64(x)
	default:
		return nil
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func percentage(v any) *float64 {
	n := numeric(v)
	if n == nil || *n < 0 || *n > 100 {
		return nil
	}
	return n
}
func timeAttribute(r Resource, key string) *time.Time {
	value, err := time.Parse(time.RFC3339Nano, textAttribute(r, key))
	if err != nil {
		return nil
	}
	return &value
}
func resourceSync(r Resource, now time.Time) string {
	status := textAttribute(r, "sync_status")
	if status == "disconnected" {
		return status
	}
	observed := timeAttribute(r, "sync_last_success_at")
	if observed == nil {
		observed = r.ObservedAt
	}
	if observed == nil {
		return "unknown"
	}
	if now.Sub(*observed) > 3*time.Minute || observed.After(now.Add(time.Minute)) {
		return "stale"
	}
	if status == "stale" {
		return status
	}
	return "current"
}
func syncRank(value string) int {
	switch value {
	case "disconnected":
		return 4
	case "stale":
		return 3
	case "unknown":
		return 2
	default:
		return 1
	}
}
func freshMetric(r Resource, now time.Time) string {
	status := textAttribute(r, "metrics_status")
	if status == "" {
		return "unsupported"
	}
	sampled := timeAttribute(r, "metrics_observed_at")
	if sampled == nil {
		sampled = timeAttribute(r, "metrics_queried_at")
	}
	if (status == "current" || status == "partial") && (sampled == nil || now.Sub(*sampled) > 90*time.Second || sampled.After(now.Add(time.Minute))) {
		return "stale"
	}
	switch status {
	case "current", "partial", "unavailable", "unsupported", "stale":
		return status
	default:
		return "unavailable"
	}
}
func metricIdentity(r Resource) string {
	if r.Kind == "node" {
		return r.SourceID + "\x00" + r.Name
	}
	cluster, node := textAttribute(r, "metric_cluster"), textAttribute(r, "metric_node")
	if cluster != "" && node != "" {
		return cluster + "\x00" + node
	}
	return r.ID
}
func quantity(raw any, _ bool) *float64 {
	if n := numeric(raw); n != nil {
		return n
	}
	value, ok := raw.(string)
	if !ok || value == "" {
		return nil
	}
	multipliers := []struct {
		suffix     string
		multiplier float64
	}{{"Ei", math.Pow(1024, 6)}, {"Pi", math.Pow(1024, 5)}, {"Ti", math.Pow(1024, 4)}, {"Gi", math.Pow(1024, 3)}, {"Mi", math.Pow(1024, 2)}, {"Ki", 1024}, {"E", 1e18}, {"P", 1e15}, {"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"k", 1e3}, {"m", .001}, {"u", 1e-6}, {"n", 1e-9}}
	multiplier := 1.0
	for _, entry := range multipliers {
		if strings.HasSuffix(value, entry.suffix) {
			multiplier = entry.multiplier
			value = strings.TrimSuffix(value, entry.suffix)
			break
		}
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return nil
	}
	n *= multiplier
	return &n
}
func buckets(now time.Time) []MetricPoint {
	out := make([]MetricPoint, 31)
	end := now.Truncate(30 * time.Second)
	for i := range out {
		out[i].Time = end.Add(time.Duration(i-30) * 30 * time.Second)
	}
	return out
}
func nodeOverview(r Resource, now time.Time) NodeOverview {
	node := NodeOverview{ID: r.ID, Name: r.Name, Kind: r.Kind, SourceID: r.SourceID, MetricsStatus: freshMetric(r, now), MetricsScope: textAttribute(r, "metrics_scope"), History: buckets(now)}
	capacity, _ := r.Attributes["capacity"].(map[string]any)
	node.CapacityCPU = quantity(capacity["cpu"], false)
	node.CapacityMemoryBytes = quantity(capacity["memory"], true)
	if node.CapacityCPU == nil {
		node.CapacityCPU = quantity(r.Attributes["cpu_count"], false)
	}
	if node.CapacityMemoryBytes == nil {
		node.CapacityMemoryBytes = quantity(r.Attributes["memory_bytes"], true)
	}
	if node.MetricsScope == "container" {
		node.CapacityCPU = quantity(r.Attributes["metrics_capacity_cpu"], false)
		node.CapacityMemoryBytes = quantity(r.Attributes["metrics_capacity_memory_bytes"], true)
	}
	if node.MetricsStatus == "current" || node.MetricsStatus == "partial" {
		node.CPUPercent = percentage(r.Attributes["cpu_percent"])
		node.MemoryPercent = percentage(r.Attributes["memory_percent"])
		if node.CPUPercent == nil || node.MemoryPercent == nil {
			node.MetricsStatus = "partial"
		}
		if node.CPUPercent == nil && node.MemoryPercent == nil {
			node.MetricsStatus = "unavailable"
		}
	}
	for _, value := range sliceAny(r.Attributes["history"]) {
		point, ok := value.(map[string]any)
		if !ok {
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, fmt.Sprint(point["time"]))
		if err != nil {
			continue
		}
		index := int(stamp.Truncate(30*time.Second).Sub(node.History[0].Time) / (30 * time.Second))
		if stamp.Before(node.History[0].Time) || index < 0 || index >= len(node.History) {
			continue
		}
		node.History[index].CPU = percentage(point["cpu"])
		node.History[index].Memory = percentage(point["memory"])
	}
	return node
}
func sliceAny(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	if values, ok := value.([]map[string]any); ok {
		out := make([]any, len(values))
		for i, v := range values {
			out[i] = v
		}
		return out
	}
	return nil
}
func runtimeAnomaly(r Resource, now time.Time) (string, bool) {
	if resourceSync(r, now) != "current" {
		return "", false
	}
	status := textAttribute(r, "status")
	lower := strings.ToLower(status)
	if r.Kind == "event" {
		return "", false
	} // Historical warning events are not current failures.
	for _, needle := range []string{"crashloop", "imagepullbackoff", "errimagepull", "failed", "notready", "unhealthy", "error", "异常"} {
		if strings.Contains(lower, needle) {
			return "运行状态需要关注", true
		}
	}
	if r.Kind == "pod" && lower == "pending" {
		return "Pod 尚未调度或就绪", true
	}
	if r.Kind == "deployment" && lower == "progressing" {
		return "控制器尚未达到目标副本", true
	}
	return "", false
}
func inventoryKind(kind string) bool {
	return kind != "pod" && kind != "event" && kind != "application"
}
func replicaBearing(r Resource) bool {
	switch textAttribute(r, "workload_type") {
	case "Deployment", "StatefulSet", "DaemonSet":
		return true
	case "ReplicaSet":
		return textAttribute(r, "owner_controller_kind") == ""
	default:
		return false
	}
}
func buildOverview(input []Resource, q url.Values, now time.Time) Overview {
	out := Overview{GeneratedAt: now, WindowSeconds: 900, Distribution: []KindCount{}, WorkloadTypes: []WorkloadCount{}, Clusters: []ClusterOverview{}, Nodes: []NodeOverview{}, Sources: []SourceOverview{}, Anomalies: []ResourceAnomaly{}, Metrics: MetricOverview{History: buckets(now)}}
	rows := []Resource{}
	distribution := map[string]int{}
	workloads := map[string]int{}
	sources := map[string]*SourceOverview{}
	clusters := map[string]*ClusterOverview{}
	for _, r := range input {
		if !matches(r, q, "kind") {
			continue
		}
		if textAttribute(r, "inventory_presence") == "missing" {
			out.Stats.MissingResourcesTotal++
			continue
		}
		rows = append(rows, r)
	}
	nodeKeys := map[string]bool{}
	for _, r := range rows {
		if r.Kind == "node" {
			nodeKeys[metricIdentity(r)] = true
		}
	}
	distinct := rows[:0]
	for _, r := range rows {
		if r.Kind == "host" && nodeKeys[metricIdentity(r)] {
			continue
		}
		distinct = append(distinct, r)
	}
	rows = distinct
	for _, r := range rows {
		syncStatus := resourceSync(r, now)
		sourceKey := r.SourceKind + "\x00" + r.SourceID
		source := sources[sourceKey]
		if source == nil {
			name := textAttribute(r, "sync_source_name")
			if name == "" {
				name = r.SourceID
			}
			if name == "" {
				name = r.SourceKind
			}
			source = &SourceOverview{ID: r.SourceID, Kind: r.SourceKind, Name: name, SyncStatus: syncStatus, LastSuccessAt: timeAttribute(r, "sync_last_success_at")}
			sources[sourceKey] = source
		} else if syncRank(syncStatus) > syncRank(source.SyncStatus) {
			source.SyncStatus = syncStatus
		}
		if inventoryKind(r.Kind) {
			out.Stats.ResourcesTotal++
			distribution[r.Kind]++
			source.ResourcesCount++
			if syncStatus != "current" {
				out.Stats.StaleResourcesTotal++
			}
		}
		switch r.Kind {
		case "cluster":
			out.Stats.ClustersTotal++
			clusters[r.SourceID] = &ClusterOverview{ID: r.ID, Name: r.Name, SourceID: r.SourceID, SyncStatus: syncStatus, LastSuccessAt: timeAttribute(r, "sync_last_success_at")}
		case "deployment":
			out.Stats.WorkloadsTotal++
			typ := textAttribute(r, "workload_type")
			if typ == "" {
				typ = "Unknown"
			}
			workloads[typ]++
		case "pod":
			out.Stats.RuntimePodsTotal++
		}
		if reason, yes := runtimeAnomaly(r, now); yes {
			out.Anomalies = append(out.Anomalies, ResourceAnomaly{r.ID, r.Name, r.Kind, reason, textAttribute(r, "status")})
		}
	}
	// Cluster figures use only visible children, never unfiltered source totals.
	for _, r := range rows {
		cluster := clusters[r.SourceID]
		if cluster == nil {
			continue
		}
		switch r.Kind {
		case "node":
			cluster.NodesTotal++
			if resourceSync(r, now) != "current" || textAttribute(r, "status") == "Unknown" {
				cluster.NodesUnknown++
			} else if textAttribute(r, "status") == "Ready" {
				cluster.NodesReady++
			}
		case "deployment":
			cluster.WorkloadsTotal++
			if replicaBearing(r) && resourceSync(r, now) == "current" {
				if value := numeric(r.Attributes["replicas"]); value != nil {
					cluster.DesiredReplicas += int(*value)
				}
				if value := numeric(r.Attributes["ready_replicas"]); value != nil {
					cluster.ReadyReplicas += int(*value)
				}
			}
		}
	}
	// Compute identities, preferring a visible Kubernetes Node to the VM alias.
	compute := map[string]Resource{}
	for _, r := range rows {
		if r.Kind != "node" && r.Kind != "host" {
			continue
		}
		key := metricIdentity(r)
		prior, exists := compute[key]
		if !exists || r.Kind == "node" && prior.Kind != "node" {
			compute[key] = r
		}
	}
	allNodes := []NodeOverview{}
	for _, r := range compute {
		node := nodeOverview(r, now)
		allNodes = append(allNodes, node)
		if r.Kind == "node" {
			out.Stats.NodesTotal++
		} else {
			out.Stats.HostsTotal++
		}

	}
	// Coverage includes every explicitly mapped resource (including middleware
	// containers), independently of the compute-only heatmap and Node weighting.
	for _, r := range rows {
		if r.Kind != "node" && r.Kind != "host" && textAttribute(r, "metric_cluster") == "" {
			continue
		}
		source := sources[r.SourceKind+"\x00"+r.SourceID]
		status := freshMetric(r, now)
		if status == "unsupported" {
			out.Metrics.Unsupported++
			continue
		}
		out.Metrics.Eligible++
		source.MetricsEligible++
		switch status {
		case "current":
			out.Metrics.Current++
			source.MetricsCurrent++
		case "partial":
			out.Metrics.Partial++
		case "stale":
			out.Metrics.Stale++
		default:
			out.Metrics.Unavailable++
		}
	}

	for kind, count := range distribution {
		label := kinds[kind]
		if label == "" {
			label = kind
		}
		out.Distribution = append(out.Distribution, KindCount{kind, label, count})
	}
	sort.Slice(out.Distribution, func(i, j int) bool { return out.Distribution[i].Kind < out.Distribution[j].Kind })
	for name, count := range workloads {
		out.WorkloadTypes = append(out.WorkloadTypes, WorkloadCount{name, count})
	}
	sort.Slice(out.WorkloadTypes, func(i, j int) bool { return out.WorkloadTypes[i].Name < out.WorkloadTypes[j].Name })
	for _, cluster := range clusters {
		out.Clusters = append(out.Clusters, *cluster)
	}
	sort.Slice(out.Clusters, func(i, j int) bool { return out.Clusters[i].Name < out.Clusters[j].Name })
	for _, source := range sources {
		out.Sources = append(out.Sources, *source)
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	sort.Slice(allNodes, func(i, j int) bool {
		a, b := allNodes[i], allNodes[j]
		if a.Kind != b.Kind {
			return a.Kind == "node"
		}
		if (a.MetricsStatus == "current") != (b.MetricsStatus == "current") {
			return a.MetricsStatus == "current"
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	var expectedCPU, expectedMemory int
	out.Metrics.CPUPercent, expectedCPU = weightedSamples(allNodes, func(n NodeOverview) *float64 { return n.CPUPercent }, false)
	out.Metrics.MemoryPercent, expectedMemory = weightedSamples(allNodes, func(n NodeOverview) *float64 { return n.MemoryPercent }, true)
	for i := range out.Metrics.History {
		point := &out.Metrics.History[i]
		cpu, cpuSamples := weightedSamples(allNodes, func(n NodeOverview) *float64 { return n.History[i].CPU }, false)
		memory, memorySamples := weightedSamples(allNodes, func(n NodeOverview) *float64 { return n.History[i].Memory }, true)
		point.CPU = cpu
		point.Memory = memory
		point.CPUSamples = &cpuSamples
		point.MemorySamples = &memorySamples
		// Independently polled sources can reach the current bucket at different
		// times. Do not render a changing subset as a fleet usage spike.
		if i == len(out.Metrics.History)-1 {
			if cpuSamples < expectedCPU {
				point.CPU = nil
			}
			if memorySamples < expectedMemory {
				point.Memory = nil
			}
		}
	}
	out.ComputeGroups.Node = computeGroup(allNodes, "node")
	out.ComputeGroups.Host = computeGroup(allNodes, "host")
	out.Nodes = allNodes
	if len(out.Nodes) > 200 {
		out.Nodes = out.Nodes[:200]
		out.NodesTruncated = true
	}
	sort.Slice(out.Anomalies, func(i, j int) bool { return out.Anomalies[i].ID < out.Anomalies[j].ID })
	out.Stats.AnomaliesTotal = len(out.Anomalies)
	if len(out.Anomalies) > 50 {
		out.Anomalies = out.Anomalies[:50]
		out.AnomaliesTruncated = true
	}
	return out
}
func weightedSamples(nodes []NodeOverview, value func(NodeOverview) *float64, memory bool) (*float64, int) {
	samples := 0
	sum, total := 0.0, 0.0
	for _, node := range nodes {
		if node.Kind != "node" {
			continue
		}
		sample := value(node)
		capacity := node.CapacityCPU
		if memory {
			capacity = node.CapacityMemoryBytes
		}
		if sample == nil || capacity == nil || *capacity <= 0 {
			continue
		}
		samples++
		sum += *sample * *capacity
		total += *capacity
	}
	if total == 0 {
		return nil, samples
	}
	result := math.Round(sum/total*100) / 100
	return &result, samples
}

func computeGroup(nodes []NodeOverview, kind string) ComputeGroup {
	out := ComputeGroup{}
	cpuTotal, memoryTotal, cpuUsed, memoryUsed, cpuWeight, memoryWeight := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
	cpuKnown, memoryKnown := true, true
	for _, node := range nodes {
		if node.Kind != kind {
			continue
		}
		out.Total++
		if node.MetricsScope == "container" {
			out.ContainerScoped++
		}
		if node.CapacityCPU == nil || *node.CapacityCPU <= 0 {
			cpuKnown = false
		} else {
			cpuTotal += *node.CapacityCPU
			if node.CPUPercent != nil {
				cpuUsed += *node.CPUPercent * *node.CapacityCPU
				cpuWeight += *node.CapacityCPU
				out.CPUCovered++
			}
		}
		if node.CapacityMemoryBytes == nil || *node.CapacityMemoryBytes <= 0 {
			memoryKnown = false
		} else {
			memoryTotal += *node.CapacityMemoryBytes
			if node.MemoryPercent != nil {
				memoryUsed += *node.MemoryPercent * *node.CapacityMemoryBytes
				memoryWeight += *node.CapacityMemoryBytes
				out.MemoryCovered++
			}
		}
	}
	if out.Total > 0 && cpuKnown {
		out.CapacityCPU = &cpuTotal
	}
	if out.Total > 0 && memoryKnown {
		out.CapacityMemoryBytes = &memoryTotal
	}
	if cpuWeight > 0 {
		value := math.Round(cpuUsed/cpuWeight*100) / 100
		out.CPUPercent = &value
	}
	if memoryWeight > 0 {
		value := math.Round(memoryUsed/memoryWeight*100) / 100
		out.MemoryPercent = &value
	}
	return out
}
