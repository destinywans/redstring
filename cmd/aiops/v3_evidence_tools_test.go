package main

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestV3ReadToolsPreserveModelAndInvestigationScope(t *testing.T) {
	f := v3ReadTools{
		Models:      map[string]bool{"approved": true},
		CurrentUser: func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil },
		LookupService: func(_ context.Context, _ core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			return v3catalog.ServiceEvidence{ID: base64.RawURLEncoding.EncodeToString([]byte("app-" + key + "\x00" + env)), Key: key, Name: key, Environment: env}, nil
		},
	}
	if tools, err := f.ForTurn(t.Context(), "user", "app-orders", "dev", "unapproved"); err != nil || len(tools) != 0 {
		t.Fatalf("unapproved model received tools: %v %v", tools, err)
	}
	if _, err := f.ForTurn(t.Context(), "user", "app-orders", "all", "approved"); err == nil {
		t.Fatal("aggregate environment accepted for tool execution")
	}
	tools, err := f.ForTurn(t.Context(), "user", "app-orders", "dev", "approved")
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	if _, err := tools[0].Read(t.Context(), "payments"); err == nil {
		t.Fatal("investigation read another accessible application")
	}
	evidence, err := tools[0].Read(t.Context(), "orders")
	if err != nil || evidence.TargetID != base64.RawURLEncoding.EncodeToString([]byte("app-orders\x00dev")) || evidence.Source != "service_catalog" {
		t.Fatalf("bound profile: %+v %v", evidence, err)
	}
	f.CurrentUser = func(context.Context, string) (core.User, error) { return core.User{}, errors.New("revoked") }
	tools, err = f.ForTurn(t.Context(), "user", "app-orders", "dev", "approved")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools[0].Read(t.Context(), "orders"); err == nil {
		t.Fatal("revoked actor still read profile")
	}
}

func TestV3ChatKnowledgeResolvesAuthorizedApplicationBeforeReading(t *testing.T) {
	reads := 0
	f := v3ReadTools{
		Models:      map[string]bool{"approved": true},
		CurrentUser: func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil },
		LookupService: func(_ context.Context, _ core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			if key != "orders" {
				return v3catalog.ServiceEvidence{}, errors.New("hidden or missing")
			}
			return v3catalog.ServiceEvidence{ID: base64.RawURLEncoding.EncodeToString([]byte("persistent-app\x00" + env)), Key: key, Environment: env}, nil
		},
		KnowledgeTool: func(actor, app, env string) (inference.EvidenceTool, bool) {
			if actor != "user" || app != "persistent-app" || env != "dev" {
				t.Errorf("untrusted knowledge scope: %s %s %s", actor, app, env)
			}
			return inference.EvidenceTool{Read: func(_ context.Context, query string) (inference.Evidence, error) {
				reads++
				if query != "database timeout" {
					t.Errorf("wrong query: %q", query)
				}
				return inference.Evidence{Kind: "knowledge", TargetID: app}, nil
			}}, true
		},
	}
	tools, err := f.ForTurn(t.Context(), "user", "", "dev", "approved")
	if err != nil || len(tools) != 2 || tools[1].Name != "search_knowledge" {
		t.Fatalf("knowledge not assembled: %v %v", tools, err)
	}
	for _, input := range []string{
		`{"service":"hidden","query":"database timeout"}`,
		`{"service":"orders","query":"database timeout","environment":"prod"}`,
		`{"service":"orders","query":"database timeout"} {}`,
		`{"application_id":"persistent-app","query":"database timeout"}`,
	} {
		if _, err := tools[1].Read(t.Context(), input); err == nil {
			t.Fatalf("unsafe selector accepted: %s", input)
		}
	}
	if reads != 0 {
		t.Fatal("rejected selector reached knowledge reader")
	}
	if _, err := tools[1].Read(t.Context(), `{"service":"orders","query":"database timeout"}`); err != nil || reads != 1 {
		t.Fatalf("authorized search failed: reads=%d err=%v", reads, err)
	}
	bound, err := f.ForTurn(t.Context(), "user", "persistent-app", "dev", "approved")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound[1].Read(t.Context(), "database timeout"); err != nil || reads != 2 {
		t.Fatalf("bound investigation knowledge failed: %v", err)
	}
}

func TestV3ServiceKeyInputAcceptsBareKeyOrServiceObject(t *testing.T) {
	for input, want := range map[string]string{
		"checkout-api":                     "checkout-api",
		` "checkout-api" `:                 "checkout-api",
		`{"service":"checkout-api"}`:       "checkout-api",
		`{"service":"checkout-api","x":1}`: `{"service":"checkout-api","x":1}`,
		`{"service":"a"} {}`:               `{"service":"a"} {}`,
	} {
		if got := v3ServiceKeyInput(input); got != want {
			t.Fatalf("v3ServiceKeyInput(%q) = %q, want %q", input, got, want)
		}
	}
}

// PA-50: chat offers list_services first, and the shared tools tell an
// unknown key, a catalog failure and an unconfigured knowledge index apart.
func TestV3ReadToolsExplainFailures(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://unused.invalid/none") // never connected by these reads
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	catalogDown := false
	f := v3ReadTools{
		DB:          pool,
		Models:      map[string]bool{"approved": true},
		CurrentUser: func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil },
		LookupService: func(_ context.Context, _ core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			if catalogDown {
				return v3catalog.ServiceEvidence{}, errors.New("database unavailable")
			}
			if key != "orders" {
				return v3catalog.ServiceEvidence{}, store.ErrNotFound
			}
			return v3catalog.ServiceEvidence{ID: base64.RawURLEncoding.EncodeToString([]byte("app-orders\x00" + env)), Key: key, Environment: env}, nil
		},
		ListServices: func(context.Context, core.User, string, int) ([]v3catalog.ServiceEvidence, bool, error) {
			return nil, false, nil
		},
		KnowledgeTool: func(string, string, string) (inference.EvidenceTool, bool) { return inference.EvidenceTool{}, false },
	}
	tools, err := f.ForTurn(t.Context(), "user", "", "dev", "approved")
	if err != nil || len(tools) != 3 || tools[0].Name != "list_services" || tools[1].Name != "read_service_profile" {
		t.Fatalf("chat tools: %v %v", tools, err)
	}
	profile, knowledge := tools[1], tools[2]
	if _, err := profile.Read(t.Context(), "payments"); inference.ToolErrorCode(err) != inference.ToolUnknownService {
		t.Fatalf("unknown key: %v", err)
	}
	if _, err := knowledge.Read(t.Context(), `{"service":"payments","query":"timeout"}`); inference.ToolErrorCode(err) != inference.ToolUnknownService {
		t.Fatalf("knowledge unknown key: %v", err)
	}
	if _, err := knowledge.Read(t.Context(), `{"service":"orders","query":"timeout"}`); inference.ToolErrorCode(err) != inference.ToolNotConfigured {
		t.Fatalf("knowledge without an index: %v", err)
	}
	if _, err := knowledge.Read(t.Context(), "timeout"); inference.ToolErrorCode(err) != inference.ToolInvalidInput {
		t.Fatalf("knowledge input: %v", err)
	}
	catalogDown = true
	if _, err := profile.Read(t.Context(), "orders"); inference.ToolErrorCode(err) != inference.ToolSourceUnavailable {
		t.Fatalf("catalog failure: %v", err)
	}
	// An investigation reads one service: no list tool there.
	f.DB = nil
	if tools, err = f.ForTurn(t.Context(), "user", "app-orders", "dev", "approved"); err != nil || tools[0].Name == "list_services" {
		t.Fatalf("investigation tools: %v %v", tools, err)
	}
}
