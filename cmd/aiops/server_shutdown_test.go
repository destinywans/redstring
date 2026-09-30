package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShutdownHTTPServerClosesActiveRequestsAfterGracePeriod(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := shutdownHTTPServer(ctx, server.Config); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error=%v; want grace deadline", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active request survived forced close")
	}
}
