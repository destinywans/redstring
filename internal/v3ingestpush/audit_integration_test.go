package v3ingestpush

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/v3audit"
)

func TestPushAuditVisibleWithExactEnvironmentAndOutcome(t *testing.T) {
	f := setupPush(t, 0)
	ctx := t.Context()
	if _, err := f.db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','audit','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	v3audit.New(f.db, []string{"dev", "stage"}).Register(f.api)
	for _, tc := range []struct {
		key  string
		body []byte
		want int
	}{
		{"accepted", eventBody("change", "audit-accepted"), 202},
		{"accepted", eventBody("change", "audit-accepted"), 409},
		{"invalid", []byte(`{"schema":"change.v1"}`), 422},
	} {
		got := callPush(f, signedPush(f, "change", tc.key, tc.body))
		if got.Code != tc.want {
			t.Fatalf("push status=%d want=%d body=%s", got.Code, tc.want, got.Body.String())
		}
	}
	testReq := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/push/test", strings.NewReader(`{"type":"change"}`))
	testReq.Header.Set("X-AIOps-Environment", "dev")
	testReq.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
	testResp := httptest.NewRecorder()
	f.api.ServeHTTP(testResp, testReq)
	if testResp.Code != 202 {
		t.Fatalf("synthetic push status=%d body=%s", testResp.Code, testResp.Body.String())
	}
	var scopedRows, integrationRows, syntheticRows int
	if err := f.db.QueryRow(ctx, `SELECT count(*),
	 count(*) FILTER (WHERE actor_kind='integration' AND actor_id='source-a'),
	 count(*) FILTER (WHERE actor_kind='user' AND action='v3.push.test_accepted')
	 FROM management_audit WHERE action LIKE 'v3.push.%'
	 AND application_id='app-a' AND changes->>'environment_id'='dev'
	 AND changes ? 'source_id' AND changes ? 'receipt_id'
	 AND NOT (changes ?| ARRAY['token','secret','body','attributes'])`).Scan(&scopedRows, &integrationRows, &syntheticRows); err != nil {
		t.Fatal(err)
	}
	if scopedRows != 4 || integrationRows != 3 || syntheticRows != 1 {
		t.Fatalf("Push audit provenance rows=%d integration=%d synthetic=%d", scopedRows, integrationRows, syntheticRows)
	}

	list := func(env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
		w := httptest.NewRecorder()
		f.api.ServeHTTP(w, r)
		return w
	}
	w := list("dev")
	if w.Code != 200 {
		t.Fatalf("audit list status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Items []struct {
			ID     string                      `json:"id"`
			Actor  struct{ Kind, Name string } `json:"actor"`
			Action string                      `json:"action"`
			Result string                      `json:"result"`
		} `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 4 || got.Counts["all"] != 4 || got.Counts["system"] != 3 || got.Counts["user"] != 1 {
		t.Fatalf("audit list missing Push rows: %+v", got)
	}
	want := map[string]string{
		"v3.push.accepted":      "ok",
		"v3.push.duplicate":     "blocked",
		"v3.push.rejected":      "blocked",
		"v3.push.test_accepted": "ok",
	}
	for _, item := range got.Items {
		if item.Result != want[item.Action] {
			t.Fatalf("audit action %q result %q", item.Action, item.Result)
		}
		delete(want, item.Action)
		if item.Action == "v3.push.test_accepted" {
			if item.Actor.Kind != "user" {
				t.Fatalf("synthetic actor = %+v", item.Actor)
			}
		} else if item.Actor.Kind != "system" || item.Actor.Name != "source-a" {
			t.Fatalf("integration actor = %+v", item.Actor)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/audit/"+item.ID, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
		detail := httptest.NewRecorder()
		f.api.ServeHTTP(detail, r)
		if detail.Code != 200 || !strings.Contains(detail.Body.String(), `"chain_ok":true`) ||
			strings.Contains(detail.Body.String(), f.token) || strings.Contains(detail.Body.String(), f.secret) ||
			strings.Contains(detail.Body.String(), "audit-accepted") {
			t.Fatalf("audit detail status=%d body=%s", detail.Code, detail.Body.String())
		}
		if _, err := strconv.ParseInt(item.ID, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing audit actions: %v", want)
	}
	if stage := list("stage"); stage.Code != 200 || strings.Contains(stage.Body.String(), "v3.push.") {
		t.Fatalf("other environment saw dev Push audit: status=%d body=%s", stage.Code, stage.Body.String())
	}
	if _, err := f.db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='audit' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if revoked := list("dev"); revoked.Code != 403 {
		t.Fatalf("revoked audit status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}
