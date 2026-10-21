package v3postmortems

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/actions"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PA-65: a saved draft can be read, edited and submitted for review under
// If-Match, by a knowledge manager, with an audit row; a second person then
// publishes it. Readers without manage never see the draft.
func TestPostmortemDraftEditSubmitAndPublish(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_postmortem_edit_" + store.ID()
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
	cfg.MaxConns = 4
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := knowledge.New(db, nil).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := actions.New(db, nil, "").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	ids := map[string]string{}
	for _, name := range []string{"author", "reviewer", "reader"} {
		user, err := st.CreateUser(ctx, core.User{Email: name + "@example.invalid", Name: name, Role: "viewer"}, "isolated-"+name+"-password")
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = user.ID
	}
	for _, statement := range []string{
		`INSERT INTO rbac_roles(id,name) VALUES('pm-editor','Postmortem Editor')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('pm-editor','knowledge','view','global','*'),('pm-editor','knowledge','manage','global','*'),('pm-editor','events','view','global','*')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) SELECT id,'pm-editor' FROM users WHERE email IN ('author@example.invalid','reviewer@example.invalid')`,
		`INSERT INTO applications(id,key,name) VALUES('app','app','App')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,actor_id,request_key,request_hash)
 VALUES('INC-edit','app','app-dev','dev','Bad release','P2',$1,'case-request-1',repeat('a',64))`,
		`INSERT INTO v3_incident_case_status_events(case_id,revision,from_status,to_status,actor_id,request_id,request_hash)
 VALUES('INC-edit',2,'active','resolved',$1,'case-status-1',repeat('b',64))`,
		`UPDATE v3_incident_cases c SET status='resolved',status_revision=2,resolved_status_event_id=se.id,resolved_at=se.created_at
 FROM v3_incident_case_status_events se WHERE se.case_id=c.id AND c.id='INC-edit' AND $1<>''`,
		`INSERT INTO v3_postmortems(id,incident_id,application_id,environment_id,source_kind,source_case_status_event_id,sections,actions,status,created_by)
 SELECT 'pm-draft','INC-edit','app','dev','case',se.id,
  '[{"key":"impact","title":"Impact","text":""},{"key":"cause","title":"Root cause","text":"Bad config"}]','[]','draft',$1
 FROM v3_incident_case_status_events se WHERE se.case_id='INC-edit'`,
	} {
		if _, err := db.Exec(ctx, statement, ids["author"]); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"author", "reviewer", "reader"} {
		if _, tokens[name], err = st.Login(ctx, name+"@example.invalid", "isolated-"+name+"-password"); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, policy).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session,
		func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, who, ifMatch, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		if ifMatch != "" {
			r.Header.Set("If-Match", ifMatch)
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: tokens[who]})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}

	read := call("GET", "/api/v1/postmortems/pm-draft", "author", "", "")
	var detail Detail
	if err := json.Unmarshal(read.Body.Bytes(), &detail); err != nil || read.Code != 200 || detail.Status != "draft" || detail.Revision != 1 ||
		len(detail.Sections) != 2 || !detail.AuthoredByMe || read.Header().Get("ETag") != `"1"` || !strings.HasPrefix(detail.SourceRevision, "case:") {
		t.Fatalf("draft detail: %d %s", read.Code, read.Body.String())
	}
	expect(call("GET", "/api/v1/postmortems/pm-draft", "reader", "", ""), 404, "not_found")
	// The incident already has this draft: the draft preview points to it.
	existing := call("GET", "/api/v1/postmortems/draft?incident=INC-edit", "author", "", "")
	if existing.Code != 409 || !strings.Contains(existing.Body.String(), `"postmortem_id":"pm-draft"`) {
		t.Fatalf("existing draft pointer: %d %s", existing.Code, existing.Body.String())
	}
	expect(call("GET", "/api/v1/postmortems/draft?incident=INC-edit", "reader", "", ""), 404, "not_found")

	expect(call("PATCH", "/api/v1/postmortems/pm-draft", "author", "", `{"status":"review"}`), 428, "postmortem_revision_required")
	expect(call("PATCH", "/api/v1/postmortems/pm-draft", "reader", `"1"`, `{"status":"review"}`), 404, "not_found")
	// Review needs every section filled in.
	expect(call("PATCH", "/api/v1/postmortems/pm-draft", "author", `"1"`, `{"status":"review"}`), 422, "invalid_postmortem")
	edited := call("PATCH", "/api/v1/postmortems/pm-draft", "author", `"1"`,
		`{"sections":[{"key":"impact","title":"Impact","text":"Checkout errors for 9 minutes"},{"key":"cause","title":"Root cause","text":"Bad config"}],"actions":[{"text":"Add a release gate","owner":"SRE","due":"next week"}]}`)
	if edited.Code != 200 || edited.Header().Get("ETag") != `"2"` || !strings.Contains(edited.Body.String(), `"status":"draft"`) {
		t.Fatalf("edit: %d %s", edited.Code, edited.Body.String())
	}
	expect(call("PATCH", "/api/v1/postmortems/pm-draft", "author", `"1"`, `{"status":"review"}`), 412, "postmortem_revision_conflict")
	expect(call("POST", "/api/v1/postmortems/pm-draft/publish", "reviewer", "", ""), 409, "invalid_state")
	submitted := call("PATCH", "/api/v1/postmortems/pm-draft", "author", `"2"`, `{"status":"review"}`)
	if submitted.Code != 200 || !strings.Contains(submitted.Body.String(), `"status":"review"`) || submitted.Header().Get("ETag") != `"3"` {
		t.Fatalf("submit: %d %s", submitted.Code, submitted.Body.String())
	}
	expect(call("PATCH", "/api/v1/postmortems/pm-draft", "author", `"3"`, `{"status":"draft"}`), 409, "invalid_state")
	expect(call("POST", "/api/v1/postmortems/pm-draft/publish", "author", "", ""), 403, "self_review_forbidden")

	// PA-128: the reviewer can send it back with a reason instead; the author
	// cannot, and a return needs a reason.
	expect(call("POST", "/api/v1/postmortems/pm-draft/return", "author", "", `{"reason":"Fix it"}`), 403, "self_review_forbidden")
	expect(call("POST", "/api/v1/postmortems/pm-draft/return", "reader", "", `{"reason":"Fix it"}`), 404, "not_found")
	expect(call("POST", "/api/v1/postmortems/pm-draft/return", "reviewer", "", `{"reason":"  "}`), 422, "invalid_return")
	expect(call("POST", "/api/v1/postmortems/pm-draft/return", "reviewer", "", `{"reason":"x","note":"y"}`), 422, "invalid_return")
	returned := call("POST", "/api/v1/postmortems/pm-draft/return", "reviewer", "", `{"reason":"Name the metric behind the impact"}`)
	var back Detail
	if err := json.Unmarshal(returned.Body.Bytes(), &back); err != nil || returned.Code != 200 || back.Status != "draft" || back.Revision != 4 ||
		returned.Header().Get("ETag") != `"4"` || back.AuthoredByMe || back.Returned == nil || back.Returned.By != "reviewer" ||
		back.Returned.Reason != "Name the metric behind the impact" || back.Submitted == nil || back.Submitted.By != "author" {
		t.Fatalf("return: %d %s", returned.Code, returned.Body.String())
	}
	expect(call("POST", "/api/v1/postmortems/pm-draft/return", "reviewer", "", `{"reason":"Again"}`), 409, "invalid_state")
	expect(call("POST", "/api/v1/postmortems/pm-draft/publish", "reviewer", "", ""), 409, "invalid_state")
	read = call("GET", "/api/v1/postmortems/pm-draft", "author", "", "")
	detail = Detail{}
	if err := json.Unmarshal(read.Body.Bytes(), &detail); err != nil || read.Code != 200 || detail.Author != "author" || detail.Returned == nil ||
		detail.Returned.Reason != "Name the metric behind the impact" || detail.Published != nil || detail.Incident == nil ||
		detail.Incident.ID != "INC-edit" || detail.Incident.Title != "Bad release" || detail.Incident.Severity == nil || *detail.Incident.Severity != "P2" ||
		detail.Incident.ClosedAt == "" {
		t.Fatalf("returned draft detail: %d %s", read.Code, read.Body.String())
	}
	resubmitted := call("PATCH", "/api/v1/postmortems/pm-draft", "author", `"4"`, `{"status":"review"}`)
	detail = Detail{}
	if err := json.Unmarshal(resubmitted.Body.Bytes(), &detail); err != nil || resubmitted.Code != 200 || detail.Status != "review" ||
		detail.Returned != nil || detail.Submitted == nil || resubmitted.Header().Get("ETag") != `"5"` {
		t.Fatalf("resubmit: %d %s", resubmitted.Code, resubmitted.Body.String())
	}

	expect(call("POST", "/api/v1/postmortems/pm-draft/publish", "reviewer", "", ""), 200, "")
	var updatedAudit, submittedAudit, returnedAudit int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='postmortem.updated' AND actor_id=$1),
 count(*) FILTER (WHERE action='postmortem.submitted' AND actor_id=$1),count(*) FILTER (WHERE action='postmortem.returned' AND actor_id=$2)
 FROM management_audit WHERE target='pm-draft'`, ids["author"], ids["reviewer"]).Scan(&updatedAudit, &submittedAudit, &returnedAudit); err != nil ||
		updatedAudit != 1 || submittedAudit != 2 || returnedAudit != 1 {
		t.Fatalf("audit: updated=%d submitted=%d returned=%d err=%v", updatedAudit, submittedAudit, returnedAudit, err)
	}
	got := call("GET", "/api/v1/postmortems/pm-draft", "reader", "", "")
	detail = Detail{}
	if err := json.Unmarshal(got.Body.Bytes(), &detail); err != nil || got.Code != 200 || detail.Status != "published" ||
		detail.Published == nil || detail.Published.By != "reviewer" || detail.Returned != nil || detail.AuthoredByMe {
		t.Fatalf("published read: %d %s", got.Code, got.Body.String())
	}
}
