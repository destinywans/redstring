package v3alertmanager

import (
	"fmt"
	"net/http"
	"testing"
)

func TestListAlertStatesReadsIdentityAndSuppression(t *testing.T) {
	body := `[{"fingerprint":"a1","startsAt":"2026-10-10T01:00:00Z","labels":{"alertname":"x","secret":"y"},"status":{"state":"suppressed","silencedBy":["s1"],"inhibitedBy":[]}},
	{"fingerprint":"b2","startsAt":"2026-10-10T01:05:00Z","labels":{"alertname":"y"},"status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}]`
	client, _ := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alertmanager/api/v2/alerts" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, body)
	}, "")
	states, err := client.ListAlertStates(t.Context(), "dev")
	if err != nil || len(states) != 2 || states[0].Fingerprint != "a1" || !states[0].Silenced || states[0].Inhibited ||
		states[0].State != "suppressed" || states[1].State != "active" || states[1].StartsAt.IsZero() {
		t.Fatalf("states: %+v %v", states, err)
	}
	for name, bad := range map[string]string{
		"no fingerprint": `[{"startsAt":"2026-10-10T01:00:00Z","status":{"state":"active"}}]`,
		"no start":       `[{"fingerprint":"a","status":{"state":"active"}}]`,
		"unknown state":  `[{"fingerprint":"a","startsAt":"2026-10-10T01:00:00Z","status":{"state":"resolved"}}]`,
		"not an array":   `{"fingerprint":"a"}`,
	} {
		client, _ := localClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, bad) }, "")
		if _, err := client.ListAlertStates(t.Context(), "dev"); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := client.ListAlertStates(t.Context(), "prod"); err != ErrNotConfigured {
		t.Fatalf("unconfigured environment: %v", err)
	}
}
