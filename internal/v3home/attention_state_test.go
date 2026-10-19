package v3home

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
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
		// PA-83: the time comes only in RFC 3339 (the start while firing,
		// otherwise the last update); meta no longer repeats it.
		wantMeta := "已入库事件"
		if item.AlertState != nil && *item.AlertState == "firing" {
			wantMeta = "已入库告警"
		}
		if at, err := time.Parse(time.RFC3339, item.At); err != nil || item.Meta != wantMeta ||
			(item.ID == "firing" && time.Since(at) < 50*time.Minute) || (item.ID == "resolved" && time.Since(at) > 10*time.Minute) {
			t.Fatalf("attention time: %+v %v", item, err)
		}
		// PA-86: an attention item opens its incident page; the frontend has
		// no /alerts/{id} route.
		if item.Href != "/incidents/"+item.ID || !frontendHomeRoute.MatchString(item.Href) {
			t.Fatalf("attention href: %+v", item)
		}
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

	// PA-83: a recent conversation carries its environment as ID and display
	// name (the ID when the instance has no label) and its time in RFC 3339,
	// instead of the "<environment> · <time>" meta text.
	if _, err = tx.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('u','u@example.test','U','viewer','x');
 INSERT INTO v3_conversations(id,owner_id,environment_id,title,updated_at) VALUES
 ('c-dev','u','dev','Dev question',now()-interval '1 minute'),('c-stg','u','stg','Stage question',now()-interval '2 minute')`); err != nil {
		t.Fatal(err)
	}
	recent := Summary{Availability: map[string]string{}}
	home := &Service{DB: db, Access: allowAll{}, EnvironmentLabels: map[string]string{"dev": "Development"}}
	if err = home.recentConversations(ctx, tx, core.User{ID: "u"}, []string{"dev", "stg"}, &recent); err != nil {
		t.Fatal(err)
	}
	if len(recent.Recent) != 2 || recent.Recent[0].ID != "c-dev" || recent.Recent[0].EnvironmentID != "dev" ||
		recent.Recent[0].EnvironmentLabel != "Development" || recent.Recent[1].EnvironmentID != "stg" ||
		recent.Recent[1].EnvironmentLabel != "stg" || recent.Recent[0].Href != "/conversations/c-dev" {
		t.Fatalf("recent: %+v", recent.Recent)
	}
	for _, item := range recent.Recent {
		if at, err := time.Parse(time.RFC3339, item.At); err != nil || at.Location() != time.UTC {
			t.Fatalf("recent time: %+v %v", item, err)
		}
	}
	if raw, err := json.Marshal(recent.Recent); err != nil || strings.Contains(string(raw), `"meta"`) {
		t.Fatalf("recent kept meta: %s %v", raw, err)
	}
}

type allowAll struct{}

func (allowAll) AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error {
	return nil
}

// frontendHomeRoute matches the frontend routes the home page links to:
// incidents/:id, review/:id and conversations/:id (web/src/v3/app/V3App.tsx).
var frontendHomeRoute = regexp.MustCompile(`^/(incidents|review|conversations)/[^/?#]+$`)

// PA-86: activity links use routes the frontend has, with the IDs escaped.
func TestHomeActivityLinksUseFrontendRoutes(t *testing.T) {
	now := time.Now()
	out := Summary{Availability: map[string]string{}}
	buildActivity([]incident{{id: "inc/1", title: "Checkout 5xx", updated: now.Add(-time.Minute)}},
		[]proposal{{id: "prop-1", title: "Roll back", created: now.Add(-2 * time.Minute)}}, now, text{}, &out)
	if len(out.Activity) != 2 || out.Activity[0].Href != "/incidents/inc%2F1" || out.Activity[1].Href != "/review/prop-1" {
		t.Fatalf("activity: %+v", out.Activity)
	}
	for _, item := range out.Activity {
		if !frontendHomeRoute.MatchString(item.Href) {
			t.Fatalf("activity href %q is not a frontend route", item.Href)
		}
	}
}
