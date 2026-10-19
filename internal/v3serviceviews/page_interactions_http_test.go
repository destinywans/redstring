package v3serviceviews

import (
	"context"
	"encoding/json"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/v3traces"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPageSLOAndTeamTraceViews(t *testing.T) {
	st, _ := serviceTestDB(t)
	seedServiceViews(t, st, time.Now())
	policy := &rbac.V3Policy{Store: st}
	user, token, e := st.Login(t.Context(), "admin@example.invalid", "isolated-password")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB.Exec(t.Context(), `INSERT INTO team_members(team_id,user_id) VALUES('ops',$1)`, user.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB.Exec(t.Context(), `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','services','manage','global','*'),('admin','traces','manage','global','*'),('admin','traces','view','global','*') ON CONFLICT DO NOTHING`); e != nil {
		t.Fatal(e)
	}
	api := httpapi.New(st, nil, true)
	New(st, policy, nil, nil).Register(api)
	v3traces.New(st, policy).Register(api)
	if e = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}, policy, st.Session, func(ctx context.Context, u core.User, module, action, env string) error {
		return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); e != nil {
		t.Fatal(e)
	}
	req := func(method, path, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
		}
	}
	path := "/services/" + encodeID("checkout-app", "test") + "/slo"
	w := req("GET", path, "test", "")
	check(w, 200)
	if !strings.Contains(w.Body.String(), `"target":null`) || !strings.Contains(w.Body.String(), `"revision":0`) ||
		!strings.Contains(w.Body.String(), `"window":"not_configured"`) {
		t.Fatal(w.Body.String())
	}
	check(req("PUT", path, "all", `{"expected_revision":0,"target":"99.9","window":"30d"}`), 409)
	check(req("PUT", path, "test", `{"expected_revision":0,"target":"99.95","window":"7d"}`), 200)
	check(req("PUT", path, "test", `{"expected_revision":0,"target":"99.9","window":"30d"}`), 409)
	w = req("GET", path, "test", "")
	check(w, 200)
	if !strings.Contains(w.Body.String(), `"window":"7d"`) || !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatal(w.Body.String())
	}
	var target string
	if e = st.DB.QueryRow(t.Context(), `SELECT slo_target_percent::text FROM v3_service_environment_profiles WHERE application_id='checkout-app' AND environment_id='test'`).Scan(&target); e != nil || target != "99.950" {
		t.Fatal(target, e)
	}
	check(req("PUT", path, "test", `{"expected_revision":1,"target":"99.0","window":"7d"}`), 422)
	w = req("POST", "/trace-views", "test", `{"scope":"app","name":"Errors","query":"{ status = error }"}`)
	check(w, 201)
	var created struct {
		ID string `json:"id"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil || created.ID == "" {
		t.Fatal(w.Body.String())
	}
	check(req("GET", "/trace-views?scope=app", "test", ""), 200)
	if _, e = st.DB.Exec(t.Context(), `DELETE FROM team_members WHERE user_id=$1`, user.ID); e != nil {
		t.Fatal(e)
	}
	check(req("POST", "/trace-views", "test", `{"scope":"app","name":"Ambiguous","query":""}`), 422)
	if _, e = st.DB.Exec(t.Context(), `INSERT INTO team_members(team_id,user_id) VALUES('ops',$1)`, user.ID); e != nil {
		t.Fatal(e)
	}
	check(req("DELETE", "/trace-views/"+created.ID, "test", ""), 204)
	check(req("DELETE", "/trace-views/"+created.ID, "test", ""), 404)
	if _, e = st.DB.Exec(t.Context(), `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='manage'`); e != nil {
		t.Fatal(e)
	}
	check(req("PUT", path, "test", `{"expected_revision":1,"target":"99.9","window":"30d"}`), 403)
	var revision int
	if e = st.DB.QueryRow(t.Context(), `SELECT revision FROM v3_service_slo_settings WHERE application_id='checkout-app'`).Scan(&revision); e != nil || revision != 1 {
		t.Fatal(revision, e)
	}
}
func TestSLOValidationRequiresRevisionAndSupportedTargets(t *testing.T) {
	if validSLO(sloPut{}) {
		t.Fatal("missing fields accepted")
	}
	zero := int64(0)
	for _, in := range []sloPut{{&zero, "99.95", "7d"}, {&zero, "99.9", "30d"}} {
		if !validSLO(in) {
			t.Fatal(in)
		}
	}
}
