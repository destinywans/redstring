package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type blockedMCPReadWriter struct {
	*httptest.ResponseRecorder
	reached chan struct{}
	release chan struct{}
}

func (w *blockedMCPReadWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *blockedMCPReadWriter) Write(data []byte) (int, error) {
	select {
	case w.reached <- struct{}{}:
	default:
	}
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func TestMCPReadsAndBindingUseSingleConnectionWithoutPartialLists(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_reader_" + store.ID()
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
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := aiextensions.New(db, "").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','application','app'),
 ('viewer','connectors','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES
 ('app','app','App','dev',false),('hidden','hidden','Hidden','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('app-dev','app','dev','Dev',false),('hidden-dev','hidden','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app','dev'),('hidden','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	record := aiextensions.Record{
		ID: "mcp-1", ApplicationID: "app", Kind: "mcp", Title: "App MCP",
		Maintainer: "platform", Enabled: true, Version: 1, UpdatedAt: time.Now().UTC(),
		MCP: &aiextensions.MCPConfig{Endpoint: "https://private.example.invalid/mcp", CredentialRef: "secret-ref",
			Bindings: []aiextensions.Binding{{Name: "read_app", Tool: "read_app", ReadOnlyConfirmed: true, Arguments: map[string]any{}}}},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES('mcp-1','app','mcp',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(db, policy)
	service.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "stage"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, languages ...string) *httptest.ResponseRecorder {
		t.Helper()
		callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, nil).WithContext(callCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		if len(languages) > 0 {
			r.Header.Set("Accept-Language", languages[0])
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/mcp-servers", "/api/v1/mcp-servers/mcp-1"} {
		w := request(http.MethodGet, path)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"complete_v3_dto":false`) ||
			strings.Contains(w.Body.String(), "secret-ref") || strings.Contains(w.Body.String(), "private.example.invalid") {
			t.Fatalf("read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	list := request(http.MethodGet, "/api/v1/mcp-servers", "en-US")
	var listDTO struct {
		Complete    bool     `json:"complete_v3_dto"`
		Unavailable []string `json:"unavailable_fields"`
		KPIs        []struct {
			ID, Label, Sub string
			Value          *string
		} `json:"kpis"`
		Items []struct {
			ID, Caps, Last, Status string
			Trust                  struct{ Label, Tone string }
			Apps                   []any `json:"apps"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listDTO); err != nil {
		t.Fatal(err)
	}
	if list.Code != http.StatusOK || listDTO.Complete || len(listDTO.Unavailable) == 0 || len(listDTO.KPIs) != 3 ||
		listDTO.KPIs[0].Value == nil || *listDTO.KPIs[0].Value != "1" || listDTO.KPIs[1].Value == nil || *listDTO.KPIs[1].Value != "1" || listDTO.KPIs[1].Label != "Registered read-only bindings" || listDTO.KPIs[2].Value != nil ||
		len(listDTO.Items) != 1 || listDTO.Items[0].ID != "mcp-1" || listDTO.Items[0].Status != "pending" ||
		listDTO.Items[0].Trust.Tone != "warn" || listDTO.Items[0].Apps == nil || len(listDTO.Items[0].Apps) != 0 ||
		listDTO.Items[0].Caps != "MCP capabilities unverified" || listDTO.Items[0].Last != "V3 call history unavailable" {
		t.Fatalf("registration presented as live or reviewed: %s", list.Body.String())
	}
	detail := request(http.MethodGet, "/api/v1/mcp-servers/mcp-1", "zh-CN")
	var detailDTO struct {
		Complete                         bool     `json:"complete_v3_dto"`
		Unavailable                      []string `json:"unavailable_fields"`
		Status, Last                     string
		Version                          *string
		Counts                           struct{ Tools, Resources, Prompts *int } `json:"counts"`
		Tools, Resources, Prompts, Calls []any
		TrustInfo                        struct {
			ReadOnlyCheck string   `json:"readonly_check"`
			Scope         []string `json:"scope"`
			Limit, Auth   string
		} `json:"trust_info"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailDTO); err != nil {
		t.Fatal(err)
	}
	if detail.Code != http.StatusOK || detailDTO.Complete || detailDTO.Status != "pending" ||
		detailDTO.Version != nil || detailDTO.Last != "V3 调用记录未接入" ||
		detailDTO.Counts.Tools != nil || detailDTO.Counts.Resources != nil || detailDTO.Counts.Prompts != nil ||
		detailDTO.Tools != nil || detailDTO.Resources != nil || detailDTO.Prompts != nil || detailDTO.Calls != nil ||
		detailDTO.TrustInfo.ReadOnlyCheck != "尚无当前版本人工审核" || detailDTO.TrustInfo.Scope == nil || len(detailDTO.TrustInfo.Scope) != 0 ||
		detailDTO.TrustInfo.Limit != "V3 Agent 调用尚不可用" || detailDTO.TrustInfo.Auth != "已登记私有文件凭据引用，当前有效性未验证" {
		t.Fatalf("detail presented unavailable MCP evidence as available: %s", detail.Body.String())
	}
	// One registration may be bound to two environments. Each list row must
	// retain its own binding identity; detail always requires one environment.
	for _, statement := range []string{
		`INSERT INTO teams(id,name) VALUES('owner-team','Owner Team'),('other-team','Other Team')`,
		`UPDATE applications SET owner_team_id='owner-team' WHERE id='app'`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES('app-stage','app','stage','Stage',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','stage')`,
		`INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','stage',id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','environment','view','environment','stage')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	scopedGet := func(env, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	all := scopedGet("all", "/api/v1/mcp-servers")
	var boundRows struct {
		Items []struct {
			ID                string `json:"id"`
			EnvironmentID     string `json:"environment_id"`
			BindingInstanceID string `json:"binding_instance_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(all.Body.Bytes(), &boundRows); err != nil {
		t.Fatal(err)
	}
	if all.Code != http.StatusOK || len(boundRows.Items) != 2 || boundRows.Items[0].ID != "mcp-1" || boundRows.Items[1].ID != "mcp-1" ||
		boundRows.Items[0].EnvironmentID == boundRows.Items[1].EnvironmentID ||
		boundRows.Items[0].BindingInstanceID == boundRows.Items[1].BindingInstanceID {
		t.Fatalf("multi-environment bindings conflated: %d %s", all.Code, all.Body.String())
	}
	if w := scopedGet("all", "/api/v1/mcp-servers/mcp-1"); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("all-environment detail selected an arbitrary binding: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','view','team','owner-team')`); err != nil {
		t.Fatal(err)
	}
	if w := scopedGet("all", "/api/v1/mcp-servers"); w.Code != http.StatusOK || strings.Count(w.Body.String(), `"id":"mcp-1"`) != 2 {
		t.Fatalf("persisted owner team grant did not expose bound app: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='other-team' WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	if w := scopedGet("all", "/api/v1/mcp-servers"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("unrelated team grant leaked MCP binding: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='owner-team' WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='environment' AND scope_id='stage'`); err != nil {
		t.Fatal(err)
	}
	if w := scopedGet("all", "/api/v1/mcp-servers"); w.Code != http.StatusOK || strings.Count(w.Body.String(), `"id":"mcp-1"`) != 1 {
		t.Fatalf("ungranted environment leaked MCP binding: %d %s", w.Code, w.Body.String())
	}
	if w := scopedGet("stage", "/api/v1/mcp-servers/mcp-1"); w.Code != http.StatusForbidden {
		t.Fatalf("stage detail bypassed environment grant: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_mcp_environment_bindings WHERE extension_id='mcp-1' AND environment_id='stage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	var bindingInstance string
	if err := db.QueryRow(ctx, `SELECT binding_instance_id FROM v3_mcp_environment_bindings WHERE extension_id='mcp-1' AND environment_id='dev'`).Scan(&bindingInstance); err != nil {
		t.Fatal(err)
	}
	observedTools := []byte(`[{"name":"read_app","description":"untrusted observation","read_only_hint":true,"input_schema":{"type":"object"}}]`)
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_discovery_snapshots
 (id,extension_id,binding_instance_id,application_id,environment_id,registration_version,registration_hash,tools_hash,tools,tool_count,captured_by)
 VALUES($1,'mcp-1',$2,'app','dev',1,$3,$4,$5,1,$6)`,
		store.ID(), bindingInstance, digestBytes(raw), digestBytes(observedTools), observedTools, viewer.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/mcp-servers", "/api/v1/mcp-servers/mcp-1"} {
		w := request(http.MethodGet, path)
		// The binding itself may be enabled; untrusted discovery must not enable any tool or report health.
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"pending"`) ||
			!strings.Contains(w.Body.String(), `"complete_v3_dto":false`) ||
			discoveredToolEnabled(t, w.Body.Bytes()) || strings.Contains(w.Body.String(), `"status":"ok"`) {
			t.Fatalf("untrusted discovery promoted to health or Agent tool: %s %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request(http.MethodPut, "/api/v1/mcp-servers/mcp-1/environment-binding"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"changed":false`) {
		t.Fatalf("idempotent binding: %d %s", w.Code, w.Body.String())
	}
	// A committed logout or grant revoke must never race past a read that is
	// already sending observed tool and resource metadata. The transaction
	// holds the session/grant/source locks until the bounded body write ends.
	assertReadPinsRevocation := func(path, statement string, args ...any) {
		t.Helper()
		writer := &blockedMCPReadWriter{ResponseRecorder: httptest.NewRecorder(), reached: make(chan struct{}, 1), release: make(chan struct{})}
		readDone := make(chan struct{})
		readToken := session
		go func() {
			defer close(readDone)
			readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(readCtx)
			r.Header.Set("X-AIOps-Environment", "dev")
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: readToken})
			api.ServeHTTP(writer, r)
		}()
		select {
		case <-writer.reached:
		case <-time.After(5 * time.Second):
			close(writer.release)
			t.Fatal("MCP read did not reach response write")
		}
		revokeTx, err := adminDB.Begin(ctx)
		if err != nil {
			close(writer.release)
			t.Fatal(err)
		}
		if _, err = revokeTx.Exec(ctx, `SET LOCAL lock_timeout='250ms'`); err == nil {
			_, err = revokeTx.Exec(ctx, statement, args...)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			_ = revokeTx.Rollback(context.Background())
			close(writer.release)
			t.Fatalf("revocation passed active MCP response lock: %v", err)
		}
		_ = revokeTx.Rollback(context.Background())
		close(writer.release)
		select {
		case <-readDone:
		case <-time.After(5 * time.Second):
			t.Fatal("MCP read did not finish after response release")
		}
		if writer.Code != http.StatusOK {
			t.Fatalf("locked MCP read failed: %d %s", writer.Code, writer.Body.String())
		}
		tag, err := adminDB.Exec(ctx, statement, args...)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("post-read revocation failed: %v %v", tag, err)
		}
	}
	assertReadPinsRevocation("/api/v1/mcp-servers", `DELETE FROM `+quoted+`.v3_role_grants
 WHERE role_id='viewer' AND module='connectors' AND action='view'`)
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("revoked grant still read MCP list: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	assertReadPinsRevocation("/api/v1/mcp-servers/mcp-1", `DELETE FROM `+quoted+`.sessions WHERE token_hash=$1`, store.Hash(session))
	if w := request(http.MethodGet, "/api/v1/mcp-servers/mcp-1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session still read MCP detail: %d %s", w.Code, w.Body.String())
	}
	_, session, err = st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("revoked list: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers/mcp-1"); w.Code != http.StatusNotFound {
		t.Fatalf("revoked detail: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','connectors','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record)
 SELECT 'extra-'||g,'hidden','mcp',1,
 jsonb_build_object('id','extra-'||g,'application_id','hidden','kind','mcp','version',1,
  'title','Hidden','mcp',jsonb_build_object('bindings','[]'::jsonb))
 FROM generate_series(1,1000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'extra-'||g,'hidden','dev',(SELECT id FROM users WHERE email='admin@example.invalid')
 FROM generate_series(1,1000) g`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"id":"mcp-1"`) || strings.Contains(w.Body.String(), `"id":"extra-`) {
		t.Fatalf("hidden records blocked visible list: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','connectors','view','application','hidden')`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(w.Body.String(), `"code":"scope_too_large"`) || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("truncated list: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_mcp_environment_bindings WHERE extension_id='mcp-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record)
 SELECT 'extra-'||g,'hidden','mcp',1,
 jsonb_build_object('id','extra-'||g,'application_id','hidden','kind','mcp','version',1,
  'title','Hidden','mcp',jsonb_build_object('bindings','[]'::jsonb))
 FROM generate_series(1001,2000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'extra-'||g,'hidden','dev',(SELECT id FROM users WHERE email='admin@example.invalid')
 FROM generate_series(1001,2000) g`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("exact candidate cap should allow an empty visible list: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record)
 VALUES('extra-2001','hidden','mcp',1,
 jsonb_build_object('id','extra-2001','application_id','hidden','kind','mcp',
  'title','Hidden','mcp',jsonb_build_object('bindings','[]'::jsonb)))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'extra-2001','hidden','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(w.Body.String(), `"code":"candidate_scan_limit"`) || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("candidate overflow should not return a partial list: %d %s", w.Code, w.Body.String())
	}
}

// discoveredToolEnabled reports whether any object under a "tools" key has enabled=true.
func discoveredToolEnabled(t *testing.T, body []byte) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	var walk func(any, bool) bool
	walk = func(node any, underTools bool) bool {
		switch x := node.(type) {
		case map[string]any:
			if underTools && x["enabled"] == true {
				return true
			}
			for k, child := range x {
				if walk(child, underTools || k == "tools") {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if walk(child, underTools) {
					return true
				}
			}
		}
		return false
	}
	return walk(v, false)
}
