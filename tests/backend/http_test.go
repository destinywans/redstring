package backend_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

func authenticatedClient(t *testing.T, f fixture) *http.Client {
	t.Helper()
	c := httpClient()
	c.Jar, _ = cookiejar.New(nil)
	expect(t, request(t, c, f.BaseURLs[0], "POST", "/api/v1/auth/login", "", map[string]any{"email": f.Admin.Email, "password": f.Admin.Password}, nil), 200)
	return c
}

func objectID(t *testing.T, r response) string {
	t.Helper()
	id, _ := r.Data["id"].(string)
	if id == "" {
		t.Fatal("successful create omitted durable object id")
	}
	return id
}

// Synthetic identities and incoming alert rows provide controlled data, while
// authentication, RBAC, business transactions and HTTP use the real servers.
// This is not evidence of connectivity to a real IdP/Alertmanager/LLM.
func seedHuman(t *testing.T, f fixture, db *pgxpool.Pool, grants [][2]string) *http.Client {
	t.Helper()
	id, role, token := store.ID(), store.ID(), store.ID()
	if _, err := db.Exec(t.Context(), `INSERT INTO users(id,email,name,role,password_hash) VALUES($1,$2,'Synthetic test human','viewer','unused')`, id, id+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM rbac_user_roles WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO rbac_roles(id,name) VALUES($1,$1);`, role); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, id, role); err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if _, err := db.Exec(t.Context(), `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES($1,$2,$3,'environment',$4)`, role, grant[0], grant[1], f.Environment); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, store.Hash(token), id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DELETE FROM sessions WHERE user_id=$1;`, id)
		if err != nil {
			t.Error(err)
		}
		_, err = db.Exec(context.Background(), `UPDATE users SET disabled=true WHERE id=$1`, id)
		if err != nil {
			t.Error(err)
		}
		_, err = db.Exec(context.Background(), `DELETE FROM rbac_user_roles WHERE user_id=$1`, id)
		if err != nil {
			t.Error(err)
		}
		_, err = db.Exec(context.Background(), `DELETE FROM rbac_roles WHERE id=$1`, role)
		if err != nil {
			t.Error(err)
		}
	})
	c := httpClient()
	c.Jar, _ = cookiejar.New(nil)
	for _, base := range f.BaseURLs {
		u, _ := url.Parse(base)
		c.Jar.SetCookies(u, []*http.Cookie{{Name: "aiops_session", Value: token, Path: "/"}})
	}
	return c
}

func TestFormalBackendHTTP(t *testing.T) {
	f := readFixture(t, true)
	db, err := pgxpool.New(t.Context(), f.DatabaseURL)
	if err != nil {
		t.Fatal("disposable app database unavailable")
	}
	defer db.Close()
	admin := authenticatedClient(t, f)
	a, b, env := f.BaseURLs[0], f.BaseURLs[1], f.Environment

	t.Run("AuthorizationAndSessionRevocation", func(t *testing.T) {
		for _, base := range f.BaseURLs {
			expect(t, request(t, httpClient(), base, "GET", "/api/v1/canvases", env, nil, nil), 401)
			expect(t, request(t, admin, base, "GET", "/api/v1/canvases", env, nil, nil), 200)
		}
		limited := seedHuman(t, f, db, [][2]string{{"environment", "view"}, {"canvas", "view"}})
		expect(t, request(t, limited, a, "GET", "/api/v1/canvases", env, nil, nil), 200)
		expect(t, request(t, limited, b, "POST", "/api/v1/canvases", env, map[string]any{"title": "Denied", "kind": "plan", "services": []string{}}, nil), 403)
		expect(t, request(t, limited, b, "GET", "/api/v1/canvases", f.OtherEnvironment, nil, nil), 403)
		u, _ := url.Parse(a)
		cookies := limited.Jar.Cookies(u)
		var token string
		for _, c := range cookies {
			if c.Name == "aiops_session" {
				token = c.Value
			}
		}
		if _, err := db.Exec(t.Context(), `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(token)); err != nil {
			t.Fatal(err)
		}
		expect(t, request(t, limited, b, "GET", "/api/v1/canvases", env, nil, nil), 401)
	})

	t.Run("WorkspaceBoundaryETagAcrossInstances", func(t *testing.T) {
		original := request(t, admin, a, "GET", "/api/v1/workspace", env, nil, nil)
		expect(t, original, 200)
		etag := original.Header.Get("ETag")
		if etag == "" {
			t.Fatal("workspace read omitted ETag")
		}
		changed := map[string]any{}
		expect(t, request(t, admin, a, "PUT", "/api/v1/workspace", env, original.Data, nil), 428)
		for k, v := range original.Data {
			changed[k] = v
		}
		changed["name"] = "Backend test " + store.ID()
		updated := request(t, admin, a, "PUT", "/api/v1/workspace", env, changed, map[string]string{"If-Match": etag})
		expect(t, updated, 200)
		t.Cleanup(func() {
			latest := request(t, admin, b, "GET", "/api/v1/workspace", env, nil, nil)
			expect(t, latest, 200)
			expect(t, request(t, admin, b, "PUT", "/api/v1/workspace", env, original.Data, map[string]string{"If-Match": latest.Header.Get("ETag")}), 200)
		})
		current := request(t, admin, b, "GET", "/api/v1/workspace", env, nil, nil)
		expect(t, current, 200)
		if current.Header.Get("ETag") == etag || current.Data["name"] != changed["name"] {
			t.Fatal("second instance lost committed workspace revision")
		}
		expect(t, request(t, admin, b, "PUT", "/api/v1/workspace", env, original.Data, map[string]string{"If-Match": etag}), 412)
	})

	t.Run("TokenAuthorizationIssueConsumeRevoke", func(t *testing.T) {
		issued := request(t, admin, a, "POST", "/api/v1/api-tokens", env, map[string]any{"name": "testlab " + store.ID(), "scopes": []string{"connectors:view"}, "ttl": 3600}, nil)
		expect(t, issued, 201)
		id := objectID(t, issued)
		secret, _ := issued.Data["secret_shown_once"].(string)
		if secret == "" {
			t.Fatal("one-time token secret missing")
		}
		t.Cleanup(func() { expect(t, request(t, admin, a, "DELETE", "/api/v1/api-tokens/"+id, env, nil, nil), 204) })
		listed := request(t, admin, b, "GET", "/api/v1/api-tokens", env, nil, nil)
		expect(t, listed, 200)
		if strings.Contains(string(listed.Raw), secret) || strings.Contains(string(listed.Raw), "secret_shown_once") {
			t.Fatal("token list exposed credential material")
		}
		headers := map[string]string{"Authorization": "Bearer " + secret}
		expect(t, request(t, httpClient(), b, "GET", "/api/v1/connectors", env, nil, headers), 200)
		expect(t, request(t, httpClient(), a, "GET", "/api/v1/connectors", f.OtherEnvironment, nil, headers), 401)
		expect(t, request(t, httpClient(), a, "GET", "/api/v1/canvases", env, nil, headers), 401)
		expect(t, request(t, admin, b, "DELETE", "/api/v1/api-tokens/"+id, env, nil, nil), 204)
		expect(t, request(t, httpClient(), a, "GET", "/api/v1/connectors", env, nil, headers), 401)
	})

	t.Run("CanvasPositiveAndCrossEnvironmentBoundary", func(t *testing.T) {
		title := "Backend plan " + store.ID()
		created := request(t, admin, a, "POST", "/api/v1/canvases", env, map[string]any{"title": title, "kind": "plan", "goal": "Synthetic plan", "services": []string{}, "ai_draft": false}, nil)
		expect(t, created, 201)
		id := objectID(t, created)
		t.Cleanup(func() {
			if _, err := db.Exec(context.Background(), `DELETE FROM v3_canvases WHERE id=$1`, id); err != nil {
				t.Error(err)
			}
		})
		got := request(t, admin, b, "GET", "/api/v1/canvases/"+id, env, nil, nil)
		expect(t, got, 200)
		if got.Data["title"] != title || got.Data["kind"] != "plan" {
			t.Fatal("persisted Canvas content differs")
		}
		expect(t, request(t, admin, b, "GET", "/api/v1/canvases/"+id, f.OtherEnvironment, nil, nil), 404)
		expect(t, request(t, admin, a, "POST", "/api/v1/canvases", env, map[string]any{"title": "", "kind": "plan"}, nil), 422)
	})

	t.Run("RunbookPositiveAndCrossEnvironmentBoundary", func(t *testing.T) {
		title := "Backend runbook " + store.ID()
		created := request(t, admin, a, "POST", "/api/v1/runbooks", env, map[string]any{"title": title, "source": "blank", "from": "", "steps": []any{}, "status": "draft"}, nil)
		expect(t, created, 201)
		id := objectID(t, created)
		t.Cleanup(func() {
			for _, table := range []string{"v3_runbooks", "knowledge_history", "knowledge_revisions", "knowledge_documents"} {
				column := "document_id"
				if table == "knowledge_documents" {
					column = "id"
				}
				if _, err := db.Exec(context.Background(), "DELETE FROM "+table+" WHERE "+column+"=$1", id); err != nil {
					t.Error(err)
				}
			}
		})
		got := request(t, admin, b, "GET", "/api/v1/runbooks/"+id, env, nil, nil)
		expect(t, got, 200)
		if got.Data["id"] != id || got.Data["title"] != title || got.Data["status"] != "draft" {
			t.Fatal("persisted Runbook content/lifecycle differs")
		}
		expect(t, request(t, admin, b, "GET", "/api/v1/runbooks/"+id, f.OtherEnvironment, nil, nil), 404)
		expect(t, request(t, admin, a, "POST", "/api/v1/runbooks", env, map[string]any{"title": "Invalid", "source": "blank", "from": "", "steps": []any{}, "status": "review"}, nil), 422)
	})

	t.Run("ConversationPositiveAndCrossEnvironmentBoundary", func(t *testing.T) {
		if f.ModelID == "" {
			t.Skip("local protocol model fixture is not configured; no external LLM used")
		}
		created := request(t, admin, a, "POST", "/api/v1/conversations", env, map[string]any{"text": "Synthetic backend conversation", "context": "", "model": f.ModelID}, nil)
		expect(t, created, 201)
		id := objectID(t, created)
		t.Cleanup(func() {
			if _, err := db.Exec(context.Background(), `DELETE FROM v3_conversations WHERE id=$1`, id); err != nil {
				t.Error(err)
			}
		})
		expect(t, request(t, admin, b, "GET", "/api/v1/conversations/"+id, env, nil, nil), 200)
		expect(t, request(t, admin, b, "GET", "/api/v1/conversations/"+id, f.OtherEnvironment, nil, nil), 404)
		// The LLM response is a labeled local protocol Mock. The accepting
		// transaction, asynchronous worker and durable cross-instance read are real.
		accepted := request(t, admin, a, "POST", "/api/v1/conversations/"+id+"/messages", env, map[string]any{"text": "Reply with the local synthetic fixture", "model": f.ModelID}, nil)
		expect(t, accepted, 202)
		if accepted.Data["reply_id"] == nil {
			t.Fatal("accepted turn omitted durable reply id")
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			got := request(t, admin, b, "GET", "/api/v1/conversations/"+id, env, nil, nil)
			expect(t, got, 200)
			messages, _ := got.Data["messages"].([]any)
			for _, value := range messages {
				m, _ := value.(map[string]any)
				if m["role"] != "assistant" {
					continue
				}
				if m["state"] == "failed" {
					t.Fatal("local protocol Mock turn failed")
				}
				parts, _ := m["parts"].([]any)
				if m["state"] == "completed" && len(parts) > 0 {
					raw, _ := json.Marshal(parts)
					if !strings.Contains(string(raw), "Synthetic provider response") {
						t.Fatal("completed turn lost the local Mock provider content")
					}
					outline := request(t, admin, b, "GET", "/api/v1/runbooks/draft?source=conversation&from="+id, env, nil, nil)
					expect(t, outline, 200)
					digest, _ := outline.Data["source_sha256"].(string)
					if len(digest) != 64 || outline.Data["draft_mode"] != "source_outline" {
						t.Fatal("conversation outline omitted its exact source digest")
					}
					runbook := request(t, admin, a, "POST", "/api/v1/runbooks", env, map[string]any{"title": "Conversation derived " + store.ID(), "source": "conversation", "from": id, "expected_source_sha256": digest, "steps": []any{}, "status": "draft"}, nil)
					expect(t, runbook, 201)
					runbookID := objectID(t, runbook)
					cleanupContractRunbook(t, db, runbookID)
					record := request(t, admin, b, "GET", "/api/v1/runbooks/"+runbookID+"/record", env, nil, nil)
					expect(t, record, 200)
					if record.Data["source_kind"] != "conversation" {
						t.Fatal("derived Runbook record lost its conversation source kind")
					}
					// The record DTO intentionally omits source_ref; the full
					// Runbook DTO and durable binding expose the exact source.
					full := request(t, admin, b, "GET", "/api/v1/runbooks/"+runbookID, env, nil, nil)
					expect(t, full, 200)
					if full.Data["source_kind"] != "conversation" || full.Data["source_ref"] != id {
						t.Fatal("derived Runbook lost its private conversation reference")
					}
					var kind, ref, conversationID string
					var snapshot map[string]string
					if err := db.QueryRow(t.Context(), `SELECT source_kind,source_ref,conversation_id,source_snapshot FROM v3_runbooks WHERE document_id=$1`, runbookID).Scan(&kind, &ref, &conversationID, &snapshot); err != nil {
						t.Fatal("derived Runbook source binding could not be read")
					}
					if kind != "conversation" || ref != id || conversationID != id || snapshot["source_id"] != id || snapshot["source_kind"] != "conversation" || snapshot["content_sha256"] != digest {
						t.Fatal("derived Runbook did not persist the exact conversation and source digest")
					}
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("accepted Mock turn did not complete within 15 seconds")
			}
			select {
			case <-t.Context().Done():
				t.Fatal("turn verification canceled")
			case <-time.After(50 * time.Millisecond):
			}
		}
	})

	t.Run("UnconfiguredAuthorityFailureClosed", func(t *testing.T) {
		r := request(t, admin, a, "GET", "/api/v1/me/profile", env, nil, nil)
		expect(t, r, 503)
		if e, ok := r.Data["error"].(map[string]any); !ok || e["code"] != "identity_evidence_unavailable" {
			t.Fatal("missing authority was not reported explicitly")
		}
	})

	t.Run("IncidentIdempotencyLifecyclePostmortemPositive", func(t *testing.T) {
		app, row, alert := store.ID(), store.ID(), store.ID()
		if _, err := db.Exec(t.Context(), `INSERT INTO applications(id,key,name,environment) VALUES($1,$1,'Synthetic service',$2)`, app, env); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// Run-owned descendants only. Retain audit history and archive the
			// synthetic service so repeated runs cannot expose old alert data.
			for _, query := range []string{
				`DELETE FROM v3_postmortems WHERE application_id=$1`,
				`UPDATE v3_incident_cases SET resolved_status_event_id=NULL,resolved_at=NULL WHERE application_id=$1`,
				`DELETE FROM v3_incident_case_status_events WHERE case_id IN(SELECT id FROM v3_incident_cases WHERE application_id=$1)`,
				`DELETE FROM v3_incident_cases WHERE application_id=$1`,
				`DELETE FROM ops_incidents WHERE application_id=$1`,
				`UPDATE applications SET archived=true WHERE id=$1`,
			} {
				if _, err := db.Exec(context.Background(), query, app); err != nil {
					t.Error(err)
				}
			}
		})
		if _, err := db.Exec(t.Context(), `INSERT INTO application_environments(id,application_id,key,name) VALUES($1,$2,$3,'Test')`, row, app, env); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(t.Context(), `INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,$2)`, app, env); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(t.Context(), `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,labels,starts_at) VALUES($1,'synthetic-testlab',$2,$3,$1,'Synthetic alert','critical','open','firing','{}',now())`, alert, app, row); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"title": "Synthetic human incident", "severity": "P2", "impact": nil, "env": env, "services": []string{app}, "alert_ids": []string{alert}, "commander": nil, "war_room": false, "notify": false, "status_page": false, "open_canvas": false}
		headers := map[string]string{"Idempotency-Key": "backend-" + store.ID()}
		created := request(t, admin, a, "POST", "/api/v1/incidents", env, body, headers)
		expect(t, created, 201)
		id := objectID(t, created)
		replayed := request(t, admin, b, "POST", "/api/v1/incidents", env, body, headers)
		expect(t, replayed, 200)
		if objectID(t, replayed) != id {
			t.Fatal("idempotent retry created a second incident")
		}
		body["title"] = "Conflicting retry"
		expect(t, request(t, admin, b, "POST", "/api/v1/incidents", env, body, headers), 409)
		expect(t, request(t, admin, b, "GET", "/api/v1/incidents/"+id, env, nil, nil), 200)
		expect(t, request(t, admin, b, "GET", "/api/v1/incidents/"+id, f.OtherEnvironment, nil, nil), 404)
		status := map[string]any{"status": "resolved", "expected_revision": 1, "request_id": "backend-status-" + store.ID(), "reason": "Synthetic human resolution"}
		expect(t, request(t, admin, a, "POST", "/api/v1/incidents/"+id+"/status", env, status, nil), 201)
		expect(t, request(t, admin, b, "POST", "/api/v1/incidents/"+id+"/status", env, status, nil), 200)
		closed := request(t, admin, b, "GET", "/api/v1/incidents/"+id, env, nil, nil)
		expect(t, closed, 200)
		if closed.Data["status"] != "resolved" || closed.Data["status_revision"] != float64(2) || closed.Data["resolved_status_event_id"] == nil {
			t.Fatal("persisted incident close revision/evidence differs")
		}
		draft := request(t, admin, b, "GET", "/api/v1/postmortems/draft?incident="+id, env, nil, nil)
		expect(t, draft, 200)
		revision, _ := draft.Data["source_revision"].(string)
		if !strings.HasPrefix(revision, "case:") {
			t.Fatal("postmortem draft did not bind verified case closing event")
		}
		pmBody := map[string]any{"incident_id": id, "source_revision": revision, "sections": []any{map[string]any{"key": "summary", "title": "Summary", "text": "Synthetic review evidence"}}, "actions": []any{}, "status": "review"}
		pm := request(t, admin, a, "POST", "/api/v1/postmortems", env, pmBody, nil)
		expect(t, pm, 201)
		pmID := objectID(t, pm)
		expect(t, request(t, admin, a, "POST", "/api/v1/postmortems/"+pmID+"/publish", env, map[string]any{}, nil), 403)
		reviewer := seedHuman(t, f, db, [][2]string{{"environment", "view"}, {"events", "view"}, {"knowledge", "view"}, {"knowledge", "manage"}})
		expect(t, request(t, reviewer, b, "POST", "/api/v1/postmortems/"+pmID+"/publish", env, map[string]any{}, nil), 200)
		records := request(t, admin, a, "GET", "/api/v1/postmortems/records", env, nil, nil)
		expect(t, records, 200)
		items, _ := records.Data["items"].([]any)
		found := false
		for _, item := range items {
			row, _ := item.(map[string]any)
			if row["id"] == pmID && row["status"] == "published" {
				found = true
			}
		}
		if !found {
			t.Fatal("published postmortem missing from durable public records")
		}
	})
}
