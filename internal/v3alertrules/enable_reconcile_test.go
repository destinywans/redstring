package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// These tests use only a random PostgreSQL schema and in-memory adapters.
// No product connection configuration or real GitLab/Argo endpoint is read.
func enableRecoveryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "enable_recovery_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.MaxConns = 4
	db, err = pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&store.Store{DB: db}).Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

type recoveryAccess struct{ denied bool }

func (a *recoveryAccess) AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error {
	if a.denied {
		return store.ErrForbidden
	}
	return nil
}

type recoveryApproval struct {
	approval AlertRuleApproval
	calls    atomic.Int32
	rejectAt int32
}

func (a *recoveryApproval) VerifyWithTx(ctx context.Context, tx pgx.Tx, id, digest string) (AlertRuleApproval, error) {
	n := a.calls.Add(1)
	var live bool
	if err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() AND NOT EXISTS
 (SELECT 1 FROM v3_proposal_approval_revocations WHERE proposal_id=$1)
 FROM v3_proposal_approval_decisions WHERE proposal_id=$1`, id).Scan(&live); err != nil {
		return AlertRuleApproval{}, err
	}
	if !live || a.rejectAt > 0 && n >= a.rejectAt || digest != a.approval.ActionDigest {
		return AlertRuleApproval{}, problem(409, "approval_unavailable", "Approval expired or revoked")
	}
	return a.approval, nil
}

type recoveryApplier struct {
	intent                           AlertRuleEnableIntent
	id                               string
	receipt                          AlertRuleDeploymentReceipt
	found                            bool
	lookupErr                        error
	lookupHook                       func()
	prepareErr                       error
	observation                      AlertRuleDeploymentObservation
	planCalls, prepareCalls, lookups atomic.Int32
	observeCalls                     atomic.Int32
	identityMismatch                 atomic.Bool
}

func (a *recoveryApplier) Plan(context.Context, AlertRuleEnableIntent) (AlertRuleDeploymentPlan, error) {
	a.planCalls.Add(1)
	return a.intent.Deployment, nil
}
func (a *recoveryApplier) Prepare(_ context.Context, in AlertRuleEnableIntent, _ AlertRuleApproval, id string) (AlertRuleDeploymentReceipt, error) {
	a.prepareCalls.Add(1)
	a.check(in, id)
	return a.receipt, a.prepareErr
}
func (a *recoveryApplier) Lookup(_ context.Context, in AlertRuleEnableIntent, id string) (AlertRuleDeploymentReceipt, bool, error) {
	a.lookups.Add(1)
	a.check(in, id)
	if a.lookupHook != nil {
		a.lookupHook()
	}
	return a.receipt, a.found, a.lookupErr
}
func (a *recoveryApplier) Observe(context.Context, AlertRuleEnableIntent, AlertRuleDeploymentReceipt) (AlertRuleDeploymentObservation, error) {
	a.observeCalls.Add(1)
	return a.observation, nil
}
func (a *recoveryApplier) check(in AlertRuleEnableIntent, id string) {
	if id != a.id || !reflect.DeepEqual(in, a.intent) {
		a.identityMismatch.Store(true)
	}
}

type recoveryFixture struct {
	db        *pgxpool.Pool
	s         *WriteService
	id        string
	execution string
	intent    AlertRuleEnableIntent
	authority *recoveryApproval
	applier   *recoveryApplier
	access    *recoveryAccess
}

func seedEnableRecovery(t *testing.T, db *pgxpool.Pool, expired, revoked bool, state string) recoveryFixture {
	t.Helper()
	return seedEnableRecoveryPlan(t, db, expired, revoked, state, nil)
}

// seedEnableRecoveryPlan lets prepare change the seeded intent, e.g. make it
// a version 2 intent with a real adapter's plan (D11).
func seedEnableRecoveryPlan(t *testing.T, db *pgxpool.Pool, expired, revoked bool, state string,
	prepare func(*AlertRuleEnableIntent)) recoveryFixture {
	t.Helper()
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	id, execution, creator, first, second := store.ID(), store.ID(), store.ID(), store.ID(), store.ID()
	app, investigation, draft, op := store.ID(), store.ID(), store.ID(), store.ID()
	for _, u := range []string{creator, first, second} {
		exec(`INSERT INTO users(id,email,name,role,password_hash) VALUES($1,$2,'Recovery test','admin','unused')`, u, u+"@example.invalid")
	}
	exec(`INSERT INTO applications(id,key,name,environment) VALUES($1,$1,'Recovery test','test')`, app)
	exec(`INSERT INTO application_environments(id,application_id,key,name) VALUES($1,$2,'test','Test')`, app+"-env", app)
	exec(`INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,'test')`, app)
	now := time.Now().UTC().Truncate(time.Microsecond)
	exec(`INSERT INTO investigations(id,application_id,environment_id,title,updated_at) VALUES($1,$2,$3,'Recovery test',$4)`, investigation, app, app+"-env", now)
	spec := DraftRuleSpec{ApplicationID: app, EnvironmentID: "test", Group: "g", Name: "RecoveredRule", Expr: "up == 0", For: "1m", Mode: "draft",
		Labels: map[string]string{"application": app, "environment": "test", "severity": "P2"}, Annotations: map[string]string{"summary": "test"}}
	hash, specRaw, err := specDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newManifest(op, "test", app, []draftManifestRule{{DraftID: draft, Version: 1, ProviderFileIdentityDigest: fileIdentityDigest(testRuleFile), Spec: spec}})
	if err != nil {
		t.Fatal(err)
	}
	branch, artifactPath := "alert-rule-drafts/"+op, "ops-config/alert-rule-drafts/"+op+".json"
	exec(`INSERT INTO v3_alert_rule_drafts(id,environment_id,application_id,provider_file,provider_group,alert_name,current_version,created_by)
 VALUES($1,'test',$2,$3,'g',$4,1,$5)`, draft, app, testRuleFile, spec.Name, creator)
	exec(`INSERT INTO v3_alert_rule_draft_operations(id,actor_id,operation,idempotency_key,request_digest,environment_id,application_id,payload,branch,path,project_id,state,mr_iid,mr_url)
 VALUES($1,$2,'enable',$1,$3,'test',$4,$5::jsonb,$6,$7,42,'pending_review',1,'https://gitlab.example.invalid/draft/1')`, op, creator, strings.Repeat("a", 64), app, manifest, branch, artifactPath)
	exec(`INSERT INTO v3_alert_rule_draft_revisions(draft_id,version,operation_id,spec,spec_digest,actor_id) VALUES($1,1,$2,$3::jsonb,$4,$5)`, draft, op, specRaw, hash, creator)
	// Match PostgreSQL's immutable payload representation, as the executor does.
	if err := db.QueryRow(ctx, `SELECT payload FROM v3_alert_rule_draft_operations WHERE id=$1`, op).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	intent := AlertRuleEnableIntent{Kind: alertRuleEnableKind, SchemaVersion: 1, InvestigationID: investigation, InvestigationUpdatedAt: now,
		DraftOperationID: op, DraftID: draft, DraftVersion: 1, EnvironmentID: "test", ApplicationID: app,
		ProviderFileIdentityDigest: fileIdentityDigest(testRuleFile), SpecDigest: hash,
		ManifestDigest: enableDigest("aiops.v3.alert_rule.manifest.v1", manifest), ProjectID: 42, MR: 1,
		MRURL: "https://gitlab.example.invalid/draft/1", Branch: branch, Path: artifactPath, Spec: spec,
		Deployment: AlertRuleDeploymentPlan{ProjectID: 42, TargetBranch: "main", Path: "rules/alerts.yml", BaseRevision: strings.Repeat("b", 40), ContentDigest: strings.Repeat("c", 64), ScopeFingerprint: strings.Repeat("d", 64)}}
	if prepare != nil {
		prepare(&intent)
	}
	marshalCanonical := func(v any) []byte {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw, err = canonicalEnableJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	actionRaw, reviewRaw := marshalCanonical(intent), marshalCanonical(enableReviewContent(intent, creator))
	actionDigest := enableDigest("aiops.v3.proposal.action.v1", actionRaw)
	reviewDigest := enableDigest("aiops.v3.proposal.review.v1", reviewRaw)
	exec(`INSERT INTO v3_proposals(id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 VALUES($1,$2,$3,'test',$4::jsonb,$5,'user')`, id, investigation, app, reviewRaw, creator)
	exec(`INSERT INTO v3_proposal_actions(proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,request_hash,binding_fingerprint,
 creator_id,environment_id,application_id,investigation_id,investigation_updated_at,idempotency_key,source_kind)
 VALUES($1,$2,1,$3::jsonb,$4,$5,$4,$4,$6,'test',$7,$8,$9,$1,'human_request')`, id, alertRuleEnableKind, actionRaw, actionDigest, reviewDigest, creator, app, investigation, now)
	approvedAt, expiresAt := now.Add(-time.Minute), now.Add(10*time.Minute)
	if expired {
		approvedAt, expiresAt = now.Add(-10*time.Minute), now.Add(-time.Minute)
	}
	votes := []string{store.ID(), store.ID()}
	for i, u := range []string{first, second} {
		credential, handle, challenge := []byte(u), []byte(u), store.ID()
		exec(`INSERT INTO v3_passkey_accounts(instance_id,rp_id,user_id,user_handle) VALUES('recovery-test','example.invalid',$1,$2)`, u, handle)
		exec(`INSERT INTO v3_passkey_credentials(instance_id,rp_id,user_id,credential_id,credential_json) VALUES('recovery-test','example.invalid',$1,$2,'{}')`, u, credential)
		exec(`INSERT INTO v3_passkey_challenges(id,instance_id,rp_id,user_id,session_hash,tier,environment_id,kind,session_data,created_at,expires_at,consumed_at,proposal_id,action_kind,action_digest,review_content_digest)
 VALUES($1,'recovery-test','example.invalid',$2,$3,'nonprod','test','proposal_approve','{}',$4::timestamptz,$4::timestamptz+interval '9 minutes',$4::timestamptz+interval '1 second',$5,$6,$7,$8)`,
			challenge, u, strings.Repeat("a", 64), approvedAt, id, alertRuleEnableKind, actionDigest, reviewDigest)
		exec(`INSERT INTO v3_proposal_approval_votes(id,proposal_id,approver_id,challenge_id,action_kind,action_digest,review_content_digest,instance_id,rp_id,credential_id,credential_fingerprint,request_hash,verified_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,'recovery-test','example.invalid',$8,$9,$6,$10,$11)`, votes[i], id, u, challenge, alertRuleEnableKind, actionDigest, reviewDigest, credential, strings.Repeat("a", 16), approvedAt, expiresAt)
	}
	approvalDigest := strings.Repeat("e", 64)
	exec(`INSERT INTO v3_proposal_approval_decisions(proposal_id,execution_id,action_kind,action_digest,review_content_digest,approval_digest,creator_id,environment_id,application_id,risk,required_count,first_vote_id,second_vote_id,approved_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,'test',$8,'high',2,$9,$10,$11,$12)`, id, execution, alertRuleEnableKind, actionDigest, reviewDigest, approvalDigest, creator, app, votes[0], votes[1], approvedAt, expiresAt)
	exec(`UPDATE v3_proposals SET status='approved' WHERE id=$1`, id)
	if revoked {
		exec(`INSERT INTO v3_proposal_approval_revocations(proposal_id,revoked_by,reason) VALUES($1,$2,'Test revocation')`, id, second)
	}
	receipt := AlertRuleDeploymentReceipt{ProjectID: 42, MR: 7, URL: "https://gitlab.example.invalid/mr/7", SourceBranch: "aiops-alert-rule-enable/" + execution,
		TargetBranch: intent.Deployment.TargetBranch, Path: intent.Deployment.Path, ContentDigest: intent.Deployment.ContentDigest, State: "opened"}
	if state != "" {
		deployment := []byte(`{}`)
		if state == "pending_review" || state == "awaiting_provider" || state == "enabled" {
			deployment = marshalCanonical(receipt)
		}
		exec(`INSERT INTO v3_alert_rule_enable_executions(proposal_id,execution_id,action_digest,approval_id,approval_digest,first_approver_id,approver_id,state,generation,lease_until,deployment,updated_at)
 VALUES($1,$2,$3,$2,$4,$5,$6,$7,3,clock_timestamp()-interval '1 minute',$8::jsonb,clock_timestamp()-interval '1 minute')`, id, execution, actionDigest, approvalDigest, first, second, state, deployment)
	}
	access := &recoveryAccess{}
	reader := mustService(t, fakeCatalog{{ID: app, EnvironmentID: "test"}}, fakeAccess{}, fakeSource{}, RuleGroupBinding{EnvironmentID: "test", ApplicationID: app, File: testRuleFile, Group: "g"})
	s := NewWriteService(db, access, reader, nil)
	authority := &recoveryApproval{approval: AlertRuleApproval{ID: execution, Digest: approvalDigest, ActionDigest: actionDigest,
		ApproverID: second, ApproverIDs: []string{first, second}, ApprovedAt: approvedAt, ExpiresAt: expiresAt}}
	applier := &recoveryApplier{intent: intent, id: execution, receipt: receipt, found: true, observation: AlertRuleDeploymentObservation{State: "pending_review"}}
	s.EnableApproval, s.EnableApplier = authority, applier
	return recoveryFixture{db: db, s: s, id: id, execution: execution, intent: intent, authority: authority, applier: applier, access: access}
}

func assertRecoveryOnly(t *testing.T, f recoveryFixture, lookups int32, observes ...int32) {
	t.Helper()
	var expectedObserves int32
	if len(observes) > 0 {
		expectedObserves = observes[0]
	}
	if f.applier.lookups.Load() != lookups || f.applier.prepareCalls.Load() != 0 || f.applier.planCalls.Load() != 0 || f.applier.observeCalls.Load() != expectedObserves || f.applier.identityMismatch.Load() {
		t.Fatalf("recovery crossed the remote write or identity boundary: lookup=%d plan=%d prepare=%d observe=%d mismatch=%v",
			f.applier.lookups.Load(), f.applier.planCalls.Load(), f.applier.prepareCalls.Load(), f.applier.observeCalls.Load(), f.applier.identityMismatch.Load())
	}
}

func TestEnableRecoveryInvalidApprovalNeverPrepares(t *testing.T) {
	db := enableRecoveryDB(t)
	for _, tc := range []struct {
		name, state, remote string
		revoked, absent     bool
	}{
		{"expired_crash_after_remote_success", "exporting", "opened", false, false},
		{"revoked_timeout", "uncertain", "opened", true, false},
		{"expired_reserved_absent", "reserved", "", false, true},
		{"expired_already_merged", "exporting", "merged", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := seedEnableRecovery(t, db, !tc.revoked, tc.revoked, tc.state)
			f.applier.found = !tc.absent
			f.applier.receipt.State = tc.remote
			result, err := f.s.ExecuteApproved(t.Context(), f.id)
			expected := "pending_review"
			if tc.absent {
				expected = "stale_remote"
			}
			if err != nil || result.Status != expected || result.ExecutionID != f.execution || result.ObservedRuleID != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			assertRecoveryOnly(t, f, 1)
			if f.authority.calls.Load() != 1 {
				t.Fatalf("recovery reverified current approval: calls=%d", f.authority.calls.Load())
			}
			if tc.absent && result.Deployment != nil || !tc.absent && (result.Deployment == nil || result.Deployment.State != tc.remote) {
				t.Fatalf("remote evidence lost or invented: %+v", result)
			}
			var generation int64
			var state string
			var lease *time.Time
			if err := db.QueryRow(t.Context(), `SELECT state,generation,lease_until FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, f.id).Scan(&state, &generation, &lease); err != nil {
				t.Fatal(err)
			}
			if state != expected || generation != 4 || lease != nil {
				t.Fatalf("state=%s generation=%d lease=%v", state, generation, lease)
			}
			var auditCount int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE target=$1 AND action IN
 ('v3.alert_rule.enable.reconcile','v3.alert_rule.enable.execution_state')`, f.id).Scan(&auditCount); err != nil || auditCount != 2 {
				t.Fatalf("reconciliation audit count=%d err=%v", auditCount, err)
			}
			// A direct retry cannot fall back to Prepare after recovery completed.
			if _, err := f.s.ExecuteApproved(t.Context(), f.id); (err != nil) != tc.absent {
				t.Fatalf("recovered retry err=%v absent=%v", err, tc.absent)
			}
			assertRecoveryOnly(t, f, 1)
		})
	}
}

func TestEnableRecoveryOutageLeaseAndHistoricalIdentity(t *testing.T) {
	db := enableRecoveryDB(t)
	t.Run("lookup_outage_retries_same_identity", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "uncertain")
		f.applier.lookupErr = errors.New("read timeout")
		if _, err := f.s.ExecuteApproved(t.Context(), f.id); err == nil {
			t.Fatal("lookup outage was reported as success")
		}
		var state string
		if err := db.QueryRow(t.Context(), `SELECT state FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, f.id).Scan(&state); err != nil || state != "uncertain" {
			t.Fatalf("state=%s err=%v", state, err)
		}
		f.applier.lookupErr = nil
		if result, err := f.s.ExecuteApproved(t.Context(), f.id); err != nil || result.ExecutionID != f.execution || result.Status != "pending_review" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		assertRecoveryOnly(t, f, 2)
	})
	t.Run("active_lease_blocks_recovery", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "exporting")
		if _, err := db.Exec(t.Context(), `UPDATE v3_alert_rule_enable_executions SET lease_until=clock_timestamp()+interval '1 minute' WHERE proposal_id=$1`, f.id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ExecuteApproved(t.Context(), f.id); err == nil {
			t.Fatal("active writer lease was stolen")
		}
		assertRecoveryOnly(t, f, 0)
	})
	t.Run("historical_execution_mismatch_fails_closed", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "uncertain")
		if _, err := db.Exec(t.Context(), `UPDATE v3_alert_rule_enable_executions SET execution_id=$2 WHERE proposal_id=$1`, f.id, store.ID()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ExecuteApproved(t.Context(), f.id); err == nil {
			t.Fatal("mismatching historical identity was accepted")
		}
		assertRecoveryOnly(t, f, 0)
	})
	t.Run("access_revoked_does_not_block_lookup", func(t *testing.T) {
		f := seedEnableRecovery(t, db, false, false, "uncertain")
		f.access.denied = true
		if result, err := f.s.ExecuteApproved(t.Context(), f.id); err != nil || result.Status != "pending_review" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if f.authority.calls.Load() != 0 {
			t.Fatal("recovery requires current approval authority")
		}
		assertRecoveryOnly(t, f, 1)
	})
	t.Run("untrusted_remote_identity_requires_review", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "uncertain")
		f.applier.receipt.SourceBranch = "aiops-alert-rule-enable/" + store.ID()
		if _, err := f.s.ExecuteApproved(t.Context(), f.id); err == nil {
			t.Fatal("a different remote execution was accepted")
		}
		var state string
		var raw []byte
		if err := db.QueryRow(t.Context(), `SELECT state,deployment FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, f.id).Scan(&state, &raw); err != nil || state != "stale_remote" || string(raw) != "{}" {
			t.Fatalf("untrusted remote result retained: state=%s deployment=%s err=%v", state, raw, err)
		}
		assertRecoveryOnly(t, f, 1)
	})
}

func TestEnableRecoveryWorkerAndApprovalRace(t *testing.T) {
	db := enableRecoveryDB(t)
	t.Run("expired_existing_selected_by_worker", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "exporting")
		if _, err := f.s.enableWorkerPass(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		assertRecoveryOnly(t, f, 1, 1)
	})
	t.Run("revoked_existing_selected_by_worker", func(t *testing.T) {
		f := seedEnableRecovery(t, db, false, true, "uncertain")
		if _, err := f.s.enableWorkerPass(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		assertRecoveryOnly(t, f, 1, 1)
	})
	t.Run("expired_unclaimed_never_allocates_identity", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "")
		if _, err := f.s.ExecuteApproved(t.Context(), f.id); err == nil {
			t.Fatal("expired approval without execution was accepted")
		}
		if _, err := f.s.enableWorkerPass(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, f.id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("new identity allocated after expiry: count=%d err=%v", count, err)
		}
		assertRecoveryOnly(t, f, 0)
	})
	t.Run("approval_disappears_during_claim", func(t *testing.T) {
		f := seedEnableRecovery(t, db, false, false, "uncertain")
		f.authority.rejectAt = 2
		if result, err := f.s.ExecuteApproved(t.Context(), f.id); err != nil || result.Status != "pending_review" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		assertRecoveryOnly(t, f, 1)
	})
}

func TestEnableRecoveryGenerationFencesLateResult(t *testing.T) {
	db := enableRecoveryDB(t)
	f := seedEnableRecovery(t, db, true, false, "exporting")
	entered, release := make(chan struct{}), make(chan struct{})
	f.applier.lookupHook = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() {
		_, err := f.s.ExecuteApproved(t.Context(), f.id)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("recovery did not reach lookup")
	}
	// Recovery owns generation 4, so a late generation-3 Prepare response
	// from before the crash cannot publish pending_review over the new claim.
	err := f.s.markEnableExecution(t.Context(), enableExecutionRecord{ProposalID: f.id, Generation: 3}, "pending_review", &f.applier.receipt, "late_prepare")
	if err == nil {
		close(release)
		<-done
		t.Fatal("old writer was not fenced")
	}
	// A second claim after this recovery lease expires must similarly fence
	// its late Lookup result, leaving the newer claimant's state untouched.
	if _, err := db.Exec(t.Context(), `UPDATE v3_alert_rule_enable_executions SET generation=generation+1 WHERE proposal_id=$1`, f.id); err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("late recovery result overwrote a newer generation")
	}
	var state string
	var generation int64
	if err := db.QueryRow(t.Context(), `SELECT state,generation FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, f.id).Scan(&state, &generation); err != nil || state != "exporting" || generation != 5 {
		t.Fatalf("state=%s generation=%d err=%v", state, generation, err)
	}
	assertRecoveryOnly(t, f, 1)
}

func TestEnableLiveApprovalAndPendingObservationUnchanged(t *testing.T) {
	db := enableRecoveryDB(t)
	t.Run("live_approval_prepares_once", func(t *testing.T) {
		f := seedEnableRecovery(t, db, false, false, "")
		result, err := f.s.ExecuteApproved(t.Context(), f.id)
		if err != nil || result.Status != "pending_review" || result.ExecutionID != f.execution || result.ObservedRuleID != "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if f.applier.prepareCalls.Load() != 1 || f.applier.lookups.Load() != 0 || f.applier.identityMismatch.Load() {
			t.Fatal("live approval no longer uses the normal Prepare path")
		}
	})
	t.Run("pending_review_remains_observable_after_expiry", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "pending_review")
		if _, err := f.s.enableWorkerPass(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		if f.applier.observeCalls.Load() != 1 || f.applier.prepareCalls.Load() != 0 || f.applier.lookups.Load() != 0 {
			t.Fatal("pending-review observation changed after expiry")
		}
	})
	t.Run("recovered_merged_requires_sync_and_exact_provider", func(t *testing.T) {
		f := seedEnableRecovery(t, db, true, false, "exporting")
		f.applier.receipt.State = "merged"
		f.applier.observation = AlertRuleDeploymentObservation{State: "waiting_sync"}
		if result, err := f.s.ExecuteApproved(t.Context(), f.id); err != nil || result.Status != "pending_review" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result, err := f.s.ObserveEnabled(t.Context(), f.id); err != nil || result.Status != "waiting_sync" || result.ObservedRuleID != "" {
			t.Fatalf("unsynced merge reported enabled: result=%+v err=%v", result, err)
		}
		revision := strings.Repeat("f", 40)
		f.applier.observation = AlertRuleDeploymentObservation{State: "synced", MergeRevision: revision, SyncedRevision: revision, ContentDigest: f.intent.Deployment.ContentDigest}
		if result, err := f.s.ObserveEnabled(t.Context(), f.id); err != nil || result.Status != "awaiting_provider" || result.ObservedRuleID != "" {
			t.Fatalf("sync without actual rule reported enabled: result=%+v err=%v", result, err)
		}
		spec := f.intent.Spec
		f.s.Reader.Source = fakeSource{rules: map[string][]ObservedRule{"test": {{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: spec.Name,
			Expression: spec.Expr, Duration: 60, Labels: spec.Labels, Annotations: spec.Annotations}}}}
		if result, err := f.s.ObserveEnabled(t.Context(), f.id); err != nil || result.Status != "enabled" || result.ObservedRuleID == "" || result.Deployment.State != "merged" {
			t.Fatalf("exact reviewed, synced provider rule not recovered: result=%+v err=%v", result, err)
		}
		assertRecoveryOnly(t, f, 1, 3)
	})
}
