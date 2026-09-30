package v3conversation

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type blockedSSEWriter struct {
	*httptest.ResponseRecorder
	entered  chan struct{}
	release  chan struct{}
	deadline time.Time
}

func (w *blockedSSEWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *blockedSSEWriter) Write(p []byte) (int, error) {
	select {
	case <-w.entered:
	default:
		close(w.entered)
	}
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func TestSSEBlockedClientDoesNotRetainOnlyPoolConnection(t *testing.T) {
	svc, request := sseRegressionService(t)
	ctx := context.Background()
	if _, err := svc.db.Exec(ctx, `UPDATE v3_conversation_replies SET state='completed' WHERE id='r';
		INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES
		('c','r','{"type":"text.delta","delta":"hello"}'),
		('c','r','{"type":"message.completed"}')`); err != nil {
		t.Fatal(err)
	}
	cfg := svc.db.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc.db = pool
	w := &blockedSSEWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	req := request("0")
	req.URL.RawQuery = "reply_id=r"
	go func() { svc.events(w, req); close(done) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not enter client write")
	}
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var value int
	if err := pool.QueryRow(queryCtx, "SELECT 1").Scan(&value); err != nil || value != 1 {
		t.Fatalf("slow SSE client retained the only DB connection: value=%d err=%v", value, err)
	}
	if w.deadline.IsZero() {
		t.Fatal("network write has no deadline")
	}
}

func TestSSEReplayBatchesBoundBytesWithoutSkippingEvents(t *testing.T) {
	svc, _ := sseRegressionService(t)
	ctx := context.Background()
	if _, err := svc.db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		SELECT 'c','r',jsonb_build_object('type','text.delta','delta',repeat('x',1500000))
		FROM generate_series(1,4)`); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	count := 0
	for {
		batch, more, err := svc.replayBatch(ctx, "r", cursor)
		if err != nil {
			t.Fatal(err)
		}
		bytes := 0
		for _, event := range batch {
			if event.seq <= cursor {
				t.Fatal("replay order regressed")
			}
			cursor = event.seq
			bytes += len(event.raw)
			count++
		}
		if bytes > replayBatchBytes {
			t.Fatalf("batch is %d bytes", bytes)
		}
		if !more {
			break
		}
	}
	if count != 4 {
		t.Fatalf("byte-bounded replay lost events: %d", count)
	}
	if _, err := svc.db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r',jsonb_build_object('type','text.delta','delta',repeat('x',$1)))`, replayBatchBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.replayBatch(ctx, "r", cursor); err == nil {
		t.Fatal("oversized event was not rejected")
	}
}

type failingSSEFlushWriter struct {
	*httptest.ResponseRecorder
	flushes int
}

func (w *failingSSEFlushWriter) FlushError() error {
	w.flushes++
	if w.flushes == 2 {
		return errors.New("client disconnected")
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestSSEFlushFailureStopsReplay(t *testing.T) {
	svc, request := sseRegressionService(t)
	ctx := context.Background()
	if _, err := svc.db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		SELECT 'c','r',jsonb_build_object('type','text.delta','delta',n::text)
		FROM generate_series(1,130) AS n`); err != nil {
		t.Fatal(err)
	}
	w := &failingSSEFlushWriter{ResponseRecorder: httptest.NewRecorder()}
	req := request("0")
	req.URL.RawQuery = "reply_id=r"
	svc.events(w, req)
	if count := strings.Count(w.Body.String(), "data: "); count != replayBatchSize || w.flushes != 2 {
		t.Fatalf("continued after failed network flush: events=%d flushes=%d", count, w.flushes)
	}
}

// Exercise net/http's actual transport deadline, rather than a writer mock.
func TestEventStreamDeadlineDisconnectsNonReadingClient(t *testing.T) {
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := newEventStreamWriter(w, 100*time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		if err := stream.flush(); err != nil {
			done <- err
			return
		}
		raw := []byte(strings.Repeat("x", 2<<20))
		for i := range 64 {
			if err := stream.writeEvent(replayEvent{seq: int64(i + 1), raw: raw}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.(*net.TCPConn).SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("non-reading client did not hit transport timeout: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("network write remained blocked")
	}
}
