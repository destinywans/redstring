package v3integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectorInteractionSettingsDisableAndWebhookIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_interactions_" + store.ID()
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

	// A live viewer has environment visibility but no connector management.
	// Rejected writes must be audited outside the module/environment gates.
	failedAuditCount := func(action string) int {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action=$1 AND outcome='failed'`, action).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	auditedReject := func(method, path, environment, body, action string, status int, code string) {
		t.Helper()
		before := failedAuditCount(action)
		assert(request(method, path, environment, token, body), status, code)
		if after := failedAuditCount(action); after != before+1 {
			t.Fatalf("%s failed audit count=%d want=%d", action, after, before+1)
		}
		var target, raw string
		if err := db.QueryRow(ctx, `SELECT target,changes::text FROM management_audit WHERE action=$1 AND outcome='failed' ORDER BY chain_seq DESC LIMIT 1`, action).Scan(&target, &raw); err != nil {
			t.Fatal(err)
		}
		var changes map[string]any
		if err := json.Unmarshal([]byte(raw), &changes); err != nil {
			t.Fatal(err)
		}
		if len(changes) != 3 || changes["http_status"] != float64(status) || changes["code"] != code {
			t.Fatalf("rejection audit contains unexpected fields: %s", raw)
		}
		if target == "" || strings.Contains(raw, "PRIVATE_REQUEST_CANARY") || strings.Contains(target, "PRIVATE_REQUEST_CANARY") || strings.Contains(raw, body) {
			t.Fatalf("rejection audit retained request payload or lacked stable target")
		}
	}
	auditedReject("PUT", "/api/v1/connectors/editable/settings", "dev", `{"expected_revision":1,"address":"https://PRIVATE_REQUEST_CANARY.invalid","scope":"dev"}`, "v3.connector.settings_updated", 403, "forbidden")
	auditedReject("POST", "/api/v1/connectors/editable/disable", "dev", `{"reason":"PRIVATE_REQUEST_CANARY"}`, "v3.connector.disabled", 403, "forbidden")
	auditedReject("POST", "/api/v1/webhooks", "dev", `{"name":"PRIVATE_REQUEST_CANARY","type":"change","verify":"hmac"}`, "v3.webhook.registration_unavailable", 403, "forbidden")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','connectors','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled,tools,status,last_checked_at,last_latency_ms) VALUES('editable','dev','prometheus','Prometheus','observe','https://old.example.invalid','none',true,'[]','ok',now(),1)`); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/connectors/editable/settings"
	auditedReject("PUT", path, "all", `{"expected_revision":1,"address":"https://PRIVATE_REQUEST_CANARY.invalid","scope":"dev"}`, "v3.connector.settings_updated", 409, "env_view_only")
	auditedReject("POST", "/api/v1/connectors/editable/disable", "all", `{"reason":"PRIVATE_REQUEST_CANARY"}`, "v3.connector.disabled", 409, "env_view_only")
	auditedReject("POST", "/api/v1/webhooks", "all", `{"name":"PRIVATE_REQUEST_CANARY","type":"change","verify":"hmac"}`, "v3.webhook.registration_unavailable", 409, "env_view_only")
	assert(request("PUT", path, "dev", token, `{"expected_revision":1,"address":"https://new.example.invalid","scope":"test"}`), 422, "invalid_scope")
	changed := request("PUT", path, "dev", token, `{"expected_revision":1,"address":"https://new.example.invalid","scope":"dev"}`)
	assert(changed, 200, "")
	var detail map[string]any
	if err := json.Unmarshal(changed.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["revision"] != float64(2) || detail["enabled"] != false || detail["status"] != "unverified" || detail["address"] != "https://new.example.invalid" {
		t.Fatalf("settings projection %s", changed.Body.String())
	}
	assert(request("PUT", path, "dev", token, `{"expected_revision":1,"address":"https://new.example.invalid","scope":"dev"}`), 409, "connector_revision_changed")
	assert(request("POST", "/api/v1/connectors/editable/test", "dev", token, ""), 409, "connector_disabled")
	assert(request("POST", "/api/v1/connectors/editable/disable", "dev", token, `{"reason":"operator pause"}`), 200, "")
	assert(request("POST", "/api/v1/webhooks", "dev", token, `{"name":"Lab","type":"change","verify":"hmac"}`), 503, "webhook_registration_unavailable")
	var hookAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.webhook.registration_unavailable' AND outcome='failed' AND changes->>'http_status'='503'`).Scan(&hookAudits); err != nil || hookAudits != 1 {
		t.Fatalf("webhook failed audit %d %v", hookAudits, err)
	}
	var checked bool
	var rev int64
	if err := db.QueryRow(ctx, `SELECT last_checked_at IS NULL,revision FROM v3_connectors WHERE id='editable'`).Scan(&checked, &rev); err != nil || !checked {
		t.Fatal("probe evidence not cleared", err)
	}
	// Denied audit insertion must roll the address change back.
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_interaction_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='v3.connector.settings_updated' THEN RAISE EXCEPTION 'audit fixture'; END IF;RETURN NEW;END $$;CREATE TRIGGER reject_interaction_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_interaction_audit()`); err != nil {
		t.Fatal(err)
	}
	assert(request("PUT", path, "dev", token, `{"expected_revision":3,"address":"https://blocked.example.invalid","scope":"dev"}`), 503, "audit_unavailable")
	var address string
	if err := db.QueryRow(ctx, `SELECT endpoint,revision FROM v3_connectors WHERE id='editable'`).Scan(&address, &rev); err != nil || address != "https://new.example.invalid" || rev != 3 {
		t.Fatalf("audit rollback address=%s revision=%d err=%v", address, rev, err)
	}
	// Disabling an already-disabled registration must cancel an in-flight
	// activation, even though enabled/address are unchanged (revision witness).
	if _, err := db.Exec(ctx, `DROP TRIGGER reject_interaction_audit ON management_audit`); err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(reached); <-release; w.WriteHeader(204) }))
	defer remote.Close()
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled,tools) VALUES('activation-race','dev','prometheus','Race','observe',$1,'none',false,'[]')`, remote.URL); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request("POST", "/api/v1/connectors/activation-race/activate", "dev", token, `{}`) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("activation did not reach isolated upstream")
	}
	disabled := request("POST", "/api/v1/connectors/activation-race/disable", "dev", token, "")
	close(release)
	assert(disabled, 200, "")
	select {
	case activated := <-done:
		assert(activated, 409, "connector_changed")
	case <-time.After(5 * time.Second):
		t.Fatal("activation stuck after disable")
	}

}
