package v3home

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
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
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer';
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),('viewer','events','view','application','app');
 INSERT INTO rbac_user_roles(user_id,role_id,origin) VALUES('u','viewer','direct') ON CONFLICT DO NOTHING;
 INSERT INTO applications(id,key,name)
 SELECT 'a-hidden-'||n,'hidden-'||n,'Hidden '||n FROM generate_series(1,5001) n;
 INSERT INTO application_environments(id,application_id,key,name)
 SELECT 'hidden-dev-'||n,'a-hidden-'||n,'dev','Dev' FROM generate_series(1,5001) n;
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev') ON CONFLICT DO NOTHING;
 INSERT INTO v3_application_environments(application_id,environment_id)
 SELECT 'a-hidden-'||n,'dev' FROM generate_series(1,5001) n ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	readTargets := func() ([]target, error) {
		t.Helper()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		return New(db, &rbac.V3Policy{Store: st}).targets(ctx, tx, core.User{ID: "u"}, []string{"dev"})
	}
	visible, err := readTargets()
	if err != nil || len(visible) != 1 || visible[0].AppID != "app" || !visible[0].EventsView {
		t.Fatalf("hidden home rows changed the view: %+v, %v", visible, err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','events','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := readTargets(); !errors.Is(err, errTooMany) {
		t.Fatalf("authorized home view exceeded the cap: %v", err)
	}
}
