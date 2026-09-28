package v3admin

import (
	"errors"
	"testing"
)

func TestMatrixUsesStableCodesAndRejectsUnknownCells(t *testing.T) {
	rows := displayMatrix([]grant{
		{Module: "roles", Action: "manage", ScopeKind: "global", ScopeID: "*", Assurance: "standard"},
		{Module: "proposals", Action: "approve", ScopeKind: "global", ScopeID: "*", Assurance: "passkey"},
	}, false)
	if rows[4].Grants[3] != 2 || rows[8].Grants[4] != 1 {
		t.Fatalf("stored grants not represented: %+v", rows)
	}
	got, err := canonicalMatrix(rows)
	if err != nil || got["proposals"][3] != 2 || got["identity"][4] != 1 {
		t.Fatalf("localized labels did not map to stable codes: %+v %v", got, err)
	}
	english := displayMatrix(nil, true)
	if _, err = canonicalMatrix(english); err != nil {
		t.Fatalf("known English labels rejected: %v", err)
	}

	rows[0].Resource = "untrusted module"
	if _, err = canonicalMatrix(rows); err == nil {
		t.Fatal("unknown display label accepted as an authorization module")
	}
	rows[0].Resource = matrixCatalog[0].ZH
	rows[0].Grants[0] = 2
	if _, err = canonicalMatrix(rows); err == nil {
		t.Fatal("passkey-only state accepted for a non-approval action")
	}
}

func TestMatrixDoesNotSurfaceExecuteInFiveColumnEditor(t *testing.T) {
	rows := displayMatrix([]grant{{Module: "proposals", Action: "execute", ScopeKind: "global", ScopeID: "*", Assurance: "standard"}}, false)
	for _, cell := range rows[4].Grants {
		if cell != 0 {
			t.Fatal("execute grant was inaccurately mapped to another operation")
		}
	}
}

func TestMatrixEditorRejectsScopesAndPartialGroups(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grants []grant
		want   error
	}{
		{"global identity pair", []grant{
			{Module: "members", Action: "view", ScopeKind: "global", ScopeID: "*", Assurance: "standard"},
			{Module: "roles", Action: "view", ScopeKind: "global", ScopeID: "*", Assurance: "standard"},
		}, nil},
		{"environment grant", []grant{{Module: "events", Action: "view", ScopeKind: "environment", ScopeID: "dev", Assurance: "standard"}}, errScopedCell},
		{"application grant", []grant{{Module: "events", Action: "view", ScopeKind: "application", ScopeID: "app-1", Assurance: "standard"}}, errScopedCell},
		{"partial identity group", []grant{{Module: "members", Action: "view", ScopeKind: "global", ScopeID: "*", Assurance: "standard"}}, errPartialMatrix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMatrixEditable(tc.grants); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
