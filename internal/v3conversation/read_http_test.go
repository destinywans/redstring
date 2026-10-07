package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if (user.ID == "read-owner" && (env == "test" || env == "dev")) ||
		(user.ID == "other" && env == "test") {
		return nil
	}
	return store.ErrForbidden
}

func TestConversationListRegisteredRouteUsesPersistedChatFacts(t *testing.T) {
	// The revocation test holds four concurrent connections and needs one
	// additional connection to observe PostgreSQL lock waits.
	db := isolatedConversationDBWithConns(t, 8)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		-- Test-double the search function; the full pinyin dictionary is an
		-- independent migration and is not needed for this route test.
		CREATE FUNCTION aiops_search_match(value text, query text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
			SELECT strpos(lower(value),lower(query))>0 OR (lower(query)='jiesuan' AND strpos(value,'结算')>0)
				OR (lower(query)='rongliang' AND strpos(value,'容量')>0)
		$$;
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN status text NOT NULL DEFAULT 'active', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		CREATE TABLE v3_canvases (id text PRIMARY KEY,owner_id text NOT NULL,environment_id text NOT NULL,title text NOT NULL,status text NOT NULL,services text[] NOT NULL DEFAULT '{}',nodes jsonb NOT NULL DEFAULT '[]',updated_at timestamptz NOT NULL);
		CREATE TABLE v3_canvas_node_exchanges (id text PRIMARY KEY,canvas_id text NOT NULL,state text NOT NULL);
		CREATE TABLE applications (id text PRIMARY KEY,name text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE application_environments (id text PRIMARY KEY,application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE v3_application_environments (application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE investigations (id text PRIMARY KEY,application_id text NOT NULL,environment_id text,title text NOT NULL,status text NOT NULL DEFAULT 'open',created_by text,updated_at timestamptz NOT NULL DEFAULT now());
		CREATE TABLE branches (id text PRIMARY KEY,investigation_id text NOT NULL);
		CREATE TABLE runs (id text PRIMARY KEY,branch_id text NOT NULL,request_id text NOT NULL,status text NOT NULL);
		CREATE TABLE ops_incidents (id text PRIMARY KEY,investigation_id text NOT NULL,application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE ops_automation_runs (id text PRIMARY KEY,investigation_id text NOT NULL,incident_id text NOT NULL,environment_id text NOT NULL,kind text NOT NULL,run_id text NOT NULL DEFAULT '');
		CREATE TABLE team_members (team_id text NOT NULL,user_id text NOT NULL);
		CREATE TABLE IF NOT EXISTS users (id text PRIMARY KEY,email text NOT NULL,name text NOT NULL,role text NOT NULL,department_id text,disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz);
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE TABLE IF NOT EXISTS rbac_user_roles (user_id text NOT NULL,role_id text NOT NULL);
		CREATE TABLE IF NOT EXISTS rbac_department_roles (department_id text NOT NULL,role_id text NOT NULL);
		CREATE TABLE IF NOT EXISTS rbac_roles (id text PRIMARY KEY);
		CREATE TABLE IF NOT EXISTS v3_role_grants (role_id text NOT NULL,module text NOT NULL,action text NOT NULL,scope_kind text NOT NULL,scope_id text NOT NULL);
		CREATE VIEW v3_effective_role_grants AS
		 SELECT a.user_id,g.module,g.action,g.scope_kind,g.scope_id FROM rbac_user_roles a
		 JOIN rbac_roles r ON r.id=a.role_id JOIN v3_role_grants g ON g.role_id=r.id;
		INSERT INTO users(id,email,name,role) VALUES('read-owner','read@local','Read Owner','viewer'),('other','other@local','Other','viewer');
		INSERT INTO rbac_roles(id) VALUES('read-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('read-owner','read-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
		('read-role','environment','view','environment','test'),
		('read-role','environment','view','environment','dev'),
		('read-role','conversation','view','environment','test'),
		('read-role','conversation','view','environment','dev'),
		('read-role','canvas','view','environment','test');
		INSERT INTO v3_conversations(id,owner_id,environment_id,title,status,context_ref,updated_at) VALUES
		('read-a','read-owner','test','结算排查','active','service:checkout-api',now()+interval '2 minutes'),
		('read-b','read-owner','dev','库存复盘','resolved','service:inventory',now()+interval '1 minute'),
		('read-archive','read-owner','dev','归档记录','archived','',now()),
		('read-private','other','test','私人记录','active','',now()),
		('read-prod','read-owner','prod','生产记录','active','',now());
		INSERT INTO v3_canvases(id,owner_id,environment_id,title,status,services,nodes,updated_at) VALUES
		('CV-own','read-owner','test','容量评估','draft',ARRAY['billing-api'],'[{"id":"root"},{"id":"branch"}]',now()-interval '1 minute'),
		('CV-no-grant','read-owner','dev','授权外画布','done',ARRAY['inventory'],'[]',now()),
		('CV-other','other','test','他人画布','active',ARRAY['billing-api'],'[]',now());
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
			if granted && user.ID == "other" {
				return []string{"test"}, nil
			}
			return nil, nil
		},
	})
	chat.RegisterV3(server)
	policy := func(_ context.Context, user core.User, module, action, env string) error {
		if granted && user.ID == "read-owner" && module == "conversation" && action == "view" && (env == "test" || env == "dev") {
			return nil
		}
		if granted && user.ID == "other" && module == "conversation" && action == "view" && env == "test" {
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
			Mode         string    `json:"mode"`
			Status       string    `json:"status"`
			Source       *string   `json:"source"`
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
	if page.Total != 3 || page.Page != 1 || page.PageSize != 1 || len(page.Items) != 1 || page.Items[0].ID != "read-a" || page.Items[0].Scope != "checkout-api · test" || page.Items[0].Size != "2 轮" || page.Items[0].Tokens != nil || strings.Join(page.Items[0].Participants, ",") != "Read Owner,AI" {
		t.Fatalf("persisted first page: %+v", page)
	}
	if page.Counts["mine"] == nil || *page.Counts["mine"] != 3 || page.Counts["archived"] == nil || *page.Counts["archived"] != 1 || page.Counts["team"] == nil || *page.Counts["team"] != 0 || page.Counts["triage"] == nil || *page.Counts["triage"] != 0 {
		t.Fatalf("persisted source counts: %+v", page.Counts)
	}
	if rr := get(base+"?page_size=1&page=2", "all", "en-US", true); rr.Code != http.StatusOK {
		t.Fatalf("second page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 1 || page.Items[0].ID != "read-b" || page.Items[0].Size != "1 turns" || len(page.Items[0].Participants) != 1 || page.Items[0].Tokens == nil || *page.Items[0].Tokens != 0 {
		t.Fatalf("persisted second page: %+v", page)
	}
	if rr := get(base+"?page_size=1&page=3", "all", "en-US", true); rr.Code != http.StatusOK {
		t.Fatalf("third page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 3 || len(page.Items) != 1 || page.Items[0].ID != "CV-own" || page.Items[0].Mode != "canvas" || page.Items[0].Status != "active" || page.Items[0].Source == nil || *page.Items[0].Source != "manual" || page.Items[0].Scope != "billing-api · test" || page.Items[0].Size != "2 nodes" || page.Items[0].Tokens == nil || *page.Items[0].Tokens != 0 {
		t.Fatalf("generic canvas third page: %+v", page)
	}
	for _, query := range []string{"?scope=" + url.QueryEscape("checkout-api · test") + "&status=active&status=resolved", "?sort=tokens&page_size=1"} {
		rr := get(base+query, "all", "", true)
		if rr.Code != 200 {
			t.Fatalf("page query %s: %d %s", query, rr.Code, rr.Body.String())
		}
		if e := json.Unmarshal(rr.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		want := "read-a"
		total := 1
		if strings.Contains(query, "sort") {
			want = "CV-own"
			total = 3
		}
		if page.Total != total || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatalf("filter/sort before pagination %s: %+v", query, page)
		}
	}
	if rr := get(base+"?sort=recent&sort=title", "all", "", true); rr.Code != 422 {
		t.Fatalf("ambiguous sort accepted: %d", rr.Code)
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
	for _, query := range []string{"?mode=canvas&q=rongliang", "?mode=canvas&q=billing-api"} {
		if rr := get(base+query, "test", "zh-CN", true); rr.Code != http.StatusOK {
			t.Fatalf("canvas search %s: %d %s", query, rr.Code, rr.Body.String())
		} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "CV-own" || page.Items[0].Size != "2 个节点" || page.Counts["mine"] == nil || *page.Counts["mine"] != 2 {
			t.Fatalf("canvas filtered count/search %s: %+v", query, page)
		}
	}
	if rr := get(base+"?mode=canvas", "all", "", true); rr.Code != http.StatusOK {
		t.Fatalf("canvas mode all environments: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "CV-own" {
		t.Fatalf("other owner or ungranted canvas leaked: %+v", page)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_canvas_node_exchanges(id,canvas_id,state) VALUES('CNT-own','CV-own','completed')`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?mode=canvas", "test", "", true); rr.Code != http.StatusOK {
		t.Fatalf("canvas usage unknown: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if len(page.Items) != 1 || page.Items[0].Tokens != nil || strings.Join(page.Items[0].Participants, ",") != "Read Owner,AI" {
		t.Fatalf("canvas exchanges have unknown token usage: %+v", page)
	}
	// The list entry itself cannot require conversation.view: a canvas-only
	// owner must see their own canvas without seeing private chat rows.
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='read-role' AND module='conversation'`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base, "test", "", true); rr.Code != http.StatusOK {
		t.Fatalf("canvas-only list: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "CV-own" || page.Counts["mine"] == nil || *page.Counts["mine"] != 1 {
		t.Fatalf("canvas-only source leaked chat or omitted canvas: %+v", page)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
		('read-role','conversation','view','environment','test'),('read-role','conversation','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?tab=archived", "all", "", true); rr.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "read-archive" {
		t.Fatalf("archive projection: %+v", page)
	}
	if rr := get(base+"?tab=archived&mode=canvas", "all", "", true); rr.Code != http.StatusOK {
		t.Fatalf("archived generic canvases: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 0 || len(page.Items) != 0 || page.Counts["archived"] == nil || *page.Counts["archived"] != 1 {
		t.Fatalf("archived facet/filtered page mismatch: %+v", page)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,run,state)
	 VALUES('read-b-stream','read-b','assistant','AI','[]',NULL,'streaming')`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?mode=chat&q=inventory", "dev", "", true); rr.Code != http.StatusOK {
		t.Fatalf("streaming usage page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "read-b" || page.Items[0].Tokens != nil {
		t.Fatalf("incomplete assistant usage must remain unknown: %+v", page)
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
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_messages SET run='{"tokens":9007199254740991}' WHERE id='read-unknown'`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?page_size=1&page=1", "all", "", true); rr.Code != http.StatusOK {
		t.Fatalf("unsafe integer usage page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if len(page.Items) != 1 || page.Items[0].Tokens != nil {
		t.Fatalf("tokens beyond JavaScript's exact integer range must remain unknown: %+v", page.Items)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,owner_team_id) VALUES
		('app-team','Team Application','team-one'),('app-other','Other Application','team-two');
		INSERT INTO application_environments(id,application_id,key) VALUES
		('ae-team','app-team','test'),('ae-other','app-other','test');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES
		('app-team','test'),('app-other','test');
		INSERT INTO team_members(team_id,user_id) VALUES('team-one','read-owner');
		INSERT INTO investigations(id,application_id,environment_id,title,created_by,updated_at) VALUES
		('INV-mine','app-team','ae-team','Own investigation','read-owner',now()-interval '4 minutes'),
		('INV-triage','app-team','ae-team','Automatic investigation',NULL,now()-interval '5 minutes'),
		('INV-unknown','app-team','ae-team','Historical investigation',NULL,now()-interval '6 minutes'),
		('INV-other-team','app-other','ae-other','Hidden team investigation',NULL,now()-interval '7 minutes');
		INSERT INTO branches(id,investigation_id) VALUES
		('b-mine','INV-mine'),('b-triage','INV-triage'),('b-unknown','INV-unknown'),('b-other','INV-other-team');
		INSERT INTO ops_incidents(id,investigation_id,application_id,environment_id) VALUES
		('INC-triage','INV-triage','app-team','ae-team');
		INSERT INTO ops_automation_runs(id,investigation_id,incident_id,environment_id,kind,run_id) VALUES
		('auto-one','INV-triage','INC-triage','ae-team','initial','run-one');
		INSERT INTO runs(id,branch_id,request_id,status) VALUES
		('run-one','b-triage','automation:auto-one','completed');`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?tab=team&page_size=2", "test", "", true); rr.Code != http.StatusOK {
		t.Fatalf("team page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 3 || len(page.Items) != 2 || page.Counts["team"] == nil || *page.Counts["team"] != 3 || page.Counts["triage"] == nil || *page.Counts["triage"] != 1 {
		t.Fatalf("team page/count: %+v", page)
	}
	if rr := get(base+"?tab=triage", "test", "", true); rr.Code != http.StatusOK {
		t.Fatalf("triage page: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "INV-triage" || page.Items[0].Source != nil || page.Items[0].Tokens != nil {
		t.Fatalf("triage provenance: %+v", page)
	}
	if rr := get(base+"?tab=mine&mode=canvas", "test", "", true); rr.Code != http.StatusOK {
		t.Fatalf("mine canvas: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != "CV-own" || page.Items[1].ID != "INV-mine" {
		t.Fatalf("mine creator scope: %+v", page)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_roles(id) VALUES('other-team-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('other','other-team-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
		('other-team-role','environment','view','environment','test'),
		('other-team-role','canvas','view','team','team-one');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'other',now()+interval '1 hour')`, store.Hash("other-team-session")); err != nil {
		t.Fatal(err)
	}
	getOther := func(tab string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, base+"?tab="+tab, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "other-team-session"})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	if rr := getOther("triage"); rr.Code != http.StatusOK {
		t.Fatalf("nonmember team grant read status=%d body=%s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 0 || page.Counts["team"] == nil || *page.Counts["team"] != 0 {
		t.Fatalf("team grant without membership exposed investigation: %+v", page)
	}
	if _, err := db.Exec(ctx, `INSERT INTO team_members(team_id,user_id) VALUES('team-one','other')`); err != nil {
		t.Fatal(err)
	}
	if rr := getOther("triage"); rr.Code != http.StatusOK {
		t.Fatalf("member team grant read status=%d body=%s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 1 || page.Items[0].ID != "INV-triage" || page.Counts["team"] == nil || *page.Counts["team"] != 3 {
		t.Fatalf("current team grant not honored: %+v", page)
	}
	if _, err := db.Exec(ctx, `DELETE FROM team_members WHERE user_id='read-owner'`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?tab=team", "test", "", true); rr.Code != http.StatusOK {
		t.Fatalf("team revocation: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 0 || len(page.Items) != 0 || page.Counts["team"] == nil || *page.Counts["team"] != 0 {
		t.Fatalf("revoked team membership visible: %+v", page)
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
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='read-role' AND module='canvas'`); err != nil {
		t.Fatal(err)
	}
	if rr := get(base+"?mode=canvas", "test", "", true); rr.Code != http.StatusOK {
		t.Fatalf("revoked canvas grant: %d %s", rr.Code, rr.Body.String())
	} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	} else if page.Total != 0 || len(page.Items) != 0 || page.Counts["mine"] == nil || *page.Counts["mine"] != 1 {
		t.Fatalf("revoked canvas still counted or shown: %+v", page)
	}
	// The list holds its session and grant sources until the page has been
	// read. Concurrent revocations wait; the following request observes both.
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `LOCK TABLE v3_conversation_messages IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	reading := make(chan *httptest.ResponseRecorder, 1)
	go func() { reading <- get(base+"?mode=chat", "test", "", true) }()
	waitForLock := func(pattern string) {
		t.Helper()
		for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			var waiting bool
			if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			 WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, pattern).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
		}
		t.Fatalf("query did not wait for lock: %s", pattern)
	}
	waitForLock("WITH owned AS MATERIALIZED%")
	sessionRevoked := make(chan error, 1)
	go func() {
		_, revokeErr := db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash("read-session"))
		sessionRevoked <- revokeErr
	}()
	grantRevoked := make(chan error, 1)
	go func() {
		_, revokeErr := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='read-role' AND module='environment' AND scope_id='test'`)
		grantRevoked <- revokeErr
	}()
	waitForLock("DELETE FROM sessions WHERE token_hash=%")
	waitForLock("DELETE FROM v3_role_grants WHERE role_id=%")
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if rr := <-reading; rr.Code != http.StatusOK {
		t.Fatalf("authorized read did not finish before revocation: %d %s", rr.Code, rr.Body.String())
	}
	if err = <-sessionRevoked; err != nil {
		t.Fatal(err)
	}
	if err = <-grantRevoked; err != nil {
		t.Fatal(err)
	}
	if rr := get(base, "test", "", true); rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session remained valid: %d %s", rr.Code, rr.Body.String())
	}
	if _, err = db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'read-owner',now()+interval '1 hour')`, store.Hash("read-session")); err != nil {
		t.Fatal(err)
	}
	if rr := get(base, "test", "", true); rr.Code != http.StatusForbidden {
		t.Fatalf("revoked environment grant remained valid: %d %s", rr.Code, rr.Body.String())
	}
	granted = false
	if rr := get(base, "test", "", true); rr.Code != http.StatusForbidden {
		t.Fatalf("revoked policy: %d %s", rr.Code, rr.Body.String())
	}
}
