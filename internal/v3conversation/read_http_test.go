package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type readRouteEnvironmentAuthorizer struct{}

func (readRouteEnvironmentAuthorizer) AuthorizeEnvironment(_ context.Context, user core.User, env string) error {
	if user.ID == "read-owner" && (env == "test" || env == "dev") {
		return nil
	}
	return store.ErrForbidden
}

func TestConversationListRegisteredRouteUsesPersistedChatFacts(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		-- Test-double the search function; the full pinyin dictionary is an
		-- independent migration and is not needed for this route test.
		CREATE FUNCTION aiops_search_match(value text, query text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
			SELECT strpos(lower(value),lower(query))>0 OR (lower(query)='jiesuan' AND strpos(value,'结算')>0)
		$$;
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN status text NOT NULL DEFAULT 'active', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		CREATE TABLE users (id text PRIMARY KEY,email text NOT NULL,name text NOT NULL,role text NOT NULL,disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz);
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE TABLE rbac_user_roles (user_id text NOT NULL,role_id text NOT NULL);
		INSERT INTO users(id,email,name,role) VALUES('read-owner','read@local','Read Owner','viewer'),('other','other@local','Other','viewer');
		INSERT INTO v3_conversations(id,owner_id,environment_id,title,status,context_ref,updated_at) VALUES
		('read-a','read-owner','test','结算排查','active','service:checkout-api',now()+interval '2 minutes'),
		('read-b','read-owner','dev','库存复盘','resolved','service:inventory',now()+interval '1 minute'),
		('read-archive','read-owner','dev','归档记录','archived','',now()),
		('read-private','other','test','私人记录','active','',now()),
		('read-prod','read-owner','prod','生产记录','active','',now());
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,run,state) VALUES
		('read-u1','read-a','user','Read Owner','[]',NULL,'completed'),
		('read-u2','read-a','user','Read Owner','[]',NULL,'completed'),
		('read-answer','read-a','assistant','AI','[]','{"tokens":26}','completed'),
		('read-unknown','read-a','assistant','AI','[]',NULL,'completed'),
		('read-u3','read-b','user','Read Owner','[]',NULL,'completed');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'read-owner',now()+interval '1 hour')`, store.Hash("read-session")); err != nil {
		t.Fatal(err)
	}
	var searchReady bool
	if err := db.QueryRow(ctx, `SELECT aiops_search_match('结算排查','jiesuan')`).Scan(&searchReady); err != nil {
		t.Fatalf("search migration is required: %v", err)
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
		AllowedEnvironments: func(_ context.Context, user core.User) ([]string, error) {
			if granted && user.ID == "read-owner" {
				return []string{"test", "dev", "prod"}, nil
			}
			return nil, nil
		},
	})
	chat.Register(server.Register)
	server.MarkV3Route("GET /api/v1/conversations", "conversation", "view")
	server.MarkV3Route("GET /api/v1/conversations/{id}", "conversation", "view")
	policy := func(_ context.Context, user core.User, module, action, env string) error {
		if granted && user.ID == "read-owner" && module == "conversation" && action == "view" && (env == "test" || env == "dev") {
			return nil
		}
		return store.ErrForbidden
	}
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "dev"}}, readRouteEnvironmentAuthorizer{}, repository.Session, policy); err != nil {
		t.Fatal(err)
	}
	get := func(path, env, language string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", env)
		if language != "" {
			r.Header.Set("Accept-Language", language)
		}
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "read-session"})
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/conversations"
	if rr := get(base, "test", "", false); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", rr.Code, rr.Body.String())
	}
	var page struct {
		Items []struct {
			ID           string    `json:"id"`
			Scope        string    `json:"scope"`
			Size         string    `json:"size"`
			Tokens       *int      `json:"tokens"`
			Participants []string  `json:"participants"`
			UpdatedAt    time.Time `json:"updated_at"`
		} `json:"items"`
		Total    int             `json:"total"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
		Counts   map[string]*int `json:"counts"`
	}
	if rr := get(base+"?page_size=1&page=1", "all", "zh-CN", true); rr.Code != http.StatusOK {
		t.Fatalf("all page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Page != 1 || page.PageSize != 1 || len(page.Items) != 1 || page.Items[0].ID != "read-a" || page.Items[0].Scope != "checkout-api · test" || page.Items[0].Size != "2 轮" || page.Items[0].Tokens != nil || strings.Join(page.Items[0].Participants, ",") != "Read Owner,AI" {
		t.Fatalf("persisted first page: %+v", page)
	}
	if page.Counts["mine"] == nil || *page.Counts["mine"] != 2 || page.Counts["archived"] == nil || *page.Counts["archived"] != 1 || page.Counts["team"] != nil || page.Counts["triage"] != nil {
		t.Fatalf("source counts must distinguish known values from unavailable sources: %+v", page.Counts)
	}
	if rr := get(base+"?page_size=1&page=2", "all", "en-US", true); rr.Code != http.StatusOK {
		t.Fatalf("second page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != "read-b" || page.Items[0].Size != "1 turns" || len(page.Items[0].Participants) != 1 || page.Items[0].Tokens == nil || *page.Items[0].Tokens != 0 {
		t.Fatalf("persisted second page: %+v", page)
	}
	for _, query := range []string{"?q=jiesuan", "?q=checkout-api"} {
		if rr := get(base+query, "test", "zh-CN", true); rr.Code != http.StatusOK {
			t.Fatalf("search %s: %d %s", query, rr.Code, rr.Body.String())
		} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "read-a" {
			t.Fatalf("search %s: %+v", query, page)
		}
	}
	if rr := get(base+"?tab=archived", "all", "", true); rr.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "read-archive" {
		t.Fatalf("archive projection: %+v", page)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_messages SET run='{"tokens":10}' WHERE id='read-unknown'`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?page_size=1&page=1", "all", "", true); rr.Code != http.StatusOK {
		t.Fatalf("known usage page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if len(page.Items) != 1 || page.Items[0].Tokens == nil || *page.Items[0].Tokens != 36 {
		t.Fatalf("complete provider usage should sum exactly: %+v", page.Items)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_messages SET run='{"tokens":-1}' WHERE id='read-unknown'`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?page_size=1&page=1", "all", "", true); rr.Code != http.StatusOK {
		t.Fatalf("invalid usage page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if len(page.Items) != 1 || page.Items[0].Tokens != nil {
		t.Fatalf("invalid persisted usage must remain unknown: %+v", page.Items)
	}
	for _, query := range []string{"?tab=team", "?tab=triage", "?mode=canvas"} {
		if rr := get(base+query, "test", "", true); rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "conversation_source_unavailable") {
			t.Fatalf("unconnected %s: %d %s", query, rr.Code, rr.Body.String())
		}
	}
	if rr := get(base+"?tab=invalid", "test", "", true); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid filter: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get(base+"/read-private", "test", "", true); rr.Code != http.StatusNotFound {
		t.Fatalf("other owner: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get(base+"/read-prod", "all", "", true); rr.Code != http.StatusNotFound {
		t.Fatalf("production from all: %d %s", rr.Code, rr.Body.String())
	}
	granted = false
	if rr := get(base, "test", "", true); rr.Code != http.StatusForbidden {
		t.Fatalf("revoked policy: %d %s", rr.Code, rr.Body.String())
	}
}
