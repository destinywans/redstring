package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3models"
)

type v3BindingRunner struct {
	runs int
}

func (*v3BindingRunner) Models() []inference.Model {
	return []inference.Model{{ID: "local:a", API: "responses"}, {ID: "local:b", API: "chat_completions"}}
}

func (r *v3BindingRunner) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	r.runs++
	return "called", nil
}

func TestConfiguredV3ModelRuntimeUsesOneBindingSnapshot(t *testing.T) {
	raw := &v3BindingRunner{}
	cfg := httpapi.V3InstanceConfig{Tier: "nonprod", EnvironmentIDs: []string{"dev", "test"}}
	if got, bindings, err := configuredV3ModelRuntime("", cfg, raw); err != nil || got != raw || bindings != nil {
		t.Fatalf("absent binding file should preserve raw V3 runtime: runner=%T bindings=%v err=%v", got, bindings, err)
	}
	path := filepath.Join(t.TempDir(), "bindings.json")
	data := `{"tier":"nonprod","environments":[{"id":"dev","models":["local:a","local:b"],"default_model":"local:a"},{"id":"test","models":["local:b"],"default_model":"local:b"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, bindings, err := configuredV3ModelRuntime(path, cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	bound, ok := got.(*v3models.BoundRunner)
	if !ok || bound.Bindings() != bindings || bindings.DefaultModel("test") != "local:b" {
		t.Fatalf("V3 runtime and admin view must share binding snapshot: runner=%T bindings=%p", got, bindings)
	}
	if _, err := got.Run(context.Background(), inference.Request{EnvironmentID: "test", ModelID: "local:a"}, nil); !errors.Is(err, v3models.ErrModelBindingDenied) || raw.runs != 0 {
		t.Fatalf("cross-environment run reached raw provider: err=%v runs=%d", err, raw.runs)
	}
	if _, err := got.Run(context.Background(), inference.Request{EnvironmentID: "test", ModelID: "local:b"}, nil); err != nil || raw.runs != 1 {
		t.Fatalf("bound run failed: err=%v runs=%d", err, raw.runs)
	}
}

func TestMalformedV3ModelBindingsFailBeforeV3Configuration(t *testing.T) {
	t.Setenv("AIOPS_V3_MODEL_BINDINGS_FILE", filepath.Join(t.TempDir(), "missing.json"))
	err := configureV3(context.Background(), &store.Store{}, &v3BindingRunner{}, nil, nil, nil, &v3Instance{}, httpapi.V3InstanceConfig{Tier: "nonprod", EnvironmentIDs: []string{"dev"}}, operations.Config{})
	if err == nil || !strings.Contains(err.Error(), "load V3 model bindings") {
		t.Fatalf("invalid binding path reached V3 registration: %v", err)
	}
}
