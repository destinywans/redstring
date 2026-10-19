package v3postmortems

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// PA-247: postmortems read an incident's severity through the shared alert
// severity table. An ops incident whose severity is outside it, or whose
// alert carried none (PA-240's old "warning" default), is listed with an
// unknown severity instead of failing the candidate list, the draft or the
// record lists; a value the table knows (error) gets its level.
func TestPostmortemsReadIncidentSeverityThroughTheSharedTable(t *testing.T) {
	f := newPostmortemTextFixture(t)
	for _, statement := range []string{
		`INSERT INTO investigations(id,application_id,title,status)
 SELECT 'inv-'||id,'app',id,'closed' FROM unnest(ARRAY['odd','legacy','unlabelled','jsonnull','errored']) id`,
		`INSERT INTO action_plans(id,investigation_id,proposal,digest,created_by)
 SELECT 'plan-'||id,'inv-'||id,'{}','digest',$1 FROM unnest(ARRAY['odd','legacy','unlabelled','jsonnull','errored']) id`,
		`INSERT INTO executions(id,plan_id,status,created_by)
 SELECT 'exe-'||id,'plan-'||id,'succeeded',$1 FROM unnest(ARRAY['odd','legacy','unlabelled','jsonnull','errored']) id`,
		`INSERT INTO investigation_reports(investigation_id,execution_id,report)
 SELECT 'inv-'||id,'exe-'||id,'{}' FROM unnest(ARRAY['odd','legacy','unlabelled','jsonnull','errored']) id`,
		`INSERT INTO events(investigation_id,type,payload,created_at)
 SELECT 'inv-'||id,'investigation.closed','{}',now()-interval '1 hour' FROM unnest(ARRAY['odd','legacy','unlabelled','jsonnull','errored']) id`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at,investigation_id,labels) VALUES
 ('odd','am','app','app-dev','odd','Odd severity','major','resolved','resolved',now()-interval '2 hours','inv-odd','{"severity":"major"}'),
 ('legacy','am','app','app-dev','legacy','Legacy default','warning','resolved','resolved',now()-interval '2 hours','inv-legacy','{"alertname":"Legacy"}'),
 ('unlabelled','ai-patrol','app','app-dev','unlabelled','No recorded labels','warning','resolved','resolved',now()-interval '2 hours','inv-unlabelled',NULL),
 ('jsonnull','am','app','app-dev','jsonnull','JSON null labels','warning','resolved','resolved',now()-interval '2 hours','inv-jsonnull','null'),
 ('errored','am','app','app-dev','errored','Error severity','error','resolved','resolved',now()-interval '2 hours','inv-errored','{"severity":"error"}')`,
	} {
		args := []any{}
		if strings.Contains(statement, "$1") {
			args = append(args, f.userID)
		}
		if _, err := f.db.Exec(t.Context(), statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	type item struct {
		ID             string   `json:"id"`
		Severity       *string  `json:"severity"`
		SeverityReason *string  `json:"severity_reason"`
		Duration       *string  `json:"duration"`
		UnknownFields  []string `json:"unknown_fields"`
	}
	// want is a level, or "unknown:<reason>".
	want := map[string]string{"odd": "unknown:unclassified", "legacy": "unknown:not_filled", "unlabelled": "P2", "jsonnull": "P2", "errored": "P2", "INC-tl": "P2"}
	check := func(where string, v item, durationKnown bool) {
		t.Helper()
		expected, listed := want[v.ID]
		if !listed {
			return
		}
		got := ""
		switch {
		case v.Severity != nil && v.SeverityReason == nil:
			got = *v.Severity
		case v.Severity == nil && v.SeverityReason != nil:
			got = "unknown:" + *v.SeverityReason
		}
		if got != expected {
			t.Errorf("%s %s: severity=%v reason=%v; want %s", where, v.ID, v.Severity, v.SeverityReason, expected)
		}
		if unknown := expected[0] == 'u'; unknown != slices.Contains(v.UnknownFields, "severity") {
			t.Errorf("%s %s: unknown_fields=%v", where, v.ID, v.UnknownFields)
		}
		if durationKnown && v.Duration == nil {
			t.Errorf("%s %s: an unknown severity hid the measured duration", where, v.ID)
		}
	}

	var candidates struct {
		Items []item `json:"items"`
	}
	if err := json.Unmarshal(f.get(t, "/api/v1/postmortems/candidates", "zh-CN"), &candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates.Items) != len(want) {
		t.Fatalf("candidates=%+v; want every resolved incident listed", candidates.Items)
	}
	for _, v := range candidates.Items {
		check("candidate", v, v.ID != "INC-tl")
	}

	var draft struct {
		Incident item     `json:"incident"`
		Missing  []string `json:"missing"`
	}
	if err := json.Unmarshal(f.get(t, "/api/v1/postmortems/draft?incident=odd", "zh-CN"), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Incident.Severity != nil || draft.Incident.SeverityReason == nil || *draft.Incident.SeverityReason != "unclassified" ||
		!slices.Contains(draft.Missing, "incident.severity") {
		t.Errorf("draft incident=%+v missing=%v; want severity null, unclassified", draft.Incident, draft.Missing)
	}

	// Saved postmortems for the unknown ones: the record list and the
	// dashboard list say their severity is unknown.
	if _, err := f.db.Exec(t.Context(), `INSERT INTO v3_postmortems
 (id,incident_id,investigation_id,application_id,environment_id,close_event_seq,source_kind,sections,actions,status,created_by)
 SELECT 'pm-'||i.id,i.id,i.investigation_id,'app','dev',ev.seq,'ops','[]','[]','published',$1
 FROM ops_incidents i JOIN events ev ON ev.investigation_id=i.investigation_id AND ev.type='investigation.closed'
 WHERE i.id IN ('odd','legacy','errored')`, f.userID); err != nil {
		t.Fatal(err)
	}
	var records struct {
		Items []struct {
			item
			IncidentID string `json:"incident_id"`
		} `json:"items"`
	}
	r := httptest.NewRequest("GET", "/api/v1/postmortems/records?tab=all", nil)
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.token})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("records = %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records.Items) != 3 {
		t.Fatalf("records=%+v", records.Items)
	}
	for _, v := range records.Items {
		v.item.ID = v.IncidentID
		check("record", v.item, true)
	}

	var summary struct {
		Items []struct {
			item
			IncidentID string `json:"incident_id"`
		} `json:"items"`
		UnknownFields []string `json:"unknown_fields"`
	}
	if err := json.Unmarshal(f.get(t, "/api/v1/postmortems", "zh-CN"), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Items) != 3 || !slices.Contains(summary.UnknownFields, "items.severity") {
		t.Fatalf("summary items=%+v unknown=%v", summary.Items, summary.UnknownFields)
	}
	for _, v := range summary.Items {
		v.item.ID = v.IncidentID
		// The dashboard list keeps its page-level unknown_fields.
		v.item.UnknownFields = nil
		if v.Severity == nil {
			v.item.UnknownFields = []string{"severity"}
		}
		check("summary", v.item, true)
	}
}
