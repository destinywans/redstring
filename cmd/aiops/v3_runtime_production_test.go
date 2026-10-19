package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/destinywans/redstring/internal/actions"
	"github.com/destinywans/redstring/internal/aiextensions"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Use the real composition root, ADK/provider serialization and loopback HTTP.
// Only identity/application prerequisites are seeded; the new product objects
// are all created through their formal routes. No external provider is called.
func TestConfiguredV3MemoryEvaluationAndAgentExecution(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random disposable schema")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "AIOPS_V3_") {
			t.Setenv(name, "")
		}
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_runtime_entry_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = actions.New(db, nil, "dev").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = aiextensions.New(db, "").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	know := knowledge.New(db, nil)
	if err = know.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "runtime-admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "runtime-admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO applications(id,key,name,environment) VALUES('app','app','App','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["max_tokens"] != float64(64) {
			t.Errorf("version budget not sent to provider: %v", body["max_tokens"])
		}
		if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
			t.Error("tool-free version received tools")
		}
		raw, _ := json.Marshal(body)
		answer := "baseline observation"
		if strings.Contains(string(raw), "CANDIDATE") {
			answer = "healthy restart"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	defer provider.Close()
	runner, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "local-model", Kind: "openai", Model: "local-fixture", BaseURL: provider.URL, MaxOutputTokens: 128}}, HTTPClient: provider.Client()})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, runner, true)
	var info v3Instance
	if err = json.Unmarshal([]byte(`{"tier":"nonprod","domain":"runtime.example.invalid","environments":[{"id":"dev"}]}`), &info); err != nil {
		t.Fatal(err)
	}
	if err = configureV3(ctx, st, runner, api, know, &info, httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, operations.Config{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		var input io.Reader
		if body != nil {
			b, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			input = bytes.NewReader(b)
		}
		r, e := http.NewRequestWithContext(ctx, method, server.URL+"/api/v1"+path, input)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if e != nil {
			t.Fatal(e)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s want %d", method, path, resp.StatusCode, b, want)
		}
		var out map[string]any
		if e = json.Unmarshal(b, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	create := map[string]any{"id": "agent", "application_id": "app", "name": "Registered Agent"}
	call("POST", "/agents", create, 201)
	if call("POST", "/agents", create, 200)["created"] != false {
		t.Fatal("duplicate created another Agent")
	}
	create["name"] = "Changed"
	call("POST", "/agents", create, 409)
	for _, v := range []string{"v1", "v2"} {
		call("POST", "/agents/agent/versions", map[string]any{"version": v, "artifact_ref": "git:" + v}, 201)
	}
	call("POST", "/agents/agent/versions", map[string]any{"version": "v1", "artifact_ref": "git:different"}, 409)
	prompt := call("POST", "/prompts", map[string]any{"agent_id": "agent", "name": "Runtime prompt", "text": "BASELINE: give the baseline observation"}, 201)["id"].(string)
	call("POST", "/prompts/"+prompt+"/versions", map[string]any{"text": "CANDIDATE: give the improved observation"}, 201)
	for i, v := range []string{"v1", "v2"} {
		call("PUT", "/agents/agent/versions/"+v+"/execution", map[string]any{"model_id": "local-model", "prompt_id": prompt, "prompt_version": i + 1, "tool_names": []string{}, "max_model_calls": 1, "max_tool_calls": 0, "max_input_bytes": 1024, "max_output_bytes": 1024, "max_output_tokens": 64, "timeout_seconds": 10}, 200)
	}
	call("POST", "/agents/agent/rollout", map[string]any{"to": "v1", "percent": 100, "expected_revision": 1, "override": true, "override_reason": "Explicit local test baseline"}, 200)
	first := call("POST", "/agents/agent/runs", map[string]any{"input": "status"}, 200)
	if first["version"] != "v1" || first["status"] != "success" || first["output"] != "baseline observation" {
		t.Fatalf("baseline: %#v", first)
	}
	input := map[string]any{"agent_id": "agent", "title": "Human case", "input": "Check service", "queue": "Human supplied", "alert": map[string]any{"sev": "P3", "title": "Human note", "labels": []any{}}, "evidence": []any{map[string]any{"brand": "manual", "title": "Operator evidence", "sub": "Synthetic local test"}}, "expected": map[string]any{"cause": "healthy", "advice": "restart"}, "checks": []string{"healthy", "restart"}}
	caseID := call("POST", "/eval-cases", input, 201)["id"].(string)
	experiment := call("POST", "/evals", map[string]any{"agent_id": "agent", "title": "Two frozen prompts", "baseline_version": "v1", "candidate_version": "v2"}, 201)
	if experiment["execution_available"] != true {
		t.Fatalf("formal runtime not wired: %#v", experiment)
	}
	evalID := experiment["id"].(string)
	request := map[string]any{"case_id": caseID, "request_key": "main-entry-eval-key"}
	call("POST", "/evals/"+evalID+"/execute", request, 200)
	call("POST", "/evals/"+evalID+"/execute", request, 200)
	if calls.Load() != 3 {
		t.Fatalf("replayed evaluation caused model calls: %d", calls.Load())
	}
	if call("GET", "/eval-cases/"+caseID, nil, 200)["complete_v3_dto"] != true {
		t.Fatal("executed case lacks its real snapshot")
	}
	call("POST", "/agents/agent/rollout", map[string]any{"to": "v2", "percent": 100, "expected_revision": 2}, 200)
	last := call("POST", "/agents/agent/runs", map[string]any{"input": "status"}, 200)
	if last["version"] != "v2" || last["status"] != "success" || last["output"] != "healthy restart" {
		t.Fatalf("candidate: %#v", last)
	}
	if call("GET", "/agent-runs/"+last["id"].(string)+"/result", nil, 200)["output"] != last["output"] {
		t.Fatal("private result not persisted")
	}
	if calls.Load() != 4 {
		t.Fatalf("unexpected provider calls %d", calls.Load())
	}
	source := call("POST", "/memory/sources", map[string]any{"request_key": "main-memory-source", "application_id": "app", "title": "Explicit operator source", "body": "The maintenance window is Sunday."}, 201)
	fact := call("POST", "/memory/facts", map[string]any{"document_id": source["document_id"], "expected_version": source["version"], "expected_content_hash": source["content_hash"], "exact_excerpt": "The maintenance window is Sunday."}, 201)
	produced, ok := fact["fact"].(map[string]any)
	if !ok || produced["id"] == nil || produced["state"] != "candidate" {
		t.Fatalf("source did not produce a fact %#v", fact)
	}
	call("GET", "/memory", nil, 200)
}
