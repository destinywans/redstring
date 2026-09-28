package v3catalog

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestMappingBindingSupportedUsesLegacyRuntimePolicy(t *testing.T) {
	// Model a legacy platform with one Kubernetes registration. Match its
	// duplicate-kind and exact-registration checks, including GitLab rejection.
	legacyValidator := func(bindings []core.Binding) error {
		seen := map[string]bool{}
		for _, binding := range bindings {
			if seen[binding.Kind] || binding != (core.Binding{Kind: "kubernetes", ConnectorID: "cluster-1", Resource: "deployment"}) {
				return store.ErrInvalidReference
			}
			seen[binding.Kind] = true
		}
		return nil
	}
	expected := core.Binding{Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "123"}
	if !errors.Is(legacyValidator([]core.Binding{expected}), store.ErrInvalidReference) {
		t.Fatal("global legacy policy unexpectedly accepts a GitLab binding")
	}
	registered := core.Binding{Kind: "kubernetes", ConnectorID: "cluster-1", Resource: "deployment"}
	cases := []struct {
		name     string
		bindings []core.Binding
		want     bool
	}{
		{"exact", []core.Binding{registered, expected}, true},
		{"missing GitLab", []core.Binding{registered}, false},
		{"wrong project", []core.Binding{registered, {Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "124"}}, false},
		{"wrong connector", []core.Binding{registered, {Kind: "gitlab", ConnectorID: "gitlab-2", Resource: "123"}}, false},
		{"duplicate code", []core.Binding{expected, expected}, false},
		{"other code family", []core.Binding{expected, {Kind: "github", ConnectorID: "github-1", Resource: "123"}}, false},
		{"unregistered legacy", []core.Binding{expected, {Kind: "kubernetes", ConnectorID: "cluster-2", Resource: "deployment"}}, false},
		{"duplicate legacy", []core.Binding{registered, registered, expected}, false},
	}
	service := &Service{MappingBindingValidator: legacyValidator}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := service.mappingBindingSupported(tc.bindings, expected); got != tc.want {
				t.Fatalf("supported=%t, want %t", got, tc.want)
			}
		})
	}
	service.MappingBindingValidator = nil
	if service.mappingBindingSupported([]core.Binding{expected}, expected) {
		t.Fatal("missing legacy runtime validator did not fail closed")
	}
}

func TestMappingCandidateDiscoveryAndAcceptance(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mapping_candidates_" + store.ID()
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
	cfg.MaxConns = 4 // Concurrent accept uses the same connector across services.
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-mapping-password"); err != nil {
		t.Fatal(err)
	}
	admin, token, err := st.Login(ctx, "admin@example.invalid", "isolated-mapping-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('admin','connectors','view','global','*'),('admin','connectors','manage','global','*'),
 ('admin','services','view','global','*'),('admin','services','change','global','*')
 ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES
 ('app-checkout','Checkout','checkout','dev'),('app-other','Other','other','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('env-checkout','app-checkout','dev','Dev'),('env-other','app-other','dev','Dev');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-checkout','dev'),('app-other','dev');
	 INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled,status,last_checked_at) VALUES
 ('gitlab-1','dev','gitlab','GitLab 1','ci','https://gitlab.example.invalid','token',true,'ok',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('target-checkout','app-checkout','dev','gitlab-1','project','123',$1),
 ('target-other','app-other','dev','gitlab-1','project','456',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, "dev").Register(api)
	// No legacy connector is registered in this fixture. Model the platform
	// policy: an empty legacy set passes; every unregistered kind, including
	// GitLab, is rejected by the global writer.
	legacyValidator := func(bindings []core.Binding) error {
		if len(bindings) != 0 {
			return store.ErrInvalidReference
		}
		return nil
	}
	if !errors.Is(legacyValidator([]core.Binding{{Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "123"}}), store.ErrInvalidReference) {
		t.Fatal("global legacy validator accepted GitLab")
	}
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	// The frozen page sends a bodyless PUT, and its GET rows contain display
	// values without stable source identities or binding revisions. Neither
	// shape may be acknowledged as a persisted full mapping save.
	for _, body := range []string{"", `{"rows":[{"service":"checkout","cells":{"code":{"value":"123"}}}]}`} {
		w := request(http.MethodPut, "/service-mappings", body)
		if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), `"v3_not_implemented"`) {
			t.Fatalf("unsafe mapping save status=%d body=%s", w.Code, w.Body.String())
		}
	}
	var saveBindings []byte
	if err := db.QueryRow(ctx, `SELECT bindings FROM application_environments WHERE id='env-checkout'`).Scan(&saveBindings); err != nil || string(saveBindings) != "[]" {
		t.Fatalf("unimplemented save changed bindings: bindings=%s err=%v", saveBindings, err)
	}
	var saveAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&saveAudits); err != nil || saveAudits != 0 {
		t.Fatalf("unimplemented save wrote audit: count=%d err=%v", saveAudits, err)
	}
	readMappings := func() ServiceMappingForTest {
		t.Helper()
		w := request(http.MethodGet, "/service-mappings", "")
		if w.Code != 200 {
			t.Fatalf("GET mappings status=%d body=%s", w.Code, w.Body.String())
		}
		var result ServiceMappingForTest
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	readAccepted := func(body string, wantStatus, wantCount int) {
		t.Helper()
		w := request(http.MethodPost, "/service-mappings/accept", body)
		if w.Code != wantStatus {
			t.Fatalf("POST accept status=%d body=%s", w.Code, w.Body.String())
		}
		if wantStatus == 200 {
			var result struct {
				Accepted int `json:"accepted"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Accepted != wantCount {
				t.Fatalf("POST accept response=%s err=%v", w.Body.String(), err)
			}
		}
	}
	if initial := readMappings(); initial.Suggestions != 0 {
		t.Fatalf("unreviewed sources appeared as suggestions: %+v", initial)
	}
	// A migrated enabled connector has no activation evidence for its target.
	// Even an otherwise valid binding shape cannot turn it into a suggestion.
	api.ValidateBindings = legacyValidator
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"suggestions":0`) {
		t.Fatalf("unactivated discover status=%d body=%s", w.Code, w.Body.String())
	}
	var pendingCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_service_mapping_candidates WHERE accepted_at IS NULL`).Scan(&pendingCount); err != nil || pendingCount != 0 {
		t.Fatalf("unactivated source was persisted: count=%d err=%v", pendingCount, err)
	}
	// A connector probe may be fresh while project activation is older. Its
	// historical verification remains usable because enabled connectors have
	// no renewal endpoint. Backdate the target so the audit follows binding.
	if _, err := db.Exec(ctx, `UPDATE v3_connector_targets
 SET bound_at=now()-interval '25 minutes' WHERE connector_id='gitlab-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes,created_at)
 VALUES($1,'connector.activated','gitlab-1','success',
 '{"environment":"dev","verified_projects":2}',now()-interval '20 minutes')`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"suggestions":2`) {
		t.Fatalf("historical project verification was lost: status=%d body=%s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_service_mapping_candidates WHERE accepted_at IS NULL`).Scan(&pendingCount); err != nil || pendingCount != 2 {
		t.Fatalf("historical project candidates missing: count=%d err=%v", pendingCount, err)
	}
	api.ValidateBindings = nil
	// A registration that the runtime environment writer cannot validate is
	// not offered as an actionable suggestion.
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"suggestions":0`) {
		t.Fatalf("unsupported discover status=%d body=%s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_service_mapping_candidates WHERE accepted_at IS NULL`).Scan(&pendingCount); err != nil || pendingCount != 2 {
		t.Fatalf("unsupported candidate snapshot not persisted: count=%d err=%v", pendingCount, err)
	}
	api.ValidateBindings = legacyValidator
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"suggestions":2`) {
		t.Fatalf("discover status=%d body=%s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_service_mapping_candidates WHERE accepted_at IS NULL`).Scan(&pendingCount); err != nil || pendingCount != 2 {
		t.Fatalf("candidate persistence count=%d err=%v", pendingCount, err)
	}
	view := readMappings()
	if view.Suggestions != 2 || len(view.Rows) != 2 {
		t.Fatalf("configured candidates not projected: %+v", view)
	}
	for _, row := range view.Rows {
		if row.Cells["code"].State != "suggested" || row.Cells["code"].Brand != "gitlab" {
			t.Fatalf("candidate was asserted as a current binding: %+v", row)
		}
	}
	allRequest := httptest.NewRequest(http.MethodGet, "/api/v1/service-mappings", nil)
	allRequest.Header.Set("X-AIOps-Environment", "all")
	allRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	allResponse := httptest.NewRecorder()
	api.ServeHTTP(allResponse, allRequest)
	if allResponse.Code != 200 {
		t.Fatalf("all-environment view status=%d body=%s", allResponse.Code, allResponse.Body.String())
	}
	var allView ServiceMappingForTest
	if err := json.Unmarshal(allResponse.Body.Bytes(), &allView); err != nil || allView.Suggestions != 0 {
		t.Fatalf("all-environment view offered unacceptably ambiguous suggestions: %+v err=%v", allView, err)
	}
	api.ValidateBindings = nil
	readAccepted(`{"cells":["checkout:code"]}`, 409, 0)
	api.ValidateBindings = legacyValidator
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id='admin' AND module='services' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	lockTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var lockedApp string
	if err := lockTx.QueryRow(ctx, `SELECT a.id FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='dev'
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id
 WHERE a.key='checkout' FOR UPDATE OF a,ve,e`).Scan(&lockedApp); err != nil {
		t.Fatal(err)
	}
	// An unauthorized request must fail before waiting on the service lock.
	quickCtx, quickCancel := context.WithTimeout(ctx, time.Second)
	quickRequest := httptest.NewRequest(http.MethodPost, "/api/v1/service-mappings/accept",
		strings.NewReader(`{"cells":["checkout:code"]}`)).WithContext(quickCtx)
	quickRequest.Header.Set("X-AIOps-Environment", "dev")
	quickRequest.Header.Set("Content-Type", "application/json")
	quickRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	quickResponse := httptest.NewRecorder()
	api.ServeHTTP(quickResponse, quickRequest)
	quickCancel()
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if quickResponse.Code != 404 {
		t.Fatalf("unauthorized request waited on a service lock: status=%d body=%s", quickResponse.Code, quickResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','services','change','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id='admin' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	readAccepted(`{"cells":["checkout:code"]}`, 403, 0)
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','connectors','manage','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET last_checked_at=now()-interval '20 minutes'
 WHERE id='gitlab-1'`); err != nil {
		t.Fatal(err)
	}
	readAccepted(`{"cells":["checkout:code"]}`, 409, 0)
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET last_checked_at=now() WHERE id='gitlab-1'`); err != nil {
		t.Fatal(err)
	}
	readAccepted(`{"cells":["checkout:code"]}`, 200, 1)
	readAccepted(`{"cells":["checkout:code"]}`, 200, 0)
	var revision int64
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT binding_revision,bindings FROM application_environments WHERE id='env-checkout'`).Scan(&revision, &raw); err != nil {
		t.Fatal(err)
	}
	var bindings []core.Binding
	if err := json.Unmarshal(raw, &bindings); err != nil || revision != 2 || len(bindings) != 1 || bindings[0].Kind != "gitlab" || bindings[0].ConnectorID != "gitlab-1" || bindings[0].Resource != "123" {
		t.Fatalf("accepted mapping is not exact: revision=%d bindings=%s err=%v", revision, raw, err)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("accept was not exactly-once audited: count=%d err=%v", audits, err)
	}
	view = readMappings()
	for _, row := range view.Rows {
		if row.Service == "checkout" && (row.Cells["code"].State != "ok" || row.Cells["code"].Value != "123") {
			t.Fatalf("accepted binding was not read back: %+v", row)
		}
	}
	// Removing an accepted binding permits a fresh review of the same exact
	// target without erasing the earlier accepted decision and audit history.
	if _, err := db.Exec(ctx, `UPDATE application_environments
 SET bindings='[]',binding_revision=binding_revision+1 WHERE id='env-checkout'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"suggestions":2`) {
		t.Fatalf("rediscover status=%d body=%s", w.Code, w.Body.String())
	}
	readAccepted(`{"cells":["checkout:code"]}`, 200, 1)
	readAccepted(`{"cells":["checkout:code"]}`, 200, 0)
	if err := db.QueryRow(ctx, `SELECT binding_revision FROM application_environments WHERE id='env-checkout'`).Scan(&revision); err != nil || revision != 4 {
		t.Fatalf("rediscovery did not advance binding revision once: revision=%d err=%v", revision, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("rediscovery audit count=%d err=%v", audits, err)
	}
	// A second reviewed target invalidates the frozen service:system key.
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('target-other-2','app-other','dev','gitlab-1','project','789',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	readAccepted(`{"cells":["other:code"]}`, 409, 0)
	view = readMappings()
	for _, row := range view.Rows {
		if row.Service == "other" && row.Cells["code"].State != "missing" {
			t.Fatalf("ambiguous target remained suggested: %+v", row)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='target-other-2';
 UPDATE v3_connector_targets SET external_id='999' WHERE id='target-other'`); err != nil {
		t.Fatal(err)
	}
	readAccepted(`{"cells":["other:code"]}`, 409, 0)
	if err := db.QueryRow(ctx, `SELECT bindings FROM application_environments WHERE id='env-other'`).Scan(&raw); err != nil || string(raw) != "[]" {
		t.Fatalf("stale or ambiguous source mutated mapping: bindings=%s err=%v", raw, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("failed acceptance wrote audit: count=%d err=%v", audits, err)
	}
	// Accept-all and a single-cell accept share one connector. Locking all
	// service rows first prevents a connector/service lock cycle.
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES
 ('app-concurrent-a','Concurrent A','concurrent-a','dev'),
 ('app-concurrent-b','Concurrent B','concurrent-b','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('env-concurrent-a','app-concurrent-a','dev','Dev'),
 ('env-concurrent-b','app-concurrent-b','dev','Dev');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-concurrent-a','dev'),('app-concurrent-b','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('target-concurrent-a','app-concurrent-a','dev','gitlab-1','project','1001',$1),
 ('target-concurrent-b','app-concurrent-b','dev','gitlab-1','project','1002',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'connector.activated','gitlab-1','success','{"environment":"dev","verified_projects":4}')`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 {
		t.Fatalf("concurrent source discover status=%d body=%s", w.Code, w.Body.String())
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, body := range []string{
		`{"cells":["concurrent-a:code","concurrent-b:code"]}`,
		`{"cells":["concurrent-b:code"]}`,
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(http.MethodPost, "/service-mappings/accept", body)
		}()
	}
	wg.Wait()
	close(results)
	acceptedTotal := 0
	for result := range results {
		if result.Code != 200 {
			t.Fatalf("concurrent accept status=%d body=%s", result.Code, result.Body.String())
		}
		var acceptedResponse struct {
			Accepted int `json:"accepted"`
		}
		if err := json.Unmarshal(result.Body.Bytes(), &acceptedResponse); err != nil {
			t.Fatal(err)
		}
		acceptedTotal += acceptedResponse.Accepted
	}
	if acceptedTotal != 2 {
		t.Fatalf("concurrent accept counted %d new mappings, want 2", acceptedTotal)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&audits); err != nil || audits != 4 {
		t.Fatalf("concurrent accept audit count=%d err=%v", audits, err)
	}
	t.Run("concurrent discovery keeps both candidates", func(t *testing.T) {
		// Reopen the two accepted services. Each discover request then updates
		// both pending candidate rows in one transaction, exercising the lock
		// order when the requests start together.
		if _, err := db.Exec(ctx, `UPDATE application_environments
		 SET bindings='[]',binding_revision=binding_revision+1
		 WHERE id IN ('env-concurrent-a','env-concurrent-b')`); err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 12; round++ {
			start := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- request(http.MethodPost, "/service-mappings/discover", "")
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			for result := range results {
				if result.Code != http.StatusOK {
					t.Fatalf("round %d concurrent discover status=%d body=%s", round, result.Code, result.Body.String())
				}
				var response struct {
					Suggestions int `json:"suggestions"`
				}
				if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil || response.Suggestions < 2 {
					t.Fatalf("round %d concurrent discover suggestions=%d body=%s err=%v", round, response.Suggestions, result.Body.String(), err)
				}
			}
			var pending, distinctApps, currentRevisions int
			if err := db.QueryRow(ctx, `SELECT count(*),count(DISTINCT m.application_id),
			 count(*) FILTER (WHERE m.binding_revision=e.binding_revision
			 AND m.expires_at>now() AND m.source_id=t.id AND m.external_id=t.external_id)
			 FROM v3_service_mapping_candidates m
			 JOIN application_environments e ON e.application_id=m.application_id AND e.key=m.environment_id
			 JOIN v3_connector_targets t ON t.application_id=m.application_id AND t.environment_id=m.environment_id
			 WHERE m.application_id IN ('app-concurrent-a','app-concurrent-b') AND m.accepted_at IS NULL`).Scan(
				&pending, &distinctApps, &currentRevisions); err != nil {
				t.Fatal(err)
			}
			if pending != 2 || distinctApps != 2 || currentRevisions != 2 {
				t.Fatalf("round %d inconsistent candidates: pending=%d apps=%d current=%d", round, pending, distinctApps, currentRevisions)
			}
		}
	})
}
