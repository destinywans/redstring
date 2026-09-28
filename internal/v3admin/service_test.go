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
	byName := map[string][]int{}
	for _, row := range rows {
		byName[row.Resource] = row.Grants
	}
	if len(byName["写操作提案"]) != 5 || len(byName["成员与角色"]) != 5 ||
		len(byName["服务目录"]) != 5 || len(byName["知识来源与检索"]) != 5 ||
		byName["写操作提案"][3] != 2 || byName["成员与角色"][4] != 1 {
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
		{"grouped modules have different environment scopes", []grant{
			{Module: "members", Action: "view", ScopeKind: "environment", ScopeID: "dev", Assurance: "standard"},
			{Module: "roles", Action: "view", ScopeKind: "environment", ScopeID: "test", Assurance: "standard"},
		}, errScopedCell},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMatrixEditable(tc.grants); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRoleDetailRejectsMisleadingGroupedGrant(t *testing.T) {
	for _, grants := range [][]grant{
		{{Module: "members", Action: "view", ScopeKind: "global", ScopeID: "*", Assurance: "standard"}},
		{
			{Module: "members", Action: "view", ScopeKind: "environment", ScopeID: "dev", Assurance: "standard"},
			{Module: "roles", Action: "view", ScopeKind: "environment", ScopeID: "test", Assurance: "standard"},
		},
		{
			{Module: "members", Action: "approve", ScopeKind: "global", ScopeID: "*", Assurance: "passkey"},
			{Module: "roles", Action: "approve", ScopeKind: "global", ScopeID: "*", Assurance: "standard"},
		},
	} {
		if !errors.Is(validateMatrixGroups(grants), errPartialMatrix) {
			t.Fatalf("grouped grant should require dedicated editor: %+v", grants)
		}
	}
	paired := []grant{
		{Module: "members", Action: "view", ScopeKind: "application", ScopeID: "app", Assurance: "standard"},
		{Module: "roles", Action: "view", ScopeKind: "application", ScopeID: "app", Assurance: "standard"},
	}
	if err := validateMatrixGroups(paired); err != nil {
		t.Fatalf("equivalent scoped grants rejected: %v", err)
	}
}
