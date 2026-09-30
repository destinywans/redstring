package operations

import (
	"context"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
)

func TestIncidentDedupOrderIsolationAndAtomicInvestigation(t *testing.T) {
	s := isolatedAutomation(t)
	ctx := context.Background()
	s.Config = Config{Channels: []Channel{{ID: "group-a", ApplicationID: "a", EnvironmentID: "e1"}}, PublicURL: "https://workspace.example.invalid"}
	source := Source{ID: "prom", Mappings: []Mapping{{Labels: map[string]string{"service": "one"}, ApplicationID: "a", EnvironmentID: "e1"}}}
	start := time.Now().UTC().Add(-time.Minute)
	alert := Alert{Status: "firing", Fingerprint: "same", StartsAt: start, Labels: map[string]string{"service": "one", "alertname": "ErrorRate"}}
	for range 2 {
		if e := s.Ingest(ctx, source, alert); e != nil {
			t.Fatal(e)
		}
	}
	op := core.User{ID: "op", Role: "operator"}
	items, e := s.Incidents(ctx, op, "", "")
	if e != nil || len(items) != 1 {
		t.Fatalf("dedup: %d %v", len(items), e)
	}
	v, e := s.CreateInvestigation(ctx, items[0].ID, op)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.CreateInvestigation(ctx, v.ID, op)
	if e != nil || again.InvestigationID != v.InvestigationID {
		t.Fatal("investigation not idempotent", e)
	}
	alert.Status = "resolved"
	alert.EndsAt = time.Now().UTC()
	if e = s.Ingest(ctx, source, alert); e != nil {
		t.Fatal(e)
	}
	alert.Status = "firing"
	alert.Annotations = map[string]string{"summary": "late duplicate"}
	if e = s.Ingest(ctx, source, alert); e != nil {
		t.Fatal(e)
	}
	current, e := s.incident(ctx, v.ID)
	if e != nil || current.AlertStatus != "resolved" || current.Status != "investigating" {
		t.Fatalf("wrong alert/work state %+v %v", current, e)
	}
	var notifications int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM ops_notifications").Scan(&notifications); e != nil || notifications != 2 {
		t.Fatalf("notifications %d %v", notifications, e)
	}
	// Unknown and ambiguous labels remain in the administrator queue.
	unknown := alert
	unknown.Fingerprint = "unknown"
	unknown.Labels = map[string]string{"service": "other"}
	if e = s.Ingest(ctx, source, unknown); e != nil {
		t.Fatal(e)
	}
	items, e = s.Incidents(ctx, op, "", "")
	if e != nil || len(items) != 1 {
		t.Fatal("unassigned incident leaked", e)
	}
	all, e := s.Incidents(ctx, core.User{ID: "admin", Role: "admin"}, "", "")
	if e != nil || len(all) != 2 {
		t.Fatal("admin unassigned queue missing", e)
	}
	if _, e = s.DB.Exec(ctx, "DELETE FROM rbac_user_roles WHERE user_id='op' AND role_id='auto-fixture'"); e != nil {
		t.Fatal(e)
	}
	items, e = s.Incidents(ctx, op, "", "")
	if e != nil || len(items) != 0 {
		t.Fatal("revoked incident still visible", e)
	}
	if _, e = s.CreateInvestigation(ctx, v.ID, op); e == nil {
		t.Fatal("revoked direct incident action allowed")
	}
}
func TestNotificationOutboxTerminalWithoutExternalSend(t *testing.T) {
	s := isolatedAutomation(t)
	ctx := context.Background()
	_, e := s.DB.Exec(ctx, "INSERT INTO ops_notifications(id,application_id,environment_id,channel,event_key,body,attempts) VALUES('n','a','e1','removed','x','summary',4)")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.deliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	var tries int
	if e = s.DB.QueryRow(ctx, "SELECT status,attempts FROM ops_notifications WHERE id='n'").Scan(&state, &tries); e != nil || state != "failed" || tries != 5 {
		t.Fatalf("outbox state %s %d %v", state, tries, e)
	}
}

func TestNotificationRelayOnlyAutomaticInitialRun(t *testing.T) {
	s := isolatedAutomation(t)
	ctx := context.Background()
	s.Config = Config{Channels: []Channel{{ID: "group-a", ApplicationID: "a", EnvironmentID: "e1"}}, PublicURL: "https://workspace.example.invalid"}
	_, err := s.DB.Exec(ctx, `INSERT INTO investigations(id,application_id,environment_id,title) VALUES('inv','a','e1','Test');
INSERT INTO branches(id,investigation_id,title) VALUES('branch','inv','Root');
INSERT INTO context_revisions(branch_id,revision,messages) VALUES('branch',0,'[]');
INSERT INTO runs(id,branch_id,request_id,request_hash,status,model_id,input) VALUES('auto','branch','automation:initial','unused','completed','fake','{}'),('manual','branch','manual-followup','unused','completed','fake','{}');
INSERT INTO ops_automation_runs(id,environment_id,kind,investigation_id,run_id,policy) VALUES('initial','e1','initial','inv','','{}');
INSERT INTO events(investigation_id,branch_id,type,payload) VALUES('inv','branch','run.completed','{"run_id":"auto"}'),('inv','branch','run.completed','{"run_id":"manual"}');`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.relayEvents(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count, receipts int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM ops_notifications").Scan(&count); err != nil || count != 1 {
		t.Fatalf("manual follow-up or replay sent notification: %d %v", count, err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM ops_event_receipts").Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("event receipts: %d %v", receipts, err)
	}
}
