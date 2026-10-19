package v3conversation

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/jackc/pgx/v5"
)

func TestRecoverInterruptedPreservesDurablePartsInEventOrder(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	part := func(kind string, fields map[string]any) map[string]any {
		fields["type"] = kind
		return fields
	}
	firstStatus := part("status", map[string]any{"text": "Checking service", "detail": "First evidence"})
	firstCalls := part("tool_calls", map[string]any{"summary": "First tool", "calls": []any{}, "tokens": 4, "duration_s": 0.1})
	chart := part("chart", map[string]any{"title": "CPU", "unit": "%", "labels": []string{"now"},
		"series": []map[string]any{{"name": "cpu", "values": []int{82}}}})
	secondStatus := part("status", map[string]any{"text": "Checking deployment", "detail": "Second evidence"})
	secondCalls := part("tool_calls", map[string]any{"summary": "Second tool", "calls": []any{}, "tokens": 8, "duration_s": 0.2})
	events := []map[string]any{
		{"type": "message.started", "message_id": "a"},
		{"type": "part.added", "message_id": "a", "part": map[string]any{"type": "text", "markdown": ""}},
		{"type": "part.added", "message_id": "a", "part": firstStatus},
		{"type": "text.delta", "message_id": "a", "index": 0, "delta": "partial "},
		{"type": "part.added", "message_id": "a", "part": firstCalls},
		{"type": "part.added", "message_id": "a", "part": chart},
		{"type": "part.added", "message_id": "a", "part": secondStatus},
		{"type": "text.delta", "message_id": "a", "index": 0, "delta": "answer"},
		{"type": "part.added", "message_id": "a", "part": secondCalls},
	}
	for _, event := range events {
		if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES('c','r',$1)`, marshal(event)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='running',lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil }})
	if err := svc.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}

	var raw []byte
	var state string
	if err := db.QueryRow(ctx, `SELECT parts,state FROM v3_conversation_messages WHERE id='a'`).Scan(&raw, &state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("assistant state=%q, want failed", state)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"type": "text", "markdown": "partial answer"}, firstStatus, firstCalls, chart, secondStatus, secondCalls,
		// Lease recovery has no request, so the interruption status is in the
		// default language with a stable code (PA-60).
		replyFailurePart(false, true)}
	// Normalize number representations through JSON, as PostgreSQL returns
	// jsonb numbers into interface values as float64.
	var normalizedWant []map[string]any
	if err := json.Unmarshal(marshal(want), &normalizedWant); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, normalizedWant) {
		t.Fatalf("recovered parts = %#v, want %#v", got, normalizedWant)
	}
	var terminalCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='message.completed'`).Scan(&terminalCount); err != nil {
		t.Fatal(err)
	}
	if terminalCount != 1 {
		t.Fatalf("terminal SSE events=%d, want 1", terminalCount)
	}
}

type suffixAfterDeltaRuntime struct{}

func (suffixAfterDeltaRuntime) Models() []inference.Model { return nil }
func (suffixAfterDeltaRuntime) Run(_ context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	if err := emit(inference.Event{Type: "delta", Delta: "visible"}); err != nil {
		return "", err
	}
	return "visible but unsent", nil
}

type failedDeltaRuntime struct{}

func (failedDeltaRuntime) Models() []inference.Model { return nil }
func (failedDeltaRuntime) Run(_ context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	if err := emit(inference.Event{Type: "delta", Delta: "visible"}); err != nil {
		return "", err
	}
	if err := emit(inference.Event{Type: "delta", Delta: " but unsent"}); err != nil {
		return "", err
	}
	return "visible but unsent", nil
}

type emptyFinalRuntime struct{}

func (emptyFinalRuntime) Models() []inference.Model { return nil }
func (emptyFinalRuntime) Run(_ context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	if err := emit(inference.Event{Type: "delta", Delta: "visible partial answer"}); err != nil {
		return "", err
	}
	return "", nil
}

func TestEmptyFinalRetainsStreamedTextInFailedDetail(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	svc := New(db, emptyFinalRuntime{}, Options{
		RefreshSession:            func(context.Context, string) (core.User, error) { return core.User{ID: "persisted-owner"}, nil },
		AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
		AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
	})
	svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", false)
	var raw []byte
	var state string
	if err := db.QueryRow(ctx, `SELECT parts,state FROM v3_conversation_messages WHERE id='a'`).Scan(&raw, &state); err != nil {
		t.Fatal(err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || len(parts) != 2 || parts[0]["markdown"] != "visible partial answer" || parts[1]["type"] != "status" {
		t.Fatalf("failed detail lost durable delta: state=%q parts=%#v", state, parts)
	}
}

func TestFailedSuffixEventDoesNotPersistUnsentText(t *testing.T) {
	assertFailedDeltaDoesNotPersistUnsentText(t, suffixAfterDeltaRuntime{})
}

func TestFailedStreamEventDoesNotPersistUnsentText(t *testing.T) {
	assertFailedDeltaDoesNotPersistUnsentText(t, failedDeltaRuntime{})
}

func assertFailedDeltaDoesNotPersistUnsentText(t *testing.T, runtime inference.Runner) {
	t.Helper()
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		CREATE FUNCTION reject_unsent_suffix() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.payload->>'type'='text.delta' AND NEW.payload->>'delta'=' but unsent' THEN
				RAISE EXCEPTION 'simulated append failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER reject_unsent_suffix BEFORE INSERT ON v3_conversation_events
		FOR EACH ROW EXECUTE FUNCTION reject_unsent_suffix();`)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, runtime, Options{
		RefreshSession:            func(context.Context, string) (core.User, error) { return core.User{ID: "persisted-owner"}, nil },
		AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
		AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
	})
	svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", false)
	var raw []byte
	var state string
	if err := db.QueryRow(ctx, `SELECT parts,state FROM v3_conversation_messages WHERE id='a'`).Scan(&raw, &state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("assistant state=%q, want failed", state)
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[0]["markdown"] != "visible" || parts[1]["type"] != "status" {
		var replyErr string
		_ = db.QueryRow(ctx, `SELECT error FROM v3_conversation_replies WHERE id='r'`).Scan(&replyErr)
		t.Fatalf("failed reply persisted an unsent suffix: %#v; reply error=%s", parts, replyErr)
	}
	var deltas []string
	rows, err := db.Query(ctx, `SELECT payload->>'delta' FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='text.delta' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var delta string
		if err := rows.Scan(&delta); err != nil {
			t.Fatal(err)
		}
		deltas = append(deltas, delta)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deltas, []string{"visible"}) {
		t.Fatalf("durable deltas=%#v, want only visible", deltas)
	}
}
