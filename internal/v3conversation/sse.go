package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	replayBatchSize = 64
	// Bound both a single retained payload and a batch before socket writes.
	// Normal model deltas and completion envelopes are well below this limit.
	replayBatchBytes = 4 << 20
	sseWriteTimeout  = 10 * time.Second
)

type replayEvent struct {
	seq int64
	raw []byte
}

func (s *Service) replayBatch(ctx context.Context, reply string, cursor int64) ([]replayEvent, bool, error) {
	return s.replayBatchQuery(ctx, s.db, reply, cursor)
}

type sseReadQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *Service) replayBatchQuery(ctx context.Context, q sseReadQueryer, reply string, cursor int64) ([]replayEvent, bool, error) {
	// NULL prevents an oversized persisted payload from being transferred to
	// the process at all. Never advance the cursor past an omitted event.
	rows, err := q.Query(ctx, `SELECT seq,
		CASE WHEN octet_length(payload::text)<=$4 THEN payload ELSE NULL END
		FROM v3_conversation_events WHERE reply_id=$1 AND seq>$2 ORDER BY seq LIMIT $3`,
		reply, cursor, replayBatchSize, replayBatchBytes)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	batch := make([]replayEvent, 0, replayBatchSize)
	bytes := 0
	for rows.Next() {
		var event replayEvent
		if err := rows.Scan(&event.seq, &event.raw); err != nil {
			return nil, false, err
		}
		if event.raw == nil {
			return nil, false, fmt.Errorf("conversation replay event %d exceeds %d bytes", event.seq, replayBatchBytes)
		}
		if bytes+len(event.raw) > replayBatchBytes {
			return batch, true, nil
		}
		bytes += len(event.raw)
		batch = append(batch, event)
		var marker struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(event.raw, &marker)
		if marker.Type == "message.completed" {
			return batch, false, nil
		}
	}
	return batch, len(batch) == replayBatchSize, rows.Err()
}

type eventStreamWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

func newEventStreamWriter(w http.ResponseWriter, timeout time.Duration) *eventStreamWriter {
	return &eventStreamWriter{w: w, controller: http.NewResponseController(w), timeout: timeout}
}

func (s *eventStreamWriter) deadline() error {
	err := s.controller.SetWriteDeadline(time.Now().Add(s.timeout))
	// In-memory writers have no transport deadline. The production net/http
	// writer supports this operation; wrappers must expose it with Unwrap.
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *eventStreamWriter) flush() error {
	if err := s.deadline(); err != nil {
		return err
	}
	return s.controller.Flush()
}

func (s *eventStreamWriter) writeEvent(event replayEvent) error {
	if err := s.deadline(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(s.w, "id: %d\ndata: %s\n\n", event.seq, event.raw)
	return err
}

func (s *eventStreamWriter) keepalive() error {
	if err := s.deadline(); err != nil {
		return err
	}
	if _, err := io.WriteString(s.w, ": keepalive\n\n"); err != nil {
		return err
	}
	return s.controller.Flush()
}
