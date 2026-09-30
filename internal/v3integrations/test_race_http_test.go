package v3integrations

import (
	"context"
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

func TestConnectorTestRejectsChangesDuringProbe(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_test_race_" + store.ID()
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
	if _, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
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

	cases := []struct {
		name, mutation, code string
		wantStatus           int
	}{
		{"endpoint changed", "UPDATE v3_connectors SET endpoint=$1 || '/changed' WHERE id='probe'", "connector_changed", http.StatusConflict},
		{"disabled", "UPDATE v3_connectors SET enabled=false WHERE id='probe'", "connector_changed", http.StatusConflict},
		{"scope changed", "UPDATE v3_connectors SET environment_id='test' WHERE id='probe'", "connector_changed", http.StatusConflict},
		{"manage grant revoked", "DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'", "forbidden", http.StatusForbidden},
		{"environment grant revoked", "DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='environment' AND action='view'", "forbidden", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/-/ready" {
					t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
				}
				entered <- struct{}{}
				<-release
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			for _, statement := range []string{
				`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','manage','environment','dev') ON CONFLICT DO NOTHING`,
				`DELETE FROM v3_connectors WHERE id='probe'`,
			} {
				if _, err := db.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled)
 VALUES('probe','dev','prometheus','Probe','observe',$1,'none',true)`, upstream.URL); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/probe/test", nil)
			request.Header.Set("X-AIOps-Environment", "dev")
			request.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				response := httptest.NewRecorder()
				api.ServeHTTP(response, request)
				responses <- response
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				close(release)
				t.Fatal("probe did not reach upstream")
			}
			var args []any
			if tc.name == "endpoint changed" {
				args = append(args, upstream.URL)
			}
			if _, err := db.Exec(ctx, tc.mutation, args...); err != nil {
				close(release)
				t.Fatal(err)
			}
			close(release)
			var response *httptest.ResponseRecorder
			select {
			case response = <-responses:
			case <-time.After(10 * time.Second):
				t.Fatal("probe did not return")
			}
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), tc.code) {
				t.Fatalf("probe after %s: %d %s", tc.name, response.Code, response.Body.String())
			}
			var status string
			var checked bool
			if err := db.QueryRow(ctx, `SELECT status,last_checked_at IS NOT NULL FROM v3_connectors WHERE id='probe'`).Scan(&status, &checked); err != nil || status != "unverified" || checked {
				t.Fatalf("stale probe result persisted: status=%q checked=%v err=%v", status, checked, err)
			}
			var audits int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='connector.connection.tested' AND target='probe'`).Scan(&audits); err != nil || audits != 0 {
				t.Fatalf("stale probe audited: count=%d err=%v", audits, err)
			}
		})
	}
}
