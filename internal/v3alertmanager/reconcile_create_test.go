package v3alertmanager

import (
	"errors"
	"net/http"
	"sync"
	"testing"
)

func TestSilenceCreateReconciliationNeedsCompleteUniqueMatch(t *testing.T) {
	const firstID = "11111111-2222-3333-4444-555555555555"
	const secondID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	tests := []struct {
		name      string
		prepare   func(*testing.T, *DatabaseSilenceLedger, SilenceCreateOperation)
		snapshot  func(SilenceCreateOperation) []Silence
		wantState string
		wantErr   error
	}{
		{
			name:      "no operation marker",
			snapshot:  func(SilenceCreateOperation) []Silence { return []Silence{} },
			wantState: SilenceCreateUnknown, wantErr: ErrSilenceCreateUnresolved,
		},
		{
			name: "duplicate operation marker",
			snapshot: func(op SilenceCreateOperation) []Silence {
				first := observedSilenceForCreate(op, firstID)
				second := observedSilenceForCreate(op, secondID)
				second.Comment = "different upstream payload"
				return []Silence{first, second}
			},
			wantState: SilenceCreateManualReview, wantErr: ErrSilenceCreateNeedsReview,
		},
		{
			name: "unique operation marker with changed matcher",
			snapshot: func(op SilenceCreateOperation) []Silence {
				one := observedSilenceForCreate(op, firstID)
				one.Matchers[0].Value = "other-environment"
				return []Silence{one}
			},
			wantState: SilenceCreateManualReview, wantErr: ErrSilenceCreateNeedsReview,
		},
		{
			name: "acknowledged ID disagrees with snapshot",
			prepare: func(t *testing.T, ledger *DatabaseSilenceLedger, op SilenceCreateOperation) {
				t.Helper()
				if err := ledger.acknowledgeCreate(t.Context(), op.ID, firstID); err != nil {
					t.Fatal(err)
				}
			},
			snapshot: func(op SilenceCreateOperation) []Silence {
				return []Silence{observedSilenceForCreate(op, secondID)}
			},
			wantState: SilenceCreateManualReview, wantErr: ErrSilenceCreateNeedsReview,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, ledger, actor := testSilenceLedger(t)
			op, err := ledger.ReserveCreate(t.Context(), actor, ledgerAppInput("reconcile-key-001"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.beginCreate(t.Context(), actor, op.ID); err != nil {
				t.Fatal(err)
			}
			if tt.prepare != nil {
				tt.prepare(t, ledger, op)
			}
			client := testReconcileClient(t, http.StatusOK, tt.snapshot(op))
			result, err := ledger.reconcileCreate(t.Context(), op.ID, client)
			if !errors.Is(err, tt.wantErr) || result.State != tt.wantState {
				t.Fatalf("reconcile state=%q err=%v", result.State, err)
			}
			var persisted string
			if err := db.QueryRow(t.Context(), `SELECT state FROM v3_silence_create_operations WHERE id=$1`, op.ID).Scan(&persisted); err != nil || persisted != tt.wantState {
				t.Fatalf("persisted state=%q err=%v", persisted, err)
			}
			if tt.wantState == SilenceCreateManualReview {
				if _, err := ledger.reconcileCreate(t.Context(), op.ID, client); !errors.Is(err, ErrSilenceCreateNeedsReview) {
					t.Fatalf("manual state auto-resolved: %v", err)
				}
			}
			if _, err := ledger.beginCreate(t.Context(), actor, op.ID); !errors.Is(err, ErrSilenceCreateBusy) {
				t.Fatalf("unresolved intent redispatched: %v", err)
			}
		})
	}
}

func TestSilenceCreateReconciliationReadFailureStaysUnknown(t *testing.T) {
	db, ledger, actor := testSilenceLedger(t)
	op, err := ledger.ReserveCreate(t.Context(), actor, ledgerAppInput("read-failure-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.beginCreate(t.Context(), actor, op.ID); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{[]Silence(nil), map[string]any{"invalid": true}} {
		client := testReconcileClient(t, http.StatusOK, body)
		if _, err := ledger.reconcileCreate(t.Context(), op.ID, client); err == nil {
			t.Fatal("invalid complete snapshot accepted")
		}
	}
	client := testReconcileClient(t, http.StatusBadGateway, map[string]string{"error": "unavailable"})
	if _, err := ledger.reconcileCreate(t.Context(), op.ID, client); err == nil {
		t.Fatal("upstream failure accepted")
	}
	var state string
	if err := db.QueryRow(t.Context(), `SELECT state FROM v3_silence_create_operations WHERE id=$1`, op.ID).Scan(&state); err != nil || state != SilenceCreateUnknown {
		t.Fatalf("state=%q err=%v", state, err)
	}
}

func TestSilenceCreateReconciliationConcurrentAndAuditRollback(t *testing.T) {
	const providerID = "11111111-2222-3333-4444-555555555555"
	db, ledger, actor := testSilenceLedger(t)
	op, err := ledger.ReserveCreate(t.Context(), actor, ledgerAppInput("concurrent-reconcile-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.beginCreate(t.Context(), actor, op.ID); err != nil {
		t.Fatal(err)
	}
	client := testReconcileClient(t, http.StatusOK, []Silence{observedSilenceForCreate(op, providerID)})
	var wg sync.WaitGroup
	results := make([]SilenceCreateOperation, 2)
	errorsFound := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errorsFound[index] = ledger.reconcileCreate(t.Context(), op.ID, client)
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errorsFound[i] != nil || results[i].State != SilenceCreateConfirmed || results[i].UpstreamID != providerID {
			t.Fatalf("reconciler %d state=%+v err=%v", i, results[i], errorsFound[i])
		}
	}
	var confirmedAudits int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit
 WHERE target=$1 AND action='v3.silence_create.confirmed'`, op.ID).Scan(&confirmedAudits); err != nil || confirmedAudits != 1 {
		t.Fatalf("confirmed audits=%d err=%v", confirmedAudits, err)
	}

	second, err := ledger.ReserveCreate(t.Context(), actor, ledgerAppInput("manual-audit-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.beginCreate(t.Context(), actor, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `CREATE FUNCTION reject_manual_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.silence_create.manual_review' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_manual_review_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_manual_review_audit()`); err != nil {
		t.Fatal(err)
	}
	first := observedSilenceForCreate(second, providerID)
	duplicate := observedSilenceForCreate(second, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	client = testReconcileClient(t, http.StatusOK, []Silence{first, duplicate})
	if _, err = ledger.reconcileCreate(t.Context(), second.ID, client); err == nil {
		t.Fatal("manual review state committed without audit")
	}
	var state string
	if err = db.QueryRow(t.Context(), `SELECT state FROM v3_silence_create_operations WHERE id=$1`, second.ID).Scan(&state); err != nil || state != SilenceCreateDispatching {
		t.Fatalf("audit rollback state=%q err=%v", state, err)
	}
}
