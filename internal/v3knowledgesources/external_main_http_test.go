package v3knowledgesources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3sourceregistry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExternalMainCreateAndRegisteredEvidenceHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adminDB.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	schema := "v3_external_main_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = (&knowledge.PGRepository{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO applications(id,key,name,environment,archived) VALUES ('app-a','app-a','App','dev',false)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO application_environments(id,application_id,key,name,archived) VALUES ('app-a-dev','app-a','dev','Dev',false)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app-a','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO teams(id,name) VALUES ('team-a','Team')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE applications SET owner_team_id='team-a' WHERE id='app-a'`); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	confluence, _, _, _ := externalFixture(t, "confluence", confluenceHandler)
	notionID := "2499c512-bbf1-4e46-9175-6ae89f64ec7c"
	notion, _, _, _ := externalFixture(t, "notion", notionHandler(notionID, "# Findings\n\nRead only.", false, []string{}, "2026-09-29T08:00:00Z"))
	bindings := []ExternalBinding{confluence.bindings[0], notion.bindings[0]}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	v3sourceregistry.NewWithReaders(db, policy, map[string]v3sourceregistry.Reader{"confluence": confluence, "notion": notion}).Register(api)
	main := NewWithSourceBindings(db, policy, nil, bindings)
	main.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	ids := map[string]string{}
	for _, b := range bindings {
		spaces := []string{}
		if b.Type == "confluence" {
			spaces = []string{b.SpaceID}
		}
		fields := map[string]any{"name": b.Type + " notes", "type": b.Type, "url": b.SourceURL,
			"secret_ref": b.CredentialRef, "spaces": spaces, "schedule": "manual", "chunk": "heading",
			"inherit_acl": true, "application_id": b.ApplicationID, "team_id": b.TeamID}
		if b.Type == "notion" {
			delete(fields, "spaces")
		}
		payload, _ := json.Marshal(fields)
		created := request(http.MethodPost, "/api/v1/knowledge-sources", string(payload))
		if created.Code != http.StatusCreated {
			t.Fatalf("main %s create: %d %s", b.Type, created.Code, created.Body.String())
		}
		var result struct {
			ID          string `json:"id"`
			SyncStarted bool   `json:"sync_started"`
		}
		if err = json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" || result.SyncStarted {
			t.Fatalf("main create result: %+v %v", result, err)
		}
		ids[b.Type] = result.ID
		var fingerprint string
		var savedSpaces string
		if err = db.QueryRow(ctx, `SELECT binding_fingerprint,spaces::text FROM v3_knowledge_source_registrations WHERE id=$1`, result.ID).Scan(&fingerprint, &savedSpaces); err != nil || fingerprint != ExternalBindingFingerprint(b) || (b.Type == "notion" && savedSpaces != "[]") {
			t.Fatalf("saved binding mismatch: %q %v", fingerprint, err)
		}
	}
	before := request(http.MethodGet, "/api/v1/knowledge-sources", "")
	if before.Code != http.StatusOK {
		t.Fatalf("pre-sync list: %d %s", before.Code, before.Body.String())
	}
	var initial registeredEvidenceResponse
	if err = json.Unmarshal(before.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if len(initial.Items) != 2 {
		t.Fatalf("created sources absent: %s", before.Body.String())
	}
	for _, item := range initial.Items {
		if item.DocumentCount != nil || item.LastRead != nil {
			t.Fatalf("registration was presented as synced: %+v", item)
		}
	}
	for kind, id := range ids {
		synced := request(http.MethodPost, fmt.Sprintf("/api/v1/knowledge-sources/registrations/%s/sync", id), "")
		if synced.Code != http.StatusOK || !strings.Contains(synced.Body.String(), `"observed_count":1`) {
			t.Fatalf("%s manual sync: %d %s", kind, synced.Code, synced.Body.String())
		}
	}
	after := request(http.MethodGet, "/api/v1/knowledge-sources", "")
	if after.Code != http.StatusOK {
		t.Fatalf("post-sync main list: %d %s", after.Code, after.Body.String())
	}
	var listed registeredEvidenceResponse
	if err = json.Unmarshal(after.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 2 || !listed.Partial || listed.Index != nil {
		t.Fatalf("main evidence completeness incorrect: %s", after.Body.String())
	}
	for _, item := range listed.Items {
		if item.LastRead == nil || item.LastRead.ObservedCount != 1 || item.DocumentCount == nil || *item.DocumentCount != 1 ||
			item.LastSyncAt == nil || item.ConnectionStatus != nil || item.IndexStatus != nil {
			t.Fatalf("persisted content evidence missing or falsely healthy: %+v", item)
		}
	}
	// Breaking the observation/body relation must remove persisted content proof.
	if _, err = db.Exec(ctx, `UPDATE v3_external_source_documents SET content_hash=repeat('0',64) WHERE read_id=(
	 SELECT id FROM v3_knowledge_source_reads WHERE registration_id=$1 ORDER BY observed_at DESC LIMIT 1)`, ids["notion"]); err != nil {
		t.Fatal(err)
	}
	tampered := request(http.MethodGet, "/api/v1/knowledge-sources", "")
	var tamperedResult registeredEvidenceResponse
	if tampered.Code != http.StatusOK || json.Unmarshal(tampered.Body.Bytes(), &tamperedResult) != nil {
		t.Fatalf("tampered list: %d %s", tampered.Code, tampered.Body.String())
	}
	for _, item := range tamperedResult.Items {
		if item.Kind == "notion" && item.DocumentCount != nil {
			t.Fatalf("tampered body still has content proof: %+v", item)
		}
	}
	main.externalBindings = nil
	withoutBinding := request(http.MethodGet, "/api/v1/knowledge-sources", "")
	var hidden registeredEvidenceResponse
	if withoutBinding.Code != http.StatusOK || json.Unmarshal(withoutBinding.Body.Bytes(), &hidden) != nil || len(hidden.Items) != 0 {
		t.Fatalf("private source visible without current binding: %d %s", withoutBinding.Code, withoutBinding.Body.String())
	}
}
