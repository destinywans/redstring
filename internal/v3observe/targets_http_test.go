package v3observe

import (
	"context"
	"encoding/json"
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
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type databaseObservationAccess struct {
	db       *pgxpool.Pool
	allowAll bool
	waitFor  string
}

func (a *databaseObservationAccess) Authorize(ctx context.Context, _ core.User, decision rbac.V3Decision) error {
	// A policy read must be able to borrow the sole pool connection. Keeping
	// the target SELECT cursor open during this call would time out.
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1)`, decision.ApplicationID).Scan(&exists); err != nil {
		return err
	}
	if decision.ApplicationID == a.waitFor {
		<-ctx.Done()
		return ctx.Err()
	}
	if !exists || (!a.allowAll && !strings.HasPrefix(decision.ApplicationID, "z")) {
		return store.ErrForbidden
	}
	return nil
}

// emptyLoki answers every Loki read with no lines and counts the line
// queries (one per target and level).
func emptyLoki(t *testing.T, calls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query := r.FormValue("query"); !strings.Contains(query, `app="z-visible"`) {
			t.Errorf("Loki query escaped the registered scope: %s", query)
		}
		switch r.URL.Path {
		case "/loki/api/v1/query_range":
			calls.Add(1)
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
		case "/loki/api/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			t.Errorf("unexpected Loki route: %s", r.URL)
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
}

func TestObservationTargetsAuthorizeAfterClosingSinglePoolCursor(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_observation_target_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-observation-password"); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-observation-password")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	loki := emptyLoki(t, &calls)
	defer loki.Close()
	// More than two database pages are hidden from the request principal.
	// The visible service sorts last and must still be reached.
	_, err = db.Exec(ctx, `INSERT INTO applications(id,name,key,environment)
 SELECT 'h'||lpad(n::text,4,'0'),'Hidden '||n,'h'||lpad(n::text,4,'0'),'dev' FROM generate_series(1,230) n;
 INSERT INTO applications(id,name,key,environment) VALUES('z-visible','Visible','z-visible','dev');
 INSERT INTO application_environments(id,application_id,key,name) SELECT 'env-'||id,id,'dev','Dev' FROM applications;
 INSERT INTO v3_application_environments(application_id,environment_id)
 SELECT id,'dev' FROM applications;`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools)
 VALUES('loki-1','dev','loki','Loki','observe',$1,'none','',true,'[{"name":"search_logs","desc":"","enabled":true}]')`, loki.URL); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 SELECT 't-'||id,id,'dev','loki-1','log_stream',CASE WHEN id='z-visible' THEN 'visible-provider' ELSE 'hidden-'||id END,
  (SELECT id FROM users WHERE email='admin@example.invalid')
 FROM applications`); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"scopes":[{"connector_id":"loki-1","application_id":"z-visible","environment_id":"dev",
 "resource_id":"visible-provider","provider":"loki","loki_labels":{"app":"z-visible"},"loki_pattern_field":"message_template",
 "loki_complete_lookback_seconds":7200}]}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	access := &databaseObservationAccess{db: db}
	api := httpapi.New(st, nil, true)
	observeService := New(st, access)
	observeService.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)}
	observeService.Clients = &v3telemetry.Clients{Tier: "nonprod"}
	observeService.Register(api)
	api.MarkV3ObjectRoute("GET /api/v1/logs", "logs", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	requestWith := func(query string, budget time.Duration) *httptest.ResponseRecorder {
		deadline, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/logs"+query, nil).WithContext(deadline)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	request := func() *httptest.ResponseRecorder { return requestWith("?levels=INFO", 10*time.Second) }
	w := request()
	if w.Code != 200 || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"total":"0"`) ||
		!strings.Contains(w.Body.String(), `"patterns":[]`) || strings.Contains(w.Body.String(), "hidden-") {
		t.Fatalf("hidden targets blocked or escaped: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	access.allowAll = true
	w = request()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 422 || body.Error.Code != "scope_limit" || calls.Load() != 1 {
		t.Fatalf("authorized bound failed: status=%d calls=%d body=%s error=%v", w.Code, calls.Load(), w.Body.String(), err)
	}
	// A timeout after one permitted target must discard it rather than return
	// a partial list. The caller's shorter deadline also bounds the scan.
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id LIKE 'h%';
 INSERT INTO applications(id,name,key,environment) VALUES('zz-visible2','Other','zz-visible2','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('env-zz-visible2','zz-visible2','dev','Dev');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('zz-visible2','dev');
 INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-zz-visible2','zz-visible2','dev','loki-1','log_stream','hidden-zz-visible2',(SELECT id FROM users WHERE email='admin@example.invalid'))`); err != nil {
		t.Fatal(err)
	}
	access.allowAll = false
	access.waitFor = "zz-visible2"
	w = requestWith("?levels=INFO", 75*time.Millisecond)
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 503 || body.Error.Code != "log_scope_unavailable" || calls.Load() != 1 || strings.Contains(w.Body.String(), "visible-provider") {
		t.Fatalf("timed-out target scan returned partial data: status=%d calls=%d body=%s error=%v", w.Code, calls.Load(), w.Body.String(), err)
	}
	access.waitFor = ""
	// Use the actual persisted V3 policy for the team fallback. The request
	// supplies no team identity; only applications.owner_team_id is trusted.
	if _, err := db.Exec(ctx, `INSERT INTO teams(id,name) VALUES('team-one','Team One'),('team-other','Team Other');
 UPDATE applications SET owner_team_id='team-one' WHERE id='z-visible';
 UPDATE applications SET owner_team_id='team-other' WHERE id='zz-visible2';
 DELETE FROM v3_role_grants WHERE role_id='admin' AND module='logs' AND action='view';
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','logs','view','team','team-one')`); err != nil {
		t.Fatal(err)
	}
	observeService.Access = policy
	w = requestWith("?service=z-visible&levels=INFO", 10*time.Second)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":"0"`) || calls.Load() != 2 {
		t.Fatalf("persisted team grant did not expose its service: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	w = requestWith("?service=zz-visible2&levels=INFO", 10*time.Second)
	if w.Code != 403 || calls.Load() != 2 {
		t.Fatalf("other team's service escaped scope: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='logs' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	w = requestWith("?service=z-visible&levels=INFO", 10*time.Second)
	if w.Code != 403 || calls.Load() != 2 {
		t.Fatalf("revoked team grant still queried logs: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
}
