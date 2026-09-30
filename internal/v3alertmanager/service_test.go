package v3alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type fakeStore struct{ store.Repository }

func (fakeStore) Session(context.Context, string) (core.User, error) {
	return core.User{ID: "reader"}, nil
}

type fakeEnvAccess struct{ allowStaging bool }

type fakeCatalog struct{}

func (fakeCatalog) Applications(context.Context, []string) ([]Application, error) { return nil, nil }
func (fakeCatalog) EnvironmentAdmin(context.Context, core.User, string) (bool, error) {
	return true, nil
}

type fakeSilenceAccess struct{}

func (fakeSilenceAccess) Authorize(context.Context, core.User, rbac.V3Decision) error { return nil }

type inhibitionTeamCatalog struct{}

func (inhibitionTeamCatalog) Applications(context.Context, []string) ([]Application, error) {
	return []Application{{ID: "owned-id", EnvironmentID: "test", OwnerTeamID: "ops"}}, nil
}
func (inhibitionTeamCatalog) EnvironmentAdmin(context.Context, core.User, string) (bool, error) {
	return false, nil
}

type inhibitionTeamAccess struct{}

func (inhibitionTeamAccess) Authorize(_ context.Context, _ core.User, decision rbac.V3Decision) error {
	if decision.Module == "silences" && decision.Action == "view" && decision.ApplicationID == "owned-id" {
		return nil
	}
	return store.ErrForbidden
}

type inhibitionDeniedAccess struct{}

func (inhibitionDeniedAccess) Authorize(context.Context, core.User, rbac.V3Decision) error {
	return store.ErrForbidden
}

func (f fakeEnvAccess) AuthorizeEnvironment(_ context.Context, _ core.User, env string) error {
	if env == "staging" && !f.allowStaging {
		return store.ErrForbidden
	}
	return nil
}

type fakeSource struct {
	silences map[string][]Silence
	alerts   map[string][]Alert
	rules    map[string][]InhibitionRule
	fail     map[string]bool
}

func (f fakeSource) ListSilences(_ context.Context, env string) ([]Silence, error) {
	if f.fail[env] {
		return nil, errors.New("upstream failed")
	}
	return f.silences[env], nil
}
func (f fakeSource) ListAlerts(_ context.Context, env string) ([]Alert, error) {
	if f.fail[env] {
		return nil, errors.New("upstream failed")
	}
	return f.alerts[env], nil
}
func (f fakeSource) ListInhibitionRules(_ context.Context, env string) ([]InhibitionRule, error) {
	if f.fail[env] {
		return nil, errors.New("upstream failed")
	}
	return f.rules[env], nil
}

func testServer(t *testing.T, source Source) *httpapi.Server {
	return testServerWithAccess(t, source, false)
}

func testServerWithAccess(t *testing.T, source Source, allowStaging bool) *httpapi.Server {
	t.Helper()
	api := httpapi.New(fakeStore{}, nil, true)
	err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}},
		fakeEnvAccess{allowStaging: allowStaging}, func(context.Context, string) (core.User, error) { return core.User{ID: "reader"}, nil },
		func(_ context.Context, _ core.User, module, action, env string) error {
			if module != "silences" || action != "view" || (env == "staging" && !allowStaging) {
				return store.ErrForbidden
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	(&Service{Source: source, Catalog: fakeCatalog{}, Access: fakeSilenceAccess{}, EnvironmentLabels: map[string]string{"test": "environment", "staging": "environment"},
		Now: func() time.Time { return time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC) }}).Register(api)
	return api
}

func TestSilenceListAllFailsWhenOneAuthorizedSourceIsUnavailable(t *testing.T) {
	source := fakeSource{silences: map[string][]Silence{"test": {silence(t, "s1", "active", "test")}}, fail: map[string]bool{"staging": true}}
	w := request(t, testServerWithAccess(t, source, true), "/api/v1/silences?state=all", "all")
	if w.Code != 503 || strings.Contains(w.Body.String(), `"counts"`) || strings.Contains(w.Body.String(), `"id":"s1"`) {
		t.Fatalf("incomplete source status=%d body=%s", w.Code, w.Body.String())
	}
}

func request(t *testing.T, api *httpapi.Server, path, env string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
	r.Header.Set("X-AIOps-Environment", env)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func silence(t *testing.T, id, state, matcher string) Silence {
	t.Helper()
	var s Silence
	data := `{"id":"` + id + `","status":{"state":"` + state + `"},"matchers":[{"name":"environment","value":"` + matcher + `","isEqual":true}],"startsAt":"2026-09-28T00:00:00Z","endsAt":"2026-09-28T02:00:00Z","updatedAt":"2026-09-28T00:00:00Z","createdBy":"operator","comment":"maintenance"}`
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMixedApplicationMatchersCannotEstablishOwnership(t *testing.T) {
	for _, extra := range []Matcher{
		{Name: "application", Value: "owned", IsEqual: false},
		{Name: "application", Value: "owned|hidden", IsEqual: true, IsRegex: true},
	} {
		s := silence(t, "mixed-app", "active", "test")
		s.Matchers = append(s.Matchers, Matcher{Name: "application", Value: "owned", IsEqual: true}, extra)
		if id, exact, ambiguous := exactSilenceApplication(s); id != "" || exact || !ambiguous {
			t.Fatalf("mixed application matcher was attributed: id=%q exact=%v ambiguous=%v", id, exact, ambiguous)
		}
	}
}

func TestMixedEnvironmentMatchersCannotEstablishOwnership(t *testing.T) {
	for _, extra := range []Matcher{
		{Name: "environment", Value: "test", IsEqual: false},
		{Name: "environment", Value: "test|staging", IsEqual: true, IsRegex: true},
	} {
		s := silence(t, "mixed-env", "active", "test")
		s.Matchers = append(s.Matchers, extra, Matcher{Name: "application", Value: "owned", IsEqual: true})
		if got := classifySilence(s, "environment", "test", map[string]string{"test": "environment", "staging": "environment"}); got != scopeOther {
			t.Fatalf("mixed environment matcher was attributed: scope=%v", got)
		}
	}
}

func TestVisibleSilenceAffectsUseApplicationKeyAndDeduplicatePerAlert(t *testing.T) {
	alert := func(env, app string, ids ...string) Alert {
		a := Alert{Labels: map[string]string{"environment": env, "application": app}}
		a.Status.SilencedBy = ids
		return a
	}
	counts := countVisibleSilencedAlerts([]Alert{
		alert("test", "checkout", "app-silence", "app-silence", "wide", "hidden"),
		alert("test", "other", "app-silence", "wide"),
		alert("staging", "checkout", "app-silence", "wide"),
	}, "environment", "test", map[string]string{"app-silence": "checkout", "wide": ""})
	if counts["app-silence"] != 1 || counts["wide"] != 2 || counts["hidden"] != 0 || len(counts) != 2 {
		t.Fatalf("incorrect visible affects: %+v", counts)
	}
}

func TestSilenceListScopesFiltersAndCountsObservedAlerts(t *testing.T) {
	a := Alert{Labels: map[string]string{"environment": "test"}}
	a.Status.SilencedBy = []string{"s1"}
	b := Alert{Labels: map[string]string{"environment": "staging"}}
	b.Status.SilencedBy = []string{"s1"}
	source := fakeSource{silences: map[string][]Silence{"test": {
		silence(t, "s1", "active", "test"), silence(t, "s2", "pending", "test"),
		silence(t, "other-env", "active", "staging"),
	}}, alerts: map[string][]Alert{"test": {a, b}}}
	api := testServer(t, source)
	w := request(t, api, "/api/v1/silences?state=active", "test")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Items []struct {
			ID        string `json:"id"`
			Affects   int    `json:"affects"`
			Remaining struct {
				Pct int `json:"pct"`
			} `json:"remaining"`
		} `json:"items"`
		Counts          map[string]int `json:"counts"`
		Partial         bool           `json:"partial"`
		UnscopedPresent bool           `json:"unscoped_present"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "s1" || result.Items[0].Affects != 1 || result.Items[0].Remaining.Pct != 50 || result.Counts["active"] != 1 || result.Counts["planned"] != 1 || result.Partial || result.UnscopedPresent {
		t.Fatalf("wrong scoped result: %+v", result)
	}
	w = request(t, api, "/api/v1/silences?state=planned", "test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"s2"`) || strings.Contains(w.Body.String(), `"id":"s1"`) {
		t.Fatalf("planned filter status=%d body=%s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/silences?state=", "/api/v1/silences?state=active&state=planned"} {
		w = request(t, api, path, "test")
		if w.Code != 422 {
			t.Fatalf("invalid state %s: %d", path, w.Code)
		}
	}
	w = request(t, api, "/api/v1/silences", "staging")
	if w.Code != 403 {
		t.Fatalf("unauthorized environment: %d", w.Code)
	}
}

func TestSharedSilenceSourceOtherEnvironmentDoesNotChangeCoverage(t *testing.T) {
	shared := []Silence{silence(t, "test-only", "active", "test"), silence(t, "staging-only", "active", "staging")}
	source := fakeSource{silences: map[string][]Silence{"test": shared, "staging": shared}}
	api := testServerWithAccess(t, source, true)
	for _, tc := range []struct {
		env  string
		want string
		deny string
	}{
		{"test", "test-only", "staging-only"},
		{"staging", "staging-only", "test-only"},
	} {
		w := request(t, api, "/api/v1/silences", tc.env)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+tc.want+`"`) || strings.Contains(w.Body.String(), `"id":"`+tc.deny+`"`) || !strings.Contains(w.Body.String(), `"unscoped_present":false`) {
			t.Fatalf("%s status=%d body=%s", tc.env, w.Code, w.Body.String())
		}
	}
	w := request(t, api, "/api/v1/silences?state=all", "all")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":2`) || strings.Count(w.Body.String(), `"id":"test-only"`) != 1 || strings.Count(w.Body.String(), `"id":"staging-only"`) != 1 || !strings.Contains(w.Body.String(), `"unscoped_present":false`) {
		t.Fatalf("all status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestLargeSharedSourceCountsOnlyVisibleRecords(t *testing.T) {
	foreign := silence(t, "foreign", "active", "staging")
	visible := silence(t, "visible", "active", "test")
	silences := make([]Silence, maxRecords+1)
	for i := 0; i < maxRecords; i++ {
		silences[i] = foreign
	}
	silences[maxRecords] = visible
	alerts := make([]Alert, maxRecords+1)
	for i := 0; i < maxRecords; i++ {
		alerts[i] = Alert{Labels: map[string]string{"environment": "staging"}}
		alerts[i].Status.SilencedBy = []string{"visible"}
	}
	alerts[maxRecords] = Alert{Labels: map[string]string{"environment": "test"}}
	alerts[maxRecords].Status.SilencedBy = []string{"visible", "visible"}
	w := request(t, testServer(t, fakeSource{silences: map[string][]Silence{"test": silences}, alerts: map[string][]Alert{"test": alerts}}), "/api/v1/silences", "test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"visible"`) || strings.Contains(w.Body.String(), `"id":"foreign"`) || !strings.Contains(w.Body.String(), `"affects":1`) || !strings.Contains(w.Body.String(), `"active":1`) {
		t.Fatalf("scoped shared source status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestTooManyVisibleSilencesDoNotReturnIncompleteCounts(t *testing.T) {
	visible := silence(t, "visible", "active", "test")
	silences := make([]Silence, maxRecords+1)
	for i := range silences {
		silences[i] = visible
	}
	w := request(t, testServer(t, fakeSource{silences: map[string][]Silence{"test": silences}}), "/api/v1/silences", "test")
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"code":"silence_limit"`) || strings.Contains(w.Body.String(), `"counts"`) {
		t.Fatalf("unbounded view status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestTrulyBroadSilenceOnlySetsNonCountCoverageFlag(t *testing.T) {
	broad := silence(t, "broad", "active", "test")
	broad.Matchers[0].IsRegex = true
	api := testServer(t, fakeSource{silences: map[string][]Silence{"test": {broad}}})
	w := request(t, api, "/api/v1/silences", "test")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"unscoped_present":true`) || strings.Contains(w.Body.String(), "unscoped_omitted") {
		t.Fatalf("broad status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSilenceListUnconfiguredAndFailedSources(t *testing.T) {
	api := testServer(t, nil)
	if w := request(t, api, "/api/v1/silences", "test"); w.Code != 503 || !strings.Contains(w.Body.String(), "alertmanager_unconfigured") {
		t.Fatalf("unconfigured status=%d body=%s", w.Code, w.Body.String())
	}
	api = testServer(t, fakeSource{fail: map[string]bool{"test": true}})
	if w := request(t, api, "/api/v1/silences", "test"); w.Code != 503 || !strings.Contains(w.Body.String(), "alertmanager_unavailable") {
		t.Fatalf("upstream failure status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestInhibitionsArePartialAndEnvironmentBound(t *testing.T) {
	source := fakeSource{rules: map[string][]InhibitionRule{"test": {
		{SourceMatchers: []string{`environment="test"`, `severity="critical"`}, TargetMatchers: []string{`environment="test"`, `severity="warning"`}, Equal: []string{"service"}},
		{SourceMatchers: []string{`environment="staging"`}, TargetMatchers: []string{`environment="staging"`}},
		{SourceMatchers: []string{`environment=~"test|staging"`}, TargetMatchers: []string{`environment="test"`}},
	}}}
	w := request(t, testServer(t, source), "/api/v1/inhibitions", "test")
	if w.Code != 206 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Items           []map[string]any `json:"items"`
		Partial         bool             `json:"partial"`
		UnscopedPresent bool             `json:"unscoped_present"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || !result.Partial || !result.UnscopedPresent || result.Items[0]["active"] != nil || result.Items[0]["stat"] == "" {
		t.Fatalf("inhibition projection invented status or leaked scope: %+v", result)
	}
}

func TestInhibitionMatchersWhitespaceQuotingAndOperators(t *testing.T) {
	matchers, err := ruleMatchers([]string{
		" \t\nenvironment \t=\n \"test\" \t",
		`application = "owned"`,
		`severity != "critical"`,
		`team =~ "ops|sre"`,
		`note !~ "a,b"`,
		`escaped = "quote\" and slash\\"`,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []matcherView{
		{K: "environment", Op: "=", V: "test"},
		{K: "application", Op: "=", V: "owned"},
		{K: "severity", Op: "!=", V: "! critical"},
		{K: "team", Op: "=~", V: "~ ops|sre"},
		{K: "note", Op: "!~", V: "!~ a,b"},
		{K: "escaped", Op: "=", V: `quote" and slash\`},
	}
	if len(matchers) != len(want) {
		t.Fatalf("matchers=%+v", matchers)
	}
	for i := range want {
		if matchers[i] != want[i] {
			t.Fatalf("matcher[%d]=%+v, want %+v", i, matchers[i], want[i])
		}
	}
}

func TestInhibitionMatchersRejectUnsupportedComposition(t *testing.T) {
	for _, raw := range []string{
		`environment="test",application="hidden"`,
		`application="owned",application=~"owned|hidden"`,
		`environment = test, environment != staging`,
		`{environment="test",application="owned"}`,
		`application=owned environment="staging"`,
		`note=unquoted,bad`,
		`note="unterminated`,
	} {
		if _, err := ruleMatchers([]string{raw}, nil, nil); !errors.Is(err, ErrInvalidMatcher) {
			t.Errorf("raw=%q err=%v", raw, err)
		}
	}
}

func TestMalformedInhibitionMatcherFailsWholeResponse(t *testing.T) {
	source := fakeSource{rules: map[string][]InhibitionRule{"test": {
		{SourceMatchers: []string{`environment="test"`}, TargetMatchers: []string{`environment="test"`}},
		{SourceMatchers: []string{`environment="test",application="hidden"`}, TargetMatchers: []string{`environment="test"`}},
	}}}
	w := request(t, testServer(t, source), "/api/v1/inhibitions", "test")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "invalid_inhibition_source") ||
		strings.Contains(w.Body.String(), `"items"`) || strings.Contains(w.Body.String(), `"partial"`) {
		t.Fatalf("malformed inhibition status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestForeignInhibitionRulesDoNotChangeCoverage(t *testing.T) {
	source := fakeSource{rules: map[string][]InhibitionRule{"test": {
		{SourceMatchers: []string{`environment="staging"`}, TargetMatchers: []string{`environment="staging"`}},
	}}}
	w := request(t, testServer(t, source), "/api/v1/inhibitions", "test")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"items":[]`) || !strings.Contains(w.Body.String(), `"unscoped_present":false`) {
		t.Fatalf("foreign rule status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestForeignSourceLocalTargetReportsAdminCoverage(t *testing.T) {
	for _, target := range [][]string{{`environment="test"`}, {`severity="warning"`}} {
		source := fakeSource{rules: map[string][]InhibitionRule{"test": {{
			SourceMatchers: []string{`environment="staging"`}, TargetMatchers: target,
		}}}}
		w := request(t, testServer(t, source), "/api/v1/inhibitions", "test")
		if w.Code != 206 || !strings.Contains(w.Body.String(), `"items":[]`) ||
			!strings.Contains(w.Body.String(), `"unscoped_present":true`) || strings.Contains(w.Body.String(), "staging") {
			t.Fatalf("foreign source target=%v status=%d body=%s", target, w.Code, w.Body.String())
		}
	}
}

func TestUnboundExactInhibitionOnlyReportsAdminCoverage(t *testing.T) {
	// The sole rule has exact environment and application matchers, but no
	// current server binding. There is no broad rule to create this flag.
	source := fakeSource{rules: map[string][]InhibitionRule{"test": {{
		SourceMatchers: []string{`environment="test"`, `application="retired"`},
		TargetMatchers: []string{`environment="test"`, `application="retired"`},
	}}}}
	w := request(t, testServer(t, source), "/api/v1/inhibitions", "test")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"items":[]`) ||
		!strings.Contains(w.Body.String(), `"unscoped_present":true`) || strings.Contains(w.Body.String(), `"retired"`) {
		t.Fatalf("unbound admin coverage status=%d body=%s", w.Code, w.Body.String())
	}
	api := httpapi.New(fakeStore{}, nil, true)
	err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		fakeEnvAccess{}, func(context.Context, string) (core.User, error) { return core.User{ID: "reader"}, nil },
		func(context.Context, core.User, string, string, string) error { return store.ErrForbidden })
	if err != nil {
		t.Fatal(err)
	}
	(&Service{Source: source, Catalog: inhibitionTeamCatalog{}, Access: inhibitionTeamAccess{},
		EnvironmentLabels:   map[string]string{"test": "environment"},
		ApplicationBindings: map[string]map[string]string{"test": {"owned": "owned-id"}}}).Register(api)
	w = request(t, api, "/api/v1/inhibitions", "test")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"items":[]`) ||
		!strings.Contains(w.Body.String(), `"unscoped_present":false`) || strings.Contains(w.Body.String(), `"retired"`) {
		t.Fatalf("unbound team coverage status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestInhibitionDeniedBeforeSourceCapabilityCheck(t *testing.T) {
	for _, source := range []Source{nil, fakeSource{rules: map[string][]InhibitionRule{"test": {}}}} {
		api := httpapi.New(fakeStore{}, nil, true)
		err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
			fakeEnvAccess{}, func(context.Context, string) (core.User, error) { return core.User{ID: "reader"}, nil },
			func(context.Context, core.User, string, string, string) error { return store.ErrForbidden })
		if err != nil {
			t.Fatal(err)
		}
		(&Service{Source: source, Catalog: inhibitionTeamCatalog{}, Access: inhibitionDeniedAccess{},
			EnvironmentLabels:   map[string]string{"test": "environment"},
			ApplicationBindings: map[string]map[string]string{"test": {"owned": "owned-id"}}}).Register(api)
		w := request(t, api, "/api/v1/inhibitions", "test")
		if w.Code != 403 || !strings.Contains(w.Body.String(), `"forbidden"`) ||
			strings.Contains(w.Body.String(), "alertmanager") || strings.Contains(w.Body.String(), `"items"`) {
			t.Fatalf("denied inhibition source=%T status=%d body=%s", source, w.Code, w.Body.String())
		}
	}
}
