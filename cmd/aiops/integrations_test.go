package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/httpapi"
)

func TestLogEvidenceToolLimitIsIndependentFromBrowsing(t *testing.T) {
	for _, maximum := range []int{100, 3} {
		t.Run(strconv.Itoa(maximum), func(t *testing.T) {
			var limit int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
				fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
			}))
			defer server.Close()
			client, err := connectors.NewTelemetry(connectors.TelemetryConfig{Loki: connectors.TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, MaxLogEntries: maximum, Applications: map[string]connectors.ApplicationTelemetry{"lab": {LokiLabels: map[string]string{"application": "checkout"}}}})
			if err != nil {
				t.Fatal(err)
			}
			tool := logEvidenceTool(client, "lab")
			if _, err = tool.Read(context.Background(), "ignored user scope"); err != nil {
				t.Fatal(err)
			}
			if limit != min(10, maximum) {
				t.Fatalf("AI limit: %d", limit)
			}
			if _, err = client.QueryLogs(context.Background(), "lab", connectors.TimeWindow{}, 0); err != nil {
				t.Fatal(err)
			}
			if limit != min(100, maximum) {
				t.Fatalf("browsing limit changed: %d", limit)
			}
		})
	}
}

// PA-40: the model sees masked line text, label values and templates only.
func TestLogEvidenceToolMasksBeforeTheModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"application":"checkout","user":"bob@example.com"},"values":[["%d","login failed password=hunter2 for 13812345678 trace_id=9d2870f771d3eac3a7faeb4db4515e00"]]}]}}`, time.Now().Add(-time.Minute).UnixNano())
	}))
	defer server.Close()
	client, err := connectors.NewTelemetry(connectors.TelemetryConfig{Loki: connectors.TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, MaxLogEntries: 10, Applications: map[string]connectors.ApplicationTelemetry{"lab": {LokiLabels: map[string]string{"application": "checkout"}}}})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := logEvidenceTool(client, "lab").Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"hunter2", "13812345678", "bob@example.com"} {
		if strings.Contains(evidence.Content, leaked) {
			t.Fatalf("%q reached the model: %s", leaked, evidence.Content)
		}
	}
	for _, kept := range []string{"password=[REDACTED:secret]", "[REDACTED:phone]", "9d2870f771d3eac3a7faeb4db4515e00", `"redacted":true`, `"email":1`} {
		if !strings.Contains(evidence.Content, kept) {
			t.Fatalf("missing %q: %s", kept, evidence.Content)
		}
	}
}

func TestKubernetesInventoryUsesRegistry(t *testing.T) {
	api := &httpapi.Server{}
	registry := &platformRegistry{kubernetes: map[string]registeredKubernetes{
		"lab-order":    {ID: "lab-order"},
		"lab-checkout": {ID: "lab-checkout"},
	}}
	registerKubernetesIntegrations(api, registry)
	if len(api.Integrations) != 2 {
		t.Fatalf("inventory: %+v", api.Integrations)
	}
	for i, id := range []string{"lab-checkout", "lab-order"} {
		item := api.Integrations[i]
		if item.ID != id || !item.Configured || item.Status != "configured_unverified" || len(item.RegisteredBindings) != 1 || item.RegisteredBindings[0].ConnectorID != id {
			t.Fatalf("registration missing: %+v", item)
		}
		if len(item.Capabilities) != 1 || item.Capabilities[0] != "deployment.read" {
			t.Fatalf("read-only registration advertised writes: %+v", item)
		}
	}
}
