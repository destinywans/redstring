package v3home

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

// PA-176: the home open-incident count matches the incident page's active
// tab: unresolved alerts (open or investigating) not shown through a case,
// plus unresolved cases. Attention still lists only unclaimed ones (D7, D58).
func TestHomeOpenCountMatchesIncidentPage(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_home_open_count_" + store.ID()
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
	cfg.MaxConns = 2
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if _, err = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('u','u@example.test','U','viewer','x');
 INSERT INTO applications(id,key,name) VALUES('app','app','App');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
 INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,suspected,starts_at,updated_at) VALUES
 ('open','am','app','app-dev','f1','Open','critical','open','resolved',false,now()-interval '1 hour',now()-interval '1 minute'),
 ('investigating','am','app','app-dev','f2','Investigating','critical','investigating','resolved',false,now()-interval '1 hour',now()-interval '2 minute'),
 ('closed','am','app','app-dev','f3','Closed','warning','resolved','resolved',false,now()-interval '1 hour',now()-interval '3 minute'),
 ('linked','am','app','app-dev','f4','Linked to a case','warning','investigating','resolved',false,now()-interval '1 hour',now()-interval '4 minute');
 INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,status,actor_id,request_key,request_hash) VALUES
 ('case','app','app-dev','dev','Case','P2','mitigated','u','request-1','`+hash+`');
 INSERT INTO v3_incident_case_alerts(case_id,alert_id) VALUES('case','linked')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	out := Summary{KPIs: []KPI{}, Attention: []Attention{}, Availability: map[string]string{}}
	targets := []target{{AppID: "app", Env: "dev", Name: "App", EventsView: true}}
	if err = (&Service{DB: db}).populate(ctx, tx, core.User{ID: "u"}, targets, nil, time.Now(), text{}, &out); err != nil {
		t.Fatal(err)
	}
	count := ""
	for _, k := range out.KPIs {
		if k.ID == "incidents" {
			count = k.Value
		}
	}
	// open + investigating + the unresolved case; the linked alert is the case.
	if count != "3" || !strings.Contains(out.Greeting.Summary, "3 个未解决事件") {
		t.Fatalf("open count=%q summary=%q", count, out.Greeting.Summary)
	}
	if len(out.Attention) != 1 || out.Attention[0].ID != "open" {
		t.Fatalf("attention=%+v", out.Attention)
	}
}
