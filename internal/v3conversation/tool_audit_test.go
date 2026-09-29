package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
)

func TestToolAuditRejectsMalformedEventsBeforeWrite(t *testing.T) {
	s := New(nil, nil, Options{})
	for _, event := range []*inference.ToolInvocation{
		nil,
		{Name: "read_service_profile", StepID: "s1", Status: "unknown"},
		{Name: "read_service_profile", StepID: "s1", Status: "started", DurationMS: 1},
		{Name: "read_service_profile", StepID: "", Status: "started"},
		{Name: "read_service_profile", StepID: "s1", Status: "failed", DurationMS: -1},
		{Name: "read_service_profile", StepID: "s1", Status: "started", TargetID: "resource_1"},
		{Name: "read_service_profile", StepID: "s1", Status: "succeeded", TargetID: "unsafe target with spaces"},
	} {
		if err := s.appendToolAudit(context.Background(), "owner", "test", "c", "r", "r", event); !errors.Is(err, errToolInvocation) {
			t.Fatalf("event %#v: %v", event, err)
		}
	}
}

type toolAuditRuntime struct {
	calls             int
	reportUsage       bool
	duplicateEvidence bool
}

func (*toolAuditRuntime) Models() []inference.Model { return nil }
func (r *toolAuditRuntime) Run(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
	r.calls++
	if len(req.Tools) != 1 || req.Tools[0].Name != "read_service_profile" {
		return "", errors.New("expected one scoped read tool")
	}
	if r.reportUsage {
		step := inference.Event{Type: "step", Step: &inference.Step{ID: "model-tool", Kind: "model", Status: "succeeded", ToolCalls: []string{"read_service_profile"}, Usage: &inference.Usage{InputTokens: 20, OutputTokens: 3}}}
		if err := emit(step); err != nil {
			return "", err
		}
		if err := emit(step); err != nil { // duplicate terminal snapshot must not double-count
			return "", err
		}
	}
	start := inference.ToolInvocation{Name: req.Tools[0].Name, StepID: "step-1", Status: "started"}
	if err := emit(inference.Event{Type: "tool_invocation", StepID: start.StepID, Invocation: &start}); err != nil {
		return "", err
	}
	evidence, err := req.Tools[0].Read(ctx, "service-key")
	end := inference.ToolInvocation{Name: start.Name, StepID: start.StepID, Status: "succeeded", DurationMS: 3}
	if err != nil {
		end.Status = "failed"
	} else {
		end.TargetID = evidence.TargetID
	}
	if emitErr := emit(inference.Event{Type: "tool_invocation", StepID: end.StepID, Invocation: &end}); emitErr != nil {
		return "", emitErr
	}
	if err != nil {
		return "", err
	}
	if err := emit(inference.Event{Type: "evidence", StepID: end.StepID, Evidence: &evidence}); err != nil {
		return "", err
	}
	if r.duplicateEvidence {
		if err := emit(inference.Event{Type: "evidence", StepID: end.StepID, Evidence: &evidence}); err != nil {
			return "", err
		}
	}
	if err := emit(inference.Event{Type: "delta", Delta: "answer"}); err != nil {
		return "", err
	}
	if r.reportUsage {
		if err := emit(inference.Event{Type: "step", Step: &inference.Step{ID: "model-final", Kind: "model", Status: "succeeded", Usage: &inference.Usage{InputTokens: 21, OutputTokens: 2}}}); err != nil {
			return "", err
		}
	}
	return "answer", nil
}

func TestToolAuditPrecedesReadAndPersistsOutcome(t *testing.T) {
	db := isolatedConversationDB(t)
	runtime := &toolAuditRuntime{}
	checks := 0
	svc := New(db, runtime, Options{
		RefreshSession: func(_ context.Context, token string) (core.User, error) {
			if token != "test-session" {
				t.Fatalf("unexpected session token")
			}
			return core.User{ID: "persisted-owner"}, nil
		},
		AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
			checks++
			if owner != "persisted-owner" || env != "test" {
				t.Fatalf("unexpected scope %q %q", owner, env)
			}
			return nil
		},
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if owner != "persisted-owner" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
		ToolsForTurn: func(_ context.Context, owner, env, modelID string) ([]inference.EvidenceTool, error) {
			if owner != "persisted-owner" || env != "test" || modelID != "model" {
				t.Fatalf("tool factory received request scope instead of persisted scope")
			}
			return []inference.EvidenceTool{{Name: "read_service_profile", Read: func(ctx context.Context, key string) (inference.Evidence, error) {
				if key != "service-key" {
					t.Fatal("unexpected query")
				}
				var started bool
				if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management_audit WHERE action='v3.conversation.tool' AND target='read_service_profile' AND outcome='accepted')`).Scan(&started); err != nil || !started {
					t.Fatalf("tool called without durable started audit: %v", err)
				}
				return inference.Evidence{Kind: "service", Title: "Profile", Content: "bounded data", Source: "service catalog", TargetID: "resource_1"}, nil
			}}}, nil
		},
	})
	svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model", Tools: true}, "", "test-session")
	if runtime.calls != 1 || checks < 3 {
		t.Fatalf("model calls=%d authorization checks=%d", runtime.calls, checks)
	}
	rows, err := db.Query(context.Background(), `SELECT outcome,changes FROM management_audit WHERE action='v3.conversation.tool' ORDER BY outcome`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]map[string]any{}
	for rows.Next() {
		var outcome string
		var raw []byte
		if err := rows.Scan(&outcome, &raw); err != nil {
			t.Fatal(err)
		}
		var changes map[string]any
		if err := json.Unmarshal(raw, &changes); err != nil {
			t.Fatal(err)
		}
		got[outcome] = changes
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["accepted"]["environment_id"] != "test" || got["succeeded"]["step_id"] != "step-1" || got["succeeded"]["resource_id"] != "resource_1" {
		t.Fatalf("audit rows: %#v", got)
	}
	var actorKind, action, target, outcome string
	var receipt []byte
	if err := db.QueryRow(context.Background(), `SELECT actor_kind,action,target,outcome,changes FROM management_audit WHERE action='v3.conversation.evidence'`).Scan(&actorKind, &action, &target, &outcome, &receipt); err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err := json.Unmarshal(receipt, &source); err != nil {
		t.Fatal(err)
	}
	if actorKind != "agent" || action != "v3.conversation.evidence" || target != "read_service_profile" || outcome != "succeeded" || len(source) != 5 ||
		source["environment_id"] != "test" || source["conversation_id"] != "c" || source["reply_id"] != "r" || source["step_id"] != "step-1" || source["source"] != "service catalog" {
		t.Fatalf("invalid evidence receipt: %q %q %q %q %#v", actorKind, action, target, outcome, source)
	}
	if strings.Contains(string(receipt), "bounded data") || strings.Contains(string(receipt), "service-key") || strings.Contains(string(receipt), "Profile") {
		t.Fatalf("evidence receipt leaked query or content: %s", receipt)
	}
	var runMissing bool
	var parts []byte
	if err := db.QueryRow(context.Background(), `SELECT run IS NULL,parts FROM v3_conversation_messages WHERE id='a'`).Scan(&runMissing, &parts); err != nil {
		t.Fatal(err)
	}
	if !runMissing || string(parts) == "" || !json.Valid(parts) {
		t.Fatalf("provider omitted usage: run_missing=%v parts=%s", runMissing, parts)
	}
	var saved []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(parts, &saved); err != nil {
		t.Fatal(err)
	}
	for _, part := range saved {
		if part.Type == "tool_calls" {
			t.Fatal("tool card must not claim zero usage when the provider omitted it")
		}
	}
	var completion []byte
	if err := db.QueryRow(context.Background(), `SELECT payload FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='message.completed'`).Scan(&completion); err != nil {
		t.Fatal(err)
	}
	var completed map[string]json.RawMessage
	if err := json.Unmarshal(completion, &completed); err != nil {
		t.Fatal(err)
	}
	if _, hasRun := completed["run"]; hasRun {
		t.Fatalf("completion must omit run when provider usage is unknown: %s", completion)
	}
}

func TestEvidenceReceiptRejectsFailedReadAndDuplicateEvent(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed read", true: "duplicate evidence"}[duplicate], func(t *testing.T) {
			db := isolatedConversationDB(t)
			runtime := &toolAuditRuntime{duplicateEvidence: duplicate}
			svc := New(db, runtime, Options{
				RefreshSession:            func(context.Context, string) (core.User, error) { return core.User{ID: "persisted-owner"}, nil },
				AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
				AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
				ToolsForTurn: func(context.Context, string, string, string) ([]inference.EvidenceTool, error) {
					return []inference.EvidenceTool{{Name: "read_service_profile", Read: func(context.Context, string) (inference.Evidence, error) {
						if !duplicate {
							return inference.Evidence{}, errors.New("secret provider failure")
						}
						return inference.Evidence{Title: "Secret title", Content: "Secret content", Source: "service catalog"}, nil
					}}}, nil
				},
			})
			svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model", Tools: true}, "", "test-session")
			var count int
			if err := db.QueryRow(context.Background(), `SELECT count(*) FROM management_audit WHERE action='v3.conversation.evidence'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if duplicate {
				want = 1
			}
			if count != want {
				t.Fatalf("evidence receipts=%d want=%d", count, want)
			}
			var state string
			if err := db.QueryRow(context.Background(), `SELECT state FROM v3_conversation_replies WHERE id='r'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "failed" {
				t.Fatalf("reply state=%q", state)
			}
		})
	}
}

func TestEvidenceReceiptRejectsUnsafeSource(t *testing.T) {
	s := New(nil, nil, Options{})
	invocation := inference.ToolInvocation{Name: "read_service_profile", StepID: "step-1", Status: "succeeded"}
	for _, source := range []string{"", "line\nbreak", string([]byte{0xff}), strings.Repeat("x", 257)} {
		if err := s.appendEvidenceReceipt(context.Background(), "owner", "test", "c", "r", "r", invocation, source); !errors.Is(err, errEvidenceReceipt) {
			t.Fatalf("source %q: %v", source, err)
		}
	}
}

func TestEvidenceReceiptRequiresPersistedOwnerAndEnvironment(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='running' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	svc := New(db, nil, Options{})
	invocation := inference.ToolInvocation{Name: "read_service_profile", StepID: "step-1", Status: "succeeded"}
	if err := svc.appendToolAudit(ctx, "persisted-owner", "test", "c", "r", "r", &invocation); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ owner, env string }{{"other-owner", "test"}, {"persisted-owner", "other-env"}} {
		if err := svc.appendEvidenceReceipt(ctx, scope.owner, scope.env, "c", "r", "r", invocation, "service catalog"); !errors.Is(err, errEvidenceReceipt) {
			t.Fatalf("mismatched scope %+v: %v", scope, err)
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.conversation.evidence'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("mismatched scope wrote %d evidence receipts", count)
	}
}

func TestToolCardUsesProviderUsageAndReadDuration(t *testing.T) {
	db := isolatedConversationDB(t)
	runtime := &toolAuditRuntime{reportUsage: true}
	svc := New(db, runtime, Options{
		RefreshSession: func(_ context.Context, token string) (core.User, error) {
			if token != "test-session" {
				return core.User{}, ErrForbidden
			}
			return core.User{ID: "persisted-owner"}, nil
		},
		AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
			if owner != "persisted-owner" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if owner != "persisted-owner" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
		ToolsForTurn: func(_ context.Context, _, _, _ string) ([]inference.EvidenceTool, error) {
			return []inference.EvidenceTool{{Name: "read_service_profile", Read: func(context.Context, string) (inference.Evidence, error) {
				return inference.Evidence{Kind: "service", Title: "Profile", Content: "bounded data", Source: "service catalog", TargetID: "resource_1"}, nil
			}}}, nil
		},
	})
	svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model", Tools: true}, "", "test-session")
	var rawRun, rawParts []byte
	if err := db.QueryRow(context.Background(), `SELECT run,parts FROM v3_conversation_messages WHERE id='a'`).Scan(&rawRun, &rawParts); err != nil {
		t.Fatal(err)
	}
	var run struct {
		Tokens int `json:"tokens"`
	}
	if err := json.Unmarshal(rawRun, &run); err != nil || run.Tokens != 46 {
		t.Fatalf("provider run usage: %s %v", rawRun, err)
	}
	var parts []struct {
		Type     string  `json:"type"`
		Tokens   int     `json:"tokens"`
		Duration float64 `json:"duration_s"`
	}
	if err := json.Unmarshal(rawParts, &parts); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, part := range parts {
		if part.Type == "tool_calls" {
			found = true
			if part.Tokens != 23 || part.Duration != 0.003 {
				t.Fatalf("tool usage or measured duration: %+v", part)
			}
		}
	}
	if !found {
		t.Fatalf("missing tool card: %s", rawParts)
	}
}
