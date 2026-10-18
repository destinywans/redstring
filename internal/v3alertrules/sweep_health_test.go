package v3alertrules

import (
	"encoding/json"
	"net/http"
	"testing"
)

// PA-210: a rule is "ok" only when Prometheus evaluated it without error. A
// failing evaluation is "error" (its alert state is stale, so it is not
// counted as firing) and a rule Prometheus has not evaluated is "unknown";
// the detail carries the provider's last error.
func TestRuleStateFollowsPrometheusHealth(t *testing.T) {
	rule := func(name, state, health, lastError string) ObservedRule {
		return ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: name, Expression: "up", State: state,
			Health: health, LastError: lastError, Labels: map[string]string{"environment": "test", "application": "app"}}
	}
	rules := []ObservedRule{
		rule("Healthy", "inactive", "ok", ""),
		rule("Broken", "inactive", "err", "query timed out"),
		rule("BrokenFiring", "firing", "err", "query timed out"),
		rule("Fresh", "inactive", "unknown", ""),
		rule("Unreported", "inactive", "", ""),
		rule("Firing", "firing", "ok", ""),
		rule("Pending", "pending", "ok", ""),
	}
	s := mustService(t, fakeCatalog{{ID: "app", EnvironmentID: "test"}}, fakeAccess{}, fakeSource{rules: map[string][]ObservedRule{"test": rules}},
		RuleGroupBinding{EnvironmentID: "test", ApplicationID: "app", File: testRuleFile, Group: "g"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/alert-rules", s.MainList)
	mux.HandleFunc("GET /api/v1/alert-rules/{id}", s.MainDetail)
	mux.HandleFunc("GET /api/v1/alert-rules/observed/{id}", s.Detail)
	handler := scopedHandler(t, mux)

	w := call(t, handler, "GET", "/api/v1/alert-rules", "test")
	var list struct {
		Items  []struct{ ID, Name, State string } `json:"items"`
		Counts map[string]int                     `json:"counts"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	want := map[string]string{"Healthy": "ok", "Broken": "error", "BrokenFiring": "error", "Fresh": "unknown", "Unreported": "unknown",
		"Firing": "firing", "Pending": "pending"}
	ids := map[string]string{}
	for _, item := range list.Items {
		ids[item.Name] = item.ID
		if item.State != want[item.Name] {
			t.Errorf("%s: state %q, want %q", item.Name, item.State, want[item.Name])
		}
	}
	if len(list.Items) != len(want) || list.Counts["all"] != len(want) || list.Counts["firing"] != 1 {
		t.Fatalf("items or counts: %s", w.Body.String())
	}

	var detail struct {
		State     string  `json:"state"`
		LastError *string `json:"last_error"`
	}
	for name, lastError := range map[string]string{"Broken": "query timed out", "Healthy": ""} {
		w = call(t, handler, "GET", "/api/v1/alert-rules/"+ids[name], "test")
		detail.LastError = nil
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.State != want[name] {
			t.Fatalf("%s detail: %d %s", name, w.Code, w.Body.String())
		}
		if lastError == "" && detail.LastError != nil || lastError != "" && (detail.LastError == nil || *detail.LastError != lastError) {
			t.Fatalf("%s last_error: %s", name, w.Body.String())
		}
	}
	w = call(t, handler, "GET", "/api/v1/alert-rules/observed/"+ids["Fresh"], "test")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.State != "unknown" {
		t.Fatalf("observed detail: %d %s", w.Code, w.Body.String())
	}
}
