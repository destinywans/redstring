package store

import (
	"errors"
	"testing"

	"github.com/destinywans/redstring/internal/core"
)

func TestBootstrapRejectsPublishedPlaceholder(t *testing.T) {
	s := isolated(t)
	ctx := t.Context()
	if err := s.Bootstrap(ctx, "admin@example.invalid", "replace-with-long-random-password"); err == nil {
		t.Fatal("first startup accepted the published administrator password")
	}
	var count int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected bootstrap left %d users, err=%v", count, err)
	}
}

func TestLoginRejectsPublishedPlaceholderForExistingMember(t *testing.T) {
	s := isolated(t)
	ctx := t.Context()
	const password = "replace-with-random-value"
	const distinctPassword = " replace-with-random-value "
	_, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
 ('existing-member','member@example.invalid','Member','viewer',$1),
 ('distinct-member','distinct@example.invalid','Member','viewer',$2)`, PasswordHash(password), PasswordHash(distinctPassword))
	if err != nil {
		t.Fatal(err)
	}
	if _, token, err := s.Login(ctx, "member@example.invalid", password); !errors.Is(err, ErrNotFound) || token != "" {
		t.Fatalf("published password authenticated: token present=%t, err=%v", token != "", err)
	}
	if _, token, err := s.Login(ctx, "distinct@example.invalid", distinctPassword); err != nil || token == "" {
		t.Fatalf("distinct password no longer authenticates: token present=%t, err=%v", token != "", err)
	}
}

func TestPlaceholderSecrets(t *testing.T) {
	for _, input := range []string{exampleAdminPassword, exampleDatabasePassword, " REPLACE-WITH-test "} {
		if !IsPlaceholderSecret(input) {
			t.Errorf("example secret was accepted: %q", input)
		}
	}
	for _, input := range []string{"", "synthetic-admin-password", "6578616d706c652d696e76616c69642d6c6f63616c2d74657374"} {
		if IsPlaceholderSecret(input) {
			t.Errorf("ordinary value was refused: %q", input)
		}
	}
	if !IsPublishedPlaceholder(exampleAdminPassword) || IsPublishedPlaceholder(" "+exampleAdminPassword+" ") || IsPublishedPlaceholder("REPLACE-WITH-unpublished") {
		t.Error("published placeholder matching does not match the known credential set")
	}
}

func TestRetirePlaceholderAdminPassword(t *testing.T) {
	s := isolated(t)
	ctx := t.Context()
	const replacement = "synthetic-replacement-password"
	staleHash := PasswordHash(exampleAdminPassword)
	normalHash := PasswordHash("synthetic-unchanged-password")
	_, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,disabled) VALUES
 ('stale','admin@localhost','Administrator','admin',$1,false),
 ('configured','configured@example.invalid','Administrator','admin',$2,false),
 ('ordinary','ordinary@example.invalid','Member','viewer',$1,false),
 ('disabled','disabled@example.invalid','Administrator','admin',$1,true)`, staleHash, normalHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"stale", "configured", "ordinary", "disabled"} {
		_, err = s.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at)
 VALUES($1,$2,now()+interval '1 hour')`, Hash(id), id)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, unusable := range []string{"", "too-short", exampleAdminPassword, " REPLACE-WITH-OTHER "} {
		if err = s.RetirePlaceholderAdminPassword(ctx, "CONFIGURED@EXAMPLE.INVALID", unusable); err == nil {
			t.Fatal("active example credential did not require a replacement")
		}
	}
	check := func(id, wantHash string, wantSession bool) {
		t.Helper()
		var hash string
		var hasSession bool
		err = s.DB.QueryRow(ctx, `SELECT password_hash,EXISTS(SELECT 1 FROM sessions WHERE user_id=$1)
 FROM users WHERE id=$1`, id).Scan(&hash, &hasSession)
		if err != nil || hash != wantHash || hasSession != wantSession {
			t.Fatalf("unexpected persisted state for %s: session=%t, err=%v", id, hasSession, err)
		}
	}
	check("stale", staleHash, true)
	if err = s.RetirePlaceholderAdminPassword(ctx, "CONFIGURED@EXAMPLE.INVALID", replacement); err != nil {
		t.Fatal(err)
	}
	var rotatedHash string
	if err = s.DB.QueryRow(ctx, "SELECT password_hash FROM users WHERE id='stale'").Scan(&rotatedHash); err != nil ||
		!CheckPassword(replacement, rotatedHash) || CheckPassword(exampleAdminPassword, rotatedHash) {
		t.Fatalf("default administrator password did not rotate: %v", err)
	}
	check("stale", rotatedHash, false)
	check("configured", normalHash, true)
	check("ordinary", staleHash, true)
	check("disabled", staleHash, true)
	if err = s.RetirePlaceholderAdminPassword(ctx, "absent@example.invalid", ""); err != nil {
		t.Fatalf("idempotent startup refused an absent configured account: %v", err)
	}
	var count int
	err = s.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='' AND actor_kind='system'
 AND action='bootstrap.placeholder_password.replaced' AND target='stale' AND outcome='success'
 AND changes='{"sessions_revoked":true}'::jsonb`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected one password rotation audit, got %d: %v", count, err)
	}
}

func TestMemberPasswordWritesRejectPublishedExample(t *testing.T) {
	s := isolated(t)
	ctx := t.Context()
	member, err := s.CreateUser(ctx, core.User{Email: "member@example.invalid", Name: "Member", Role: "viewer"}, "synthetic-member-password")
	if err != nil {
		t.Fatal(err)
	}
	password := exampleAdminPassword
	if _, err = s.CreateUser(ctx, core.User{Email: "new@example.invalid", Name: "New", Role: "viewer"}, password); err == nil {
		t.Fatal("member creation accepted the published password")
	}
	if _, err = s.UpdateMember(ctx, member.ID, MemberUpdate{Password: &password}); err == nil {
		t.Fatal("member update accepted the published password")
	}
	if _, token, err := s.Login(ctx, member.Email, "synthetic-member-password"); err != nil || token == "" {
		t.Fatalf("rejected password update changed the existing credential: %v", err)
	}
}
