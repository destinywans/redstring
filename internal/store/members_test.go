package store

import (
	"context"
	"errors"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
)

func TestMemberLifecycleAndImportPostgres(t *testing.T) {
	s := platformDatabase(t)
	ctx := context.Background()
	admin, err := s.CreateMember(ctx, MemberCreate{Email: "admin@example.invalid", Name: "Administrator", Role: "admin", Password: "isolated-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithPrincipal(ctx, admin.User)
	if err = s.DeleteMember(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last administrator deletion: %v", err)
	}
	member, err := s.CreateMember(ctx, MemberCreate{Email: "operator@example.invalid", Name: "Operator", Role: "operator", Password: "isolated-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, member.Email, "isolated-test-password")
	if err != nil {
		t.Fatal(err)
	}
	role := "viewer"
	if _, err = s.UpdateMember(ctx, member.ID, MemberUpdate{AccountUpdate: AccountUpdate{Role: &role}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, token); err == nil {
		t.Fatal("role change retained previous session")
	}
	request := MemberImportRequest{Source: "test-directory", Rows: []MemberImportRow{{ExternalID: "person-1", Email: "imported@example.invalid", Name: "Imported member"}}}
	preview, err := s.PreviewMemberImport(ctx, request)
	if err != nil || !preview.CanImport {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	request.Fingerprint = preview.Fingerprint
	name := "Changed directory snapshot"
	if _, err = s.UpdateMember(ctx, member.ID, MemberUpdate{AccountUpdate: AccountUpdate{Name: &name}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportMembers(ctx, request); !errors.Is(err, ErrMemberPreviewStale) {
		t.Fatalf("stale preview admitted: %v", err)
	}
	preview, err = s.PreviewMemberImport(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Fingerprint = preview.Fingerprint
	result, err := s.ImportMembers(ctx, request)
	if err != nil || result.Created != 1 {
		t.Fatalf("import: %+v %v", result, err)
	}
	members, err := s.Members(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var imported Member
	for _, m := range members {
		if m.ExternalID == "person-1" {
			imported = m
		}
	}
	if imported.ID == "" || !imported.Disabled || imported.Role != "viewer" {
		t.Fatalf("unsafe imported identity: %+v", imported)
	}
	enabled := false
	if _, err = s.UpdateMember(ctx, imported.ID, MemberUpdate{AccountUpdate: AccountUpdate{Disabled: &enabled}}); !errors.Is(err, ErrMemberInvalid) {
		t.Fatalf("passwordless activation allowed: %v", err)
	}
	if err = s.DeleteMember(ctx, member.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.Member(ctx, member.ID)
	if err != nil || !deleted.Disabled || deleted.DeletedAt == nil {
		t.Fatalf("identity not retained safely: %+v %v", deleted, err)
	}
}

func TestApplicationProfileAndConfigurationPostgres(t *testing.T) {
	s := platformDatabase(t)
	ctx := context.Background()
	app, err := s.RegisterApplication(ctx, core.Application{Name: "Order API", Key: "orders", Environment: "lab", Profile: core.ApplicationProfile{Language: "Go", Framework: "net/http"}})
	if err != nil {
		t.Fatal(err)
	}
	if app.Profile.Language != "Go" {
		t.Fatalf("profile lost on create: %+v", app.Profile)
	}
	app.Profile.DefaultBranch = "main"
	app, err = s.UpdateApplication(ctx, app)
	if err != nil || app.Profile.DefaultBranch != "main" {
		t.Fatalf("profile update: %+v %v", app.Profile, err)
	}
	envs, err := s.Environments(ctx, app.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %v %v", envs, err)
	}
	before := envs[0]
	port := 8080
	change := before
	change.Configuration = &core.EnvironmentConfiguration{Port: &port}
	saved, err := s.SaveEnvironment(ctx, change)
	if err != nil || saved.Configuration.Port == nil || *saved.Configuration.Port != port {
		t.Fatalf("configuration update: %+v %v", saved, err)
	}
	if saved.ConfigurationRevision <= before.ConfigurationRevision {
		t.Fatal("configuration update did not increment version")
	}
	if _, err = s.SaveEnvironment(ctx, change); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale configuration overwrote newer version: %v", err)
	}
}
