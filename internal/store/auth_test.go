package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestAuthenticationRejectsSoftDeletedIdentity(t *testing.T) {
	s := isolated(t)
	ctx := context.Background()
	const userID = "deleted-auth-user"
	const email = "deleted-auth@example.invalid"
	const password = "isolated-test-password"
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash)
		VALUES($1,$2,'Deleted identity','viewer',$3)`, userID, email, PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, email, password)
	if err != nil {
		t.Fatalf("initial login: %v", err)
	}
	if _, err = s.Session(ctx, token); err != nil {
		t.Fatalf("initial session: %v", err)
	}

	// Simulate a legacy or damaged row that bypassed the migration constraint.
	// Production deletion also disables the user and revokes sessions, but both
	// authentication reads must independently enforce the soft-delete marker.
	if _, err = s.DB.Exec(ctx, `ALTER TABLE users DROP CONSTRAINT users_deleted_disabled`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Login(ctx, email, password); !errors.Is(err, ErrNotFound) {
		t.Fatalf("soft-deleted identity logged in: %v", err)
	}
	if _, err = s.Session(ctx, token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("soft-deleted identity retained session: %v", err)
	}
}

func TestLockActiveSessionRevocationAndLogoutSerialization(t *testing.T) {
	s := isolated(t)
	ctx := context.Background()
	const userID = "session-lock-user"
	const token = "raw-session-lock-token"
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash)
		VALUES($1,'session-lock@example.invalid','Session lock','viewer','unused')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at)
		VALUES($1,$2,clock_timestamp()+interval '1 hour')`, Hash(token), userID); err != nil {
		t.Fatal(err)
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := LockActiveSession(ctx, tx, token, "different-user"); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("wrong actor: %v", err)
	}
	if err := LockActiveSession(ctx, tx, token, userID); err != nil {
		t.Fatalf("active session: %v", err)
	}

	logoutStarted := make(chan struct{})
	logoutDone := make(chan error, 1)
	go func() {
		close(logoutStarted)
		logoutDone <- s.Logout(ctx, token)
	}()
	<-logoutStarted
	select {
	case err := <-logoutDone:
		t.Fatalf("logout completed while session was locked: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-logoutDone:
		if err != nil {
			t.Fatalf("logout after lock released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("logout did not resume after transaction commit")
	}
	newTx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer newTx.Rollback(ctx)
	if err := LockActiveSession(ctx, newTx, token, userID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revoked session accepted: %v", err)
	}
	if err := newTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at)
		VALUES($1,$2,clock_timestamp()-interval '1 second')`, Hash("expired-token"), userID); err != nil {
		t.Fatal(err)
	}
	expiredTx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := LockActiveSession(ctx, expiredTx, "expired-token", userID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("expired session accepted: %v", err)
	}
	_ = expiredTx.Rollback(ctx)
	if _, err := s.DB.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 hour'
		WHERE token_hash=$1`, Hash("expired-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE users SET disabled=true WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	disabledTx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer disabledTx.Rollback(ctx)
	if err := LockActiveSession(ctx, disabledTx, "expired-token", userID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("disabled user accepted: %v", err)
	}
}

func TestLockActiveSessionWaitsForAccountDisable(t *testing.T) {
	s := isolated(t)
	ctx := t.Context()
	const userID = "session-disable-user"
	const token = "raw-session-disable-token"
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash)
		VALUES($1,'session-disable@example.invalid','Session disable','viewer','unused')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at)
		VALUES($1,$2,clock_timestamp()+interval '1 hour')`, Hash(token), userID); err != nil {
		t.Fatal(err)
	}
	accountTx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer accountTx.Rollback(context.Background())
	if _, err := accountTx.Exec(ctx, `UPDATE users SET disabled=true WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		requestTx, beginErr := s.DB.Begin(ctx)
		if beginErr != nil {
			result <- beginErr
			return
		}
		defer requestTx.Rollback(context.Background())
		result <- LockActiveSession(ctx, requestTx, token, userID)
	}()
	select {
	case err := <-result:
		t.Fatalf("session check completed before account update: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := accountTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrSessionRevoked) {
			t.Fatalf("disabled user retained active session: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session check did not resume after account update")
	}
}
