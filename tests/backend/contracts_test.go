package backend_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func expectErrorCode(t *testing.T, r response, status int, code string) {
	t.Helper()
	expect(t, r, status)
	e, ok := r.Data["error"].(map[string]any)
	if !ok || e["code"] != code {
		t.Fatalf("%s %s omitted expected structured error %q", r.Method, r.Path, code)
	}
}

func contractPool(t *testing.T, f fixture) *pgxpool.Pool {
	t.Helper()
	db, err := pgxpool.New(t.Context(), f.DatabaseURL)
	if err != nil {
		t.Fatal("owned APP database pool unavailable")
	}
	// Register first so resource/user cleanup completes before pool closure.
	t.Cleanup(db.Close)
	return db
}

// The source contract also declares legacy and conditional routes. This
// deliberately bounded family matrix proves anonymous rejection ONLY. It
// never upgrades normal, object authorization, or registration coverage.
func TestFormalV3AnonymousContractMatrix(t *testing.T) {
	f := readFixture(t, true)
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "apicontract", "openapi.json"))
	if err != nil {
		t.Fatal("machine API contract unavailable")
	}
	var contract struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if json.Unmarshal(raw, &contract) != nil {
		t.Fatal("machine API contract invalid")
	}
	paths := []string{"/canvases", "/canvases/{id}", "/conversations", "/conversations/{id}", "/conversations/{id}/messages", "/runbooks", "/runbooks/{id}", "/runbooks/{id}/submit", "/runbooks/{id}/dry-run", "/runbooks/{id}/reviews", "/runbooks/{id}/record", "/runbooks/{id}/history", "/postmortems/records", "/api-tokens", "/api-tokens/{id}", "/me/preferences", "/me/ai-preferences", "/me/notification-settings", "/workspace", "/models", "/incidents/{id}/timeline", "/incidents/{id}/timeline/manual", "/alerts/{id}", "/alerts/{id}/claim", "/search"}
	selectedPaths := make(map[string]bool, len(paths))
	for _, path := range paths {
		selectedPaths[path] = true
		if contract.Paths[path] == nil {
			t.Fatalf("explicit V3 matrix path %s missing from machine contract", path)
		}
	}
	var operations []string
	for path, methods := range contract.Paths {
		if !selectedPaths[path] {
			continue
		}
		for method := range methods {
			switch method {
			case "get", "post", "put", "patch", "delete":
				operations = append(operations, strings.ToUpper(method)+" "+path)
			}
		}
	}
	sort.Strings(operations)
	placeholder := regexp.MustCompile(`\{[^}]+\}`)
	for index, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			method, path, _ := strings.Cut(operation, " ")
			path = "/api/v1" + placeholder.ReplaceAllString(path, strings.Repeat("0", 32))
			expectErrorCode(t, request(t, httpClient(), f.BaseURLs[index%2], method, path, f.Environment, nil, nil), 401, "unauthorized")
		})
	}
}

func TestFormalV3EnvironmentAndInputContractMatrix(t *testing.T) {
	f := readFixture(t, true)
	admin := authenticatedClient(t, f)
	type queryCase struct{ path, code string }
	for _, c := range []queryCase{
		{"/api/v1/runbooks?page=0", "invalid_query"},
		{"/api/v1/runbooks?per_page=101", "invalid_query"},
		{"/api/v1/runbooks?tab=unknown", "invalid_query"},
		{"/api/v1/runbooks/draft?source=blank", "invalid_source"},
		{"/api/v1/search?q=a&q=b", "invalid_query"},
	} {
		t.Run(c.path, func(t *testing.T) {
			expectErrorCode(t, request(t, admin, f.BaseURLs[0], "GET", c.path, f.Environment, nil, nil), 422, c.code)
		})
	}
	// HTTP header OWS is stripped by the transport; internal whitespace is
	// preserved and must still fail environment validation.
	for _, env := range []string{"unknown-testlab-environment", f.Environment + "," + f.OtherEnvironment, f.Environment + " invalid"} {
		t.Run("InvalidEnvironment_"+env, func(t *testing.T) {
			expectErrorCode(t, request(t, admin, f.BaseURLs[1], "GET", "/api/v1/runbooks", env, nil, nil), 422, "invalid_environment")
		})
	}
	expect(t, request(t, admin, f.BaseURLs[1], "GET", "/api/v1/runbooks", " "+f.Environment+" ", nil, nil), 200)
	expectErrorCode(t, request(t, admin, f.BaseURLs[0], "GET", "/api/v1/runbooks", "", nil, nil), 422, "environment_required")
	expectErrorCode(t, request(t, admin, f.BaseURLs[1], "POST", "/api/v1/runbooks", "all", map[string]any{}, nil), 409, "env_view_only")
	expectErrorCode(t, request(t, admin, f.BaseURLs[0], "POST", "/api/v1/knowledge/search", f.Environment, map[string]any{"q": "", "top_k": 21}, nil), 422, "invalid_query")
	for _, scopes := range [][]string{{"connectors:manage"}, {"unknown:view"}, {"connectors:view", "connectors:view"}} {
		expectErrorCode(t, request(t, admin, f.BaseURLs[0], "POST", "/api/v1/api-tokens", f.Environment, map[string]any{"name": "invalid-testlab", "scopes": scopes, "ttl": 3600}, nil), 422, "invalid_token_request")
	}
}

func TestFormalV3OwnSettingsAcrossInstances(t *testing.T) {
	f := readFixture(t, true)
	db := contractPool(t, f)
	u := seedHuman(t, f, db, [][2]string{{"environment", "view"}})
	a, b := f.BaseURLs[0], f.BaseURLs[1]
	for _, body := range []map[string]any{{"theme": nil}, {"theme": "purple"}, {"unexpected": true}} {
		r := request(t, u, a, "PATCH", "/api/v1/me/preferences", f.Environment, body, nil)
		expect(t, r, 422)
		if r.Data["error"] == nil {
			t.Fatal("invalid settings omitted structured error")
		}
	}
	first := request(t, u, a, "PATCH", "/api/v1/me/preferences", f.Environment, map[string]any{"theme": "dark", "locale": "en-US"}, nil)
	expect(t, first, 200)
	second := request(t, u, b, "PATCH", "/api/v1/me/preferences", f.Environment, map[string]any{"density": "compact"}, nil)
	expect(t, second, 200)
	if second.Data["theme"] != "dark" || second.Data["locale"] != "en-US" || second.Data["density"] != "compact" {
		t.Fatal("cross-instance partial preference patch erased another field")
	}
	ai := request(t, u, a, "GET", "/api/v1/me/ai-preferences", f.Environment, nil, nil)
	expect(t, ai, 200)
	if f.ModelID == "" {
		t.Skip("model protocol Mock not configured; AI preference positive path remains unverified")
	}
	ai.Data["default"] = map[string]any{"model": f.ModelID}
	ai.Data["budget_warn_tokens"] = 9000
	expect(t, request(t, u, b, "PUT", "/api/v1/me/ai-preferences", f.Environment, ai.Data, nil), 200)
	got := request(t, u, a, "GET", "/api/v1/me/ai-preferences", f.Environment, nil, nil)
	expect(t, got, 200)
	if got.Data["budget_warn_tokens"] != float64(9000) {
		t.Fatal("AI preferences did not persist across instances")
	}
	before := request(t, u, a, "GET", "/api/v1/me/notification-settings", f.Environment, nil, nil)
	expect(t, before, 200)
	expect(t, request(t, u, b, "PUT", "/api/v1/me/notification-settings", f.Environment, map[string]any{"digest": "weekly"}, nil), 200)
	got = request(t, u, a, "GET", "/api/v1/me/notification-settings", f.Environment, nil, nil)
	expect(t, got, 200)
	if got.Data["digest"] != "weekly" {
		t.Fatal("notification settings did not persist")
	}
	expectErrorCode(t, request(t, u, b, "PUT", "/api/v1/me/notification-settings", f.Environment, map[string]any{"digest": "hourly"}, nil), 422, "invalid_settings")
	// Preferences are configuration only: no external delivery is exercised.
	expectErrorCode(t, request(t, u, a, "GET", "/api/v1/runbooks", f.OtherEnvironment, nil, nil), 403, "forbidden")
}

func cleanupContractRunbook(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Error("run-owned Runbook cleanup transaction unavailable")
			return
		}
		defer tx.Rollback(ctx)
		var source string
		if err := tx.QueryRow(ctx, `SELECT source FROM knowledge_documents WHERE id=$1 FOR UPDATE`, id).Scan(&source); err != nil || source != "runbook://v3/"+id {
			t.Error("run-owned Runbook cleanup refused an unexpected source")
			return
		}
		// Publication creates index generations and chunks. Delete this
		// fixture's full dependency graph atomically; immutable management
		// audit evidence remains intact, including the original failed run.
		for phase, statement := range []string{
			`DELETE FROM v3_runbook_validation_previews WHERE document_id=$1`,
			`DELETE FROM v3_runbook_reviews WHERE document_id=$1`,
			`DELETE FROM v3_runbooks WHERE document_id=$1`,
			`DELETE FROM knowledge_history WHERE document_id=$1`,
			`DELETE FROM knowledge_revisions WHERE document_id=$1`,
			`DELETE FROM knowledge_chunks WHERE generation_id IN (SELECT id FROM knowledge_generations WHERE document_id=$1)`,
			`DELETE FROM knowledge_generations WHERE document_id=$1`,
			`DELETE FROM knowledge_documents WHERE id=$1`,
		} {
			if _, err := tx.Exec(ctx, statement, id); err != nil {
				t.Errorf("run-owned Runbook cleanup phase %d failed: %v", phase, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Error("run-owned Runbook cleanup commit failed")
		}
	})
}

func TestFormalV3RunbookGovernanceAndDiscovery(t *testing.T) {
	f := readFixture(t, true)
	db := contractPool(t, f)
	admin := authenticatedClient(t, f)
	reviewer := seedHuman(t, f, db, [][2]string{{"environment", "view"}, {"knowledge", "view"}, {"knowledge", "manage"}})
	viewer := seedHuman(t, f, db, [][2]string{{"environment", "view"}, {"knowledge", "view"}})
	a, b, env := f.BaseURLs[0], f.BaseURLs[1], f.Environment
	title := "ContractRunbook" + store.ID()
	created := request(t, admin, a, "POST", "/api/v1/runbooks", env, map[string]any{"title": title, "source": "blank", "steps": []any{map[string]any{"kind": "check", "title": "Check synthetic state", "readonly": true}}, "status": "draft"}, nil)
	expect(t, created, 201)
	id := objectID(t, created)
	cleanupContractRunbook(t, db, id)
	expectErrorCode(t, request(t, viewer, b, "GET", "/api/v1/runbooks/"+id, env, nil, nil), 404, "not_found")
	preview := request(t, admin, b, "POST", "/api/v1/runbooks/"+id+"/dry-run", env, map[string]any{}, nil)
	expect(t, preview, 200)
	if preview.Data["mode"] != "validation_only" || preview.Data["steps"] != float64(1) || preview.Data["id"] == nil {
		t.Fatal("Runbook preview lost its persisted validation-only contract")
	}
	expect(t, request(t, admin, a, "POST", "/api/v1/runbooks/"+id+"/submit", env, map[string]any{}, nil), 200)
	expectErrorCode(t, request(t, admin, b, "POST", "/api/v1/runbooks/"+id+"/submit", env, map[string]any{}, nil), 409, "invalid_state")
	review := map[string]any{"decision": "approve", "note": "Synthetic second-human review"}
	expectErrorCode(t, request(t, admin, a, "POST", "/api/v1/runbooks/"+id+"/reviews", env, review, nil), 403, "self_review_forbidden")
	published := request(t, reviewer, b, "POST", "/api/v1/runbooks/"+id+"/reviews", env, review, nil)
	expect(t, published, 200)
	if published.Data["status"] != "published" {
		t.Fatal("review did not durably publish the Runbook")
	}
	for _, suffix := range []string{"", "/record", "/history"} {
		r := request(t, viewer, a, "GET", "/api/v1/runbooks/"+id+suffix, env, nil, nil)
		expect(t, r, 200)
		if r.Data["id"] != id {
			t.Fatal("published record/history selected a different Runbook")
		}
		if suffix == "/history" {
			rows, _ := r.Data["validation_previews"].([]any)
			if len(rows) != 1 || r.Data["execution_verification_available"] != false {
				t.Fatal("validation history was mistaken for executed verification")
			}
		}
		expectErrorCode(t, request(t, viewer, b, "GET", "/api/v1/runbooks/"+id+suffix, f.OtherEnvironment, nil, nil), 403, "forbidden")
	}
	list := request(t, viewer, b, "GET", "/api/v1/runbooks?q="+url.QueryEscape(title)+"&page=1&per_page=1", env, nil, nil)
	expect(t, list, 200)
	items, _ := list.Data["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != id || list.Data["total"] != float64(1) || list.Data["has_next"] != false {
		t.Fatal("filtered Runbook pagination lost its published record")
	}
	// Discovery uses the same published-reference object ACL, without claiming
	// complete usage, resolution, or real embedding-provider connectivity.
	discovery := request(t, viewer, a, "GET", "/api/v1/search?q="+url.QueryEscape(title), env, nil, nil)
	expect(t, discovery, 200)
	if !strings.Contains(string(discovery.Raw), id) {
		t.Fatal("published Runbook reference missing from authorized discovery")
	}
}

func TestFormalV3AlertApplicationIsolationAndClaim(t *testing.T) {
	f := readFixture(t, true)
	db := contractPool(t, f)
	admin := authenticatedClient(t, f)
	reader := seedHuman(t, f, db, [][2]string{{"environment", "view"}})
	me := request(t, reader, f.BaseURLs[0], "GET", "/api/v1/auth/me", "", nil, nil)
	expect(t, me, 200)
	user, _ := me.Data["user"].(map[string]any)
	userID, _ := user["id"].(string)
	if userID == "" {
		t.Fatal("synthetic human session omitted owner")
	}
	apps, alerts := []string{store.ID(), store.ID()}, []string{store.ID(), store.ID()}
	for i, app := range apps {
		envRow := store.ID()
		t.Cleanup(func() {
			if _, err := db.Exec(context.Background(), `DELETE FROM ops_incidents WHERE application_id=$1`, app); err != nil {
				t.Error("owned alert cleanup failed")
			}
			if _, err := db.Exec(context.Background(), `UPDATE applications SET archived=true WHERE id=$1`, app); err != nil {
				t.Error("owned service archive failed")
			}
		})
		for _, entry := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO applications(id,key,name,environment) VALUES($1,$1,'Owned isolation fixture',$2)`, []any{app, f.Environment}},
			{`INSERT INTO application_environments(id,application_id,key,name) VALUES($1,$2,$3,'Test')`, []any{envRow, app, f.Environment}},
			{`INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,$2)`, []any{app, f.Environment}},
			{`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,labels,starts_at) VALUES($1,'synthetic-contract',$2,$3,$1,'Owned alert','critical','open','firing','{}',now())`, []any{alerts[i], app, envRow}},
		} {
			if _, err := db.Exec(t.Context(), entry.query, entry.args...); err != nil {
				t.Fatal("owned alert fixture creation failed")
			}
		}
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
		SELECT role_id,'events','view','application',$2 FROM rbac_user_roles WHERE user_id=$1`, userID, apps[0]); err != nil {
		t.Fatal("owned application view grant failed")
	}
	a, b, env := f.BaseURLs[0], f.BaseURLs[1], f.Environment
	allowed := request(t, reader, a, "GET", "/api/v1/alerts/"+alerts[0], env, nil, nil)
	expect(t, allowed, 200)
	if allowed.Data["id"] != alerts[0] {
		t.Fatal("allowed application returned a different alert")
	}
	// This pair uses the same selected environment: rejection must follow
	// persisted application scope, rather than just an environment header.
	expectErrorCode(t, request(t, reader, b, "GET", "/api/v1/alerts/"+alerts[1], env, nil, nil), 403, "forbidden")
	initial := request(t, admin, a, "GET", "/api/v1/alerts/"+alerts[0], env, nil, nil)
	expect(t, initial, 200)
	revision, _ := initial.Data["revision"].(string)
	if revision == "" {
		t.Fatal("claim precondition revision unavailable")
	}
	claim := request(t, admin, b, "POST", "/api/v1/alerts/"+alerts[0]+"/claim", env, map[string]any{"expected_revision": revision}, nil)
	expect(t, claim, 200)
	assignee, _ := claim.Data["assignee_id"].(string)
	claimedRevision, _ := claim.Data["revision"].(string)
	if assignee == "" || claimedRevision == "" || claimedRevision == revision {
		t.Fatal("claim did not commit ownership/revision")
	}
	expectErrorCode(t, request(t, admin, a, "POST", "/api/v1/alerts/"+alerts[0]+"/claim", env, map[string]any{"expected_revision": revision}, nil), 412, "claim_revision_changed")
	var before, after int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target=$1`, alerts[0]).Scan(&before); err != nil {
		t.Fatal(err)
	}
	expect(t, request(t, admin, a, "POST", "/api/v1/alerts/"+alerts[0]+"/claim", env, map[string]any{"expected_revision": claim.Data["revision"]}, nil), 200)
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target=$1`, alerts[0]).Scan(&after); err != nil || before != 1 || after != before {
		t.Fatal("idempotent claim retry created another ownership audit")
	}
}
