package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type registryLogVault struct{ empty atomic.Bool }

func (v *registryLogVault) Resolve(_ context.Context, reference string) (string, error) {
	if v.empty.Load() || reference != "vault://aiops/lab/elasticsearch#password" {
		return "", v3integrations.ErrCredentialUnavailable
	}
	return "es-password", nil
}

// PA-19 P3 stage 2 step 3: with the logs reader switched, read_service_logs
// resolves the service's one registered log target and reads it with its
// connector's client (here Elasticsearch basic: options user, Vault
// password). A legacy binding is not a source; unready, ambiguous and
// unreadable sources fail closed with codes the model can act on.
func TestServiceLogsToolReadsTheRegistry(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	var mu sync.Mutex
	var sent []string
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		mu.Lock()
		sent = append(sent, user+"|"+pass)
		mu.Unlock()
		switch r.URL.Path {
		case "/lab-search-logs/_mapping/field/service.name,service.name.keyword":
			_, _ = w.Write([]byte(`{"lab-search-logs":{"mappings":{"service.name":{"full_name":"service.name","mapping":{"name":{"type":"keyword"}}}}}}`))
			return
		case "/lab-search-logs/_settings/index.mapping.ignore_above":
			_, _ = w.Write([]byte(`{"lab-search-logs":{"settings":{}}}`))
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		filters := request["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
		window := filters[0].(map[string]any)["range"].(map[string]any)["@timestamp"].(map[string]any)
		start, _ := time.Parse(time.RFC3339Nano, window["gte"].(string))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"timed_out": false, "_shards": map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits": map[string]any{"total": map[string]any{"value": 1, "relation": "eq"}, "hits": []any{map[string]any{"_source": map[string]any{
				"@timestamp": start.Add(time.Minute).Format(time.RFC3339Nano), "message": "search failed token=abc123secret", "level": "error",
				"service": map[string]any{"name": "search"}}}}},
		})
	}))
	defer es.Close()
	tools := `[{"name":"search_logs","enabled":true}]`
	if _, err := db.Exec(ctx, `
		CREATE TABLE applications (id text PRIMARY KEY,key text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE v3_application_environments (application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE application_environments (application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false,bindings jsonb NOT NULL DEFAULT '[]');
		CREATE TABLE v3_connectors (id text PRIMARY KEY,environment_id text NOT NULL,provider text NOT NULL,endpoint text NOT NULL,
		 auth_method text NOT NULL,credential_ref text NOT NULL DEFAULT '',enabled boolean NOT NULL,tools jsonb NOT NULL,options jsonb NOT NULL DEFAULT '{}');
		CREATE TABLE v3_connector_targets (id text PRIMARY KEY,application_id text NOT NULL,environment_id text NOT NULL,connector_id text NOT NULL,
		 target_kind text NOT NULL,external_id text NOT NULL);
		INSERT INTO applications(id,key) VALUES('app-1','search');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-1','test');
		INSERT INTO application_environments(application_id,key,bindings) VALUES('app-1','test','[{"kind":"loki","resource":"legacy-search"}]');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,endpoint,auth_method,credential_ref,enabled,tools,options)
 VALUES('es-1','test','elasticsearch',$1,'basic','vault://aiops/lab/elasticsearch#password',true,$2,'{"username":"reader"}')`, es.URL, tools); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"scopes": []any{map[string]any{"connector_id": "es-1", "application_id": "app-1",
		"environment_id": "test", "resource_id": "search", "provider": "elasticsearch", "elasticsearch_index": "lab-search-logs",
		"elasticsearch_filters": map[string]string{"service.name": "search"}}}})
	catalog, err := v3telemetry.ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	vault := &registryLogVault{}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	logsAllowed := true
	options := ServiceLogsToolOptions{
		Enabled: true, DB: db, OwnerID: "owner", EnvironmentID: "test",
		Registry: &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals([]v3integrations.CredentialBinding{{
			Tier: "nonprod", EnvironmentID: "test", ConnectorID: "es-1", Provider: "elasticsearch", Endpoint: es.URL,
			CredentialRef: "vault://aiops/lab/elasticsearch#password"}})},
		Clients: &v3telemetry.Clients{Tier: "nonprod", Credentials: vault, Now: func() time.Time { return time.Unix(0, clock.Load()) }},
		Access: logToolAccess(func(decision rbac.V3Decision) error {
			if decision.Module == "logs" && decision.ApplicationID == "app-1" && logsAllowed {
				return nil
			}
			return store.ErrForbidden
		}),
		CurrentUser: func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil },
		LookupService: func(_ context.Context, _ core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			if key != "search" {
				return v3catalog.ServiceEvidence{}, store.ErrNotFound
			}
			return v3catalog.ServiceEvidence{ID: "app-1", Key: key, Environment: env}, nil
		},
	}
	tool, ok := NewServiceLogsTool(options)
	if !ok {
		t.Fatal("tool not registered")
	}
	if _, ok := NewServiceLogsTool(ServiceLogsToolOptions{Enabled: true, DB: db, OwnerID: "owner", EnvironmentID: "test",
		Access: options.Access, CurrentUser: options.CurrentUser, LookupService: options.LookupService}); ok {
		t.Fatal("tool registered without a registry")
	}
	const input = `{"service":"search","query_id":"recent_errors"}`
	expect := func(code, message string) {
		t.Helper()
		_, err := tool.Read(ctx, input)
		if inference.ToolErrorCode(err) != code || !strings.Contains(err.Error(), message) {
			t.Fatalf("want %s %q: %v", code, message, err)
		}
	}
	calls := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(sent)
	}

	// The legacy Loki binding is not a source once switched.
	expect(inference.ToolNotConfigured, "no log source is registered")
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets VALUES('t-es','app-1','test','es-1','log_index','search')`); err != nil {
		t.Fatal(err)
	}
	evidence, err := tool.Read(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var body logsToolEvidence
	if json.Unmarshal([]byte(evidence.Content), &body) != nil || evidence.Source != "elasticsearch" || body.Provider != "elasticsearch" ||
		len(body.Samples) != 1 || body.Samples[0].Level != "error" || strings.Contains(evidence.Content, "abc123secret") {
		t.Fatalf("registered sample: %+v %s", evidence, evidence.Content)
	}
	mu.Lock()
	if sent[0] != "reader|es-password" {
		t.Fatalf("basic auth sent: %v", sent)
	}
	mu.Unlock()

	// Not ready: the reasons follow logs.view; without it nothing is read.
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	before := calls()
	logsAllowed = false
	expect(inference.ToolNotPermitted, "not its logs")
	logsAllowed = true
	expect(inference.ToolNotConfigured, "connector_disabled")
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=true`); err != nil {
		t.Fatal(err)
	}
	if calls() != before {
		t.Fatal("an unready or unauthorized source was read")
	}

	// Two targets are ambiguous.
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets VALUES('t-es-2','app-1','test','es-1','log_index','search-2')`); err != nil {
		t.Fatal(err)
	}
	expect(inference.ToolNotConfigured, "more than one registered log source")
	if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='t-es-2'`); err != nil {
		t.Fatal(err)
	}

	// An empty Vault is a retryable outage once the cached password expires.
	vault.empty.Store(true)
	clock.Add(int64(61 * time.Second))
	expect(inference.ToolSourceUnavailable, "log query failed")
	vault.empty.Store(false)
	if _, err := tool.Read(ctx, input); err != nil {
		t.Fatal(err)
	}
}
