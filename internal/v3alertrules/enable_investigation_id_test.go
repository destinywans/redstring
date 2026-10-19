package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type enableIDSource struct{ reads, validations int }

func (s *enableIDSource) List(context.Context, string) ([]ObservedRule, error) {
	s.reads++
	return nil, nil
}
func (s *enableIDSource) ValidatePromQL(context.Context, string, string) error {
	s.validations++
	return nil
}

type enableIDRepository struct{ exports int }

func (*enableIDRepository) ProjectID() int64     { return 42 }
func (*enableIDRepository) BranchPrefix() string { return "aiops-alert-rule-drafts/" }
func (r *enableIDRepository) ExportDraft(_ context.Context, branch, _ string, _ []byte, _ string) (DraftMR, error) {
	r.exports++
	return DraftMR{IID: int64(r.exports), SourceProjectID: 42, TargetProjectID: 42,
		URL: "https://gitlab.example.invalid/draft/1", State: "opened", SourceBranch: branch}, nil
}

type enableIDFixture struct {
	db                    *pgxpool.Pool
	writer                *WriteService
	actor                 core.User
	session               string
	source                *enableIDSource
	repository            *enableIDRepository
	applier               *recoveryApplier
	prefixed, legacy      string
	hidden, stage, closed string
}

func newEnableIDFixture(t *testing.T) enableIDFixture {
	t.Helper()
	db := enableRecoveryDB(t)
	st := &store.Store{DB: db}
	if err := st.Bootstrap(t.Context(), "enable-id@example.invalid", "isolated-enable-id-password"); err != nil {
		t.Fatal(err)
	}
	actor, session, err := st.Login(t.Context(), "enable-id@example.invalid", "isolated-enable-id-password")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, app := range []string{"allowed", "hidden"} {
		exec(`INSERT INTO applications(id,key,name,environment) VALUES($1,$1,$1,'test')`, app)
		exec(`INSERT INTO application_environments(id,application_id,key,name) VALUES($1,$2,'test','Test'),($3,$2,'staging','Staging')`, app+"-test", app, app+"-staging")
		exec(`INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,'test'),($1,'staging')`, app)
	}
	f := enableIDFixture{db: db, actor: actor, session: session, source: &enableIDSource{}, repository: &enableIDRepository{},
		prefixed: "INV-" + store.ID(), legacy: store.ID(), hidden: "INV-" + store.ID(), stage: "INV-" + store.ID(), closed: "INV-" + store.ID()}
	for _, row := range []struct{ id, app, environment, status string }{
		{f.prefixed, "allowed", "allowed-test", "open"}, {f.legacy, "allowed", "allowed-test", "open"},
		{f.hidden, "hidden", "hidden-test", "open"}, {f.stage, "allowed", "allowed-staging", "open"},
		{f.closed, "allowed", "allowed-test", "closed"},
	} {
		exec(`INSERT INTO investigations(id,application_id,environment_id,title,status,created_by) VALUES($1,$2,$3,'ID boundary',$4,$5)`, row.id, row.app, row.environment, row.status, actor.ID)
	}
	policy := &rbac.V3Policy{Store: st}
	reader := mustService(t, DatabaseCatalog{DB: db}, policy, f.source,
		RuleGroupBinding{EnvironmentID: "test", ApplicationID: "allowed", File: testRuleFile, Group: "g"})
	f.writer = NewWriteService(db, policy, reader, f.repository)
	f.applier = &recoveryApplier{intent: AlertRuleEnableIntent{Deployment: AlertRuleDeploymentPlan{
		ProjectID: 42, TargetBranch: "main", Path: "rules/alerts.yml", BaseRevision: strings.Repeat("b", 40),
		ContentDigest: strings.Repeat("c", 64), ScopeFingerprint: strings.Repeat("d", 64)}}}
	f.writer.EnableApplier = f.applier
	f.writer.EnableReady = func(context.Context, string) error { return nil }
	return f
}

func (f enableIDFixture) request(t *testing.T, investigation, name string) *httptest.ResponseRecorder {
	t.Helper()
	return f.requestWithKey(t, investigation, name, store.ID())
}

func (f enableIDFixture) requestWithKey(t *testing.T, investigation, name, key string) *httptest.ResponseRecorder {
	t.Helper()
	in := createRequest{InvestigationID: investigation, DraftRuleSpec: DraftRuleSpec{
		ApplicationID: "allowed", EnvironmentID: "test", Group: "g", Name: name,
		Expr: "up == 0", For: "1m", Mode: "enabled",
		Labels: map[string]string{"application": "allowed", "environment": "test", "severity": "P2"}}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/alert-rules", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.session})
	w := httptest.NewRecorder()
	// Exercise the enabled handler including its real transactional policy and
	// immutable proposal persistence. Only provider/draft protocols are Mock.
	f.writer.handleEnable(w, r, "test", f.actor, key, in)
	return w
}

func TestEnableInvestigationIDFormatBoundaries(t *testing.T) {
	for _, id := range []string{strings.Repeat("a", 32), strings.Repeat("A", 32), "INV-" + strings.Repeat("0", 32), "INV-" + strings.Repeat("F", 32)} {
		if !validEnableInvestigationID(id) {
			t.Errorf("supported investigation format rejected: %q", id)
		}
		if strings.HasPrefix(id, "INV-") && hexID(id) {
			t.Error("INV compatibility must not change the proposal/execution validator")
		}
	}
	for _, id := range []string{"", "INV-", strings.Repeat("a", 31), strings.Repeat("a", 33),
		"INV-" + strings.Repeat("a", 31), "INV-" + strings.Repeat("a", 33), "INV-" + strings.Repeat("g", 32),
		"inv-" + strings.Repeat("a", 32), "INV-INV-" + strings.Repeat("a", 32), " INV-" + strings.Repeat("a", 32),
		"INV-" + strings.Repeat("a", 32) + "\n", "INV-" + strings.Repeat("a", 32) + "\x00", "INV-" + strings.Repeat("Ａ", 32)} {
		if validEnableInvestigationID(id) {
			t.Errorf("malformed investigation format accepted: %q", id)
		}
	}
}

func TestEnableInvestigationIDPreservesFormalAndLegacyIdentity(t *testing.T) {
	f := newEnableIDFixture(t)
	for i, id := range []string{f.prefixed, f.legacy} {
		t.Run(fmt.Sprintf("format_%d", i), func(t *testing.T) {
			w := f.request(t, id, fmt.Sprintf("EnabledRule%d", i))
			if w.Code != http.StatusAccepted {
				t.Fatalf("open scoped investigation %q: status=%d body=%s", id, w.Code, w.Body.String())
			}
			var proposalID, storedID, reviewID, actionID, auditID string
			var action json.RawMessage
			err := f.db.QueryRow(t.Context(), `SELECT p.id,p.investigation_id,p.content->>'investigation_id',a.investigation_id,a.action
 FROM v3_proposals p JOIN v3_proposal_actions a ON a.proposal_id=p.id
 WHERE p.investigation_id=$1`, id).Scan(&proposalID, &storedID, &reviewID, &actionID, &action)
			if err != nil {
				t.Fatal(err)
			}
			var intent AlertRuleEnableIntent
			if err := json.Unmarshal(action, &intent); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(t.Context(), `SELECT changes->>'investigation_id' FROM management_audit
 WHERE action='v3.alert_rule.enable_requested' AND target=$1`, proposalID).Scan(&auditID); err != nil {
				t.Fatal(err)
			}
			if storedID != id || reviewID != id || actionID != id || intent.InvestigationID != id || auditID != id {
				t.Fatal("proposal, immutable intent, review or audit normalized the original investigation ID")
			}
			if !hexID(proposalID) || hexID(f.prefixed) {
				t.Fatal("investigation compatibility broadened the proposal/execution hex ID contract")
			}
		})
	}
	if f.repository.exports != 0 || f.applier.planCalls.Load() != 2 || f.applier.prepareCalls.Load() != 0 ||
		f.applier.lookups.Load() != 0 || f.applier.observeCalls.Load() != 0 {
		t.Fatal("request must plan two proposals without a draft and never execute or approve them")
	}
}

// D11 decision 6: the request proposes the rule and plan directly, without a
// nondeployable draft MR, and a same-key retry returns the same proposal.
func TestEnableRequestWithoutDraft(t *testing.T) {
	f := newEnableIDFixture(t)
	key := "enable-" + store.ID()
	w := f.requestWithKey(t, f.prefixed, "DirectRule", key)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["draft_id"] != nil || body["version"] != nil || body["mr"] != nil || body["status"] != "pending_approval" || body["proposal_id"] == "" {
		t.Fatalf("response: %s", w.Body.String())
	}
	var action json.RawMessage
	var envelope int
	if err := f.db.QueryRow(t.Context(), `SELECT action,schema_version FROM v3_proposal_actions WHERE proposal_id=$1`, body["proposal_id"]).
		Scan(&action, &envelope); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(action, &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"draft_operation_id", "draft_id", "draft_version", "manifest_digest", "project_id", "mr_iid", "mr_url", "branch", "path"} {
		if _, ok := raw[field]; ok {
			t.Errorf("version 2 intent carries %s", field)
		}
	}
	var review string
	if err := f.db.QueryRow(t.Context(), `SELECT content::text FROM v3_proposals WHERE id=$1`, body["proposal_id"]).Scan(&review); err != nil ||
		!strings.Contains(review, "Scope labels: environment=test application=allowed") {
		t.Fatalf("review summary lacks the scope labels: %v %s", err, review)
	}
	if raw["schema_version"] != float64(2) || envelope != 1 || f.repository.exports != 0 {
		t.Fatalf("intent %v envelope %d exports %d", raw["schema_version"], envelope, f.repository.exports)
	}
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	intent, err := LoadAlertRuleEnableIntentWithTx(t.Context(), tx, body["proposal_id"].(string))
	_ = tx.Rollback(t.Context())
	if err != nil || intent.SchemaVersion != 2 || intent.Spec.Name != "DirectRule" || intent.Deployment.ProjectID != 42 {
		t.Fatalf("load: %+v %v", intent, err)
	}
	// Same key and body: the same proposal; another body: a conflict.
	again := f.requestWithKey(t, f.prefixed, "DirectRule", key)
	var replay map[string]any
	if again.Code != http.StatusAccepted || json.Unmarshal(again.Body.Bytes(), &replay) != nil || replay["proposal_id"] != body["proposal_id"] {
		t.Fatalf("replay: %d %s", again.Code, again.Body.String())
	}
	if other := f.requestWithKey(t, f.prefixed, "OtherRule", key); other.Code != http.StatusConflict {
		t.Fatalf("same key, other body: %d %s", other.Code, other.Body.String())
	}
	var proposals int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_proposals`).Scan(&proposals); err != nil || proposals != 1 {
		t.Fatalf("proposals: %d %v", proposals, err)
	}
	// The schema keeps version 2 free of draft fields (migration 176).
	tx, err = f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	copyID := store.ID()
	if _, err := tx.Exec(t.Context(), `INSERT INTO v3_proposals(id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 SELECT $2,investigation_id,application_id,environment_id,content,created_by,creator_kind FROM v3_proposals WHERE id=$1`, body["proposal_id"], copyID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO v3_proposal_actions(proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,
 request_hash,binding_fingerprint,creator_id,environment_id,application_id,investigation_id,investigation_updated_at,idempotency_key,source_kind)
 SELECT $2,action_kind,schema_version,action||'{"draft_id":"x"}'::jsonb,action_digest,review_content_digest,request_hash,binding_fingerprint,
 creator_id,environment_id,application_id,investigation_id,investigation_updated_at,'other-key',source_kind
 FROM v3_proposal_actions WHERE proposal_id=$1`, body["proposal_id"], copyID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "v3_alert_rule_enable_action_shape" {
		t.Fatalf("a version 2 intent with a draft field: %v", err)
	}
}

func TestEnableInvestigationIDRejectsMalformedAndOutOfScope(t *testing.T) {
	f := newEnableIDFixture(t)
	for _, tc := range []struct {
		name, id string
		status   int
	}{
		{"empty", "", 422}, {"short", "INV-" + strings.Repeat("a", 31), 422},
		{"long", "INV-" + strings.Repeat("a", 33), 422}, {"wrong_prefix", "inv-" + strings.Repeat("a", 32), 422},
		{"non_hex", "INV-" + strings.Repeat("z", 32), 422}, {"whitespace", " " + f.prefixed, 422},
		{"trailing_whitespace", f.prefixed + " ", 422}, {"double_prefix", "INV-" + f.prefixed, 422},
		{"path", "INV-../" + strings.Repeat("a", 32), 422}, {"nul", f.prefixed + "\x00", 422},
		{"unknown_formal", "INV-" + store.ID(), 404}, {"unknown_legacy", store.ID(), 404},
		// Only the naked legacy record exists: stripping INV- would select it.
		{"no_strip_alias", "INV-" + f.legacy, 404},
		{"other_application", f.hidden, 404}, {"other_environment", f.stage, 404}, {"closed", f.closed, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.request(t, tc.id, "DeniedRule")
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body.String(), tc.status)
			}
		})
	}
	st := &store.Store{DB: f.db}
	user, err := st.CreateUser(t.Context(), core.User{Email: "enable-denied@example.invalid", Name: "Denied", Role: "viewer"}, "isolated-denied-password")
	if err != nil {
		t.Fatal(err)
	}
	f.actor = user
	if w := f.request(t, f.prefixed, "DeniedRule"); w.Code != 403 {
		t.Fatalf("valid formal ID without change grants: status=%d body=%s", w.Code, w.Body.String())
	}
	if f.source.reads != 0 || f.repository.exports != 0 || f.applier.planCalls.Load() != 0 {
		t.Fatal("invalid or unauthorized investigation reached provider, draft export or deployment planning")
	}
	var proposals, operations, requested int
	if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM v3_proposals),
 (SELECT count(*) FROM v3_alert_rule_draft_operations),
 (SELECT count(*) FROM management_audit WHERE action='v3.alert_rule.enable_requested')`).Scan(&proposals, &operations, &requested); err != nil {
		t.Fatal(err)
	}
	if proposals != 0 || operations != 0 || requested != 0 {
		t.Fatal("rejected investigation left proposal, remote draft reservation or request audit")
	}
}
