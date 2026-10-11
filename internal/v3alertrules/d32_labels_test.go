package v3alertrules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3webhook"
)

// D32: rules carry the application key the alert source mappings use. A
// rule the product writes for checkout-api, once Prometheus fires it, maps
// to checkout-api through the prod instance's real ingestion; the same rule
// written for test and routed to prod is not stored there.
func TestProductRuleAlertMapsToItsServiceAndStaysInItsEnvironment(t *testing.T) {
	db := enableRecoveryDB(t)
	ctx := t.Context()
	st := &store.Store{DB: db}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	appID := store.ID()
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,key,name,environment) VALUES($1,'checkout-api','Checkout API','prod')`, appID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO application_environments(id,application_id,key,name) VALUES('checkout-prod',$1,'prod','Prod')`, appID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,'prod')`, appID); err != nil {
		t.Fatal(err)
	}
	group := "redstring-prod-checkout-api"
	file := "/etc/redstring-rules/current/rules/prod/checkout-api.yml"
	reader := mustService(t, fakeCatalog{{ID: appID, EnvironmentID: "prod"}}, fakeAccess{}, fakeSource{},
		RuleGroupBinding{EnvironmentID: "prod", ApplicationID: appID, File: file, Group: group, ApplicationLabel: "checkout-api"},
		RuleGroupBinding{EnvironmentID: "test", ApplicationID: appID, File: "/etc/redstring-rules/current/rules/test/checkout-api.yml",
			Group: "redstring-test-checkout-api", ApplicationLabel: "checkout-api"})
	writer := NewWriteService(db, &recoveryAccess{}, reader, nil)

	// The request names the application; the server adds its scope labels.
	product := func(env, rule string) []GroupRule {
		t.Helper()
		spec := writer.withScopeLabels(DraftRuleSpec{ApplicationID: appID, EnvironmentID: env, Group: "redstring-" + env + "-checkout-api",
			Name: rule, Expr: "vector(1) > 0", For: "1m", Mode: "draft", Labels: map[string]string{"severity": "P2"}}, env)
		if spec.Labels["application"] != "checkout-api" || spec.Labels["environment"] != env {
			t.Fatalf("scope labels: %v", spec.Labels)
		}
		if err := validateDraftSpec(spec, env, appID, reader.ApplicationLabel(env, appID)); err != nil {
			t.Fatal(err)
		}
		content, err := renderDeployableRule([]byte("groups:\n- name: "+spec.Group+"\n  rules: []\n"), spec)
		if err != nil {
			t.Fatal(err)
		}
		rules, err := groupRules(content, spec.Group)
		if err != nil || len(rules) != 1 {
			t.Fatalf("rendered: %s %v", content, err)
		}
		return rules
	}
	token := strings.Repeat("t", 32)
	tokenFile := filepath.Join(t.TempDir(), "source-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	// The prod instance's real mapping (operations.json): key + redstring_env.
	service, err := v3webhook.New(db, httpapi.V3InstanceConfig{Tier: "production", EnvironmentIDs: []string{"prod"}},
		operations.Config{Sources: []operations.Source{{ID: "lab-alertmanager", TokenFile: tokenFile, Mappings: []operations.Mapping{{
			Labels: map[string]string{"application": "checkout-api", "redstring_env": "prod"}, ApplicationID: appID, EnvironmentID: "checkout-prod"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil, false)
	api.RegisterPublic(v3webhook.Pattern, service.Handler)
	fire := func(rule GroupRule, extra map[string]string) map[string]any {
		t.Helper()
		// Prometheus adds alertname; the lab relabeling adds redstring_env
		// to every alert of the VM cluster, whichever group it comes from.
		labels := map[string]string{"alertname": rule.Name, "redstring_env": "prod"}
		for k, v := range rule.Labels {
			labels[k] = v
		}
		for k, v := range extra {
			labels[k] = v
		}
		body, _ := json.Marshal(operations.AlertEnvelope{Alerts: []operations.Alert{{Status: "firing", Labels: labels,
			StartsAt: time.Now().UTC().Truncate(time.Second), Fingerprint: fmt.Sprintf("%x", time.Now().UnixNano())}}})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager/lab-alertmanager", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		var out map[string]any
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("webhook: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	incidents := func() (int, string) {
		var n int
		var app string
		if err := db.QueryRow(ctx, `SELECT count(*),COALESCE(max(application_id),'') FROM ops_incidents`).Scan(&n, &app); err != nil {
			t.Fatal(err)
		}
		return n, app
	}

	if out := fire(product("prod", "CheckoutErrors")[0], nil); out["accepted"] != float64(1) || out["ignored"] != float64(0) {
		t.Fatalf("prod rule: %v", out)
	}
	if n, app := incidents(); n != 1 || app != appID {
		t.Fatalf("the product rule's alert did not map to checkout-api: %d %s", n, app)
	}
	// A test rule's alert that reaches prod is not stored there.
	if out := fire(product("test", "CheckoutErrorsTest")[0], nil); out["accepted"] != float64(0) || out["ignored"] != float64(1) {
		t.Fatalf("test rule in prod: %v", out)
	}
	// An existing lab alert without an environment label still maps.
	if out := fire(GroupRule{Name: "LabAlert", Labels: map[string]string{"application": "checkout-api"}}, nil); out["accepted"] != float64(1) {
		t.Fatalf("lab alert: %v", out)
	}
	if n, _ := incidents(); n != 2 {
		t.Fatalf("incidents: %d", n)
	}
	// D33: the lab's own alerts inherit environment="lab" from their
	// metrics (the real label set of LabHigh5xxRate below). Without the
	// lab's alert relabeling prod does not store them; with it they map to
	// checkout-api.
	lab := map[string]string{"application": "checkout-api", "cluster": "aiops-vm-cluster", "environment": "lab",
		"namespace": "aiops-lab", "severity": "warning"}
	if out := fire(GroupRule{Name: "LabHigh5xxRate", Labels: lab}, nil); out["accepted"] != float64(0) || out["ignored"] != float64(1) {
		t.Fatalf("lab alert without relabeling: %v", out)
	}
	if out := fire(GroupRule{Name: "LabHigh5xxRate", Labels: labAlertRelabel(lab)}, nil); out["accepted"] != float64(1) || out["ignored"] != float64(0) {
		t.Fatalf("relabeled lab alert: %v", out)
	}
	if n, _ := incidents(); n != 3 {
		t.Fatalf("incidents: %d", n)
	}
	var relabeled string
	if err := db.QueryRow(ctx, `SELECT application_id FROM ops_incidents WHERE title='LabHigh5xxRate'`).Scan(&relabeled); err != nil || relabeled != appID {
		t.Fatalf("relabeled lab alert: %s %v", relabeled, err)
	}
	// A product test rule's alert keeps environment="test" through the
	// relabeling, so prod still does not store it.
	if out := fire(product("test", "CheckoutErrorsTestRelabeled")[0], labAlertRelabel(map[string]string{"cluster": "aiops-vm-cluster"})); out["ignored"] != float64(1) {
		t.Fatalf("test rule through the relabeling: %v", out)
	}
}

// labAlertRelabel applies the lab Prometheus alert_relabel_configs (D33):
// every alert of the VM cluster gets redstring_env="prod", and its
// environment="lab" becomes environment="prod".
func labAlertRelabel(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	if out["cluster"] == "aiops-vm-cluster" {
		out["redstring_env"] = "prod"
		if out["environment"] == "lab" {
			out["environment"] = "prod"
		}
	}
	return out
}

// D32: the rule list shows rules labelled with the bound key (the lab's
// placeholder included), and a label maps back to one application.
func TestApplicationLabelBinding(t *testing.T) {
	rule := func(app, name string) ObservedRule {
		return ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: name, Expression: "up", State: "inactive",
			Labels: map[string]string{"environment": "test", "application": app}}
	}
	s := mustService(t, fakeCatalog{{ID: "app-id", EnvironmentID: "test"}}, fakeAccess{}, fakeSource{rules: map[string][]ObservedRule{
		"test": {rule("checkout-api", "RedstringRuleGroupPlaceholder"), rule("app-id", "LabelledByID")},
	}}, RuleGroupBinding{EnvironmentID: "test", ApplicationID: "app-id", File: testRuleFile, Group: "g", ApplicationLabel: "checkout-api"})
	w := call(t, scopedHandler(t, http.HandlerFunc(s.List)), "GET", "/api/v1/alert-rules/observed", "test")
	var body struct {
		Items []Summary `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Items) != 1 || body.Items[0].Name != "RedstringRuleGroupPlaceholder" ||
		body.Items[0].ApplicationID != "app-id" {
		t.Fatalf("list by key: %d %s", w.Code, w.Body.String())
	}
	if app, ok := s.applicationOfLabel("test", "checkout-api"); !ok || app != "app-id" {
		t.Fatalf("label to application: %s %v", app, ok)
	}
	if s.ApplicationLabel("test", "app-id") != "checkout-api" || s.ApplicationLabel("test", "unbound") != "unbound" {
		t.Fatal("application label")
	}
	// Import maps the YAML's key label to the application.
	specs, err := parseRuleImportYAML("groups:\n- name: g\n  rules:\n  - alert: Imported\n    expr: up == 0\n    for: 1m\n    labels:\n      application: checkout-api\n      environment: test\n      severity: P2\n", "test", "", s)
	if err != nil || len(specs) != 1 || specs[0].ApplicationID != "app-id" {
		t.Fatalf("import by key: %+v %v", specs, err)
	}
	if _, err := parseRuleImportYAML("groups:\n- name: g\n  rules:\n  - alert: Imported\n    expr: up == 0\n    labels:\n      application: app-id\n      environment: test\n      severity: P2\n", "test", "", s); err == nil {
		t.Fatal("an ID label was imported for a key-labelled application")
	}
	// One application has one label per environment, and one label one application.
	for name, bindings := range map[string][]RuleGroupBinding{
		"two labels": {{EnvironmentID: "test", ApplicationID: "a", File: "f1", Group: "g", ApplicationLabel: "x"},
			{EnvironmentID: "test", ApplicationID: "a", File: "f2", Group: "g", ApplicationLabel: "y"}},
		"shared label": {{EnvironmentID: "test", ApplicationID: "a", File: "f1", Group: "g", ApplicationLabel: "x"},
			{EnvironmentID: "test", ApplicationID: "b", File: "f2", Group: "g", ApplicationLabel: "x"}},
	} {
		if _, err := New(fakeCatalog{}, fakeAccess{}, fakeSource{}, bindings); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
