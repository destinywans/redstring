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

	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

const gitTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const gitTestBody = "# Check\n\nRead only.\n"

var gitTestBlob = gitBlobID([]byte(gitTestBody), 40)

func gitReaderFixture(t *testing.T, handler http.HandlerFunc) (*GitMarkdownReader, v3sourceregistry.ReadSpec, *atomic.Int32, *atomic.Bool) {
	return gitReaderFixtureWithProject(t, handler, "")
}

func gitReaderFixtureWithProject(t *testing.T, handler http.HandlerFunc, webURLOverride string) (*GitMarkdownReader, v3sourceregistry.ReadSpec, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	var vaultCalls atomic.Int32
	var denyVault atomic.Bool
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vaultCalls.Add(1)
		if denyVault.Load() {
			http.Error(w, "unavailable", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/ops/data/gitlab" || r.Header.Get("X-Vault-Token") != "vault-access" {
			http.Error(w, "unavailable", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"repo-read-only"}}}`))
	}))
	t.Cleanup(vault.Close)
	var gitlab *httptest.Server
	gitlab = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/projects/123" {
			if r.Header.Get("PRIVATE-TOKEN") != "repo-read-only" {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
			webURL := gitlab.URL + "/team/handbook"
			if webURLOverride != "" {
				webURL = webURLOverride
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "web_url": webURL})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(gitlab.Close)
	binding := GitMarkdownBinding{
		SourceURL: gitlab.URL + "/team/handbook", Endpoint: gitlab.URL,
		ProjectID: 123, Branch: "main", PathPrefix: "docs", CredentialRef: "vault://ops/gitlab#token",
		Tier: "nonprod", EnvironmentID: "dev", ApplicationID: "app-a", TeamID: "team-a", ConnectorID: "gitlab-dev",
	}
	inner := v3integrations.NewVaultResolver(vault.URL, "vault-access")
	resolver := v3integrations.NewBoundResolver(inner, []v3integrations.CredentialBinding{{
		Tier: binding.Tier, EnvironmentID: binding.EnvironmentID, ConnectorID: binding.ConnectorID,
		Provider: "gitlab", Endpoint: binding.Endpoint, CredentialRef: binding.CredentialRef,
	}})
	reader, err := NewGitMarkdownReader([]GitMarkdownBinding{binding}, resolver, gitlab.Client())
	if err != nil {
		t.Fatal(err)
	}
	spec := v3sourceregistry.ReadSpec{SourceType: "git_markdown", URL: binding.SourceURL,
		SecretRef: binding.CredentialRef, EnvironmentID: binding.EnvironmentID,
		ApplicationID: binding.ApplicationID, TeamID: binding.TeamID,
		BindingFingerprint: GitMarkdownBindingFingerprint(binding)}
	if !BindingMatchesCreate(binding, spec.URL, spec.SecretRef, spec.EnvironmentID, spec.ApplicationID, spec.TeamID) ||
		!reader.BindingMatchesCreate(spec) {
		t.Fatal("exact source binding must match registration")
	}
	fingerprint, snapshot, ok := reader.BindingEvidenceForCreate(spec)
	if !ok || fingerprint != spec.BindingFingerprint || !strings.Contains(string(snapshot), `"project_id":123`) ||
		strings.Contains(string(snapshot), "vault://") {
		t.Fatal("binding evidence must preserve nonsecret project scope")
	}
	return reader, spec, &vaultCalls, &denyVault
}

func gitSuccessHandler(t *testing.T, visits *atomic.Int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		visits.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "repo-read-only" ||
			!strings.HasPrefix(r.URL.Path, "/api/v4/projects/123/repository/") {
			t.Errorf("unexpected GitLab request: %s", r.URL.String())
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches/main"):
			_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"` + gitTestSHA + `"}}`))
		case strings.HasSuffix(r.URL.Path, "/tree"):
			if r.URL.Query().Get("path") != "docs" || r.URL.Query().Get("ref") != gitTestSHA ||
				r.URL.Query().Get("recursive") != "true" {
				t.Errorf("tree was not pinned to configured scope and commit: %s", r.URL.String())
			}
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			if r.URL.Query().Get("page") != "1" {
				t.Errorf("unexpected tree page: %s", r.URL.String())
			}
			_, _ = w.Write([]byte(`[{"id":"` + gitTestBlob + `","path":"docs/check.md","type":"blob"},` +
				`{"id":"` + gitTestBlob + `","path":"docs/notes.txt","type":"blob"}]`))
		case strings.Contains(r.URL.Path, "/files/docs/check.md/raw"):
			if r.URL.Query().Get("ref") != gitTestSHA {
				t.Errorf("raw file did not use pinned commit: %s", r.URL.String())
			}
			_, _ = w.Write([]byte(gitTestBody))
		default:
			t.Errorf("unexpected GitLab path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func TestGitMarkdownReaderPinnedReadAndProbe(t *testing.T) {
	var visits atomic.Int32
	reader, spec, vaultCalls, _ := gitReaderFixture(t, gitSuccessHandler(t, &visits))
	probe := spec
	probe.BindingFingerprint = ""
	if err := reader.Probe(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadGitMarkdown(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.CommitSHA != gitTestSHA || len(result.Files) != 1 ||
		result.Files[0].Path != "docs/check.md" || result.Files[0].Observation.ExternalID != "docs/check.md" ||
		result.Files[0].Observation.EnvironmentID != "dev" || result.Files[0].Observation.ApplicationID != "app-a" ||
		result.Files[0].Observation.TeamID != "team-a" || !result.Files[0].Observation.ACLVerified {
		t.Fatalf("invalid pinned result: %+v", result)
	}
	hash := sha256.Sum256([]byte(result.Files[0].Body))
	if result.Files[0].Observation.ContentHash != hex.EncodeToString(hash[:]) {
		t.Fatal("hash is not from exact returned body")
	}
	observations, err := reader.Read(t.Context(), spec)
	if err != nil || !observations.Complete || len(observations.Items) != 1 || visits.Load() != 12 || vaultCalls.Load() != 3 {
		t.Fatalf("observation read failed: result=%+v err=%v requests=%d vault=%d", observations, err, visits.Load(), vaultCalls.Load())
	}
}

func TestGitMarkdownReaderScopeAndConfigurationFailClosed(t *testing.T) {
	var visits atomic.Int32
	reader, spec, vaultCalls, denyVault := gitReaderFixture(t, gitSuccessHandler(t, &visits))
	for _, mutation := range []func(*v3sourceregistry.ReadSpec){
		func(s *v3sourceregistry.ReadSpec) { s.ApplicationID = "other" },
		func(s *v3sourceregistry.ReadSpec) { s.TeamID = "other" },
		func(s *v3sourceregistry.ReadSpec) { s.URL += "/other" },
		func(s *v3sourceregistry.ReadSpec) { s.SecretRef = "vault://ops/other#token" },
		func(s *v3sourceregistry.ReadSpec) { s.EnvironmentID = "prod" },
		func(s *v3sourceregistry.ReadSpec) { s.BindingFingerprint = "" },
		func(s *v3sourceregistry.ReadSpec) { s.BindingFingerprint = strings.Repeat("0", 64) },
	} {
		modified := spec
		mutation(&modified)
		if _, err := reader.ReadGitMarkdown(t.Context(), modified); !errors.Is(err, ErrGitMarkdownScope) {
			t.Fatalf("unbound source was read: %+v err=%v", modified, err)
		}
	}
	if visits.Load() != 0 || vaultCalls.Load() != 0 {
		t.Fatal("unbound specs must not contact Vault or GitLab")
	}
	denyVault.Store(true)
	if _, err := reader.ReadGitMarkdown(t.Context(), spec); !errors.Is(err, ErrGitMarkdownRead) ||
		strings.Contains(err.Error(), "repo-read-only") || strings.Contains(err.Error(), spec.SecretRef) {
		t.Fatalf("Vault denial must fail without leaking credentials: %v", err)
	}
	if visits.Load() != 0 || vaultCalls.Load() != 1 {
		t.Fatal("Vault denial must prevent GitLab access")
	}
	denyVault.Store(false)
	for _, raw := range []string{
		`[{"source_url":"https://example.test/a/b","source_url":"https://example.test/c/d"}]`,
		`[{"unexpected":"field"}]`,
		`null`, `{}`, `[] true`,
	} {
		if _, err := ParseGitMarkdownBindings(raw); !errors.Is(err, ErrGitMarkdownConfig) {
			t.Fatalf("invalid config parsed: %q err=%v", raw, err)
		}
	}
	configs := []GitMarkdownBinding{}
	encoded, err := json.Marshal(configs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseGitMarkdownBindings(string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGitMarkdownReader(nil, nil, nil); !errors.Is(err, ErrGitMarkdownConfig) {
		t.Fatal("reader without an exact resolver must fail closed")
	}
	otherApp := reader.bindings[0]
	otherApp.ApplicationID = "app-b"
	multiAppReader, err := NewGitMarkdownReader([]GitMarkdownBinding{reader.bindings[0], otherApp}, reader.resolver, reader.client)
	if err != nil {
		t.Fatal("exact app scope makes shared source URL unambiguous:", err)
	}
	otherAppSpec := spec
	otherAppSpec.ApplicationID = "app-b"
	otherAppSpec.BindingFingerprint = ""
	if err := multiAppReader.Probe(t.Context(), otherAppSpec); err != nil {
		t.Fatal("probe must select exact application binding:", err)
	}
	otherAppSpec.ApplicationID = "app-c"
	if err := multiAppReader.Probe(t.Context(), otherAppSpec); !errors.Is(err, ErrGitMarkdownScope) {
		t.Fatal("probe must reject application absent from server bindings")
	}
	otherAppSpec.ApplicationID = ""
	if err := multiAppReader.Probe(t.Context(), otherAppSpec); !errors.Is(err, ErrGitMarkdownScope) {
		t.Fatal("unsaved probe must require application scope")
	}
	beforeGit, beforeVault := visits.Load(), vaultCalls.Load()
	for _, change := range []func(*GitMarkdownBinding){
		func(b *GitMarkdownBinding) { b.ProjectID = 124 },
		func(b *GitMarkdownBinding) { b.Branch = "release" },
		func(b *GitMarkdownBinding) { b.PathPrefix = "runbooks" },
		func(b *GitMarkdownBinding) { b.ConnectorID = "gitlab-other" },
	} {
		changed := reader.bindings[0]
		change(&changed)
		if GitMarkdownBindingFingerprint(changed) == spec.BindingFingerprint {
			t.Fatal("read-scope change did not change binding fingerprint")
		}
		// A changed connector requires its own binding and is covered by the
		// fingerprint comparison; the other mutations also construct a reader.
		if changed.ConnectorID == reader.bindings[0].ConnectorID {
			changedReader, err := NewGitMarkdownReader([]GitMarkdownBinding{changed}, reader.resolver, reader.client)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := changedReader.ReadGitMarkdown(t.Context(), spec); !errors.Is(err, ErrGitMarkdownScope) {
				t.Fatal("saved registration must not silently repoint after config change:", err)
			}
		}
	}
	if visits.Load() != beforeGit || vaultCalls.Load() != beforeVault {
		t.Fatal("binding drift must be rejected before Vault or GitLab access")
	}
	badInactive := reader.bindings[0]
	badInactive.Tier = "inactive"
	badJSON, err := json.Marshal([]GitMarkdownBinding{badInactive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseGitMarkdownBindings(string(badJSON)); !errors.Is(err, ErrGitMarkdownConfig) {
		t.Fatal("configuration must validate every tier before startup filtering")
	}
	duplicateJSON, err := json.Marshal([]GitMarkdownBinding{reader.bindings[0], reader.bindings[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseGitMarkdownBindings(string(duplicateJSON)); !errors.Is(err, ErrGitMarkdownConfig) {
		t.Fatal("configuration must reject duplicate exact bindings")
	}
}

func TestGitMarkdownReaderRejectsUnsafeGitLabResponses(t *testing.T) {
	tests := []struct {
		name    string
		respond http.HandlerFunc
		want    error
	}{
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://other.invalid/secret", http.StatusFound)
		}, ErrGitMarkdownRead},
		{"branch mismatch", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"name":"other","commit":{"id":"` + gitTestSHA + `"}}`))
		}, ErrGitMarkdownRead},
		{"tree traversal", func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/branches/") {
				_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"` + gitTestSHA + `"}}`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":"` + gitTestBlob + `","path":"docs/../secrets.md","type":"blob"}]`))
		}, ErrGitMarkdownRead},
		{"oversize file", func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/branches/"):
				_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"` + gitTestSHA + `"}}`))
			case strings.HasSuffix(r.URL.Path, "/tree") && r.URL.Query().Get("page") == "2":
				_, _ = w.Write([]byte(`[]`))
			case strings.HasSuffix(r.URL.Path, "/tree"):
				_, _ = w.Write([]byte(`[{"id":"` + gitTestBlob + `","path":"docs/big.md","type":"blob"}]`))
			default:
				_, _ = w.Write([]byte(strings.Repeat("x", maxGitMarkdownBytes+1)))
			}
		}, ErrGitMarkdownBounds},
		{"file differs from pinned tree", func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/branches/"):
				_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"` + gitTestSHA + `"}}`))
			case strings.HasSuffix(r.URL.Path, "/tree") && r.URL.Query().Get("page") == "2":
				_, _ = w.Write([]byte(`[]`))
			case strings.HasSuffix(r.URL.Path, "/tree"):
				_, _ = w.Write([]byte(`[{"id":"` + gitTestBlob + `","path":"docs/check.md","type":"blob"}]`))
			default:
				_, _ = w.Write([]byte("# changed under same ref\n"))
			}
		}, ErrGitMarkdownRead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader, spec, _, _ := gitReaderFixture(t, tc.respond)
			_, err := reader.ReadGitMarkdown(context.Background(), spec)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGitMarkdownReaderPaginatesBeforeComplete(t *testing.T) {
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case strings.Contains(r.URL.Path, "/branches/"):
			_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"` + gitTestSHA + `"}}`))
		case strings.HasSuffix(r.URL.Path, "/tree") && r.URL.Query().Get("page") == "1":
			w.Header().Set("X-Next-Page", "2")
			items := make([]gitTreeItem, gitTreePageSize)
			for i := range items {
				items[i] = gitTreeItem{ID: gitTestBlob, Path: "docs/text-" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".txt", Type: "blob"}
			}
			_ = json.NewEncoder(w).Encode(items)
		case strings.HasSuffix(r.URL.Path, "/tree") && r.URL.Query().Get("page") == "2":
			_, _ = w.Write([]byte(`[{"id":"` + gitTestBlob + `","path":"docs/check.md","type":"blob"}]`))
		case strings.HasSuffix(r.URL.Path, "/tree") && r.URL.Query().Get("page") == "3":
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/files/docs/check.md/raw"):
			_, _ = w.Write([]byte(gitTestBody))
		default:
			t.Errorf("unexpected page request: %s", r.URL.String())
			http.NotFound(w, r)
		}
	})
	reader, spec, _, _ := gitReaderFixture(t, handler)
	result, err := reader.ReadGitMarkdown(t.Context(), spec)
	if err != nil || !result.Complete || len(result.Files) != 1 || calls.Load() != 5 {
		t.Fatalf("paginated read failed: %+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestGitMarkdownReaderEmptyCompleteTree(t *testing.T) {
	var calls atomic.Int32
	reader, spec, _, _ := gitReaderFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.Path, "/branches/") {
			_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"` + gitTestSHA + `"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tree") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected raw read on empty tree: %s", r.URL.String())
		http.NotFound(w, r)
	})
	result, err := reader.ReadGitMarkdown(t.Context(), spec)
	if err != nil || !result.Complete || result.CommitSHA != gitTestSHA || len(result.Files) != 0 || calls.Load() != 2 {
		t.Fatalf("complete empty tree must be a proven zero: %+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestGitMarkdownReaderRejectsProjectURLMismatch(t *testing.T) {
	var repositoryCalls atomic.Int32
	reader, spec, vaultCalls, _ := gitReaderFixtureWithProject(t,
		gitSuccessHandler(t, &repositoryCalls), "https://other.example.invalid/team/handbook")
	if _, err := reader.ReadGitMarkdown(t.Context(), spec); !errors.Is(err, ErrGitMarkdownRead) {
		t.Fatalf("project metadata mismatch must fail: %v", err)
	}
	if repositoryCalls.Load() != 0 || vaultCalls.Load() != 1 {
		t.Fatal("mismatched project must fail before branch, tree, and raw reads")
	}
}
