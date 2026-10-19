package v3audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

func isolatedAuditStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops only a random isolated schema")
	}
	ctx := t.Context()
	admin, err := store.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_audit_test_" + store.ID()
	if _, err := admin.DB.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.DB.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	s, err := store.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuditRegisteredHTTPScopesListAndVerifiedDetail(t *testing.T) {
	s := isolatedAuditStore(t)
	ctx := t.Context()
	password := "isolated-audit-test-password"
	_, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
	 ('audit-admin','audit-admin@example.invalid','Audit Admin','admin',$1),
	 ('audit-viewer','audit-viewer@example.invalid','Audit Viewer','viewer',$1)`, store.PasswordHash(password))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, "audit-admin@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := s.Login(ctx, "audit-viewer@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	var devID, testID, otherID, globalID int64
	for _, row := range []struct {
		id   *int64
		kind string
		name string
		env  string
	}{
		{&devID, "user", "部署变更", "dev"},
		{&testID, "agent", "triage_test", "test"},
		{&otherID, "user", "hidden_other", "other"},
		{&globalID, "system", "workspace_change", ""},
	} {
		changes := `{"environment_id":"` + row.env + `"}`
		if row.id == &devID {
			changes = `{"environment_id":"dev","credential":"private-test-value"}`
		}
		if row.env == "" {
			changes = `{}`
		}
		if err := s.DB.QueryRow(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes,created_at)
		 VALUES('audit-admin',$1,$2,'target','success',$3::jsonb,CASE WHEN $2='部署变更' THEN now()-interval '2 hours' ELSE now() END) RETURNING id`, row.kind, row.name, changes).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: s}
	api := httpapi.New(s, nil, true)
	New(s.DB, []string{"dev", "test"}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, policy, s.Session,
		func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(authToken, env, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authToken != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: authToken})
		}
		if env != "" {
			r.Header.Set("X-AIOps-Environment", env)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
	}
	assertStatus(request("", "dev", "/api/v1/audit"), http.StatusUnauthorized)
	assertStatus(request(viewerToken, "dev", "/api/v1/audit"), http.StatusForbidden)
	assertStatus(request(token, "other", "/api/v1/audit"), http.StatusUnprocessableEntity)
	w := request(token, "dev", "/api/v1/audit")
	assertStatus(w, http.StatusOK)
	var list struct {
		Items  []entry          `json:"items"`
		Counts map[string]int64 `json:"counts"`
		Volume [][]int64        `json:"volume"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].ID != strconv.FormatInt(globalID, 10) || list.Items[1].ID != strconv.FormatInt(devID, 10) ||
		list.Counts["all"] != 2 || list.Counts["user"] != 1 || list.Counts["system"] != 1 || len(list.Volume) != 7 {
		t.Fatalf("unexpected scoped audit list: %+v", list)
	}
	for _, rangeValue := range []string{"15m", "24h"} {
		w = request(token, "dev", "/api/v1/audit?range="+rangeValue+"&action="+url.QueryEscape("部署变更")+"&result=ok&result=failed")
		assertStatus(w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		want := 0
		if rangeValue == "24h" {
			want = 1
		}
		if len(list.Items) != want || list.Counts["all"] != int64(want) {
			t.Fatalf("page audit filters %s: %+v", rangeValue, list)
		}
		n := int64(0)
		for _, bucket := range list.Volume {
			for _, count := range bucket {
				n += count
			}
		}
		if n != int64(want) {
			t.Fatalf("volume ignores facets: %v", list.Volume)
		}
	}
	w = request(token, "dev", "/api/v1/audit?filter=user&q=bushubian")
	assertStatus(w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != strconv.FormatInt(devID, 10) {
		t.Fatalf("filtered audit list=%+v err=%v", list, err)
	}
	w = request(token, "dev", "/api/v1/audit/"+strconv.FormatInt(devID, 10))
	assertStatus(w, http.StatusOK)
	var detail struct {
		ID      string `json:"id"`
		ChainOK bool   `json:"chain_ok"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.ID != strconv.FormatInt(devID, 10) || !detail.ChainOK {
		t.Fatalf("audit detail=%+v err=%v", detail, err)
	}
	if detail.Payload == "" || !json.Valid([]byte(detail.Payload)) || strings.Contains(detail.Payload, "private-test-value") {
		t.Fatalf("invalid redacted payload: %q", detail.Payload)
	}
	assertStatus(request(token, "dev", "/api/v1/audit/"+strconv.FormatInt(testID, 10)), http.StatusNotFound)
	assertStatus(request(token, "dev", "/api/v1/audit/"+strconv.FormatInt(otherID, 10)), http.StatusNotFound)
	assertStatus(request(token, "dev", "/api/v1/audit/invalid"), http.StatusNotFound)
	export := func(authToken, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/audit/export", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: authToken})
		r.Header.Set("X-AIOps-Environment", env)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertStatus(export(token, "all", `{"filter":"user"}`), http.StatusConflict)
	w = export(token, "dev", `{"filter":"user"}`)
	assertStatus(w, http.StatusOK)
	var exported struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &exported); err != nil || !strings.HasPrefix(exported.URL, "/api/v1/audit/exports/") {
		t.Fatalf("export response=%s err=%v", w.Body.String(), err)
	}
	assertStatus(request(viewerToken, "", exported.URL), http.StatusNotFound)
	assertStatus(request(token, "test", exported.URL), http.StatusForbidden)
	w = request(token, "", exported.URL)
	assertStatus(w, http.StatusOK)
	if got := w.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("export content type=%q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "部署变更") || strings.Contains(body, "triage_test") || strings.Contains(body, "hidden_other") || strings.Contains(body, "private-test-value") {
		t.Fatalf("incorrect scoped CSV: %q", body)
	}
	// PA-170: an export holds the list's time range.
	for _, c := range []struct {
		rangeValue string
		want       bool
	}{{"15m", false}, {"24h", true}} {
		w = export(token, "dev", `{"filter":"user","range":"`+c.rangeValue+`"}`)
		assertStatus(w, http.StatusOK)
		var ranged struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &ranged); err != nil {
			t.Fatal(err)
		}
		w = request(token, "", ranged.URL)
		assertStatus(w, http.StatusOK)
		if strings.Contains(w.Body.String(), "部署变更") != c.want {
			t.Fatalf("export range %s: %q", c.rangeValue, w.Body.String())
		}
	}
	assertStatus(export(token, "dev", `{"filter":"user","range":"2h"}`), http.StatusUnprocessableEntity)
	// A lifecycle row is "pending" and an unclassifiable outcome degrades only
	// its own row to a null result (PA-42) instead of failing the whole log.
	var pendingID, oddID int64
	for _, row := range []struct {
		id      *int64
		outcome string
	}{{&pendingID, "pending"}, {&oddID, "half_done"}} {
		if err := s.DB.QueryRow(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
		 VALUES('audit-admin','system','v3.silence_create.dispatching','silence',$1,'{"environment_id":"dev"}') RETURNING id`, row.outcome).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	w = request(token, "dev", "/api/v1/audit")
	assertStatus(w, http.StatusOK)
	var lifecycle struct {
		Items []entry `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lifecycle); err != nil || len(lifecycle.Items) != 4 {
		t.Fatalf("lifecycle rows: %s", w.Body.String())
	}
	if odd := lifecycle.Items[0]; odd.ID != strconv.FormatInt(oddID, 10) || odd.Result != nil || odd.Unavailable["result"] != "unclassified" {
		t.Fatalf("unmapped row: %+v", odd)
	}
	if pending := lifecycle.Items[1]; pending.ID != strconv.FormatInt(pendingID, 10) || pending.Result == nil || *pending.Result != "pending" || pending.Unavailable != nil {
		t.Fatalf("pending row: %+v", pending)
	}
	w = request(token, "dev", "/api/v1/audit?result=pending")
	if err := json.Unmarshal(w.Body.Bytes(), &lifecycle); err != nil || w.Code != 200 || len(lifecycle.Items) != 1 {
		t.Fatalf("pending filter: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='audit' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(request(token, "dev", "/api/v1/audit"), http.StatusForbidden)
	assertStatus(request(token, "", exported.URL), http.StatusForbidden)
}

// PA-230: lifecycle writers record outcomes beyond the canonical set. Each one
// the instances hold reads as its own result, and the result filter selects
// exactly the rows the list shows with that result. A rollout step's outcome
// is needs_attention for every state but reserved, succeeded and failed
// (v3rollouts outcomeOf), so its progress steps read by their action.
func TestAuditClassifiesLifecycleOutcomes(t *testing.T) {
	s := isolatedAuditStore(t)
	ctx := t.Context()
	password := "isolated-audit-test-password"
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
	 ('audit-admin','audit-admin@example.invalid','Audit Admin','admin',$1)`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, "audit-admin@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: s}
	api := httpapi.New(s, nil, true)
	New(s.DB, []string{"dev"}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, s.Session,
		func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	list := func(query string) []entry {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/audit"+query, nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		r.Header.Set("X-AIOps-Environment", "dev")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		var body struct {
			Items []entry `json:"items"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("GET /audit%s: %d %s", query, w.Code, w.Body.String())
		}
		return body.Items
	}
	rows := []struct{ action, outcome, want string }{
		{"v3.investigation_canvas.tool", "started", "pending"},
		{"v3.investigation_canvas.reply_started", "started", "pending"},
		{"v3.canvas_node.reply_started", "started", "pending"},
		{"v3.alert_rule.execution.rechecked", "requested", "ok"},
		{"runbook.reindex.requested", "requested", "ok"},
		{"v3.knowledge_source.connection.test", "attempted", "pending"},
		{"v3.knowledge_source.connection.test", "authorization_failed", "blocked"},
		{"v3.knowledge_source.connection.test", "scope_changed", "blocked"},
		{"v3.alert_rule.enable.execution_state", "pending_review", "pending"},
		{"v3.alert_rule.enable.execution_state", "merged", "pending"},
		{"v3.alert_rule.enable.execution_state", "awaiting_provider", "pending"},
		{"v3.alert_rule.enable.execution_state", "uncertain", "unconfirmed"},
		{"v3.alert_rule.enable.execution_state", "stale_remote", "needs_attention"},
		{"v3.alert_rule.enable.observed", "awaiting_provider", "pending"},
		{"v3.alert_rule.enable.observed", "enabled", "ok"},
		{"v3.alert_rule.enable.observed", "disabled", "ok"},
		{"v3.rollout.claimed", "needs_attention", "needs_attention"},
		{"v3.rollout.dispatching", "needs_attention", "pending"},
		{"v3.rollout.applied", "needs_attention", "pending"},
		{"v3.rollout.rolling_out", "needs_attention", "pending"},
		{"v3.rollout.verifying", "needs_attention", "pending"},
		{"v3.rollout.unknown", "needs_attention", "unconfirmed"},
		{"v3.rollout.needs_attention", "needs_attention", "needs_attention"},
		{"v3.rollout.succeeded", "success", "ok"},
		{"v3.ai.rollback_suggested", "suggested", "ok"},
		{"v3.ai.rollback_suggested", "declined", "ok"},
		{"v3.ai.rollback_suggested", "rejected", "blocked"},
		{"v3.ai.rollback_suggested", "unavailable", "failed"},
		{"v3.check.unclassifiable", "half_done", ""},
	}
	want := map[string]string{}
	for _, row := range rows {
		var id int64
		if err := s.DB.QueryRow(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
		 VALUES('audit-admin','system',$1,'pa230',$2,'{"environment_id":"dev"}') RETURNING id`, row.action, row.outcome).Scan(&id); err != nil {
			t.Fatal(err)
		}
		want[strconv.FormatInt(id, 10)] = row.want
	}
	byResult := map[string][]string{}
	for _, item := range list("") {
		result := ""
		if item.Result != nil {
			result = *item.Result
		}
		if result != want[item.ID] {
			t.Errorf("%s: result %q, want %q", item.Action, result, want[item.ID])
		}
		if item.Result == nil && item.Unavailable["result"] != "unclassified" {
			t.Errorf("%s: an unclassifiable outcome says %q, not that it is unclassified", item.Action, item.Unavailable["result"])
		}
		byResult[result] = append(byResult[result], item.ID)
	}
	if len(byResult[""]) != 1 {
		t.Fatalf("unclassified rows: %v", byResult[""])
	}
	for _, result := range []string{"ok", "pending", "blocked", "failed", "unconfirmed", "needs_attention"} {
		var got []string
		for _, item := range list("?result=" + result) {
			got = append(got, item.ID)
		}
		if strings.Join(got, ",") != strings.Join(byResult[result], ",") {
			t.Errorf("result=%s selects %v; the list shows %v with that result", result, got, byResult[result])
		}
	}
}
