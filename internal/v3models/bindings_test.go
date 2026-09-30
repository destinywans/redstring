package v3models

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/inference"
)

type bindingTestRunner struct {
	models []inference.Model
	runs   int
}

func (r *bindingTestRunner) Models() []inference.Model { return r.models }

func (r *bindingTestRunner) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	r.runs++
	return "called", nil
}

func (r *bindingTestRunner) ProbeModel(_ context.Context, id string) (inference.ModelProbeResult, error) {
	return inference.ModelProbeResult{Model: id, Status: "unsupported", ErrorCode: "local_credential_free_only"}, nil
}

func writeBindingConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model-bindings.json")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bindingFixtureRunner() *bindingTestRunner {
	return &bindingTestRunner{models: []inference.Model{
		{ID: "local:a", API: "chat_completions", ReasoningLevels: []string{"low", "high"}},
		{ID: "local:b", API: "responses"},
	}}
}

const validBindingConfig = `{"tier":"nonprod","environments":[{"id":"dev","models":["local:a","local:b"],"default_model":"local:b"},{"id":"test","models":["local:a"],"default_model":"local:a"}]}`

func TestLoadBindingsAndBoundRunner(t *testing.T) {
	runtime := bindingFixtureRunner()
	bindings, err := LoadBindings(writeBindingConfig(t, validBindingConfig), "nonprod", []string{"dev", "test"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !bindings.Allowed("dev", "local:b") || bindings.Allowed("test", "local:b") || bindings.Allowed("absent", "local:a") || bindings.Allowed("dev", "") {
		t.Fatal("exact environment/model boundary failed")
	}
	if got := bindings.DefaultModel("dev"); got != "local:b" {
		t.Fatalf("default=%q", got)
	}
	if got := bindings.DefaultModel("absent"); got != "" {
		t.Fatalf("unknown environment default=%q", got)
	}
	models := bindings.ModelsForEnvironment("dev")
	if len(models) != 2 || models[0].ID != "local:a" || models[1].ID != "local:b" {
		t.Fatalf("unexpected scoped models: %+v", models)
	}
	models[0].ID = "mutated"
	models[0].ReasoningLevels[0] = "mutated"
	if again := bindings.ModelsForEnvironment("dev"); again[0].ID != "local:a" || again[0].ReasoningLevels[0] != "low" {
		t.Fatal("returned model list mutates binding snapshot")
	}
	wrapper := NewBoundRunner(runtime, bindings)
	if len(wrapper.Models()) != 2 {
		t.Fatal("wrapper did not forward process catalog")
	}
	for _, req := range []inference.Request{
		{ModelID: "local:a"},
		{EnvironmentID: "test", ModelID: "local:b"},
		{EnvironmentID: "absent", ModelID: "local:a"},
		{EnvironmentID: "dev", ModelID: ""},
	} {
		if _, err := wrapper.Run(context.Background(), req, nil); !errors.Is(err, ErrModelBindingDenied) {
			t.Fatalf("request %+v: expected binding denial, got %v", req, err)
		}
	}
	if runtime.runs != 0 {
		t.Fatal("denied requests reached provider runner")
	}
	answer, err := wrapper.Run(context.Background(), inference.Request{EnvironmentID: "dev", ModelID: "local:b"}, nil)
	if err != nil || answer != "called" || runtime.runs != 1 {
		t.Fatalf("bound run result=%q err=%v runs=%d", answer, err, runtime.runs)
	}
	probe, err := wrapper.ProbeModel(context.Background(), "local:a")
	if err != nil || probe.Model != "local:a" || probe.Status != "unsupported" {
		t.Fatalf("probe forwarding: %+v %v", probe, err)
	}
}

func TestLoadBindingsRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"missing environment", `{"tier":"nonprod","environments":[{"id":"dev","models":["local:a"],"default_model":"local:a"}]}`},
		{"duplicate environment", `{"tier":"nonprod","environments":[{"id":"dev","models":["local:a"],"default_model":"local:a"},{"id":"dev","models":["local:b"],"default_model":"local:b"}]}`},
		{"unknown environment", `{"tier":"nonprod","environments":[{"id":"dev","models":["local:a"],"default_model":"local:a"},{"id":"prod","models":["local:b"],"default_model":"local:b"}]}`},
		{"unknown model", strings.Replace(validBindingConfig, `"local:b"`, `"remote:unknown"`, 1)},
		{"duplicate model", strings.Replace(validBindingConfig, `["local:a","local:b"]`, `["local:a","local:a"]`, 1)},
		{"default outside list", strings.Replace(validBindingConfig, `"default_model":"local:b"`, `"default_model":"remote:unknown"`, 1)},
		{"empty model list", strings.Replace(validBindingConfig, `["local:a","local:b"]`, `[]`, 1)},
		{"unknown field", strings.Replace(validBindingConfig, `"tier":"nonprod"`, `"tier":"nonprod","api_key":"secret"`, 1)},
		{"case variant root field", strings.Replace(validBindingConfig, `"tier":"nonprod"`, `"tier":"nonprod","Tier":"production"`, 1)},
		{"case variant environment field", strings.Replace(validBindingConfig, `"models":["local:a","local:b"]`, `"models":["local:a","local:b"],"Models":["local:b"]`, 1)},
		{"duplicate field", strings.Replace(validBindingConfig, `"tier":"nonprod"`, `"tier":"nonprod","tier":"nonprod"`, 1)},
		{"nested duplicate field", strings.Replace(validBindingConfig, `"id":"dev"`, `"id":"dev","id":"dev"`, 1)},
		{"malformed", `{"tier":`},
		{"trailing data", validBindingConfig + ` {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadBindings(writeBindingConfig(t, tc.json), "nonprod", []string{"dev", "test"}, bindingFixtureRunner()); err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
		})
	}
	if _, err := LoadBindings(writeBindingConfig(t, validBindingConfig), "production", []string{"dev", "test"}, bindingFixtureRunner()); err == nil {
		t.Fatal("tier mismatch was accepted")
	}
	if _, err := LoadBindings(writeBindingConfig(t, validBindingConfig), "nonprod", []string{"dev", "dev"}, bindingFixtureRunner()); err == nil {
		t.Fatal("duplicate instance environment was accepted")
	}
	if _, err := LoadBindings(writeBindingConfig(t, validBindingConfig), "nonprod", []string{"dev", "test"}, &bindingTestRunner{models: []inference.Model{{ID: "local:a"}, {ID: "local:a"}}}); err == nil {
		t.Fatal("duplicate runtime model was accepted")
	}
	if bindings, err := LoadBindings("", "nonprod", []string{"dev"}, bindingFixtureRunner()); err != nil || bindings != nil {
		t.Fatalf("absent file should leave binding unavailable: %v %+v", err, bindings)
	}
}
