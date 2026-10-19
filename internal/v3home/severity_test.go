package v3home

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PA-239: deliveries of an alert whose severity is unknown still count in
// the trend total; the summary says how many have no known severity instead
// of leaving them out.
func TestTrendCountsUnknownSeverityDeliveries(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_home_severity_" + store.ID()
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
	if _, err = db.Exec(ctx, `CREATE TABLE ops_alert_occurrences(incident_id text NOT NULL,received_at timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC)
	if _, err = db.Exec(ctx, `INSERT INTO ops_alert_occurrences VALUES('known',$1),('odd',$1),('odd',$2)`, now.Add(-time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	trend, err := loadTrend(ctx, tx, []incident{{id: "known", severity: "critical"}, {id: "odd", severity: "major"}}, now, text{})
	if err != nil {
		t.Fatal(err)
	}
	known := 0
	for _, bucket := range trend.Counts {
		for _, count := range bucket {
			known += count
		}
	}
	if known != 1 || trend.UnknownSeverity != 2 {
		t.Fatalf("known=%d unknown=%d; want 1, 2", known, trend.UnknownSeverity)
	}
	if !strings.HasPrefix(trend.Summary, "近 24 小时已入库告警接收记录 3 条") || !strings.Contains(trend.Summary, "2 条级别未知") {
		t.Fatalf("summary=%q", trend.Summary)
	}
}

// PA-239, PA-240: the attention list keeps an unknown severity null and
// says why; a "warning" stored for an alert without a severity label is
// unknown too.
func TestAttentionSeverityReason(t *testing.T) {
	recorded := map[string]string{"alertname": "X"}
	level, reason := attentionSeverity("major", recorded)
	if level != nil || reason == nil || *reason != "unclassified" {
		t.Errorf("major = %v,%v", level, reason)
	}
	level, reason = attentionSeverity("critical", recorded)
	if level == nil || *level != "P1" || reason != nil {
		t.Errorf("critical = %v,%v", level, reason)
	}
	level, reason = attentionSeverity("warning", recorded)
	if level != nil || reason == nil || *reason != "not_filled" {
		t.Errorf("default warning = %v,%v", level, reason)
	}
	level, reason = attentionSeverity("warning", nil)
	if level == nil || *level != "P2" || reason != nil {
		t.Errorf("warning without recorded labels = %v,%v", level, reason)
	}
}
