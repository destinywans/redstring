package v3alertrules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

// PA-108: a member who may only view alert rules is refused before any write
// source is checked, so the refusal never says which write source this
// environment lacks. Administrators still learn what is missing.
func TestAlertRuleWritesDeniedBeforeWriteSourceCheck(t *testing.T) {
	db := enableRecoveryDB(t)
	ctx := t.Context()
	st := &store.Store{DB: db}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-rule-authz-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	admin, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-rule-authz-password")
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	investigation := "INV-" + store.ID()
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','test'),('viewer','alert_rules','view','environment','test')`,
		`INSERT INTO applications(id,key,name,environment) VALUES('allowed','allowed','allowed','test')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('allowed-test','allowed','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('allowed','test')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO investigations(id,application_id,environment_id,title,status,created_by)
 VALUES($1,'allowed','allowed-test','Authorization order','open',$2)`, investigation, admin.ID); err != nil {
		t.Fatal(err)
	}
	rule := ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "ActiveRule", Expression: "up == 0", Duration: 60,
		Labels: map[string]string{"application": "allowed", "environment": "test", "severity": "P2"}, State: "inactive"}
	policy := &rbac.V3Policy{Store: st}
	reader := mustService(t, DatabaseCatalog{DB: db}, policy, disableSource{enableIDSource: &enableIDSource{}, rules: []ObservedRule{rule}},
		RuleGroupBinding{EnvironmentID: "test", ApplicationID: "allowed", File: testRuleFile, Group: "g"})
	// No draft repository, deployment target, approval worker or trial runtime.
	writer := NewWriteService(db, policy, reader, nil)
	api := httpapi.New(st, nil, true)
	writer.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	send := func(token, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "test")
		r.Header.Set("Idempotency-Key", "authz-"+store.ID())
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	spec := `"application_id":"allowed","environment_id":"test","group":"g","name":"NewRule","expr":"up == 0","for":"1m",` +
		`"labels":{"application":"allowed","environment":"test","severity":"P2"}`
	yaml, err := json.Marshal("groups:\n- name: g\n  rules:\n  - alert: ImportedRule\n    expr: up == 0\n    for: 1m\n" +
		"    labels:\n      application: allowed\n      environment: test\n      severity: P2\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body string
		// What the viewer gets: 403 for a target they can see, 404 otherwise.
		denied int
		// What an administrator gets: the missing write source; "" when the
		// target itself is unavailable to everyone.
		missing string
	}{
		{"create draft", "POST", "/api/v1/alert-rules", `{` + spec + `,"mode":"draft"}`, 403, "not_connected"},
		{"create enabled", "POST", "/api/v1/alert-rules", `{` + spec + `,"mode":"enabled","investigation_id":"` + investigation + `"}`, 403, "rule_applier_not_connected"},
		{"create dry run", "POST", "/api/v1/alert-rules", `{` + spec + `,"mode":"dry_run"}`, 403, "dry_run_isolation_unavailable"},
		{"import", "POST", "/api/v1/alert-rules/import", `{"application_id":"allowed","yaml":` + string(yaml) + `}`, 403, "not_connected"},
		{"update", "PUT", "/api/v1/alert-rules/" + store.ID(), `{"expected_version":1,"rule":{` + spec + `,"mode":"draft"}}`, 403, "not_connected"},
		{"disable", "POST", "/api/v1/alert-rules/" + toDetail("test", "allowed", rule).ID + "/disable", `{"investigation_id":"` + investigation + `"}`, 403, "rule_applier_not_connected"},
		// Another environment's rule is not found here, for anyone.
		{"disable another environment's rule", "POST", "/api/v1/alert-rules/" + toDetail("staging", "allowed", rule).ID + "/disable", `{"investigation_id":"` + investigation + `"}`, 404, ""},
		{"recheck an unknown execution", "POST", "/api/v1/executions/" + store.ID() + "/recheck", `{}`, 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := send(viewerToken, tc.method, tc.path, tc.body)
			if w.Code != tc.denied || strings.Contains(w.Body.String(), "connected") || strings.Contains(w.Body.String(), "unavailable") {
				t.Fatalf("viewer: %d %s", w.Code, w.Body.String())
			}
			w = send(adminToken, tc.method, tc.path, tc.body)
			if tc.missing == "" {
				if w.Code != http.StatusNotFound {
					t.Fatalf("admin: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"`+tc.missing+`"`) {
				t.Fatalf("admin: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
