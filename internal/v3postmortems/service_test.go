package v3postmortems

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/actions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestValidateCreateInput(t *testing.T) {
	base := CreateInput{IncidentID: "INC-1", Sections: []Section{{Key: "cause", Title: "Cause", Text: "Observed facts"}}, Actions: []Action{}, Status: "draft"}
	if !validate(base) {
		t.Fatal("valid draft rejected")
	}
	for _, change := range []func(*CreateInput){
		func(v *CreateInput) { v.Status = "published" },
		func(v *CreateInput) { v.IncidentID = "" },
		func(v *CreateInput) { v.Sections = nil },
		func(v *CreateInput) { v.Sections = append(v.Sections, v.Sections[0]) },
		func(v *CreateInput) {
			v.Status = "review"
			v.Sections = []Section{{Key: "cause", Title: "Cause", Text: ""}}
		},
		func(v *CreateInput) { v.Actions = []Action{{Text: "fix", Owner: "", Due: "tomorrow"}} },
	} {
		v := base
		change(&v)
		if validate(v) {
			t.Fatalf("invalid input accepted: %+v", v)
		}
	}
}

func TestSeverityDoesNotInventUnknownLevel(t *testing.T) {
	if got, ok := severity("critical"); !ok || got != "P1" {
		t.Fatalf("critical = %q, %v", got, ok)
	}
	if got, ok := severity("legacy-custom"); ok || got != "" {
		t.Fatalf("unknown severity invented: %q, %v", got, ok)
	}
}

func TestPostmortemHTTPUsesVerifiedCloseAndScopedTransaction(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_postmortem_http_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := actions.New(db, nil, "").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-postmortem-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	teamMember, err := st.CreateUser(ctx, core.User{Email: "team@example.invalid", Name: "Team Member", Role: "viewer"}, "isolated-team-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO teams(id,name) VALUES('ops','Operations'),('other-team','Other Team')`,
		`INSERT INTO applications(id,name,key,environment,owner_team_id) VALUES('app','App','app','dev','ops'),('other','Other','other','test','other-team'),('hidden','Hidden','hidden','dev','other-team')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev'),('other-test','other','test','Test'),('hidden-dev','hidden','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('other','test'),('hidden','dev')`,
		`INSERT INTO investigations(id,application_id,title,status) VALUES('closed-inv','app','Closed','closed'),('missing-inv','app','Missing','closed'),('other-inv','other','Other','closed'),('hidden-inv','hidden','Hidden','closed')`,
		`INSERT INTO branches(id,investigation_id,title) VALUES('branch','closed-inv','Main')`,
		`INSERT INTO context_revisions(branch_id,revision,messages) VALUES('branch',0,'[]')`,
		`INSERT INTO runs(id,branch_id,request_id,request_hash,status,model_id,input) VALUES('run','branch','req','test-hash','completed','model','{}')`,
		`INSERT INTO evidence(id,branch_id,run_id,kind,title,content,source) VALUES('evidence','branch','run','finding','Evidence','Observed','test')`,
		`INSERT INTO action_plans(id,investigation_id,proposal,digest,created_by) SELECT 'plan',id,'{}','digest',(SELECT id FROM users WHERE email='admin@example.invalid') FROM investigations WHERE id='closed-inv'`,
		`INSERT INTO executions(id,plan_id,status,created_by) SELECT 'execution','plan','succeeded',id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO investigation_reports(investigation_id,execution_id,report) VALUES('closed-inv','execution','{}')`,
		`INSERT INTO events(investigation_id,type,payload,created_at) VALUES('closed-inv','investigation.closed','{}',now()-interval '1 hour'),('missing-inv','other','{}',now()-interval '1 hour')`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at,investigation_id)
		 VALUES('ready','test','app','app-dev','ready','Real close','critical','resolved','resolved',now()-interval '2 hours','closed-inv'),
		       ('missing','test','app','app-dev','missing','No close','warning','resolved','resolved',now()-interval '2 hours','missing-inv'),
		       ('open','test','app','app-dev','open','Open','info','open','firing',now()-interval '2 hours',''),
		       ('other','test','other','other-test','other','Other app','info','resolved','resolved',now()-interval '2 hours','other-inv')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev'),('viewer','knowledge','view','application','app'),('viewer','events','view','application','app') ON CONFLICT DO NOTHING`,
		`INSERT INTO rbac_roles(id,name) VALUES('team-postmortems','Team Postmortems')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('team-postmortems','events','view','team','ops'),('team-postmortems','knowledge','view','team','ops'),('team-postmortems','knowledge','manage','team','ops')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'team-postmortems')`, teamMember.ID); err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-postmortem-password")
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, teamToken, err := st.Login(ctx, teamMember.Email, "isolated-team-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, policy).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, env, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != status || payload.Error.Code != code {
			t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	assertError(call("GET", "/api/v1/postmortems/candidates", "dev", "", ""), 401, "unauthorized")
	candidates := call("GET", "/api/v1/postmortems/candidates", "dev", adminToken, "")
	if candidates.Code != 200 {
		t.Fatalf("candidates: %d %s", candidates.Code, candidates.Body.String())
	}
	var result struct {
		Items []Candidate `json:"items"`
	}
	if err := json.Unmarshal(candidates.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "ready" || result.Items[0].Evidence != 1 || result.Items[0].Duration == "" || result.Items[0].Severity != "P1" {
		t.Fatalf("unexpected candidates: %+v", result.Items)
	}
	// More than the old pre-authorization limit of hidden records sort ahead
	// of the permitted incident. The keyset scanner must still find it.
	for _, statement := range []string{
		`INSERT INTO action_plans(id,investigation_id,proposal,digest,created_by) SELECT 'hidden-plan','hidden-inv','{}','digest',id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO executions(id,plan_id,status,created_by) SELECT 'hidden-execution','hidden-plan','succeeded',id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO investigation_reports(investigation_id,execution_id,report) VALUES('hidden-inv','hidden-execution','{}')`,
		`INSERT INTO events(investigation_id,type,payload,created_at) VALUES('hidden-inv','investigation.closed','{}',now()-interval '30 minutes')`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at,investigation_id)
 SELECT 'hidden-'||n,'test','hidden','hidden-dev','hidden-'||n,'Hidden incident','info','resolved','resolved',now()-interval '2 hours','hidden-inv'
 FROM generate_series(1,1001) AS n`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	viewerCandidates := call("GET", "/api/v1/postmortems/candidates", "all", viewerToken, "")
	if viewerCandidates.Code != 200 {
		t.Fatalf("viewer candidates: %d %s", viewerCandidates.Code, viewerCandidates.Body.String())
	}
	var viewerResult struct {
		Items []Candidate `json:"items"`
	}
	if err := json.Unmarshal(viewerCandidates.Body.Bytes(), &viewerResult); err != nil || len(viewerResult.Items) != 1 || viewerResult.Items[0].ID != "ready" {
		t.Fatalf("viewer scope: %+v err=%v", viewerResult, err)
	}
	teamCandidates := call("GET", "/api/v1/postmortems/candidates", "dev", teamToken, "")
	if teamCandidates.Code != 200 || !strings.Contains(teamCandidates.Body.String(), `"id":"ready"`) || strings.Contains(teamCandidates.Body.String(), `"id":"hidden-`) {
		t.Fatalf("team scope: %d %s", teamCandidates.Code, teamCandidates.Body.String())
	}
	input := `{"incident_id":"ready","sections":[{"key":"cause","title":"Root cause","text":"Observed"}],"actions":[{"text":"Fix","owner":"SRE","due":"2026-10-01"}],"status":"review"}`
	assertError(call("POST", "/api/v1/postmortems", "all", adminToken, input), 409, "env_view_only")
	assertError(call("POST", "/api/v1/postmortems", "dev", viewerToken, input), 403, "forbidden")
	assertError(call("POST", "/api/v1/postmortems", "dev", teamToken, strings.Replace(input, `"ready"`, `"hidden-1"`, 1)), 403, "forbidden")
	assertError(call("POST", "/api/v1/postmortems", "dev", adminToken, strings.Replace(input, `"review"`, `"published"`, 1)), 422, "invalid_postmortem")
	assertError(call("POST", "/api/v1/postmortems", "dev", adminToken, strings.Replace(input, `"ready"`, `"missing"`, 1)), 409, "incident_not_resolved")
	assertError(call("POST", "/api/v1/postmortems", "dev", adminToken, strings.Replace(input, `"ready"`, `"other"`, 1)), 404, "not_found")
	created := call("POST", "/api/v1/postmortems", "dev", teamToken, input)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &made); err != nil || made.ID == "" {
		t.Fatalf("missing id: %s", created.Body.String())
	}
	assertError(call("POST", "/api/v1/postmortems", "dev", adminToken, input), 409, "postmortem_exists")
	var status string
	var sectionRaw []byte
	var closeSeq int64
	if err := db.QueryRow(ctx, `SELECT status,sections,close_event_seq FROM v3_postmortems WHERE id=$1`, made.ID).Scan(&status, &sectionRaw, &closeSeq); err != nil || status != "review" || !strings.Contains(string(sectionRaw), "Observed") || closeSeq == 0 {
		t.Fatalf("persisted status=%q sections=%s closeSeq=%d err=%v", status, sectionRaw, closeSeq, err)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='postmortem.created' AND target=$1`, made.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
	remaining := call("GET", "/api/v1/postmortems/candidates", "dev", teamToken, "")
	if remaining.Code != 200 || !strings.Contains(remaining.Body.String(), `"items":[]`) {
		t.Fatalf("remaining: %d %s", remaining.Code, remaining.Body.String())
	}
}
