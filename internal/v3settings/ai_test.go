package v3settings

import (
	"net/http"
	"os"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/inference"
)

func TestModelsRejectsNativeOllamaUntilCatalogSupportsIt(t *testing.T) {
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{{
		ID: "ollama:test", Kind: "ollama", API: "ollama", Model: "test",
		BaseURL: "http://127.0.0.1:11434",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(nil, runtime)
	w := httptest.NewRecorder()
	service.ModelsHandler(w, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"model_catalog_unavailable"`) {
		t.Fatalf("expected explicit unsupported protocol response, got %d %s", w.Code, w.Body.String())
	}
}

func TestModelCatalogToolsFollowV3Allowlist(t *testing.T) {
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{
		{ID: "openai:enabled", Kind: "openai", Model: "enabled", BaseURL: "http://127.0.0.1:8080"},
		{ID: "openai:disabled", Kind: "openai", Model: "disabled", BaseURL: "http://127.0.0.1:8080"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(nil, runtime)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	for _, test := range []struct {
		name      string
		allowlist map[string]bool
		expected  []bool
	}{
		{name: "no V3 tools enabled", expected: []bool{false, false}},
		{name: "one V3 model enabled", allowlist: map[string]bool{"openai:enabled": true}, expected: []bool{true, false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.ToolModels = test.allowlist
			items, err := service.modelCatalogItems(request, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != len(test.expected) {
				t.Fatalf("got %d models, want %d", len(items), len(test.expected))
			}
			for i, want := range test.expected {
				if got := items[i]["tools"]; got != want {
					t.Errorf("model %q tools = %v, want %t", items[i]["id"], got, want)
				}
			}
		})
	}
	if !runtime.Models()[1].Tools {
		t.Fatal("V3 catalog must not change legacy model capabilities")
	}
}

// PA-251: `make init` writes .env through scripts/dev-init.sh. Its default
// model must be one the V3 catalog can list; native Ollama made the whole
// catalog 503, so a fresh setup had no usable model at all.
func TestDevInitDefaultModelServesV3Catalog(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/dev-init.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	start := strings.Index(script, "cat > .env <<EOF\n")
	end := strings.Index(script, "\nEOF\n")
	if start < 0 || end < start {
		t.Fatal("dev-init.sh no longer writes .env from a heredoc")
	}
	for _, prefix := range []string{"OLLAMA", "OPENAI", "CLAUDE"} {
		for _, suffix := range []string{"_MODEL", "_API", "_BASE_URL"} {
			t.Setenv(prefix+suffix, "")
		}
	}
	for _, line := range strings.Split(script[start+len("cat > .env <<EOF\n"):end], "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.HasPrefix(key, "OLLAMA_") && !strings.Contains(value, "$") {
			t.Setenv(key, value)
		}
	}
	runner, err := inference.NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	// The catalog itself, as in TestModelCatalogToolsFollowV3Allowlist: the
	// handler goes on to read the user's preferences, which need a store.
	items, err := New(nil, runner).modelCatalogItems(httptest.NewRequest(http.MethodGet, "/api/v1/models", nil), "")
	if err != nil {
		t.Fatalf("the make init default model is refused by the V3 catalog: %v", err)
	}
	if len(items) != 1 || items[0]["api"] != "chat_completions" || items[0]["available"] != true {
		t.Fatalf("the make init default model is not listed as an available chat_completions model: %v", items)
	}
}
