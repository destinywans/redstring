package v3mfa

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSameOriginWrite(t *testing.T) {
	s := &Service{allowedOrigin: "https://canvas.example.invalid"}
	for _, tc := range []struct {
		name, origin, site string
		want               bool
	}{
		{"configured origin", "https://canvas.example.invalid", "", true},
		{"same origin metadata", "", "same-origin", true},
		{"missing browser signal", "", "", false},
		{"cross site metadata", "", "cross-site", false},
		{"foreign origin", "https://evil.example.invalid", "same-origin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://canvas.example.invalid/api/v1/me/mfa/verify", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			w := httptest.NewRecorder()
			if got := s.sameOriginWrite(w, r); got != tc.want {
				t.Fatalf("allowed=%t, want %t", got, tc.want)
			}
			if !tc.want && w.Code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", w.Code)
			}
		})
	}
}

func TestReadCodeStrictInput(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		valid            bool
	}{
		{"totp", `{"code":"012345"}`, "012345", true},
		{"unknown field", `{"code":"012345","extra":true}`, "", false},
		{"trailing object", `{"code":"012345"}{"code":"678901"}`, "", false},
		{"whitespace code", `{"code":" 012345"}`, "", false},
		{"oversized body", `{"code":"` + strings.Repeat("A", 4096) + `"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/me/mfa/verify", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			code, ok := readCode(w, r)
			if ok != tc.valid || code != tc.want {
				t.Fatalf("code=%q, valid=%t", code, ok)
			}
			if !tc.valid && w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d, want 422", w.Code)
			}
		})
	}
}
