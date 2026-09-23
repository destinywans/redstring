package delivery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveGitLabActions(t *testing.T) {
	conn, demo := liveConfig(t)
	token, err := readCredential(filepath.Join(os.Getenv("AIOPS_DELIVERY_LIVE_LAB"), "gitlab-delivery-token"))
	if err != nil {
		t.Fatal(err)
	}
	const ref = "codex-delivery-verification"
	c, err := NewGitLabClient(GitLabConfig{ID: "lab", BaseURL: conn["gitlab_url"], AllowHTTP: true, Bindings: []GitLabBinding{{ApplicationID: "lab", EnvironmentID: "lab", ProjectID: 1, AllowedRefs: []string{ref}, AllowActions: true, ExternalGovernance: true, ManualJobs: []string{"manual_gate", "failure_probe", "wait_probe"}}}}, token, strings.Repeat("local-webhook-", 3))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var branch map[string]any
	if _, err = c.json(ctx, "GET", projectPath(1)+"/repository/branches/"+ref, nil, &branch); err != nil {
		if _, err = c.json(ctx, "POST", projectPath(1)+"/repository/branches", map[string]string{"branch": ref, "ref": demo["commit"].(string)}, &branch); err != nil {
			t.Fatal(err)
		}
	}
	var commit map[string]any
	_, err = c.json(ctx, "POST", projectPath(1)+"/repository/commits", map[string]any{"branch": ref, "commit_message": "Controlled delivery adapter verification [skip ci]", "actions": []map[string]string{{"action": "update", "file_path": ".gitlab-ci.yml", "content": verificationCI}}}, &commit)
	if err != nil { // A previous run may already have exactly this file.
		var file struct {
			Content string `json:"content"`
		}
		if _, e := c.json(ctx, "GET", projectPath(1)+"/repository/files/.gitlab-ci.yml?ref="+ref, nil, &file); e != nil {
			t.Fatal(err)
		}
	}
	operationID := "live-actions-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	p, err := c.Trigger(ctx, 1, ref, operationID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("gitlab_pipeline=%d", p.ID)
	// Reconcile an acknowledged external write as if its HTTP response had
	// been lost. Recovery is read-only and must return the same pipeline.
	recovered, err := c.RecoverTrigger(ctx, 1, operationID)
	if err != nil || recovered.ID != p.ID {
		t.Fatalf("trigger recovery did not find original pipeline: %v", err)
	}
	manual := waitGitLabJob(t, ctx, c, p.ID, "manual_gate", "manual", 0)
	if _, err = c.Command(ctx, 1, p.ID, "play", manual.ID); err != nil {
		t.Fatal(err)
	}
	waitGitLabJob(t, ctx, c, p.ID, "manual_gate", "success", 0)
	logs, err := c.Trace(ctx, 1, manual.ID, 0)
	if err != nil || !strings.Contains(logs.Text, "manual gate verified") {
		t.Fatal("manual job logs missing")
	}
	failure := waitGitLabJob(t, ctx, c, p.ID, "failure_probe", "manual", 0)
	if _, err = c.Command(ctx, 1, p.ID, "play", failure.ID); err != nil {
		t.Fatal(err)
	}
	waitGitLabJob(t, ctx, c, p.ID, "failure_probe", "failed", 0)
	if _, err = c.Command(ctx, 1, p.ID, "retry", 0); err != nil {
		t.Fatal(err)
	}
	// GitLab keeps a retried manual job manual; it requires a new explicit play.
	retryManual := waitGitLabJob(t, ctx, c, p.ID, "failure_probe", "manual", failure.ID)
	if _, err = c.Command(ctx, 1, p.ID, "play", retryManual.ID); err != nil {
		t.Fatal(err)
	}
	retried := waitGitLabJob(t, ctx, c, p.ID, "failure_probe", "failed", failure.ID)
	q, err := c.Trigger(ctx, 1, ref, "live-cancel-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err != nil {
		t.Fatal(err)
	}
	wait := waitGitLabJob(t, ctx, c, q.ID, "wait_probe", "manual", 0)
	if _, err = c.Command(ctx, 1, q.ID, "play", wait.ID); err != nil {
		t.Fatal(err)
	}
	waitGitLabJob(t, ctx, c, q.ID, "wait_probe", "running", 0)
	if _, err = c.Command(ctx, 1, q.ID, "cancel", 0); err != nil {
		t.Fatal(err)
	}
	waitGitLabJob(t, ctx, c, q.ID, "wait_probe", "canceled", 0)
	t.Logf("verified_gitlab_actions pipeline=%d manual_job=%d original_failed_job=%d retried_job=%d cancelled_pipeline=%d cancelled_job=%d trace_bytes=%d", p.ID, manual.ID, failure.ID, retried.ID, q.ID, wait.ID, len(logs.Text))
}
func waitGitLabJob(t *testing.T, ctx context.Context, c *GitLabClient, pipeline int64, name, status string, except int64) GitLabJob {
	t.Helper()
	for {
		jobs, err := c.Jobs(ctx, 1, pipeline, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs.Items {
			if j.Name == name && j.Status == status && j.ID != except {
				return j
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(fmt.Sprintf("waiting for %s %s exceeded deadline", name, status))
		case <-time.After(2 * time.Second):
		}
	}
}

const verificationCI = `image: alpine:3.23.6
stages: [verify]
verify:
  stage: verify
  script: ["echo isolated adapter verification"]
manual_gate:
  stage: verify
  when: manual
  allow_failure: true
  script: ["echo manual gate verified"]
failure_probe:
  stage: verify
  when: manual
  allow_failure: false
  script: ["echo intentional failure fixture", "exit 1"]
wait_probe:
  stage: verify
  when: manual
  allow_failure: true
  script: ["echo cancellation fixture started", "sleep 180", "echo cancellation did not happen"]
`
