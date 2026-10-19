package v3settings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

// PA-64: /me reports the caller's effective grants for the selected
// environment with the policy's scope rules: environment-wide grants are
// "all", application/team grants "scoped", approve "conditional", and an
// application grant outside this environment is absent.
func TestMeReportsEffectivePermissions(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-me-permissions-password"
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO applications(id,key,name) VALUES('app-x','app-x','App X'),('app-y','app-y','App Y')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-x-dev','app-x','dev','Dev'),('app-y-test','app-y','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-x','dev'),('app-y','test')`,
		`INSERT INTO teams(id,name) VALUES('ops','Ops')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES
 ('viewer','environment','view','environment','dev','standard'),
 ('viewer','conversation','view','environment','dev','standard'),
 ('viewer','conversation','change','application','app-x','standard'),
 ('viewer','knowledge','manage','application','app-y','standard'),
 ('viewer','events','change','team','ops','standard'),
 ('viewer','services','view','environment','test','standard'),
 ('viewer','proposals','approve','global','*','passkey')`,
	} {
		if _, err := st.DB.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	service := New(st, nil)
	service.MeEvidence = MeEvidence{MFAInstanceID: "i-test", PasskeyRPID: "example.invalid", Oncall: &meOncallSubstitute{status: freshOncall(true)}}
	api := httpapi.New(st, nil, true)
	service.Register(api)
	api.MarkV3Route("GET /api/v1/me", "", "")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		&rbac.V3Policy{Store: st}, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	var got meView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("GET /me: %d %s", w.Code, w.Body.String())
	}
	want := map[string]map[string]map[string]string{"dev": {
		"environment":  {"view": "all"},
		"conversation": {"view": "all", "change": "scoped"},
		"events":       {"change": "scoped"},
		"proposals":    {"approve": "conditional"},
	}}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Fatalf("permissions=%v want %v", got.Permissions, want)
	}
}
