package v3ingestpush

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestWebhookViewProjectsOnlyVisibleRealPushReceipts(t *testing.T) {
	f := setupPush(t, 0)
	f.s.RegisterPublic(f.api)
	if got := pushBrowserRequest(f, http.MethodGet, "/api/v1/webhooks", "dev", "en-US", ""); got.Code != http.StatusOK {
		t.Fatalf("empty receipt view status=%d body=%s", got.Code, got.Body.String())
	} else {
		var view webhookView
		if err := json.Unmarshal(got.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Inbound) != 2 || len(view.Deliveries) != 0 || view.Inbound[0].Status != "unverified" ||
			view.Inbound[0].Count != "0" || view.Inbound[0].LastAt != nil || view.Inbound[0].LastCode != nil ||
			view.Availability.APITokens != "not_connected" ||
			view.Availability.NativeInbound != "not_connected" || view.Availability.PublicMCP != "not_connected" ||
			view.Availability.Outbound != "not_connected" || strings.Contains(got.Body.String(), "source-b") {
			t.Fatalf("unobserved or cross-environment source was misrepresented: %s", got.Body.String())
		}
	}
	if got := callPush(f, signedPush(f, "change", "webhooks-real", eventBody("change", "webhooks-real"))); got.Code != http.StatusAccepted {
		t.Fatalf("real ingress status=%d body=%s", got.Code, got.Body.String())
	}
	got := pushBrowserRequest(f, http.MethodGet, "/api/v1/webhooks", "dev", "en-US", "")
	if got.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", got.Code, got.Body.String())
	}
	var view webhookView
	if err := json.Unmarshal(got.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Inbound) != 2 || len(view.Deliveries) != 1 {
		t.Fatalf("unexpected webhook view: %s", got.Body.String())
	}
	var change *webhookInbound
	for i := range view.Inbound {
		if view.Inbound[i].EventType == "change" {
			change = &view.Inbound[i]
		}
	}
	// PA-83: the latest receipt comes only as code and RFC 3339; the "last"
	// text is gone.
	if change == nil || change.LastAt == nil || change.LastCode == nil || *change.LastCode != 202 ||
		strings.Contains(got.Body.String(), `"last":`) || mustRFC3339(t, *change.LastAt).IsZero() {
		t.Fatalf("latest receipt: %s", got.Body.String())
	}
	if change.Status != "ok" || change.Count != "1" ||
		change.URL != "/api/v1/events/change" || change.StatusBasis != "latest_receipt_in_window" ||
		view.Deliveries[0].Code != "202" || strings.Contains(got.Body.String(), "source-b") ||
		strings.Contains(got.Body.String(), f.token) || strings.Contains(got.Body.String(), f.secret) {
		t.Fatalf("real receipt or credential boundary incorrect: %s", got.Body.String())
	}
	if stage := pushBrowserRequest(f, http.MethodGet, "/api/v1/webhooks", "stage", "en-US", ""); stage.Code != http.StatusOK {
		t.Fatalf("stage scoped view status=%d body=%s", stage.Code, stage.Body.String())
	} else if strings.Contains(stage.Body.String(), "source-a") || !strings.Contains(stage.Body.String(), "source-b") {
		t.Fatalf("stage view crossed application environment: %s", stage.Body.String())
	}
	if _, err := f.db.Exec(t.Context(), `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if denied := pushBrowserRequest(f, http.MethodGet, "/api/v1/webhooks", "dev", "en-US", ""); denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), "source-a") {
		t.Fatalf("revoked connector view leaked source: %d %s", denied.Code, denied.Body.String())
	}
}

// PA-227: each delivery names the inbound entry it belongs to, and a busy
// entry can't push another entry's receipts out of the view.
func TestWebhookDeliveriesNameTheirEntryAndAreBoundedPerEntry(t *testing.T) {
	f := setupPush(t, 0)
	f.s.RegisterPublic(f.api)
	if _, err := f.db.Exec(t.Context(), `INSERT INTO v3_push_receipts(source_id,provider,application_id,environment_id,event_type,key_hash,code,result,latency_ms,synthetic,created_at)
 VALUES('source-a','generic','app-a','dev','alert','alert-key',422,'invalid_event',4,false,now()-interval '2 hours')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(t.Context(), `INSERT INTO v3_push_receipts(source_id,provider,application_id,environment_id,event_type,key_hash,code,result,latency_ms,synthetic,created_at)
 SELECT 'source-a','generic','app-a','dev','change','change-key-'||n,202,'accepted',3,false,now()-n*interval '1 second' FROM generate_series(1,40) n`); err != nil {
		t.Fatal(err)
	}
	got := pushBrowserRequest(f, http.MethodGet, "/api/v1/webhooks", "dev", "en-US", "")
	if got.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", got.Code, got.Body.String())
	}
	var view struct {
		Deliveries []struct {
			Time      string `json:"time"`
			SourceID  string `json:"source_id"`
			EventType string `json:"event_type"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	perEntry := map[string]int{}
	for _, d := range view.Deliveries {
		if d.SourceID != "source-a" || (d.EventType != "change" && d.EventType != "alert") {
			t.Fatalf("delivery without its entry: %+v", d)
		}
		perEntry[d.SourceID+":"+d.EventType]++
	}
	if perEntry["source-a:alert"] != 1 || perEntry["source-a:change"] != 30 {
		t.Fatalf("deliveries per entry = %v, want 1 alert and 30 change: %s", perEntry, got.Body.String())
	}
	for i := 1; i < len(view.Deliveries); i++ {
		if view.Deliveries[i-1].Time < view.Deliveries[i].Time {
			t.Fatalf("deliveries not newest first: %s", got.Body.String())
		}
	}
}
