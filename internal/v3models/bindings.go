package v3models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/wans/aiops-canvas/internal/inference"
)

const maxModelBindingsFileBytes = 1 << 20

// ErrModelBindingDenied means that a V3 run did not carry an exact configured
// environment/model pair. A queued run must never silently select a replacement.
var ErrModelBindingDenied = errors.New("model is not bound to the environment")

type modelBindingsFile struct {
	Tier         string `json:"tier"`
	Environments []struct {
		ID           string   `json:"id"`
		Models       []string `json:"models"`
		DefaultModel string   `json:"default_model"`
	} `json:"environments"`
}

type environmentBinding struct {
	models       []inference.Model
	allowed      map[string]struct{}
	defaultModel string
}

// Bindings is an immutable, process-local snapshot. Its model list comes only
// from the configured inference runtime, never from client input or discovery.
type Bindings struct {
	environments map[string]environmentBinding
}

// LoadBindings reads one explicit, credential-free configuration file. An
// absent path keeps the V3 model admin interface unavailable; callers decide
// whether existing unbound V3 routes remain active during rollout.
func LoadBindings(path, tier string, environmentIDs []string, runtime inference.Runner) (*Bindings, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	if runtime == nil || (tier != "nonprod" && tier != "production") || len(environmentIDs) == 0 {
		return nil, errors.New("invalid model binding instance configuration")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("model bindings file is unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxModelBindingsFileBytes+1))
	if err != nil || len(data) > maxModelBindingsFileBytes {
		return nil, errors.New("model bindings file is unreadable or too large")
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return nil, errors.New("model bindings contain duplicate or invalid JSON fields")
	}
	if err := rejectNonCanonicalJSONFields(data); err != nil {
		return nil, errors.New("model bindings contain invalid field names")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config modelBindingsFile
	if err := decoder.Decode(&config); err != nil {
		return nil, errors.New("invalid model bindings JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid trailing model bindings data")
	}
	if config.Tier != tier {
		return nil, errors.New("model bindings tier does not match instance")
	}
	knownEnvironments := make(map[string]struct{}, len(environmentIDs))
	for _, id := range environmentIDs {
		if id == "" || id != strings.TrimSpace(id) {
			return nil, errors.New("invalid instance environment ID")
		}
		if _, duplicate := knownEnvironments[id]; duplicate {
			return nil, errors.New("duplicate instance environment ID")
		}
		knownEnvironments[id] = struct{}{}
	}
	if len(config.Environments) != len(knownEnvironments) {
		return nil, errors.New("model bindings must cover every instance environment exactly once")
	}
	configured := make(map[string]inference.Model)
	for _, model := range runtime.Models() {
		if model.ID == "" || model.ID != strings.TrimSpace(model.ID) {
			return nil, errors.New("invalid configured model ID")
		}
		if _, duplicate := configured[model.ID]; duplicate {
			return nil, errors.New("duplicate configured model ID")
		}
		configured[model.ID] = model
	}
	bindings := &Bindings{environments: make(map[string]environmentBinding, len(environmentIDs))}
	for _, entry := range config.Environments {
		if _, known := knownEnvironments[entry.ID]; !known {
			return nil, errors.New("model binding references an unknown environment")
		}
		if _, duplicate := bindings.environments[entry.ID]; duplicate {
			return nil, errors.New("duplicate model binding environment")
		}
		if len(entry.Models) == 0 || entry.DefaultModel == "" {
			return nil, errors.New("model binding requires models and an explicit default")
		}
		binding := environmentBinding{allowed: make(map[string]struct{}, len(entry.Models)), models: make([]inference.Model, 0, len(entry.Models)), defaultModel: entry.DefaultModel}
		for _, id := range entry.Models {
			model, exists := configured[id]
			if !exists {
				return nil, errors.New("model binding references an unconfigured model")
			}
			if _, duplicate := binding.allowed[id]; duplicate {
				return nil, errors.New("duplicate model binding model")
			}
			binding.allowed[id] = struct{}{}
			binding.models = append(binding.models, cloneModel(model))
		}
		if _, allowed := binding.allowed[entry.DefaultModel]; !allowed {
			return nil, errors.New("default model is not bound to its environment")
		}
		bindings.environments[entry.ID] = binding
	}
	return bindings, nil
}

// Allowed tests one exact pair. Unknown environments and empty IDs deny.
func (b *Bindings) Allowed(environmentID, modelID string) bool {
	if b == nil || environmentID == "" || modelID == "" {
		return false
	}
	binding, ok := b.environments[environmentID]
	if !ok {
		return false
	}
	_, ok = binding.allowed[modelID]
	return ok
}

// ModelsForEnvironment preserves configured order and returns detached copies.
func (b *Bindings) ModelsForEnvironment(environmentID string) []inference.Model {
	if b == nil {
		return nil
	}
	binding, ok := b.environments[environmentID]
	if !ok {
		return nil
	}
	models := make([]inference.Model, len(binding.models))
	for i, model := range binding.models {
		models[i] = cloneModel(model)
	}
	return models
}

func (b *Bindings) DefaultModel(environmentID string) string {
	if b == nil {
		return ""
	}
	return b.environments[environmentID].defaultModel
}

func cloneModel(model inference.Model) inference.Model {
	model.ReasoningLevels = append([]string(nil), model.ReasoningLevels...)
	return model
}

// BoundRunner enforces the same snapshot used to render the V3 admin view.
// Models remains the process catalog for compatibility; environment-aware
// callers must use Bindings.ModelsForEnvironment for selection and display.
type BoundRunner struct {
	next     inference.Runner
	bindings *Bindings
}

func NewBoundRunner(next inference.Runner, bindings *Bindings) *BoundRunner {
	return &BoundRunner{next: next, bindings: bindings}
}

// Bindings identifies the exact policy enforced by this runner. The V3 admin
// handler can require pointer identity with its view policy before serving.
func (r *BoundRunner) Bindings() *Bindings {
	if r == nil {
		return nil
	}
	return r.bindings
}

func (r *BoundRunner) Models() []inference.Model {
	if r == nil || r.next == nil {
		return nil
	}
	return r.next.Models()
}

func (r *BoundRunner) Run(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
	if r == nil || r.next == nil || !r.bindings.Allowed(req.EnvironmentID, req.ModelID) {
		return "", ErrModelBindingDenied
	}
	return r.next.Run(ctx, req, emit)
}

// ProbeModel forwards the existing bounded metadata check. Callers must first
// validate the environment/model pair; the probe API itself has no environment.
func (r *BoundRunner) ProbeModel(ctx context.Context, id string) (inference.ModelProbeResult, error) {
	if r != nil && r.next != nil {
		if source, ok := r.next.(interface {
			ProbeModel(context.Context, string) (inference.ModelProbeResult, error)
		}); ok {
			return source.ProbeModel(ctx, id)
		}
	}
	return inference.ModelProbeResult{}, inference.ErrModelNotConfigured
}

// rejectDuplicateJSONFields rejects duplicate keys at every object depth.
// The strict decoder subsequently validates the expected schema and types.
func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// encoding/json matches struct fields without regard to case. Require the
// published spelling so a case variant cannot override a reviewed setting.
func rejectNonCanonicalJSONFields(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return errors.New("invalid root object")
	}
	for key := range root {
		if key != "tier" && key != "environments" {
			return errors.New("invalid root field")
		}
	}
	var environments []map[string]json.RawMessage
	if err := json.Unmarshal(root["environments"], &environments); err != nil {
		return errors.New("invalid environments")
	}
	for _, environment := range environments {
		if environment == nil {
			return errors.New("invalid environment object")
		}
		for key := range environment {
			if key != "id" && key != "models" && key != "default_model" {
				return errors.New("invalid environment field")
			}
		}
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate key")
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
		return errors.New("invalid closing delimiter")
	}
	return nil
}
