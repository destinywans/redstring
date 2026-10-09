package v3conversation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
)

type migrationListAuthorizer struct{ ownerID string }

func (a migrationListAuthorizer) AuthorizeEnvironment(_ context.Context, user core.User, environment string) error {
	if user.ID == a.ownerID && environment == "test" {
		return nil
	}
	return store.ErrForbidden
}

func TestConversationListRealMigrationGrantAndPinyin(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_list_real_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, dropErr := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); dropErr != nil {
			t.Error(dropErr)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin-list@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	owner, err := st.CreateUser(ctx, core.User{Email: "list-owner@example.invalid", Name: "List Owner", Role: "viewer"}, "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "list-owner@example.invalid", "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		// The owner keeps the legacy viewer binding; drop the viewer's global
		// environment grant (PA-55) so access comes only from list-reader.
		{sql: `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='environment'`},
		{sql: `INSERT INTO rbac_roles(id,name) VALUES('list-reader','List Reader')`},
		{sql: `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'list-reader')`, args: []any{owner.ID}},
		{sql: `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('list-reader','environment','view','environment','test'),
 ('list-reader','conversation','view','environment','test'),
 ('list-reader','canvas','view','environment','test')`},
		{sql: `INSERT INTO v3_conversations(id,owner_id,environment_id,title) VALUES('C-list-real',$1,'test','结算排查')`, args: []any{owner.ID}},
		{sql: `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,services,nodes) VALUES
 ('CV-list-real',$1,'test','容量评估','analysis',ARRAY['billing-api'],
  '[{"id":"root","x":0,"y":0,"w":240,"icon":"layers","tone":"info","title":"主题","sub":"","blocks":[]}]'::jsonb)`, args: []any{owner.ID}},
	} {
		if _, err = db.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(db, nil, Options{})
	var items []map[string]any
	var total int
	var counts conversationListCounts
	var readErr error
	scoped, err := httpapi.NewV3EnvironmentHandler(
		httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		migrationListAuthorizer{owner.ID}, st.Session,
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			tab, mode, query := r.URL.Query().Get("tab"), r.URL.Query().Get("mode"), r.URL.Query().Get("q")
			items, total, counts, readErr = svc.readOwnedList(r.Context(), token, owner, []string{"test"}, tab, mode, "", query, 1, 50, "zh-CN")
		}))
	if err != nil {
		t.Fatal(err)
	}
	read := func(tab, mode, query string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/conversations?tab="+tab+"&mode="+mode+"&q="+query, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		scoped.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("scoped request status=%d body=%s", w.Code, w.Body.String())
		}
	}
	read("mine", "canvas", "rongliang")
	if readErr != nil || total != 1 || counts.Mine != 2 || counts.Archived != 0 || len(items) != 1 || items[0]["id"] != "CV-list-real" || items[0]["size"] != "1 个节点" {
		t.Fatalf("real migrated canvas list: items=%v total=%d counts=%+v err=%v", items, total, counts, readErr)
	}
	read("mine", "chat", "jiesuan")
	if readErr != nil || total != 1 || len(items) != 1 || items[0]["id"] != "C-list-real" {
		t.Fatalf("real migrated chat pinyin: items=%v total=%d err=%v", items, total, readErr)
	}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{sql: `INSERT INTO teams(id,name) VALUES('list-team','List Team')`},
		{sql: `INSERT INTO applications(id,name,key,owner_team_id) VALUES('list-app','List App','list-app','list-team')`},
		{sql: `INSERT INTO application_environments(id,application_id,key,name) VALUES('list-ae','list-app','test','Test')`},
		{sql: `INSERT INTO v3_application_environments(application_id,environment_id) VALUES('list-app','test')`},
		{sql: `INSERT INTO team_members(team_id,user_id) VALUES('list-team',$1)`, args: []any{owner.ID}},
		{sql: `INSERT INTO investigations(id,application_id,environment_id,title,status,created_by) VALUES
 ('INV-list-mine','list-app','list-ae','Created by owner','open',$1),
 ('INV-list-triage','list-app','list-ae','Automated work','closed',NULL),
 ('INV-list-unknown','list-app','list-ae','Imported history','imported',NULL)`, args: []any{owner.ID}},
		{sql: `INSERT INTO branches(id,investigation_id,title) VALUES
 ('list-root-mine','INV-list-mine','Main'),
 ('list-root-triage','INV-list-triage','Main'),
 ('list-root-unknown','INV-list-unknown','Main')`},
		{sql: `INSERT INTO context_revisions(branch_id,revision,messages) VALUES
 ('list-root-mine',0,'[]'),('list-root-triage',0,'[]'),('list-root-unknown',0,'[]')`},
		{sql: `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,investigation_id,starts_at) VALUES
 ('INC-list-triage','alert','list-app','list-ae','list-fingerprint','Real alert','warning','firing','INV-list-triage',now())`},
		{sql: `INSERT INTO ops_automation_runs(id,environment_id,kind,incident_id,investigation_id,run_id,policy) VALUES
 ('list-auto','list-ae','initial','INC-list-triage','INV-list-triage','list-run','{}')`},
		{sql: `INSERT INTO runs(id,branch_id,request_id,request_hash,status,model_id,input) VALUES
 ('list-run','list-root-triage','automation:list-auto','digest','completed','model','{}')`},
	} {
		if _, err = db.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatal(err)
		}
	}
	read("team", "canvas", "")
	if readErr != nil || total != 3 || counts.Mine != 3 || counts.Team != 3 || counts.Triage != 1 || len(items) != 3 {
		t.Fatalf("real migrated investigation team: items=%v total=%d counts=%+v err=%v", items, total, counts, readErr)
	}
	byID := map[string]map[string]any{}
	for _, item := range items {
		byID[item["id"].(string)] = item
	}
	if byID["INV-list-mine"]["source"] != "manual" || byID["INV-list-mine"]["tokens"] != nil ||
		byID["INV-list-triage"]["source"] != nil || byID["INV-list-triage"]["status"] != "resolved" ||
		byID["INV-list-unknown"]["source"] != nil || byID["INV-list-unknown"]["status"] != nil {
		t.Fatalf("historical source, status or usage fabricated: %v", byID)
	}
	read("triage", "canvas", "")
	if readErr != nil || total != 1 || len(items) != 1 || items[0]["id"] != "INV-list-triage" {
		t.Fatalf("real migrated auto-initial source: items=%v total=%d err=%v", items, total, readErr)
	}
	if _, err = db.Exec(ctx, `DELETE FROM team_members WHERE user_id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	read("team", "canvas", "")
	if readErr != nil || total != 0 || counts.Team != 0 || counts.Triage != 1 {
		t.Fatalf("current team membership not enforced: items=%v counts=%+v err=%v", items, counts, readErr)
	}
	if _, err = db.Exec(ctx, `INSERT INTO team_members(team_id,user_id) VALUES('list-team',$1)`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO teams(id,name) VALUES('list-other-team','Other List Team');
 UPDATE applications SET owner_team_id='list-other-team' WHERE id='list-app'`); err != nil {
		t.Fatal(err)
	}
	read("team", "canvas", "")
	if readErr != nil || total != 0 || counts.Team != 0 || counts.Triage != 1 {
		t.Fatalf("current application ownership not enforced: items=%v counts=%+v err=%v", items, counts, readErr)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_application_environments WHERE application_id='list-app' AND environment_id='test'`); err != nil {
		t.Fatal(err)
	}
	read("triage", "canvas", "")
	if readErr != nil || total != 0 || counts.Triage != 0 {
		t.Fatalf("revoked application environment mapping exposed triage: items=%v counts=%+v err=%v", items, counts, readErr)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='list-reader' AND module='environment'`); err != nil {
		t.Fatal(err)
	}
	read("mine", "", "")
	if !errors.Is(readErr, ErrForbidden) {
		t.Fatalf("revoked environment grant must hide all sources: %v", readErr)
	}
}
