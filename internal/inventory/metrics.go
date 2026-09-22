package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Standard node-exporter samples and explicitly labelled container-node gauges
// are independent inputs. Runtime readiness, source availability and metric
// coverage are never inferred from one another.
func enrichMetrics(ctx context.Context, snapshot *Snapshot, prometheus string) {
	groups := map[string][]int{}
	for i := range snapshot.Resources {
		r := &snapshot.Resources[i]
		if r.Kind != "node" && r.Kind != "host" && str(r.Attributes["metric_cluster"]) == "" {
			continue
		}
		for _, key := range []string{"cpu_percent", "memory_percent", "history", "metrics_observed_at", "metrics_scope", "metrics_cpu_status", "metrics_memory_status", "metrics_capacity_cpu", "metrics_capacity_memory_bytes"} {
			delete(r.Attributes, key)
		}
		cluster := str(r.Attributes["metric_cluster"])
		if r.Kind == "node" && snapshot.SourceKind == "kubernetes" {
			cluster = snapshot.SourceID
			r.Attributes["metric_cluster"] = cluster
			r.Attributes["metric_node"] = r.Name
		}
		r.Attributes["metrics_status"] = "unsupported"
		if cluster != "" && str(r.Attributes["metric_node"]) != "" {
			r.Attributes["metrics_status"] = "unavailable"
			groups[cluster] = append(groups[cluster], i)
		}
	}
	if prometheus == "" {
		return
	}
	for cluster, indices := range groups {
		now := time.Now().UTC()
		selector := fmt.Sprintf(`{cluster=%s}`, strconv.Quote(cluster))
		idle := fmt.Sprintf(`node_cpu_seconds_total{cluster=%s,mode="idle"}`, strconv.Quote(cluster))
		// Gate each actual exporter series by its own target and underlying timestamp.
		valid := func(metric string) string {
			return "(" + metric + " and on(job,instance) (up" + selector + " == 1) and (time()-timestamp(" + metric + ") >= 0) and (time()-timestamp(" + metric + ") <= 90))"
		}
		cpuRaw := `rate(` + idle + `[2m]) and on(job,instance,cpu,mode) ` + valid(idle)
		cpu := `100 * (1 - avg by(node,metric_scope) (` + cpuRaw + `))`
		memory := `100 * (1 - max by(node,metric_scope) (` + valid("node_memory_MemAvailable_bytes"+selector) + `) / max by(node,metric_scope) (` + valid("node_memory_MemTotal_bytes"+selector) + `))`
		// Prometheus timestamps a scrape near request start. A gauge reporting
		// its actual collection time can be fractions of a second later than
		// the range evaluation bucket. Permit only a bounded five-second lead;
		// retain the 90-second age ceiling and reject materially future clocks.
		gaugeGuard := ` and on(node) (max by(node) (aiops_node_metrics_up` + selector + `) == 1) and on(node) ((time()-max by(node) (aiops_node_metrics_timestamp_seconds` + selector + `)) >= -5) and on(node) ((time()-max by(node) (aiops_node_metrics_timestamp_seconds` + selector + `)) <= 90)`
		fallback := func(metric string) string {
			return `(100 * max by(node,metric_scope) (` + metric + selector + `))` + gaugeGuard
		}
		queries := map[string]string{"cpu": "(" + cpu + ") or on(node) (" + fallback("aiops_node_cpu_usage_ratio") + ")", "memory": "(" + memory + ") or on(node) (" + fallback("aiops_node_memory_usage_ratio") + ")"}
		histories := map[string]map[int64]map[string]any{}
		scopes := map[string]string{}
		failed := map[string]bool{}
		for _, metric := range []string{"cpu", "memory"} {
			series, err := rangeQuery(ctx, prometheus, queries[metric])
			if err != nil {
				failed[metric] = true
				continue
			}
			for _, line := range series {
				node := line.Metric["node"]
				if node == "" {
					continue
				}
				if histories[node] == nil {
					histories[node] = map[int64]map[string]any{}
				}
				if scope := line.Metric["metric_scope"]; scope != "" {
					scopes[node] = scope
				}
				for _, sample := range line.Values {
					stamp, value, ok := metricSample(sample, now, true)
					if !ok {
						continue
					}
					second := stamp.Unix()
					if histories[node][second] == nil {
						histories[node][second] = map[string]any{"time": stamp.Format(time.RFC3339)}
					}
					histories[node][second][metric] = math.Round(value*100) / 100
				}
			}
		}
		// Only container-scoped fallbacks need a different capacity denominator from
		// Kubernetes node.capacity. Missing gauges leave capacity unknown.
		capacities := map[string]map[string]float64{}
		if len(scopes) > 0 {
			for key, metric := range map[string]string{"metrics_capacity_cpu": "aiops_node_cpu_capacity_cores", "metrics_capacity_memory_bytes": "aiops_node_memory_capacity_bytes"} {
				series, err := rangeQuery(ctx, prometheus, `max by(node) (`+metric+selector+`)`+gaugeGuard)
				if err != nil {
					continue
				}
				for _, line := range series {
					for _, sample := range line.Values {
						stamp, value, ok := metricSample(sample, now, false)
						if !ok || now.Sub(stamp) > 90*time.Second || value <= 0 {
							continue
						}
						node := line.Metric["node"]
						if capacities[node] == nil {
							capacities[node] = map[string]float64{}
						}
						capacities[node][key] = value
					}
				}
			}
		}
		for _, i := range indices {
			r := &snapshot.Resources[i]
			node := str(r.Attributes["metric_node"])
			r.Attributes["metrics_queried_at"] = now.Format(time.RFC3339)
			r.Attributes["metrics_scope"] = "node"
			if scopes[node] != "" {
				r.Attributes["metrics_scope"] = scopes[node]
			}
			for key, value := range capacities[node] {
				r.Attributes[key] = value
			}
			ordered := []map[string]any{}
			for _, point := range histories[node] {
				ordered = append(ordered, point)
			}
			sortSamples(ordered)
			r.Attributes["history"] = ordered
			current := 0
			stale := false
			var observed time.Time
			for _, metric := range []string{"cpu", "memory"} {
				state := "unavailable"
				if failed[metric] {
					state = "error"
				}
				for j := len(ordered) - 1; j >= 0; j-- {
					value, exists := ordered[j][metric]
					if !exists {
						continue
					}
					stamp, _ := time.Parse(time.RFC3339, str(ordered[j]["time"]))
					if now.Sub(stamp) > 90*time.Second {
						state = "stale"
						stale = true
						break
					}
					r.Attributes[metric+"_percent"] = value
					state = "current"
					current++
					if stamp.After(observed) {
						observed = stamp
					}
					break
				}
				r.Attributes["metrics_"+metric+"_status"] = state
			}
			switch {
			case current == 2:
				r.Attributes["metrics_status"] = "current"
			case current == 1:
				r.Attributes["metrics_status"] = "partial"
			case stale:
				r.Attributes["metrics_status"] = "stale"
			default:
				r.Attributes["metrics_status"] = "unavailable"
			}
			if !observed.IsZero() {
				r.Attributes["metrics_observed_at"] = observed.Format(time.RFC3339)
			}
		}
	}
}
func metricSample(sample []any, now time.Time, percentage bool) (time.Time, float64, bool) {
	if len(sample) != 2 {
		return time.Time{}, 0, false
	}
	seconds, ok := sample[0].(float64)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}, 0, false
	}
	stamp := time.Unix(int64(seconds), 0).UTC()
	value, err := strconv.ParseFloat(str(sample[1]), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || stamp.After(now.Add(30*time.Second)) || stamp.Before(now.Add(-16*time.Minute)) {
		return time.Time{}, 0, false
	}
	if percentage && (value < 0 || value > 100) {
		return time.Time{}, 0, false
	}
	return stamp, value, true
}

type metricSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][]any           `json:"values"`
}

func rangeQuery(ctx context.Context, base, query string) ([]metricSeries, error) {
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("invalid metrics endpoint")
	}
	now := time.Now().UTC().Truncate(30 * time.Second)
	params := url.Values{"query": {query}, "start": {strconv.FormatInt(now.Add(-15*time.Minute).Unix(), 10)}, "end": {strconv.FormatInt(now.Unix(), 10)}, "step": {"30"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v1/query_range?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metrics unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("metrics HTTP failure")
	}
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []metricSeries `json:"result"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil || result.Status != "success" {
		return nil, fmt.Errorf("invalid metrics response")
	}
	return result.Data.Result, nil
}
func sortSamples(values []map[string]any) {
	sort.Slice(values, func(i, j int) bool { return str(values[i]["time"]) < str(values[j]["time"]) })
}
