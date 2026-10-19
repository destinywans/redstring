package store

import (
	"testing"

	"github.com/destinywans/redstring/internal/core"
)

func TestNewMembersInheritSavedWorkspaceLocaleAndTimezone(t *testing.T) {
	s := platformDatabase(t)
	ctx := t.Context()
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('workspace-admin','admin@example.invalid','Admin','admin','unused')`); err != nil {
		t.Fatal(err)
	}
	ctx = WithPrincipal(ctx, core.User{ID: "workspace-admin", Role: "admin"})
	if _, err := s.DB.Exec(ctx, `UPDATE workspace_settings SET timezone='Europe/London',language='en-US',revision=revision+1 WHERE id='default'`); err != nil {
		t.Fatal(err)
	}
	local, err := s.CreateMember(ctx, MemberCreate{Email: "local@example.invalid", Name: "Local", Role: "viewer", Password: "isolated-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	request := MemberImportRequest{Source: "test-directory", Rows: []MemberImportRow{{ExternalID: "employee-1", Email: "directory@example.invalid", Name: "Directory"}}}
	preview, err := s.PreviewMemberImport(ctx, request)
	if err != nil || !preview.CanImport {
		t.Fatalf("import preview: %+v %v", preview, err)
	}
	request.Fingerprint = preview.Fingerprint
	if result, err := s.ImportMembers(ctx, request); err != nil || result.Created != 1 {
		t.Fatalf("import result: %+v %v", result, err)
	}
	for _, email := range []string{local.Email, "directory@example.invalid"} {
		var timezone, locale string
		if err := s.DB.QueryRow(ctx, `SELECT u.v3_timezone,p.locale FROM users u JOIN v3_user_settings p ON p.user_id=u.id WHERE u.email=$1`, email).Scan(&timezone, &locale); err != nil {
			t.Fatal(err)
		}
		if timezone != "Europe/London" || locale != "en-US" {
			t.Fatalf("member %s inherited timezone=%q locale=%q", email, timezone, locale)
		}
	}
	// Existing members retain their own values when workspace defaults change.
	var previousTimezone, previousLocale string
	if _, err := s.DB.Exec(ctx, `UPDATE workspace_settings SET timezone='Asia/Shanghai',language='zh-CN',revision=revision+1 WHERE id='default'`); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(ctx, `SELECT u.v3_timezone,p.locale FROM users u JOIN v3_user_settings p ON p.user_id=u.id WHERE u.id=$1`, local.ID).Scan(&previousTimezone, &previousLocale); err != nil {
		t.Fatal(err)
	}
	if previousTimezone != "Europe/London" || previousLocale != "en-US" {
		t.Fatalf("existing member defaults were overwritten: %q %q", previousTimezone, previousLocale)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE workspace_settings SET timezone='invalid/timezone' WHERE id='default'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMember(ctx, MemberCreate{Email: "invalid@example.invalid", Name: "Invalid", Role: "viewer", Password: "isolated-test-password"}); err == nil {
		t.Fatal("new member created with invalid workspace defaults")
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE email='invalid@example.invalid'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid defaults left partial member: count=%d err=%v", count, err)
	}
}
