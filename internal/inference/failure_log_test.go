package inference

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// PA-107: a failed model call leaves one structured server log record with the
// failure class, upstream status and elapsed time, and never the credential,
// the prompt or the upstream response body.

const (
	logTestKey    = "sk-test-SECRET-KEY-0123456789"
	logTestPrompt = "PROMPT-MARKER-restart payments pod"
	logTestBody   = "UPSTREAM-BODY-MARKER-do-not-log"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func runFailing(t *testing.T, ctx context.Context, baseURL string, client *http.Client, timeout time.Duration) error {
	t.Helper()
	r, err := New(Config{
		Providers:  []Provider{{ID: "ds", Kind: "openai", DisplayProvider: "deepseek", Model: "deepseek-chat", BaseURL: baseURL, APIKey: logTestKey}},
		HTTPClient: client, Timeout: timeout, MaxCalls: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(ctx, Request{
		RunID: "run-1", AttemptID: "attempt-107", BranchID: "node-1", ModelID: "ds",
		Messages: []Message{{Role: "user", Content: logTestPrompt}},
	}, func(Event) error { return nil })
	if err == nil {
		t.Fatal("expected the model call to fail")
	}
	return err
}

func assertFailureLog(t *testing.T, logs string, want ...string) {
	t.Helper()
	if strings.Count(logs, "model call failed") != 1 {
		t.Fatalf("want exactly one failure record, got:\n%s", logs)
	}
	for _, w := range append(want, "provider=deepseek", "model=deepseek-chat", "attempt_id=attempt-107", "elapsed_ms=") {
		if !strings.Contains(logs, w) {
			t.Errorf("log missing %q:\n%s", w, logs)
		}
	}
	for _, leaked := range []string{logTestKey, logTestPrompt, logTestBody, "Bearer"} {
		if strings.Contains(logs, leaked) {
			t.Errorf("log leaks %q:\n%s", leaked, logs)
		}
	}
}

func TestModelFailureLogUpstreamStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		class  string
	}{
		{"server error", 500, "upstream_5xx"},
		{"rate limited", 429, "rate_limited"},
		{"bad request", 400, "http_status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":{"type":"overloaded_error","code":"server_busy","message":"` + logTestBody + `"}}`))
			}))
			defer s.Close()
			runFailing(t, context.Background(), s.URL, s.Client(), 5*time.Second)
			assertFailureLog(t, logs.String(), "class="+tc.class, "status="+strconv.Itoa(tc.status), "upstream_error_code=server_busy", "upstream_error_type=overloaded_error")
		})
	}
}

func TestModelFailureLogTimeout(t *testing.T) {
	logs := captureLogs(t)
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	defer close(release)
	runFailing(t, context.Background(), s.URL, s.Client(), 200*time.Millisecond)
	assertFailureLog(t, logs.String(), "class=timeout")
}

func TestModelFailureLogConnectionRefused(t *testing.T) {
	logs := captureLogs(t)
	s := httptest.NewServer(http.NotFoundHandler())
	url, client := s.URL, s.Client()
	s.Close()
	runFailing(t, context.Background(), url, client, 5*time.Second)
	assertFailureLog(t, logs.String(), "class=connect")
}

func TestModelFailureLogCanceled(t *testing.T) {
	logs := captureLogs(t)
	started, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reading the body lets the server notice the client hanging up.
		io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	runFailing(t, ctx, s.URL, s.Client(), 5*time.Second)
	assertFailureLog(t, logs.String(), "class=canceled")
}

func TestModelFailureLogUnparsableResponse(t *testing.T) {
	logs := captureLogs(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {not json " + logTestBody + "\n\n"))
	}))
	defer s.Close()
	runFailing(t, context.Background(), s.URL, s.Client(), 5*time.Second)
	assertFailureLog(t, logs.String(), "class=parse", "status=200")
}

func TestModelSuccessWritesNoFailureLog(t *testing.T) {
	logs := captureLogs(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		openChunk(w, map[string]any{"content": "ok"}, "stop")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer s.Close()
	r := runtimeFor(t, s, "openai")
	if _, err := r.Run(context.Background(), Request{AttemptID: "a", ModelID: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "model call failed") {
		t.Fatalf("unexpected failure record:\n%s", logs)
	}
}
