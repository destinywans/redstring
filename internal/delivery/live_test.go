package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/store"
)

// These opt-in checks only target the explicitly named isolated aiops-test lab.
// They build synthetic source and retain workflows for audit, never deploy.
func liveConfig(t *testing.T) (map[string]string, map[string]any) {
	t.Helper()
	dir := os.Getenv("AIOPS_DELIVERY_LIVE_LAB")
	if dir == "" {
		t.Skip("set AIOPS_DELIVERY_LIVE_LAB for the isolated aiops-test lab")
	}
	if filepath.Base(dir) != "delivery" || filepath.Base(filepath.Dir(dir)) != ".local" || filepath.Base(filepath.Dir(filepath.Dir(dir))) != "aiops-test" {
		t.Fatal("not the isolated aiops-test lab")
	}
	var conn map[string]string
	raw, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &conn) != nil {
		t.Fatal("invalid lab connections")
	}
	if conn["workflows_url"] != "http://127.0.0.1:2746" || conn["workflows_namespace"] != "delivery-runs" || conn["gitlab_url"] != "http://127.0.0.1:28080" || conn["s3_endpoint"] != "http://127.0.0.1:28333" {
		t.Fatal("unexpected live endpoints")
	}
	var demo map[string]any
	raw, err = os.ReadFile(filepath.Join(dir, "gitlab-demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &demo) != nil {
		t.Fatal("invalid demo metadata")
	}
	return conn, demo
}
func TestLiveNativeImageBuild(t *testing.T)  { liveNativeBuild(t, 0) }
func TestLiveNativeBinaryBuild(t *testing.T) { liveNativeBuild(t, 2) }
func liveNativeBuild(t *testing.T, template int) {
	conn, demo := liveConfig(t)
	token, err := readCredential(conn["workflows_token_file"])
	if err != nil {
		t.Fatal(err)
	}
	config := argoConfig(conn["workflows_url"])
	config.ArtifactRepositoryName = "artifact-repositories"
	config.ArtifactRepositoryKey = "default-v1"
	client, err := NewArgoClient(config, token)
	if err != nil {
		t.Fatal(err)
	}
	nodes := Templates()[template].Definition.Nodes[:4]
	for n := range nodes {
		node := &nodes[n]
		for i := range node.Inputs {
			switch node.Inputs[i].Name {
			case "repository":
				node.Inputs[i].Value = "http://host.docker.internal:28080/root/aiops-demo.git"
			case "revision":
				if node.Inputs[i].FromNode == "" {
					node.Inputs[i].Value = demo["commit"].(string)
				}
			case "image":
				node.Inputs[i].Value = "host.docker.internal:25000/aiops-demo"
			case "buildkit":
				node.Inputs[i].Value = "tcp://host.docker.internal:21234"
			case "insecure-registry":
				node.Inputs[i].Value = "true"
			}
		}
	}
	run := testRun(nodes...)
	run.ID = "live-" + store.ID()
	if old := os.Getenv("AIOPS_DELIVERY_REUSE_WORKFLOW"); old != "" {
		if !strings.HasPrefix(old, "aiops-live-") {
			t.Fatal("only own retained lab workflow may be reused")
		}
		run.ID = strings.TrimPrefix(old, "aiops-")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	projection, err := client.Submit(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	run.Projection = projection
	t.Logf("workflow=%s uid=%s", projection.ExternalID, projection.ExternalUID)
	for !terminal(projection.Status) {
		select {
		case <-ctx.Done():
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = client.Cancel(cleanup, run)
			stop()
			t.Fatal("native build exceeded deadline")
		case <-time.After(5 * time.Second):
		}
		projection, err = client.Observe(ctx, run)
		if err != nil {
			t.Log("transient engine observation; continuing reconciliation")
			projection.Status = "running"
			continue
		}
	}
	if projection.Status != "succeeded" {
		logs, _ := client.Logs(ctx, run, "")
		t.Logf("engine log: %s", logs.Text)
		t.Fatalf("native build %s: %s", projection.Status, projection.Message)
	}
	var result NodeResult
	for _, n := range projection.Nodes {
		if n.Name == "build" {
			result = n
		}
	}
	if !digestPattern.MatchString(result.Outputs["artifact-digest"]) || result.Outputs["commit-sha"] != demo["commit"] {
		t.Fatalf("invalid build provenance: %+v", result)
	}
	object, err := NewS3Store(S3Config{Endpoint: conn["s3_endpoint"], Bucket: conn["s3_bucket"], Region: "us-east-1", AllowHTTP: true}, conn["s3_access_key"], conn["s3_secret_key"])
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) == 0 {
		t.Fatal("no archived build artifact")
	}
	archivedLogs := false
	for _, a := range result.Artifacts {
		if a.Bucket != conn["s3_bucket"] {
			t.Fatal("unexpected artifact bucket")
		}
		empty := sha256.Sum256(nil)
		resp, e := object.request(ctx, "GET", a.Key, hex.EncodeToString(empty[:]), nil, 0)
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if e != nil || (n == 0 && a.Name != "main-logs") {
			t.Fatal("archive empty or unreadable")
		}
		if a.Name == "package" && "sha256:"+hex.EncodeToString(h.Sum(nil)) != result.Outputs["artifact-digest"] {
			t.Fatal("binary archive digest mismatch")
		}
		if a.Name == "main-logs" {
			archivedLogs = true
		}
		t.Logf("archived_artifact=%s bytes=%d", a.Name, n)
	}
	if !archivedLogs {
		t.Fatal("logs not archived independently in S3")
	}
	logs, err := client.Logs(ctx, run, "")
	if err != nil || strings.TrimSpace(logs.Text) == "" {
		t.Fatal("native logs unavailable")
	}
	if template == 0 {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:25000/v2/aiops-demo/manifests/"+result.Outputs["artifact-digest"], nil)
		req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json")
		resp, e := newHTTPClient().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal("built image missing in registry")
		}
	}
	t.Logf("verified_native_build template=%s commit=%s digest=%s artifacts=%d log_bytes=%d", Templates()[template].ID, result.Outputs["commit-sha"], result.Outputs["artifact-digest"], len(result.Artifacts), len(logs.Text))
	evidence, _ := json.MarshalIndent(map[string]any{"run": run, "projection": projection, "artifact_digest": result.Outputs["artifact-digest"], "verified_s3_logs": archivedLogs}, "", "  ")
	path := filepath.Join(os.TempDir(), "aiops-delivery-native-"+Templates()[template].ID+".json")
	if err = os.WriteFile(path, evidence, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence=%s", path)
}
