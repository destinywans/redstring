package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/actions"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
)

// Exercise the actual composition root, middleware, domain readers and audit
// rollback in a disposable schema. No provider or external writer is invoked.
func TestConfiguredV3PageInteractions(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated random schema")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "AIOPS_V3_") {
			t.Setenv(name, "")
		}
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v11_page_" + store.ID()
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
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = actions.New(db, nil, "dev").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = aiextensions.New(db, "").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	know := knowledge.New(db, nil)
	if err = know.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "page-admin@example.invalid", "isolated-page-password"); err != nil {
		t.Fatal(err)
	}
	user, token, err := st.Login(ctx, "page-admin@example.invalid", "isolated-page-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO applications(id,key,name,environment) VALUES('app','app','App','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev');
 INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at,labels)
 VALUES('alert1','fixture','app','app-dev','f1','First alert','warning','open','firing',now(),'{"alertname":"HighErrors","severity":"warning"}'),
 ('alert2','fixture','app','app-dev','f2','Second alert','warning','open','firing',now(),'{"alertname":"HighErrors","severity":"warning"}');
 INSERT INTO v3_investigation_templates(id,owner,environment_id,application_id,name,version,branches,tools,runbooks,triggers,budget)
 VALUES('tmpl','official','dev','app','Original template',1,'[{"name":"Check","step":"Read evidence","tools":[]}]','[]','[]','{"manual":true,"auto_triage":false,"matchers":[]}','{"turns":2,"tools":0,"minutes":2}');
 INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,status,body,created_at)
 VALUES('fresh','alert2','app','app-dev','channel-id','fresh','sent','not returned',now()),
 ('old','alert2','app','app-dev','channel-id','old','pending','not returned',now()-interval '2 hours');`); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { providerCalls.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	runner, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "local-model", Kind: "openai", Model: "fixture", BaseURL: provider.URL, MaxOutputTokens: 128}}, HTTPClient: provider.Client()})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, runner, true)
	var info v3Instance
	if err = json.Unmarshal([]byte(`{"tier":"nonprod","domain":"page.example.invalid","environments":[{"id":"dev"},{"id":"test"}]}`), &info); err != nil {
		t.Fatal(err)
	}
	if err = configureV3(ctx, st, runner, api, know, &info, httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, operations.Config{}); err != nil {
		t.Fatal(err)
	}
	currentT := t
	request := func(method, path, env, auth, contentType string, body io.Reader, key string, want int) *httptest.ResponseRecorder {
		currentT.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, body)
		if env != "" {
			r.Header.Set("X-AIOps-Environment", env)
		}
		if auth != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: auth})
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			currentT.Fatalf("%s %s: status%d want%d body%s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	call := func(method, path, env string, body any, want int) map[string]any {
		currentT.Helper()
		var input io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			input = bytes.NewReader(raw)
		}
		w := request(method, path, env, token, "application/json", input, "page-request-key", want)
		var out map[string]any
		if w.Body.Len() > 0 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				currentT.Fatal(err)
			}
		}
		return out
	}
	t.Run("participants_create_read_append_and_audit_rollback", func(t *testing.T) {
		currentT = t
		out := call("POST", "/incidents", "dev", map[string]any{"title": "Incident", "severity": "P2", "env": "dev", "services": []string{"app"}, "alert_ids": []string{"alert1"}, "participants": []string{"Alice", "Bob"}}, 201)
		id := out["id"].(string)
		detail := call("GET", "/incidents/"+id, "dev", nil, 200)
		if len(detail["participants"].([]any)) != 2 {
			t.Fatal(detail)
		}
		out = call("POST", "/incidents/"+id+"/participants", "dev", map[string]any{"names": []string{"Bob", "Carol"}}, 200)
		if len(out["participants"].([]any)) != 3 {
			t.Fatal(out)
		}
		call("POST", "/incidents/"+id+"/participants", "all", map[string]any{"names": []string{"hidden"}}, 409)
		call("POST", "/incidents/"+id+"/participants", "test", map[string]any{"names": []string{"hidden"}}, 404)
		if _, err = db.Exec(ctx, `CREATE FUNCTION reject_page_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled audit failure'; END $$; CREATE TRIGGER reject_page_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_page_audit();`); err != nil {
			t.Fatal(err)
		}
		call("POST", "/incidents/"+id+"/participants", "dev", map[string]any{"names": []string{"MustRollback"}}, 503)
		if _, err = db.Exec(ctx, `DROP TRIGGER reject_page_audit ON management_audit; DROP FUNCTION reject_page_audit();`); err != nil {
			t.Fatal(err)
		}
		detail = call("GET", "/incidents/"+id, "dev", nil, 200)
		if len(detail["participants"].([]any)) != 3 {
			t.Fatal("audit failure left participants", detail)
		}
		call("POST", "/incidents/"+id+"/participants", "dev", map[string]any{"names": []string{"\x00"}}, 422)
		call("POST", "/assignments", "dev", map[string]any{"item_kind": "incident", "item_id": id, "assignee": user.ID}, 200)
	})
	t.Run("template_CAS_preserves_started_snapshot", func(t *testing.T) {
		currentT = t
		call("POST", "/investigation-templates/tmpl/start", "dev", map[string]any{"inputs": map[string]any{"application_id": "app", "template_version": "v1", "request_id": "page-template-start"}}, 201)
		out := call("PUT", "/investigation-templates/tmpl", "dev", map[string]any{"expected_revision": 1, "name": "Updated template", "budget": map[string]any{"tools": 4, "minutes": 3}}, 200)
		if out["revision"] != float64(2) {
			t.Fatal(out)
		}
		call("PUT", "/investigation-templates/tmpl", "dev", map[string]any{"expected_revision": 1, "name": "Stale", "budget": map[string]any{"tools": 1, "minutes": 1}}, 409)
		var snapshot []byte
		if err = db.QueryRow(ctx, `SELECT snapshot FROM v3_investigation_template_starts WHERE request_id='page-template-start'`).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(snapshot, []byte("Original template")) || bytes.Contains(snapshot, []byte("Updated template")) {
			t.Fatal("started snapshot mutated", string(snapshot))
		}
	})
	t.Run("local_maintenance_and_durable_inbox_assignment", func(t *testing.T) {
		currentT = t
		out := call("POST", "/maintenance-windows", "dev", map[string]any{"schedule": "Thursdays 02:00–04:00 Asia/Shanghai", "matchers": []string{"application_id=app", "environment=dev"}, "reason": "Planned maintenance"}, 201)
		if out["execution_enabled"] != false || out["status"] != "planned" {
			t.Fatal(out)
		}
		out = call("GET", "/maintenance-windows", "dev", nil, 200)
		if len(out["items"].([]any)) != 1 {
			t.Fatal(out)
		}
		call("POST", "/maintenance-windows", "dev", map[string]any{"schedule": "Tomorrow", "matchers": []string{"application_id=app", "environment=test"}, "reason": "Wrong environment"}, 422)
		body := map[string]any{"item_kind": "alert", "item_id": "alert2", "assignee": user.ID}
		call("POST", "/assignments", "dev", body, 200)
		call("POST", "/assignments", "dev", body, 200)
		var count int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_assignment_notifications WHERE item_kind='alert' AND item_id='alert2'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("notification count%d error%v", count, err)
		}
		out = call("GET", "/inbox", "dev", nil, 200)
		notices := 0
		for _, raw := range out["items"].([]any) {
			if raw.(map[string]any)["kind"] == "assignment" {
				notices++
			}
		}
		if notices < 2 {
			t.Fatal("assignment notices not delivered to inbox", out)
		}
		outsider, err := st.CreateUser(ctx, core.User{Name: "Outsider", Email: "page-outsider@example.invalid", Role: "viewer"}, "isolated-outsider-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer'`); err != nil {
			t.Fatal(err)
		}
		call("POST", "/assignments", "dev", map[string]any{"item_kind": "alert", "item_id": "alert2", "assignee": outsider.ID}, 422)
		call("POST", "/assignments", "all", body, 409)
		groups := call("GET", "/alert-groups?group_by=service", "dev", nil, 200)
		if groups["actionable"] != false || len(groups["items"].([]any)) != 1 {
			t.Fatal(groups)
		}
		call("GET", "/alert-groups?group_by=cluster,namespace", "dev", nil, 503)
		deliveries := call("GET", "/notification-deliveries?range=15m&q=sent", "dev", nil, 200)
		if len(deliveries["items"].([]any)) != 1 {
			t.Fatal(deliveries)
		}
		call("GET", "/notification-deliveries?range=bad", "dev", nil, 422)
	})
	t.Run("permission_failure_remains_visible_in_selected_environment_audit", func(t *testing.T) {
		currentT = t
		viewer, err := st.CreateUser(ctx, core.User{Name: "Audit viewer", Email: "page-audit@example.invalid", Role: "viewer"}, "isolated-audit-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','environment','view','environment','dev'),('viewer','audit','view','environment','dev') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatal(err)
		}
		_, viewerToken, err := st.Login(ctx, viewer.Email, "isolated-audit-password")
		if err != nil {
			t.Fatal(err)
		}
		request("PUT", "/connectors/missing/settings", "dev", viewerToken, "application/json", strings.NewReader(`{"expected_revision":1,"address":"PRIVATE_CANARY"}`), "", 403)
		var count int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.connector.settings_updated' AND outcome='failed' AND changes->>'environment_id'='dev' AND changes->>'http_status'='403'`, viewer.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("scoped audit count%d err%v", count, err)
		}
		out := request("GET", "/audit?action=v3.connector.settings_updated&result=failed", "dev", viewerToken, "", nil, "", 200)
		if !bytes.Contains(out.Body.Bytes(), []byte(`"name":"Audit viewer"`)) || bytes.Contains(out.Body.Bytes(), []byte("PRIVATE_CANARY")) {
			t.Fatal("scoped audit hidden or leaked body", out.Body.String())
		}
	})
	t.Run("missing_trusted_sources_fail_closed_and_writes_audited", func(t *testing.T) {
		currentT = t
		call("GET", "/notification-templates", "dev", nil, 503)
		call("GET", "/oncall-schedules", "dev", nil, 503)
		call("POST", "/me/contacts/email/verification", "dev", map[string]any{"value": "private@example.invalid"}, 503)
		call("POST", "/me/contacts/phone/verification", "dev", map[string]any{"value": "+8613800000000"}, 503)
		call("DELETE", "/me/contacts/feishu", "dev", nil, 503)
		call("POST", "/webhooks", "dev", map[string]any{"name": "Ingress", "type": "alert", "verify": "hmac"}, 503)
		var count int
		var leaked bool
		if err = db.QueryRow(ctx, `SELECT count(*),bool_or(changes::text LIKE '%private@example.invalid%' OR changes::text LIKE '%+8613800000000%') FROM management_audit WHERE action LIKE 'profile.contact.%' AND outcome='failed'`).Scan(&count, &leaked); err != nil || count != 3 || leaked {
			t.Fatalf("failure audit count%d leaked%t err%v", count, leaked, err)
		}
	})
	t.Run("avatar_decode_private_read_replace_delete_and_audit_rollback", func(t *testing.T) {
		currentT = t
		imageBytes := func(width int) []byte {
			var b bytes.Buffer
			if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, width, 256))); err != nil {
				t.Fatal(err)
			}
			return b.Bytes()
		}
		upload := func(data []byte, want int) map[string]any {
			t.Helper()
			var b bytes.Buffer
			form := multipart.NewWriter(&b)
			part, err := form.CreateFormFile("file", "avatar.png")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write(data)
			_ = form.Close()
			w := request("PUT", "/me/avatar", "dev", token, form.FormDataContentType(), &b, "", want)
			var out map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			return out
		}
		upload(imageBytes(255), 422)
		upload([]byte("not an image"), 422)
		pngBytes := imageBytes(256)
		out := upload(pngBytes, 200)
		path := strings.TrimPrefix(out["url"].(string), "/api/v1")
		w := request("GET", path, "", token, "", nil, "", 200)
		if w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), pngBytes) {
			t.Fatal("avatar bytes not private original")
		}
		request("GET", path, "dev", "", "", nil, "", 401)
		if _, err = db.Exec(ctx, `CREATE FUNCTION reject_avatar_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='profile.avatar.put' AND NEW.outcome='succeeded' THEN RAISE EXCEPTION 'controlled avatar audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_avatar_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_avatar_audit();`); err != nil {
			t.Fatal(err)
		}
		upload(pngBytes, 503)
		request("GET", path, "dev", token, "", nil, "", 200)
		if _, err = db.Exec(ctx, `DROP TRIGGER reject_avatar_audit ON management_audit; DROP FUNCTION reject_avatar_audit();`); err != nil {
			t.Fatal(err)
		}
		newAvatar := upload(pngBytes, 200)
		if newAvatar["url"] == out["url"] {
			t.Fatal("avatar revision reused")
		}
		request("GET", path, "dev", token, "", nil, "", 404)
		call("DELETE", "/me/avatar", "all", nil, 409)
		call("DELETE", "/me/avatar", "dev", nil, 204)
		request("GET", strings.TrimPrefix(newAvatar["url"].(string), "/api/v1"), "dev", token, "", nil, "", 404)
	})
	if providerCalls.Load() != 0 {
		t.Fatalf("page operations invoked model%d times", providerCalls.Load())
	}
}
