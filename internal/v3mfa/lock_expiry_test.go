package v3mfa

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
	"testing"
	"time"
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
