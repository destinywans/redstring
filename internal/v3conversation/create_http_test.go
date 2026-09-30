package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestCreateConversationHTTPPersistsScopeModelAndAudit(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT ''`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit ALTER COLUMN actor_kind SET DEFAULT 'user'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO users(id,email,name,role) VALUES('owner-1','owner-1@local','Owner','viewer')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'owner-1',now()+interval '1 hour')`, store.Hash("owner-session")); err != nil {
		t.Fatal(err)
	}
	granted, writeGranted := true, true
	defaultModel := "model-2"
	svc := New(db, testRuntime{models: []inference.Model{{ID: "model-1"}, {ID: "model-2"}}}, Options{
		CurrentUser: func(*http.Request) core.User { return core.User{ID: "owner-1"} },
		AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
			if granted {
				return []string{"test"}, nil
			}
			return nil, nil
		},
		DefaultModel: func(_ context.Context, owner, env string) (string, error) {
			if owner != "owner-1" {
				t.Fatalf("unexpected model owner %q", owner)
			}
			return defaultModel, nil
		},
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if !writeGranted || owner != "owner-1" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
	})
	post := func(env, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", strings.NewReader(body))
		req.Header.Set("X-AIOps-Environment", env)
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "owner-session"})
		rr := httptest.NewRecorder()
		svc.createHTTP(rr, req)
		return rr
	}

	rr := post("test", `{"text":"  调查 API 延迟  ","context":"service:checkout","model":"model-1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create response=%s err=%v", rr.Body.String(), err)
	}
	var owner, env, title, contextRef, model string
	if err := db.QueryRow(ctx, `SELECT owner_id,environment_id,title,context_ref,default_model_id FROM v3_conversations WHERE id=$1`, created.ID).
		Scan(&owner, &env, &title, &contextRef, &model); err != nil {
		t.Fatal(err)
	}
	if owner != "owner-1" || env != "test" || title != "调查 API 延迟" || contextRef != "service:checkout" || model != "model-1" {
		t.Fatalf("persisted owner=%q env=%q title=%q context=%q model=%q", owner, env, title, contextRef, model)
	}
	var auditActor, auditTarget, auditEnv string
	if err := db.QueryRow(ctx, `SELECT actor_id,target,changes->>'environment' FROM management_audit WHERE action='v3.conversation.create'`).
		Scan(&auditActor, &auditTarget, &auditEnv); err != nil {
		t.Fatal(err)
	}
	if auditActor != owner || auditTarget != created.ID || auditEnv != env {
		t.Fatalf("audit actor=%q target=%q env=%q", auditActor, auditTarget, auditEnv)
	}
	defaultResponse := post("test", `{"text":"default model","context":null,"model":null}`)
	if defaultResponse.Code != http.StatusCreated {
		t.Fatalf("default model create status=%d body=%s", defaultResponse.Code, defaultResponse.Body.String())
	}
	var defaultCreated struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(defaultResponse.Body.Bytes(), &defaultCreated); err != nil || defaultCreated.ID == "" {
		t.Fatalf("default model response=%s err=%v", defaultResponse.Body.String(), err)
	}
	if err := db.QueryRow(ctx, `SELECT default_model_id FROM v3_conversations WHERE id=$1`, defaultCreated.ID).Scan(&model); err != nil || model != "model-2" {
		t.Fatalf("default model=%q err=%v", model, err)
	}
	defaultModel = "removed-model"
	staleResponse := post("test", `{"text":"stale model preference"}`)
	if staleResponse.Code != http.StatusCreated {
		t.Fatalf("stale preference status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
	var staleCreated struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(staleResponse.Body.Bytes(), &staleCreated); err != nil || staleCreated.ID == "" {
		t.Fatalf("stale preference response=%s err=%v", staleResponse.Body.String(), err)
	}
	if err := db.QueryRow(ctx, `SELECT default_model_id FROM v3_conversations WHERE id=$1`, staleCreated.ID).Scan(&model); err != nil || model != "model-1" {
		t.Fatalf("stale preference fallback=%q err=%v", model, err)
	}

	for _, tc := range []struct {
		env, body string
		status    int
	}{
		{"all", `{"text":"question"}`, http.StatusConflict},
		{"test", `{"text":"  "}`, http.StatusUnprocessableEntity},
		{"test", `{"text":"question","model":"missing"}`, http.StatusUnprocessableEntity},
		{"test", `{"text":"question","unexpected":true}`, http.StatusUnprocessableEntity},
	} {
		if got := post(tc.env, tc.body); got.Code != tc.status {
			t.Errorf("env=%s body=%s status=%d want=%d response=%s", tc.env, tc.body, got.Code, tc.status, got.Body.String())
		}
	}
	writeGranted = false
	if got := post("test", `{"text":"after write revocation"}`); got.Code != http.StatusForbidden {
		t.Fatalf("revoked write grant status=%d body=%s", got.Code, got.Body.String())
	}
	writeGranted = true
	granted = false
	if got := post("test", `{"text":"after revocation"}`); got.Code != http.StatusForbidden {
		t.Fatalf("revoked grant status=%d body=%s", got.Code, got.Body.String())
	}
	var total int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversations WHERE owner_id='owner-1'`).Scan(&total); err != nil || total != 3 {
		t.Fatalf("conversation count=%d err=%v", total, err)
	}
	granted = true
	if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash("owner-session")); err != nil {
		t.Fatal(err)
	}
	if got := post("test", `{"text":"after session revocation"}`); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d body=%s", got.Code, got.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversations WHERE owner_id='owner-1'`).Scan(&total); err != nil || total != 3 {
		t.Fatalf("revoked session created conversation: count=%d err=%v", total, err)
	}
}
