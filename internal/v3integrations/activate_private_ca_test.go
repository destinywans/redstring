package v3integrations

import (
	"bytes"
	"context"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// vaultEntry is one Vault secret with a token and, optionally, a CA.
type vaultEntry map[string]string

func (v vaultEntry) Resolve(_ context.Context, ref string) (string, error) {
	field, ok := strings.CutPrefix(ref, "vault://isolated/gitlab#")
	if !ok || v[field] == "" {
		return "", ErrCredentialUnavailable
	}
	return v[field], nil
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// D11: a self-hosted GitLab behind a private CA activates only when the CA is
// kept next to the token; without it the TLS check fails, and the service log
// says why without the credential.
func TestGitLabActivationTrustsPrivateCANextToToken(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_activation_ca_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	gitlab := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/12345" || r.Header.Get("PRIVATE-TOKEN") != "isolated-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"id":12345}`))
	}))
	defer gitlab.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gitlab.Certificate().Raw}))
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','environment','dev'),
 ('viewer','connectors','manage','environment','dev')`,
		`INSERT INTO applications(id,name,key,environment,archived) VALUES('ca-app','CA App','ca-app','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES('ca-dev','ca-app','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('ca-app','dev')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,tools)
 VALUES('lab','dev','gitlab','Lab GitLab','delivery',$1,'pat','[]')`, gitlab.URL); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('project','ca-app','dev','lab','project','12345',(SELECT id FROM users WHERE email='admin@example.invalid'))`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateUser(ctx, core.User{Email: "ca-viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "ca-viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	binding := CredentialBinding{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "lab", Provider: "gitlab",
		Endpoint: gitlab.URL, CredentialRef: "vault://isolated/gitlab#token"}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc := New(db, "nonprod", NewBoundResolver(vaultEntry{"token": "isolated-token"}, []CredentialBinding{binding}))
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	activate := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/lab/activate", strings.NewReader(`{"credential_ref":"vault://isolated/gitlab#token"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}

	// No "ca" next to the token: the private certificate is not trusted.
	if w := activate(); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "project_unavailable") {
		t.Fatalf("without CA: %d %s", w.Code, w.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, "class=tls") || !strings.Contains(logged, "x509") || !strings.Contains(logged, "project_id=12345") ||
		strings.Contains(logged, "isolated-token") || strings.Contains(logged, "vault://") {
		t.Fatalf("activation failure log: %s", logged)
	}
	// A "ca" that is not a certificate is reported as such.
	svc.Prober.Resolver = NewBoundResolver(vaultEntry{"token": "isolated-token", "ca": "not a certificate"}, []CredentialBinding{binding})
	if w := activate(); w.Code != http.StatusBadGateway || !strings.Contains(logs.String(), "class=ca_invalid") {
		t.Fatalf("invalid CA: %d %s %s", w.Code, w.Body.String(), logs.String())
	}
	// The CA next to the token: activation verifies the project.
	svc.Prober.Resolver = NewBoundResolver(vaultEntry{"token": "isolated-token", "ca": ca}, []CredentialBinding{binding})
	if w := activate(); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"verified_projects":1`) {
		t.Fatalf("with CA: %d %s", w.Code, w.Body.String())
	}
	var enabled bool
	if err := db.QueryRow(ctx, `SELECT enabled FROM v3_connectors WHERE id='lab'`).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	// An upstream refusal is logged with its status.
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=false WHERE id='lab'`); err != nil {
		t.Fatal(err)
	}
	svc.Prober.Resolver = NewBoundResolver(vaultEntry{"token": "other-token", "ca": ca}, []CredentialBinding{binding})
	if w := activate(); w.Code != http.StatusBadGateway || !strings.Contains(logs.String(), "class=upstream_status upstream_status=403") ||
		strings.Contains(logs.String(), "other-token") {
		t.Fatalf("refused token: %d %s", w.Code, logs.String())
	}
}
