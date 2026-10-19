package v3alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSilenceListRegisteredHTTPUsesPersistedTeamAndAdministrativeScope(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_silences_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 2
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-silence-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO teams(id,name) VALUES('ops','Ops'),('other','Other')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','test'),('viewer','environment','view','environment','staging'),
 ('viewer','silences','view','team','ops')`,
		`INSERT INTO applications(id,key,name,environment,owner_team_id,archived) VALUES
	 ('owned-id','owned','Owned','test','ops',false),('unbound-id','unbound','Unbound','test','ops',false),('hidden-id','hidden','Hidden','test','other',false),
 ('archived','archived','Archived','test','ops',true),('stage','stage','Stage','staging','ops',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
	 ('owned-test','owned-id','test','Test',false),('unbound-test','unbound-id','test','Test',false),('hidden-test','hidden-id','test','Test',false),
 ('archived-test','archived','test','Test',false),('stage-staging','stage','staging','Staging',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
	 ('owned-id','test'),('unbound-id','test'),('hidden-id','test'),('archived','test'),('stage','staging')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	makeSilence := func(id, env, app string) Silence {
		s := silence(t, id, "active", env)
		if app != "" {
			s.Matchers = append(s.Matchers, Matcher{Name: "application", Value: app, IsEqual: true})
		}
		return s
	}
	owned := makeSilence("owned", "test", "owned")
	// A platform-created silence stores the creator's user ID upstream.
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email='viewer@example.invalid'`).Scan(&owned.CreatedBy); err != nil {
		t.Fatal(err)
	}
	// A current catalog key also grants no ownership without an explicit binding.
	unboundKey := makeSilence("unbound-key", "test", "unbound")
	// A provider label equal to a persisted ID is not an implicit binding.
	unboundID := makeSilence("unbound-id", "test", "owned-id")
	hidden := makeSilence("hidden", "test", "hidden")
	archived := makeSilence("archived", "test", "archived")
	wide := makeSilence("wide", "test", "")
	regex := makeSilence("regex", "test", "")
	regex.Matchers = append(regex.Matchers, Matcher{Name: "application", Value: "owned|hidden", IsEqual: true, IsRegex: true})
	cross := makeSilence("cross", "test", "owned")
	cross.Matchers[0].IsRegex = true
	conflict := makeSilence("conflict", "test", "owned")
	conflict.Matchers = append(conflict.Matchers, Matcher{Name: "application", Value: "hidden", IsEqual: true})
	mixedAppNegative := makeSilence("mixed-app-negative", "test", "owned")
	mixedAppNegative.Matchers = append(mixedAppNegative.Matchers, Matcher{Name: "application", Value: "owned", IsEqual: false})
	mixedAppRegex := makeSilence("mixed-app-regex", "test", "owned")
	mixedAppRegex.Matchers = append(mixedAppRegex.Matchers, Matcher{Name: "application", Value: "owned|hidden", IsEqual: true, IsRegex: true})
	mixedEnvNegative := makeSilence("mixed-env-negative", "test", "owned")
	mixedEnvNegative.Matchers = append(mixedEnvNegative.Matchers, Matcher{Name: "environment", Value: "test", IsEqual: false})
	mixedEnvRegex := makeSilence("mixed-env-regex", "test", "owned")
	mixedEnvRegex.Matchers = append(mixedEnvRegex.Matchers, Matcher{Name: "environment", Value: "test|staging", IsEqual: true, IsRegex: true})
	foreign := makeSilence("foreign", "staging", "stage")
	stage := makeSilence("stage", "staging", "stage")
	alert := func(env, app string, ids ...string) Alert {
		a := Alert{Labels: map[string]string{"environment": env, "application": app}}
		a.Status.SilencedBy = ids
		return a
	}
	var stageFails atomic.Bool
	var malformedInhibition atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/staging/") && stageFails.Load() {
			w.WriteHeader(503)
			return
		}
		var payload any
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v2/silences"):
			payload = []Silence{owned, unboundKey, unboundID, hidden, archived, wide, regex, cross, conflict,
				mixedAppNegative, mixedAppRegex, mixedEnvNegative, mixedEnvRegex, foreign, stage}
		case strings.HasSuffix(r.URL.Path, "/api/v2/alerts"):
			payload = []Alert{alert("test", "owned", "owned", "owned"), alert("test", "hidden", "owned", "hidden", "wide"), alert("test", "archived", "archived"), alert("staging", "stage", "stage")}
		case strings.HasSuffix(r.URL.Path, "/api/v2/status"):
			original := `inhibit_rules:
  - source_matchers: ['environment = "test"', 'application 	= "owned"', 'rule="owned"']
    target_matchers: ['environment = "test"', 'application = "owned"']
  - source_matchers: ['environment="test"', 'application="owned"', 'rule="cross"']
    target_matchers: ['environment="test"', 'application="hidden"']
  - source_matchers: ['environment="test"', 'application="hidden"', 'rule="hidden"']
    target_matchers: ['environment="test"', 'application="hidden"']
  - source_matchers: ['environment="test"', 'rule="broad"']
    target_matchers: ['environment="test"']
  - source_matchers: ['environment="test"', 'application="unbound"', 'rule="unbound"']
    target_matchers: ['environment="test"', 'application="unbound"']
  - source_matchers: ['environment = "test"', 'application = "owned"', 'application =~ "owned|hidden"', 'rule="mixed-app"']
    target_matchers: ['environment="test"', 'application="owned"']
  - source_matchers: ['environment = "test"', 'application = "owned"', 'application !~ "hidden"', 'rule="mixed-app-negative"']
    target_matchers: ['environment="test"', 'application="owned"']
  - source_matchers: ['environment = "test"', 'environment != "staging"', 'application = "owned"', 'rule="mixed-env"']
    target_matchers: ['environment="test"', 'application="owned"']
  - source_matchers: ['environment = "test"', 'environment =~ "test|staging"', 'application = "owned"', 'rule="mixed-env-regex"']
    target_matchers: ['environment="test"', 'application="owned"']
  - source_matchers: ['environment="staging"', 'application="stage"', 'rule="foreign"']
    target_matchers: ['environment="staging"', 'application="stage"']
`
			if malformedInhibition.Load() {
				original += `  - source_matchers: ['environment="test",application="hidden"']
    target_matchers: ['environment="test"']
`
			}
			payload = map[string]any{"config": map[string]string{"original": original}}
		default:
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer provider.Close()
	source, err := New(map[string]Endpoint{
		"test":    {URL: provider.URL + "/test", AllowHTTP: true},
		"staging": {URL: provider.URL + "/staging", AllowHTTP: true},
	}, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	(&Service{Source: source, Catalog: DatabaseCatalog{DB: db}, Access: policy,
		EnvironmentLabels: map[string]string{"test": "environment", "staging": "environment"},
		ApplicationBindings: map[string]map[string]string{
			"test":    {"owned": "owned-id", "hidden": "hidden-id", "archived": "archived"},
			"staging": {"stage": "stage"},
		},
		Now: func() time.Time { return time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC) }}).Register(api)
	request := func(env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/silences?state=all", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		r.Header.Set("X-AIOps-Environment", env)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	preview := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/silences/preview", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		r.Header.Set("X-AIOps-Environment", "test")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	inhibitions := func(env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/inhibitions", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		r.Header.Set("X-AIOps-Environment", env)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	// Rule IDs are the public stable identity; matcher values are deliberately
	// redacted. The controlled source has known order: team permits only 0,
	// environment administrator permits exactly 0..3, never mixed/unknown ones.
	configuredRules, err := source.ListInhibitionRules(ctx, "test")
	if err != nil || len(configuredRules) != 10 {
		t.Fatalf("controlled inhibition rules: count=%d err=%v", len(configuredRules), err)
	}
	assertInhibitions := func(got *httptest.ResponseRecorder, indices []int, unscoped bool) {
		t.Helper()
		var response struct {
			Items []struct {
				ID             string `json:"id"`
				Source, Target []map[string]json.RawMessage
				Stat           string `json:"stat"`
				Active         any    `json:"active"`
				Hits7D         any    `json:"hits_7d"`
			} `json:"items"`
			Partial         bool     `json:"partial"`
			UnscopedPresent bool     `json:"unscoped_present"`
			Unavailable     []string `json:"unavailable"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil || got.Code != 206 || !response.Partial || response.UnscopedPresent != unscoped ||
			// No reviewed GitOps inhibition repository here, so drafts are unavailable too (PA-123).
			len(response.Items) != len(indices) || strings.Join(response.Unavailable, ",") != "activity,hit_counts,inhibition_drafts" {
			t.Fatalf("inhibition scope/partial contract: status=%d body=%s err=%v", got.Code, got.Body.String(), err)
		}
		for i, index := range indices {
			raw, err := json.Marshal(struct {
				Environment string
				Index       int
				Rule        InhibitionRule
			}{"test", index, configuredRules[index]})
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(raw)
			item := response.Items[i]
			if item.ID != hex.EncodeToString(hash[:12]) || item.Stat == "" || item.Active != nil || item.Hits7D != nil || len(item.Source) == 0 || len(item.Target) == 0 {
				t.Fatalf("unexpected inhibition identity/activity at index %d: %+v", index, item)
			}
			for _, matcher := range append(item.Source, item.Target...) {
				var redacted bool
				if _, present := matcher["v"]; present || json.Unmarshal(matcher["value_redacted"], &redacted) != nil || !redacted {
					t.Fatalf("matcher value was not fully redacted for rule %s", item.ID)
				}
			}
		}
	}
	assertInhibitions(inhibitions("test"), []int{0}, false)
	malformedInhibition.Store(true)
	if got := inhibitions("test"); got.Code != 503 || !strings.Contains(got.Body.String(), "invalid_inhibition_source") ||
		strings.Contains(got.Body.String(), `"items"`) || strings.Contains(got.Body.String(), `"partial"`) {
		t.Fatalf("malformed registered inhibition status=%d body=%s", got.Code, got.Body.String())
	}
	malformedInhibition.Store(false)
	if p := preview(`{"matchers":[{"k":"application","op":"=","v":"owned"}]}`); p.Code != 200 || !strings.Contains(p.Body.String(), `"affects":1`) {
		t.Fatalf("team preview status=%d body=%s", p.Code, p.Body.String())
	}
	if p := preview(`{"matchers":[{"k":"application","op":"=","v":"hidden"}]}`); p.Code != 404 || strings.Contains(p.Body.String(), `"affects"`) {
		t.Fatalf("hidden app preview status=%d body=%s", p.Code, p.Body.String())
	}
	if p := preview(`{"matchers":[{"k":"alertname","op":"!=","v":"unmatched"}]}`); p.Code != 403 {
		t.Fatalf("broad preview without environment grant status=%d body=%s", p.Code, p.Body.String())
	}
	w := request("test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"owned"`) || !strings.Contains(w.Body.String(), `"affects":1`) || !strings.Contains(w.Body.String(), `"active":1`) ||
		!strings.Contains(w.Body.String(), `"creator":"Viewer"`) || !strings.Contains(w.Body.String(), `"empty_reason":null`) {
		t.Fatalf("team visible status=%d body=%s", w.Code, w.Body.String())
	}
	for _, denied := range []string{"unbound-key", "unbound-id", "hidden", "archived", "wide", "regex", "cross", "conflict", "mixed-app-negative", "mixed-app-regex", "mixed-env-negative", "mixed-env-regex", "foreign", "stage"} {
		if strings.Contains(w.Body.String(), `"id":"`+denied+`"`) {
			t.Fatalf("team leaked %s: %s", denied, w.Body.String())
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
	 VALUES('viewer','silences','view','application','hidden-id')`); err != nil {
		t.Fatal(err)
	}
	w = request("test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":2`) || !strings.Contains(w.Body.String(), `"id":"hidden"`) || strings.Contains(w.Body.String(), `"id":"wide"`) {
		t.Fatalf("application grant status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='silences' AND action='view' AND scope_kind='application' AND scope_id='hidden-id'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='other' WHERE id='owned-id'`); err != nil {
		t.Fatal(err)
	}
	// Nothing readable before the upstream read is a known empty scope (PA-09).
	emptyScope := func(got *httptest.ResponseRecorder, what string) {
		t.Helper()
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"empty_reason":"no_authorized_target"`) ||
			!strings.Contains(got.Body.String(), `"items":[]`) || strings.Contains(got.Body.String(), `"id":"owned"`) ||
			strings.Contains(got.Body.String(), `"unscoped_present":true`) {
			t.Fatalf("%s status=%d body=%s", what, got.Code, got.Body.String())
		}
	}
	emptyScope(request("test"), "ownership change")
	emptyScope(inhibitions("test"), "revoked team inhibition scope")
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='ops' WHERE id='owned-id'`); err != nil {
		t.Fatal(err)
	}
	// Reassigning the human-facing key to another application cannot transfer
	// the old silence. The explicit binding still targets the retired ID.
	for _, statement := range []string{
		`UPDATE applications SET key='retired-owned', archived=true WHERE id='owned-id'`,
		`INSERT INTO applications(id,key,name,environment,owner_team_id,archived) VALUES ('new-owner-id','owned','New Owner','test','ops',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES ('new-owner-test','new-owner-id','test','Test',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('new-owner-id','test')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	emptyScope(request("test"), "reassigned key gained old silence")
	for _, statement := range []string{
		`UPDATE applications SET key='new-owned', archived=true WHERE id='new-owner-id'`,
		`UPDATE applications SET key='owned', archived=false WHERE id='owned-id'`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
	 ('viewer','silences','manage','environment','test')`); err != nil {
		t.Fatal(err)
	}
	w = request("test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":1`) || strings.Contains(w.Body.String(), `"id":"wide"`) {
		t.Fatalf("manage without view status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','silences','view','environment','test')`); err != nil {
		t.Fatal(err)
	}
	w = request("test")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"active":4`) || !strings.Contains(w.Body.String(), `"unscoped_present":true`) {
		t.Fatalf("admin visible status=%d body=%s", w.Code, w.Body.String())
	}
	assertInhibitions(inhibitions("test"), []int{0, 1, 2, 3}, true)
	if p := preview(`{"matchers":[{"k":"environment","op":"=","v":"test"}]}`); p.Code != 200 || !strings.Contains(p.Body.String(), `"affects":3`) {
		t.Fatalf("environment-wide preview status=%d body=%s", p.Code, p.Body.String())
	}
	for _, visible := range []string{"owned", "hidden", "wide", "regex"} {
		if !strings.Contains(w.Body.String(), `"id":"`+visible+`"`) {
			t.Fatalf("admin missing %s: %s", visible, w.Body.String())
		}
	}
	for _, denied := range []string{"unbound-key", "unbound-id", "cross", "archived", "conflict", "mixed-app-negative", "mixed-app-regex", "mixed-env-negative", "mixed-env-regex"} {
		if strings.Contains(w.Body.String(), `"id":"`+denied+`"`) {
			t.Fatalf("admin saw unproven ownership %s: %s", denied, w.Body.String())
		}
	}
	w = request("all")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"active":6`) || strings.Count(w.Body.String(), `"id":"stage"`) != 1 || strings.Count(w.Body.String(), `"id":"foreign"`) != 1 {
		t.Fatalf("complete all status=%d body=%s", w.Code, w.Body.String())
	}
	stageFails.Store(true)
	w = request("all")
	if w.Code != 503 || strings.Contains(w.Body.String(), `"counts"`) {
		t.Fatalf("partial all status=%d body=%s", w.Code, w.Body.String())
	}
	if got := inhibitions("all"); got.Code != 503 || strings.Contains(got.Body.String(), `"items"`) ||
		strings.Contains(got.Body.String(), `"unavailable_environments"`) {
		t.Fatalf("partial inhibition source status=%d body=%s", got.Code, got.Body.String())
	}
}
