package v3alertmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func previewRequest(t *testing.T, api *httpapi.Server, environment, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/silences/preview", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
	r.Header.Set("X-AIOps-Environment", environment)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestSilencePreviewCountsOnlyCurrentSelectedEnvironment(t *testing.T) {
	alert := func(env, service, name string) Alert {
		return Alert{Labels: map[string]string{"environment": env, "service": service, "alertname": name}}
	}
	api := testServer(t, fakeSource{alerts: map[string][]Alert{"test": {
		alert("test", "checkout-api", "DownstreamTimeout"),
		alert("test", "checkout-api", "HighLatency"),
		alert("test", "payments", "DownstreamTimeout"),
		alert("staging", "checkout-api", "DownstreamTimeout"),
	}}})
	w := previewRequest(t, api, "test", `{"matchers":[{"k":"service","op":"=","v":"checkout-api"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Affects int      `json:"affects"`
		Sample  []string `json:"sample"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Affects != 2 || len(got.Sample) != 2 || got.Sample[0] != "DownstreamTimeout" || got.Sample[1] != "HighLatency" {
		t.Fatalf("wrong live preview: %+v err=%v", got, err)
	}
	if w := previewRequest(t, api, "all", `{"matchers":[{"k":"service","op":"=","v":"checkout-api"}]}`); w.Code != http.StatusConflict {
		t.Fatalf("all environment write-shaped POST was accepted: %d %s", w.Code, w.Body.String())
	}
}

type previewCatalog struct {
	apps  []Application
	admin bool
}

func (c previewCatalog) Applications(context.Context, []string) ([]Application, error) {
	return c.apps, nil
}
func (c previewCatalog) EnvironmentAdmin(context.Context, core.User, string) (bool, error) {
	return c.admin, nil
}

type previewAccess struct{ appID string }

func (a previewAccess) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	if d.Module != "silences" || d.Action != "view" || d.EnvironmentID != "test" || d.ApplicationID != a.appID {
		return store.ErrForbidden
	}
	return nil
}

type countingPreviewSource struct {
	Source
	alertCalls atomic.Int32
}

func (s *countingPreviewSource) ListAlerts(ctx context.Context, env string) ([]Alert, error) {
	s.alertCalls.Add(1)
	return s.Source.ListAlerts(ctx, env)
}

func scopedPreviewServer(t *testing.T, source Source) *httpapi.Server {
	t.Helper()
	api := httpapi.New(fakeStore{}, nil, true)
	err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		fakeEnvAccess{}, func(context.Context, string) (core.User, error) { return core.User{ID: "reader"}, nil },
		func(context.Context, core.User, string, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	(&Service{Source: source, Catalog: previewCatalog{apps: []Application{{ID: "app-owned", EnvironmentID: "test"}, {ID: "app-hidden", EnvironmentID: "test"}}},
		Access: previewAccess{appID: "app-owned"}, EnvironmentLabels: map[string]string{"test": "environment"},
		ApplicationBindings: map[string]map[string]string{"test": {"checkout": "app-owned", "hidden": "app-hidden", "inactive": "app-inactive"}}}).Register(api)
	return api
}

func TestSilencePreviewRequiresExactApplicationOrEnvironmentGrant(t *testing.T) {
	alert := func(app string) Alert {
		return Alert{Labels: map[string]string{"environment": "test", "application": app, "alertname": "DownstreamTimeout"}}
	}
	source := &countingPreviewSource{Source: fakeSource{alerts: map[string][]Alert{"test": {alert("checkout"), alert("hidden")}}}}
	api := scopedPreviewServer(t, source)
	broad := `{"matchers":[{"k":"alertname","op":"=","v":"DownstreamTimeout"}]}`
	if w := previewRequest(t, api, "test", broad); w.Code != http.StatusForbidden {
		t.Fatalf("application-only reader previewed broad silence: %d %s", w.Code, w.Body.String())
	}
	exact := `{"matchers":[{"k":"application","op":"=","v":"checkout"},{"k":"alertname","op":"=","v":"DownstreamTimeout"}]}`
	w := previewRequest(t, api, "test", exact)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"affects":1`) {
		t.Fatalf("exact authorized application preview failed: %d %s", w.Code, w.Body.String())
	}
	hidden := previewRequest(t, api, "test", `{"matchers":[{"k":"application","op":"=","v":"hidden"}]}`)
	unknown := previewRequest(t, api, "test", `{"matchers":[{"k":"application","op":"=","v":"unbound"}]}`)
	inactive := previewRequest(t, api, "test", `{"matchers":[{"k":"application","op":"=","v":"inactive"}]}`)
	if hidden.Code != http.StatusNotFound || unknown.Code != hidden.Code || inactive.Code != hidden.Code ||
		unknown.Body.String() != hidden.Body.String() || inactive.Body.String() != hidden.Body.String() || source.alertCalls.Load() != 1 {
		t.Fatalf("hidden, unknown, or inactive binding is distinguishable: hidden=%d %s unknown=%d %s inactive=%d %s source calls=%d",
			hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String(), inactive.Code, inactive.Body.String(), source.alertCalls.Load())
	}
	for _, input := range []string{
		`{"matchers":[{"k":"application","op":"=~","v":"checkout|hidden"}]}`,
		`{"matchers":[{"k":"environment","op":"!=","v":"staging"}]}`,
		`{"matchers":[{"k":"alertname","op":"=~","v":"["}]}`,
	} {
		if w := previewRequest(t, api, "test", input); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unsafe matcher accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestSilencePreviewFailsWhenLiveSourceFails(t *testing.T) {
	api := testServer(t, fakeSource{fail: map[string]bool{"test": true}})
	w := previewRequest(t, api, "test", `{"matchers":[{"k":"alertname","op":"=","v":"DownstreamTimeout"}]}`)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), `"affects"`) {
		t.Fatalf("source failure became a zero or partial count: %d %s", w.Code, w.Body.String())
	}
}

func TestSilencePreviewRejectsNullUpstreamAlertList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}))
	defer upstream.Close()
	source, err := New(map[string]Endpoint{"test": {URL: upstream.URL, AllowHTTP: true}}, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	w := previewRequest(t, testServer(t, source), "test", `{"matchers":[{"k":"alertname","op":"=","v":"DownstreamTimeout"}]}`)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), `"affects"`) {
		t.Fatalf("null upstream became a zero count: %d %s", w.Code, w.Body.String())
	}
}

func TestSilencePreviewConfiguredAlertmanagerResponse(t *testing.T) {
	var requested atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/alertmanager/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		requested.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
 {"labels":{"environment":"test","application":"checkout","alertname":"Zeta"}},
 {"labels":{"environment":"staging","application":"checkout","alertname":"Foreign"}},
 {"labels":{"environment":"test","application":"hidden","alertname":"Hidden"}},
 {"labels":{"environment":"test","application":"checkout","alertname":"Alpha"}},
 {"labels":{"environment":"test","application":"checkout","alertname":"Zeta"}},
 {"labels":{"environment":"test","application":"checkout","alertname":"Gamma"}},
 {"labels":{"environment":"test","application":"checkout","alertname":"Delta"}},
 {"labels":{"environment":"test","application":"checkout","alertname":"Beta"}},
 {"labels":{"environment":"test","application":"checkout","alertname":"Epsilon"}}
]`))
	}))
	defer upstream.Close()
	source, err := New(map[string]Endpoint{"test": {URL: upstream.URL + "/alertmanager", AllowHTTP: true}}, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	api := scopedPreviewServer(t, source)
	w := previewRequest(t, api, "test", `{"matchers":[{"k":"application","op":"=","v":"checkout"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("live preview status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Affects int      `json:"affects"`
		Sample  []string `json:"sample"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Affects != 7 || strings.Join(got.Sample, ",") != "Alpha,Beta,Delta,Epsilon,Gamma" || requested.Load() != 1 {
		t.Fatalf("wrong scoped live response: %+v requested=%d", got, requested.Load())
	}
}

func TestSilencePreviewAlertLimitCountsOnlySelectedEnvironment(t *testing.T) {
	alert := func(env string) Alert {
		return Alert{Labels: map[string]string{"environment": env, "application": "checkout", "alertname": "Active"}}
	}
	for _, tc := range []struct {
		name      string
		inEnv     int
		wantCode  int
		wantCount int
	}{
		{name: "foreign records do not inflate limit", inEnv: maxRecords, wantCode: http.StatusOK, wantCount: maxRecords},
		{name: "selected environment exceeds limit", inEnv: maxRecords + 1, wantCode: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alerts := make([]Alert, 0, tc.inEnv+maxRecords+1)
			for i := 0; i < tc.inEnv; i++ {
				alerts = append(alerts, alert("test"))
			}
			for i := 0; i < maxRecords+1; i++ {
				alerts = append(alerts, alert("staging"))
			}
			api := scopedPreviewServer(t, fakeSource{alerts: map[string][]Alert{"test": alerts}})
			w := previewRequest(t, api, "test", `{"matchers":[{"k":"application","op":"=","v":"checkout"}]}`)
			if w.Code != tc.wantCode {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantCode == http.StatusOK {
				var got struct {
					Affects int `json:"affects"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Affects != tc.wantCount {
					t.Fatalf("count=%d err=%v", got.Affects, err)
				}
			} else if strings.Contains(w.Body.String(), `"affects"`) {
				t.Fatalf("over-limit preview exposed partial result: %s", w.Body.String())
			}
		})
	}
}

type blockedPreviewSource struct {
	entered chan struct{}
	release chan struct{}
}

func (s blockedPreviewSource) ListAlerts(ctx context.Context, _ string) ([]Alert, error) {
	close(s.entered)
	select {
	case <-s.release:
		return []Alert{{Labels: map[string]string{"environment": "test", "application": "checkout", "alertname": "Active"}}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (blockedPreviewSource) ListSilences(context.Context, string) ([]Silence, error) {
	return nil, nil
}
func (blockedPreviewSource) ListInhibitionRules(context.Context, string) ([]InhibitionRule, error) {
	return nil, nil
}

type changingPreviewCatalog struct {
	ownerTeam atomic.Value
	active    atomic.Bool
	manage    atomic.Bool
}

func (c *changingPreviewCatalog) Applications(context.Context, []string) ([]Application, error) {
	if !c.active.Load() {
		return nil, nil
	}
	return []Application{{ID: "app-owned", EnvironmentID: "test", OwnerTeamID: c.ownerTeam.Load().(string)}}, nil
}
func (c *changingPreviewCatalog) EnvironmentAdmin(context.Context, core.User, string) (bool, error) {
	return c.manage.Load(), nil
}

type changingPreviewAccess struct {
	envGrant  atomic.Bool
	appGrant  atomic.Bool
	teamGrant atomic.Bool
	broadView atomic.Bool
}

func (a *changingPreviewAccess) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	// The production V3 policy checks environment.view on every object grant.
	if !a.envGrant.Load() || d.Module != "silences" || d.Action != "view" || d.EnvironmentID != "test" {
		return store.ErrForbidden
	}
	if d.ApplicationID == "app-owned" && a.appGrant.Load() ||
		d.TeamID == "ops" && a.teamGrant.Load() ||
		d.ApplicationID == "" && d.TeamID == "" && a.broadView.Load() {
		return nil
	}
	return store.ErrForbidden
}

func TestSilencePreviewRechecksScopeAfterAlertmanagerRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		broad  bool
		setup  func(*changingPreviewCatalog, *changingPreviewAccess)
		revoke func(*changingPreviewCatalog, *changingPreviewAccess)
	}{
		{name: "application grant", setup: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.appGrant.Store(true) }, revoke: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.appGrant.Store(false) }},
		{name: "environment grant", setup: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.appGrant.Store(true) }, revoke: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.envGrant.Store(false) }},
		{name: "team grant", setup: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.teamGrant.Store(true) }, revoke: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.teamGrant.Store(false) }},
		{name: "owner team change", setup: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.teamGrant.Store(true) }, revoke: func(c *changingPreviewCatalog, _ *changingPreviewAccess) { c.ownerTeam.Store("other") }},
		{name: "application removed", setup: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.appGrant.Store(true) }, revoke: func(c *changingPreviewCatalog, _ *changingPreviewAccess) { c.active.Store(false) }},
		{name: "broad view revoked", broad: true, setup: func(c *changingPreviewCatalog, a *changingPreviewAccess) {
			c.manage.Store(true)
			a.broadView.Store(true)
		}, revoke: func(_ *changingPreviewCatalog, a *changingPreviewAccess) { a.broadView.Store(false) }},
		{name: "broad manage revoked", broad: true, setup: func(c *changingPreviewCatalog, a *changingPreviewAccess) {
			c.manage.Store(true)
			a.broadView.Store(true)
		}, revoke: func(c *changingPreviewCatalog, _ *changingPreviewAccess) { c.manage.Store(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &changingPreviewCatalog{}
			catalog.ownerTeam.Store("ops")
			catalog.active.Store(true)
			access := &changingPreviewAccess{}
			access.envGrant.Store(true)
			tc.setup(catalog, access)
			source := blockedPreviewSource{entered: make(chan struct{}), release: make(chan struct{})}
			api := httpapi.New(fakeStore{}, nil, true)
			if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
				fakeEnvAccess{}, func(context.Context, string) (core.User, error) { return core.User{ID: "reader"}, nil },
				func(context.Context, core.User, string, string, string) error { return nil }); err != nil {
				t.Fatal(err)
			}
			(&Service{Source: source, Catalog: catalog, Access: access, EnvironmentLabels: map[string]string{"test": "environment"},
				ApplicationBindings: map[string]map[string]string{"test": {"checkout": "app-owned"}}}).Register(api)
			body := `{"matchers":[{"k":"application","op":"=","v":"checkout"}]}`
			if tc.broad {
				body = `{"matchers":[{"k":"alertname","op":"=","v":"Active"}]}`
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- previewRequest(t, api, "test", body) }()
			<-source.entered
			tc.revoke(catalog, access)
			close(source.release)
			w := <-result
			if w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"affects"`) {
				t.Fatalf("scope revoked during upstream read but preview returned status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
