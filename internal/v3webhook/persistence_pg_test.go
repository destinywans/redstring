package v3webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
)

type pgWebhook struct {
	db      *pgxpool.Pool
	api     *httpapi.Server
	service *Service
	token   string
	start   time.Time
}

func realWebhook(t *testing.T) pgWebhook {
	t.Helper()
	if os.Getenv("AIOPS_TESTLAB_BACKEND_ENABLED") != "dedicated-temp-container" {
		t.Skip("explicit disposable testlab opt-in required")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/aiops_testlab" {
		t.Fatal("explicit disposable loopback PostgreSQL required")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("test pool unavailable")
	}
	schema := "webhook_persist_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("test pool invalid")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 4
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("test pool unavailable")
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,key,name,environment) VALUES('checkout','checkout','Synthetic checkout','test'),('other','other','Synthetic other','test');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('checkout-test','checkout','test','Test'),('other-test','other','test','Test');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('checkout','test'),('other','test');`); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s", 32)
	file := filepath.Join(t.TempDir(), "source-token")
	if err := os.WriteFile(file, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	configuration := operations.Config{Sources: []operations.Source{{
		ID: "prom", TokenFile: file,
		Mappings: []operations.Mapping{
			{ApplicationID: "checkout", EnvironmentID: "checkout-test", Labels: map[string]string{"service": "checkout"}},
			{ApplicationID: "other", EnvironmentID: "other-test", Labels: map[string]string{"service": "other"}},
		},
	}}}
	service, err := New(db, httpapi.V3InstanceConfig{Tier: "nonprod", EnvironmentIDs: []string{"test"}}, configuration)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil, false)
	api.RegisterPublic(Pattern, service.Handler)
	return pgWebhook{db: db, api: api, service: service, token: token, start: time.Date(2026, 9, 30, 1, 2, 3, 0, time.UTC)}
}

func TestWebhookInsertBindingRealPG(t *testing.T) {
	h := realWebhook(t)
	source := h.service.sources["prom"]
	err := h.service.persistAlerts(t.Context(), source, []mappedAlert{{alert: h.alert("sql-binding", "checkout", "firing"), mapping: source.Mappings[0]}})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatalf("real webhook persistence failed SQLSTATE=%s", pgErr.Code)
		}
		t.Fatalf("real webhook persistence failed type=%T", err)
	}
	if count(t, h.db, `SELECT count(*) FROM ops_incidents`) != 1 {
		t.Fatal("persistence did not commit incident")
	}
}

func (h pgWebhook) alert(fingerprint, service, status string) operations.Alert {
	a := operations.Alert{Fingerprint: fingerprint, Status: status, Labels: map[string]string{"service": service, "alertname": "Synthetic persisted signal", "severity": "critical"}, Annotations: map[string]string{"summary": "Synthetic input"}, StartsAt: h.start}
	if status == "resolved" {
		a.EndsAt = h.start.Add(time.Minute)
	}
	return a
}

func (h pgWebhook) post(t *testing.T, want int, alerts ...operations.Alert) {
	t.Helper()
	raw, err := json.Marshal(operations.AlertEnvelope{Alerts: alerts})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager/prom", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+h.token)
	w := httptest.NewRecorder()
	h.api.ServeHTTP(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != want {
		code := ""
		if e, ok := body["error"].(map[string]any); ok {
			code, _ = e["code"].(string)
		}
		t.Fatalf("registered webhook status=%d want=%d error_code=%s", w.Code, want, code)
	}
	if want == 202 && (body["accepted"] != float64(len(alerts)) || body["notification_delivery"] != "not_configured") {
		t.Fatal("acceptance contract did not report committed alerts and disconnected notifications")
	}
}

func count(t *testing.T, db *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRegisteredWebhookPersistsRealPostgres(t *testing.T) {
	h := realWebhook(t)
	a := h.alert("identity", "checkout", "firing")
	h.post(t, 202, a)
	if count(t, h.db, `SELECT count(*) FROM ops_incidents WHERE fingerprint='identity' AND application_id='checkout' AND environment_id='checkout-test' AND labels->>'service'='checkout'`) != 1 || count(t, h.db, `SELECT count(*) FROM ops_alert_occurrences`) != 1 {
		t.Fatal("202 did not commit exact incident/occurrence")
	}
	// Identical delivery is accepted but cannot append another occurrence.
	h.post(t, 202, a)
	if count(t, h.db, `SELECT count(*) FROM ops_alert_occurrences`) != 1 {
		t.Fatal("duplicate webhook appended a duplicate occurrence")
	}
	resolved := h.alert("identity", "checkout", "resolved")
	h.post(t, 202, resolved)
	late := a
	late.Annotations = map[string]string{"summary": "Late firing delivery"}
	h.post(t, 202, late)
	var state string
	var ends time.Time
	if err := h.db.QueryRow(t.Context(), `SELECT alert_status,ends_at FROM ops_incidents WHERE fingerprint='identity'`).Scan(&state, &ends); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" || !ends.Equal(resolved.EndsAt) {
		t.Fatal("late firing reopened a resolved occurrence or lost its end time")
	}
	conflict := a
	conflict.Labels = map[string]string{"service": "checkout", "alertname": "Changed identity labels", "severity": "critical"}
	h.post(t, 409, conflict)
	if count(t, h.db, `SELECT count(*) FROM ops_alert_occurrences`) != 3 {
		t.Fatal("label conflict changed the durable occurrence history")
	}
	// Webhook ingest has no management-audit producer or notification worker.
	// Assert real incidence/history, rather than inventing an audit contract.
	if count(t, h.db, `SELECT count(*) FROM ops_notifications`) != 0 {
		t.Fatal("ingest queued external notifications without a configured delivery worker")
	}
}

func TestRegisteredWebhookMultiAlertRollbackAndArchivedMapping(t *testing.T) {
	h := realWebhook(t)
	if _, err := h.db.Exec(t.Context(), `ALTER TABLE ops_incidents ADD CONSTRAINT synthetic_rejected_second CHECK(fingerprint<>'rejected-second')`); err != nil {
		t.Fatal(err)
	}
	// Valid HTTP payloads pass mapping. The second real INSERT fails in PG,
	// after the first INSERT/occurrence, and must roll back the entire envelope.
	h.post(t, 503, h.alert("accepted-first", "checkout", "firing"), h.alert("rejected-second", "checkout", "firing"))
	if count(t, h.db, `SELECT count(*) FROM ops_incidents`) != 0 || count(t, h.db, `SELECT count(*) FROM ops_alert_occurrences`) != 0 {
		t.Fatal("second database failure left partial successful alerts")
	}
	if _, err := h.db.Exec(t.Context(), `UPDATE applications SET archived=true WHERE id='other'`); err != nil {
		t.Fatal(err)
	}
	h.post(t, 503, h.alert("before-stale-map", "checkout", "firing"), h.alert("archived-map", "other", "firing"))
	if count(t, h.db, `SELECT count(*) FROM ops_incidents`) != 0 || count(t, h.db, `SELECT count(*) FROM ops_alert_occurrences`) != 0 {
		t.Fatal("archived mapping left a partially committed envelope")
	}
	if _, err := h.db.Exec(t.Context(), `UPDATE applications SET archived=false WHERE id='other';UPDATE application_environments SET archived=true WHERE id='other-test'`); err != nil {
		t.Fatal(err)
	}
	h.post(t, 503, h.alert("archived-env", "other", "firing"))
	if count(t, h.db, `SELECT count(*) FROM ops_incidents`) != 0 {
		t.Fatal("archived environment accepted an alert")
	}
}
