package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Metric enrichment is optional and never determines Kubernetes readiness.
// Missing or failed Prometheus reads omit values rather than inventing zero.
func enrichMetrics(ctx context.Context, snapshot *Snapshot, prometheus string) {
	if prometheus == "" {
		return
	}
	groups := map[string][]int{}
	for i, r := range snapshot.Resources {
		cluster := ""
		if r.Kind == "node" && snapshot.SourceKind == "kubernetes" {
			cluster = snapshot.SourceID
			r.Attributes["metric_node"] = r.Name
		}
		if snapshot.SourceKind == "lima" && r.Kind == "host" {
			cluster = str(r.Attributes["metric_cluster"])
		}
		if cluster != "" {
			groups[cluster] = append(groups[cluster], i)
		}
	}
	for cluster, indices := range groups {
		selector := fmt.Sprintf(`{cluster=%s,job="aiops-vm-nodes"}`, strconv.Quote(cluster))
		cpu := fmt.Sprintf(`100 * (1 - avg by(node) (rate(node_cpu_seconds_total{cluster=%s,job="aiops-vm-nodes",mode="idle"}[2m])))`, strconv.Quote(cluster))
		memory := `100 * (1 - max by(node) (node_memory_MemAvailable_bytes` + selector + `) / max by(node) (node_memory_MemTotal_bytes` + selector + `))`
		fresh := ` and on(node) (max by(node) (up` + selector + `)==1) and on(node) (max by(node) (time()-timestamp(node_memory_MemAvailable_bytes` + selector + `)) < 90)`
		cpu = "(" + cpu + ")" + fresh
		memory = "(" + memory + ")" + fresh
		histories := map[string]map[int64]map[string]any{}
		failed := false
		for metric, query := range map[string]string{"cpu": cpu, "memory": memory} {
			series, err := rangeQuery(ctx, prometheus, query)
			if err != nil {
				failed = true
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
				for _, sample := range line.Values {
					if len(sample) != 2 {
						continue
					}
					ts, ok := sample[0].(float64)
					if !ok {
						continue
					}
					value, err := strconv.ParseFloat(str(sample[1]), 64)
					if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
						continue
					}
					second := int64(ts)
					if histories[node][second] == nil {
						histories[node][second] = map[string]any{"time": time.Unix(second, 0).UTC().Format(time.RFC3339)}
					}
					histories[node][second][metric] = math.Round(value*100) / 100
				}
			}
		}
		for _, i := range indices {
			r := &snapshot.Resources[i]
			r.Attributes["metrics_status"] = "unavailable"
			r.Attributes["metrics_queried_at"] = time.Now().UTC().Format(time.RFC3339)
			if failed {
				continue
			}
			byTime := histories[str(r.Attributes["metric_node"])]
			ordered := []map[string]any{}
			for _, sample := range byTime {
				ordered = append(ordered, sample)
			}
			sortSamples(ordered)
			if len(ordered) == 0 {
				continue
			}
			last := ordered[len(ordered)-1]
			lastTime, _ := time.Parse(time.RFC3339, str(last["time"]))
			if time.Since(lastTime) > 90*time.Second {
				r.Attributes["metrics_status"] = "stale"
				continue
			}
			r.Attributes["metrics_status"] = "current"
			r.Attributes["history"] = ordered
			if value, ok := last["cpu"]; ok {
				r.Attributes["cpu_percent"] = value
			}
			if value, ok := last["memory"]; ok {
				r.Attributes["memory_percent"] = value
			}
		}
	}
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
	// At most 31 timestamped points per node; keep dependencies minimal.
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && str(values[j]["time"]) < str(values[j-1]["time"]); j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
