package v3catalog

import (
	"context"
	"encoding/json"
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
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// PA-19 P4: the code binding must be exactly the candidate's project; the
// other stored bindings need a known system, each at most once, and are not
// checked against the legacy registrations a V3 instance no longer loads.
func TestMappingBindingSupportedChecksTheCodeBinding(t *testing.T) {
	expected := core.Binding{Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "123"}
	registered := core.Binding{Kind: "kubernetes", ConnectorID: "cluster-1", Resource: "deployment"}
	cases := []struct {
		name     string
		bindings []core.Binding
		want     bool
	}{
		{"exact", []core.Binding{registered, expected}, true},
		{"only GitLab", []core.Binding{expected}, true},
		{"other registered system", []core.Binding{expected, {Kind: "kubernetes", ConnectorID: "cluster-2", Resource: "deployment"}}, true},
		{"deploy binding", []core.Binding{expected, {Kind: "argocd", ConnectorID: "argo-1", Resource: "checkout"}}, true},
		{"missing GitLab", []core.Binding{registered}, false},
		{"wrong project", []core.Binding{registered, {Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "124"}}, false},
		{"wrong connector", []core.Binding{registered, {Kind: "gitlab", ConnectorID: "gitlab-2", Resource: "123"}}, false},
		{"duplicate code", []core.Binding{expected, expected}, false},
		{"other code family", []core.Binding{expected, {Kind: "github", ConnectorID: "github-1", Resource: "123"}}, false},
		{"duplicate system", []core.Binding{registered, registered, expected}, false},
		{"unknown kind", []core.Binding{expected, {Kind: "custom", ConnectorID: "x", Resource: "y"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mappingBindingSupported(tc.bindings, expected); got != tc.want {
				t.Fatalf("supported=%t, want %t", got, tc.want)
			}
		})
	}
	if mappingBindingSupported([]core.Binding{{Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "not-a-project"}},
		core.Binding{Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "not-a-project"}) {
		t.Fatal("an invalid project was accepted")
	}
}

func waitForMappingServiceLock(t *testing.T, db *pgxpool.Pool, lockerPID int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := db.QueryRow(t.Context(), `SELECT EXISTS (
 SELECT 1 FROM pg_stat_activity WHERE state='active' AND wait_event_type='Lock'
 AND $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%FOR UPDATE OF a,ve,e%')`, lockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("mapping writer did not reach the service lock")
}

// The service mapping reads and writes the registry systems as targets
// (PA-19 P3 stage 2); code keeps its stored binding and the GitLab candidate
// flow.
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
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
	catalogService := New(st, policy, "dev")
	catalogService.Registry = &v3telemetry.Resolver{Tier: "nonprod"}
	catalogService.Register(api)
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
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unsafe mapping save status=%d body=%s", w.Code, w.Body.String())
		}
	}
	var saveBindings []byte
	if err := db.QueryRow(ctx, `SELECT bindings FROM application_environments WHERE id='env-checkout'`).Scan(&saveBindings); err != nil || string(saveBindings) != "[]" {
		t.Fatalf("invalid save changed bindings: bindings=%s err=%v", saveBindings, err)
	}
	var saveAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action IN ('v3.service_mapping.accepted','v3.service_mapping.saved')`).Scan(&saveAudits); err != nil || saveAudits != 0 {
		t.Fatalf("invalid save wrote audit: count=%d err=%v", saveAudits, err)
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
	selected := func(service string) mappingAcceptCandidate {
		t.Helper()
		for _, row := range readMappings().Rows {
			if row.Service == service && row.Cells["code"].Candidate != nil {
				candidate := row.Cells["code"].Candidate
				return mappingAcceptCandidate{ID: candidate.ID, Version: candidate.Version,
					ServiceID: candidate.ServiceID, EnvironmentID: candidate.EnvironmentID,
					SystemID: candidate.SystemID, BindingRevision: candidate.BindingRevision}
			}
		}
		t.Fatalf("missing reviewable candidate for %s", service)
		return mappingAcceptCandidate{}
	}
	acceptBody := func(candidates ...mappingAcceptCandidate) string {
		t.Helper()
		body, err := json.Marshal(mappingAcceptInput{Candidates: candidates})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	readAccepted := func(body string, wantStatus, wantCount int) []mappingAcceptItem {
		t.Helper()
		w := request(http.MethodPost, "/service-mappings/accept", body)
		if w.Code != wantStatus {
			t.Fatalf("POST accept status=%d body=%s", w.Code, w.Body.String())
		}
		if wantStatus == 200 {
			var result struct {
				Accepted int                 `json:"accepted"`
				Items    []mappingAcceptItem `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Accepted != wantCount {
				t.Fatalf("POST accept response=%s err=%v", w.Body.String(), err)
			}
			if len(result.Items) == 0 || len(result.Items) < result.Accepted {
				t.Fatalf("accept omitted persisted items: %s", w.Body.String())
			}
			return result.Items
		}
		return nil
	}
	if initial := readMappings(); initial.Suggestions != 0 {
		t.Fatalf("unreviewed sources appeared as suggestions: %+v", initial)
	}
	// A migrated enabled connector has no activation evidence for its target.
	// Even an otherwise valid binding shape cannot turn it into a suggestion.
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
	discovery := request(http.MethodPost, "/service-mappings/discover", "")
	if discovery.Code != 200 || !strings.Contains(discovery.Body.String(), `"suggestions":2`) {
		t.Fatalf("discover status=%d body=%s", discovery.Code, discovery.Body.String())
	}
	var discovered struct {
		Suggestions int                      `json:"suggestions"`
		SourceState string                   `json:"source_state"`
		Candidates  []mappingCandidateReview `json:"candidates"`
	}
	if err := json.Unmarshal(discovery.Body.Bytes(), &discovered); err != nil ||
		discovered.SourceState != "configured" || discovered.Suggestions != 2 || len(discovered.Candidates) != 2 {
		t.Fatalf("discovery omitted reviewable candidates: %s err=%v", discovery.Body.String(), err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_service_mapping_candidates WHERE accepted_at IS NULL`).Scan(&pendingCount); err != nil || pendingCount != 2 {
		t.Fatalf("candidate persistence count=%d err=%v", pendingCount, err)
	}
	view := readMappings()
	if view.Suggestions != 2 || len(view.Rows) != 2 {
		t.Fatalf("configured candidates not projected: %+v", view)
	}
	for _, row := range view.Rows {
		if row.Cells["code"].State != "suggested" || row.Cells["code"].Brand != "gitlab" ||
			row.Cells["code"].Candidate == nil || row.Cells["code"].Candidate.Version == "" {
			t.Fatalf("candidate was asserted as a current binding: %+v", row)
		}
	}
	checkout := selected("checkout")
	other := selected("other")
	for _, candidate := range discovered.Candidates {
		if candidate.ID == "" || candidate.Version == "" || candidate.EnvironmentID != "dev" ||
			candidate.SystemID != "code" || candidate.ConnectorID != "gitlab-1" ||
			candidate.TargetID == "" || candidate.ExternalID == "" || candidate.BindingRevision != 1 ||
			candidate.ExpiresAt.Before(time.Now()) {
			t.Fatalf("incomplete discovery candidate: %+v", candidate)
		}
		projected := selected(candidate.Service)
		if projected.ID != candidate.ID || projected.Version != candidate.Version || projected.BindingRevision != candidate.BindingRevision {
			t.Fatalf("GET mapping candidate differs from discover: discover=%+v projected=%+v", candidate, projected)
		}
	}
	readAccepted(`{"cells":["checkout:code"]}`, 422, 0)
	readAccepted(`{"candidates":[]}`, 422, 0)
	// Rediscovery keeps the row identity while advancing its review version.
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 {
		t.Fatalf("rediscover status=%d body=%s", w.Code, w.Body.String())
	}
	newCheckout := selected("checkout")
	if newCheckout.ID != checkout.ID || newCheckout.Version == checkout.Version {
		t.Fatalf("rediscovery failed to version same candidate: before=%+v after=%+v", checkout, newCheckout)
	}
	readAccepted(acceptBody(checkout), 409, 0)
	checkout, other = newCheckout, selected("other")
	// A batch containing one stale binding revision must write neither item.
	if _, err := db.Exec(ctx, `UPDATE application_environments
	 SET binding_revision=binding_revision+1 WHERE id='env-other'`); err != nil {
		t.Fatal(err)
	}
	readAccepted(acceptBody(checkout, other), 409, 0)
	var preAcceptAudit int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&preAcceptAudit); err != nil || preAcceptAudit != 0 {
		t.Fatalf("stale batch wrote audit: count=%d err=%v", preAcceptAudit, err)
	}
	var atomicBindings []byte
	if err := db.QueryRow(ctx, `SELECT bindings FROM application_environments WHERE id='env-checkout'`).Scan(&atomicBindings); err != nil || string(atomicBindings) != "[]" {
		t.Fatalf("stale batch persisted an earlier item: bindings=%s err=%v", atomicBindings, err)
	}
	if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 {
		t.Fatalf("refresh status=%d body=%s", w.Code, w.Body.String())
	}
	checkout, other = selected("checkout"), selected("other")
	wrongService := checkout
	wrongService.ServiceID = other.ServiceID
	readAccepted(acceptBody(wrongService), 409, 0)
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
		strings.NewReader(acceptBody(checkout))).WithContext(quickCtx)
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
	readAccepted(acceptBody(checkout), 403, 0)
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','connectors','manage','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET last_checked_at=now()-interval '20 minutes'
 WHERE id='gitlab-1'`); err != nil {
		t.Fatal(err)
	}
	readAccepted(acceptBody(checkout), 409, 0)
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET last_checked_at=now() WHERE id='gitlab-1'`); err != nil {
		t.Fatal(err)
	}
	acceptedItems := readAccepted(acceptBody(checkout), 200, 1)
	if len(acceptedItems) != 1 || acceptedItems[0].CandidateID != checkout.ID ||
		acceptedItems[0].ServiceID != checkout.ServiceID || acceptedItems[0].EnvironmentID != "dev" ||
		acceptedItems[0].SystemID != "code" || acceptedItems[0].ConnectorID != "gitlab-1" ||
		acceptedItems[0].TargetID != "target-checkout" || acceptedItems[0].ExternalID != "123" ||
		acceptedItems[0].BindingRevision != 2 || acceptedItems[0].Status != "accepted" ||
		acceptedItems[0].Binding != (core.Binding{Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "123"}) {
		t.Fatalf("accept response is not exact persisted binding: %+v", acceptedItems)
	}
	repeatItems := readAccepted(acceptBody(checkout), 200, 0)
	if len(repeatItems) != 1 || repeatItems[0].Status != "already_accepted" || repeatItems[0].BindingRevision != 2 {
		t.Fatalf("exact repeat response changed: %+v", repeatItems)
	}
	staleRepeat := checkout
	staleRepeat.Version = mappingVersion(time.Now().UTC())
	readAccepted(acceptBody(staleRepeat), 409, 0)
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
	var auditedCandidate, auditedProject string
	if err := db.QueryRow(ctx, `SELECT changes->>'candidate_id',changes->>'project_id'
	 FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&auditedCandidate, &auditedProject); err != nil ||
		auditedCandidate != checkout.ID || auditedProject != "123" {
		t.Fatalf("audit lost accepted identity: candidate=%s project=%s err=%v", auditedCandidate, auditedProject, err)
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
	checkout = selected("checkout")
	other = selected("other")
	readAccepted(acceptBody(checkout), 200, 1)
	readAccepted(acceptBody(checkout), 200, 0)
	if err := db.QueryRow(ctx, `SELECT binding_revision FROM application_environments WHERE id='env-checkout'`).Scan(&revision); err != nil || revision != 4 {
		t.Fatalf("rediscovery did not advance binding revision once: revision=%d err=%v", revision, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("rediscovery audit count=%d err=%v", audits, err)
	}
	// A second GitLab project invalidates the reviewed candidate.
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('target-other-2','app-other','dev','gitlab-1','project','789',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	readAccepted(acceptBody(other), 409, 0)
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
	readAccepted(acceptBody(other), 409, 0)
	if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='target-other'`); err != nil {
		t.Fatal(err)
	}
	missingSource := request(http.MethodPost, "/service-mappings/accept", acceptBody(other))
	if missingSource.Code != 409 || !strings.Contains(missingSource.Body.String(), `"source_unavailable"`) {
		t.Fatalf("missing source did not report unavailable: status=%d body=%s", missingSource.Code, missingSource.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT bindings FROM application_environments WHERE id='env-other'`).Scan(&raw); err != nil || string(raw) != "[]" {
		t.Fatalf("stale or ambiguous source mutated mapping: bindings=%s err=%v", raw, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.accepted'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("failed acceptance wrote audit: count=%d err=%v", audits, err)
	}
	// Batch and single-candidate accepts share one connector. Locking all
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
	concurrentA, concurrentB := selected("concurrent-a"), selected("concurrent-b")
	for _, body := range []string{
		acceptBody(concurrentA, concurrentB),
		acceptBody(concurrentB),
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
	t.Run("discover locks service before expired candidate", func(t *testing.T) {
		if _, err := db.Exec(ctx, `UPDATE application_environments
		 SET bindings='[]',binding_revision=binding_revision+1
		 WHERE id='env-concurrent-a'`); err != nil {
			t.Fatal(err)
		}
		if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 {
			t.Fatalf("prepare candidate status=%d body=%s", w.Code, w.Body.String())
		}
		if _, err := db.Exec(ctx, `UPDATE v3_service_mapping_candidates
		 SET expires_at=now()-interval '1 second'
		 WHERE application_id='app-concurrent-a' AND accepted_at IS NULL`); err != nil {
			t.Fatal(err)
		}
		serviceLock, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer serviceLock.Rollback(context.Background())
		var appID string
		if err := serviceLock.QueryRow(ctx, `SELECT a.id FROM applications a
		 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='dev'
		 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id
		 WHERE a.id='app-concurrent-a' FOR UPDATE OF a,ve,e`).Scan(&appID); err != nil {
			t.Fatal(err)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- request(http.MethodPost, "/service-mappings/discover", "") }()
		deadline := time.Now().Add(3 * time.Second)
		waiting := false
		for time.Now().Before(deadline) {
			if err := adminDB.QueryRow(ctx, `SELECT EXISTS (
			 SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
			 AND state='active' AND wait_event_type='Lock'
			 AND query LIKE '%FOR UPDATE OF a,ve,e%'
			 AND query LIKE '%COALESCE(a.owner_team_id%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !waiting {
			t.Fatal("discover did not reach the service lock")
		}
		candidateProbe, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var candidateID string
		probeErr := candidateProbe.QueryRow(ctx, `SELECT id FROM v3_service_mapping_candidates
		 WHERE application_id='app-concurrent-a' AND accepted_at IS NULL
		 FOR UPDATE NOWAIT`).Scan(&candidateID)
		_ = candidateProbe.Rollback(ctx)
		if probeErr != nil || candidateID == "" {
			t.Fatalf("discover locked an expired candidate before its service: id=%s err=%v", candidateID, probeErr)
		}
		if _, err := serviceLock.Exec(ctx, `UPDATE application_environments
		 SET bindings='[{"kind":"gitlab","connector_id":"gitlab-1","resource":"1001"}]',
		 binding_revision=binding_revision+1 WHERE id='env-concurrent-a'`); err != nil {
			t.Fatal(err)
		}
		if err := serviceLock.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case response := <-result:
			var refreshed struct {
				Candidates []mappingCandidateReview `json:"candidates"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &refreshed); response.Code != 200 || err != nil {
				t.Fatalf("discovery after service unlock: %d %s", response.Code, response.Body.String())
			}
			for _, candidate := range refreshed.Candidates {
				if candidate.ServiceID == "app-concurrent-a" {
					t.Fatalf("discover offered candidate after concurrent binding change: %+v", candidate)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("discovery did not finish after service unlock")
		}
	})
	t.Run("discover rechecks connector grant after service lock", func(t *testing.T) {
		if _, err := db.Exec(ctx, `UPDATE application_environments
		 SET bindings='[]',binding_revision=binding_revision+1 WHERE id='env-concurrent-b'`); err != nil {
			t.Fatal(err)
		}
		if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 {
			t.Fatalf("prepare candidate status=%d body=%s", w.Code, w.Body.String())
		}
		var before time.Time
		if err := db.QueryRow(ctx, `SELECT discovered_at FROM v3_service_mapping_candidates
		 WHERE application_id='app-concurrent-b' AND accepted_at IS NULL`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		serviceLock, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer serviceLock.Rollback(context.Background())
		var appID string
		if err := serviceLock.QueryRow(ctx, `SELECT a.id FROM applications a
		 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='dev'
		 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id
		 WHERE a.id='app-concurrent-b' FOR UPDATE OF a,ve,e`).Scan(&appID); err != nil {
			t.Fatal(err)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- request(http.MethodPost, "/service-mappings/discover", "") }()
		deadline := time.Now().Add(3 * time.Second)
		waiting := false
		for time.Now().Before(deadline) {
			if err := adminDB.QueryRow(ctx, `SELECT EXISTS (
			 SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
			 AND state='active' AND wait_event_type='Lock'
			 AND query LIKE '%FOR UPDATE OF a,ve,e%'
			 AND query LIKE '%COALESCE(a.owner_team_id%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !waiting {
			t.Fatal("discover did not reach the service lock")
		}
		if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants
		 WHERE role_id='admin' AND module='connectors' AND action='manage'`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := db.Exec(context.Background(), `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
			 VALUES('admin','connectors','manage','global','*') ON CONFLICT DO NOTHING`); err != nil {
				t.Error(err)
			}
		}()
		if err := serviceLock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case response := <-result:
			if response.Code != 403 {
				t.Fatalf("revoked discover status=%d body=%s", response.Code, response.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("revoked discovery did not finish")
		}
		var after time.Time
		if err := db.QueryRow(ctx, `SELECT discovered_at FROM v3_service_mapping_candidates
		 WHERE application_id='app-concurrent-b' AND accepted_at IS NULL`).Scan(&after); err != nil || !after.Equal(before) {
			t.Fatalf("revoked discover refreshed candidate: before=%s after=%s err=%v", before, after, err)
		}
	})
	t.Run("accept rejects candidate expired during service lock wait", func(t *testing.T) {
		if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 {
			t.Fatalf("prepare candidate status=%d body=%s", w.Code, w.Body.String())
		}
		candidate := selected("concurrent-b")
		serviceLock, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer serviceLock.Rollback(context.Background())
		var lockerPID int32
		if err := serviceLock.QueryRow(ctx, `SELECT pg_backend_pid() FROM applications a
 JOIN application_environments e ON e.application_id=a.id AND e.key='dev'
 WHERE a.id='app-concurrent-b' FOR UPDATE OF a,e`).Scan(&lockerPID); err != nil {
			t.Fatal(err)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- request(http.MethodPost, "/service-mappings/accept", acceptBody(candidate)) }()
		waitForMappingServiceLock(t, adminDB, lockerPID)
		// The request transaction started before this expiry, so now() would
		// still consider the candidate live after the service lock releases.
		if _, err := db.Exec(ctx, `UPDATE v3_service_mapping_candidates
 SET expires_at=clock_timestamp() WHERE id=$1`, candidate.ID); err != nil {
			t.Fatal(err)
		}
		if err := serviceLock.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case response := <-result:
			if response.Code != 409 || !strings.Contains(response.Body.String(), `"candidate_unavailable"`) {
				t.Fatalf("expired accept status=%d body=%s", response.Code, response.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("expired accept did not finish")
		}
		var raw []byte
		if err := db.QueryRow(ctx, `SELECT bindings FROM application_environments WHERE id='env-concurrent-b'`).Scan(&raw); err != nil || string(raw) != "[]" {
			t.Fatalf("expired accept changed bindings=%s err=%v", raw, err)
		}
	})
	for _, method := range []string{"discover", "accept"} {
		t.Run(method+" rejects revoked session during service lock wait", func(t *testing.T) {
			if w := request(http.MethodPost, "/service-mappings/discover", ""); w.Code != 200 {
				t.Fatalf("prepare candidate status=%d body=%s", w.Code, w.Body.String())
			}
			candidate := selected("concurrent-b")
			var before time.Time
			if err := db.QueryRow(ctx, `SELECT discovered_at FROM v3_service_mapping_candidates WHERE id=$1`, candidate.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			_, raceToken, err := st.Login(ctx, "admin@example.invalid", "isolated-mapping-password")
			if err != nil {
				t.Fatal(err)
			}
			serviceLock, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer serviceLock.Rollback(context.Background())
			var lockerPID int32
			if err := serviceLock.QueryRow(ctx, `SELECT pg_backend_pid() FROM applications a
 JOIN application_environments e ON e.application_id=a.id AND e.key='dev'
 WHERE a.id='app-concurrent-b' FOR UPDATE OF a,e`).Scan(&lockerPID); err != nil {
				t.Fatal(err)
			}
			path, body := "/service-mappings/discover", ""
			if method == "accept" {
				path, body = "/service-mappings/accept", acceptBody(candidate)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest(http.MethodPost, "/api/v1"+path, strings.NewReader(body))
				r.Header.Set("X-AIOps-Environment", "dev")
				if body != "" {
					r.Header.Set("Content-Type", "application/json")
				}
				r.AddCookie(&http.Cookie{Name: "aiops_session", Value: raceToken})
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				result <- w
			}()
			waitForMappingServiceLock(t, adminDB, lockerPID)
			if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(raceToken)); err != nil {
				t.Fatal(err)
			}
			if err := serviceLock.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-result:
				if response.Code != 403 {
					t.Fatalf("revoked %s status=%d body=%s", method, response.Code, response.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("revoked mapping write did not finish")
			}
			var after time.Time
			if err := db.QueryRow(ctx, `SELECT discovered_at FROM v3_service_mapping_candidates WHERE id=$1`, candidate.ID).Scan(&after); err != nil || !after.Equal(before) {
				t.Fatalf("revoked %s refreshed candidate: before=%s after=%s err=%v", method, before, after, err)
			}
			var raw []byte
			if err := db.QueryRow(ctx, `SELECT bindings FROM application_environments WHERE id='env-concurrent-b'`).Scan(&raw); err != nil || string(raw) != "[]" {
				t.Fatalf("revoked %s changed bindings=%s err=%v", method, raw, err)
			}
		})
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE environment_id='dev'`); err != nil {
		t.Fatal(err)
	}
	unconfigured := request(http.MethodPost, "/service-mappings/discover", "")
	var emptySource struct {
		SourceState string                   `json:"source_state"`
		Suggestions int                      `json:"suggestions"`
		Candidates  []mappingCandidateReview `json:"candidates"`
	}
	if err := json.Unmarshal(unconfigured.Body.Bytes(), &emptySource); unconfigured.Code != 200 || err != nil ||
		emptySource.SourceState != "not_configured" || emptySource.Suggestions != 0 || len(emptySource.Candidates) != 0 {
		t.Fatalf("unconfigured source response=%d %s err=%v", unconfigured.Code, unconfigured.Body.String(), err)
	}
}
