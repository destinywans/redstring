package v3events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestAlertDetailSeverityRefusesUnmappedRawValue(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		valid     bool
	}{
		{"critical", "P1", true},
		{"warning", "P2", true},
		{"info", "P3", true},
		{"p4", "P4", true},
		{"not-a-severity", "", false},
	} {
		got, valid := alertDetailSeverity(tc.raw)
		if got != tc.want || valid != tc.valid {
			t.Errorf("severity %q = %q,%t; want %q,%t", tc.raw, got, valid, tc.want, tc.valid)
		}
	}
}

func TestAlertDetailReadsOnlyEvidenceBackedFields(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_alert_detail_test_" + store.ID()
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
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var notificationIndex bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM pg_indexes WHERE schemaname=current_schema()
		AND indexname='ops_notifications_incident_recent'
	)`).Scan(&notificationIndex); err != nil || !notificationIndex {
		t.Fatalf("incident notification index absent: exists=%t error=%v", notificationIndex, err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-detail-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev'),('other','Other','other','test');
		INSERT INTO application_environments(id,application_id,key,name)
		 VALUES('app-dev','app','dev','Dev'),('other-test','other','test','Test');
		INSERT INTO v3_application_environments(application_id,environment_id)
		 VALUES('app','dev'),('other','test');
		INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,summary,starts_at)
		 VALUES('alert','source-alpha','app','app-dev','fingerprint','Real alert','critical','firing','Observed summary',now()-interval '5 minutes'),
		       ('hidden','source-beta','other','other-test','hidden','Other alert','warning','firing','',now());
		INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,status,attempts,created_at,body)
		 VALUES('first','alert','app','app-dev','feishu','first','sent',1,now()-interval '1 minute','private body'),
		       ('second','alert','app','app-dev','feishu','second','retrying',2,now(),'private body');`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-detail-password")
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
	api.Register("GET /api/v1/alerts/{id}", "viewer", service.AlertDetail)
	api.MarkV3ObjectRoute("GET /api/v1/alerts/{id}", "events", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(id, env string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/"+id, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	first := request("alert", "dev")
	if first.Code != http.StatusOK || first.Header().Get("ETag") != "" {
		t.Fatalf("GET status=%d ETag=%q body=%s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	var detail AlertDetailResponse
	if err := json.Unmarshal(first.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "alert" || detail.Revision == "" || detail.SourceID != "source-alpha" || detail.Source != nil || detail.Env != "dev" ||
		detail.Severity != "P1" || detail.State != "firing" || detail.Summary != "Observed summary" ||
		detail.Rule != nil || detail.Series != nil || detail.Labels != nil || detail.Triage != nil || detail.Runbook != nil ||
		detail.Grouping.Group != nil || detail.Grouping.Inhibited != nil || detail.Grouping.Incident.ID != "alert" ||
		len(detail.Notifications) != 2 || detail.Notifications[0].ID != "first" || detail.Notifications[0].Status != "sent" ||
		detail.Notifications[1].Status != "retrying" || detail.Notifications[1].Attempts != 2 {
		t.Fatalf("detail includes unsupported or missing evidence: %+v", detail)
	}
	if strings.Contains(first.Body.String(), "private body") || strings.Contains(first.Body.String(), `"threshold":0`) {
		t.Fatalf("detail leaked body or invented a metric: %s", first.Body.String())
	}
	if got := request("missing", "dev"); got.Code != http.StatusNotFound {
		t.Fatalf("unknown alert status=%d body=%s", got.Code, got.Body.String())
	}
	if got := request("hidden", "dev"); got.Code != http.StatusNotFound {
		t.Fatalf("out-of-scope alert status=%d body=%s", got.Code, got.Body.String())
	}
	// Notification status is independent from the incident-state revision.
	if _, err := db.Exec(ctx, `UPDATE ops_notifications SET status='failed' WHERE id='second'`); err != nil {
		t.Fatal(err)
	}
	second := request("alert", "dev")
	var refreshed AlertDetailResponse
	if err := json.Unmarshal(second.Body.Bytes(), &refreshed); err != nil || second.Code != http.StatusOK ||
		refreshed.Revision != detail.Revision || refreshed.Notifications[1].Status != "failed" {
		t.Fatalf("independent notification status/revision: status=%d body=%s err=%v", second.Code, second.Body.String(), err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,body,created_at)
		SELECT 'bulk-'||i,'alert','app','app-dev','feishu','bulk-'||i,'private body',now()+i*interval '1 second'
		FROM generate_series(1,501) AS i`); err != nil {
		t.Fatal(err)
	}
	bounded := request("alert", "dev")
	var boundedDetail AlertDetailResponse
	if err := json.Unmarshal(bounded.Body.Bytes(), &boundedDetail); err != nil || bounded.Code != http.StatusOK ||
		!boundedDetail.NotificationsTruncated || len(boundedDetail.Notifications) != 500 ||
		boundedDetail.Revision != detail.Revision {
		t.Fatalf("bounded notification history: status=%d count=%d truncated=%t err=%v", bounded.Code,
			len(boundedDetail.Notifications), boundedDetail.NotificationsTruncated, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,body,created_at)
		VALUES('wrong-scope','alert','other','other-test','feishu','wrong-scope','private body',now()-interval '1000 seconds')`); err != nil {
		t.Fatal(err)
	}
	if got := request("alert", "dev"); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("cross-scope notification status=%d body=%s", got.Code, got.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM ops_notifications WHERE id='wrong-scope'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='events' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if got := request("alert", "dev"); got.Code == http.StatusOK {
		t.Fatalf("revoked grant still reads alert: %s", got.Body.String())
	}
}
