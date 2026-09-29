package v3configsync

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3canvas"
)

// The two stores have independent schemas and pools. Only the repository
// fixture crosses the source/production boundary, as a reviewed MR would.
func isolatedPromotionStores(t *testing.T) (*store.Store, *store.Store, *fakeRepository) {
	t.Helper()
	source := isolatedConfigStore(t)
	production := isolatedConfigStore(t)
	if _, err := production.DB.Exec(t.Context(), `DELETE FROM v3_canvas_templates WHERE id='CT-source'`); err != nil {
		t.Fatal(err)
	}
	return source, production, &fakeRepository{}
}

func promotionCount(t *testing.T, db *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestConfigPromotionIndependentStoresRoundTrip(t *testing.T) {
	source, production, repo := isolatedPromotionStores(t)
	nonprodHTTP := configServer(t, source, repo, "nonprod")
	prodHTTP := configServer(t, production, repo, "production")

	for _, kind := range []string{"runbook", "alert_rule", "agent_version"} {
		response := configRequest(nonprodHTTP, "POST", "/api/v1/promotions", "test", "owner-token",
			`{"kind":"`+kind+`","id":"CT-source"}`)
		if response.Code != 501 || !strings.Contains(response.Body.String(), "promotion_kind_unavailable") {
			t.Fatalf("unsupported %s export: %d %s", kind, response.Code, response.Body.String())
		}
	}
	if response := configRequest(prodHTTP, "POST", "/api/v1/promotions", "prod", "admin-token",
		`{"kind":"templates","id":"CT-source"}`); response.Code != 409 || !strings.Contains(response.Body.String(), "already_production") {
		t.Fatalf("production export: %d %s", response.Code, response.Body.String())
	}
	if response := configRequest(nonprodHTTP, "POST", "/api/v1/config-sync/mr-42/import", "test", "owner-token", ""); response.Code != 409 || !strings.Contains(response.Body.String(), "production_only") {
		t.Fatalf("nonproduction import: %d %s", response.Code, response.Body.String())
	}

	exported := configRequest(nonprodHTTP, "POST", "/api/v1/promotions", "test", "owner-token",
		`{"kind":"templates","id":"CT-source"}`)
	if exported.Code != 200 || !strings.Contains(exported.Body.String(), `"mr":"!42"`) {
		t.Fatalf("source export: %d %s", exported.Code, exported.Body.String())
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_config_exports`) != 0 ||
		promotionCount(t, source.DB, `SELECT count(*) FROM v3_config_imports`) != 0 {
		t.Fatal("source export or production import ledger crossed the schema boundary")
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_canvas_templates`) != 0 {
		t.Fatal("production gained a template before import")
	}

	repo.merged = true
	merged := configRequest(prodHTTP, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	if merged.Code != 200 || !strings.Contains(merged.Body.String(), `"status":"merged"`) {
		t.Fatalf("verified merged candidate: %d %s", merged.Code, merged.Body.String())
	}
	for attempt := 0; attempt < 2; attempt++ {
		imported := configRequest(prodHTTP, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", "")
		if imported.Code != 200 || !strings.Contains(imported.Body.String(), `"status":"imported"`) {
			t.Fatalf("production import %d: %d %s", attempt, imported.Code, imported.Body.String())
		}
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_config_imports`) != 1 ||
		promotionCount(t, production.DB, `SELECT count(*) FROM management_audit WHERE action='v3.config_import.completed'`) != 1 ||
		promotionCount(t, source.DB, `SELECT count(*) FROM v3_config_imports`) != 0 {
		t.Fatal("import was not idempotent or its ledger/audit crossed the schema boundary")
	}
	var exportID string
	if err := source.DB.QueryRow(t.Context(), `SELECT id FROM v3_config_exports`).Scan(&exportID); err != nil {
		t.Fatal(err)
	}
	var owner, environment, title string
	if err := production.DB.QueryRow(t.Context(), `SELECT COALESCE(owner_id,''),COALESCE(environment_id,''),name
		FROM v3_canvas_templates WHERE id=$1`, "CTP-"+exportID).Scan(&owner, &environment, &title); err != nil ||
		owner != "" || environment != "" || title != "Safe plan" {
		t.Fatalf("persisted system template owner=%q environment=%q title=%q err=%v", owner, environment, title, err)
	}

	// Reconstruct both the DB pool and registered HTTP service from the
	// production schema, rather than relying on the import service's pool.
	config := production.DB.Config().Copy()
	freshPool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(freshPool.Close)
	freshProd := &store.Store{DB: freshPool}
	reconstructed := configServer(t, freshProd, repo, "production")
	if _, err := freshPool.Exec(t.Context(), `INSERT INTO v3_effective_role_grants
		(user_id,module,action,scope_kind,scope_id) VALUES('admin','canvas','view','environment','prod')`); err != nil {
		t.Fatal(err)
	}
	v3canvas.New(freshProd).Register(reconstructed)
	readBack := configRequest(reconstructed, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	if readBack.Code != 200 || !strings.Contains(readBack.Body.String(), `"status":"imported"`) {
		t.Fatalf("reconstructed production sync read: %d %s", readBack.Code, readBack.Body.String())
	}
	if err := freshPool.QueryRow(t.Context(), `SELECT name FROM v3_canvas_templates WHERE id=$1`, "CTP-"+exportID).Scan(&title); err != nil || title != "Safe plan" {
		t.Fatalf("fresh production pool template title=%q err=%v", title, err)
	}
	if promotionCount(t, freshPool, `SELECT count(*) FROM v3_config_imports`) != 1 {
		t.Fatal("fresh production pool cannot read the import ledger")
	}
	templates := configRequest(reconstructed, "GET", "/api/v1/canvas-templates", "prod", "admin-token", "")
	if templates.Code != 200 || !strings.Contains(templates.Body.String(), "CTP-"+exportID) ||
		!strings.Contains(templates.Body.String(), `"name":"Safe plan"`) {
		t.Fatalf("reconstructed production template read: %d %s", templates.Code, templates.Body.String())
	}
}

func TestConfigImportPreexistingSystemTemplateConflicts(t *testing.T) {
	source, production, repo := isolatedPromotionStores(t)
	nonprodHTTP := configServer(t, source, repo, "nonprod")
	prodHTTP := configServer(t, production, repo, "production")
	exported := configRequest(nonprodHTTP, "POST", "/api/v1/promotions", "test", "owner-token",
		`{"kind":"templates","id":"CT-source"}`)
	if exported.Code != 200 {
		t.Fatalf("source export: %d %s", exported.Code, exported.Body.String())
	}
	var exportID string
	if err := source.DB.QueryRow(t.Context(), `SELECT id FROM v3_config_exports`).Scan(&exportID); err != nil {
		t.Fatal(err)
	}
	if _, err := production.DB.Exec(t.Context(), `INSERT INTO v3_canvas_templates
		(id,owner_id,environment_id,kind,name,nodes,edges) VALUES($1,NULL,NULL,'plan','Unrelated','[]','[]')`,
		"CTP-"+exportID); err != nil {
		t.Fatal(err)
	}
	repo.merged = true
	response := configRequest(prodHTTP, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", "")
	if response.Code != 409 || !strings.Contains(response.Body.String(), "promotion_conflict") {
		t.Fatalf("preexisting target conflict: %d %s", response.Code, response.Body.String())
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_config_imports`) != 0 ||
		promotionCount(t, production.DB, `SELECT count(*) FROM management_audit WHERE action='v3.config_import.completed'`) != 0 {
		t.Fatal("conflicting target produced an import ledger or success audit")
	}
	var title string
	if err := production.DB.QueryRow(t.Context(), `SELECT name FROM v3_canvas_templates WHERE id=$1`, "CTP-"+exportID).Scan(&title); err != nil || title != "Unrelated" {
		t.Fatalf("conflicting template modified title=%q err=%v", title, err)
	}
}

func TestConfigImportPromotionLedgerConflictRollsBackTemplate(t *testing.T) {
	source, production, repo := isolatedPromotionStores(t)
	nonprodHTTP := configServer(t, source, repo, "nonprod")
	prodHTTP := configServer(t, production, repo, "production")
	exported := configRequest(nonprodHTTP, "POST", "/api/v1/promotions", "test", "owner-token",
		`{"kind":"templates","id":"CT-source"}`)
	if exported.Code != 200 {
		t.Fatalf("source export: %d %s", exported.Code, exported.Body.String())
	}
	var exportID string
	if err := source.DB.QueryRow(t.Context(), `SELECT id FROM v3_config_exports`).Scan(&exportID); err != nil {
		t.Fatal(err)
	}
	// A different MR has already claimed this promotion. The attempted target
	// insert must roll back when the later import-ledger constraint rejects it.
	if _, err := production.DB.Exec(t.Context(), `INSERT INTO v3_canvas_templates
		(id,owner_id,environment_id,kind,name,nodes,edges)
		VALUES('CTP-other',NULL,NULL,'plan','Previous import','[]','[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := production.DB.Exec(t.Context(), `INSERT INTO v3_config_imports
		(id,project_id,mr_iid,merge_sha,promotion_id,kind,target_id,actor_id)
		VALUES($1,77,41,$2,$3,'template','CTP-other','admin')`,
		store.ID(), strings.Repeat("c", 40), exportID); err != nil {
		t.Fatal(err)
	}
	repo.merged = true
	response := configRequest(prodHTTP, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", "")
	if response.Code != 409 || !strings.Contains(response.Body.String(), "promotion_conflict") {
		t.Fatalf("promotion ledger conflict: %d %s", response.Code, response.Body.String())
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_canvas_templates WHERE id=$1`, "CTP-"+exportID) != 0 ||
		promotionCount(t, production.DB, `SELECT count(*) FROM v3_config_imports`) != 1 ||
		promotionCount(t, production.DB, `SELECT count(*) FROM management_audit WHERE action='v3.config_import.completed'`) != 0 {
		t.Fatal("ledger conflict did not roll back the target or produced a success record")
	}
}
