package v3conversation

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
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type usageReportingRuntime struct{}

func (usageReportingRuntime) Models() []inference.Model {
	return []inference.Model{{ID: "model-1", InputBudget: 8192}}
}

func TestConversationDetailRegisteredRouteCanvasAndInvestigationScope(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_conversation_detail_" + store.ID()
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
		if _, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES ('detail-canvas','Canvas'),('detail-chat','Chat'),('detail-team','Team'),('detail-app','App')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('detail-canvas','environment','view','environment','test'),('detail-canvas','canvas','view','environment','test'),
 ('detail-chat','environment','view','environment','test'),('detail-chat','conversation','view','environment','test'),
 ('detail-team','environment','view','environment','test'),('detail-team','canvas','view','team','team-one'),
 ('detail-app','environment','view','environment','test'),('detail-app','canvas','view','application','app-one')`,
		`INSERT INTO teams(id,name) VALUES('team-one','Team One')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES('app-one','App One','app-one','team-one')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('ae-one','app-one','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-one','test')`,
		`INSERT INTO investigations(id,application_id,environment_id,title,status,goal) VALUES('inv-one','app-one','ae-one','Investigation','open','find cause')`,
		`INSERT INTO branches(id,investigation_id,title) VALUES('root-one','inv-one','Main')`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES('root-one',0,'[{"id":"historic","role":"user","content":"secret without timestamp"}]','{"kind":"initial"}')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	create := func(email, role string) (string, string) {
		t.Helper()
		u, e := st.CreateUser(ctx, core.User{Email: email, Name: email, Role: "viewer"}, "isolated-password")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, u.ID, role); e != nil {
			t.Fatal(e)
		}
		_, token, e := st.Login(ctx, email, "isolated-password")
		if e != nil {
			t.Fatal(e)
		}
		return u.ID, token
	}
	owner, ownerToken := create("canvas-owner@example.invalid", "detail-canvas")
	_, strangerToken := create("canvas-other@example.invalid", "detail-canvas")
	_, chatToken := create("chat-only@example.invalid", "detail-chat")
	teamUser, teamToken := create("team-member@example.invalid", "detail-team")
	_, teamNonmemberToken := create("team-nonmember@example.invalid", "detail-team")
	_, appToken := create("app-reader@example.invalid", "detail-app")
	if _, err = db.Exec(ctx, `INSERT INTO team_members(team_id,user_id) VALUES('team-one',$1)`, teamUser); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,status,nodes) VALUES
 ('canvas-one',$1,'test','Node thread','analysis','draft','[{"id":"n1"},{"id":"n2"}]')`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvas_node_exchanges
 (id,canvas_id,node_id,user_id,author,user_text,assistant_text,model_id,model_name,model_provider,state,lease_expires_at,completed_at) VALUES
 ('x1','canvas-one','n1',$1,'Owner','first node','first answer','model','Model','Test','completed',now(),now()),
 ('x2','canvas-one','n2',$1,'Owner','second node',NULL,'model','Model','Test','failed',now(),NULL)`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_conversations(id,owner_id,environment_id,title,status) VALUES('chat-one',$1,'test','Private chat','active')`, owner); err != nil {
		t.Fatal(err)
	}
	runtime := testRuntime{models: []inference.Model{{ID: "model", InputBudget: 8192}}}
	api := httpapi.New(st, runtime, true)
	chat := New(db, runtime, Options{
		CurrentUser: httpapi.CurrentUser,
		Environment: func(r *http.Request) (string, error) {
			scope, ok := httpapi.V3ScopeFromContext(r.Context())
			if !ok || len(scope.EnvironmentIDs) == 0 {
				return "", ErrEnvironment
			}
			if scope.All {
				return "all", nil
			}
			return scope.EnvironmentIDs[0], nil
		},
		AllowedEnvironments: func(_ context.Context, _ core.User) ([]string, error) { return []string{"test"}, nil },
	})
	chat.RegisterV3(api)
	policy := &rbac.V3Policy{Store: st}
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	get := func(id, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/"+id, nil)
		req.Header.Set("X-AIOps-Environment", "test")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		rr := httptest.NewRecorder()
		api.ServeHTTP(rr, req)
		return rr
	}
	rr := get("canvas-one", ownerToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("canvas detail=%d %s", rr.Code, rr.Body.String())
	}
	var detail Conversation
	if err = json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Mode != "canvas" || detail.Status != "active" || len(detail.Messages) != 3 ||
		detail.Messages[0].NodeID != "n1" || detail.Messages[1].NodeID != "n1" ||
		detail.Messages[2].NodeID != "n2" ||
		!strings.Contains(string(detail.Messages[1].Parts[0]), "first answer") ||
		detail.Messages[2].Role != "user" || detail.Messages[2].State != "failed" ||
		detail.Messages[2].Streaming || detail.Side["context"].(map[string]any)["used"] != nil {
		t.Fatalf("canvas thread projection: %s", rr.Body.String())
	}
	for _, tc := range []struct{ id, token string }{
		{"canvas-one", strangerToken}, {"canvas-one", chatToken}, {"chat-one", strangerToken}, {"chat-one", ownerToken},
	} {
		want := http.StatusNotFound
		if tc.id == "chat-one" && tc.token == ownerToken {
			// The owner has only canvas:view, so this chat remains private to
			// the conversation module even though the object belongs to them.
			want = http.StatusForbidden
		}
		if got := get(tc.id, tc.token); got.Code != want {
			t.Fatalf("scoped read %s got %d want %d: %s", tc.id, got.Code, want, got.Body.String())
		}
	}
	// A pure investigation has per-branch context, not a linear conversation.
	// Even a current application or team canvas reader must use the existing
	// investigation graph and node message routes for its actual turns.
	for _, token := range []string{teamToken, appToken, teamNonmemberToken, chatToken} {
		got := get("inv-one", token)
		if got.Code != http.StatusNotFound || strings.Contains(got.Body.String(), "secret without timestamp") {
			t.Fatalf("pure investigation must not be a conversation detail: %d %s", got.Code, got.Body.String())
		}
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='detail-canvas' AND module='canvas'`); err != nil {
		t.Fatal(err)
	}
	if got := get("canvas-one", ownerToken); got.Code != http.StatusNotFound {
		t.Fatalf("revoked canvas grant=%d %s", got.Code, got.Body.String())
	}
}
func (usageReportingRuntime) Run(_ context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	if err := emit(inference.Event{Type: "step", Step: &inference.Step{ID: "model-call", Kind: "model", Status: "succeeded", Usage: &inference.Usage{InputTokens: 28, OutputTokens: 6}}}); err != nil {
		return "", err
	}
	if err := emit(inference.Event{Type: "delta", Delta: "answer"}); err != nil {
		return "", err
	}
	return "answer", nil
}

func TestRunTurnPersistsReportedUsageForConversationDetail(t *testing.T) {
	db := isolatedConversationDB(t)
	svc := New(db, usageReportingRuntime{}, Options{
		RefreshSession: func(_ context.Context, token string) (core.User, error) {
			if token != "test-session" {
				return core.User{}, ErrForbidden
			}
			return core.User{ID: "persisted-owner"}, nil
		},
		AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
			if owner != "persisted-owner" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if owner != "persisted-owner" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
	})
	svc.runTurn("c", "r", inference.Model{ID: "model-1", Name: "Test model", InputBudget: 8192}, "", "test-session")
	var raw []byte
	if err := db.QueryRow(context.Background(), `SELECT run FROM v3_conversation_messages WHERE id='a'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var run struct {
		Tokens int `json:"tokens"`
		Input  int `json:"context_input_tokens"`
		Budget int `json:"context_budget"`
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.Tokens != 34 || run.Input != 28 || run.Budget != 8192 {
		t.Fatalf("reported usage: %+v", run)
	}
}

type detailRouteEnvironmentAuthorizer struct{}

func (detailRouteEnvironmentAuthorizer) AuthorizeEnvironment(_ context.Context, u core.User, env string) error {
	if (u.ID == "owner" || u.ID == "other") && env == "test" {
		return nil
	}
	return store.ErrForbidden
}

func TestConversationDetailRegisteredRouteReadsPersistedContext(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN status text NOT NULL DEFAULT 'active', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		CREATE TABLE v3_canvases (id text PRIMARY KEY,owner_id text NOT NULL,environment_id text NOT NULL,title text NOT NULL,status text NOT NULL);
		CREATE TABLE v3_canvas_node_exchanges (id text PRIMARY KEY,canvas_id text NOT NULL,node_id text NOT NULL,author text NOT NULL,user_text text NOT NULL,assistant_text text,model_name text NOT NULL,state text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz,sort_order bigint GENERATED ALWAYS AS IDENTITY);
		CREATE TABLE teams (id text PRIMARY KEY);
		CREATE TABLE team_members (team_id text NOT NULL,user_id text NOT NULL);
		CREATE TABLE applications (id text PRIMARY KEY,owner_team_id text,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE application_environments (id text PRIMARY KEY,application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE v3_application_environments (application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE investigations (id text PRIMARY KEY,application_id text NOT NULL,environment_id text NOT NULL,title text NOT NULL,status text NOT NULL);
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE VIEW v3_effective_role_grants AS
			SELECT a.user_id,g.module,g.action,g.scope_kind,g.scope_id FROM rbac_user_roles a
			JOIN rbac_roles r ON r.id=a.role_id JOIN v3_role_grants g ON g.role_id=r.id;
		INSERT INTO users(id,email,name,role) VALUES('owner','owner@local','Owner','viewer'),('other','other@local','Other','viewer');
		INSERT INTO rbac_roles(id) VALUES('detail-reader');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('owner','detail-reader');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
		('detail-reader','environment','view','environment','test'),
		('detail-reader','conversation','view','environment','test'),
		('detail-reader','environment','view','environment','dev'),
		('detail-reader','conversation','view','environment','dev');
		INSERT INTO v3_conversations(id,owner_id,environment_id,title,context_ref,default_model_id) VALUES
		('c1','owner','test','Checkout review','alert:a1,service:checkout-api','model-1'),
		('c2','other','test','Private review','service:private-api','model-1'),
		('c3','owner','prod','Production review','service:production-api','model-1'),
		('c4','owner','dev','Development review','service:dev-api','model-1');
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,run,state) VALUES
		('m1','c1','user','Owner','[{"type":"text","markdown":"question"}]',NULL,'completed'),
		('m2','c1','assistant','AI','[{"type":"text","markdown":"answer"},{"type":"tool_calls","summary":"2 calls","tokens":8,"duration_s":1,"calls":[{"id":"a","connector":"prometheus","tool":"metrics","args":"","duration_ms":2,"status":"ok"},{"id":"b","connector":"loki","tool":"logs","args":"","duration_ms":3,"status":"ok"}]}]','{"id":"r1","model":"test","tokens":44,"duration_s":1,"context_input_tokens":32,"context_budget":8192}','completed'),
		('m3','c1','assistant','AI','[{"type":"tool_calls","summary":"1 call","tokens":0,"duration_s":1,"calls":[{"id":"c","connector":"prometheus","tool":"metrics","args":"","duration_ms":4,"status":"ok"}]}]','{"id":"r2","model":"test","tokens":80,"duration_s":1,"context_input_tokens":60,"context_budget":8192}','completed');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'owner',now()+interval '1 hour'),($2,'other',now()+interval '1 hour')`, store.Hash("owner-session"), store.Hash("other-session")); err != nil {
		t.Fatal(err)
	}
	repository := &store.Store{DB: db}
	runtime := testRuntime{models: []inference.Model{{ID: "model-1", InputBudget: 8192}}}
	server := httpapi.New(repository, runtime, true)
	granted := true
	chat := New(db, runtime, Options{
		CurrentUser: httpapi.CurrentUser,
		Environment: func(r *http.Request) (string, error) {
			scope, ok := httpapi.V3ScopeFromContext(r.Context())
			if !ok || len(scope.EnvironmentIDs) == 0 {
				return "", ErrEnvironment
			}
			if scope.All {
				return "all", nil
			}
			return scope.EnvironmentIDs[0], nil
		},
		AllowedEnvironments: func(_ context.Context, u core.User) ([]string, error) {
			if !granted {
				return nil, nil
			}
			return []string{"test", "dev"}, nil
		},
	})
	chat.Register(server.Register)
	server.MarkV3Route("GET /api/v1/conversations/{id}", "conversation", "view")
	policy := func(_ context.Context, u core.User, module, action, env string) error {
		if granted && u.ID != "" && module == "conversation" && action == "view" && env == "test" {
			return nil
		}
		return store.ErrForbidden
	}
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}, detailRouteEnvironmentAuthorizer{}, repository.Session, policy); err != nil {
		t.Fatal(err)
	}
	get := func(id, env, session string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/"+id, nil)
		req.Header.Set("X-AIOps-Environment", env)
		if session != "" {
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		}
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		return rr
	}
	if rr := get("c1", "test", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: %d %s", rr.Code, rr.Body.String())
	}
	rr := get("c1", "test", "owner-session")
	if rr.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rr.Code, rr.Body.String())
	}
	var detail Conversation
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Scope["service"] != "checkout-api" || detail.Scope["env"] != "test" || len(detail.Messages) != 3 {
		t.Fatalf("detail scope/messages: %+v", detail)
	}
	connectors := detail.Side["connectors"].([]any)
	if len(connectors) != 2 || connectors[0].(map[string]any)["name"] != "prometheus" || connectors[0].(map[string]any)["calls"] != float64(2) {
		t.Fatalf("connectors: %+v", connectors)
	}
	contextSide := detail.Side["context"].(map[string]any)
	if contextSide["used"] != float64(60) || contextSide["budget"] != float64(8192) {
		t.Fatalf("context: %+v", contextSide)
	}
	if detail.Messages[1].Run != nil || detail.Messages[2].Run != nil {
		t.Fatalf("historical runs missing agent/version cannot satisfy the public DTO: %+v", detail.Messages)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_messages SET run='{"id":"r2","agent":"AI","version":"v3","model":"test","tokens":80,"duration_s":1,"context_input_tokens":60,"context_budget":8192}' WHERE id='m3'`); err != nil {
		t.Fatal(err)
	}
	if rr := get("c1", "test", "owner-session"); rr.Code != http.StatusOK {
		t.Fatalf("detail after complete run: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	} else if string(detail.Messages[2].Run) == "" {
		t.Fatal("complete persisted run was dropped")
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,attachments,state) VALUES
	 ('m4','c1','user','Owner','[{"type":"text","markdown":"inspect attached log"}]','[{"id":"upload-1","kind":"file","name":"incident.log","size":"1 KB","mime":"text/plain"}]','completed'),
	 ('m5','c1','assistant','AI','[{"type":"tool_calls","summary":"read failed","tokens":0,"duration_s":1,"calls":[{"id":"call-1","connector":"loki","tool":"logs","args":"","duration_ms":5,"status":"error"}]}]','[]','failed'),
	 ('m6','c1','assistant','AI','[{"type":"status","text":"Model reply failed","detail":"Retry this message after checking the model connection."}]','[]','failed')`); err != nil {
		t.Fatal(err)
	}
	if rr := get("c1", "test", "owner-session"); rr.Code != http.StatusOK {
		t.Fatalf("detail after failed tool and upload: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	} else if len(detail.Messages) != 6 || len(detail.Messages[3].Attachments) != 1 ||
		!strings.Contains(string(detail.Messages[3].Attachments[0]), `"id":"upload-1"`) ||
		!strings.Contains(string(detail.Messages[4].Parts[0]), `"status":"error"`) ||
		len(detail.Messages[4].Parts) != 2 || detail.Messages[4].State != "failed" || detail.Messages[4].Streaming ||
		len(detail.Messages[5].Parts) != 1 {
		t.Fatalf("attachment/tool/failure detail: %+v", detail.Messages)
	}
	if rr := get("c2", "test", "owner-session"); rr.Code != http.StatusNotFound {
		t.Fatalf("other owner: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get("c3", "test", "owner-session"); rr.Code != http.StatusNotFound {
		t.Fatalf("other env: %d %s", rr.Code, rr.Body.String())
	}
	// This instance exposes only test. An all-scope request must not reveal
	// the owner's dev conversation even though the owner has current dev grants.
	if rr := get("c4", "all", "owner-session"); rr.Code != http.StatusNotFound {
		t.Fatalf("all scope leaked environment outside instance: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get("c1", "test", "other-session"); rr.Code != http.StatusNotFound {
		t.Fatalf("other session: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state)
		SELECT 'oversize-'||n,'c1','user','Owner','[]'::jsonb,'completed' FROM generate_series(1,$1) AS n`, maxDetailMessages); err != nil {
		t.Fatal(err)
	}
	if rr := get("c1", "test", "owner-session"); rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "conversation_unavailable") {
		t.Fatalf("oversized history must fail instead of truncating: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='detail-reader' AND module='conversation'`); err != nil {
		t.Fatal(err)
	}
	if rr := get("c1", "test", "owner-session"); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "forbidden") {
		t.Fatalf("persisted grant revoked: %d %s", rr.Code, rr.Body.String())
	}
	granted = false
	if rr := get("c1", "test", "owner-session"); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "forbidden") {
		t.Fatalf("revoked: %d %s", rr.Code, rr.Body.String())
	}
}

func TestConversationDetailDoesNotInventMissingUsage(t *testing.T) {
	svc := New(nil, testRuntime{models: []inference.Model{{ID: "model-1", InputBudget: 8192}}}, Options{})
	detail := svc.conversationDetail("c", "title", "active", "test", "service:checkout", "model-1", []Message{{Role: "assistant", Run: json.RawMessage(`{"id":"old-run","tokens":0}`)}})
	contextSide := detail.Side["context"].(map[string]any)
	if contextSide["used"] != nil || contextSide["budget"] != nil {
		t.Fatalf("unknown historical usage must remain unknown: %+v", contextSide)
	}
	detail = svc.conversationDetail("c", "title", "active", "test", "service:checkout", "model-1", []Message{
		{Role: "assistant", Run: json.RawMessage(`{"id":"reported-run","context_input_tokens":40,"context_budget":8192}`)},
		{Role: "assistant"},
	})
	contextSide = detail.Side["context"].(map[string]any)
	if contextSide["used"] != nil || contextSide["budget"] != nil || len(contextSide["parts"].([]any)) != 0 {
		t.Fatalf("newer unknown usage must not reuse an older model input count: %+v", contextSide)
	}
	detail = svc.conversationDetail("c", "title", "active", "test", "", "model-1", nil)
	contextSide = detail.Side["context"].(map[string]any)
	if contextSide["used"] != 0 || contextSide["budget"] != 8192 {
		t.Fatalf("a conversation with no model turn has zero usage and its selected default budget: %+v", contextSide)
	}
}
