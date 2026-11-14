package store

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestLoginRejectedAccountsVerifyPasswordPostgres(t *testing.T) {
	s := isolated(t)
	ctx := t.Context()
	const password = "synthetic-local-password"
	hash := PasswordHash(password)
	_, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,disabled,deleted_at) VALUES
 ('login-valid','valid@example.invalid','Valid','viewer',$1,false,NULL),
 ('login-disabled','disabled@example.invalid','Disabled','viewer',$1,true,NULL),
 ('login-deleted','deleted@example.invalid','Deleted','viewer',$1,true,now()),
 ('login-no-password','no-password@example.invalid','No password','viewer','',false,NULL),
 ('login-invalid-hash','invalid-hash@example.invalid','Invalid hash','viewer','invalid',false,NULL)`, hash)
	if err != nil {
		t.Fatal(err)
	}
	emails := []string{
		"valid@example.invalid",
		"missing@example.invalid",
		"disabled@example.invalid",
		"deleted@example.invalid",
		"no-password@example.invalid",
		"invalid-hash@example.invalid",
	}
	elapsed := make([][]time.Duration, len(emails))
	for range 5 {
		for i, email := range emails {
			start := time.Now()
			_, token, err := s.Login(ctx, email, "incorrect-synthetic-password")
			if !errors.Is(err, ErrNotFound) || token != "" {
				t.Fatalf("rejected login %s: token present=%t, err=%v", email, token != "", err)
			}
			elapsed[i] = append(elapsed[i], time.Since(start))
		}
	}
	slices.Sort(elapsed[0])
	baseline := elapsed[0][2]
	for i := 1; i < len(elapsed); i++ {
		slices.Sort(elapsed[i])
		// This catches a missing Argon2 call while allowing for host scheduling
		// and the row lock on an existing account. It is not a latency SLA.
		if median := elapsed[i][2]; median < baseline/4 {
			t.Errorf("%s login took %s; existing wrong-password median is %s", emails[i], median, baseline)
		}
	}
	for _, email := range emails[1:] {
		if _, token, err := s.Login(ctx, email, password); !errors.Is(err, ErrNotFound) || token != "" {
			t.Fatalf("inactive or passwordless login %s: token present=%t, err=%v", email, token != "", err)
		}
	}
	if user, token, err := s.Login(ctx, "valid@example.invalid", password); err != nil || token == "" || user.ID != "login-valid" {
		t.Fatalf("valid login: user=%s, token present=%t, err=%v", user.ID, token != "", err)
	}
}
