package inventory

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestSaveSnapshotPreservesManualLinksAndRetiresMissingUID(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "inventory_links_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&store.Store{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES
 ('app-manual','Manual','manual','dev'),('app-collector','Collector','collector','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('env-manual','app-manual','dev','Dev'),('env-collector','app-collector','dev','Dev')`); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Add(-2 * time.Minute)
	record := func(uid, name string, bindings ...Link) Record {
		return Record{ExternalID: uid, Kind: "deployment", Name: name,
			Attributes: map[string]any{"uid": uid}, Applications: bindings}
	}
	collectorLink := Link{ApplicationID: "app-collector", EnvironmentID: "env-collector"}
	first := Snapshot{SourceID: "source-a", SourceKind: "kubernetes", SourceName: "A", EnvironmentKey: "dev", ObservedAt: base,
		Resources: []Record{record("old-uid", "api", collectorLink), record("other-uid", "worker", collectorLink)}}
	if err := SaveSnapshot(ctx, db, first); err != nil {
		t.Fatal(err)
	}
	if err := SaveSnapshot(ctx, db, Snapshot{SourceID: "source-b", SourceKind: "kubernetes", SourceName: "B", EnvironmentKey: "dev", ObservedAt: base,
		Resources: []Record{record("other-source-uid", "independent", collectorLink)}}); err != nil {
		t.Fatal(err)
	}
	oldID := ResourceID("kubernetes", "source-a", "old-uid")
	otherID := ResourceID("kubernetes", "source-a", "other-uid")
	otherSourceID := ResourceID("kubernetes", "source-b", "other-source-uid")
	// V3 registration owns its association, including a triple that was first
	// created by collector configuration.
	if _, err := db.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id,provenance)
 VALUES($1,'app-manual','env-manual','manual')`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id,provenance)
 VALUES($1,'app-collector','env-collector','manual')
 ON CONFLICT (resource_id,application_id,environment_id) DO UPDATE SET provenance='manual'`, oldID); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ObservedAt = base.Add(time.Second)
	second.Resources = []Record{record("old-uid", "api", collectorLink), record("other-uid", "worker")}
	if err := SaveSnapshot(ctx, db, second); err != nil {
		t.Fatal(err)
	}
	assertLink := func(id, app, want string) {
		t.Helper()
		var got string
		err := db.QueryRow(ctx, `SELECT provenance FROM cmdb_resource_applications WHERE resource_id=$1 AND application_id=$2`, id, app).Scan(&got)
		if want == "" && err == pgx.ErrNoRows {
			return
		}
		if err != nil || got != want {
			t.Fatalf("resource %s app %s: provenance=%q err=%v; want %q", id, app, got, err, want)
		}
	}
	assertLink(oldID, "app-manual", "manual")
	assertLink(oldID, "app-collector", "manual")
	assertLink(otherID, "app-collector", "")
	assertLink(otherSourceID, "app-collector", "discovered")

	// Failed and older observations do not retire a current manual link.
	if err := SaveSnapshot(ctx, db, Snapshot{SourceID: "source-a", SourceKind: "kubernetes", SourceName: "A", Error: "collector unavailable"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSnapshot(ctx, db, first); err != nil {
		t.Fatal(err)
	}
	assertLink(oldID, "app-manual", "manual")

	// A complete source snapshot removes the old UID. The replacement has a
	// distinct identity and must not inherit the manual service association.
	third := second
	third.ObservedAt = base.Add(2 * time.Second)
	third.Resources = []Record{record("replacement-uid", "api"), record("other-uid", "worker", collectorLink)}
	if err := SaveSnapshot(ctx, db, third); err != nil {
		t.Fatal(err)
	}
	assertLink(oldID, "app-manual", "")
	assertLink(oldID, "app-collector", "")
	assertLink(otherID, "app-collector", "discovered")
	assertLink(otherSourceID, "app-collector", "discovered")
	assertLink(ResourceID("kubernetes", "source-a", "replacement-uid"), "app-manual", "")
	var presence string
	if err := db.QueryRow(ctx, `SELECT attributes->>'inventory_presence' FROM cmdb_resources WHERE id=$1`, oldID).Scan(&presence); err != nil {
		t.Fatal(err)
	}
	if presence != "missing" {
		t.Fatalf("old UID presence=%q; want missing", presence)
	}
}
