package v3telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// recordingResolver counts reads and records the credential use each read
// was approved for.
type recordingResolver struct {
	mu    sync.Mutex
	reads int
	uses  []v3integrations.CredentialUse
}

func (r *recordingResolver) Resolve(ctx context.Context, reference string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	use, _ := v3integrations.CredentialUseFrom(ctx)
	r.uses = append(r.uses, use)
	if reference != "vault://kv/lab/loki#token" {
		return "", errors.New("unknown reference")
	}
	return "loki-token", nil
}

// Stage 2 step 0: a client per registered source, cached until what it was
// built from changes; its credential comes through the approved bindings,
// cached for a minute and dropped on 401.
func TestClientsBuildCacheAndCredential(t *testing.T) {
	status := 200
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		auth = append(auth, r.Header.Get("Authorization"))
		if r.Method != http.MethodPost || r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("loki request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
	}))
	defer server.Close()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	resolver := &recordingResolver{}
	clients := &Clients{Tier: "nonprod", Credentials: resolver, Now: func() time.Time { return now }}
	source := Source{TargetID: "t", ConnectorID: "loki-1", Provider: "loki", Endpoint: server.URL, AuthMethod: "bearer",
		ResourceID: "checkout", CredentialRef: "vault://kv/lab/loki#token",
		Scope: connectors.ApplicationTelemetry{LokiLabels: map[string]string{"app": "checkout"}}}
	client, resource, err := clients.For("dev", source)
	if err != nil || resource != "checkout" {
		t.Fatalf("client: %v", err)
	}
	read := func() error {
		_, err := client.QueryLogs(context.Background(), resource, connectors.TimeWindow{Start: time.Now().Add(-5 * time.Minute), End: time.Now()}, 10)
		return err
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if resolver.reads != 1 || auth[0] != "Bearer loki-token" ||
		resolver.uses[0] != (v3integrations.CredentialUse{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "loki-1", Provider: "loki", Endpoint: server.URL}) {
		t.Fatalf("credential reads %d uses %+v auth %v", resolver.reads, resolver.uses, auth)
	}
	now = now.Add(61 * time.Second)
	_ = read()
	if resolver.reads != 2 {
		t.Fatalf("credential not refreshed after its TTL: %d", resolver.reads)
	}
	status = 401
	_ = read()
	status = 200
	_ = read()
	if resolver.reads != 3 {
		t.Fatalf("credential not dropped after 401: %d", resolver.reads)
	}

	// The same source reuses its client; a changed scope or endpoint rebuilds.
	again, _, _ := clients.For("dev", source)
	changed := source
	changed.Scope = connectors.ApplicationTelemetry{LokiLabels: map[string]string{"app": "orders"}}
	rebuilt, _, _ := clients.For("dev", changed)
	if again != client || rebuilt == client {
		t.Fatal("client cache does not follow the source")
	}

	// Sources that cannot be read as registered are refused.
	for name, bad := range map[string]Source{
		"not ready":       func() Source { s := source; s.NotReady = []string{"tool_disabled"}; return s }(),
		"remote http":     func() Source { s := source; s.Endpoint = "http://logs.example.invalid"; return s }(),
		"unknown auth":    func() Source { s := source; s.AuthMethod = "digest"; return s }(),
		"none with ref":   func() Source { s := source; s.AuthMethod = "none"; return s }(),
		"no resource":     func() Source { s := source; s.ResourceID = ""; return s }(),
		"unknown product": func() Source { s := source; s.Provider = "splunk"; return s }(),
	} {
		if _, _, err := clients.For("dev", bad); !errors.Is(err, ErrClientUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseReaders(t *testing.T) {
	if readers, err := ParseReaders(" "); err != nil || len(readers) != 0 {
		t.Fatalf("empty: %v %v", readers, err)
	}
	if _, err := ParseReaders("traces"); err == nil {
		t.Fatal("a reader whose step is not built was accepted")
	}
	if _, err := ParseReaders("nonsense"); err == nil {
		t.Fatal("unknown reader accepted")
	}
	if readers, err := ParseReaders("coverage"); err != nil || !readers.Unified(ReaderCoverage) || readers.Unified(ReaderLogs) {
		t.Fatalf("coverage: %v %v", readers, err)
	}
	if readers, err := ParseReaders("coverage,mappings,logs"); err != nil || !readers.Unified(ReaderLogs) || readers.Unified(ReaderTraces) {
		t.Fatalf("through step 3: %v %v", readers, err)
	}
	if _, err := ParseReaders("coverage,coverage"); err == nil {
		t.Fatal("repeated reader accepted")
	}
}

type passwordResolver string

func (p passwordResolver) Resolve(context.Context, string) (string, error) { return string(p), nil }

// Stage 2 step 3 (decision 3): an Elasticsearch basic source sends the user
// from the connector options and the Vault credential as the password. The
// options must name a valid user, or the source is not ready.
func TestClientsBasicAuthAndOptions(t *testing.T) {
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		sent = append(sent, user+"|"+pass)
		switch r.URL.Path {
		case "/lab-search-logs/_mapping/field/service.name,service.name.keyword":
			_, _ = w.Write([]byte(`{"lab-search-logs":{"mappings":{"service.name":{"full_name":"service.name",
 "mapping":{"name":{"type":"keyword"}}}}}}`))
		case "/lab-search-logs/_settings/index.mapping.ignore_above":
			_, _ = w.Write([]byte(`{"lab-search-logs":{"settings":{}}}`))
		default:
			_, _ = w.Write([]byte(`{"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},
 "hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`))
		}
	}))
	defer server.Close()
	clients := &Clients{Tier: "nonprod", Credentials: passwordResolver("es-password")}
	source := Source{TargetID: "t", ConnectorID: "es-1", Provider: "elasticsearch", Endpoint: server.URL, AuthMethod: "basic",
		Username: "reader", ResourceID: "search", CredentialRef: "vault://aiops/lab/elasticsearch#password",
		Scope: connectors.ApplicationTelemetry{ElasticsearchIndex: "lab-search-logs", ElasticsearchFilters: map[string]string{"service.name": "search"}}}
	client, resource, err := clients.For("dev", source)
	if err != nil {
		t.Fatal(err)
	}
	window := connectors.TimeWindow{Start: time.Now().Add(-5 * time.Minute), End: time.Now()}
	if _, err := client.QueryLogsOptions(context.Background(), resource, "elasticsearch", "", "", false, window, 10); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 3 || sent[0] != "reader|es-password" || sent[2] != "reader|es-password" {
		t.Fatalf("basic auth sent: %v", sent)
	}
	// Another user is another client; no user cannot be read.
	other := source
	other.Username = "auditor"
	if again, _, _ := clients.For("dev", other); again == client {
		t.Fatal("a changed user reused the client")
	}
	other.Username = ""
	if _, _, err := clients.For("dev", other); !errors.Is(err, ErrClientUnavailable) {
		t.Fatalf("basic without a user: %v", err)
	}

	resolver := &Resolver{Tier: "nonprod", Approvals: NewApprovals([]v3integrations.CredentialBinding{{Tier: "nonprod", EnvironmentID: "dev",
		ConnectorID: "es-1", Provider: "elasticsearch", Endpoint: server.URL, CredentialRef: source.CredentialRef}})}
	tools := []byte(`[{"name":"search_logs","enabled":true}]`)
	state := ConnectorState{ID: "es-1", Provider: "elasticsearch", Endpoint: server.URL, AuthMethod: "basic",
		CredentialRef: source.CredentialRef, Enabled: true, Tools: tools}
	for options, ready := range map[string]bool{
		`{"username":"reader"}`:                   true,
		`{}`:                                      false, // basic needs a user
		`{"username":"re:ader"}`:                  false, // the user cannot hold a colon
		`{"username":"reader","password":"x"}`:    false, // no secrets in options
		`{"username":"reader","tenant_id":"lab"}`: true,
	} {
		state.Options = []byte(options)
		notReady := resolver.Readiness("dev", state)
		if (len(notReady) == 0) != ready || (!ready && notReady[0] != "options_invalid") {
			t.Errorf("options %s: %v", options, notReady)
		}
	}
}
