package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type dropTriggerResponse struct {
	inner http.RoundTripper
	posts int
}

func (d *dropTriggerResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := d.inner.RoundTrip(r)
	if err == nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pipeline") {
		d.posts++
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("controlled response loss after GitLab accepted trigger")
	}
	return response, err
}
func TestLiveGitLabLostTriggerResponse(t *testing.T) {
	conn, _ := liveConfig(t)
	token, err := readCredential(filepath.Join(os.Getenv("AIOPS_DELIVERY_LIVE_LAB"), "gitlab-delivery-token"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewGitLabClient(GitLabConfig{ID: "lab", BaseURL: conn["gitlab_url"], AllowHTTP: true, Bindings: []GitLabBinding{{ApplicationID: "lab", EnvironmentID: "lab", ProjectID: 1, AllowedRefs: []string{"codex-delivery-verification"}, AllowActions: true, ExternalGovernance: true}}}, token, strings.Repeat("w", 24))
	if err != nil {
		t.Fatal(err)
	}
	transport := &dropTriggerResponse{inner: http.DefaultTransport}
	c.http.Transport = transport
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	op := "response-loss-" + time.Now().UTC().Format("20060102T150405.000000000")
	if _, err = c.Trigger(ctx, 1, "codex-delivery-verification", op); err == nil {
		t.Fatal("response loss not injected")
	}
	recovered, err := c.RecoverTrigger(ctx, 1, op)
	if err != nil {
		t.Fatal(err)
	}
	if transport.posts != 1 || recovered.ID == 0 {
		t.Fatal("trigger was repeated")
	}
	if _, err = c.Command(ctx, 1, recovered.ID, "cancel", 0); err != nil {
		t.Fatal(err)
	}
	t.Logf("lost_trigger_reconciled pipeline=%d post_count=%d; cleanup cancellation requested", recovered.ID, transport.posts)
}
func TestLiveArchivedLogRetrieval(t *testing.T) {
	conn, _ := liveConfig(t)
	path := os.Getenv("AIOPS_DELIVERY_ARCHIVE_PROOF")
	if path == "" {
		t.Skip("AIOPS_DELIVERY_ARCHIVE_PROOF requires own completed lab workflow evidence")
	}
	var proof struct {
		Run        Run        `json:"run"`
		Projection Projection `json:"projection"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &proof) != nil {
		t.Fatal("invalid archive proof")
	}
	if !strings.HasPrefix(proof.Run.ID, "live-") || proof.Projection.Status != "succeeded" {
		t.Fatal("not completed own lab workflow")
	}
	token, err := readCredential(conn["workflows_token_file"])
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewArgoClient(argoConfig(conn["workflows_url"]), token)
	if err != nil {
		t.Fatal(err)
	}
	object, err := NewS3Store(S3Config{Endpoint: conn["s3_endpoint"], Bucket: conn["s3_bucket"], Region: "us-east-1", AllowHTTP: true}, conn["s3_access_key"], conn["s3_secret_key"])
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Argo: client, Artifacts: object}
	proof.Run.Projection = proof.Projection
	var node string
	for _, n := range proof.Projection.Nodes {
		if n.Name == "build" {
			node = n.ID
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	logs, err := s.Logs(ctx, proof.Run, node, 0)
	if err != nil || logs.Source != "s3_archive" || logs.Text == "" {
		t.Fatalf("archived retrieval failed: %v source=%s", err, logs.Source)
	}
	t.Logf("archived_log_retrieval workflow=%s source=%s bytes=%d", proof.Projection.ExternalID, logs.Source, len(logs.Text))
}
