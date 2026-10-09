package v3integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestPrometheusRegistrationBindingVaultAndScopedQuery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_prom_query_" + store.ID()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment,archived) VALUES
 ('checkout','Checkout','checkout','dev',false),('plain','Plain','plain','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
		 ('checkout-dev','checkout','dev','Dev','[]'),('checkout-test','checkout','test','Test','[]'),
 ('plain-dev','plain','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('checkout','dev'),('checkout','test'),('plain','dev')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','services','view','application','checkout'),
 ('viewer','services','view','application','plain')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	admin, adminSession, err := st.Login(ctx, "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	// This test grants the built-in viewer narrow scopes itself; drop the global defaults from migration 162 first.
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND scope_kind='global' AND module<>'workspace'`); err != nil {
		t.Fatal(err)
	}
	readerUser, _, err := st.Login(ctx, "reader@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	var vaultCalls atomic.Int32
	var vaultUnavailable atomic.Bool
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vaultCalls.Add(1)
		if r.URL.Path != "/v1/isolated/data/prom" || r.Header.Get("X-Vault-Token") != "test-vault-access" {
			t.Errorf("unexpected Vault request path=%s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if vaultUnavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"test-prom-bearer"}}}`))
	}))
	defer vault.Close()
	resolver := NewVaultResolver(vault.URL, "test-vault-access")
	var queryCalls atomic.Int32
	var queryUnavailable atomic.Bool
	var queryEntered chan struct{}
	var releaseQuery chan struct{}
	const expression = `sum(rate(checkout_requests_total{application="checkout",environment="dev"}[5m]))`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-prom-bearer" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/-/ready":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/query_range":
			queryCalls.Add(1)
			if r.URL.Query().Get("query") != expression || r.URL.Query().Get("limit") != "100" {
				t.Errorf("unexpected registered query request")
			}
			if queryEntered != nil {
				close(queryEntered)
				<-releaseQuery
			}
			if queryUnavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			start, parseErr := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
			if parseErr != nil {
				t.Errorf("query start: %v", parseErr)
				return
			}
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"checkout"},"values":[[%d,"2.5"]]}]}}`, start.Unix())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", resolver).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: adminSession})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertHTTP := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d body=%s; want %d", w.Code, w.Body.String(), status)
		}
	}
	created := request(http.MethodPost, "/api/v1/connectors", fmt.Sprintf(`{"type":"prometheus","url":%q,"auth":"bearer","credential_ref":"vault://isolated/prom#token","tools":["query_metrics"]}`, upstream.URL))
	assertHTTP(created, http.StatusCreated)
	var registration struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &registration); err != nil || registration.ID == "" {
		t.Fatalf("connector registration: %s %v", created.Body.String(), err)
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("checkout\x00dev"))
	targetPath := "/api/v1/services/" + serviceID + "/external-targets"
	bad := request(http.MethodPost, targetPath, fmt.Sprintf(`{"connector_id":%q,"target_kind":"metric_resource","external_id":"https://unexpected.example"}`, registration.ID))
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("URL-shaped resource was accepted: %s", bad.Body.String())
	}
	bound := request(http.MethodPost, targetPath, fmt.Sprintf(`{"connector_id":%q,"target_kind":"metric_resource","external_id":"checkout-web"}`, registration.ID))
	assertHTTP(bound, http.StatusCreated)
	var target externalTarget
	if err := json.Unmarshal(bound.Body.Bytes(), &target); err != nil || target.ID == "" || target.Provider != "prometheus" {
		t.Fatalf("metric target: %s %v", bound.Body.String(), err)
	}
	reader, err := NewPrometheusReader(db, "nonprod", resolver, map[PrometheusQueryKey]string{
		{ConnectorID: registration.ID, ApplicationID: "checkout", EnvironmentID: "dev", ResourceID: "checkout-web", QueryID: "request_rate"}: expression,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := httpapi.V3EnvironmentScope{Tier: "nonprod", EnvironmentIDs: []string{"dev"}}
	window := prometheusTestWindow()
	code := func(err error) string {
		t.Helper()
		if item, ok := err.(*PrometheusReadError); ok {
			return item.Code
		}
		if item, ok := err.(*PrometheusQueryError); ok {
			return item.Code
		}
		return ""
	}
	var registrationStatus string
	var registrationEnabled bool
	if err := db.QueryRow(ctx, `SELECT status,enabled FROM v3_connectors WHERE id=$1`, registration.ID).Scan(&registrationStatus, &registrationEnabled); err != nil || registrationStatus != "unverified" || registrationEnabled {
		t.Fatalf("credentialed registration was enabled before authorization and probe: status=%q enabled=%v err=%v", registrationStatus, registrationEnabled, err)
	}
	assertHTTP(request(http.MethodPost, "/api/v1/connectors/"+registration.ID+"/activate", `{}`), http.StatusOK)
	assertHTTP(request(http.MethodPatch, "/api/v1/connectors/"+registration.ID,
		`{"tools":[{"name":"query_metrics","enabled":true}]}`), http.StatusOK)
	queryUnavailable.Store(true)
	if _, err := reader.Query(ctx, scope, admin, "checkout", "dev", target.ID, "request_rate", window); code(err) != "unavailable" || queryCalls.Load() != 1 {
		t.Fatalf("new registration masked failed live query: code=%q calls=%d", code(err), queryCalls.Load())
	}
	queryUnavailable.Store(false)
	assertHTTP(request(http.MethodPost, "/api/v1/connectors/"+registration.ID+"/test", ""), http.StatusOK)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "forbidden" || queryCalls.Load() != 1 {
		t.Fatalf("missing connector grant: code=%q calls=%d", code(err), queryCalls.Load())
	}
	beforeVault := vaultCalls.Load()
	for _, service := range []string{"checkout", "plain"} {
		if _, err := reader.QueryService(ctx, scope, readerUser, service, "dev", "request_rate", window); code(err) != "forbidden" ||
			queryCalls.Load() != 1 || vaultCalls.Load() != beforeVault {
			t.Fatalf("visible service %q missing connector grant: code=%q queries=%d vault=%d", service, code(err), queryCalls.Load(), vaultCalls.Load())
		}
	}
	if _, err := reader.QueryService(ctx, scope, readerUser, "absent", "dev", "request_rate", window); code(err) != "source_not_found" {
		t.Fatalf("nonexistent service: code=%q", code(err))
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='services' AND action='view' AND scope_kind='application' AND scope_id='checkout'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.QueryService(ctx, scope, readerUser, "checkout", "dev", "request_rate", window); code(err) != "source_not_found" ||
		queryCalls.Load() != 1 || vaultCalls.Load() != beforeVault {
		t.Fatalf("hidden service differed from nonexistent: code=%q queries=%d vault=%d", code(err), queryCalls.Load(), vaultCalls.Load())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','services','view','application','checkout')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "not_reviewed", window); code(err) != "query_unregistered" || queryCalls.Load() != 1 {
		t.Fatalf("unregistered query: code=%q calls=%d", code(err), queryCalls.Load())
	}
	if _, err := reader.Query(ctx, httpapi.V3EnvironmentScope{Tier: "nonprod", EnvironmentIDs: []string{"test"}}, readerUser,
		"checkout", "dev", target.ID, "request_rate", window); code(err) != "source_not_found" || queryCalls.Load() != 1 {
		t.Fatalf("cross-environment read: code=%q calls=%d", code(err), queryCalls.Load())
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET last_checked_at=now()-interval '16 minutes' WHERE id=$1`, registration.ID); err != nil {
		t.Fatal(err)
	}
	evidence, err := reader.QueryService(ctx, scope, readerUser, "checkout", "dev", "request_rate", window)
	if err != nil || evidence.Empty || len(evidence.Series) != 1 || evidence.Series[0].Samples[0].Value != "2.5" || queryCalls.Load() != 2 || vaultCalls.Load() < 3 {
		t.Fatalf("real query evidence=%+v err=%v calls=%d vault=%d", evidence, err, queryCalls.Load(), vaultCalls.Load())
	}
	checkEvidence := func() error {
		return reader.AuthorizeMetricSourceVersion(ctx, readerUser.ID, "dev", evidence.ApplicationID,
			evidence.BindingID, evidence.ConnectorID, evidence.ResourceID, evidence.QueryID, evidence.SourceFingerprint)
	}
	if evidence.SourceFingerprint == "" {
		t.Fatal("query omitted private source fingerprint")
	}
	if err := checkEvidence(); err != nil {
		t.Fatalf("captured source should be authorized: %v", err)
	}
	reader.Resolver = NewBoundResolver(resolver, []CredentialBinding{{
		Tier: "nonprod", EnvironmentID: "dev", ConnectorID: evidence.ConnectorID,
		Provider: "prometheus", Endpoint: upstream.URL, CredentialRef: "vault://isolated/prom#token",
	}})
	if err := checkEvidence(); err != nil {
		t.Fatalf("exact credential binding should authorize source: %v", err)
	}
	reader.Resolver = NewBoundResolver(resolver, nil)
	if got := code(checkEvidence()); got != "source_unhealthy" {
		t.Fatalf("revoked credential binding: code=%q", got)
	}
	reader.Resolver = resolver
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.AuthorizeMetricSourceVersionTx(ctx, tx, readerUser.ID, "dev", evidence.ApplicationID,
		evidence.BindingID, evidence.ConnectorID, evidence.ResourceID, evidence.QueryID, evidence.SourceFingerprint); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("source authorization in conversation transaction: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	staleTx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if got := code(reader.AuthorizeMetricSourceVersionTx(ctx, staleTx, readerUser.ID, "dev", evidence.ApplicationID,
		evidence.BindingID, evidence.ConnectorID, evidence.ResourceID, evidence.QueryID, evidence.SourceFingerprint)); got != "invalid_scope" {
		_ = staleTx.Rollback(ctx)
		t.Fatalf("stale snapshot isolation accepted: code=%q", got)
	}
	if err := staleTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Repeatable Read cannot be used for source reauthorization: a new
	// binding is a phantom in its old snapshot even after the mapping row is
	// locked. Detail/list readers need a fresh transaction for this check.
	staleTx, err = db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	var beforeCount int
	if err := staleTx.QueryRow(ctx, `SELECT count(*) FROM v3_connector_targets
 WHERE application_id='checkout' AND environment_id='dev' AND target_kind='metric_resource'`).Scan(&beforeCount); err != nil {
		t.Fatal(err)
	}
	if beforeCount != 1 {
		t.Fatalf("unexpected target fixture count: %d", beforeCount)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES ('rr-phantom','checkout','dev',$1,'metric_resource','rr-resource',$2)`, evidence.ConnectorID, admin.ID); err != nil {
		t.Fatal(err)
	}
	svc := Service{DB: db}
	if _, err := svc.targetServiceTx(ctx, staleTx, "checkout", "dev", true); err != nil {
		t.Fatalf("RR mapping lock changed behavior: %v", err)
	}
	var staleCount int
	if err := staleTx.QueryRow(ctx, `SELECT count(*) FROM v3_connector_targets
 WHERE application_id='checkout' AND environment_id='dev' AND target_kind='metric_resource'`).Scan(&staleCount); err != nil {
		t.Fatal(err)
	}
	if staleCount != 1 {
		t.Fatalf("RR phantom premise changed: stale count=%d", staleCount)
	}
	if err := staleTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='rr-phantom'`); err != nil {
		t.Fatal(err)
	}
	staleTx, err = db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	var grantRole string
	if err := staleTx.QueryRow(ctx, `SELECT role_id FROM v3_role_grants
 WHERE role_id='viewer' AND module='connectors' AND action='view'
 AND scope_kind='environment' AND scope_id='dev'`).Scan(&grantRole); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer'
 AND module='connectors' AND action='view' AND scope_kind='environment' AND scope_id='dev'`); err != nil {
		t.Fatal(err)
	}
	var staleRole string
	err = staleTx.QueryRow(ctx, `SELECT role_id FROM v3_role_grants
 WHERE role_id='viewer' AND module='connectors' AND action='view'
 AND scope_kind='environment' AND scope_id='dev' FOR SHARE`).Scan(&staleRole)
	var serialization *pgconn.PgError
	if !errors.As(err, &serialization) || serialization.Code != "40001" {
		t.Fatalf("RR revoked-grant lock must fail serialization, got %v", err)
	}
	if err := staleTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES ('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	// A conversation transaction must pin both the binding and permission
	// witness until its message/reply rows commit.
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.AuthorizeMetricSourceVersionTx(ctx, tx, readerUser.ID, "dev", evidence.ApplicationID,
		evidence.BindingID, evidence.ConnectorID, evidence.ResourceID, evidence.QueryID, evidence.SourceFingerprint); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	writeTxs := make([]pgx.Tx, 0, 2)
	writeResults := make([]chan error, 0, 2)
	for _, statement := range []string{
		`UPDATE v3_connector_targets SET external_id='different-resource' WHERE id=$1`,
		`DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view' AND scope_kind='environment' AND scope_id='dev' AND $1<>''`,
	} {
		writer, beginErr := db.Begin(ctx)
		if beginErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(beginErr)
		}
		writeTxs = append(writeTxs, writer)
		result := make(chan error, 1)
		writeResults = append(writeResults, result)
		go func() {
			_, writeErr := writer.Exec(ctx, statement, evidence.BindingID)
			result <- writeErr
		}()
	}
	for _, result := range writeResults {
		select {
		case writeErr := <-result:
			_ = tx.Rollback(ctx)
			t.Fatalf("source or grant write was not blocked: %v", writeErr)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for i, result := range writeResults {
		select {
		case writeErr := <-result:
			if writeErr != nil {
				t.Fatal(writeErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("blocked writer did not resume")
		}
		if err := writeTxs[i].Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkEvidence(); err != nil {
		t.Fatalf("source changed during rolled-back lock check: %v", err)
	}
	if code(reader.AuthorizeMetricSource(ctx, readerUser.ID, "dev", evidence.ApplicationID,
		evidence.BindingID, evidence.ConnectorID, evidence.ResourceID, "not_reviewed")) != "query_unregistered" {
		t.Fatal("unreviewed query was accepted")
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connector_targets SET external_id='different-resource' WHERE id=$1`, evidence.BindingID); err != nil {
		t.Fatal(err)
	}
	if got := code(checkEvidence()); got != "source_changed" {
		t.Fatalf("rebound resource: code=%q", got)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connector_targets SET external_id=$2 WHERE id=$1`, evidence.BindingID, evidence.ResourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors
 (id,environment_id,provider,name,category,endpoint,auth_method,tools,enabled)
 VALUES ('vm-metrics','dev','victoriametrics','VM','observe','http://127.0.0.1:19090','none','[]',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES ('other-provider-metric','checkout','dev','vm-metrics','metric_resource','other-resource',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := checkEvidence(); err != nil {
		t.Fatalf("other provider should not change selected Prometheus target: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES ('another-metric-target','checkout','dev',$1,'metric_resource','another-resource',$2)`, evidence.ConnectorID, admin.ID); err != nil {
		t.Fatal(err)
	}
	if got := code(checkEvidence()); got != "source_ambiguous" {
		t.Fatalf("two metric targets: code=%q", got)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='another-metric-target'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET auth_method='none',credential_ref='' WHERE id=$1`, evidence.ConnectorID); err != nil {
		t.Fatal(err)
	}
	if got := code(checkEvidence()); got != "source_changed" {
		t.Fatalf("connector settings changed under stable IDs: code=%q", got)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET auth_method='bearer',credential_ref='vault://isolated/prom#token' WHERE id=$1`, evidence.ConnectorID); err != nil {
		t.Fatal(err)
	}
	if err := checkEvidence(); err != nil {
		t.Fatalf("restored connector settings: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE users SET disabled=true WHERE id=$1`, readerUser.ID); err != nil {
		t.Fatal(err)
	}
	if got := code(checkEvidence()); got != "forbidden" {
		t.Fatalf("disabled owner: code=%q", got)
	}
	if _, err := db.Exec(ctx, `UPDATE users SET disabled=false WHERE id=$1`, readerUser.ID); err != nil {
		t.Fatal(err)
	}
	if err := checkEvidence(); err != nil {
		t.Fatalf("restored source: %v", err)
	}
	raw, err := json.Marshal(evidence)
	if err != nil || strings.Contains(string(raw), "test-prom-bearer") || strings.Contains(string(raw), "vault://") || strings.Contains(string(raw), expression) || strings.Contains(string(raw), upstream.URL) || strings.Contains(string(raw), evidence.SourceFingerprint) {
		t.Fatalf("query evidence leaked private source data: %v", err)
	}
	queryUnavailable.Store(true)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "unavailable" {
		t.Fatalf("ready probe masked failed query: code=%q", code(err))
	}
	queryUnavailable.Store(false)
	vaultUnavailable.Store(true)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "credential_unavailable" {
		t.Fatalf("unavailable Vault: code=%q", code(err))
	}
	vaultUnavailable.Store(false)
	assertHTTP(request(http.MethodPatch, "/api/v1/connectors/"+registration.ID,
		`{"tools":[{"name":"query_metrics","enabled":false}]}`), http.StatusOK)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "source_unhealthy" {
		t.Fatalf("disabled tool: code=%q", code(err))
	}
	assertHTTP(request(http.MethodPatch, "/api/v1/connectors/"+registration.ID,
		`{"tools":[{"name":"query_metrics","enabled":true}]}`), http.StatusOK)
	queryEntered = make(chan struct{})
	releaseQuery = make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, queryErr := reader.QueryService(ctx, scope, readerUser, "checkout", "dev", "request_rate", window)
		result <- queryErr
	}()
	select {
	case <-queryEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("live query did not start")
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view' AND scope_kind='environment' AND scope_id='dev'`); err != nil {
		t.Fatal(err)
	}
	close(releaseQuery)
	if err := <-result; code(err) != "forbidden" {
		t.Fatalf("grant revoked during query: code=%q", code(err))
	}
	if got := code(checkEvidence()); got != "forbidden" {
		t.Fatalf("revoked connector access: code=%q", got)
	}
}
