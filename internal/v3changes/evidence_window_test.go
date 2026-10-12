package v3changes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
)

// D11 §4.4: in an investigation opened from an incident, the default window
// is from 2 hours before the alert started until now, at most 24 hours; an
// explicit window and an ordinary conversation are unchanged.
func TestChangesToolAlertStartWindow(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	window := func(input string, alert time.Time) (time.Time, time.Time) {
		t.Helper()
		_, since, until, ok := changesToolInput(input, now, alert)
		if !ok {
			t.Fatalf("%q refused", input)
		}
		return since, until
	}
	alert := now.Add(-40 * time.Minute)
	if since, until := window("checkout", alert); !since.Equal(alert.Add(-2*time.Hour)) || !until.Equal(now) {
		t.Fatalf("from the alert: %s %s", since, until)
	}
	if since, until := window("checkout", time.Time{}); !since.Equal(now.Add(-2*time.Hour)) || !until.Equal(now) {
		t.Fatalf("ordinary: %s %s", since, until)
	}
	if since, _ := window("checkout", now.Add(-30*time.Hour)); !since.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("an old alert is cut to 24 hours: %s", since)
	}
	explicit := `{"service":"checkout","since":"` + now.Add(-5*time.Hour).Format(time.RFC3339) +
		`","until":"` + now.Add(-4*time.Hour).Format(time.RFC3339) + `"}`
	if since, until := window(explicit, alert); !since.Equal(now.Add(-5*time.Hour)) || !until.Equal(now.Add(-4*time.Hour)) {
		t.Fatalf("explicit: %s %s", since, until)
	}
}

// The investigation's tool says where its default window starts and reads
// from there.
func TestReadServiceChangesFromTheAlertStart(t *testing.T) {
	f := newChangesFixture(t)
	alert := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	tool, ok := f.service.EvidenceTool(ChangesToolOptions{OwnerID: f.viewer.ID, EnvironmentID: "dev", AlertStartedAt: alert,
		CurrentUser: func(context.Context, string) (core.User, error) { return f.viewer, nil },
		Lookup: func(_ context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			if key != "checkout" || env != "dev" {
				return v3catalog.ServiceEvidence{}, store.ErrNotFound
			}
			return v3catalog.ServiceEvidence{ID: f.serviceID, Key: "checkout", Environment: "dev"}, nil
		}})
	if !ok || !strings.Contains(tool.Description, alert.Format(time.RFC3339)) {
		t.Fatalf("description: %v %s", ok, tool.Description)
	}
	evidence, err := tool.Read(t.Context(), "checkout")
	if err != nil {
		t.Fatal(err)
	}
	var got changesEvidence
	if err := json.Unmarshal([]byte(evidence.Content), &got); err != nil || got.Since != alert.Add(-2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("window: %v %s %s", err, got.Since, got.Until)
	}
}
