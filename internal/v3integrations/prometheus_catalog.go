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
		if !PrometheusQueryValid(key, query.Expression) {
			return nil, metricReadError("invalid_catalog")
		}
		if _, exists := catalog[key]; exists {
			return nil, metricReadError("invalid_catalog")
		}
		catalog[key] = query.Expression
	}
	return catalog, nil
}

// MaxPrometheusExpression bounds one reviewed expression (PA-43: 8 KiB). The
// reader sends it in a POST form, never in a URL.
const MaxPrometheusExpression = 8192

// PrometheusQueryValid is the catalog rule for one reviewed expression.
func PrometheusQueryValid(key PrometheusQueryKey, expression string) bool {
	return key.ConnectorID != "" && key.ApplicationID != "" && key.EnvironmentID != "" &&
		prometheusResourceID.MatchString(key.ResourceID) && prometheusQueryID.MatchString(key.QueryID) &&
		strings.TrimSpace(expression) == expression && expression != "" && len(expression) <= MaxPrometheusExpression
}

// ResourceIDValid reports whether id is a valid opaque catalog resource key.
func ResourceIDValid(id string) bool { return prometheusResourceID.MatchString(id) }

// ValidEndpoint is the registry's endpoint rule: HTTPS, or HTTP on loopback.
func ValidEndpoint(raw string) bool {
	_, err := validEndpoint(raw)
	return err == nil
}

// ProviderInfo describes a registrable provider without its probe.
type ProviderInfo struct {
	Name, Category string
	Methods, Tools []string
}

// Provider returns the registry descriptor of a supported provider.
func Provider(provider string) (ProviderInfo, bool) {
	d, ok := descriptors[provider]
	if !ok {
		return ProviderInfo{}, false
	}
	return ProviderInfo{Name: d.Name, Category: d.Category,
		Methods: append([]string(nil), d.Methods...), Tools: append([]string(nil), d.Tools...)}, true
}
