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

type createRouteEnvironmentAuthorizer struct{}

func (createRouteEnvironmentAuthorizer) AuthorizeEnvironment(_ context.Context, user core.User, env string) error {
	if user.ID != "route-owner" || env != "test" {
		return store.ErrForbidden
	}
	return nil
}

func TestCreateConversationRegisteredV3Route(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		ALTER TABLE management_audit ALTER COLUMN actor_kind SET DEFAULT 'user';
		CREATE TABLE IF NOT EXISTS users (id text PRIMARY KEY,email text NOT NULL,name text NOT NULL,role text NOT NULL,disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz);
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE TABLE IF NOT EXISTS rbac_user_roles (user_id text NOT NULL,role_id text NOT NULL);
		INSERT INTO users(id,email,name,role) VALUES('route-owner','route@local','Route Owner','viewer');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'route-owner',now()+interval '1 hour')`, store.Hash("route-session")); err != nil {
		t.Fatal(err)
	}

	granted := true
	repository := &store.Store{DB: db}
	runtime := testRuntime{models: []inference.Model{{ID: "model-1"}}}
	server := httpapi.New(repository, runtime, true)
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
		AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
			if granted {
				return []string{"test"}, nil
			}
			return nil, nil
		},
		DefaultModel: func(context.Context, string) (string, error) { return "model-1", nil },
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if !granted || owner != "route-owner" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
	})
	chat.Register(server.Register)
	server.MarkV3Route("POST /api/v1/conversations", "conversation", "change")
	policy := func(_ context.Context, user core.User, module, action, env string) error {
		if granted && user.ID == "route-owner" && module == "conversation" && action == "change" && env == "test" {
			return nil
		}
		return store.ErrForbidden
	}
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		createRouteEnvironmentAuthorizer{}, repository.Session, policy); err != nil {
		t.Fatal(err)
	}
	post := func(env string, cookie bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", strings.NewReader(`{"text":"investigate checkout"}`))
		req.Header.Set("X-AIOps-Environment", env)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "route-session"})
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		return response
	}
	if response := post("test", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing session status=%d body=%s", response.Code, response.Body.String())
	}
	createdResponse := post("test", true)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create response=%s err=%v", createdResponse.Body.String(), err)
	}
	var owner, env, model string
	if err := db.QueryRow(ctx, `SELECT owner_id,environment_id,default_model_id FROM v3_conversations WHERE id=$1`, created.ID).Scan(&owner, &env, &model); err != nil {
		t.Fatal(err)
	}
	if owner != "route-owner" || env != "test" || model != "model-1" {
		t.Fatalf("persisted owner=%q env=%q model=%q", owner, env, model)
	}
	if response := post("all", true); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"env_view_only"`) {
		t.Fatalf("all write status=%d body=%s", response.Code, response.Body.String())
	}
	granted = false
	if response := post("test", true); response.Code != http.StatusForbidden {
		t.Fatalf("revoked policy status=%d body=%s", response.Code, response.Body.String())
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversations WHERE owner_id='route-owner'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("created rows=%d err=%v", count, err)
	}
}
