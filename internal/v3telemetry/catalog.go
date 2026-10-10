// Package v3telemetry is the PA-19 single source registry for telemetry. The
// database (v3_connectors, v3_connector_targets) says which upstream serves
// which service; a private, operator-reviewed catalog says which data that
// service may see. This round (P0-P2) adds the catalog, a resolver, an
// importer for the file configuration and a compare mode; readers still use
// AIOPS_TELEMETRY_CONFIG until P3 switches them.
package v3telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// ScopeKey identifies one reviewed scope: the same key as a Prometheus query
// without the query ID.
type ScopeKey struct {
	ConnectorID   string
	ApplicationID string
	EnvironmentID string
	ResourceID    string
}

// Catalog is the loaded, validated catalog. Queries feed the existing V3
// Prometheus reader unchanged; Scopes hold Loki, Elasticsearch and Tempo
// scopes in the field names of the legacy telemetry registration.
type Catalog struct {
	Queries map[v3integrations.PrometheusQueryKey]string
	Scopes  map[ScopeKey]Scope
	// Workloads are reviewed Kubernetes workload scopes (PA-08).
	Workloads map[ScopeKey]WorkloadScope
}

// Scope is one provider's subset of a legacy application registration.
type Scope struct {
	Provider string
	connectors.ApplicationTelemetry
}

// QueryEntry and ScopeEntry are the file shapes.
type QueryEntry struct {
	ConnectorID   string `json:"connector_id"`
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceID    string `json:"resource_id"`
	QueryID       string `json:"query_id"`
	Expression    string `json:"expression"`
}

// ScopeEntry uses the legacy registration's field names; only the fields of
// its provider may be set.
type ScopeEntry struct {
	ConnectorID                 string            `json:"connector_id"`
	ApplicationID               string            `json:"application_id"`
	EnvironmentID               string            `json:"environment_id"`
	ResourceID                  string            `json:"resource_id"`
	Provider                    string            `json:"provider"`
	LokiLabels                  map[string]string `json:"loki_labels,omitempty"`
	LokiPatternField            string            `json:"loki_pattern_field,omitempty"`
	LokiCompleteLookbackSeconds int               `json:"loki_complete_lookback_seconds,omitempty"`
	ElasticsearchIndex          string            `json:"elasticsearch_index,omitempty"`
	ElasticsearchPatternField   string            `json:"elasticsearch_pattern_field,omitempty"`
	ElasticsearchFilters        map[string]string `json:"elasticsearch_filters,omitempty"`
	ElasticsearchPrefixes       map[string]string `json:"elasticsearch_prefixes,omitempty"`
	TempoService                string            `json:"tempo_service,omitempty"`
	TempoAttributes             map[string]string `json:"tempo_attributes,omitempty"`
	TempoSafeOperations         []string          `json:"tempo_safe_operations,omitempty"`
}

func (e ScopeEntry) telemetry() connectors.ApplicationTelemetry {
	return connectors.ApplicationTelemetry{LokiLabels: e.LokiLabels, LokiPatternField: e.LokiPatternField,
		LokiCompleteLookbackSeconds: e.LokiCompleteLookbackSeconds, ElasticsearchIndex: e.ElasticsearchIndex,
		ElasticsearchPatternField: e.ElasticsearchPatternField, ElasticsearchFilters: e.ElasticsearchFilters,
		ElasticsearchPrefixes: e.ElasticsearchPrefixes, TempoService: e.TempoService,
		TempoAttributes: e.TempoAttributes, TempoSafeOperations: e.TempoSafeOperations}
}

func scopeEntry(key ScopeKey, scope Scope) ScopeEntry {
	a := scope.ApplicationTelemetry
	return ScopeEntry{ConnectorID: key.ConnectorID, ApplicationID: key.ApplicationID, EnvironmentID: key.EnvironmentID,
		ResourceID: key.ResourceID, Provider: scope.Provider, LokiLabels: a.LokiLabels, LokiPatternField: a.LokiPatternField,
		LokiCompleteLookbackSeconds: a.LokiCompleteLookbackSeconds, ElasticsearchIndex: a.ElasticsearchIndex,
		ElasticsearchPatternField: a.ElasticsearchPatternField, ElasticsearchFilters: a.ElasticsearchFilters,
		ElasticsearchPrefixes: a.ElasticsearchPrefixes, TempoService: a.TempoService,
		TempoAttributes: a.TempoAttributes, TempoSafeOperations: a.TempoSafeOperations}
}

// CatalogFile is the private JSON file: {"queries":[...],"scopes":[...]}.
// A file with only "queries" is the existing Prometheus query catalog.
type CatalogFile struct {
	Queries   []QueryEntry    `json:"queries"`
	Scopes    []ScopeEntry    `json:"scopes,omitempty"`
	Workloads []WorkloadEntry `json:"workloads,omitempty"`
}

const (
	maxCatalogBytes = 1 << 20
	maxQueries      = 500
	maxScopes       = 500
)

// LoadCatalog reads the file at path; an empty path is an empty catalog.
func LoadCatalog(path string) (Catalog, error) {
	if path == "" {
		return Catalog{Queries: map[v3integrations.PrometheusQueryKey]string{}, Scopes: map[ScopeKey]Scope{}, Workloads: map[ScopeKey]WorkloadScope{}}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("open source catalog: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if err != nil || len(data) > maxCatalogBytes {
		return Catalog{}, fmt.Errorf("source catalog exceeds %d bytes", maxCatalogBytes)
	}
	return ParseCatalog(data)
}

// ParseCatalog validates the whole file; one invalid entry rejects it, so a
// partial catalog never takes effect.
func ParseCatalog(data []byte) (Catalog, error) {
	var file CatalogFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&file) != nil || decoder.Decode(new(any)) != io.EOF {
		return Catalog{}, fmt.Errorf("invalid source catalog JSON")
	}
	if len(file.Queries) > maxQueries || len(file.Scopes) > maxScopes {
		return Catalog{}, fmt.Errorf("source catalog has too many entries")
	}
	out := Catalog{Queries: map[v3integrations.PrometheusQueryKey]string{}, Scopes: map[ScopeKey]Scope{}}
	for i, entry := range file.Queries {
		key := v3integrations.PrometheusQueryKey{ConnectorID: entry.ConnectorID, ApplicationID: entry.ApplicationID,
			EnvironmentID: entry.EnvironmentID, ResourceID: entry.ResourceID, QueryID: entry.QueryID}
		if !v3integrations.PrometheusQueryValid(key, entry.Expression) {
			return Catalog{}, fmt.Errorf("invalid query at index %d", i)
		}
		if _, duplicate := out.Queries[key]; duplicate {
			return Catalog{}, fmt.Errorf("duplicate query at index %d", i)
		}
		out.Queries[key] = entry.Expression
	}
	for i, entry := range file.Scopes {
		key := ScopeKey{ConnectorID: entry.ConnectorID, ApplicationID: entry.ApplicationID,
			EnvironmentID: entry.EnvironmentID, ResourceID: entry.ResourceID}
		if key.ConnectorID == "" || key.ApplicationID == "" || key.EnvironmentID == "" ||
			!v3integrations.ResourceIDValid(key.ResourceID) || !validScope(entry.Provider, entry.telemetry()) {
			return Catalog{}, fmt.Errorf("invalid scope at index %d", i)
		}
		if _, duplicate := out.Scopes[key]; duplicate {
			return Catalog{}, fmt.Errorf("duplicate scope at index %d", i)
		}
		out.Scopes[key] = Scope{Provider: entry.Provider, ApplicationTelemetry: entry.telemetry()}
	}
	workloads, err := parseWorkloads(file.Workloads, out.Queries)
	if err != nil {
		return Catalog{}, err
	}
	out.Workloads = workloads
	return out, nil
}

// validScope requires exactly the provider's own fields: a scope can never
// carry PromQL or another provider's selector.
func validScope(provider string, a connectors.ApplicationTelemetry) bool {
	if !reflect.DeepEqual(normalize(ProviderScope(provider, a)), normalize(a)) {
		return false
	}
	switch provider {
	case "loki":
		if len(a.LokiLabels) == 0 {
			return false
		}
	case "elasticsearch":
		if a.ElasticsearchIndex == "" || len(a.ElasticsearchFilters) == 0 {
			return false
		}
	case "tempo":
		if a.TempoService == "" || len(a.TempoAttributes) == 0 {
			return false
		}
	default:
		return false
	}
	return connectors.ValidateApplicationScope(a) == nil
}

// ProviderScope keeps only the fields provider reads. Prometheus has no
// scope fields: its reviewed expressions are catalog queries.
func ProviderScope(provider string, a connectors.ApplicationTelemetry) connectors.ApplicationTelemetry {
	var out connectors.ApplicationTelemetry
	switch provider {
	case "loki":
		out.LokiLabels, out.LokiPatternField, out.LokiCompleteLookbackSeconds = a.LokiLabels, a.LokiPatternField, a.LokiCompleteLookbackSeconds
	case "elasticsearch":
		out.ElasticsearchIndex, out.ElasticsearchPatternField = a.ElasticsearchIndex, a.ElasticsearchPatternField
		out.ElasticsearchFilters, out.ElasticsearchPrefixes = a.ElasticsearchFilters, a.ElasticsearchPrefixes
	case "tempo":
		out.TempoService, out.TempoAttributes, out.TempoSafeOperations = a.TempoService, a.TempoAttributes, a.TempoSafeOperations
	}
	return out
}

// normalize treats nil and empty maps or lists alike and orders operations,
// so equal scopes compare equal regardless of how the JSON spelled them.
func normalize(a connectors.ApplicationTelemetry) connectors.ApplicationTelemetry {
	empty := func(m map[string]string) map[string]string {
		if len(m) == 0 {
			return nil
		}
		return m
	}
	a.PrometheusQueries = empty(a.PrometheusQueries)
	a.LokiLabels = empty(a.LokiLabels)
	a.ElasticsearchFilters = empty(a.ElasticsearchFilters)
	a.ElasticsearchPrefixes = empty(a.ElasticsearchPrefixes)
	a.TempoAttributes = empty(a.TempoAttributes)
	if len(a.TempoSafeOperations) == 0 {
		a.TempoSafeOperations = nil
	} else {
		a.TempoSafeOperations = append([]string(nil), a.TempoSafeOperations...)
		sort.Strings(a.TempoSafeOperations)
	}
	return a
}

// File returns the catalog in its file shape, sorted, for review and diffs.
func (c Catalog) File() CatalogFile {
	out := CatalogFile{Queries: make([]QueryEntry, 0, len(c.Queries))}
	for key, expression := range c.Queries {
		out.Queries = append(out.Queries, QueryEntry{ConnectorID: key.ConnectorID, ApplicationID: key.ApplicationID,
			EnvironmentID: key.EnvironmentID, ResourceID: key.ResourceID, QueryID: key.QueryID, Expression: expression})
	}
	sort.Slice(out.Queries, func(i, j int) bool {
		a, b := out.Queries[i], out.Queries[j]
		return a.EnvironmentID+"\x00"+a.ApplicationID+"\x00"+a.ConnectorID+"\x00"+a.ResourceID+"\x00"+a.QueryID <
			b.EnvironmentID+"\x00"+b.ApplicationID+"\x00"+b.ConnectorID+"\x00"+b.ResourceID+"\x00"+b.QueryID
	})
	for key, scope := range c.Scopes {
		out.Scopes = append(out.Scopes, scopeEntry(key, scope))
	}
	sort.Slice(out.Scopes, func(i, j int) bool {
		a, b := out.Scopes[i], out.Scopes[j]
		return a.EnvironmentID+"\x00"+a.ApplicationID+"\x00"+a.ConnectorID+"\x00"+a.ResourceID <
			b.EnvironmentID+"\x00"+b.ApplicationID+"\x00"+b.ConnectorID+"\x00"+b.ResourceID
	})
	for key, scope := range c.Workloads {
		out.Workloads = append(out.Workloads, WorkloadEntry{ConnectorID: key.ConnectorID, ApplicationID: key.ApplicationID,
			EnvironmentID: key.EnvironmentID, ResourceID: key.ResourceID, Namespace: scope.Namespace,
			Deployment: scope.Deployment, Rollback: scope.Rollback})
	}
	sort.Slice(out.Workloads, func(i, j int) bool {
		a, b := out.Workloads[i], out.Workloads[j]
		return a.EnvironmentID+"\x00"+a.ApplicationID+"\x00"+a.ConnectorID+"\x00"+a.ResourceID <
			b.EnvironmentID+"\x00"+b.ApplicationID+"\x00"+b.ConnectorID+"\x00"+b.ResourceID
	})
	return out
}

// Clone copies the catalog so a plan can add entries without changing the
// loaded configuration.
func (c Catalog) Clone() Catalog {
	out := Catalog{Queries: make(map[v3integrations.PrometheusQueryKey]string, len(c.Queries)), Scopes: make(map[ScopeKey]Scope, len(c.Scopes)),
		Workloads: make(map[ScopeKey]WorkloadScope, len(c.Workloads))}
	for key, value := range c.Workloads {
		out.Workloads[key] = value
	}
	for key, value := range c.Queries {
		out.Queries[key] = value
	}
	for key, value := range c.Scopes {
		out.Scopes[key] = value
	}
	return out
}

// QueriesFor returns the reviewed expressions of one resource by query ID.
func (c Catalog) QueriesFor(key ScopeKey) map[string]string {
	out := map[string]string{}
	for k, expression := range c.Queries {
		if k.ConnectorID == key.ConnectorID && k.ApplicationID == key.ApplicationID &&
			k.EnvironmentID == key.EnvironmentID && k.ResourceID == key.ResourceID {
			out[k.QueryID] = expression
		}
	}
	return out
}
