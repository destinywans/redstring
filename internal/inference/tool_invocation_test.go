package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func invocationServer(query string, calls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]string{"query": query})
			openChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call1", "function": map[string]any{"name": "read_health", "arguments": string(args)}}}}, "tool_calls")
		} else {
			openChunk(w, map[string]any{"content": "done"}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func invocationRequest(read func(context.Context, string) (Evidence, error)) Request {
	return Request{AttemptID: "audit", ModelID: "test", Messages: []Message{{Role: "user", Content: "check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: read}}}
}

func TestToolInvocationOnlyBracketsActualRead(t *testing.T) {
	const secretQuery = "private-query"
	var modelCalls atomic.Int32
	server := invocationServer(secretQuery, &modelCalls)
	defer server.Close()
	var order []string
	var invocations []ToolInvocation
	answer, err := runtimeFor(t, server, "openai").Run(context.Background(), invocationRequest(func(_ context.Context, q string) (Evidence, error) {
		if q != secretQuery || len(invocations) != 1 || invocations[0].Status != "started" {
			t.Errorf("Read began without a persisted start event: query=%q invocations=%+v", q, invocations)
		}
		order = append(order, "read")
		time.Sleep(3 * time.Millisecond)
		return Evidence{Title: "healthy", Content: "secret-result", TargetID: "service_1"}, nil
	}), func(e Event) error {
		if e.Step != nil && e.Step.Kind == "tool" {
			order = append(order, "step:"+e.Step.Status)
		}
		if e.Invocation != nil {
			if e.Type != "tool_invocation" || e.StepID != "audit-2" || e.Invocation.StepID != e.StepID {
				t.Errorf("invocation linkage = %+v", e)
			}
			invocations = append(invocations, *e.Invocation)
			order = append(order, "invocation:"+e.Invocation.Status)
			encoded, _ := json.Marshal(e.Invocation)
			if strings.Contains(string(encoded), secretQuery) || strings.Contains(string(encoded), "secret-result") {
				t.Errorf("invocation leaked private data: %s", encoded)
			}
		}
		if e.Evidence != nil {
			order = append(order, "evidence")
		}
		return nil
	})
	if err != nil || answer != "done" || modelCalls.Load() != 2 {
		t.Fatalf("answer=%q err=%v modelCalls=%d", answer, err, modelCalls.Load())
	}
	want := []string{"step:running", "invocation:started", "read", "invocation:succeeded", "evidence", "step:succeeded"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", order, want)
	}
	if len(invocations) != 2 || invocations[0].Name != "read_health" || invocations[0].TargetID != "" || invocations[1].TargetID != "service_1" || invocations[1].DurationMS < 1 {
		t.Fatalf("invocations=%+v", invocations)
	}
}

func TestToolInvocationFailureIsRecordedBeforeStepCloses(t *testing.T) {
	var modelCalls atomic.Int32
	server := invocationServer("status", &modelCalls)
	defer server.Close()
	var order []string
	_, _ = runtimeFor(t, server, "openai").Run(context.Background(), invocationRequest(func(context.Context, string) (Evidence, error) {
		order = append(order, "read")
		return Evidence{}, errors.New("private connector failure")
	}), func(e Event) error {
		if e.Invocation != nil {
			order = append(order, e.Invocation.Status)
			encoded, _ := json.Marshal(e.Invocation)
			if strings.Contains(string(encoded), "private connector failure") {
				t.Errorf("invocation leaked failure details: %s", encoded)
			}
		}
		if e.Evidence != nil {
			t.Error("failed Read produced evidence")
		}
		if e.Step != nil && e.Step.Kind == "tool" && e.Step.Status == "failed" {
			order = append(order, "step:failed")
		}
		return nil
	})
	if strings.Join(order, ",") != "started,read,failed,step:failed" {
		t.Fatalf("failure event order=%v", order)
	}
}

func TestRejectedToolCallHasNoInvocation(t *testing.T) {
	var modelCalls, reads atomic.Int32
	server := invocationServer(strings.Repeat("q", 8193), &modelCalls)
	defer server.Close()
	var invocations int
	_, _ = runtimeFor(t, server, "openai").Run(context.Background(), invocationRequest(func(context.Context, string) (Evidence, error) {
		reads.Add(1)
		return Evidence{}, nil
	}), func(e Event) error {
		if e.Invocation != nil {
			invocations++
		}
		return nil
	})
	if reads.Load() != 0 || invocations != 0 {
		t.Fatalf("rejected call: reads=%d invocations=%d", reads.Load(), invocations)
	}
}

func TestToolInvocationCallbackFailureStopsReadOrEvidence(t *testing.T) {
	for _, failAt := range []string{"step", "started", "succeeded"} {
		t.Run(failAt, func(t *testing.T) {
			var modelCalls, reads atomic.Int32
			server := invocationServer("status", &modelCalls)
			defer server.Close()
			sentinel := errors.New("audit persistence failed")
			var evidence int
			_, err := runtimeFor(t, server, "openai").Run(context.Background(), invocationRequest(func(context.Context, string) (Evidence, error) {
				reads.Add(1)
				return Evidence{Content: "result"}, nil
			}), func(e Event) error {
				if e.Evidence != nil {
					evidence++
				}
				if failAt == "step" && e.Step != nil && e.Step.Kind == "tool" && e.Step.Status == "running" || e.Invocation != nil && e.Invocation.Status == failAt {
					return sentinel
				}
				return nil
			})
			wantReads := int32(0)
			if failAt == "succeeded" {
				wantReads = 1
			}
			if !errors.Is(err, sentinel) || reads.Load() != wantReads || evidence != 0 || modelCalls.Load() != 1 {
				t.Fatalf("err=%v reads=%d evidence=%d modelCalls=%d", err, reads.Load(), evidence, modelCalls.Load())
			}
		})
	}
}
