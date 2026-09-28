package v3conversation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
)

type testRuntime struct{ models []inference.Model }

func (r testRuntime) Models() []inference.Model { return r.models }
func (testRuntime) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	return "", errors.New("unused")
}

func TestEnvironmentScopeRejectsAllWrites(t *testing.T) {
	svc := New(nil, nil, Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", nil)
	req.Header.Set("X-AIOps-Environment", "all")
	_, err := svc.environment(req, true)
	if !errors.Is(err, ErrViewOnly) {
		t.Fatalf("write in all scope: %v", err)
	}
	env, err := svc.environment(req, false)
	if err != nil || env != "all" {
		t.Fatalf("read in all scope: %q %v", env, err)
	}
}

func TestModelSelectionConfiguredOnly(t *testing.T) {
	svc := New(nil, testRuntime{models: []inference.Model{{ID: "ollama:small", Name: "small"}, {ID: "openai:large", Name: "large"}}}, Options{})
	picked, err := svc.model("", "openai:large")
	if err != nil || picked.ID != "openai:large" {
		t.Fatalf("fallback: %#v %v", picked, err)
	}
	picked, err = svc.model("ollama:small", "openai:large")
	if err != nil || picked.ID != "ollama:small" {
		t.Fatalf("override: %#v %v", picked, err)
	}
	_, err = svc.model("unconfigured", "")
	if !errors.Is(err, ErrModel) {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestPageBounds(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations?page=-1&page_size=99999", nil)
	page, size := pagination(req)
	if page != 1 || size != 100 {
		t.Fatalf("page=%d size=%d", page, size)
	}
}

func TestAllowedEnvironmentsFailsClosedAndExcludesProduction(t *testing.T) {
	user := core.User{ID: "u1"}
	if _, err := New(nil, nil, Options{}).allowed(context.Background(), user, "test"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing authorization source: %v", err)
	}
	svc := New(nil, nil, Options{AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
		return []string{"prod", "test", "staging"}, nil
	}})
	visible, err := svc.allowed(context.Background(), user, "all")
	if err != nil || len(visible) != 2 || visible[0] != "test" || visible[1] != "staging" {
		t.Fatalf("nonproduction union: %#v %v", visible, err)
	}
	if _, err := svc.allowed(context.Background(), user, "dev"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized environment: %v", err)
	}
}

func TestCompletionSuffixKeepsStreamAndPersistedReplyConsistent(t *testing.T) {
	for _, tc := range []struct {
		streamed, final, suffix string
		wantErr                 bool
	}{
		{"", "complete answer", "complete answer", false},
		{"first", "first answer", " answer", false},
		{"complete answer", "complete answer", "", false},
		{"first", "revised answer", "", true},
	} {
		suffix, err := completionSuffix(tc.streamed, tc.final)
		if suffix != tc.suffix || (err != nil) != tc.wantErr {
			t.Fatalf("streamed=%q final=%q: suffix=%q err=%v", tc.streamed, tc.final, suffix, err)
		}
	}
}
