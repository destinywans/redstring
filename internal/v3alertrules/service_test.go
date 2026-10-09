package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type fakeCatalog []Application

func (c fakeCatalog) Applications(_ context.Context, _ []string) ([]Application, error) {
	return c, nil
}

type fakeAccess struct{ denied map[string]bool }

func (a fakeAccess) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	if d.Module != "alert_rules" || d.Action != "view" || a.denied[d.EnvironmentID+"/"+d.ApplicationID] {
		return store.ErrForbidden
	}
	return nil
}

type fakeSource struct {
	rules map[string][]ObservedRule
	fail  map[string]bool
}

func (s fakeSource) List(_ context.Context, env string) ([]ObservedRule, error) {
	if s.fail[env] {
		return nil, errors.New("upstream failed")
	}
	return s.rules[env], nil
}

type fakeEnvAccess struct{}

func (fakeEnvAccess) AuthorizeEnvironment(context.Context, core.User, string) error { return nil }

func scopedHandler(t *testing.T, h http.Handler) http.Handler {
	t.Helper()
	guard, err := httpapi.NewV3EnvironmentHandler(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}}, fakeEnvAccess{}, func(context.Context, string) (core.User, error) { return core.User{ID: "u"}, nil }, h)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

func call(t *testing.T, handler http.Handler, method, path, env string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
	r.Header.Set("X-AIOps-Environment", env)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

const testRuleFile = "/etc/prometheus/rules/checkout.yml"

func mustService(t *testing.T, catalog Catalog, access Access, source Source, bindings ...RuleGroupBinding) *Service {
	t.Helper()
	s, err := New(catalog, access, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestListFiltersByPersistedApplicationAndRuleLabels(t *testing.T) {
	rule := func(env, app, name string) ObservedRule {
		return ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: name, Expression: "up", State: "firing", Labels: map[string]string{"environment": env, "application": app}}
	}
	unbound := rule("test", "allowed", "spoofed")
	unbound.GroupIdentity = "/etc/prometheus/rules/unbound.yml\x00g"
	s := mustService(t, fakeCatalog{{ID: "allowed", EnvironmentID: "test"}, {ID: "denied", EnvironmentID: "test"}, {ID: "other", EnvironmentID: "staging"}}, fakeAccess{denied: map[string]bool{"test/denied": true}}, fakeSource{rules: map[string][]ObservedRule{
		"test":    {rule("test", "allowed", "visible"), rule("test", "denied", "denied"), rule("staging", "allowed", "wrong-env"), rule("test", "unknown", "unmapped"), rule("", "allowed", "unscoped"), unbound},
		"staging": {rule("staging", "other", "other-env")},
	}}, RuleGroupBinding{EnvironmentID: "test", ApplicationID: "allowed", File: testRuleFile, Group: "g"})
	w := call(t, scopedHandler(t, http.HandlerFunc(s.List)), "GET", "/api/v1/alert-rules/observed", "test")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items  []Summary      `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Name != "visible" || body.Counts["all"] != 1 || body.Counts["firing"] != 1 {
		t.Fatalf("unscoped or forbidden rules escaped: %+v", body)
	}
}

func TestListPartialAndUnavailableAreExplicit(t *testing.T) {
	s := mustService(t, fakeCatalog{{ID: "a", EnvironmentID: "test"}, {ID: "b", EnvironmentID: "staging"}}, fakeAccess{}, fakeSource{rules: map[string][]ObservedRule{"test": {{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "A", Expression: "up", Labels: map[string]string{"environment": "test", "application": "a"}}}}, fail: map[string]bool{"staging": true}}, RuleGroupBinding{EnvironmentID: "test", ApplicationID: "a", File: testRuleFile, Group: "g"})
	w := call(t, scopedHandler(t, http.HandlerFunc(s.List)), "GET", "/api/v1/alert-rules/observed", "all")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"unavailable_environments":["staging"]`) {
		t.Fatalf("partial status=%d body=%s", w.Code, w.Body.String())
	}
	s.Source = nil
	// No rule source is a configuration state: the list is unknown, not empty.
	for _, handler := range []http.HandlerFunc{s.List, s.MainList} {
		w = call(t, scopedHandler(t, handler), "GET", "/api/v1/alert-rules", "test")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":null`) || !strings.Contains(w.Body.String(), `"counts":null`) ||
			!strings.Contains(w.Body.String(), `"items":"not_configured"`) {
			t.Fatalf("unconfigured status=%d body=%s", w.Code, w.Body.String())
		}
	}
	none := mustService(t, fakeCatalog{}, fakeAccess{}, fakeSource{}, RuleGroupBinding{EnvironmentID: "test", ApplicationID: "a", File: testRuleFile, Group: "g"})
	w = call(t, scopedHandler(t, http.HandlerFunc(none.MainList)), "GET", "/api/v1/alert-rules", "test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"empty_reason":"no_authorized_target"`) || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("no visible application status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDetailNeverUsesFallbackAndDryRunRejectsMissingBody(t *testing.T) {
	s := mustService(t, fakeCatalog{{ID: "a", EnvironmentID: "test"}}, fakeAccess{}, fakeSource{rules: map[string][]ObservedRule{"test": {{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "A", Expression: "up", Labels: map[string]string{"environment": "test", "application": "a"}}}}}, RuleGroupBinding{EnvironmentID: "test", ApplicationID: "a", File: testRuleFile, Group: "g"})
	h := scopedHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", "nonexistent")
		s.Detail(w, r)
	}))
	w := call(t, h, "GET", "/api/v1/alert-rules/observed/nonexistent", "test")
	if w.Code != 404 {
		t.Fatalf("unknown detail status=%d body=%s", w.Code, w.Body.String())
	}
	w = call(t, scopedHandler(t, http.HandlerFunc(s.DryRun)), "POST", "/api/v1/alert-rules/anything/dry-run", "test")
	if w.Code != 422 || !strings.Contains(w.Body.String(), "invalid_body") || strings.Contains(w.Body.String(), "would_fire") {
		t.Fatalf("dry-run status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBindingRejectsMissingOrDuplicateGroupIdentity(t *testing.T) {
	_, err := New(nil, nil, nil, []RuleGroupBinding{{EnvironmentID: "test", ApplicationID: "a", Group: "g"}})
	if err == nil {
		t.Fatal("accepted binding without exact provider file")
	}
	b := RuleGroupBinding{EnvironmentID: "test", ApplicationID: "a", File: testRuleFile, Group: "g"}
	_, err = New(nil, nil, nil, []RuleGroupBinding{b, b})
	if err == nil {
		t.Fatal("accepted duplicate binding")
	}
}
