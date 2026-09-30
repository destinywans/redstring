package v3events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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

func TestValidClaimRevision(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"1", true}, {"4294967295", true}, {"", false}, {"0", false},
		{"01", false}, {"4294967296", false}, {`"123"`, false}, {"W/123", false},
		{"1,2", false}, {" 1", false}, {"-1", false},
	} {
		if got := validClaimRevision(tc.raw); got != tc.ok {
			t.Errorf("revision %q valid=%t want %t", tc.raw, got, tc.ok)
		}
	}
}

func TestClaimConcurrentUsersAndRevocation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_claim_secure_" + store.ID()
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
	cfg.MaxConns = 6
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
		('alice','alice@example.invalid','Alice','admin',$1),
		('bob','bob@example.invalid','Bob','admin',$1)`, store.PasswordHash("isolated-claim-password")); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
		 VALUES('incident','test','app','app-dev','incident','Real alert','warning','firing',now())`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, aliceToken, err := st.Login(ctx, "alice@example.invalid", "isolated-claim-password")
	if err != nil {
		t.Fatal(err)
	}
	_, bobToken, err := st.Login(ctx, "bob@example.invalid", "isolated-claim-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	service := New(db,
		func(ctx context.Context, user core.User, app, env, action string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
		},
		func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
			return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
		},
	)
	api := httpapi.New(st, nil, true)
	service.RegisterNew(api)
	admitted := make(chan struct{})
	var signalOnce sync.Once
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, func(ctx context.Context, token string) (core.User, error) {
		user, err := st.Session(ctx, token)
		if err == nil {
			signalOnce.Do(func() { close(admitted) })
		}
		return user, err
	}, nil); err != nil {
		t.Fatal(err)
	}
	currentRevision := func() string {
		t.Helper()
		var revision string
		if err := db.QueryRow(ctx, `SELECT xmin::text FROM ops_incidents WHERE id='incident'`).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	request := func(token, revision string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/incident/claim", strings.NewReader(`{"expected_revision":"`+revision+`"}`))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	revision := currentRevision()
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- request(aliceToken, revision) }()
	go func() { results <- request(bobToken, revision) }()
	first, second := <-results, <-results
	if !((first.Code == http.StatusOK && second.Code == http.StatusPreconditionFailed) ||
		(first.Code == http.StatusPreconditionFailed && second.Code == http.StatusOK)) {
		t.Fatalf("concurrent claim statuses=%d/%d bodies=%s / %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var audits int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("concurrent claim audits=%d error=%v", audits, err)
	}

	// The request has passed its initial session read when the competing revoke
	// commits, but cannot authorize the claim until the incident lock releases.
	if _, err = db.Exec(ctx, `UPDATE ops_incidents SET assignee_id='',updated_at=now() WHERE id='incident'`); err != nil {
		t.Fatal(err)
	}
	revision = currentRevision()
	lockTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(context.Background())
	if _, err = lockTx.Exec(ctx, `SELECT id FROM ops_incidents WHERE id='incident' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	// Reset the signal used by the initial race and observe this request's
	// middleware session lookup before revoking its events grant.
	admitted = make(chan struct{})
	signalOnce = sync.Once{}
	blocked := make(chan *httptest.ResponseRecorder, 1)
	go func() { blocked <- request(aliceToken, revision) }()
	select {
	case <-admitted:
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not authenticate before revoke")
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='events' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	if err = lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-blocked:
		if response.Code != http.StatusForbidden {
			t.Fatalf("revoked queued claim status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked claim did not finish")
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("revoked claim audits=%d error=%v", audits, err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
		VALUES('admin','events','change','global','*')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id='alice'`); err != nil {
		t.Fatal(err)
	}
	response := request(aliceToken, currentRevision())
	if response.Code != http.StatusForbidden {
		t.Fatalf("machine claim status=%d body=%s", response.Code, response.Body.String())
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("machine claim audits=%d error=%v", audits, err)
	}

	if _, err = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash)
		VALUES('charlie','charlie@example.invalid','Charlie','viewer',$1)`, store.PasswordHash("isolated-team-password")); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO teams(id,name) VALUES('team-a','Team A'),('team-b','Team B')`,
		`UPDATE applications SET owner_team_id='team-a' WHERE id='app'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
		 ('viewer','environment','view','environment','dev'),
		 ('viewer','events','view','team','team-a'),
		 ('viewer','events','change','team','team-a')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, charlieToken, err := st.Login(ctx, "charlie@example.invalid", "isolated-team-password")
	if err != nil {
		t.Fatal(err)
	}
	service.AuthorizeTeamTx = func(ctx context.Context, tx pgx.Tx, user core.User, team, env, action string) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, TeamID: team, EnvironmentID: env})
	}
	read := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/incident", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: charlieToken})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if response = read(); response.Code != http.StatusOK {
		t.Fatalf("current owner team read status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(charlieToken, currentRevision()); response.Code != http.StatusOK {
		t.Fatalf("current owner team claim status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE applications SET owner_team_id='team-b' WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	if response = read(); response.Code != http.StatusForbidden {
		t.Fatalf("former owner team read status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(charlieToken, currentRevision()); response.Code != http.StatusForbidden {
		t.Fatalf("former owner team claim status=%d body=%s", response.Code, response.Body.String())
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("owner change audits=%d error=%v", audits, err)
	}
	for _, statement := range []string{
		`UPDATE applications SET owner_team_id='team-a' WHERE id='app'`,
		`UPDATE ops_incidents SET assignee_id='',updated_at=now() WHERE id='incident'`,
		`DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='events' AND action='view' AND scope_kind='team' AND scope_id='team-a'`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if response = read(); response.Code != http.StatusForbidden {
		t.Fatalf("change-only actor read status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(charlieToken, currentRevision()); response.Code != http.StatusForbidden {
		t.Fatalf("change-only actor claim status=%d body=%s", response.Code, response.Body.String())
	}
	var assignee string
	if err = db.QueryRow(ctx, `SELECT assignee_id,(SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident')
		FROM ops_incidents WHERE id='incident'`).Scan(&assignee, &audits); err != nil || assignee != "" || audits != 2 {
		t.Fatalf("change-only actor changed incident: assignee=%q audits=%d error=%v", assignee, audits, err)
	}
}
