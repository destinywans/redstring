package v3knowledgesources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3sourceregistry"
)

const feishuTestDocumentID = "AbCdEf1234567890"

type feishuTestResolver struct {
	calls *atomic.Int32
	deny  *atomic.Bool
}

func (r feishuTestResolver) Resolve(_ context.Context, _ string) (string, error) {
	r.calls.Add(1)
	if r.deny.Load() {
		return "", errors.New("revoked")
	}
	return "read-only-token", nil
}

func feishuFixture(t *testing.T, handler http.HandlerFunc) (*FeishuReader, v3sourceregistry.ReadSpec, *atomic.Int32, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	var visits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visits.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer read-only-token" ||
			r.URL.RawQuery != "" || r.URL.Fragment != "" {
			t.Errorf("unexpected Feishu request: %s %s", r.Method, r.URL.String())
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	binding := FeishuBinding{SourceURL: "https://tenant.feishu.cn/docx/" + feishuTestDocumentID,
		Endpoint: "https://open.feishu.cn", DocumentID: feishuTestDocumentID, CredentialRef: "vault://ops/feishu#token",
		Tier: "nonprod", EnvironmentID: "dev", ApplicationID: "app-a", TeamID: "team-a", ConnectorID: "feishu-dev"}
	if !validFeishuBinding(binding) {
		t.Fatal("fixture binding failed structural validation")
	}
	var calls atomic.Int32
	var deny atomic.Bool
	resolver := v3integrations.NewBoundResolver(feishuTestResolver{&calls, &deny}, []v3integrations.CredentialBinding{{
		Tier: binding.Tier, EnvironmentID: binding.EnvironmentID, ConnectorID: binding.ConnectorID,
		Provider: "feishu", Endpoint: binding.Endpoint, CredentialRef: binding.CredentialRef,
	}})
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	if len(server.Certificate().DNSNames) == 0 {
		t.Fatal("test TLS certificate lacks a DNS name")
	}
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client.Transport = transport
	reader, err := NewFeishuReader([]FeishuBinding{binding}, resolver, client)
	if err != nil {
		t.Fatal(err)
	}
	spec := v3sourceregistry.ReadSpec{SourceType: "feishu", URL: binding.SourceURL,
		SecretRef: binding.CredentialRef, EnvironmentID: binding.EnvironmentID,
		ApplicationID: binding.ApplicationID, TeamID: binding.TeamID,
		BindingFingerprint: FeishuBindingFingerprint(binding)}
	return reader, spec, &calls, &deny, &visits
}

func feishuSuccessHandler(t *testing.T, title, body string, revision int64) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		base := "/open-apis/docx/v1/documents/" + feishuTestDocumentID
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case base:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"document": map[string]any{
				"document_id": feishuTestDocumentID, "revision_id": revision, "title": title}}})
		case base + "/raw_content":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"content": body}})
		default:
			t.Errorf("unexpected endpoint %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func TestFeishuReaderPinnedDocxProbeAndRead(t *testing.T) {
	reader, spec, vaultCalls, _, visits := feishuFixture(t, feishuSuccessHandler(t, "Runbook", "service alert\nrecover safely", 7))
	fingerprint, snapshot, ok := reader.BindingEvidenceForCreate(spec)
	if !ok || fingerprint != spec.BindingFingerprint || !strings.Contains(string(snapshot), `"document_id":"`+feishuTestDocumentID+`"`) ||
		strings.Contains(string(snapshot), "vault://") {
		t.Fatalf("invalid registration evidence: %q %q", fingerprint, snapshot)
	}
	probe := spec
	probe.BindingFingerprint = ""
	if err := reader.Probe(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadFeishu(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.DocumentID != feishuTestDocumentID || result.Title != "Runbook" ||
		result.Revision != "7" || result.Body != "service alert\nrecover safely" ||
		result.Observation.ExternalID != feishuTestDocumentID || !result.Observation.ACLVerified ||
		result.Observation.EnvironmentID != "dev" || result.Observation.ApplicationID != "app-a" ||
		result.Observation.TeamID != "team-a" {
		t.Fatalf("unexpected result: %+v", result)
	}
	hash := sha256.Sum256([]byte(result.Body))
	if result.Observation.ContentHash != hex.EncodeToString(hash[:]) {
		t.Fatal("content hash was not from the returned body")
	}
	observations, err := reader.Read(t.Context(), spec)
	if err != nil || !observations.Complete || len(observations.Items) != 1 ||
		vaultCalls.Load() != 3 || visits.Load() != 9 {
		t.Fatalf("read did not preserve complete observation: %+v %v, vault=%d http=%d", observations, err, vaultCalls.Load(), visits.Load())
	}
}

func TestFeishuReaderScopeAndCredentialRevocation(t *testing.T) {
	reader, spec, vaultCalls, deny, visits := feishuFixture(t, feishuSuccessHandler(t, "Runbook", "body", 1))
	for _, mutation := range []func(*v3sourceregistry.ReadSpec){
		func(s *v3sourceregistry.ReadSpec) { s.ApplicationID = "other" },
		func(s *v3sourceregistry.ReadSpec) { s.TeamID = "other" },
		func(s *v3sourceregistry.ReadSpec) { s.EnvironmentID = "other" },
		func(s *v3sourceregistry.ReadSpec) { s.URL += "/other" },
		func(s *v3sourceregistry.ReadSpec) { s.SecretRef = "vault://ops/other#token" },
		func(s *v3sourceregistry.ReadSpec) { s.Spaces = []string{"x"} },
		func(s *v3sourceregistry.ReadSpec) { s.BindingFingerprint = "" },
		func(s *v3sourceregistry.ReadSpec) { s.BindingFingerprint = strings.Repeat("0", 64) },
	} {
		modified := spec
		mutation(&modified)
		if _, err := reader.ReadFeishu(t.Context(), modified); !errors.Is(err, ErrFeishuScope) {
			t.Fatalf("unbound read succeeded: %+v %v", modified, err)
		}
	}
	if vaultCalls.Load() != 0 || visits.Load() != 0 {
		t.Fatal("unbound scope contacted Vault or Feishu")
	}
	deny.Store(true)
	if _, err := reader.ReadFeishu(t.Context(), spec); !errors.Is(err, ErrFeishuRead) ||
		strings.Contains(err.Error(), spec.SecretRef) {
		t.Fatalf("revoked credential did not fail closed: %v", err)
	}
	if visits.Load() != 0 || vaultCalls.Load() != 1 {
		t.Fatal("revoked credential contacted Feishu")
	}
}

func TestFeishuReaderRejectsDriftIncompleteAndUnsafeContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"revision drift", func() http.HandlerFunc {
			var metadata atomic.Int32
			good := feishuSuccessHandler(t, "Runbook", "body", 1)
			return func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, feishuTestDocumentID) && metadata.Add(1) == 2 {
					_, _ = w.Write([]byte(`{"code":0,"data":{"document":{"document_id":"` + feishuTestDocumentID + `","revision_id":2,"title":"Runbook"}}}`))
					return
				}
				good(w, r)
			}
		}()},
		{"missing content", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/raw_content") {
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				return
			}
			feishuSuccessHandler(t, "Runbook", "body", 1)(w, r)
		}},
		{"upstream denied", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/raw_content") {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
			feishuSuccessHandler(t, "Runbook", "body", 1)(w, r)
		}},
		{"api code denied", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/raw_content") {
				_, _ = w.Write([]byte(`{"code":99991663,"msg":"permission denied","data":{}}`))
				return
			}
			feishuSuccessHandler(t, "Runbook", "body", 1)(w, r)
		}},
		{"signed URL", feishuSuccessHandler(t, "Runbook", "https://files.test/a?X-Amz-Signature=secret", 1)},
		{"uppercase signed URL", feishuSuccessHandler(t, "Runbook", "HTTPS://files.test/a?token=secret", 1)},
		{"control character", feishuSuccessHandler(t, "Runbook", "body\x00secret", 1)},
		{"invalid UTF-8 on wire", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/raw_content") {
				_, _ = w.Write(append([]byte(`{"code":0,"data":{"content":"`), append([]byte{0xff}, []byte(`"}}`)...)...))
				return
			}
			feishuSuccessHandler(t, "Runbook", "body", 1)(w, r)
		}},
		{"oversize", feishuSuccessHandler(t, "Runbook", strings.Repeat("a", maxFeishuBodyBytes+1), 1)},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/raw_content") {
				http.Redirect(w, r, "https://other.example/secret", http.StatusFound)
				return
			}
			feishuSuccessHandler(t, "Runbook", "body", 1)(w, r)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, spec, _, _, _ := feishuFixture(t, tc.handler)
			if _, err := reader.ReadFeishu(t.Context(), spec); err == nil {
				t.Fatal("incomplete or unsafe read was accepted")
			}
		})
	}
}

func TestFeishuBindingValidationAndFingerprint(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `[] true`, `[{}]`, `[1]`,
		`[{"source_url":"https://tenant.feishu.cn/docx/AbCdEf1234567890","source_url":"https://other.feishu.cn/docx/AbCdEf1234567890"}]`,
		`[{"surprise":"x"}]`,
	} {
		if _, err := ParseFeishuBindings(raw); !errors.Is(err, ErrFeishuConfig) {
			t.Fatalf("invalid binding accepted: %q %v", raw, err)
		}
	}
	reader, spec, _, _, _ := feishuFixture(t, feishuSuccessHandler(t, "Runbook", "body", 1))
	base := reader.bindings[0]
	for _, mutate := range []func(*FeishuBinding){
		func(b *FeishuBinding) { b.SourceURL = "https://tenant.feishu.cn/wiki/" + b.DocumentID },
		func(b *FeishuBinding) { b.SourceURL += "?token=secret" },
		func(b *FeishuBinding) { b.SourceURL = "https://evil.test/docx/" + b.DocumentID },
		func(b *FeishuBinding) { b.DocumentID = "../other" },
		func(b *FeishuBinding) { b.Endpoint = "http://open.feishu.cn" },
		func(b *FeishuBinding) { b.Endpoint += "/redirect" },
		func(b *FeishuBinding) { b.CredentialRef = "vault://ops/../other#token" },
	} {
		changed := base
		mutate(&changed)
		if validFeishuBinding(changed) {
			t.Fatalf("unsafe binding accepted: %+v", changed)
		}
	}
	changed := base
	changed.ConnectorID = "feishu-other"
	if FeishuBindingFingerprint(changed) == spec.BindingFingerprint {
		t.Fatal("connector change did not invalidate saved binding")
	}
	changed = base
	changed.CredentialRef = "vault://ops/other#token"
	if FeishuBindingFingerprint(changed) == spec.BindingFingerprint {
		t.Fatal("credential change did not invalidate saved binding")
	}
}
