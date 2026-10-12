package v3canvas

import (
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/operations"
)

// D11 §4.4: an investigation opened from incidents reads changes from before
// the earliest one started: an alert's start or a manual incident's creation.
func TestInvestigationAlertStart(t *testing.T) {
	_, st := suggestionDB(t)
	ctx := t.Context()
	svc := New(st)
	if _, ok := svc.investigationAlertStart(ctx, "inv"); ok {
		t.Fatal("an investigation without an incident has an alert start")
	}
	if err := operations.New(st.DB, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.investigationAlertStart(ctx, "inv"); ok {
		t.Fatal("an investigation without an incident has an alert start")
	}
	alert := time.Now().UTC().Add(-40 * time.Minute).Truncate(time.Second)
	if _, err := st.DB.Exec(ctx, `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,investigation_id)
 VALUES('incident','lab','app','app-test','incident','LabHigh5xxRate','critical','firing',$1,'inv')`, alert); err != nil {
		t.Fatal(err)
	}
	if at, ok := svc.investigationAlertStart(ctx, "inv"); !ok || !at.Equal(alert) {
		t.Fatalf("alert start: %s %v", at, ok)
	}
	if _, ok := svc.investigationAlertStart(ctx, "other"); ok {
		t.Fatal("another investigation has this alert start")
	}
}
