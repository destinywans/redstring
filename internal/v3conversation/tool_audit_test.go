package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
	calls       int
	reportUsage bool
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
