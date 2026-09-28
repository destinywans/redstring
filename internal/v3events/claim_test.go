package v3events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestClaimDecisionUnderIncidentLock(t *testing.T) {
	for _, tc := range []struct {
		name, status, alertStatus, assignee string
		allowed, change                     bool
	}{
		{"new firing alert", "open", "firing", "", true, true},
		{"new pending alert", "investigating", "pending", "", true, true},
		{"same user's retry", "open", "firing", "alice", true, false},
		{"same user's retry after resolution", "resolved", "resolved", "alice", true, false},
		{"another user's claim", "open", "firing", "bob", false, false},
		{"resolved alert", "open", "resolved", "", false, false},
		{"closed incident", "resolved", "firing", "", false, false},
		{"unknown state", "unexpected", "firing", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, change := claimAllowed(incidentRecord{Status: tc.status, AlertStatus: tc.alertStatus, AssigneeID: tc.assignee}, "alice")
			if allowed != tc.allowed || change != tc.change {
				t.Fatalf("allowed=%t change=%t; want allowed=%t change=%t", allowed, change, tc.allowed, tc.change)
			}
		})
	}
}

func TestClaimAlertAuthorizesOnLockedTransaction(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_claim_test_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 1 // A pool-scoped authorization call cannot acquire a second connection.
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-claim-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
		INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev');
		INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
		VALUES('incident','test','app','app-dev','incident','Real alert','warning','firing',now());`); err != nil {
		t.Fatal(err)
	}
	user, token, err := st.Login(ctx, "admin@example.invalid", "isolated-claim-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	decision := func(app, env, action string) rbac.V3Decision {
		return rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env}
	}
	var poolAuthorizations, txAuthorizations int
	service := New(db,
		func(ctx context.Context, u core.User, app, env, action string) error {
			poolAuthorizations++
			return policy.Authorize(ctx, u, decision(app, env, action))
		},
		func(ctx context.Context, tx pgx.Tx, u core.User, app, env, action string) error {
			txAuthorizations++
			return policy.AuthorizeWithTx(ctx, tx, u, decision(app, env, action))
		},
	)
	api := httpapi.New(st, nil, true)
	service.RegisterNew(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		t.Helper()
		requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/incident/claim", nil).WithContext(requestCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	response := request()
	if poolAuthorizations != 0 || txAuthorizations != 1 {
		t.Fatalf("claim authorizations: pool=%d tx=%d; body=%s", poolAuthorizations, txAuthorizations, response.Body.String())
	}
	if response.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", response.Code, response.Body.String())
	}
	var claimed struct {
		AssigneeID string `json:"assignee_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &claimed); err != nil || claimed.AssigneeID != user.ID {
		t.Fatalf("claim response=%s error=%v", response.Body.String(), err)
	}
	var assignee string
	var audits int
	if err := db.QueryRow(ctx, `SELECT assignee_id,(SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident') FROM ops_incidents WHERE id='incident'`).Scan(&assignee, &audits); err != nil || assignee != user.ID || audits != 1 {
		t.Fatalf("durable claim assignee=%q audits=%d error=%v", assignee, audits, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='events' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	response = request()
	if response.Code != http.StatusForbidden {
		t.Fatalf("claim after grant revocation status=%d body=%s", response.Code, response.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("revoked retry audits=%d error=%v", audits, err)
	}
}
