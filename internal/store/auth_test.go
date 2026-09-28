package store

import (
	"context"
	"errors"
	"testing"

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
