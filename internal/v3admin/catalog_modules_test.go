package v3admin

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// PA-231: routes authorize home, traces, logs, models and workspace, and the
// four built-in roles hold those grants, but the flat matrix had no rows for
// them, so a custom role could never be given them.
func TestMatrixCatalogOffersRouteModules(t *testing.T) {
	want := map[string][2]string{
		"home":      {"总览", "Overview"},
		"traces":    {"链路", "Traces"},
		"logs":      {"日志", "Logs"},
		"models":    {"模型", "Models"},
		"workspace": {"工作区设置", "Workspace settings"},
	}
	for code, names := range want {
		entry, ok := catalogResource(code)
		if !ok || entry.ZH != names[0] || entry.EN != names[1] || len(entry.Modules) != 1 || entry.Modules[0] != code {
			t.Fatalf("%s: catalog entry %+v ok=%t; want one module with labels %v", code, entry, ok, names)
		}
		for _, name := range names {
			if byName, ok := catalogResource(name); !ok || byName.Code != code {
				t.Fatalf("label %q resolves to %+v ok=%t; want %s", name, byName, ok, code)
			}
		}
	}
	// A label shared by two rows would make canonicalMatrix write the wrong module.
	seen := map[string]string{}
	for _, entry := range matrixCatalog {
		for _, v := range []string{entry.Code, entry.ZH, entry.EN} {
			if prev, ok := seen[v]; ok && prev != entry.Code {
				t.Fatalf("%q names both %s and %s", v, prev, entry.Code)
			}
			seen[v] = entry.Code
		}
	}
}

func TestMatrixShowsAndWritesRouteModuleCells(t *testing.T) {
	rows := displayMatrix([]grant{
		{Module: "traces", Action: "view", ScopeKind: "global", ScopeID: "*", Assurance: "standard"},
		{Module: "traces", Action: "manage", ScopeKind: "global", ScopeID: "*", Assurance: "standard"},
		{Module: "workspace", Action: "view", ScopeKind: "global", ScopeID: "*", Assurance: "standard"},
	}, false)
	byName := map[string][]int{}
	for _, row := range rows {
		byName[row.Resource] = row.Grants
	}
	if got := byName["链路"]; len(got) != 5 || got[0] != 1 || got[1] != 0 || got[4] != 1 {
		t.Fatalf("traces row=%v in %+v", got, rows)
	}
	if got := byName["工作区设置"]; len(got) != 5 || got[0] != 1 || got[4] != 0 {
		t.Fatalf("workspace row=%v in %+v", got, rows)
	}
	for _, name := range []string{"总览", "日志", "模型"} {
		if got := byName[name]; len(got) != 5 || got[0] != 0 {
			t.Fatalf("%s row=%v; want five empty cells", name, got)
		}
	}
	got, err := canonicalMatrix(rows)
	if err != nil || got["traces"][4] != 1 || got["workspace"][0] != 1 || got["home"] == nil || got["logs"] == nil || got["models"] == nil {
		t.Fatalf("route module rows did not map to stable codes: %+v %v", got, err)
	}

	input := createRoleInput{ID: "custom-observer", Name: "Observer", Scopes: []accessScope{{Kind: "global", ID: "*"}}, Matrix: rows}
	grants, err := normalizeRoleInput(&input, "dev")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, g := range grants {
		keys[g.Module+"."+g.Action] = true
	}
	if !keys["traces.view"] || !keys["traces.manage"] || !keys["workspace.view"] || keys["workspace.manage"] || keys["logs.view"] {
		t.Fatalf("new role grants=%+v", grants)
	}
}

// Modules a route authorizes but that are deliberately not matrix rows. A new
// module appearing in a route must either get a row or be listed here.
var modulesOutsideMatrix = map[string]string{
	// Environment admission is shown as environment_access, not a row (PA-55).
	"environment": "environment access",
}

func TestEveryRouteModuleIsGrantableFromMatrix(t *testing.T) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`MarkV3(?:Object)?Route\([^,]+,\s*"([a-z_]+)"`),
		regexp.MustCompile(`V3Decision\{\s*Module:\s*"([a-z_]+)"`),
		regexp.MustCompile(`\{\s*"(?:GET|POST|PUT|PATCH|DELETE) /api/[^"]*",\s*"([a-z_]+)",\s*"(?:view|change|delete|approve|execute|manage)"`),
	}
	used := map[string][]string{}
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllSubmatch(src, -1) {
				used[string(match[1])] = append(used[string(match[1])], path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) < 10 {
		t.Fatalf("route scan found only %d modules; the patterns no longer match the route declarations", len(used))
	}
	var missing []string
	for module, files := range used {
		if matrixModule(module) || modulesOutsideMatrix[module] != "" {
			continue
		}
		missing = append(missing, module+" ("+files[0]+")")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("routes authorize modules a role matrix cannot grant: %v", missing)
	}
}
