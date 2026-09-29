package v3settings

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
)

func TestPreferencesRejectUnsupportedValuesBeforeStorage(t *testing.T) {
	bad := "unsupported"
	for _, tc := range []struct {
		name  string
		patch preferencesPatch
	}{
		{"theme", preferencesPatch{Theme: &bad}},
		{"locale", preferencesPatch{Locale: &bad}},
		{"density", preferencesPatch{Density: &bad}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&Service{}).savePrefs(t.Context(), "user", tc.patch)
			if !errors.Is(err, errInvalid) {
				t.Fatalf("unsupported %s: got %v, want invalid settings", tc.name, err)
			}
		})
	}
}

func TestWorkspaceRejectsInvalidInputBeforeStorage(t *testing.T) {
	valid := workspace{Name: "Team", Slug: "team", Timezone: "Asia/Shanghai", Locale: "zh-CN", WeekStart: "monday", RetentionDays: 90}
	for _, tc := range []struct {
		name   string
		change func(*workspace)
	}{
		{"blank name", func(w *workspace) { w.Name = "  " }},
		{"malformed slug", func(w *workspace) { w.Slug = "Team" }},
		{"invalid timezone", func(w *workspace) { w.Timezone = "No/Such_Zone" }},
		{"invalid locale", func(w *workspace) { w.Locale = "fr-FR" }},
		{"invalid week start", func(w *workspace) { w.WeekStart = "saturday" }},
		{"zero retention", func(w *workspace) { w.RetentionDays = 0 }},
		{"excessive retention", func(w *workspace) { w.RetentionDays = 3651 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.change(&input)
			_, _, err := (&Service{}).putWorkspace(t.Context(), core.User{ID: "user"}, "dev", 1, input)
			if !errors.Is(err, errInvalid) {
				t.Fatalf("invalid %s: got %v, want invalid settings", tc.name, err)
			}
		})
	}
}

func TestUnavailableIdentityDoesNotReportFalseMFAState(t *testing.T) {
	w := httptest.NewRecorder()
	respond(w, nil, errIdentityUnavailable)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"identity_evidence_unavailable"`) {
		t.Fatalf("unexpected identity response: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"mfa_enabled":false`) || strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatalf("unverified identity status leaked as false: %s", w.Body.String())
	}
}
