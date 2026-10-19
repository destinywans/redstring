package v3alertmanager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

// silenceReaderAccess may view silences and nothing else.
type silenceReaderAccess struct{}

func (silenceReaderAccess) Authorize(_ context.Context, _ core.User, decision rbac.V3Decision) error {
	if decision.Module == "silences" && decision.Action == "view" {
		return nil
	}
	return store.ErrForbidden
}

// silenceTeamWriterAccess may view and propose silences for the ops team's
// applications only.
type silenceTeamWriterAccess struct{}

func (silenceTeamWriterAccess) Authorize(_ context.Context, _ core.User, decision rbac.V3Decision) error {
	if decision.TeamID == "ops" && (decision.Module == "silences" || decision.Module == "proposals" && decision.Action == "change") {
		return nil
	}
	return store.ErrForbidden
}

type silenceWorkflowStub struct{ err error }

func (w silenceWorkflowStub) CreateReady(context.Context, string) error { return w.err }
func (w silenceWorkflowStub) CancelReady(context.Context, string) error { return w.err }

type silenceProducerStub struct{}

func (silenceProducerStub) SubmitSilenceCreate(context.Context, core.User, string, string, SilenceCreateInput) (SilenceProductProposal, error) {
	return SilenceProductProposal{}, errors.New("a check must answer before the producer")
}
func (silenceProducerStub) SubmitSilenceCancel(context.Context, core.User, string, string, string, string, string, string) (SilenceProductProposal, error) {
	return SilenceProductProposal{}, errors.New("a check must answer before the producer")
}

// PA-108: a member who may not write silences is refused before the workflow
// and write binding are checked, so the refusal never says what this
// environment has not connected. Writers, including team-scoped ones, still
// learn what is missing.
func TestSilenceWritesDeniedBeforeWriteBindingCheck(t *testing.T) {
	client, err := New(map[string]Endpoint{"test": {URL: "http://alertmanager.invalid", AllowHTTP: true}}, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	connect := func(s *Service, source Source, workflow error) {
		s.Source, s.SilenceWorkflow = source, silenceWorkflowStub{err: workflow}
		s.SilenceCreateProposals, s.SilenceCancelProposals = silenceProducerStub{}, silenceProducerStub{}
	}
	for name, tc := range map[string]struct {
		setup   func(*Service)
		status  int
		missing string
	}{
		"no workflow":             {func(*Service) {}, http.StatusNotImplemented, "not_connected"},
		"no write binding":        {func(s *Service) { connect(s, fakeSource{}, nil) }, http.StatusNotImplemented, "not_connected"},
		"workflow not configured": {func(s *Service) { connect(s, client, ErrNotConfigured) }, http.StatusNotImplemented, "not_connected"},
		"workflow unavailable":    {func(s *Service) { connect(s, client, errors.New("worker down")) }, http.StatusServiceUnavailable, "silence_workflow_unavailable"},
	} {
		for _, actor := range []struct {
			name   string
			access Access
			denied bool
		}{{"reader", silenceReaderAccess{}, true}, {"team writer", silenceTeamWriterAccess{}, false}, {"manager", fakeSilenceAccess{}, false}} {
			t.Run(name+"/"+actor.name, func(t *testing.T) {
				s := &Service{Source: fakeSource{}, Catalog: inhibitionTeamCatalog{}, Access: actor.access,
					EnvironmentLabels:   map[string]string{"test": "environment"},
					ApplicationBindings: map[string]map[string]string{"test": {"owned": "owned-id"}}}
				tc.setup(s)
				api := httpapi.New(fakeStore{}, nil, true)
				if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
					fakeEnvAccess{}, func(context.Context, string) (core.User, error) { return core.User{ID: "reader"}, nil },
					func(context.Context, core.User, string, string, string) error { return store.ErrForbidden }); err != nil {
					t.Fatal(err)
				}
				s.Register(api)
				for _, req := range []struct{ method, path, body string }{
					{http.MethodPost, "/api/v1/silences", `{"investigation_id":"INV-check","matchers":[{"name":"application","op":"=","value":"owned"}],` +
						`"duration_seconds":3600,"comment":"maintenance"}`},
					{http.MethodDelete, "/api/v1/silences/" + strings.Repeat("a", 8) + "-aaaa-aaaa-aaaa-" + strings.Repeat("a", 12),
						`{"investigation_id":"INV-check","reason":"maintenance done"}`},
				} {
					r := httptest.NewRequest(req.method, req.path, strings.NewReader(req.body))
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("X-AIOps-Environment", "test")
					r.Header.Set("Idempotency-Key", "silence-authz-"+store.ID())
					r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
					w := httptest.NewRecorder()
					api.ServeHTTP(w, r)
					if actor.denied {
						if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "connected") || strings.Contains(w.Body.String(), "workflow") {
							t.Errorf("%s %s: %d %s", req.method, req.path, w.Code, w.Body.String())
						}
						continue
					}
					if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.missing+`"`) {
						t.Errorf("%s %s: %d %s", req.method, req.path, w.Code, w.Body.String())
					}
				}
			})
		}
	}
}
