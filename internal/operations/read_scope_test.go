package operations

import (
	"context"
	"fmt"
	"github.com/destinywans/redstring/internal/actions"
	"github.com/destinywans/redstring/internal/core"
	"testing"
)

func TestDashboardMultiScopeBeforeLimitAndACL(t *testing.T) {
	s := isolatedAutomation(t)
	base := context.Background()
	if err := actions.New(s.DB, nil, "").Migrate(base); err != nil {
		t.Fatal(err)
	}
	op := core.User{ID: "op", Role: "operator"}
	if _, err := s.DB.Exec(base, `INSERT INTO applications(id,key,name) VALUES('hidden','hidden','Hidden');INSERT INTO application_environments(id,application_id,key,name) VALUES('hidden-env','hidden','prod','Production')`); err != nil {
		t.Fatal(err)
	}
	// Put selected e1 older than the default list cap, so post-limit filtering fails.
	for i := 0; i < 205; i++ {
		env := "e2"
		if i == 0 {
			env = "e1"
		}
		if _, err := s.DB.Exec(base, `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,updated_at) VALUES($1,'test','a',$2,$1,'Test','warning','firing',now(),now()+($3 * interval '1 second'))`, fmt.Sprint(i), env, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(base, `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at) VALUES('private','test','hidden','hidden-env','private','Private','warning','firing',now())`); err != nil {
		t.Fatal(err)
	}
	ctx := core.WithReadScope(base, core.ReadScope{ApplicationIDs: []string{"a", "hidden"}, EnvironmentIDs: []string{"e1", "hidden-env"}})
	items, err := s.Incidents(ctx, op, "", "")
	if err != nil || len(items) != 1 || items[0].EnvironmentID != "e1" {
		t.Fatalf("filter/ACL before cap: %+v %v", items, err)
	}
	dashboard, err := s.Dashboard(ctx, op)
	if err != nil || len(dashboard.Incidents) != 1 {
		t.Fatalf("dashboard scoped children: %+v %v", dashboard, err)
	}
	ctx = core.WithReadScope(base, core.ReadScope{ApplicationIDs: []string{"hidden"}})
	items, err = s.Incidents(ctx, op, "", "")
	if err != nil || len(items) != 0 {
		t.Fatal("explicit filter bypassed ACL")
	}
}
