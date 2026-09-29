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

func TestSaveSnapshotPreservesUnchangedRelationRevision(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "inventory_relations_" + store.ID()
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

	base := time.Now().UTC().Add(-2 * time.Minute)
	resources := []Record{
		{ExternalID: "a", Kind: "service", Name: "a"},
		{ExternalID: "b", Kind: "service", Name: "b"},
		{ExternalID: "c", Kind: "service", Name: "c"},
	}
	unchanged := Edge{SourceExternalID: "b", TargetExternalID: "c", Kind: "depends_on"}
	retired := Edge{SourceExternalID: "a", TargetExternalID: "b", Kind: "depends_on"}
	snapshot := Snapshot{SourceID: "source-a", SourceKind: "kubernetes", SourceName: "A", EnvironmentKey: "dev",
		ObservedAt: base, Resources: resources, Relations: []Edge{unchanged, retired}}
	if err := SaveSnapshot(ctx, db, snapshot); err != nil {
		t.Fatal(err)
	}
	a, b, c := ResourceID("kubernetes", "source-a", "a"), ResourceID("kubernetes", "source-a", "b"), ResourceID("kubernetes", "source-a", "c")
	otherSource := Snapshot{SourceID: "source-b", SourceKind: "kubernetes", SourceName: "B", EnvironmentKey: "dev",
		ObservedAt: base, Resources: resources[:2], Relations: []Edge{retired}}
	if err := SaveSnapshot(ctx, db, otherSource); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id)
 VALUES('declared-edge',$1,$2,'declared_dep','declared','kubernetes','source-a')`, a, b); err != nil {
		t.Fatal(err)
	}
	relationID := func(from, to string) string {
		return ResourceID("kubernetes", "source-a", from+":depends_on:"+to)
	}
	unchangedID, retiredID := relationID(b, c), relationID(a, b)
	// Use a fixed old revision so the test does not depend on wall-clock
	// resolution or how quickly two snapshots commit.
	oldRevision := base.Add(-time.Hour).Truncate(time.Microsecond)
	if _, err := db.Exec(ctx, `UPDATE cmdb_resource_relations SET updated_at=$2 WHERE id=$1`, unchangedID, oldRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE cmdb_resource_relations SET is_mock=true,updated_at=$2 WHERE id=$1`, retiredID, oldRevision); err != nil {
		t.Fatal(err)
	}
	snapshot.ObservedAt = base.Add(time.Second)
	if err := SaveSnapshot(ctx, db, snapshot); err != nil {
		t.Fatal(err)
	}
	var mock bool
	var restoredRevision time.Time
	if err := db.QueryRow(ctx, `SELECT is_mock,updated_at FROM cmdb_resource_relations WHERE id=$1`, retiredID).Scan(&mock, &restoredRevision); err != nil {
		t.Fatal(err)
	}
	if mock || !restoredRevision.After(oldRevision) {
		t.Fatalf("restored discovered relation: is_mock=%t revision=%s", mock, restoredRevision)
	}
	var revision time.Time
	if err := db.QueryRow(ctx, `SELECT updated_at FROM cmdb_resource_relations WHERE id=$1`, unchangedID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if !revision.Equal(oldRevision) {
		t.Fatalf("unchanged relation revision=%s; want %s", revision, oldRevision)
	}

	// A changed edge receives a new identity and revision; another edge in
	// the same snapshot retains its revision. Retiring an omitted UID removes
	// every discovered edge that referenced it.
	newEdge := Edge{SourceExternalID: "a", TargetExternalID: "c", Kind: "depends_on"}
	snapshot.ObservedAt = base.Add(2 * time.Second)
	snapshot.Relations = []Edge{unchanged, newEdge}
	if err := SaveSnapshot(ctx, db, snapshot); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM cmdb_resource_relations WHERE id=$1`, retiredID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("retired relation count=%d; want 0", count)
	}
	newID := relationID(a, c)
	var newRevision time.Time
	if err := db.QueryRow(ctx, `SELECT updated_at FROM cmdb_resource_relations WHERE id=$1`, newID).Scan(&newRevision); err != nil {
		t.Fatal(err)
	}
	if !newRevision.After(oldRevision) {
		t.Fatalf("new relation revision=%s; want after %s", newRevision, oldRevision)
	}
	if err := db.QueryRow(ctx, `SELECT updated_at FROM cmdb_resource_relations WHERE id=$1`, unchangedID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if !revision.Equal(oldRevision) {
		t.Fatalf("unmodified relation revision=%s after edge change; want %s", revision, oldRevision)
	}

	snapshot.ObservedAt = base.Add(3 * time.Second)
	snapshot.Resources = resources[:2]
	snapshot.Relations = nil
	if err := SaveSnapshot(ctx, db, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM cmdb_resource_relations WHERE source_kind='kubernetes' AND source_id='source-a' AND provenance='discovered'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale discovered relation count=%d; want 0", count)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM cmdb_resource_relations WHERE (source_kind='kubernetes' AND source_id='source-b' AND provenance='discovered') OR id='declared-edge'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("unrelated and declared relation count=%d; want 2", count)
	}
	var presence string
	if err := db.QueryRow(ctx, `SELECT attributes->>'inventory_presence' FROM cmdb_resources WHERE id=$1`, c).Scan(&presence); err != nil {
		t.Fatal(err)
	}
	if presence != "missing" {
		t.Fatalf("omitted UID presence=%q; want missing", presence)
	}
}
