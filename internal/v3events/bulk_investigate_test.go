package v3events

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PA-48: an investigation opened from an alert group carries the INV- prefix
// like one created on the canvas.
func TestBulkInvestigateUsesTheCanvasIDPrefix(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_bulk_investigate_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-bulk-password"); err != nil {
		t.Fatal(err)
	}
	starts := time.Now().UTC().Add(-time.Minute)
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
		INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
		VALUES('incident','source','app','app-dev','fp','Checkout 5xx','critical','firing',$1)`, starts); err != nil {
		t.Fatal(err)
	}
	var user core.User
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&user.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	group := lockedBulkGroup{EnvironmentID: "app-dev", Title: "Checkout 5xx", Members: []bulkMember{{ID: "incident",
		SourceID: "source", Fingerprint: "fp", Status: "open", AlertStatus: "firing", StartsAt: starts}}}
	result, err := investigateBulkGroup(ctx, tx, user, "group", "dev", groupKey{EnvironmentID: "dev", ApplicationID: "app",
		SourceID: "source", Fingerprint: "fp"}, group)
	if err != nil || result.InvestigationID == nil || !strings.HasPrefix(*result.InvestigationID, "INV-") ||
		len(*result.InvestigationID) != len("INV-")+32 {
		t.Fatalf("investigation ID: %+v %v", result, err)
	}
	var linked string
	if err := tx.QueryRow(ctx, `SELECT investigation_id FROM ops_incidents WHERE id='incident'`).Scan(&linked); err != nil || linked != *result.InvestigationID {
		t.Fatalf("incident link: %q %v", linked, err)
	}
}
