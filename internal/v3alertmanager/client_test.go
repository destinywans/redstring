package v3alertmanager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localClient(t *testing.T, handler http.HandlerFunc, tokenFile string) (*Client, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := New(map[string]Endpoint{"dev": {URL: server.URL + "/alertmanager", BearerTokenFile: tokenFile, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client, &requests
}

func TestListSilencesUsesEnvironmentBoundEndpoint(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, requests := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/alertmanager/api/v2/silences" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"silence-1","status":{"state":"active"},"matchers":[{"name":"severity","value":"warning","isRegex":false,"isEqual":true}],"startsAt":"2026-09-28T00:00:00Z","endsAt":"2026-09-29T00:00:00Z","updatedAt":"2026-09-28T00:01:00Z","createdBy":"ops","comment":"maintenance"}]`))
	}, tokenFile)
	if _, err := client.ListSilences(context.Background(), "prod"); !errors.Is(err, ErrNotConfigured) || *requests != 0 {
		t.Fatalf("unbound environment queried: err=%v requests=%d", err, *requests)
	}
	values, err := client.ListSilences(context.Background(), "dev")
	if err != nil || len(values) != 1 || values[0].Status.State != "active" || values[0].Matchers[0].Name != "severity" {
		t.Fatalf("silences=%+v err=%v", values, err)
	}
}

func TestSharedUpstreamCanExceedVisibleRecordBound(t *testing.T) {
	const silence = `{"id":"s1","status":{"state":"active"},"matchers":[{"name":"environment","value":"test","isEqual":true}],"startsAt":"2026-09-28T00:00:00Z","endsAt":"2026-09-29T00:00:00Z","updatedAt":"2026-09-28T00:01:00Z","createdBy":"ops","comment":"maintenance"}`
	const alert = `{"labels":{"environment":"test"},"status":{"silencedBy":["s1"]}}`
	client, _ := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alertmanager/api/v2/silences":
			_, _ = w.Write([]byte("[" + strings.TrimSuffix(strings.Repeat(silence+",", maxRecords+1), ",") + "]"))
		case "/alertmanager/api/v2/alerts":
			_, _ = w.Write([]byte("[" + strings.TrimSuffix(strings.Repeat(alert+",", maxRecords+1), ",") + "]"))
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
	}, "")
	if items, err := client.ListSilences(context.Background(), "dev"); err != nil || len(items) != maxRecords+1 {
		t.Fatalf("silences=%d err=%v", len(items), err)
	}
	if items, err := client.ListAlerts(context.Background(), "dev"); err != nil || len(items) != maxRecords+1 {
		t.Fatalf("alerts=%d err=%v", len(items), err)
	}
}

func TestListAlertsReadsObservedSilenceIDs(t *testing.T) {
	client, _ := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alertmanager/api/v2/alerts" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"labels":{"environment":"test","alertname":"Down"},"status":{"silencedBy":["s1"],"state":"suppressed"}}]`))
	}, "")
	alerts, err := client.ListAlerts(context.Background(), "dev")
	if err != nil || len(alerts) != 1 || alerts[0].Labels["environment"] != "test" || len(alerts[0].Status.SilencedBy) != 1 || alerts[0].Status.SilencedBy[0] != "s1" {
		t.Fatalf("alerts=%+v err=%v", alerts, err)
	}
}

func TestAlertmanagerNullListsAreNotEmptySnapshots(t *testing.T) {
	client, _ := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}, "")
	if alerts, err := client.ListAlerts(context.Background(), "dev"); err == nil || alerts != nil {
		t.Fatalf("null alerts became empty snapshot: %+v %v", alerts, err)
	}
	if silences, err := client.ListSilences(context.Background(), "dev"); err == nil || silences != nil {
		t.Fatalf("null silences became empty snapshot: %+v %v", silences, err)
	}
}

func TestInhibitionRulesComeFromStatusWithoutReturningReceiverSecrets(t *testing.T) {
	client, _ := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alertmanager/api/v2/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"config":{"original":"receivers:\n  - name: webhook\n    webhook_configs:\n      - url: https://secret.example/path\ninhibit_rules:\n  - source_matchers: ['severity=\"critical\"']\n    target_matchers: ['severity=\"warning\"']\n    equal: [alertname, namespace]\n"}}`))
	}, "")
	rules, err := client.ListInhibitionRules(context.Background(), "dev")
	if err != nil || len(rules) != 1 || len(rules[0].Equal) != 2 || rules[0].SourceMatchers[0] != `severity="critical"` {
		t.Fatalf("rules=%+v err=%v", rules, err)
	}
}

func TestRedirectAndOversizedResponseRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   http.HandlerFunc
	}{
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.invalid/", http.StatusFound)
		}},
		{"oversized", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := localClient(t, tc.fn, "")
			if _, err := client.ListSilences(context.Background(), "dev"); err == nil {
				t.Fatal("unexpected successful response")
			}
		})
	}
}

func TestNewRejectsUnsafeEndpoint(t *testing.T) {
	for _, ep := range []Endpoint{
		{URL: "http://example.com"},
		{URL: "http://example.com", AllowHTTP: true, BearerTokenFile: "/private/token"},
		{URL: "http://localhost:9093", AllowHTTP: true, BearerTokenFile: "/private/token"},
		{URL: "https://user:pass@example.com"},
		{URL: "https://example.com/?token=secret"},
		{URL: "https://example.com", BearerTokenFile: "relative/token"},
	} {
		if _, err := New(map[string]Endpoint{"dev": ep}, nil); err == nil {
			t.Fatalf("accepted unsafe endpoint: %+v", ep)
		}
	}
}

func TestListSilencesRejectsReadableByOthersTokenFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("private-token"), 0644); err != nil {
		t.Fatal(err)
	}
	client, requests := localClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("request sent with insecure token file")
	}, tokenFile)
	if _, err := client.ListSilences(context.Background(), "dev"); err == nil || *requests != 0 {
		t.Fatalf("insecure token file accepted: err=%v requests=%d", err, *requests)
	}
}
