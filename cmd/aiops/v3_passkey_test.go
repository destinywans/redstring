package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConfigureV3PasskeyIsOptionalAndFailsClosed(t *testing.T) {
	info := v3Instance{Tier: "production", Name: "AIOps", Domain: "aiops.example.invalid"}
	if err := configureV3Passkey(nil, nil, info, ""); err != nil {
		t.Fatalf("unset passkey flag: %v", err)
	}
	if err := configureV3Passkey(nil, nil, info, "false"); err != nil {
		t.Fatalf("disabled passkey flag: %v", err)
	}
	if err := configureV3Passkey(nil, nil, info, "TRUE"); err == nil {
		t.Fatal("invalid passkey flag accepted")
	}
	st := &store.Store{DB: &pgxpool.Pool{}}
	api := httpapi.New(st, nil, true)
	api.AllowedOrigin = "https://aiops.example.invalid"
	if err := configureV3Passkey(st, api, info, "true"); err == nil {
		t.Fatal("passkey routes accepted insecure session cookies")
	}
	api.SecureCookies = true
	api.AllowedOrigin = "https://other.example.invalid"
	if err := configureV3Passkey(st, api, info, "true"); err == nil {
		t.Fatal("passkey routes accepted a mismatched public origin")
	}
	api.AllowedOrigin = "https://aiops.example.invalid"
	if err := configureV3Passkey(st, api, info, "true"); err != nil {
		t.Fatalf("trusted passkey configuration rejected: %v", err)
	}
	for _, path := range []string{
		"/api/v1/me/passkeys",
		"/api/v1/me/passkeys/registration/options",
		"/api/v1/me/passkeys/assertion/options",
		"/api/v1/me/passkeys/deletion/options",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s was not registered behind account session: %d", path, w.Code)
		}
	}
}
