package rbac

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoleTextLimits(t *testing.T) {
	if !validName("角色", strings.Repeat("描", 2000)) || validName("角色", strings.Repeat("描", 2001)) || validName(strings.Repeat("角", 201), "") {
		t.Fatal("text limits must count characters, not UTF-8 bytes")
	}
	for _, tc := range []struct {
		err  error
		code string
	}{{ErrRoleNameConflict, "role_name_conflict"}, {ErrLegacyMembership, "legacy_role_membership"}, {ErrLastManager, "last_rbac_manager"}} {
		w := httptest.NewRecorder()
		respond(w, nil, tc.err)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("conflict response: %d %s", w.Code, w.Body.String())
		}
	}
}
