package v3home

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTrendRangeUsesExactWindowAndTwentyFourBuckets(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_home_range_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
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
	cfg.MaxConns = 1
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	// Only the aggregate's actual input is needed; no other domain or external source is touched.
	if _, err = db.Exec(ctx, `CREATE TABLE ops_alert_occurrences(incident_id text NOT NULL,received_at timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 4, 23, 37, 0, time.UTC)
	for _, duration := range []time.Duration{15 * time.Minute, 24 * time.Hour, 30 * 24 * time.Hour} {
		if _, err = db.Exec(ctx, `TRUNCATE ops_alert_occurrences`); err != nil {
			t.Fatal(err)
		}
		start, step := now.Add(-duration), duration/24
		for _, at := range []time.Time{start.Add(-time.Second), start, start.Add(step), now, now.Add(time.Second)} {
			if _, err = db.Exec(ctx, `INSERT INTO ops_alert_occurrences VALUES('visible',$1),('unseen',$1)`, at); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		trend, err := loadTrend(ctx, tx, []incident{{id: "visible", severity: "critical"}}, now, text{}, duration)
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatal(duration, err)
		}
		if len(trend.Counts) != 24 || trend.Counts[0][0] != 1 || trend.Counts[1][0] != 1 || trend.Counts[23][0] != 1 {
			t.Fatalf("%s incorrect boundary buckets: %+v", duration, trend.Counts)
		}
		// Clients label buckets from start + i*step in their own time zone.
		// The UTC "15:04" labels were removed (PA-83).
		if raw, _ := json.Marshal(trend); trend.Start != start.UTC().Format(time.RFC3339) || trend.StepSeconds != step.Seconds() ||
			strings.Contains(string(raw), `"labels"`) {
			t.Fatalf("%s trend axis: %s", duration, raw)
		}
		total := 0
		for _, bucket := range trend.Counts {
			for _, count := range bucket {
				total += count
			}
		}
		if total != 3 {
			t.Fatalf("%s leaked outside window or incident: %d", duration, total)
		}
	}
}

func TestActivityRangeIncludesBoundaryAndExcludesFuture(t *testing.T) {
	now := time.Date(2026, 10, 7, 4, 23, 37, 0, time.UTC)
	out := Summary{Availability: map[string]string{}}
	buildActivity([]incident{{id: "old", updated: now.Add(-time.Hour)}, {id: "boundary", updated: now.Add(-15 * time.Minute)}, {id: "future", updated: now.Add(time.Second)}}, []proposal{{id: "recent", created: now.Add(-time.Minute)}}, now, text{}, &out, 15*time.Minute)
	if len(out.Activity) != 2 || out.Activity[0].ID != "proposal:recent" || out.Activity[1].ID != "incident:boundary" {
		t.Fatalf("range leaked records: %+v", out.Activity)
	}
}
