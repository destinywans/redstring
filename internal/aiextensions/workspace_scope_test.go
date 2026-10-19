package aiextensions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
)

func TestWorkspaceScopeReadWriteAndImmutableScope(t *testing.T) {
	s, admin := serviceForTest()
	record, err := s.Save(admin, Record{Kind: "skill", Title: "Shared SRE", Body: "shared fact", Maintainer: "owner", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"viewer", "operator"} {
		ctx := store.WithPrincipal(context.Background(), core.User{ID: "member", Role: role})
		rows, err := s.ListWorkspace(ctx, "skill")
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s workspace read: %v %v", role, rows, err)
		}
		if _, err = s.Get(ctx, record.ID, 1); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Save(ctx, record); !errors.Is(err, store.ErrForbidden) {
			t.Fatalf("%s modified workspace: %v", role, err)
		}
	}
	if _, err = s.ListWorkspace(context.Background(), "skill"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("anonymous read", err)
	}
	if _, err = s.ListWorkspace(admin, "mcp"); !errors.Is(err, ErrInvalid) {
		t.Fatal("global MCP list", err)
	}
	rows, err := s.List(admin, "app-a", "skill")
	if err != nil || len(rows) != 0 {
		t.Fatal("legacy app list includes workspace", rows, err)
	}
	moved := record
	moved.ApplicationID = "app-a"
	if _, err = s.Save(admin, moved); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("moved workspace into app", err)
	}
	appRecord, err := s.Save(admin, Record{ApplicationID: "app-a", Kind: "prompt", Title: "App", Body: "private", Maintainer: "owner", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	appRecord.ApplicationID = ""
	if _, err = s.Save(admin, appRecord); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("moved app into workspace", err)
	}
	if err = validate(Record{Kind: "mcp", Title: "MCP", Maintainer: "owner", MCP: &MCPConfig{Endpoint: "https://example.com"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("global MCP accepted", err)
	}
}

func TestWorkspaceCompositionRequiresTargetAndTargetTools(t *testing.T) {
	s, admin := serviceForTest()
	r, err := s.Save(admin, Record{Kind: "skill", Title: "Shared", Body: "shared fact", Maintainer: "owner", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	operator := store.WithPrincipal(admin, core.User{ID: "user", Role: "operator"})
	refs := []Reference{{ID: r.ID, Version: r.Version}}
	result, err := s.Compose(operator, Composition{ApplicationID: "app-a", References: refs})
	if err != nil || !strings.Contains(result.Content, "shared fact") {
		t.Fatal(result, err)
	}
	for _, target := range []string{"", "app-b"} {
		if _, err = s.Compose(operator, Composition{ApplicationID: target, References: refs}); !errors.Is(err, store.ErrForbidden) {
			t.Fatal("target bypass", target, err)
		}
	}
	r.RequiredTools = []string{"missing_app_tool"}
	r, err = s.Save(admin, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Compose(operator, Composition{ApplicationID: "app-a", References: []Reference{{ID: r.ID, Version: r.Version}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("tool requirement bypass", err)
	}
	r.Enabled = false
	if _, err = s.Save(admin, r); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Compose(operator, Composition{ApplicationID: "app-a", References: refs}); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("disabled global source accepted", err)
	}
}
