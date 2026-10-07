package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestRuntimeRequestOutputBudgetCannotRaiseDeploymentLimit(t *testing.T) {
	var observed []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		observed = append(observed, input.MaxTokens)
		w.Header().Set("Content-Type", "text/event-stream")
		openChunk(w, map[string]any{"content": "ok"}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	runtime, err := New(Config{Providers: []Provider{{ID: "test", Kind: "openai", Model: "local", BaseURL: server.URL, MaxOutputTokens: 128}}, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{32, 512, 0, -1} {
		out, err := runtime.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "check"}}, MaxOutputTokens: limit}, func(Event) error { return nil })
		if limit < 0 {
			if err == nil {
				t.Fatal("negative limit accepted")
			}
			continue
		}
		if err != nil || out != "ok" {
			t.Fatalf("limit %d: %q %v", limit, out, err)
		}
	}
	if fmt.Sprint(observed) != "[32 128 128]" {
		t.Fatalf("provider limits: %v", observed)
	}
	if runtime.cfg.Providers[0].MaxOutputTokens != 128 {
		t.Fatal("request mutated shared provider configuration")
	}
}

func TestNonOpenAIProvidersHonorOutputBudget(t *testing.T) {
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("check", genai.RoleUser)}}
	for _, kind := range []string{"claude", "ollama"} {
		p := &providerModel{provider: Provider{Kind: kind, Model: "local", MaxOutputTokens: 96}}
		if kind == "ollama" {
			p.provider.API = "ollama"
		}
		body, _, err := p.request(req)
		if err != nil {
			t.Fatal(err)
		}
		fields := body.(map[string]any)
		actual := fields["max_tokens"]
		if kind == "ollama" {
			actual = fields["options"].(map[string]any)["num_predict"]
		}
		if actual != 96 {
			t.Fatalf("%s output budget: %v", kind, actual)
		}
	}
}
