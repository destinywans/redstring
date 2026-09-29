package v3integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// LoadPrometheusQueryCatalog reads operator-reviewed, exact-scope PromQL from
// a private server file. An empty path disables registered metric reads. The
// operator must verify each expression's data scope; identifiers alone cannot
// prove that a PromQL expression filters to the bound application/environment.
// The file shape is {"queries":[{"connector_id":...,"application_id":...,
// "environment_id":...,"resource_id":...,"query_id":...,"expression":...}]}.
func LoadPrometheusQueryCatalog(path string) (map[PrometheusQueryKey]string, error) {
	catalog := map[PrometheusQueryKey]string{}
	if path == "" {
		return catalog, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Prometheus query catalog: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, metricReadError("invalid_catalog")
	}
	var config struct {
		Queries []struct {
			ConnectorID   string `json:"connector_id"`
			ApplicationID string `json:"application_id"`
			EnvironmentID string `json:"environment_id"`
			ResourceID    string `json:"resource_id"`
			QueryID       string `json:"query_id"`
			Expression    string `json:"expression"`
		} `json:"queries"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || len(config.Queries) > 500 {
		return nil, metricReadError("invalid_catalog")
	}
	for _, query := range config.Queries {
		key := PrometheusQueryKey{ConnectorID: query.ConnectorID, ApplicationID: query.ApplicationID,
			EnvironmentID: query.EnvironmentID, ResourceID: query.ResourceID, QueryID: query.QueryID}
		if key.ConnectorID == "" || key.ApplicationID == "" || key.EnvironmentID == "" ||
			!prometheusResourceID.MatchString(key.ResourceID) || !prometheusQueryID.MatchString(key.QueryID) ||
			strings.TrimSpace(query.Expression) != query.Expression || query.Expression == "" || len(query.Expression) > 2048 {
			return nil, metricReadError("invalid_catalog")
		}
		if _, exists := catalog[key]; exists {
			return nil, metricReadError("invalid_catalog")
		}
		catalog[key] = query.Expression
	}
	return catalog, nil
}
