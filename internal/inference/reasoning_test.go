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

func TestOpenAIOutputTokenLimitInBothProtocols(t *testing.T) {
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}}
	for _, tc := range []struct {
		api, key  string
		reasoning bool
	}{{"chat_completions", "max_tokens", false}, {"chat_completions", "max_completion_tokens", true}, {"responses", "max_output_tokens", false}} {
		p := &providerModel{provider: Provider{Kind: "openai", API: tc.api, Model: "test", MaxOutputTokens: 128}}
		if tc.reasoning {
			p.provider.ReasoningKind = "openai_effort"
			p.reasoning = "low"
		}
		body, _, err := p.request(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := body.(map[string]any)[tc.key]; got != 128 {
			t.Fatalf("%s %s=%v, want 128", tc.api, tc.key, got)
		}
	}
}

func TestInferenceCostGuardEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		max  int
	}{
		{"AIOPS_INFERENCE_MAX_CALLS", 8},
		{"OPENAI_MAX_OUTPUT_TOKENS", 4096},
	} {
		for _, raw := range []string{"0", "-1", "bogus", "4097"} {
			t.Setenv(tc.name, raw)
			if _, err := envBoundedInt(tc.name, tc.max, tc.max); err == nil {
				t.Fatalf("%s accepted %q", tc.name, raw)
			}
		}
		t.Setenv(tc.name, "1")
		if n, err := envBoundedInt(tc.name, tc.max, tc.max); err != nil || n != 1 {
			t.Fatalf("%s=1: %d %v", tc.name, n, err)
		}
		t.Setenv(tc.name, "")
		if n, err := envBoundedInt(tc.name, tc.max, tc.max); err != nil || n != tc.max {
			t.Fatalf("%s default: %d %v", tc.name, n, err)
		}
	}

	if _, err := New(Config{MaxCalls: 9}); err == nil {
		t.Fatal("accepted a run budget above eight calls")
	}
	if _, err := New(Config{Providers: []Provider{{ID: "o", Kind: "openai", Model: "test", BaseURL: "https://example.com/v1", MaxOutputTokens: 4097}}}); err == nil {
		t.Fatal("accepted an OpenAI output budget above 4096")
	}
	t.Setenv("OPENAI_MODEL", "test")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_PROVIDER_NAME", "DeepSeek")
	t.Setenv("OLLAMA_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("AIOPS_INFERENCE_MAX_CALLS", "2")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "128")
	runner, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	runtime := runner.(*Runtime)
	if runtime.cfg.MaxCalls != 2 || runtime.cfg.Providers[0].MaxOutputTokens != 128 {
		t.Fatalf("env limits not applied: calls=%d output=%d", runtime.cfg.MaxCalls, runtime.cfg.Providers[0].MaxOutputTokens)
	}
	if got := runtime.Models()[0].Provider; got != "DeepSeek" {
		t.Fatalf("model vendor=%q, want DeepSeek", got)
	}
}
