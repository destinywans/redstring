package v3security

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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type authoritySubstitute struct {
	err   error
	calls int
}

func (a *authoritySubstitute) Snapshot(_ context.Context, scope RuntimeScope) (RuntimeSnapshot, error) {
	a.calls++
	if a.err != nil {
		return RuntimeSnapshot{}, a.err
	}
	now := time.Now()
	return RuntimeSnapshot{InstanceID: scope.InstanceID, EnvironmentID: scope.EnvironmentID, Revision: 3,
		Complete: true, Enforced: true, ObservedAt: now, ValidUntil: now.Add(time.Minute),
		View: SecurityView{SSO: []SSOProvider{}, MFAPolicy: "none", MFAMethods: []MFAMethod{},
			Session:  SessionPolicyView{Idle: "30 min", Max: "12 h"},
			Vault:    VaultView{Name: "Vault", Sub: "kv/v2", Connected: true},
			TokenTTL: "90 d", PasswordPolicy: "12 characters"}}, nil
}

func (a *authoritySubstitute) Apply(context.Context, RuntimeApply) (RuntimeApplyReceipt, error) {
	return RuntimeApplyReceipt{}, errors.New("not used")
}

func isolatedSecurityStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_security_test_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st, db
}

func TestSecurityReadReportsMissingAuthorityWithoutGuessing(t *testing.T) {
	st, db := isolatedSecurityStore(t)
	ctx := t.Context()
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-security-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','workspace','manage','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('reader','reader@example.invalid','Reader','viewer',$1)`,
		store.PasswordHash("isolated-security-password")); err != nil {
		t.Fatal(err)
	}
	_, adminSession, err := st.Login(ctx, "admin@example.invalid", "isolated-security-password")
	if err != nil {
		t.Fatal(err)
	}
	_, readerSession, err := st.Login(ctx, "reader@example.invalid", "isolated-security-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	service := New(st, "acceptance.example.invalid", nil)
	api := httpapi.New(st, nil, true)
	service.Register(api)
	routePolicy := func(ctx context.Context, user core.User, module, action, environmentID string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environmentID})
	}
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, routePolicy); err != nil {
		t.Fatal(err)
	}
	request := func(method, session, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/security", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	errorCode := func(w *httptest.ResponseRecorder) string {
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return body.Error.Code
	}

	// No authority: every field is unknown, nothing is defaulted, no revision.
	w := request(http.MethodGet, adminSession, "")
	var unknown map[string]json.RawMessage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &unknown) != nil || w.Header().Get("ETag") != "" {
		t.Fatalf("unconfigured authority: status=%d etag=%q body=%s", w.Code, w.Header().Get("ETag"), w.Body.String())
	}
	var reasons map[string]string
	if err := json.Unmarshal(unknown["unavailable"], &reasons); err != nil || len(reasons) != 9 {
		t.Fatalf("unconfigured reasons: %s", w.Body.String())
	}
	for _, field := range []string{"sso", "scim", "mfa_policy", "mfa_methods", "approval_passkey", "session", "vault", "token_ttl", "password_policy"} {
		if string(unknown[field]) != "null" || reasons[field] != "not_configured" {
			t.Fatalf("%s must be null with not_configured: %s", field, w.Body.String())
		}
	}
	// Authorization is unchanged: a non-administrator learns nothing.
	if w := request(http.MethodGet, readerSession, ""); w.Code != http.StatusForbidden {
		t.Fatalf("non-administrator read: status=%d body=%s", w.Code, w.Body.String())
	}
	// Without a revision there is nothing to save against.
	if w := request(http.MethodPut, adminSession, "{}"); w.Code != http.StatusServiceUnavailable && w.Code != http.StatusPreconditionRequired {
		t.Fatalf("PUT without authority: status=%d body=%s", w.Code, w.Body.String())
	}

	// A configured authority answers with the full view and an empty map.
	authority := &authoritySubstitute{}
	service.Authority = authority
	w = request(http.MethodGet, adminSession, "")
	var known map[string]json.RawMessage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &known) != nil || w.Header().Get("ETag") == "" ||
		string(known["unavailable"]) != "{}" || string(known["mfa_policy"]) != `"none"` || string(known["scim"]) != "false" {
		t.Fatalf("configured authority: status=%d etag=%q body=%s", w.Code, w.Header().Get("ETag"), w.Body.String())
	}
	// A configured authority that fails is a failed read, not an unknown view.
	authority.err = errors.New("authority down")
	if w := request(http.MethodGet, adminSession, ""); w.Code != http.StatusServiceUnavailable || errorCode(w) != "security_authority_unavailable" {
		t.Fatalf("failing authority: status=%d body=%s", w.Code, w.Body.String())
	}
	// An unresolved operation still blocks the read, configured or not.
	service.Authority = nil
	if _, err := db.Exec(ctx, `INSERT INTO v3_security_policy_operations
 (id,instance_id,environment_id,actor_id,expected_revision,requested_controls,status)
 SELECT 'pending-op','acceptance.example.invalid','dev',id,1,'{}'::jsonb,'unknown' FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, adminSession, ""); w.Code != http.StatusServiceUnavailable || errorCode(w) != "security_reconciliation_required" {
		t.Fatalf("unresolved operation: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSecurityPUTAcceptsOnlyAnEmptyEchoedUnavailableMap(t *testing.T) {
	view := `{"sso":[],"scim":false,"mfa_policy":"none","mfa_methods":[],"approval_passkey":false,` +
		`"session":{"idle":"30 min","max":"12 h","ip_allow":"","new_device_notice":false},` +
		`"vault":{"name":"Vault","sub":"kv/v2","connected":true},"token_ttl":"90 d","password_policy":"12 characters"`
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{view + `}`, true},
		{view + `,"unavailable":{}}`, true},
		{view + `,"unavailable":{"vault":"not_configured"}}`, false},
		{view + `,"unavailable":null}`, false},
		{view + `,"unavailable":{},"unavailable":{}}`, false},
	} {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/security", strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		got, ok := decodeSecurityView(w, r)
		if ok != tc.ok || ok && (got.MFAPolicy != "none" || got.Vault.Name != "Vault") {
			t.Fatalf("decode %s: ok=%v status=%d body=%s", tc.body, ok, w.Code, w.Body.String())
		}
	}
}
