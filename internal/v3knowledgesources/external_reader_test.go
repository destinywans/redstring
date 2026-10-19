package v3knowledgesources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3sourceregistry"
)

type externalSecretResolver struct {
	calls *atomic.Int32
	token string
}

func (r externalSecretResolver) Resolve(_ context.Context, _ string) (string, error) {
	r.calls.Add(1)
	return r.token, nil
}

func externalFixture(t *testing.T, kind string, handler http.HandlerFunc) (*ExternalReader, v3sourceregistry.ReadSpec, *atomic.Int32, *atomic.Int32) {
	return externalFixtureWithAPIPath(t, kind, "", handler)
}

func externalFixtureWithAPIPath(t *testing.T, kind, apiPath string, handler http.HandlerFunc) (*ExternalReader, v3sourceregistry.ReadSpec, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var requests, vaultCalls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer scoped-read-token" ||
			(kind == "notion" && r.Header.Get("Notion-Version") != "2026-03-11") {
			t.Errorf("upstream request is not scoped read-only: %s", r.URL.String())
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)
	b := ExternalBinding{Type: kind, CredentialRef: "vault://ops/external#token", Tier: "nonprod",
		EnvironmentID: "dev", ApplicationID: "app-a", TeamID: "team-a", ConnectorID: kind + "-dev"}
	if kind == "confluence" {
		b.SourceURL = upstream.URL + "/wiki"
		if apiPath == "" {
			apiPath = "/wiki/api/v2"
		}
		b.Endpoint = upstream.URL + apiPath
		b.SpaceID = "123"
	} else {
		b.PageID = "2499c512-bbf1-4e46-9175-6ae89f64ec7c"
		b.SourceURL = upstream.URL + "/A-Page-2499c512bbf14e4691756ae89f64ec7c"
		b.Endpoint = upstream.URL + "/v1"
	}
	resolver := v3integrations.NewBoundResolver(externalSecretResolver{calls: &vaultCalls, token: "scoped-read-token"},
		[]v3integrations.CredentialBinding{{Tier: b.Tier, EnvironmentID: b.EnvironmentID, ConnectorID: b.ConnectorID,
			Provider: kind, Endpoint: b.Endpoint, CredentialRef: b.CredentialRef}})
	reader, err := NewExternalReader(kind, []ExternalBinding{b}, resolver, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	spec := v3sourceregistry.ReadSpec{SourceType: kind, URL: b.SourceURL, SecretRef: b.CredentialRef,
		EnvironmentID: b.EnvironmentID, ApplicationID: b.ApplicationID, TeamID: b.TeamID,
		BindingFingerprint: ExternalBindingFingerprint(b)}
	if kind == "confluence" {
		spec.Spaces = []string{"123"}
	}
	if fp, snapshot, ok := reader.BindingEvidenceForCreate(spec); !ok || fp != spec.BindingFingerprint ||
		strings.Contains(string(snapshot), "vault://") {
		t.Fatal("registration binding evidence is missing or secret-bearing")
	}
	return reader, spec, &requests, &vaultCalls
}

func confluenceHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/wiki/api/v2/spaces/123/pages" {
		if r.URL.Query().Get("status") != "current" || r.URL.Query().Get("body-format") != "storage" ||
			r.URL.Query().Get("limit") != "25" {
			http.Error(w, "bad scope", 400)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"id":"9001","spaceId":"123","status":"current","title":"Runbook","version":{"number":7}}],"_links":{}}`))
		return
	}
	if r.URL.Path == "/wiki/api/v2/pages/9001" {
		if r.URL.Query().Get("version") != "7" || r.URL.Query().Get("body-format") != "storage" {
			http.Error(w, "bad version", 400)
			return
		}
		_, _ = w.Write([]byte(`{"id":"9001","spaceId":"123","status":"current","title":"Runbook","version":{"number":7},"body":{"storage":{"representation":"storage","value":"<p>Restart &amp; verify.</p><script>secret()</script>"}}}`))
		return
	}
	http.NotFound(w, r)
}

func TestExternalConfluencePinnedReadAndBinding(t *testing.T) {
	reader, spec, requests, vaultCalls := externalFixture(t, "confluence", confluenceHandler)
	probe := spec
	probe.BindingFingerprint = ""
	if err := reader.Probe(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadExternalContent(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Documents) != 1 || result.Documents[0].Observation.ExternalID != "9001" ||
		result.Documents[0].SourceVersion != "7" || !strings.Contains(result.Documents[0].Body, "Restart & verify.") ||
		strings.Contains(result.Documents[0].Body, "secret()") || requests.Load() != 6 || vaultCalls.Load() != 2 {
		t.Fatalf("unexpected Confluence content/evidence: %+v requests=%d vault=%d", result, requests.Load(), vaultCalls.Load())
	}
	hash := sha256.Sum256([]byte(result.Documents[0].Body))
	if result.Documents[0].Observation.ContentHash != hex.EncodeToString(hash[:]) ||
		!result.Documents[0].Observation.ACLVerified || result.Documents[0].Observation.TeamID != "team-a" {
		t.Fatal("body and scoped observation do not match")
	}
	for _, change := range []func(*v3sourceregistry.ReadSpec){
		func(s *v3sourceregistry.ReadSpec) { s.Spaces = []string{"124"} },
		func(s *v3sourceregistry.ReadSpec) { s.BindingFingerprint = "" },
		func(s *v3sourceregistry.ReadSpec) { s.TeamID = "other" },
		func(s *v3sourceregistry.ReadSpec) { s.SecretRef = "vault://ops/other#token" },
	} {
		modified := spec
		change(&modified)
		if _, err := reader.ReadExternalContent(t.Context(), modified); !errors.Is(err, ErrExternalScope) {
			t.Fatalf("unbound spec read: %v", err)
		}
	}
	if requests.Load() != 6 || vaultCalls.Load() != 2 {
		t.Fatal("unbound reads contacted upstream or Vault")
	}
}

func notionHandler(id, markdown string, truncated bool, unknown []string, edit string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pages/"+id {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "page", "id": id, "last_edited_time": edit,
				"archived": false, "in_trash": false, "properties": map[string]any{"Name": map[string]any{"type": "title",
					"title": []any{map[string]any{"plain_text": "Incident notes"}}}}})
			return
		}
		if r.URL.Path == "/v1/pages/"+id+"/markdown" {
			if r.URL.Query().Get("include_transcript") != "true" {
				http.Error(w, "transcript omitted", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "page_markdown", "id": id,
				"markdown": markdown, "truncated": truncated, "unknown_block_ids": unknown})
			return
		}
		http.NotFound(w, r)
	}
}

func TestExternalNotionExactPageReadAndIncompleteFailClosed(t *testing.T) {
	id := "2499c512-bbf1-4e46-9175-6ae89f64ec7c"
	for _, tc := range []struct {
		name, markdown string
		truncated      bool
		unknown        []string
		wantOK         bool
	}{
		{"complete", "# Findings\n\nRead only.", false, []string{}, true},
		{"truncated", "partial", true, []string{}, false},
		{"unknown block", "partial", false, []string{id}, false},
		{"signed URL", "![file](https://s3.amazonaws.com/a?X-Amz-Signature=secret)", false, []string{}, false},
		{"signed URL with parentheses", "![img](https://s3.amazonaws.com/file(test)?X-Amz-Signature=secret)", false, []string{}, false},
		{"unknown query credential", "![file](https://files.example.test/a?new_secret_key=value)", false, []string{}, false},
		{"relative query credential", "![file](/file?jwt=value)", false, []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, spec, requests, _ := externalFixture(t, "notion", notionHandler(id, tc.markdown, tc.truncated, tc.unknown, "2026-09-29T08:00:00.000Z"))
			result, err := reader.ReadExternalContent(t.Context(), spec)
			if tc.wantOK {
				if err != nil || !result.Complete || len(result.Documents) != 1 || result.Documents[0].Observation.ExternalID != id ||
					result.Documents[0].SourceVersion != "2026-09-29T08:00:00Z" || requests.Load() != 3 {
					t.Fatalf("Notion read failed: %+v err=%v requests=%d", result, err, requests.Load())
				}
			} else if !errors.Is(err, ErrExternalRead) || result.Complete {
				t.Fatalf("incomplete/sensitive content persisted: %+v err=%v", result, err)
			}
		})
	}
}

func TestExternalReaderRejectsDriftAndUntrustedPagination(t *testing.T) {
	var enumerations atomic.Int32
	reader, spec, _, _ := externalFixture(t, "confluence", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wiki/api/v2/spaces/123/pages" {
			if enumerations.Add(1) == 2 {
				_, _ = w.Write([]byte(`{"results":[],"_links":{}}`))
				return
			}
			confluenceHandler(w, r)
			return
		}
		confluenceHandler(w, r)
	})
	if _, err := reader.ReadExternalContent(t.Context(), spec); !errors.Is(err, ErrExternalRead) {
		t.Fatalf("changed enumeration accepted: %v", err)
	}
	reader2, spec2, requests, _ := externalFixture(t, "confluence", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"id":"9001","spaceId":"123","status":"current","title":"Runbook","version":{"number":7}}],"_links":{"next":"https://evil.example/wiki/api/v2/spaces/123/pages?cursor=x"}}`))
	})
	if _, err := reader2.ReadExternalContent(t.Context(), spec2); !errors.Is(err, ErrExternalRead) || requests.Load() != 1 {
		t.Fatalf("foreign cursor followed: err=%v requests=%d", err, requests.Load())
	}
	if validBearerToken("token\r\nX:bad") || validBearerToken(strings.Repeat("x", 4097)) || !validBearerToken("safe-token") {
		t.Fatal("bearer token validation is unsafe")
	}
}

func TestConfluenceStoragePreservesCodeAndTargetsOrFailsClosed(t *testing.T) {
	storage := `<p>Open <a href="https://docs.example.test/run?var-service=api">incident guide</a> ` +
		`and <a href="https://example.test/incident?api_key=secret">console</a>.</p>` +
		`<ac:link><ri:attachment ri:filename="rollback.sh"/><ac:plain-text-link-body><![CDATA[download script]]></ac:plain-text-link-body></ac:link>` +
		`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[kubectl get pods\nfor pod in pods; do echo "$pod"; done]]></ac:plain-text-body></ac:structured-macro>` +
		`<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>Rollback: kubectl rollout undo deploy/api</p></ac:rich-text-body></ac:structured-macro>`
	reader, spec, _, _ := externalFixture(t, "confluence", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wiki/api/v2/pages/9001" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "9001", "spaceId": "123", "status": "current", "title": "Runbook",
				"version": map[string]any{"number": 7}, "body": map[string]any{"storage": map[string]any{"representation": "storage", "value": storage}}})
			return
		}
		confluenceHandler(w, r)
	})
	result, err := reader.ReadExternalContent(t.Context(), spec)
	if err != nil || !result.Complete || len(result.Documents) != 1 {
		t.Fatalf("storage source failed: %+v %v", result, err)
	}
	body := result.Documents[0].Body
	for _, required := range []string{"https://docs.example.test/run", "https://example.test/incident", "URL parameters removed", "attachment: rollback.sh", "download script", "kubectl get pods", "for pod in pods", "kubectl rollout undo deploy/api"} {
		if !strings.Contains(body, required) {
			t.Fatalf("storage lost %q: %q", required, body)
		}
	}
	if strings.Contains(body, "var-service=api") || strings.Contains(body, "api_key=secret") {
		t.Fatalf("storage leaked URL parameters: %q", body)
	}
	for _, bad := range []string{
		`<ac:structured-macro ac:name="code"><ac:plain-text-body>kubectl delete pods</ac:plain-text-body></ac:structured-macro>`,
		`<ac:structured-macro ac:name="unknown"><ac:plain-text-body><![CDATA[important]]></ac:plain-text-body></ac:structured-macro>`,
		`<a href="javascript:alert(1)">restart</a>`,
		`<ac:structured-macro ac:name="expand"><p>Missing body container</p></ac:structured-macro>`,
		`<ac:link><ri:attachment/></ac:link>`,
	} {
		if _, err := plainConfluenceStorage(bad); !errors.Is(err, ErrExternalRead) {
			t.Fatalf("unhandled storage marked complete: %q err=%v", bad, err)
		}
	}
}

func TestNotionTranscriptExplicitlyIncludedAndBounded(t *testing.T) {
	id := "2499c512-bbf1-4e46-9175-6ae89f64ec7c"
	transcript := "# Meeting\n\n## Transcript\nResponder: rollback now.\n\n[Dashboard](https://grafana.example.test/d/abc(test)?var-service=api) [Console](https://example.test/incident?jwt=secret)"
	reader, spec, _, _ := externalFixture(t, "notion", notionHandler(id, transcript, false, []string{}, "2026-09-29T08:00:00Z"))
	result, err := reader.ReadExternalContent(t.Context(), spec)
	if err != nil || len(result.Documents) != 1 || !strings.Contains(result.Documents[0].Body, "Responder: rollback now.") ||
		!strings.Contains(result.Documents[0].Body, "https://grafana.example.test/d/abc(test)") ||
		!strings.Contains(result.Documents[0].Body, "[URL parameters removed: 2]") ||
		strings.Contains(result.Documents[0].Body, "var-service=api") || strings.Contains(result.Documents[0].Body, "jwt=secret") {
		t.Fatalf("transcript or URL sanitization failed: %+v %v", result, err)
	}
	large := strings.Repeat("x", maxExternalPageBytes*2)
	reader, spec, _, _ = externalFixture(t, "notion", notionHandler(id, large, false, []string{}, "2026-09-29T08:00:00Z"))
	if _, err := reader.ReadExternalContent(t.Context(), spec); !errors.Is(err, ErrExternalBounds) {
		t.Fatalf("large transcript marked complete: %v", err)
	}
}

func TestConfluenceOAuthGatewayRelativeCursorUsesPinnedEndpoint(t *testing.T) {
	const apiPath = "/ex/confluence/cloud-123/wiki/api/v2"
	var sawCursor atomic.Bool
	reader, spec, _, _ := externalFixtureWithAPIPath(t, "confluence", apiPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPath+"/spaces/123/pages" {
			if r.URL.Query().Get("cursor") == "next" {
				sawCursor.Store(true)
				_, _ = w.Write([]byte(`{"results":[],"_links":{}}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"id":"9001","spaceId":"123","status":"current","title":"Runbook","version":{"number":7}}],"_links":{"next":"/wiki/api/v2/spaces/123/pages?limit=25&status=current&body-format=storage&cursor=next"}}`))
			return
		}
		if r.URL.Path == apiPath+"/pages/9001" {
			_, _ = w.Write([]byte(`{"id":"9001","spaceId":"123","status":"current","title":"Runbook","version":{"number":7},"body":{"storage":{"representation":"storage","value":"<p>Ready</p>"}}}`))
			return
		}
		http.NotFound(w, r)
	})
	result, err := reader.ReadExternalContent(t.Context(), spec)
	if err != nil || !result.Complete || len(result.Documents) != 1 || !sawCursor.Load() {
		t.Fatalf("OAuth gateway pagination failed: %+v %v cursor=%t", result, err, sawCursor.Load())
	}
}

func TestExternalBindingParserAndMainValidation(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[] true`, `[{"type":"notion","type":"confluence"}]`, `[{"unexpected":1}]`} {
		if _, err := ParseExternalBindings(raw); !errors.Is(err, ErrExternalConfig) {
			t.Fatalf("invalid binding accepted: %q %v", raw, err)
		}
	}
	trueValue := true
	base := mainCreateRequest{Name: "Source", ApplicationID: "app-a", Schedule: "manual", Chunk: "heading", InheritACL: &trueValue,
		SecretRef: "vault://ops/external#token", URL: "https://example.com/wiki"}
	confluence := base
	confluence.Type = "confluence"
	confluence.Spaces = []string{"123"}
	if status, _, _ := validateMainCreate(confluence); status != 0 {
		t.Fatal("valid Confluence scope rejected")
	}
	confluence.Spaces = []string{"bad"}
	if status, _, _ := validateMainCreate(confluence); status != 422 {
		t.Fatal("unbound Confluence space accepted")
	}
	notion := base
	notion.Type = "notion"
	notion.URL = "https://www.notion.so/2499c512bbf14e4691756ae89f64ec7c"
	if status, _, _ := validateMainCreate(notion); status != 0 {
		t.Fatal("valid Notion scope rejected")
	}
}
