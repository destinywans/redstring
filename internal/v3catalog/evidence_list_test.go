package v3catalog

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// PA-50: the list shows exactly the services LookupServiceEvidence would
// return, by key, and skips hidden ones without a trace.
func TestListServiceEvidenceShowsOnlyReadableServices(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_catalog_list_" + store.ID()
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
	for _, statement := range []string{
		`INSERT INTO teams(id,name) VALUES('team-search','Search')`,
		`INSERT INTO applications(id,name,key,environment,owner_team_id,archived) VALUES
		 ('app-checkout','Checkout','checkout','test',NULL,false),
		 ('app-payments','Payments','payments','test',NULL,false),
		 ('app-search','Search','search','test','team-search',false),
		 ('app-old','Old','old','test',NULL,true)`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
		 ('checkout-test','app-checkout','test','Test'),('payments-test','app-payments','test','Test'),
		 ('search-test','app-search','test','Test'),('old-test','app-old','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
		 ('app-checkout','test'),('app-payments','test'),('app-search','test'),('app-old','test')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	broken := false
	s := New(st, catalogAccess(func(d rbac.V3Decision) error {
		if broken {
			return errors.New("policy unavailable")
		}
		if d.ApplicationID == "app-checkout" || d.TeamID == "team-search" {
			return nil
		}
		return store.ErrForbidden
	}), "test")
	user := core.User{ID: "user-1"}
	services, truncated, err := s.ListServiceEvidence(ctx, user, "test", 10)
	if err != nil || truncated || len(services) != 2 || services[0].Key != "checkout" || services[1].Key != "search" ||
		services[1].Team != "Search" {
		t.Fatalf("listed: %+v %v %v", services, truncated, err)
	}
	for _, service := range services {
		if found, err := s.LookupServiceEvidence(ctx, user, "test", service.Key); err != nil || found != service {
			t.Fatalf("list and lookup disagree for %s: %+v %v", service.Key, found, err)
		}
	}
	if services, truncated, err = s.ListServiceEvidence(ctx, user, "test", 1); err != nil || !truncated || len(services) != 1 {
		t.Fatalf("limit: %+v %v %v", services, truncated, err)
	}
	if services, _, err = s.ListServiceEvidence(ctx, user, "other", 10); err != nil || len(services) != 0 {
		t.Fatalf("foreign environment: %+v %v", services, err)
	}
	broken = true
	if _, _, err = s.ListServiceEvidence(ctx, user, "test", 10); err == nil {
		t.Fatal("a policy failure listed services")
	}
}
