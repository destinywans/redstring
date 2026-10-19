package v3mfa

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A real database lock makes the request wait while the injected clock crosses
// the validity boundary. No timing assumption about goroutine scheduling.
func waitAcrossMFALock(t *testing.T, f *mfaFixture, advance time.Duration, call func() error) error {
	t.Helper()
	cfg := f.pool.Config().Copy()
	name := "mfa_wait_" + store.ID()
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f.svc.db = pool
	lock, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(t.Context(), "SELECT pg_advisory_xact_lock(752018992)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- call() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, name).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("MFA call did not reach controlled lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.clock.Add(int64(advance / time.Second))
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("MFA call did not finish")
		return nil
	}
}

func TestMFALockWaitRejectsExpiredSession(t *testing.T) {
	f := mfaDatabase(t)
	e, _ := f.enable(t)
	f.clock.Add(30)
	code := f.code(t, e.Secret)
	err := waitAcrossMFALock(t, f, time.Hour+time.Second, func() error { _, err := f.svc.Verify(t.Context(), f.actor, code); return err })
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired waiting session accepted: %v", err)
	}
}
func TestMFALockWaitRejectsStaleFreshLogin(t *testing.T) {
	f := mfaDatabase(t)
	err := waitAcrossMFALock(t, f, 11*time.Minute, func() error { _, err := f.svc.BeginEnrollment(t.Context(), f.actor); return err })
	if !errors.Is(err, ErrFreshLoginRequired) {
		t.Fatalf("stale login enrollment accepted: %v", err)
	}
}
func TestMFALockWaitRejectsOldOTP(t *testing.T) {
	f := mfaDatabase(t)
	e, _ := f.enable(t)
	f.clock.Add(30)
	code := f.code(t, e.Secret)
	err := waitAcrossMFALock(t, f, 3*time.Minute, func() error { _, err := f.svc.Verify(t.Context(), f.actor, code); return err })
	if !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("old OTP accepted after waiting: %v", err)
	}
}

// Wait on the exact factor SELECT or audit INSERT, after initial authority
// validation has completed. Only the fixture's logical clock advances; this
// is a credential-boundary test, not evidence of continuous wall-clock uptime.
func waitAcrossMFAStorageLock(t *testing.T, f *mfaFixture, advance time.Duration, lockSQL string, lockArgs []any, waitingSQL string, call func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cfg := f.pool.Config().Copy()
	name := "mfa_storage_wait_" + store.ID()
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := f.svc.db
	f.svc.db = pool
	defer func() { f.svc.db = original; pool.Close() }()
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, lockSQL, lockArgs...); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- call(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%'||$2||'%')`, name, waitingSQL).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("MFA operation ended before reaching the controlled storage wait: %v", result)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("MFA operation never waited on %s", waitingSQL)
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.clock.Add(int64(advance / time.Second))
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		t.Fatal("MFA operation did not finish after storage lock release")
		return ctx.Err()
	}
}

type mfaPersistentSnapshot struct {
	factor, recovery string
	audits           int
}

func persistentMFASnapshot(t *testing.T, f *mfaFixture) mfaPersistentSnapshot {
	t.Helper()
	var snapshot mfaPersistentSnapshot
	if err := f.pool.QueryRow(t.Context(), `SELECT
 COALESCE((SELECT to_jsonb(factor)::text FROM v3_mfa_totp factor WHERE instance_id=$1 AND user_id=$2),'null'),
 COALESCE((SELECT jsonb_agg(to_jsonb(code) ORDER BY code_hash)::text FROM v3_mfa_recovery_codes code WHERE instance_id=$1 AND user_id=$2),'[]'),
 (SELECT count(*) FROM management_audit WHERE actor_id=$2 AND action LIKE 'mfa.totp.%')`, f.svc.instanceID, f.actor.UserID).
		Scan(&snapshot.factor, &snapshot.recovery, &snapshot.audits); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func requireMFARollback(t *testing.T, f *mfaFixture, before mfaPersistentSnapshot) {
	t.Helper()
	after := persistentMFASnapshot(t, f)
	if before != after {
		// Do not print encrypted seed material or recovery hashes in evidence.
		t.Fatalf("MFA rejection did not fully roll back: factor unchanged=%t recovery unchanged=%t audit count=%d want=%d", before.factor == after.factor, before.recovery == after.recovery, after.audits, before.audits)
	}
}

func TestMFAFactorRowWaitRechecksExpiredSession(t *testing.T) {
	f := mfaDatabase(t)
	e, _ := f.enable(t)
	f.clock.Add(30)
	code := f.code(t, e.Secret)
	before := persistentMFASnapshot(t, f)
	err := waitAcrossMFAStorageLock(t, f, time.Hour+time.Second,
		`SELECT user_id FROM v3_mfa_totp WHERE instance_id=$1 AND user_id=$2 FOR UPDATE`,
		[]any{f.svc.instanceID, f.actor.UserID}, "FROM v3_mfa_totp",
		func(ctx context.Context) error { _, err := f.svc.Verify(ctx, f.actor, code); return err })
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("session expired during factor row wait was accepted: %v", err)
	}
	requireMFARollback(t, f, before)
}

func TestMFAAuditWaitRollsBackExpiredTOTP(t *testing.T) {
	f := mfaDatabase(t)
	e, _ := f.enable(t)
	f.clock.Add(30)
	code := f.code(t, e.Secret)
	before := persistentMFASnapshot(t, f)
	err := waitAcrossMFAStorageLock(t, f, 3*time.Minute, `LOCK TABLE management_audit IN ACCESS EXCLUSIVE MODE`, nil,
		"INSERT INTO management_audit", func(ctx context.Context) error { _, err := f.svc.Verify(ctx, f.actor, code); return err })
	if !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("TOTP expired during audit wait was committed: %v", err)
	}
	// last_accepted_step was already advanced before the audit wait. The
	// rejected commit must undo it and the would-be successful audit together.
	requireMFARollback(t, f, before)
}

func TestMFAAuditWaitRollsBackRecoveryWhenSessionExpires(t *testing.T) {
	f := mfaDatabase(t)
	_, confirmed := f.enable(t)
	before := persistentMFASnapshot(t, f)
	err := waitAcrossMFAStorageLock(t, f, time.Hour+time.Second, `LOCK TABLE management_audit IN ACCESS EXCLUSIVE MODE`, nil,
		"INSERT INTO management_audit", func(ctx context.Context) error {
			_, err := f.svc.Verify(ctx, f.actor, confirmed.RecoveryCodes[0])
			return err
		})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("recovery consumed after session expired during audit wait: %v", err)
	}
	// consumed_at must return to NULL, along with factor counters and audit.
	requireMFARollback(t, f, before)
}

func TestMFAAuditWaitRollsBackEnrollmentWhenOTPExpires(t *testing.T) {
	f := mfaDatabase(t)
	e, err := f.svc.BeginEnrollment(t.Context(), f.actor)
	if err != nil {
		t.Fatal(err)
	}
	code := f.code(t, e.Secret)
	before := persistentMFASnapshot(t, f)
	err = waitAcrossMFAStorageLock(t, f, 3*time.Minute, `LOCK TABLE management_audit IN ACCESS EXCLUSIVE MODE`, nil,
		"INSERT INTO management_audit", func(ctx context.Context) error {
			_, err := f.svc.ConfirmEnrollment(ctx, f.actor, code)
			return err
		})
	if !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("enrollment committed with OTP expired during audit wait: %v", err)
	}
	// The pending seed/state must remain intact; no generated recovery hashes
	// or successful enrollment audit may survive the rejected transaction.
	requireMFARollback(t, f, before)
}

func TestMFAAuditWaitRollsBackCancellationWhenFreshLoginExpires(t *testing.T) {
	f := mfaDatabase(t)
	_, confirmed := f.enable(t)
	before := persistentMFASnapshot(t, f)
	err := waitAcrossMFAStorageLock(t, f, freshLoginWindow+time.Second, `LOCK TABLE management_audit IN ACCESS EXCLUSIVE MODE`, nil,
		"INSERT INTO management_audit", func(ctx context.Context) error { return f.svc.Cancel(ctx, f.actor, confirmed.RecoveryCodes[0]) })
	if !errors.Is(err, ErrFreshLoginRequired) {
		t.Fatalf("factor cancelled after fresh login expired during audit wait: %v", err)
	}
	// DELETE cascades to recovery codes before the audit wait. Both tables
	// must be restored, including the recovery code consumed for cancellation.
	requireMFARollback(t, f, before)
}
