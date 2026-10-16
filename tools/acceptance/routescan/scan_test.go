package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func scanFixture(t *testing.T, name string, dirs ...string) *Result {
	t.Helper()
	if len(dirs) == 0 {
		dirs = []string{"internal", "cmd"}
	}
	res, err := Scan(filepath.Join("testdata", name), dirs)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func routeMap(res *Result) map[string]Route {
	out := map[string]Route{}
	for _, r := range res.Routes {
		out[r.Pattern] = r
	}
	return out
}

func patterns(rs []Registered) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Pattern)
	}
	sort.Strings(out)
	return out
}

// lineOf is the first line of a fixture file containing substr.
func lineOf(t *testing.T, file, substr string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "basic", file))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, substr) {
			return fmt.Sprintf("%s:%d", file, i+1)
		}
	}
	t.Fatalf("%q not in %s", substr, file)
	return ""
}

func TestScanMarks(t *testing.T) {
	res := scanFixture(t, "basic")
	got := routeMap(res)
	want := []struct {
		pattern, module, action string
		object                  bool
		via                     string
	}{
		{"GET /api/v1/literal", "alpha", "view", false, "literal"},
		{"POST /api/v1/literal/{id}", "alpha", "change", true, "literal"},
		{"GET /api/v1/me/self", "", "", false, "literal"},
		{"PUT /api/v1/local", "alpha", "manage", false, "variable"},
		{"GET /api/v1/const", "alpha", "view", false, "variable"},
		{"DELETE /api/v1/joined/{id}", "alpha", "manage", true, "variable"},
		{"GET /api/v1/table", "gamma", "view", true, "table"},
		{"POST /api/v1/table", "gamma", "change", true, "table"},
		{"GET /api/v1/rows", "delta", "view", false, "table"},
		{"POST /api/v1/rows", "epsilon", "change", true, "table"},
		{"GET /api/v1/rows/{id}", "delta", "view", false, "table"},
		{"GET /api/v1/named", "alpha", "view", false, "table"},
		{"POST /api/v1/named", "beta", "manage", false, "table"},
		{"GET /api/v1/strings", "zeta", "view", false, "table"},
		{"GET /api/v1/strings/{id}", "zeta", "view", false, "table"},
		{"GET /api/v1/instance-less", "eta", "view", false, "literal"},
		{"GET /api/v1/never-registered", "eta", "view", false, "literal"},
	}
	for _, w := range want {
		r, ok := got[w.pattern]
		if !ok {
			t.Errorf("%s: not found", w.pattern)
			continue
		}
		if r.Module != w.module || r.Action != w.action || r.ObjectScoped != w.object || r.Registration != w.via {
			t.Errorf("%s: got %s.%s object=%v via=%s, want %s.%s object=%v via=%s",
				w.pattern, r.Module, r.Action, r.ObjectScoped, r.Registration, w.module, w.action, w.object, w.via)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d routes, want %d", len(got), len(want))
	}
	if s := res.Summary; s.MatrixRoutes != 16 || s.SelfOrComposite != 1 || s.MatrixWrites != 6 || s.Fatal != 0 {
		t.Errorf("summary %+v", s)
	}

	table := got["POST /api/v1/table"]
	if want := lineOf(t, "internal/forms/forms.go", `{"POST /api/v1/table"`); table.Source != want {
		t.Errorf("table source %s, want the row %s", table.Source, want)
	}
	if want := lineOf(t, "internal/forms/forms.go", `api.MarkV3ObjectRoute(route.pattern, "gamma"`); table.MarkSite != want {
		t.Errorf("table mark site %s, want %s", table.MarkSite, want)
	}
	if want := lineOf(t, "internal/forms/forms.go", `api.Register(route.pattern, "viewer", handler)`); len(table.RegisteredAt) != 1 || table.RegisteredAt[0] != want {
		t.Errorf("table registered at %v, want %s", table.RegisteredAt, want)
	}
	if !table.Write || got["GET /api/v1/table"].Write {
		t.Error("write flag")
	}
	if r := got["GET /api/v1/strings"]; len(r.RegisteredAt) != 1 {
		t.Errorf("registration under an unknown condition is still a registration: %v", r.RegisteredAt)
	}
}

func TestScanLegacyAndExempt(t *testing.T) {
	res := scanFixture(t, "basic")
	legacy := patterns(res.LegacyOnly)
	want := []string{
		"DELETE /api/v1/old/resource", "GET /api/v1/legacy/thing", "GET /api/v1/old/one", "GET /api/v1/old/param",
		"GET /api/v1/old/two", "PATCH /api/v1/old/resource", "POST /api/v1/old/one", "POST /api/v1/old/two",
	}
	if strings.Join(legacy, ",") != strings.Join(want, ",") {
		t.Errorf("legacy only:\n got %v\nwant %v", legacy, want)
	}
	exempt := map[string]string{}
	for _, r := range res.Exempt {
		exempt[r.Pattern] = r.Exempt
		if len(r.RegisteredAt) == 0 {
			t.Errorf("%s: no registration site", r.Pattern)
		}
	}
	wantExempt := map[string]string{
		"POST /api/v1/auth/login": "auth_or_instance", "GET /api/v1/auth/me": "auth_or_instance",
		"POST /api/v1/events/alert": "signed_push",
	}
	if fmt.Sprint(exempt) != fmt.Sprint(wantExempt) {
		t.Errorf("exempt %v, want %v", exempt, wantExempt)
	}
	kinds := map[string]int{}
	for _, d := range res.Diagnostics {
		kinds[d.Kind]++
	}
	if kinds["marked_not_registered"] != 1 || len(res.Diagnostics) != 1 {
		t.Errorf("diagnostics %+v", res.Diagnostics)
	}
}

func TestScanUncertainAssignment(t *testing.T) {
	res := scanFixture(t, "basic")
	for _, r := range append(res.LegacyOnly, res.Exempt...) {
		if strings.HasPrefix(r.Pattern, "GET /api/v1/uncertain") {
			t.Errorf("%s: a value assigned under an unknown condition must not be trusted", r.Pattern)
		}
	}
}

func TestScanFatalDiagnostics(t *testing.T) {
	res := scanFixture(t, "broken", "internal")
	kinds := map[string]int{}
	for _, d := range res.Diagnostics {
		kinds[d.Kind]++
	}
	if kinds["unresolved_mark"] != 2 || kinds["conflicting_marks"] != 1 || res.Summary.Fatal != 3 {
		t.Errorf("diagnostics %+v", res.Diagnostics)
	}
}

// TestRepository guards the scanner against the real source: every mark must
// resolve, and a few routes registered through tables keep their grants.
func TestRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("scans the whole repository")
	}
	res, err := Scan(filepath.Join("..", "..", ".."), []string{"internal", "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diagnostics {
		if fatalKinds[d.Kind] {
			t.Errorf("%s %s: %s", d.Kind, d.Site, d.Message)
		}
	}
	got := routeMap(res)
	for pattern, want := range map[string]string{
		"POST /api/v1/proposals/{id}/reject":        "proposals.manage object",
		"POST /api/v1/runbooks":                     "knowledge.manage route",
		"POST /api/v1/runbooks/{id}/submit":         "knowledge.manage object",
		"GET /api/v1/canvases/{id}":                 "canvas.view route",
		"POST /api/v1/investigations/{id}/branches": "canvas.change object",
		"GET /api/v1/notification-templates":        "alert_rules.view route",
		"POST /api/v1/maintenance-windows":          "events.change object",
		"POST /api/v1/promotions":                   "environment.view object",
		"GET /api/v1/audit/exports/{id}":            "audit.view object",
		"POST /api/v1/knowledge/search":             "knowledge.view object",
	} {
		r, ok := got[pattern]
		kind := "route"
		if r.ObjectScoped {
			kind = "object"
		}
		if !ok || r.Module+"."+r.Action+" "+kind != want {
			t.Errorf("%s: got %+v, want %s", pattern, r, want)
		}
	}
	exempt := map[string]bool{}
	for _, r := range res.Exempt {
		exempt[r.Pattern] = true
	}
	for _, p := range []string{"POST /api/v1/auth/login", "POST /api/v1/events/alert", "POST /api/v1/webhooks/gitlab/{source}"} {
		if !exempt[p] {
			t.Errorf("%s not exempt", p)
		}
	}
	if len(res.Routes) < 250 {
		t.Errorf("only %d V3 routes", len(res.Routes))
	}
}
