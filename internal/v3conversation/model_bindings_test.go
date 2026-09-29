package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/v3models"
)

type boundConversationTestRunner struct {
	calls   int
	request inference.Request
}

func (*boundConversationTestRunner) Models() []inference.Model {
	return []inference.Model{{ID: "test-model"}, {ID: "dev-only"}}
}

func (r *boundConversationTestRunner) Run(_ context.Context, request inference.Request, _ func(inference.Event) error) (string, error) {
	r.calls++
	r.request = request
	return "bound answer", nil
}

func boundConversationRuntime(t *testing.T) (*boundConversationTestRunner, *v3models.BoundRunner) {
	t.Helper()
	runner := &boundConversationTestRunner{}
	path := filepath.Join(t.TempDir(), "bindings.json")
	config := `{"tier":"nonprod","environments":[{"id":"test","models":["test-model"],"default_model":"test-model"},{"id":"dev","models":["dev-only"],"default_model":"dev-only"}]}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	bindings, err := v3models.LoadBindings(path, "nonprod", []string{"test", "dev"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	return runner, v3models.NewBoundRunner(runner, bindings)
}

func TestBoundConversationCreateAndSendRejectCrossEnvironmentModel(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		ALTER TABLE management_audit ALTER COLUMN actor_kind SET DEFAULT 'user';
		UPDATE v3_conversations SET active_reply_id=NULL WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	_, runtime := boundConversationRuntime(t)
	service := New(db, runtime, Options{
		CurrentUser:         func(*http.Request) core.User { return core.User{ID: "persisted-owner"} },
		AllowedEnvironments: func(context.Context, core.User) ([]string, error) { return []string{"test", "dev"}, nil },
		DefaultModel: func(_ context.Context, owner, env string) (string, error) {
			if owner != "persisted-owner" || (env != "test" && env != "dev") {
				t.Fatalf("wrong preference scope owner=%q env=%q", owner, env)
			}
			return "dev-only", nil // stale in test, valid in dev
		},
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if owner != "persisted-owner" || (env != "test" && env != "dev") {
				return ErrForbidden
			}
			return nil
		},
	})
	post := func(path, env, body string, create bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("X-AIOps-Environment", env)
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "test-session"})
		if !create {
			req.SetPathValue("id", "c")
		}
		rr := httptest.NewRecorder()
		if create {
			service.createHTTP(rr, req)
		} else {
			service.send(rr, req)
		}
		return rr
	}
	if rr := post("/api/v1/conversations", "test", `{"text":"wrong","model":"dev-only"}`, true); rr.Code != 422 || !strings.Contains(rr.Body.String(), "invalid_model") {
		t.Fatalf("cross-env create status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := post("/api/v1/conversations/c/messages", "test", `{"text":"wrong","model":"dev-only"}`, false); rr.Code != 422 || !strings.Contains(rr.Body.String(), "invalid_model") {
		t.Fatalf("cross-env send status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, env := range []string{"test", "dev"} {
		rr := post("/api/v1/conversations", env, `{"text":"default"}`, true)
		if rr.Code != 201 {
			t.Fatalf("create env=%s status=%d body=%s", env, rr.Code, rr.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		var saved string
		if err := db.QueryRow(ctx, `SELECT default_model_id FROM v3_conversations WHERE id=$1`, created.ID).Scan(&saved); err != nil {
			t.Fatal(err)
		}
		want := "test-model"
		if env == "dev" {
			want = "dev-only"
		}
		if saved != want {
			t.Fatalf("env=%s saved=%q want=%q", env, saved, want)
		}
	}
}

func TestBoundConversationQueuedRunUsesPersistedEnvironmentAndRejectsLostPair(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		allowed     bool
	}{
		{"lost pair", "dev-only", false},
		{"bound pair", "test-model", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := isolatedConversationDB(t)
			runner, runtime := boundConversationRuntime(t)
			service := New(db, runtime, Options{
				RefreshSession: func(_ context.Context, token string) (core.User, error) {
					if token != "test-session" {
						t.Fatalf("wrong token")
					}
					return core.User{ID: "persisted-owner"}, nil
				},
				AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
					if owner != "persisted-owner" || env != "test" {
						return ErrForbidden
					}
					return nil
				},
				AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
					if owner != "persisted-owner" || env != "test" {
						return ErrForbidden
					}
					return nil
				},
			})
			service.runTurn("c", "r", inference.Model{ID: tc.model}, "", "test-session")
			var state string
			if err := db.QueryRow(context.Background(), `SELECT state FROM v3_conversation_replies WHERE id='r'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if !tc.allowed {
				if runner.calls != 0 || state != "failed" {
					t.Fatalf("lost pair calls=%d state=%s", runner.calls, state)
				}
				return
			}
			if runner.calls != 1 || runner.request.EnvironmentID != "test" || runner.request.ModelID != tc.model || state != "completed" {
				t.Fatalf("bound run calls=%d request=%+v state=%s", runner.calls, runner.request, state)
			}
		})
	}
}
