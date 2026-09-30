package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

type rejectedLoginRepository struct{ store.Repository }

func (rejectedLoginRepository) Login(context.Context, string, string) (core.User, string, error) {
	return core.User{}, "", errors.New("invalid credentials")
}

func loginFrom(t *testing.T, s *Server, peer, forwarded string) int {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"email":"fixture@test.invalid","password":"wrong"}`))
	r.RemoteAddr = peer
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	w := httptest.NewRecorder()
	s.login(w, r)
	return w.Code
}

func TestTrustedProxyClientsDoNotShareLoginLimit(t *testing.T) {
	s := New(rejectedLoginRepository{}, nil)
	// Configuration is explicit; request headers cannot turn proxy trust on.
	if err := s.SetTrustedProxyCIDRs("127.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if got := loginFrom(t, s, "127.0.0.1:43210", "192.0.2.1"); got != 401 {
			t.Fatalf("attempt %d: %d", i, got)
		}
	}
	if got := loginFrom(t, s, "127.0.0.1:43210", "192.0.2.2"); got != 401 {
		t.Fatalf("unrelated client inherited proxy limit: %d", got)
	}
	if got := loginFrom(t, s, "127.0.0.1:43210", "192.0.2.1"); got != 429 {
		t.Fatalf("first client escaped limit: %d", got)
	}
}

func TestUntrustedForwardingCannotEvadeLoginLimit(t *testing.T) {
	for _, trust := range []string{"", "127.0.0.0/8"} {
		s := New(rejectedLoginRepository{}, nil)
		if err := s.SetTrustedProxyCIDRs(trust); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			loginFrom(t, s, "192.0.2.8:1234", "203.0.113.9")
		}
		if got := loginFrom(t, s, "192.0.2.8:1234", "203.0.113.10"); got != 429 {
			t.Fatalf("trust %q: forged header escaped: %d", trust, got)
		}
	}
}

func TestForwardedClientSelection(t *testing.T) {
	s := New(rejectedLoginRepository{}, nil)
	if err := s.SetTrustedProxyCIDRs("127.0.0.0/8,10.0.0.0/8,::1/128"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ peer, xff, want string }{
		{"127.0.0.1:123", "203.0.113.1, 192.0.2.2, 10.0.0.2", "192.0.2.2"},
		{"[::1]:123", "2001:db8::1", "2001:db8::1"},
		{"127.0.0.1:123", "junk", "127.0.0.1"},
		{"127.0.0.1:123", "192.0.2.2, junk", "127.0.0.1"},
		{"127.0.0.1:123", "", "127.0.0.1"},
	} {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.xff)
		if got := s.loginClientIP(r); got != tc.want {
			t.Fatalf("%q / %q: got %q want %q", tc.peer, tc.xff, got, tc.want)
		}
	}
	if err := s.SetTrustedProxyCIDRs("not-a-network"); err == nil {
		t.Fatal("invalid trust configuration accepted")
	}
}
