package inference

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func mediaFixture() (Media, Media) {
	imageData, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/ZG0AAAAASUVORK5CYII=")
	return Media{Kind: "image", MIME: "image/png", Name: "pixel.png", Data: imageData},
		Media{Kind: "pdf", MIME: "application/pdf", Name: "report.pdf", Data: []byte("%PDF-1.4\nfixture body")}
}

func TestResponsesMediaSurvivesToolFollowup(t *testing.T) {
	image, pdf := mediaFixture()
	imageURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(image.Data)
	pdfURI := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf.Data)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path %q", r.URL.Path)
		}
		var body struct {
			Input     []map[string]any `json:"input"`
			Reasoning map[string]any   `json:"reasoning"`
			Store     bool             `json:"store"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Store || body.Reasoning["effort"] != "high" {
			t.Errorf("lost stateless or reasoning configuration: %+v", body)
		}
		mediaSeen := false
		for _, item := range body.Input {
			parts, ok := item["content"].([]any)
			if !ok {
				continue
			}
			var text, imageFound, pdfFound bool
			for _, raw := range parts {
				part, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				switch part["type"] {
				case "input_text":
					text = part["text"] == "Read these attachments"
				case "input_image":
					imageFound = part["image_url"] == imageURI
				case "input_file":
					pdfFound = part["filename"] == "report.pdf" && part["file_data"] == pdfURI
				}
			}
			if item["role"] == "user" && text && imageFound && pdfFound {
				mediaSeen = true
			}
		}
		if !mediaSeen {
			encoded, _ := json.Marshal(body.Input)
			t.Errorf("user media was dropped from call %d: %s", calls.Load()+1, encoded)
		}
		if calls.Add(1) == 1 {
			responseFrame(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_health"}})
			responseFrame(w, map[string]any{"type": "response.function_call_arguments.done", "output_index": 0, "name": "read_health", "arguments": "{\"query\":\"status\"}"})
			responseFrame(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_health", "arguments": "{\"query\":\"status\"}"}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4}}})
			return
		}
		responseFrame(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant"}})
		responseFrame(w, map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "Both received"})
		responseFrame(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "id": "msg_1", "content": []any{map[string]any{"type": "output_text", "text": "Both received"}}}}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 3}}})
	}))
	defer server.Close()
	runtime, err := New(Config{Providers: []Provider{{ID: "r", Kind: "openai", API: "responses", Model: "fixture", BaseURL: server.URL, ImageInput: true, PDFInput: true, ReasoningKind: "openai_effort", ReasoningLevels: []string{"high"}}}, HTTPClient: server.Client(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if model := runtime.Models()[0]; !model.ImageInput || !model.PDFInput {
		t.Fatalf("media capabilities not advertised: %+v", model)
	}
	answer, err := runtime.Run(context.Background(), Request{ModelID: "r", Reasoning: "high", Messages: []Message{{Role: "user", Content: "Read these attachments", Media: []Media{image, pdf}}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) { return Evidence{Content: "healthy"}, nil }}}}, func(Event) error { return nil })
	if err != nil || answer != "Both received" || calls.Load() != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
}

func TestMediaUnsupportedAndMalformedFailBeforeProvider(t *testing.T) {
	image, pdf := mediaFixture()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, tc := range []struct {
		name  string
		api   string
		image bool
		pdf   bool
		media Media
		want  error
	}{
		{"image not configured", "responses", false, false, image, ErrUnsupportedMedia},
		{"pdf not configured", "responses", true, false, pdf, ErrUnsupportedMedia},
		{"invalid image", "responses", true, false, Media{Kind: "image", MIME: "image/png", Data: []byte("not png")}, ErrInvalidMedia},
		{"invalid pdf", "responses", true, true, Media{Kind: "pdf", MIME: "application/pdf", Name: "report.pdf", Data: []byte("not pdf")}, ErrInvalidMedia},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime, err := New(Config{Providers: []Provider{{ID: "r", Kind: "openai", API: tc.api, Model: "fixture", BaseURL: server.URL, ImageInput: tc.image, PDFInput: tc.pdf}}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.Run(context.Background(), Request{ModelID: "r", Messages: []Message{{Role: "user", Media: []Media{tc.media}}}}, func(Event) error { return nil })
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("provider called for rejected media %d times", calls.Load())
	}
}

func TestMediaCapabilitiesRequireResponsesAndPDFVision(t *testing.T) {
	for _, tc := range []Provider{
		{ID: "bad-chat", Kind: "openai", API: "chat_completions", Model: "fixture", BaseURL: "https://example.test/v1", ImageInput: true},
		{ID: "bad-pdf", Kind: "openai", API: "responses", Model: "fixture", BaseURL: "https://example.test/v1", PDFInput: true},
		{ID: "bad-ollama", Kind: "ollama", API: "ollama", Model: "fixture", BaseURL: "https://example.test", ImageInput: true},
	} {
		if _, err := New(Config{Providers: []Provider{tc}}); err == nil {
			t.Errorf("accepted unsupported media configuration %s", tc.ID)
		}
	}
	image, _ := mediaFixture()
	if err := ValidateMediaForModel(Model{ImageInput: true}, []Media{image, image}); err != nil {
		t.Fatal(err)
	}
	large := image
	large.Data = bytes.Repeat([]byte("x"), MaxInlineMediaBytes+1)
	if err := ValidateMediaForModel(Model{ImageInput: true}, []Media{large}); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("oversized media error=%v", err)
	}
}

func TestResponsesBudgetChecksTextWithoutTreatingImageBase64AsText(t *testing.T) {
	// A normal small PNG is far larger than an 8 KiB text budget after
	// base64 encoding. Its image token cost is not its encoded byte count.
	picture := image.NewRGBA(image.Rect(0, 0, 128, 128))
	seed := uint32(1)
	for i := range picture.Pix {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		picture.Pix[i] = byte(seed)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() < 8192 {
		t.Fatalf("fixture too small to exercise media budget: %d", encoded.Len())
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		responseFrame(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "m", "role": "assistant"}})
		responseFrame(w, map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "Seen"})
		responseFrame(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "id": "m", "content": []any{map[string]any{"type": "output_text", "text": "Seen"}}}}, "usage": map[string]any{"input_tokens": 150, "output_tokens": 1}}})
	}))
	defer server.Close()
	runtime, err := New(Config{Providers: []Provider{{ID: "vision", Kind: "openai", API: "responses", Model: "fixture", BaseURL: server.URL, ImageInput: true, InputBudget: 8192}}, HTTPClient: server.Client(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	media := Media{Kind: "image", MIME: "image/png", Name: "picture.png", Data: encoded.Bytes()}
	answer, err := runtime.Run(context.Background(), Request{ModelID: "vision", Messages: []Message{{Role: "user", Content: "Describe", Media: []Media{media}}}}, func(Event) error { return nil })
	if err != nil || answer != "Seen" || calls.Load() != 1 {
		t.Fatalf("image request answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
	_, err = runtime.Run(context.Background(), Request{ModelID: "vision", Messages: []Message{{Role: "user", Content: strings.Repeat("x", 8192), Media: []Media{media}}}}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "input budget") || calls.Load() != 1 {
		t.Fatalf("text budget bypassed by media: err=%v calls=%d", err, calls.Load())
	}
}

func TestConfiguredMediaModelNamesAreExact(t *testing.T) {
	t.Setenv("OLLAMA_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("OPENAI_MODEL", "vision,text")
	t.Setenv("OPENAI_API", "responses")
	t.Setenv("OPENAI_IMAGE_MODELS", "vision")
	t.Setenv("OPENAI_PDF_MODELS", "vision")
	runner, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	models := runner.Models()
	if len(models) != 2 || !models[0].ImageInput || !models[0].PDFInput || models[1].ImageInput || models[1].PDFInput {
		t.Fatalf("wrong per-model media capabilities: %+v", models)
	}
	t.Setenv("OPENAI_IMAGE_MODELS", "unconfigured")
	if _, err := NewFromEnv(); err == nil || !strings.Contains(err.Error(), "unconfigured") {
		t.Fatalf("unknown media model error=%v", err)
	}
	t.Setenv("OPENAI_IMAGE_MODELS", "vision,vision")
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("duplicate media model was accepted")
	}
}

func TestTextOnlyAdapterRejectsUnrecognizedInlineMedia(t *testing.T) {
	p := &providerModel{provider: Provider{Kind: "openai", API: "chat_completions", Model: "fixture"}}
	request := &model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{genai.NewPartFromBytes([]byte("media"), "image/png")}}}}
	if _, _, err := p.request(request); !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("silent media discard: %v", err)
	}
}
