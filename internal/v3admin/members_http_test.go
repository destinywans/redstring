package v3admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestMembersHTTPRespectsV3ScopeAndShowsDisabledAssignments(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_members_test_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	actor, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Directory Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES
 ('member-reader','Member Reader'),('dev-member','Dev Member'),('test-member','Test Member'),
	 ('team-member','Team Member'),('app-member','App Member'),('dev-admission','Dev Admission')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('member-reader','environment','view','environment','dev'),
 ('member-reader','members','view','global','*'),
 ('dev-member','services','view','environment','dev'),
	 ('dev-member','environment','view','environment','dev'),
 ('test-member','services','view','environment','test'),
	 ('test-member','environment','view','environment','test'),
	 ('dev-admission','environment','view','environment','dev'),
 ('team-member','services','view','team','ops'),
 ('app-member','services','view','application','app')`,
		`INSERT INTO teams(id,name) VALUES('ops','运维部')`,
		`INSERT INTO applications(id,name,key,environment,owner_team_id) VALUES('app','App','app','dev','ops')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO users(id,email,name,role,password_hash,department_id,disabled) VALUES
 ('dev-user','dev@example.invalid','张明','viewer','',NULL,false),
 ('disabled-user','disabled@example.invalid','停用成员','viewer','','ops',true),
 ('team-user','team@example.invalid','团队成员','viewer','','ops',false),
 ('department-user','department@example.invalid','部门成员','viewer','','ops',false),
 ('app-user','app@example.invalid','应用成员','viewer','',NULL,false),
	 ('test-user','test@example.invalid','测试成员','viewer','',NULL,false),
	 ('unassigned-user','unassigned@example.invalid','未分配成员','viewer','',NULL,false),
	 ('legacy-only-user','legacy-only@example.invalid','待授权旧成员','viewer','',NULL,false)`,
		`DELETE FROM rbac_user_roles WHERE user_id='unassigned-user' AND role_id='viewer'`,
		`UPDATE users SET phone='scope-secret-119' WHERE id='dev-user'`,
		`INSERT INTO rbac_department_roles(department_id,role_id) VALUES
 ('ops','team-member'),('ops','dev-admission')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES
 ('dev-user','dev-member'),('dev-user','test-member'),
 ('disabled-user','dev-member'),('team-user','team-member'),
	 ('app-user','app-member'),('app-user','dev-admission'),('test-user','test-member')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'member-reader')`, actor.ID); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	admin, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(path, environment, session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d body=%s; want %d", w.Code, w.Body.String(), status)
		}
	}
	assertPartial := func(w *httptest.ResponseRecorder, view membersView, all, admin, disabled int) {
		t.Helper()
		if view.Counts.All != all || view.Counts.Admin != admin || view.Counts.Disabled != disabled ||
			view.Counts.NoMFA != nil || view.MFAGraceDays != nil || view.CompleteV3DTO {
			t.Fatalf("incorrect partial directory counts or capability flags: body=%s", w.Body.String())
		}
		if len(view.Org) == 0 || view.Org[0].Name != "可见成员" || view.Org[0].Count != all {
			t.Fatalf("directory root must describe the scoped visible set: %+v", view.Org)
		}
		wantUnavailable := map[string]bool{
			"groups": true, "org_hierarchy": true, "legacy_role_mapping": true, "items[].mfa": true,
			"items[].last_login": true, "counts.no_mfa": true,
			"mfa_grace_days": true, "directory_sync_source": true,
		}
		if len(view.UnavailableFields) != len(wantUnavailable) {
			t.Fatalf("unavailable fields=%v", view.UnavailableFields)
		}
		for _, field := range view.UnavailableFields {
			if !wantUnavailable[field] {
				t.Fatalf("unexpected or duplicate unavailable field %q: %v", field, view.UnavailableFields)
			}
			delete(wantUnavailable, field)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if string(raw["complete_v3_dto"]) != "false" || string(raw["mfa_grace_days"]) != "null" {
			t.Fatalf("partial fields must be explicit: body=%s", w.Body.String())
		}
		var counts map[string]json.RawMessage
		if err := json.Unmarshal(raw["counts"], &counts); err != nil || string(counts["no_mfa"]) != "null" {
			t.Fatalf("unknown MFA count must be explicit JSON null: body=%s err=%v", w.Body.String(), err)
		}
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(raw["items"], &items); err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if string(item["last_login"]) != "null" {
				t.Fatalf("unknown last login must be explicit JSON null: item=%s", item["id"])
			}
		}
	}
	assertStatus(request("/api/v1/members", "dev", ""), 401)
	assertStatus(request("/api/v1/members", "test", token), 403)
	assertStatus(request("/api/v1/members?filter=unknown", "dev", token), 422)
	w := request("/api/v1/members?filter=no_mfa", "dev", token)
	assertStatus(w, 503)
	var mfaError struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &mfaError); err != nil || mfaError.Error.Code != "member_mfa_unavailable" {
		t.Fatalf("no_mfa filter claimed unverifiable MFA evidence: body=%s err=%v", w.Body.String(), err)
	}
	var view membersView
	for _, environment := range []string{"dev", "all"} {
		w = request("/api/v1/members", environment, token)
		assertStatus(w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		assertPartial(w, view, 7, 1, 1)
		byID := map[string]member{}
		for _, item := range view.Items {
			byID[item.ID] = item
		}
		if len(byID) != 7 || byID["test-user"].ID != "" || byID["unassigned-user"].ID != "" || byID["legacy-only-user"].ID != "" ||
			byID["team-user"].ID == "" || byID["department-user"].ID == "" || byID["app-user"].ID == "" ||
			byID["disabled-user"].Status != "disabled" {
			t.Fatalf("%s directory leaked test member: %+v, counts=%+v", environment, view.Items, view.Counts)
		}
		if len(byID["dev-user"].Roles) != 1 || byID["dev-user"].Roles[0] != "Dev Member" ||
			byID["dev-user"].MFA != "na" || byID["dev-user"].LastLogin != nil {
			t.Fatalf("%s leaked cross-environment role or guessed MFA: %+v", environment, byID["dev-user"])
		}
		w = request("/api/v1/members?q=test%40example.invalid", environment, token)
		assertStatus(w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Items) != 0 {
			t.Fatalf("%s search leaked test-only member: body=%s err=%v", environment, w.Body.String(), err)
		}
	}
	w = request("/api/v1/members?role=dev-member&role=NoSuchRole&source=system", "dev", token)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 2 || view.Counts.All != 2 {
		t.Fatalf("directory facets must match current visible role/source: %+v", view)
	}
	w = request("/api/v1/members", "all", adminToken)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	assertPartial(w, view, 10, 1, 1)
	globalIDs := map[string]bool{}
	for _, item := range view.Items {
		globalIDs[item.ID] = true
	}
	if !globalIDs["test-user"] || !globalIDs["unassigned-user"] || !globalIDs["legacy-only-user"] {
		t.Fatalf("global reader could not inspect test-only or unadmitted member: %+v", view.Items)
	}
	englishRequest := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil)
	englishRequest.Header.Set("X-AIOps-Environment", "dev")
	englishRequest.Header.Set("Accept-Language", "en")
	englishRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w = httptest.NewRecorder()
	api.ServeHTTP(w, englishRequest)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Org) == 0 || view.Org[0].Name != "Visible members" {
		t.Fatalf("English directory root is not scoped: body=%s err=%v", w.Body.String(), err)
	}
	w = request("/api/v1/members?filter=disabled", "dev", token)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Items) != 1 || view.Items[0].ID != "disabled-user" {
		t.Fatalf("disabled filter body=%s err=%v", w.Body.String(), err)
	}
	w = request("/api/v1/members?q=zhangming", "dev", token)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Items) != 1 || view.Items[0].ID != "dev-user" {
		t.Fatalf("pinyin filter body=%s err=%v", w.Body.String(), err)
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	w = request("/api/v1/members", "dev", token)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	assertPartial(w, view, 7, 1, 1)
	byID := map[string]member{}
	for _, item := range view.Items {
		byID[item.ID] = item
	}
	if len(byID) != 7 || byID["app-user"].ID == "" ||
		byID["team-user"].ID == "" || byID["department-user"].ID == "" {
		t.Fatalf("archiving an app revoked independent dev admission: %+v counts=%+v", view.Items, view.Counts)
	}
	hasRole := func(roles []string, wanted string) bool {
		for _, role := range roles {
			if role == wanted {
				return true
			}
		}
		return false
	}
	for _, id := range []string{"app-user", "team-user", "department-user"} {
		if !hasRole(byID[id].Roles, "Dev Admission") || hasRole(byID[id].Roles, "App Member") || hasRole(byID[id].Roles, "Team Member") {
			t.Fatalf("%s retained an archived app/team role or lost dev admission: %+v", id, byID[id].Roles)
		}
	}
	t.Run("queued directory read rechecks actor after membership revocation", func(t *testing.T) {
		raceActor, err := st.CreateUser(ctx, core.User{Email: "race-reader@example.invalid", Name: "Race Reader", Role: "viewer"}, "isolated-race-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'member-reader')`, raceActor.ID); err != nil {
			t.Fatal(err)
		}
		_, raceToken, err := st.Login(ctx, "race-reader@example.invalid", "isolated-race-password")
		if err != nil {
			t.Fatal(err)
		}
		before := request("/api/v1/members", "dev", raceToken)
		if before.Code != 200 {
			t.Fatalf("race reader initial status=%d body=%s", before.Code, before.Body.String())
		}
		if !strings.Contains(before.Body.String(), "dev@example.invalid") || !strings.Contains(before.Body.String(), "scope-secret-119") {
			t.Fatalf("race reader was not initially authorized for the sensitive directory row: %s", before.Body.String())
		}
		readerRole, err := (&rbac.Service{Store: st}).Role(ctx, "member-reader")
		if err != nil {
			t.Fatal(err)
		}
		blocker, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
			t.Fatal(err)
		}
		operationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		legacyResult := make(chan error, 1)
		go func() {
			_, err := (&rbac.Service{Store: st}).SaveAssociations(
				store.WithPrincipal(operationCtx, admin), "member-reader",
				rbac.Associations{Revision: readerRole.Revision, Users: rbac.Delta{Remove: []string{raceActor.ID}}},
			)
			legacyResult <- err
		}()
		waitForQueue := func(wantExclusive, wantShared int) {
			t.Helper()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				var exclusive, shared int
				err := adminDB.QueryRow(operationCtx, `SELECT
				 count(*) FILTER (WHERE l.mode='ExclusiveLock'),
				 count(*) FILTER (WHERE l.mode='ShareLock')
				 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
				 WHERE l.locktype='advisory' AND NOT l.granted AND l.objid::bigint=$1
				 AND l.objsubid=1 AND a.application_name=$2`, int64(752018992), schema).Scan(&exclusive, &shared)
				if err != nil {
					t.Fatal(err)
				}
				if exclusive >= wantExclusive && shared >= wantShared {
					return
				}
				select {
				case <-tick.C:
				case <-deadline.C:
					t.Fatalf("identity lock queue has exclusive=%d shared=%d; want at least %d/%d", exclusive, shared, wantExclusive, wantShared)
				case <-operationCtx.Done():
					t.Fatal("identity lock queue timed out")
				}
			}
		}
		waitForQueue(1, 0)
		readResult := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil).WithContext(operationCtx)
			r.Header.Set("X-AIOps-Environment", "dev")
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: raceToken})
			result := httptest.NewRecorder()
			api.ServeHTTP(result, r)
			readResult <- result
		}()
		// The GET passed middleware while the old grant was committed and
		// entered the handler behind the already queued legacy writer.
		waitForQueue(1, 1)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-legacyResult:
			if err != nil {
				t.Fatalf("legacy association removal: %v", err)
			}
		case <-operationCtx.Done():
			t.Fatal("legacy association removal timed out")
		}
		select {
		case result := <-readResult:
			if result.Code != 403 {
				t.Fatalf("queued directory read status=%d body=%s; want 403", result.Code, result.Body.String())
			}
			body := result.Body.String()
			if strings.Contains(body, "dev@example.invalid") || strings.Contains(body, "张明") || strings.Contains(body, "scope-secret-119") {
				t.Fatalf("revoked reader received member PII: %s", body)
			}
		case <-operationCtx.Done():
			t.Fatal("queued directory read timed out")
		}
	})
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='member-reader' AND module='members'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(request("/api/v1/members", "dev", token), 403)
}
