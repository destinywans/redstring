package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func buildNode(id string) Node {
	return Node{ID: id, Kind: "container", Name: id, Image: "alpine:3.22", Command: []string{"sh", "-ec"}, Args: []string{"printf ok"}}
}
func testRun(nodes ...Node) Run {
	d := Definition{Provider: "native", Nodes: nodes}
	return Run{ID: "abc123", Provider: "native", Snapshot: Pipeline{Digest: hash(d), Definition: d}}
}
func argoConfig(endpoint string) ArgoConfig {
	return ArgoConfig{BaseURL: endpoint, AllowHTTP: true, Namespace: "delivery-runs", ServiceAccount: "aiops-build", Parallelism: 2}
}

func TestDAGRejectsUnsafeDefinitions(t *testing.T) {
	tests := map[string]func() Definition{
		"cycle": func() Definition {
			a, b := buildNode("a"), buildNode("b")
			a.DependsOn = []Dependency{{NodeID: "b"}}
			b.DependsOn = []Dependency{{NodeID: "a"}}
			return Definition{Provider: "native", Nodes: []Node{a, b}}
		},
		"skipped required output": func() Definition {
			a, b := buildNode("a"), buildNode("b")
			a.Outputs = []Output{{Name: "digest", Path: "/workspace/digest"}}
			b.DependsOn = []Dependency{{NodeID: "a", On: "skipped"}}
			b.Inputs = []Input{{Name: "digest", FromNode: "a", Output: "digest"}}
			return Definition{Provider: "native", Nodes: []Node{a, b}}
		},
		"undeclared template": func() Definition {
			a := buildNode("a")
			a.Args = []string{"{{workflow.serviceAccountName}}"}
			return Definition{Provider: "native", Nodes: []Node{a}}
		},
		"dangling artifact": func() Definition {
			a, b := buildNode("a"), buildNode("b")
			b.DependsOn = []Dependency{{NodeID: "a"}}
			b.ArtifactInputs = []FileArtifactInput{{Name: "source", FromNode: "a", Artifact: "missing", Path: "/workspace/source"}}
			return Definition{Provider: "native", Nodes: []Node{a, b}}
		},
		"deploy no approval": func() Definition {
			return Definition{Provider: "native", Nodes: []Node{{ID: "deploy", Kind: "deploy", ApprovalNodeID: "absent"}}}
		},
		"approval can run shell": func() Definition {
			return Definition{Provider: "native", Nodes: []Node{{ID: "approval", Kind: "approval", Image: "alpine", SourceNodeID: "build", TargetID: "prod"}}}
		},
	}
	for name, makeDefinition := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateDefinition(makeDefinition()); err == nil {
				t.Fatal("unsafe definition accepted")
			}
		})
	}
}
func TestArgoCompilerUsesExplicitSuccessAndIndependentArtifacts(t *testing.T) {
	a, b := buildNode("checkout"), buildNode("build")
	a.Artifacts = []FileArtifact{{Name: "source", Path: "/workspace/source"}}
	b.DependsOn = []Dependency{{NodeID: "checkout"}}
	b.ArtifactInputs = []FileArtifactInput{{Name: "source", FromNode: "checkout", Artifact: "source", Path: "/workspace/source"}}
	config := argoConfig("http://example.test")
	config.ArtifactRepositoryName = "artifact-repositories"
	config.ArtifactRepositoryKey = "default-v1"
	workflow, err := CompileWorkflow(testRun(a, b), config)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(workflow)
	text := string(raw)
	for _, required := range []string{`"depends":"checkout.Succeeded"`, `{{tasks.checkout.outputs.artifacts.source}}`, `"artifactRepositoryRef"`, `"runAsNonRoot":true`, `"allowPrivilegeEscalation":false`} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, prohibited := range []string{`"hostPath"`, `"privileged":true`, `"podGC"`, `"ttlStrategy"`} {
		if strings.Contains(text, prohibited) {
			t.Fatalf("unsafe default %s", prohibited)
		}
	}
	config.ArtifactRepositoryName = ""
	if _, err = CompileWorkflow(testRun(a, b), config); !errors.Is(err, ErrUnsupported) {
		t.Fatal("artifact workflow compiled without registered repository")
	}
}
func TestArgoReleaseNodesHaveNoDeploymentCredentialsOrCommands(t *testing.T) {
	a := buildNode("build")
	for _, name := range []string{"commit-sha", "artifact-digest", "artifact-url"} {
		a.Outputs = append(a.Outputs, Output{Name: name, Path: "/workspace/" + name})
	}
	r := testRun(a, Node{ID: "approval", Kind: "approval", SourceNodeID: "build", TargetID: "prod", DependsOn: []Dependency{{NodeID: "build"}}}, Node{ID: "deploy", Kind: "deploy", ApprovalNodeID: "approval", DependsOn: []Dependency{{NodeID: "approval"}}})
	w, err := CompileWorkflow(r, argoConfig("http://example.test"))
	if err != nil {
		t.Fatal(err)
	}
	templates := w["spec"].(map[string]any)["templates"].([]any)
	for _, template := range templates {
		v := template.(map[string]any)
		if v["name"] == "approval" || v["name"] == "deploy" {
			if _, ok := v["suspend"]; !ok {
				t.Fatal("release node not suspended")
			}
			if _, ok := v["container"]; ok {
				t.Fatal("release node contains executable user container")
			}
		}
	}
}

func TestConditionsAndSelectedJoinAreBounded(t *testing.T) {
	source, left, right, join := buildNode("source"), buildNode("left"), buildNode("right"), buildNode("join")
	source.Outputs = []Output{{Name: "route", Path: "/workspace/route"}}
	left.DependsOn = []Dependency{{NodeID: "source"}}
	left.When = &Condition{FromNode: "source", Output: "route", Operator: "eq", Value: "left"}
	right.DependsOn = left.DependsOn
	right.When = &Condition{FromNode: "source", Output: "route", Operator: "ne", Value: "left"}
	join.DependsOn = []Dependency{{NodeID: "left"}, {NodeID: "right"}}
	join.Join = "selected"
	w, err := CompileWorkflow(testRun(source, left, right, join), argoConfig("http://example.test"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(w)
	text := string(raw)
	for _, expected := range []string{"left.Skipped", "right.Skipped", "left.Succeeded || right.Succeeded", "outputs.parameters"} {
		if !strings.Contains(text, expected) {
			t.Fatal("missing condition/join term", expected)
		}
	}
	if strings.Contains(text, "Omitted") {
		t.Fatal("omitted predecessor is not a successful selected branch")
	}
	left.When.Operator = "arbitrary-expression"
	if err = ValidateDefinition(Definition{Provider: "native", Nodes: []Node{source, left, right, join}}); err == nil {
		t.Fatal("arbitrary expression accepted")
	}
}
func TestTemplatesExecuteSourceChecksArtifactsAndHealth(t *testing.T) {
	for _, template := range Templates() {
		if err := ValidateDefinition(template.Definition); err != nil {
			t.Fatalf("%s: %v", template.ID, err)
		}
		if len(template.Definition.Nodes) != 7 {
			t.Fatal("expected source, parallel checks, build, approval, deployment, health")
		}
		build := template.Definition.Nodes[3]
		if len(build.ArtifactInputs) != 3 || len(build.DependsOn) != 3 {
			t.Fatal("build does not consume source and both successful check reports")
		}
		if template.Definition.Nodes[6].Kind != "health" {
			t.Fatal("health verification not exposed")
		}
	}
}
func TestArgoSubmitRecoversAcceptedRequestWithoutDuplicate(t *testing.T) {
	r := testRun(buildNode("build"))
	created, posts := false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" {
			posts++
			created = true
			http.Error(w, "response unavailable", 503)
			return
		}
		if !created {
			http.NotFound(w, req)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "aiops-" + r.ID, "uid": "uid-one", "annotations": map[string]string{"aiops.pipeline-digest": r.Snapshot.Digest, "aiops.operation-id": r.ID}}, "status": map[string]any{"phase": "Running"}})
	}))
	defer server.Close()
	c, err := NewArgoClient(argoConfig(server.URL), "private-token")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		p, e := c.Submit(context.Background(), r)
		if e != nil || p.ExternalUID != "uid-one" {
			t.Fatalf("reconcile failed: %+v %v", p, e)
		}
	}
	if posts != 1 {
		t.Fatalf("submitted %d times", posts)
	}
	r.Projection.ExternalUID = "different-uid"
	if _, err = c.Observe(context.Background(), r); err == nil {
		t.Fatal("reused workflow identity accepted")
	}
}
func TestGitLabGovernanceAndTriggerIdentity(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/1/pipeline" || r.Header.Get("PRIVATE-TOKEN") != "secret" {
			t.Errorf("unexpected request %s", r.URL)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["ref"] != "main" {
			t.Error("wrong ref")
		}
		b, _ := json.Marshal(body)
		if !strings.Contains(string(b), `"key":"AIOPS_RUN_ID","value":"operation-1"`) {
			t.Error("missing recovery identity")
		}
		posts++
		fmt.Fprint(w, `{"id":42,"status":"pending","ref":"main","sha":"abc"}`)
	}))
	defer server.Close()
	config := GitLabConfig{ID: "local", BaseURL: server.URL, AllowHTTP: true, Bindings: []GitLabBinding{{ApplicationID: "app", EnvironmentID: "env", ProjectID: 1, AllowedRefs: []string{"main"}, AllowActions: true, ExternalGovernance: true}}}
	c, err := NewGitLabClient(config, "secret", strings.Repeat("w", 24))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Trigger(context.Background(), 1, "main", "operation-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Trigger(context.Background(), 1, "unregistered", "operation-1"); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unregistered ref accepted")
	}
	if posts != 1 {
		t.Fatal("forbidden trigger performed request")
	}
	config.Bindings[0].ExternalGovernance = false
	if _, err = NewGitLabClient(config, "secret", strings.Repeat("w", 24)); err == nil {
		t.Fatal("claimed product governance for arbitrary external pipeline")
	}
}
func TestProviderDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	c, err := NewGitLabClient(GitLabConfig{ID: "x", BaseURL: source.URL, AllowHTTP: true}, "secret", strings.Repeat("x", 24))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Project(context.Background(), 1); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked {
		t.Fatal("credential followed redirect")
	}
}
func TestS3ContentAddressedRoundTripAndTamper(t *testing.T) {
	objects := map[string][]byte{}
	tamper := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("missing signature")
		}
		if r.Method == "PUT" {
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			return
		}
		b := objects[r.URL.Path]
		if tamper {
			b = []byte("evil")
		}
		w.Write(b)
	}))
	defer server.Close()
	s, err := NewS3Store(S3Config{Endpoint: server.URL, Bucket: "delivery", Region: "us-east-1", AllowHTTP: true, ReadEndpoint: "http://host.lima.internal:28333"}, "access", "secret")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Put(context.Background(), "runs/one", strings.NewReader("data"), 4)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := s.Open(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(body) != "data" {
		t.Fatalf("read %q %v", body, err)
	}
	signed, err := s.SignedReadURL(s.CanonicalURL(a))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(signed)
	if u.Host != "host.lima.internal:28333" || u.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("read endpoint not signed")
	}
	tamper = true
	reader, err = s.Open(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(reader)
	reader.Close()
	if err == nil {
		t.Fatal("corrupted artifact accepted")
	}
	if _, err = s.SignedReadURL("https://attacker.test/delivery/x"); err == nil {
		t.Fatal("signed unregistered origin")
	}
}
func TestArtifactReferencesRejectCredentialsAndPrefixConfusion(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	s := Service{ArtifactURLPrefixes: []string{"registry.local:5000/trusted"}}
	if !s.allowedArtifact("registry.local:5000/trusted/app@"+digest, digest) {
		t.Fatal("valid image rejected")
	}
	for _, raw := range []string{"registry.local:5000/trusted-evil/app@" + digest, "user:password@registry.local:5000/trusted/app@" + digest, "registry.local:5000/trusted/app:latest", "registry.local:5000/trusted/../evil@" + digest} {
		if s.allowedArtifact(raw, digest) {
			t.Fatalf("untrusted reference accepted: %s", raw)
		}
	}
}
