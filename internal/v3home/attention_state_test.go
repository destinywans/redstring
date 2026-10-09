package v3home

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
)

// PA-58/D7: attention items carry the incident's current alert state, resolved
// but unclaimed incidents stay listed, and firing_services counts distinct
// services with a firing alert that is not merely suspected.
func TestAttentionCarriesAlertStateAndFiringServices(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_home_attention_" + store.ID()
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
	if _, err = db.Exec(ctx, `INSERT INTO applications(id,key,name) VALUES('app','app','App'),('other','other','Other');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev'),('other-dev','other','dev','Dev');
 INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,suspected,starts_at,updated_at) VALUES
 ('firing','am','app','app-dev','f1','Firing','critical','open','firing',false,now()-interval '1 hour',now()-interval '1 minute'),
 ('resolved','am','app','app-dev','f2','Resolved long ago','critical','open','resolved',false,now()-interval '3 day',now()-interval '2 minute'),
 ('claimed','am','app','app-dev','f3','Claimed','warning','investigating','firing',false,now()-interval '1 hour',now()-interval '3 minute'),
 ('suspected','ai-patrol','other','other-dev','f4','Suspected','warning','open','firing',true,now()-interval '1 hour',now()-interval '4 minute'),
 ('odd','am','other','other-dev','f5','Odd state','warning','open','unknown',false,now()-interval '1 hour',now()-interval '5 minute')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	out := Summary{KPIs: []KPI{}, Attention: []Attention{}, Availability: map[string]string{}}
	targets := []target{{AppID: "app", Env: "dev", Name: "App", EventsView: true}, {AppID: "other", Env: "dev", Name: "Other", EventsView: true}}
	if err = (&Service{DB: db}).populate(ctx, tx, core.User{ID: "u"}, targets, nil, time.Now(), text{}, &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range out.Attention {
		state := "null"
		if item.AlertState != nil {
			state = *item.AlertState
		}
		got[item.ID] = state
	}
	want := map[string]string{"firing": "firing", "resolved": "resolved", "suspected": "firing", "odd": "null"}
	if len(got) != len(want) {
		t.Fatalf("attention=%v", got)
	}
	for id, state := range want {
		if got[id] != state {
			t.Fatalf("attention=%v want %v", got, want)
		}
	}
	if out.FiringServices == nil || *out.FiringServices != 1 {
		t.Fatalf("firing_services=%v", out.FiringServices)
	}
	none := Summary{KPIs: []KPI{}, Attention: []Attention{}, Availability: map[string]string{}}
	if err = (&Service{DB: db}).populate(ctx, tx, core.User{ID: "u"}, []target{{AppID: "app", Env: "dev", Name: "App"}}, nil, time.Now(), text{}, &none); err != nil {
		t.Fatal(err)
	}
	if none.FiringServices != nil {
		t.Fatalf("firing_services without events grant=%v", *none.FiringServices)
	}
}
