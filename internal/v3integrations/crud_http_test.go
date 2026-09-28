package v3integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConnectorCRUDHTTPV3ScopePersistenceAndAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_crud_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','environment','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method)
 VALUES('other-environment','test','prometheus','Other environment','observe','https://other.example.invalid','none')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", nil).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, environment, session, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept-Language", "zh-CN")
		r.Header.Set("X-AIOps-Environment", environment)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("status=%d body=%s; want %d %q", w.Code, w.Body.String(), status, code)
		}
	}
	createBody := `{"type":"gitlab","url":"https://gitlab.example.invalid","auth":"pat","credential_ref":"vault://isolated/gitlab#token","tools":["list_pipelines","get_pipeline"],"webhook":false}`
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", "", createBody), 401, "unauthorized")
	assert(request(http.MethodPost, "/api/v1/connectors", "all", token, createBody), 409, "env_view_only")
	assert(request(http.MethodPost, "/api/v1/connectors", "test", token, createBody), 403, "forbidden")
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token, createBody), 403, "forbidden")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	wizard := request(http.MethodPost, "/api/v1/connectors", "dev", token,
		fmt.Sprintf(`{"type":"gitlab","url":%q,"auth":"oauth","tools":["list_pipelines","get_pipeline"],"webhook":true}`, upstream.URL))
	assert(wizard, 201, "")
	var wizardResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(wizard.Body.Bytes(), &wizardResult); err != nil || wizardResult.ID == "" {
		t.Fatalf("wizard registration: %s %v", wizard.Body.String(), err)
	}
	var draftCredential, draftStatus string
	var draftWebhook, draftEnabled bool
	var draftTools []byte
	if err := db.QueryRow(ctx, `SELECT credential_ref,webhook_requested,enabled,status,tools FROM v3_connectors WHERE id=$1`, wizardResult.ID).
		Scan(&draftCredential, &draftWebhook, &draftEnabled, &draftStatus, &draftTools); err != nil {
		t.Fatal(err)
	}
	if draftCredential != "" || !draftWebhook || draftEnabled || draftStatus != "unverified" {
		t.Fatalf("wizard registration was active: credential=%q webhook=%v enabled=%v status=%q", draftCredential, draftWebhook, draftEnabled, draftStatus)
	}
	var pendingTools []tool
	if err := json.Unmarshal(draftTools, &pendingTools); err != nil || len(pendingTools) != 2 || pendingTools[0].Enabled || pendingTools[1].Enabled {
		t.Fatalf("wizard tools were active: %s %v", draftTools, err)
	}
	assert(request(http.MethodPost, "/api/v1/connectors/"+wizardResult.ID+"/test", "dev", token, ""), 409, "connector_disabled")
	if upstreamCalls.Load() != 0 {
		t.Fatalf("disabled wizard contacted upstream %d times", upstreamCalls.Load())
	}
	assert(request(http.MethodPatch, "/api/v1/connectors/"+wizardResult.ID, "dev", token,
		`{"tools":[{"name":"list_pipelines","enabled":true}]}`), 409, "connector_disabled")
	draftList := request(http.MethodGet, "/api/v1/connectors?category=delivery&q="+wizardResult.ID, "dev", token, "")
	assert(draftList, 200, "")
	if !strings.Contains(draftList.Body.String(), "未启用") || strings.Contains(draftList.Body.String(), "list_pipelines") {
		t.Fatalf("wizard registration was presented as active: %s", draftList.Body.String())
	}
	created := request(http.MethodPost, "/api/v1/connectors", "dev", token, createBody)
	assert(created, 201, "")
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" {
		t.Fatalf("create DTO: %s %v", created.Body.String(), err)
	}
	settingsPath := "/api/v1/connectors/" + result.ID + "/tool-settings"
	assert(request(http.MethodGet, settingsPath, "dev", "", ""), 401, "unauthorized")
	assert(request(http.MethodGet, "/api/v1/connectors/other-environment/tool-settings", "dev", token, ""), 404, "not_found")
	settings := request(http.MethodGet, settingsPath, "dev", token, "")
	assert(settings, 200, "")
	var registered struct {
		ConnectorID             string `json:"connector_id"`
		EnvironmentID           string `json:"environment_id"`
		ConnectorEnabled        bool   `json:"connector_enabled"`
		AgentExecutionAvailable bool   `json:"agent_execution_available"`
		RegisteredTools         []tool `json:"registered_tools"`
	}
	if err := json.Unmarshal(settings.Body.Bytes(), &registered); err != nil || registered.ConnectorID != result.ID ||
		registered.EnvironmentID != "dev" || registered.ConnectorEnabled || registered.AgentExecutionAvailable ||
		len(registered.RegisteredTools) != 2 || registered.RegisteredTools[0].Enabled || registered.RegisteredTools[1].Enabled ||
		strings.Contains(settings.Body.String(), "vault://") {
		t.Fatalf("tool settings misreported registration or Agent capability: %s %v", settings.Body.String(), err)
	}
	ready := request(http.MethodPost, "/api/v1/connectors", "dev", token,
		`{"type":"prometheus","url":"https://metrics.example.invalid","auth":"none","tools":["query_metrics"]}`)
	assert(ready, 201, "")
	var readyResult struct{ ID string }
	if err := json.Unmarshal(ready.Body.Bytes(), &readyResult); err != nil || readyResult.ID == "" {
		t.Fatalf("ready registration: %s %v", ready.Body.String(), err)
	}
	readySettingsPath := "/api/v1/connectors/" + readyResult.ID + "/tool-settings"
	readySettings := request(http.MethodGet, readySettingsPath, "dev", token, "")
	assert(readySettings, 200, "")
	var readyTools struct {
		ConnectorEnabled        bool   `json:"connector_enabled"`
		AgentExecutionAvailable bool   `json:"agent_execution_available"`
		RegisteredTools         []tool `json:"registered_tools"`
	}
	if err := json.Unmarshal(readySettings.Body.Bytes(), &readyTools); err != nil || !readyTools.ConnectorEnabled ||
		readyTools.AgentExecutionAvailable || len(readyTools.RegisteredTools) != 1 || !readyTools.RegisteredTools[0].Enabled {
		t.Fatalf("enabled setting was not read back honestly: %s %v", readySettings.Body.String(), err)
	}
	assert(request(http.MethodPatch, "/api/v1/connectors/"+readyResult.ID, "dev", token,
		`{"tools":[{"name":"query_metrics","enabled":false}]}`), 200, "")
	readySettings = request(http.MethodGet, readySettingsPath, "dev", token, "")
	assert(readySettings, 200, "")
	if err := json.Unmarshal(readySettings.Body.Bytes(), &readyTools); err != nil || len(readyTools.RegisteredTools) != 1 || readyTools.RegisteredTools[0].Enabled {
		t.Fatalf("disabled setting was not read back: %s %v", readySettings.Body.String(), err)
	}
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token, createBody), 409, "connector_exists")
	var environment, credential, status string
	var enabled bool
	var persistedTools []byte
	if err := db.QueryRow(ctx, `SELECT environment_id,credential_ref,status,enabled,tools FROM v3_connectors WHERE id=$1`, result.ID).
		Scan(&environment, &credential, &status, &enabled, &persistedTools); err != nil {
		t.Fatal(err)
	}
	if environment != "dev" || credential != "vault://isolated/gitlab#token" || status != "unverified" || enabled || !strings.Contains(string(persistedTools), "get_pipeline") {
		t.Fatalf("persisted connector mismatch: environment=%s status=%s tools=%s", environment, status, persistedTools)
	}
	listed := request(http.MethodGet, "/api/v1/connectors?category=delivery&q=gitlab", "all", token, "")
	assert(listed, 200, "")
	if strings.Contains(listed.Body.String(), "vault://") || strings.Contains(listed.Body.String(), "other-environment") {
		t.Fatalf("list leaked credential or foreign environment: %s", listed.Body.String())
	}
	detail := request(http.MethodGet, "/api/v1/connectors/"+result.ID, "dev", token, "")
	assert(detail, 200, "")
	if strings.Contains(detail.Body.String(), "vault://") || strings.Contains(detail.Body.String(), "token") {
		t.Fatalf("detail leaked credential: %s", detail.Body.String())
	}
	assert(request(http.MethodGet, "/api/v1/connectors/other-environment", "all", token, ""), 404, "not_found")
	assert(request(http.MethodPatch, "/api/v1/connectors/other-environment", "dev", token,
		`{"tools":[{"name":"query_metrics","enabled":false}]}`), 404, "not_found")
	patched := request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"list_pipelines","enabled":false}]}`)
	assert(patched, 200, "")
	var patchResult struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(patched.Body.Bytes(), &patchResult); err != nil || len(patchResult.Tools) != 2 || patchResult.Tools[0].Desc != "读取流水线列表与状态" {
		t.Fatalf("localized patch DTO: %s %v", patched.Body.String(), err)
	}
	var tools []tool
	if err := db.QueryRow(ctx, `SELECT tools FROM v3_connectors WHERE id=$1`, result.ID).Scan(&persistedTools); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(persistedTools, &tools); err != nil || len(tools) != 2 || tools[0].Enabled || tools[1].Enabled {
		t.Fatalf("partial tool update lost other selection: %s %v", persistedTools, err)
	}
	assert(request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"list_pipelines"}]}`), 422, "invalid_tools")
	assert(request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"list_pipelines","enabled":null}]}`), 422, "invalid_tools")
	assert(request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"unknown","enabled":true}]}`), 422, "invalid_tools")
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action IN ('connector.created','connector.tools.updated')`, result.ID).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	var auditChanges string
	if err := db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE target=$1 AND action='connector.created'`, result.ID).Scan(&auditChanges); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditChanges, "vault://") || strings.Contains(auditChanges, "gitlab.example.invalid") {
		t.Fatalf("audit leaked connection settings: %s", auditChanges)
	}
	// Hold the row until both PATCH transactions are waiting for its lock.
	// Releasing it then proves that both partial updates merge in lock order.
	lockTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx)
	var lockedID string
	if err := lockTx.QueryRow(ctx, `SELECT id FROM v3_connectors WHERE id=$1 FOR UPDATE`, result.ID).Scan(&lockedID); err != nil || lockedID != result.ID {
		t.Fatalf("hold connector lock: %s %v", lockedID, err)
	}
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, name := range []string{"get_commit", "list_merge_requests"} {
		go func(name string) {
			responses <- request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
				fmt.Sprintf(`{"tools":[{"name":%q,"enabled":false}]}`, name))
		}(name)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
 WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%FROM v3_connectors%FOR UPDATE%'`, schema).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected two PATCH transactions waiting for row lock, saw %d", waiting)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		assert(<-responses, 200, "")
	}
	settings = request(http.MethodGet, settingsPath, "dev", token, "")
	assert(settings, 200, "")
	if err := json.Unmarshal(settings.Body.Bytes(), &registered); err != nil || len(registered.RegisteredTools) != 4 {
		t.Fatalf("concurrent settings were lost: %s %v", settings.Body.String(), err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT reject_tool_update_audit
 CHECK (action <> 'connector.tools.updated') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPatch, "/api/v1/connectors/"+readyResult.ID, "dev", token,
		`{"tools":[{"name":"query_metrics","enabled":true}]}`), 503, "audit_unavailable")
	readySettings = request(http.MethodGet, readySettingsPath, "dev", token, "")
	assert(readySettings, 200, "")
	if err := json.Unmarshal(readySettings.Body.Bytes(), &readyTools); err != nil || len(readyTools.RegisteredTools) != 1 || readyTools.RegisteredTools[0].Enabled {
		t.Fatalf("audit failure left a tool setting enabled: %s %v", readySettings.Body.String(), err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT reject_tool_update_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET tools=tools || '[{"name":"retired_tool","desc":"old catalog entry","enabled":true}]'::jsonb WHERE id=$1`, result.ID); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, settingsPath, "dev", token, ""), 503, "connector_state_unavailable")
	assert(request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"list_pipelines","enabled":false}]}`), 409, "tool_catalog_changed")
	if err := db.QueryRow(ctx, `SELECT tools FROM v3_connectors WHERE id=$1`, result.ID).Scan(&persistedTools); err != nil || !strings.Contains(string(persistedTools), "retired_tool") {
		t.Fatalf("catalog drift silently erased stored settings: %s %v", persistedTools, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"get_pipeline","enabled":false}]}`), 403, "forbidden")
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, settingsPath, "dev", token, ""), 403, "forbidden")
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token,
		`{"type":"prometheus","url":"https://metrics.example.invalid","auth":"none"}`), 403, "forbidden")
}
