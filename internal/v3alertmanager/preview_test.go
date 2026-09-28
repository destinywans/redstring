package v3alertmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		ApplicationBindings: map[string]map[string]string{"test": {"checkout": "app-owned", "hidden": "app-hidden"}}}).Register(api)
	return api
}

func TestSilencePreviewRequiresExactApplicationOrEnvironmentGrant(t *testing.T) {
	alert := func(app string) Alert {
		return Alert{Labels: map[string]string{"environment": "test", "application": app, "alertname": "DownstreamTimeout"}}
	}
	api := scopedPreviewServer(t, fakeSource{alerts: map[string][]Alert{"test": {alert("checkout"), alert("hidden")}}})
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
	if hidden.Code != http.StatusNotFound || unknown.Code != hidden.Code || unknown.Body.String() != hidden.Body.String() {
		t.Fatalf("hidden binding is distinguishable: hidden=%d %s unknown=%d %s", hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String())
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
