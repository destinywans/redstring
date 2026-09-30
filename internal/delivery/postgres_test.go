package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func deliveryDatabase(t *testing.T) (*PostgresRepository, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and removes random isolated schema")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || (!strings.Contains(u.Path, "_review_") && !strings.Contains(u.Path, "test")) {
		t.Fatal("delivery DB checks only permit a loopback review/test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("cannot open isolated test database")
	}
	schema := "test_delivery_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		if _, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error("isolated schema cleanup failed")
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated database config")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: pool}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('operator','operator@delivery.test','Operator','operator','unused'),('viewer','viewer@delivery.test','Viewer','viewer','unused'); INSERT INTO applications(id,key,name) VALUES('app','app','Application'),('hidden','hidden','Hidden'); INSERT INTO application_environments(id,application_id,key,name) VALUES('env','app','test','Test'),('hidden-env','hidden','test','Test'); INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES('app','user','operator','operate'),('app','user','viewer','view');`)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy application_grants are not infrastructure authority. Both
	// applications have registered resources; only app's host is granted.
	_, err = pool.Exec(ctx, `INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES
 ('delivery-host','host','Delivery host','manual'),('hidden-host','host','Hidden host','manual');
 INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES
 ('delivery-host','app','env'),('hidden-host','hidden','hidden-env');
 INSERT INTO rbac_roles(id,name) VALUES('delivery-fixture-view','Delivery fixture view'),('delivery-fixture-write','Delivery fixture write');
 INSERT INTO rbac_permissions(id,name) VALUES('delivery-fixture-view','Delivery host view'),('delivery-fixture-update','Delivery host update');
 INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES
 ('delivery-fixture-view','resource','delivery-host','view'),('delivery-fixture-update','resource','delivery-host','update');
 INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES
 ('delivery-fixture-view','delivery-fixture-view'),('delivery-fixture-write','delivery-fixture-view'),('delivery-fixture-write','delivery-fixture-update');
 INSERT INTO rbac_user_roles(user_id,role_id) VALUES('viewer','delivery-fixture-view'),('operator','delivery-fixture-write');`)
	if err != nil {
		t.Fatal(err)
	}
	repo := &PostgresRepository{DB: pool}
	if err = repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return repo, st
}
func savedPipeline(t *testing.T, p *PostgresRepository) Pipeline {
	t.Helper()
	v, err := p.SavePipeline(context.Background(), Pipeline{ApplicationID: "app", EnvironmentID: "env", Name: "Test", CreatedBy: "operator", Definition: Definition{Provider: "native", Nodes: []Node{buildNode("build")}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestPostgresDeliveryVersionsIdempotencyAndFencing(t *testing.T) {
	p, _ := deliveryDatabase(t)
	ctx := context.Background()
	v1 := savedPipeline(t, p)
	v2 := v1
	v2.Name = "Changed"
	v2.Definition = Definition{Provider: "native", Nodes: []Node{buildNode("other")}}
	v2, err := p.SavePipeline(ctx, v2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.SavePipeline(ctx, v1, 1); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale version accepted")
	}
	r, err := p.CreateRun(ctx, v1, "operator", "run-one")
	if err != nil {
		t.Fatal(err)
	}
	same, err := p.CreateRun(ctx, v1, "operator", "run-one")
	if err != nil || same.ID != r.ID {
		t.Fatal("duplicate submit created another run")
	}
	if _, err = p.CreateRun(ctx, v2, "operator", "run-one"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("idempotency key accepted different version")
	}
	if r.Snapshot.Definition.Nodes[0].ID != "build" {
		t.Fatal("running snapshot changed")
	}
	c1, err := p.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.DB.Exec(ctx, "UPDATE delivery_commands SET lease_until=now()-interval '1 second' WHERE id=$1", c1.ID); err != nil {
		t.Fatal(err)
	}
	c2, err := p.Claim(ctx)
	if err != nil || !c2.Recovery {
		t.Fatal("expired claim not recovered")
	}
	projection := Projection{Status: "running", Nodes: []NodeResult{}}
	if err = p.Finish(ctx, c1, &projection, ""); !errors.Is(err, store.ErrLease) {
		t.Fatal("old claim committed")
	}
	if err = p.Finish(ctx, c2, &projection, ""); err != nil {
		t.Fatal(err)
	}
	cancel, err := p.Enqueue(ctx, r, "cancel", 0, "operator", "cancel-one")
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Project(ctx, r.ID, projection); err != nil {
		t.Fatal(err)
	}
	updated, err := p.GetRun(ctx, r.ID)
	if err != nil || updated.Status != "cancel_requested" {
		t.Fatal("observation lost cancel request")
	}
	claim, err := p.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := Projection{Status: "cancelled", Nodes: []NodeResult{}}
	if err = p.Finish(ctx, claim, &done, ""); err != nil {
		t.Fatal(err)
	}
	replay, err := p.Enqueue(ctx, r, "cancel", 0, "operator", "cancel-one")
	if err != nil || replay.ID != cancel.ID {
		t.Fatal("completed command replay was not idempotent")
	}
	events, err := p.Events(ctx, r.ID, 0)
	if err != nil || len(events) < 3 {
		t.Fatal("events missing")
	}
	after := events[1].Sequence
	tail, err := p.Events(ctx, r.ID, after)
	if err != nil || len(tail) != len(events)-2 {
		t.Fatal("event cursor replay wrong")
	}
}
func TestPostgresDeliveryWebhookDedupAndApplicationACL(t *testing.T) {
	p, st := deliveryDatabase(t)
	v := savedPipeline(t, p)
	ctx := context.Background()
	run, err := p.CreateRun(ctx, v, "operator", "acl-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"operator", "viewer"} {
		_, err = p.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", store.Hash(actor+"-cookie"), actor)
		if err != nil {
			t.Fatal(err)
		}
	}
	config := GitLabConfig{ID: "lab", BaseURL: "http://127.0.0.1:28080", AllowHTTP: true, Bindings: []GitLabBinding{{ApplicationID: "app", EnvironmentID: "env", ProjectID: 1}}}
	client, err := NewGitLabClient(config, "not-used", strings.Repeat("w", 24))
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Repository: p, GitLabs: map[string]*GitLabClient{"lab": client}}
	api := httpapi.New(st, nil)
	s.Register(api)
	request := func(method, path, actor, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: actor + "-cookie"})
		r.Header.Set("Idempotency-Key", "acl-command")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if got := request("GET", "/api/v1/delivery/runs/"+run.ID, "viewer", ""); got.Code != 200 {
		t.Fatalf("viewer read status %d", got.Code)
	}
	if got := request("POST", "/api/v1/delivery/runs/"+run.ID+"/commands", "viewer", `{"kind":"cancel"}`); got.Code != 403 {
		t.Fatalf("viewer write status %d", got.Code)
	}
	if got := request("GET", "/api/v1/delivery/runs?applications=hidden", "operator", ""); got.Code != 403 {
		t.Fatalf("hidden scope status %d", got.Code)
	}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/api/v1/webhooks/gitlab/lab", strings.NewReader(`{"object_kind":"pipeline","project":{"id":1},"object_attributes":{"id":123,"status":"success"}}`))
		r.Header.Set("X-Gitlab-Token", strings.Repeat("w", 24))
		r.Header.Set("X-Gitlab-Event-UUID", "duplicate-event")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatal(w.Body.String())
		}
		var body map[string]bool
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["duplicate"] != (i == 1) {
			t.Fatal("webhook replay not deduplicated")
		}
	}
	var count int
	if err = p.DB.QueryRow(ctx, "SELECT count(*) FROM delivery_webhook_inbox").Scan(&count); err != nil || count != 1 {
		t.Fatal("webhook persisted duplicates")
	}
	unchanged, err := p.GetRun(ctx, run.ID)
	if err != nil || unchanged.Status != "queued" {
		t.Fatal("webhook untrusted status became authoritative")
	}
	if _, err := p.DB.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id='viewer' AND role_id='delivery-fixture-view'`); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", "/api/v1/delivery/runs/"+run.ID, "viewer", ""); got.Code != 403 {
		t.Fatalf("revoked resource grant still reads run: status=%d", got.Code)
	}
}

func TestPostgresDeliveryRetentionProtectsReferencesAndRequiresAdmin(t *testing.T) {
	p, st := deliveryDatabase(t)
	ctx := context.Background()
	pipe := savedPipeline(t, p)
	run, err := p.CreateRun(ctx, pipe, "operator", "retained")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := p.Retention(ctx, run)
	if err != nil || policy.Reclaimable || policy.CleanupMode != "manual_only" {
		t.Fatal("new run retention invalid", err)
	}
	_, err = p.DB.Exec(ctx, `UPDATE delivery_runs SET status='succeeded',created_at=now()-interval '31 days' WHERE id=$1;`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = p.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy, err = p.Retention(ctx, run)
	if err != nil || !policy.Reclaimable {
		t.Fatal("expired unreferenced run not eligible", err)
	}
	_, err = p.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('admin','admin@delivery.test','Admin','admin','unused');
 INSERT INTO rbac_user_roles(user_id,role_id) VALUES('admin','delivery-fixture-write');`)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"admin", "operator"} {
		_, err = p.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", store.Hash(actor+"-retention-cookie"), actor)
		if err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Repository: p}
	api := httpapi.New(st, nil)
	s.Register(api)
	request := func(path, actor, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: actor + "-retention-cookie"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := request("/api/v1/delivery/runs/"+run.ID+"/reclaim", "operator", ""); w.Code != 403 {
		t.Fatal("operator may reclaim", w.Code)
	}
	if w := request("/api/v1/delivery/runs/"+run.ID+"/reclaim", "admin", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = p.ProtectSource(ctx, run.ID, "build", "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/delivery/runs/"+run.ID+"/reclaim", "admin", ""); w.Code != 409 {
		t.Fatal("referenced artifact reclaimed", w.Code, w.Body.String())
	}
	if w := request("/api/v1/delivery/runs/"+run.ID+"/retention", "admin", `{"days":45,"held":true,"reason":"preserve release evidence"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	policy, err = p.Retention(ctx, run)
	if err != nil || !policy.Held || policy.References != 1 || policy.Reclaimable || policy.ReclaimRequestedAt != nil {
		t.Fatalf("reference/hold protection invalid: %+v %v", policy, err)
	}
}

func TestPostgresDeliveryWorkerReconcilesAcceptedGitLabTrigger(t *testing.T) {
	p, _ := deliveryDatabase(t)
	ctx := context.Background()
	posts := 0
	operation := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pipeline"):
			posts++
			var body struct {
				Variables []struct {
					Value string `json:"value"`
				} `json:"variables"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			operation = body.Variables[0].Value
			w.WriteHeader(503)
		case strings.HasSuffix(r.URL.Path, "/pipelines"):
			json.NewEncoder(w).Encode([]map[string]any{{"id": 123, "source": "api", "status": "running", "ref": "main", "sha": strings.Repeat("a", 40)}})
		case strings.HasSuffix(r.URL.Path, "/variables"):
			json.NewEncoder(w).Encode([]map[string]string{{"key": "AIOPS_RUN_ID", "value": operation}})
		case strings.HasSuffix(r.URL.Path, "/pipelines/123"):
			json.NewEncoder(w).Encode(map[string]any{"id": 123, "source": "api", "status": "running", "ref": "main", "sha": strings.Repeat("a", 40)})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := NewGitLabClient(GitLabConfig{ID: "gitlab", BaseURL: server.URL, AllowHTTP: true, Bindings: []GitLabBinding{{ApplicationID: "app", EnvironmentID: "env", ProjectID: 1, AllowedRefs: []string{"main"}, AllowActions: true, ExternalGovernance: true}}}, "token", strings.Repeat("w", 24))
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := p.SavePipeline(ctx, Pipeline{ApplicationID: "app", EnvironmentID: "env", Name: "GitLab", CreatedBy: "operator", Definition: Definition{Provider: "gitlab", SourceID: "gitlab", ProjectID: 1, Ref: "main"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.CreateRun(ctx, pipe, "operator", "lost-response")
	if err != nil {
		t.Fatal(err)
	}
	command, err := p.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Repository: p, GitLabs: map[string]*GitLabClient{"gitlab": client}, Authorize: func(context.Context, string, string, string) error { return nil }, ValidateEnvironment: func(context.Context, string, string) error { return nil }}
	s.dispatch(ctx, command)
	run, err = p.GetRun(ctx, run.ID)
	if err != nil || run.Status != "running" || run.ExternalID != "123" || posts != 1 {
		t.Fatalf("worker failed one-write reconciliation: status=%s external=%s posts=%d error=%v", run.Status, run.ExternalID, posts, err)
	}
}
