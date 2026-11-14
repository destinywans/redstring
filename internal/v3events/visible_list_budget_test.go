package v3events

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEventListsBudgetOnlyVisibleRecords(t *testing.T) {
	f := newListClaimFixture(t)
	f.svc.UseV3ListPrefilter = true
	f.svc.RegisterPageOperations(f.api)
	NewBulk(f.db, f.policy, nil).Register(f.api)
	ctx := t.Context()
	exec := func(sql string) {
		t.Helper()
		if _, err := f.db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO applications(id,name,key,environment) VALUES('hidden','Hidden','hidden','dev');
	 INSERT INTO application_environments(id,application_id,key,name) VALUES('hidden-dev','hidden','dev','Dev');
	 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('hidden','dev');
	 UPDATE v3_role_grants SET scope_kind='application',scope_id='app'
	  WHERE role_id='list-event-reader' AND module='events';
	 INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,updated_at,labels)
	 SELECT 'hidden-alert-'||n,'fixture','hidden','hidden-dev','hidden-fingerprint','FORBIDDEN ALERT','warning','firing',now()-n*interval '1 second',now(),
	 '{"application":"hidden","environment":"dev"}' FROM generate_series(1,5001) n;
	 INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,actor_id,request_key,request_hash)
	 SELECT 'hidden-case-'||n,'hidden','hidden-dev','dev','FORBIDDEN CASE','P2','alice','synthetic-hidden-'||n,repeat('a',64) FROM generate_series(1,5001) n;
	 INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,actor_id,request_key,request_hash)
	 VALUES('visible-case','app','app-dev','dev','Visible case','P2','alice','synthetic-visible',repeat('a',64));
	 INSERT INTO v3_item_assignments(item_kind,item_id,application_id,environment_id,assignee_id,actor_id)
	 SELECT 'alert','hidden-alert-'||n,'hidden','dev','alice','bob' FROM generate_series(1,501) n;
	 INSERT INTO v3_item_assignments(item_kind,item_id,application_id,environment_id,assignee_id,actor_id)
	 VALUES('incident','visible-case','app','dev','alice','bob');
	 INSERT INTO v3_assignment_notifications(id,item_kind,item_id,recipient_id,actor_id,created_at)
	 SELECT 'notification-'||item_id,item_kind,item_id,'alice','bob',updated_at FROM v3_item_assignments;
	 INSERT INTO v3_maintenance_plans(id,application_id,environment_id,schedule,matchers,reason,owner_id)
	 SELECT 'hidden-plan-'||n,'hidden','dev','Tomorrow','[]','FORBIDDEN PLAN','alice' FROM generate_series(1,501) n;
	 INSERT INTO v3_maintenance_plans(id,application_id,environment_id,schedule,matchers,reason,owner_id)
	 VALUES('visible-plan','app','dev','Tomorrow','[]','Visible plan','alice');
	 INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,status,created_at,body)
	 SELECT 'hidden-note-'||n,'hidden-alert-'||n,'hidden','hidden-dev','fixture','hidden-note-'||n,'sent',now(),'FORBIDDEN NOTE' FROM generate_series(1,501) n;
	 INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,status,created_at,body)
	 VALUES('visible-note','incident','app','app-dev','fixture','visible-note','sent',now(),'Visible note');`)
	visibleGroup := localAlertGroupID(incidentRecord{EnvironmentID: "app-dev", ApplicationID: "app", SourceID: "fixture", Fingerprint: "fingerprint"})
	for _, tc := range []struct {
		path, contains string
	}{
		{"/api/v1/incidents", "Visible case"},
		{"/api/v1/inbox", "Authorized old title"},
		{"/api/v1/inbox?filter=mine", "assignment:notification-visible-case"},
		{"/api/v1/alert-groups", visibleGroup},
		{"/api/v1/alert-groups/" + visibleGroup + "/policy", visibleGroup},
		{"/api/v1/maintenance-windows", "visible-plan"},
		{"/api/v1/notification-deliveries?q=sent", `"alert":"incident"`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := f.request(ctx, http.MethodGet, tc.path, f.tokens[0], "")
			if body := w.Body.String(); w.Code != 200 || !strings.Contains(body, tc.contains) || strings.Contains(body, "FORBIDDEN") || strings.Contains(body, "hidden-alert-") {
				t.Fatalf("hidden rows exhausted the budget or leaked into the response: %d %s", w.Code, body)
			}
		})
	}
	// The custom callback path also counts after authorization when SQL
	// admission is deliberately disabled.
	f.svc.UseV3ListPrefilter = false
	w := f.request(ctx, http.MethodGet, "/api/v1/inbox", f.tokens[0], "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Authorized old title") {
		t.Errorf("injected authorizer lost the visible alert behind hidden rows: %d %s", w.Code, w.Body.String())
	}
	f.svc.UseV3ListPrefilter = true
	w = f.request(ctx, http.MethodPost, "/api/v1/alert-groups/bulk", f.tokens[0], `{"ids":["`+visibleGroup+`"],"action":"claim"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"changed":1`) {
		t.Errorf("hidden groups exhausted the bulk budget: %d %s", w.Code, w.Body.String())
	}
	exec(`UPDATE v3_role_grants SET scope_kind='environment',scope_id='dev'
	 WHERE role_id='list-event-reader' AND module='events';
	 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
	 VALUES('list-event-reader','canvas','change','application','app')`)
	w = f.request(ctx, http.MethodPost, "/api/v1/alert-groups/bulk", f.tokens[0], `{"ids":["`+visibleGroup+`"],"action":"investigate"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"investigation_id":"INV-`) {
		t.Errorf("groups outside the canvas change scope exhausted the investigate budget: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/incidents", ""},
		{http.MethodGet, "/api/v1/inbox", ""},
		{http.MethodGet, "/api/v1/alert-groups", ""},
		{http.MethodGet, "/api/v1/alert-groups/" + visibleGroup + "/policy", ""},
		{http.MethodPost, "/api/v1/alert-groups/bulk", `{"ids":["` + visibleGroup + `"],"action":"claim"}`},
		{http.MethodGet, "/api/v1/maintenance-windows", ""},
		{http.MethodGet, "/api/v1/notification-deliveries?q=sent", ""},
	} {
		w := f.request(ctx, tc.method, tc.path, f.tokens[0], tc.body)
		var body struct{ Error struct{ Code string } }
		if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != "pagination_required" {
			t.Errorf("%s: visible overflow should remain explicit, got %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}
