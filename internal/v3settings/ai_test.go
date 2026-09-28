package v3settings

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/inference"
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
