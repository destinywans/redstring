package profile

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewPasswordRequirements(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		valid          bool
	}{
		{"minimum", "Abcd123!", true},
		{"too short", "Abc123!", false},
		{"no uppercase", "abcd123!", false},
		{"no lowercase", "ABCD123!", false},
		{"no number", "Abcdefg!", false},
		{"no symbol", "Abcde123", false},
		{"space is not symbol", "Abcd123 ", false},
		{"unicode counts as characters", "Aa1!中文你好", true},
		{"unicode symbol", "Abcd123★", true},
		{"maximum", "Aa1!" + strings.Repeat("界", 124), true},
		{"over maximum", "Aa1!" + strings.Repeat("界", 125), false},
		{"malformed unicode", "Aa1!xxxx\xff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validNewPassword(tc.password); got != tc.valid {
				t.Fatalf("validNewPassword() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func TestChangePasswordRejectsInvalidBeforeStorage(t *testing.T) {
	svc := &Service{}
	if err := svc.ChangePassword(context.Background(), "old-password", "weak"); !errors.Is(err, ErrPasswordRequirements) {
		t.Fatalf("expected password requirements error, got %v", err)
	}
	if err := svc.ChangePassword(context.Background(), "Abcd123!", "Abcd123!"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same password should be rejected, got %v", err)
	}
}
