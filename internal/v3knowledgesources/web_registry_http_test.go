package v3knowledgesources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

func TestPublicWebRegistryProbeAndObservationHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminDB.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public"); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	schema := "v3_public_web_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (&knowledge.PGRepository{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.md" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Public guide\n\nCheck status.\n"))
	}))
	defer server.Close()
	reader := PublicWebReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	v3sourceregistry.NewWithReaders(db, policy, map[string]v3sourceregistry.Reader{"web": reader}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	const address = "https://example.com/guide.md"
	probe := request("/api/v1/knowledge-sources/test", `{"type":"web","url":"`+address+`","secret_ref":""}`)
	if probe.Code != 200 || !strings.Contains(probe.Body.String(), `"ok":true`) ||
		!strings.Contains(probe.Body.String(), `"spaces":[]`) || !strings.Contains(probe.Body.String(), `"persistent":false`) {
		t.Fatalf("public Web probe was not a truthful transient success: %d %s", probe.Code, probe.Body.String())
	}
	failed := request("/api/v1/knowledge-sources/test", `{"type":"web","url":"https://example.com/missing.md","secret_ref":""}`)
	if failed.Code != 503 || !strings.Contains(failed.Body.String(), `"code":"source_connection_failed"`) {
		t.Fatalf("missing page was reported connected: %d %s", failed.Code, failed.Body.String())
	}
	for _, invalidAddress := range []string{"https://example.com/guide.md?", "https://example.com/guide.md#",
		"https://example.com:8443/guide.md", "https://example.com/" + strings.Repeat("a", v3sourceregistry.MaxPublicWebURLBytes)} {
		body, err := json.Marshal(map[string]any{"name": "Invalid public page", "type": "web", "url": invalidAddress,
			"secret_ref": "", "spaces": []string{}, "schedule": "manual", "chunk": "fixed", "inherit_acl": true})
		if err != nil {
			t.Fatal(err)
		}
		invalid := request("/api/v1/knowledge-sources/registrations", string(body))
		if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), `"code":"invalid_source_registration"`) {
			t.Fatalf("reader-incompatible public URL was persisted: %q %d %s", invalidAddress, invalid.Code, invalid.Body.String())
		}
	}
	created := request("/api/v1/knowledge-sources/registrations", `{"name":"Public guide","type":"web","url":"`+address+`",`+
		`"secret_ref":"","spaces":[],"schedule":"manual","chunk":"fixed","inherit_acl":true}`)
	if created.Code != 201 || !strings.Contains(created.Body.String(), `"registration_status":"unverified"`) {
		t.Fatalf("public registration: %d %s", created.Code, created.Body.String())
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil || saved.ID == "" {
		t.Fatalf("registration ID: %s %v", created.Body.String(), err)
	}
	var secret string
	if err := db.QueryRow(ctx, `SELECT secret_ref FROM v3_knowledge_source_registrations WHERE id=$1`, saved.ID).Scan(&secret); err != nil || secret != "" {
		t.Fatalf("public registration saved a credential: %q %v", secret, err)
	}
	read := request("/api/v1/knowledge-sources/registrations/"+saved.ID+"/sync", "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"observed_count":1`) ||
		!strings.Contains(read.Body.String(), `"index_state":"not_proven"`) {
		t.Fatalf("public read was not bounded observation: %d %s", read.Code, read.Body.String())
	}
	var documents, observations int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if documents != 0 || observations != 1 {
		t.Fatalf("observation falsely imported or disappeared: docs=%d observations=%d", documents, observations)
	}
}
