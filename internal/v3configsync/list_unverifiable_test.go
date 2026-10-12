package v3configsync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// PA-103: a proxy in front of GitLab now and then fails to connect and
// answers 504. A read is tried once more; a write is not, its caller
// reconciles.
func TestGitLabRetriesAFailedReadOnce(t *testing.T) {
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writes.Add(1)
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		if reads.Add(1) == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		respondJSON(w, 200, []MR{})
	}))
	defer server.Close()
	c := clientFor(t, server.URL)
	if list, err := c.ListMerged(context.Background(), 10); err != nil || len(list) != 0 || reads.Load() != 2 {
		t.Fatalf("read after one gateway timeout: %v %v reads=%d", list, err, reads.Load())
	}
	status, _, err := c.call(context.Background(), http.MethodPost, c.projectPath()+"/merge_requests", nil, map[string]string{}, 201, nil)
	if status != 504 || !errors.Is(err, ErrGitLabUnavailable) || writes.Load() != 1 {
		t.Fatalf("write: status=%d err=%v writes=%d", status, err, writes.Load())
	}
	// Twice in a row: the read fails as unavailable, naming its step.
	reads.Store(-10)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	_, _, _, err = c.ReadMergedManifest(context.Background(), 12)
	var check *MergedCheckError
	if !errors.As(err, &check) || check.IID != 12 || check.Step != "merge_request" || check.Status != 504 ||
		!errors.Is(err, ErrGitLabUnavailable) || errors.Is(err, ErrUntrusted) {
		t.Fatalf("unavailable read: %#v %v", check, err)
	}
}

// PA-103: an unverifiable candidate is logged with its MR, step, GitLab's
// status and the reason, never a token or a manifest.
func TestConfigSyncLogsUnverifiableCandidate(t *testing.T) {
	fixtures := fourKindFixtures(t)
	s := seedReadFixtures(t, fixtures)
	first := fixtures[0]
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: fixtures,
		readErrs: map[int64]error{first.mr.IID: &MergedCheckError{IID: first.mr.IID, Step: "versions", Status: 504, Err: &StatusError{Status: 504}}}}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	api := configServer(t, s, repo, "production")
	w := configRequest(api, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	items := decodeSyncList(t, w.Body.Bytes()).Items
	if w.Code != 200 || len(items) != len(fixtures) || items[0].Status != "unverifiable" || items[0].Reason != "gitlab_unavailable" {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	for _, it := range items[1:] {
		if it.Status != "merged" {
			t.Fatalf("other candidates: %+v", items)
		}
	}
	line := logs.String()
	for _, want := range []string{"mr_iid=" + strconv.FormatInt(first.mr.IID, 10), "step=versions", "gitlab_status=504", "reason=gitlab_unavailable"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "admin-token") || strings.Contains(line, first.title) || strings.Contains(line, "signature") {
		t.Fatalf("log carries a token or manifest content: %s", line)
	}
}
