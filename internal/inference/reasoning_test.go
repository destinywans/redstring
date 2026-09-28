package inference

import (
	"errors"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestReasoningProfilesMustBeExplicit(t *testing.T) {
	base := Provider{ID: "m", Kind: "openai", Model: "gpt", BaseURL: "https://api.example.com/v1"}
	if _, err := New(Config{Providers: []Provider{base}}); err != nil {
		t.Fatal(err)
	}
	if err := validateReasoningChoice(base, "high"); !errors.Is(err, ErrUnsupportedReasoning) {
		t.Fatalf("unconfigured level: %v", err)
	}
	base.ReasoningKind = "openai_effort"
	base.ReasoningLevels = []string{"low", "high"}
	if _, err := New(Config{Providers: []Provider{base}}); err != nil {
		t.Fatal(err)
	}
	if err := validateReasoningChoice(base, "high"); err != nil {
		t.Fatal(err)
	}
	base.ReasoningLevels = []string{"ultra"}
	if _, err := New(Config{Providers: []Provider{base}}); err == nil {
		t.Fatal("invalid effort was accepted")
	}
}

func TestReasoningMappedToProviderRequest(t *testing.T) {
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}}
	openai := &providerModel{provider: Provider{Kind: "openai", Model: "gpt", ReasoningKind: "openai_effort"}, reasoning: "high"}
	body, path, err := openai.request(req)
	if err != nil || path != "/chat/completions" {
		t.Fatalf("OpenAI request: %v %q", err, path)
	}
	fields := body.(map[string]any)
	if fields["reasoning_effort"] != "high" || fields["max_completion_tokens"] != 4096 {
		t.Fatalf("OpenAI effort not mapped: %#v", fields)
	}
	if _, ok := fields["max_tokens"]; ok {
		t.Fatal("reasoning request used incompatible max_tokens")
	}
	claude := &providerModel{provider: Provider{Kind: "claude", Model: "claude", ReasoningKind: "claude_adaptive"}, reasoning: "medium"}
	body, path, err = claude.request(req)
	if err != nil || path != "/messages" {
		t.Fatalf("Claude request: %v %q", err, path)
	}
	fields = body.(map[string]any)
	if fields["thinking"].(map[string]string)["type"] != "adaptive" || fields["output_config"].(map[string]string)["effort"] != "medium" {
		t.Fatalf("Claude effort not mapped: %#v", fields)
	}
}
