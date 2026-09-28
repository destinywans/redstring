package inference

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Run explicitly with AIOPS_REAL_DEEPSEEK=1. It makes one billable request;
// OPENAI_API_KEY is supplied only through the local environment.
func TestDeepSeekRealSmoke(t *testing.T) {
	if os.Getenv("AIOPS_REAL_DEEPSEEK") != "1" {
		t.Skip("opt-in paid provider check")
	}
	if os.Getenv("OPENAI_MODEL") != "deepseek-flash" || os.Getenv("OPENAI_API_KEY") == "" || os.Getenv("AIOPS_INFERENCE_MAX_CALLS") != "1" {
		t.Fatal("DeepSeek model, key, and one-call cap must be configured")
	}
	runner, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	answer, err := runner.Run(ctx, Request{
		ModelID: "openai:deepseek-flash",
		Messages: []Message{{Role: "user", Content: "Reply only with OK."}},
		MaxCalls: 1,
	}, func(Event) error { return nil })
	if err != nil {
		t.Fatalf("DeepSeek streaming adapter failed: %T: %v", err, err)
	}
	if strings.TrimSpace(answer) == "" {
		t.Fatal("DeepSeek returned an empty answer")
	}
}
