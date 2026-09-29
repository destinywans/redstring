package v3ingestpush

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pushBrowserRequest(f pushFixture, method, path, env, language, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-AIOps-Environment", env)
	r.Header.Set("Accept-Language", language)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}

func waitForPushGovernanceLock(t *testing.T, f pushFixture, blockerPID int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := f.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE state='active' AND wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid))
 AND query LIKE '%pg_advisory_xact_lock_shared%')`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Push request did not reach governance lock")
}

func TestPushViewLanguageAndQueuedRevocation(t *testing.T) {
	f := setupPush(t, 0)
	if got := callPush(f, signedPush(f, "change", "view-localize", eventBody("change", "view-localize"))); got.Code != 202 {
		t.Fatalf("push status=%d body=%s", got.Code, got.Body.String())
	}
	for _, tc := range []struct {
		language, typeName, response, delivery, header string
	}{
		{"zh-CN", "变更", "已接收并持久化", "已接收", "来源与应用、环境固定绑定"},
		{"en-US", "Changes", "Accepted and persisted", "Accepted", "Fixed source-to-application/environment mapping"},
	} {
		got := pushBrowserRequest(f, http.MethodGet, "/api/v1/ingest/push", "dev", tc.language, "")
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"name":"`+tc.typeName+`"`) ||
			!strings.Contains(got.Body.String(), tc.response) || !strings.Contains(got.Body.String(), tc.header) ||
			!strings.Contains(got.Body.String(), `"result":"`+tc.delivery+`"`) {
			t.Fatalf("language %s status=%d body=%s", tc.language, got.Code, got.Body.String())
		}
	}
	ctx := t.Context()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
		t.Fatal(err)
	}
	var blockerPID int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- pushBrowserRequest(f, http.MethodGet, "/api/v1/ingest/push", "dev", "en-US", "") }()
	waitForPushGovernanceLock(t, f, blockerPID)
	if _, err := blocker.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-result; got.Code != 403 || strings.Contains(got.Body.String(), "view-localize") {
		t.Fatalf("revoked Push view status=%d body=%s", got.Code, got.Body.String())
	}
}

func TestPushTestRechecksSourceUniquenessAfterLockWait(t *testing.T) {
	f := setupPush(t, 0)
	ctx := t.Context()
	second := f.s.sources["source-b"]
	second.EnvironmentID = "dev"
	f.s.sources["source-b"] = second
	if _, err := f.db.Exec(ctx, `UPDATE application_environments SET archived=true WHERE id='app-b-dev'`); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
		t.Fatal(err)
	}
	var blockerPID int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- pushBrowserRequest(f, http.MethodPost, "/api/v1/ingest/push/test", "dev", "en-US", `{"type":"change"}`)
	}()
	waitForPushGovernanceLock(t, f, blockerPID)
	if _, err := blocker.Exec(ctx, `UPDATE application_environments SET archived=false WHERE id='app-b-dev'`); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-result; got.Code != 409 || !strings.Contains(got.Body.String(), "source_selection_changed") {
		t.Fatalf("ambiguous Push test status=%d body=%s", got.Code, got.Body.String())
	}
	var events, audits int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM v3_push_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.push.%'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if events != 0 || audits != 0 {
		t.Fatalf("ambiguous test wrote events=%d audits=%d", events, audits)
	}
}
