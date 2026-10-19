package v3serviceviews

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
)

// A newly registered service has no profile, owner, metric binding, alert
// coverage or deployment. It is listed with nulls and reasons, never as a
// healthy, zero-alert or never-released service, and it does not fail the
// catalog for the services that do have evidence.
func TestServiceViewsNewServiceIsPartialNotFailed(t *testing.T) {
	st, _ := serviceTestDB(t)
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	seedServiceViews(t, st, now)
	for _, sql := range []string{
		`INSERT INTO applications(id,key,name,description,environment) VALUES('orders-app','orders','Orders','Orders API','test')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('orders-test','orders-app','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('orders-app','test')`,
	} {
		if _, err := st.DB.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	reader := &completeReader{now: now}
	covered, _ := configuredAlertCoverage(t, st)
	svc := New(st, policy, reader, covered)
	svc.Now = func() time.Time { return now }
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	list := func(query string) catalog {
		t.Helper()
		w := get("/services" + query)
		if w.Code != 200 {
			t.Fatalf("list %q status=%d body=%s", query, w.Code, w.Body.String())
		}
		var c catalog
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	names := func(c catalog) string {
		out := []string{}
		for _, item := range c.Items {
			out = append(out, item.Name)
		}
		return strings.Join(out, ",")
	}

	c := list("?sort=name")
	if names(c) != "Checkout,Orders,Payments" {
		t.Fatalf("new service changed the catalog membership: %s", names(c))
	}
	orders := c.Items[1]
	want := map[string]string{
		"tier": reasonNotConfigured, "team": reasonNotConfigured, "oncall": reasonNotConfigured,
		"slo.target": reasonNotConfigured, "slo.budget_left": reasonNotConnected, "connectors": reasonNotConnected,
		"status": reasonNotConnected, "alerts": reasonNotConfigured, "release": reasonNotConnected,
	}
	if orders.Tier != nil || orders.Team != nil || orders.Oncall != nil || orders.Status != nil || orders.Alerts != nil ||
		orders.Release != nil || orders.Connectors != nil || orders.SLO.Target != nil || orders.SLO.BudgetLeft != nil ||
		len(orders.Unavailable) != len(want) {
		t.Fatalf("new service values were not all unknown: %+v", orders)
	}
	for field, reason := range want {
		if orders.Unavailable[field] != reason {
			t.Fatalf("%s reason=%q want %q: %+v", field, orders.Unavailable[field], reason, orders.Unavailable)
		}
	}
	raw := get("/services?q=orders").Body.String()
	for _, fabricated := range []string{`"alerts":0`, `"status":"ok"`, `"budget_left":0`, `"connectors":[]`, `"oncall":""`} {
		if strings.Contains(raw, fabricated) {
			t.Fatalf("unknown rendered as %s: %s", fabricated, raw)
		}
	}
	if !strings.Contains(raw, `"status":null`) || !strings.Contains(raw, `"release":null`) {
		t.Fatalf("unknown fields must be explicit nulls: %s", raw)
	}

	// KPIs count known values only and report how many services were left out.
	for _, k := range []struct {
		id, value string
		unknown   int
	}{{"n", "3", 0}, {"bad", "1", 1}, {"warn", "1", 1}, {"rel", "2", 1}} {
		if got := kpiOf(c, k.id); str(got.Value) != k.value || got.Unknown != k.unknown || got.Reason != "" {
			t.Fatalf("KPI %s=%+v want value %s unknown %d", k.id, got, k.value, k.unknown)
		}
	}
	for _, f := range c.Facets {
		total := 0
		for _, item := range f.Items {
			total += item.Count
			if item.Label == "" {
				t.Fatalf("facet %s has an unknown bucket: %+v", f.Key, f.Items)
			}
		}
		if (f.Key == "env" && total != 3) || (f.Key != "env" && total != 2) {
			t.Fatalf("facet %s counted unknown values: %+v", f.Key, f.Items)
		}
	}
	// A KPI with no known input is null with the shared reason, not zero.
	only := list("?q=orders")
	for id, reason := range map[string]string{"bad": reasonNotConnected, "warn": reasonNotConfigured,
		"budget": reasonNotConnected, "rel": reasonNotConnected} {
		if got := kpiOf(only, id); got.Value != nil || got.Reason != reason || got.Unknown != 1 {
			t.Fatalf("all-unknown KPI %s=%+v want null %s", id, got, reason)
		}
	}
	if kpiValue(only, "n") != "1" {
		t.Fatalf("service count is always known: %+v", only.KPIs)
	}

	// The status filter matches known statuses only; sorting puts unknowns last.
	if got := names(list("?status=ok")); got != "Payments" {
		t.Fatalf("status filter matched an unknown status: %s", got)
	}
	for _, sortKey := range []string{"status", "budget", "alerts"} {
		if got := names(list("?sort=" + sortKey)); !strings.HasSuffix(got, ",Orders") {
			t.Fatalf("sort=%s did not put unknowns last: %s", sortKey, got)
		}
	}

	w := get("/services/" + encodeID("orders-app", "test"))
	if w.Code != 200 {
		t.Fatalf("new service detail status=%d body=%s", w.Code, w.Body.String())
	}
	var d detail
	if err = json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	for field, reason := range map[string]string{
		"tier": reasonNotConfigured, "team": reasonNotConfigured, "status": reasonNotConnected, "ai": reasonNotGenerated,
		"signals": reasonNotConnected, "slo.availability": reasonNotConnected, "slo.target": reasonNotConfigured,
		"slo.budget_left": reasonNotConnected, "slo.burn": reasonNotConnected,
	} {
		if d.Unavailable[field] != reason {
			t.Fatalf("detail %s reason=%q want %q: %+v", field, d.Unavailable[field], reason, d.Unavailable)
		}
	}
	// Local records that genuinely hold nothing are known empties.
	if d.AI != nil || d.Signals != nil || d.SLO.Availability != nil || d.SLO.Burn != nil || d.Status != nil ||
		d.Changes == nil || len(d.Changes) != 0 || d.Deps == nil || len(d.Deps.Upstream)+len(d.Deps.Downstream) != 0 ||
		num(d.Tabs["changes"]) != 0 || num(d.Tabs["deps"]) != 0 || num(d.Tabs["incidents"]) != 0 || len(d.Unavailable) != 9 {
		t.Fatalf("new service detail: %+v", d)
	}
	if !strings.Contains(w.Body.String(), `"env":"test"`) || !strings.Contains(w.Body.String(), `"ai":null`) {
		t.Fatalf("detail shape: %s", w.Body.String())
	}

	// A dependency with unknown status keeps the edge and explains its status.
	for _, sql := range []string{
		`INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('res-orders','service','orders','manual')`,
		`INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES('res-orders','orders-app','orders-test')`,
		`INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind)
 VALUES('checkout-orders','res-checkout','res-orders','depends_on','declared','manual')`,
	} {
		if _, err = st.DB.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	d = getDetail(t, get, "checkout-app")
	if d.Deps == nil || len(d.Deps.Downstream) != 2 || num(d.Tabs["deps"]) != 2 {
		t.Fatalf("checkout dependencies: %+v", d.Deps)
	}
	for _, dep := range d.Deps.Downstream {
		switch dep.Name {
		case "Orders":
			if dep.Status != nil || dep.Unavailable["status"] != reasonNotConnected {
				t.Fatalf("unknown dependency status: %+v", dep)
			}
		case "Payments":
			if str(dep.Status) != "ok" || len(dep.Unavailable) != 0 {
				t.Fatalf("known dependency status: %+v", dep)
			}
		default:
			t.Fatalf("unexpected dependency %+v", dep)
		}
	}
}

// Readers without separate grants get the identity they may see and an
// explicit no_authorized_target for the rest; no source is read for them.
func TestServiceViewsMissingGrantsAreUnknown(t *testing.T) {
	st, _ := serviceTestDB(t)
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	seedServiceViews(t, st, now)
	ctx := t.Context()
	for _, sql := range []string{
		`INSERT INTO rbac_roles(id,name) VALUES('service-only','Service Only')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('service-only','environment','view','environment','test'),
 ('service-only','services','view','environment','test')`,
	} {
		if _, err := st.DB.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	user, err := st.CreateUser(ctx, core.User{Email: "service-only@example.invalid", Name: "Service Only", Role: "viewer"}, "reader-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'service-only')`, user.ID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	reader := &completeReader{now: now}
	svc := New(st, policy, reader, func(_ context.Context, _, env string) string { return "service-alerts" })
	svc.Now = func() time.Time { return now }
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "service-only@example.invalid", "reader-password")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := get("/services")
	if w.Code != 200 || len(reader.calls) != 0 || strings.Contains(w.Body.String(), "prometheus") {
		t.Fatalf("services-only catalog status=%d reads=%d body=%s", w.Code, len(reader.calls), w.Body.String())
	}
	var c catalog
	if err = json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	for _, item := range c.Items {
		for _, field := range []string{"status", "alerts", "slo.budget_left", "connectors"} {
			if item.Unavailable[field] != reasonNoAuthorized {
				t.Fatalf("%s reason=%q: %+v", field, item.Unavailable[field], item)
			}
		}
		if str(item.Tier) == "null" || str(item.SLO.Target) != "99.9%" || item.Release == nil {
			t.Fatalf("visible configured values were dropped: %+v", item)
		}
	}
	d := getDetail(t, get, "checkout-app")
	for field, reason := range map[string]string{"ai": reasonNoAuthorized, "deps": reasonNoAuthorized,
		"tabs.deps": reasonNoAuthorized, "tabs.incidents": reasonNoAuthorized, "signals": reasonNoAuthorized} {
		if d.Unavailable[field] != reason {
			t.Fatalf("detail %s reason=%q: %+v", field, d.Unavailable[field], d.Unavailable)
		}
	}
	if d.AI != nil || d.Deps != nil || d.Tabs["incidents"] != nil || strings.Contains(get("/services/"+encodeID("checkout-app", "test")).Body.String(), "INV-checkout") {
		t.Fatalf("ungranted detail evidence leaked: %+v", d)
	}
}

func TestCatalogKPIsCountKnownValuesOnly(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	danger, ok, team := "danger", "ok", "Ops"
	one, zero := 1, 0
	low, high := 10.0, 90.0
	known := func(status *string, alerts *int, budget *float64) summary {
		item := summary{Env: "test", Team: &team, Status: status, Alerts: alerts, Unavailable: map[string]string{},
			Release: &release{At: now.Add(-time.Hour)}}
		item.SLO.BudgetLeft = budget
		return item
	}
	blank := summary{Env: "test", Unavailable: map[string]string{"status": reasonUnavailable, "alerts": reasonNotConfigured,
		"slo.budget_left": reasonUnavailable, "release": reasonNotConnected, "team": reasonNotConfigured}}
	c := buildCatalog([]summary{known(&danger, &one, &low), known(&ok, &zero, &high), blank, blank}, filters{}, now, english)
	for _, k := range []struct {
		id, value string
		unknown   int
	}{{"n", "4", 0}, {"bad", "1", 2}, {"warn", "1", 2}, {"budget", "1", 2}, {"rel", "2", 2}} {
		if got := kpiOf(c, k.id); str(got.Value) != k.value || got.Unknown != k.unknown || got.Reason != "" {
			t.Fatalf("KPI %s=%+v want %s/%d", k.id, got, k.value, k.unknown)
		}
	}
	c = buildCatalog([]summary{blank, blank}, filters{}, now, english)
	if got := kpiOf(c, "bad"); got.Value != nil || got.Reason != reasonUnavailable || got.Unknown != 2 {
		t.Fatalf("all-unknown KPI: %+v", got)
	}
	if got := kpiOf(c, "rel"); got.Value != nil || got.Reason != reasonNotConnected {
		t.Fatalf("all-unknown release KPI: %+v", got)
	}
	if c = buildCatalog(nil, filters{}, now, english); kpiValue(c, "bad") != "0" || kpiOf(c, "bad").Unknown != 0 {
		t.Fatalf("an empty catalog has no unknown inputs: %+v", c.KPIs)
	}
	ordered := []*int{nil, &one, &zero}
	if !nullsLast(ordered[1], ordered[0], func(x, y int) bool { return x > y }) ||
		nullsLast(ordered[0], ordered[2], func(x, y int) bool { return x > y }) ||
		nullsLast(ordered[0], ordered[0], func(x, y int) bool { return x > y }) {
		t.Fatal("unknown values must sort after known values")
	}
}
