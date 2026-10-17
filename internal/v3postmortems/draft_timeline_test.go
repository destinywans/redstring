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
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// postmortemTextFixture is an isolated schema with one resolved manual
// incident (INC-tl) that its author may read, and the postmortem routes.
type postmortemTextFixture struct {
	db     *pgxpool.Pool
	api    *httpapi.Server
	token  string
	userID string
}

func (f postmortemTextFixture) get(t *testing.T, path, lang string) []byte {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("X-AIOps-Environment", "dev")
	r.Header.Set("Accept-Language", lang)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.token})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func newPostmortemTextFixture(t *testing.T) postmortemTextFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_postmortem_timeline_" + store.ID()
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
	user, err := st.CreateUser(ctx, core.User{Email: "author@example.invalid", Name: "author", Role: "viewer"}, "isolated-author-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO rbac_roles(id,name) VALUES('pm-reader','Postmortem Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('pm-reader','knowledge','view','global','*'),('pm-reader','events','view','global','*')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'pm-reader')`,
		`INSERT INTO applications(id,key,name) VALUES('app','app','App')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,actor_id,request_key,request_hash,created_at,updated_at)
 VALUES('INC-tl','app','app-dev','dev','Checkout 5xx','P2',$1,'case-request-1',repeat('a',64),now()-interval '2 hour',now()-interval '2 hour')`,
		`INSERT INTO v3_incident_case_status_events(case_id,revision,from_status,to_status,actor_id,request_id,request_hash,created_at)
 VALUES('INC-tl',2,'active','mitigated',$1,'case-status-1',repeat('b',64),now()-interval '30 minute'),
       ('INC-tl',3,'mitigated','resolved',$1,'case-status-2',repeat('c',64),now()-interval '10 minute')`,
		`UPDATE v3_incident_cases c SET status='resolved',status_revision=3,resolved_status_event_id=se.id,resolved_at=se.created_at
 FROM v3_incident_case_status_events se WHERE se.case_id=c.id AND se.revision=3 AND $1<>''`,
		`INSERT INTO v3_incident_timeline(id,incident_id,actor_id,text,created_at) VALUES
 ('note-1','INC-tl',$1,'回滚 checkout-api 到修订 86',now()-interval '20 minute'),
 ('note-late','INC-tl',$1,'after the close',now())`,
	} {
		args := []any{}
		if strings.Contains(statement, "$1") {
			args = append(args, user.ID)
		}
		if _, err := db.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := st.Login(ctx, "author@example.invalid", "isolated-author-password")
	if err != nil {
		t.Fatal(err)
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
	return postmortemTextFixture{db: db, api: api, token: token, userID: user.ID}
}

type draftBody struct {
	Timeline []map[string]string `json:"timeline"`
	Sections []Section           `json:"sections"`
}

func (f postmortemTextFixture) draft(t *testing.T, lang string) draftBody {
	t.Helper()
	var body draftBody
	if err := json.Unmarshal(f.get(t, "/api/v1/postmortems/draft?incident=INC-tl", lang), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// PA-169: a case's draft timeline carries the notes added on the incident
// page, and its lifecycle events in words instead of event codes.
func TestPostmortemDraftTimelineHasManualNotesInWords(t *testing.T) {
	f := newPostmortemTextFixture(t)
	texts := func(items []map[string]string) []string {
		out := []string{}
		for _, item := range items {
			out = append(out, item["text"])
		}
		return out
	}
	zh := texts(f.draft(t, "zh-CN").Timeline)
	if strings.Join(zh, "|") != "事件已缓解|回滚 checkout-api 到修订 86|事件已解决" {
		t.Fatalf("zh timeline: %q", zh)
	}
	if en := texts(f.draft(t, "en-US").Timeline); strings.Join(en, "|") != "Incident mitigated|回滚 checkout-api 到修订 86|Incident resolved" {
		t.Fatalf("en timeline: %q", en)
	}
}

// PA-113: the draft's section titles are in the request language.
func TestPostmortemDraftSectionTitlesFollowRequestLanguage(t *testing.T) {
	f := newPostmortemTextFixture(t)
	titles := func(sections []Section) string {
		out := []string{}
		for _, section := range sections {
			out = append(out, section.Key+"="+section.Title)
		}
		return strings.Join(out, "|")
	}
	if zh := titles(f.draft(t, "zh-CN").Sections); zh != "summary=事件摘要|impact=影响|root=根因" {
		t.Fatalf("zh sections: %s", zh)
	}
	if en := titles(f.draft(t, "en-US").Sections); en != "summary=Incident summary|impact=Impact|root=Root cause" {
		t.Fatalf("en sections: %s", en)
	}
}
