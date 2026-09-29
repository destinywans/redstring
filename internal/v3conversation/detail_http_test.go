package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type usageReportingRuntime struct{}

func (usageReportingRuntime) Models() []inference.Model {
	return []inference.Model{{ID: "model-1", InputBudget: 8192}}
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
