package rbac

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

type viewPolicyTrace struct{ calls atomic.Int32 }

func (q *viewPolicyTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "FROM v3_effective_role_grants") {
		q.calls.Add(1)
	}
	return ctx
}
func (*viewPolicyTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func newViewPolicyFixture(t *testing.T) (*V3Policy, *viewPolicyTrace) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated testlab migration DB required")
	}
	cfgGuard, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if cfgGuard.ConnConfig.Host != "127.0.0.1" || cfgGuard.ConnConfig.Port != 15461 || cfgGuard.ConnConfig.Database != "aiops_testlab" {
		t.Skip("dedicated disposable testlab DB required")
	}
	st := isolatedDB(t)
	for _, sql := range []string{
		`INSERT INTO users(id,email,name,role,password_hash) VALUES('list-view-user','list-view@example.invalid','List View','viewer','unused')`,
		`DELETE FROM rbac_user_roles WHERE user_id='list-view-user'`,
		`INSERT INTO rbac_roles(id,name) VALUES('list-view-role','List View Role')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES('list-view-user','list-view-role')`,
		`INSERT INTO teams(id,name) VALUES('list-view-team','List View Team'),('list-other-team','List Other Team')`,
		`INSERT INTO applications(id,key,name) VALUES('list-view-app','list-view-app','List App'),('list-other-app','list-other-app','Other App')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('list-view-app-dev','list-view-app','dev','Dev'),('list-other-app-staging','list-other-app','staging','Staging')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('list-view-app','dev'),('list-other-app','staging')`,
	} {
		if _, err := st.DB.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	trace := &viewPolicyTrace{}
	cfg := st.DB.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st.DB = pool
	return &V3Policy{Store: st}, trace
}

func TestAuthorizeViewWithTxMatchesOriginalScopedMatrix(t *testing.T) {
	p, trace := newViewPolicyFixture(t)
	type grant struct{ module, kind, id string }
	env := grant{"environment", "environment", "dev"}
	user := core.User{ID: "list-view-user"}
	base := V3Decision{Module: "events", Action: "view", EnvironmentID: "dev", ApplicationID: "list-view-app"}
	cases := []struct {
		name       string
		decision   V3Decision
		grants     []grant
		department bool
		disabled   bool
		demo       bool
		allow      bool
	}{
		{name: "application", decision: base, grants: []grant{env, {"events", "application", "list-view-app"}}, allow: true},
		{name: "environment", decision: base, grants: []grant{env, {"events", "environment", "dev"}}, allow: true},
		{name: "global", decision: base, grants: []grant{{"environment", "global", "*"}, {"events", "global", "*"}}, allow: true},
		{name: "missing_environment_gate", decision: base, grants: []grant{{"events", "global", "*"}}},
		{name: "missing_module", decision: base, grants: []grant{env}},
		{name: "wrong_application", decision: base, grants: []grant{env, {"events", "application", "list-other-app"}}},
		{name: "mapping_mismatch", decision: V3Decision{Module: "events", Action: "view", EnvironmentID: "dev", ApplicationID: "list-other-app"}, grants: []grant{env, {"events", "global", "*"}}},
		{name: "wrong_environment", decision: V3Decision{Module: "events", Action: "view", EnvironmentID: "staging", ApplicationID: "list-view-app"}, grants: []grant{env, {"events", "global", "*"}}},
		{name: "wrong_module", decision: V3Decision{Module: "conversation", Action: "view", EnvironmentID: "dev", ApplicationID: "list-view-app"}, grants: []grant{env, {"events", "global", "*"}}},
		{name: "wrong_action_grant", decision: base, grants: []grant{env}},
		{name: "exact_team", decision: V3Decision{Module: "events", Action: "view", EnvironmentID: "dev", TeamID: "list-view-team"}, grants: []grant{env, {"events", "team", "list-view-team"}}, allow: true},
		{name: "wrong_team", decision: V3Decision{Module: "events", Action: "view", EnvironmentID: "dev", TeamID: "list-other-team"}, grants: []grant{env, {"events", "team", "list-view-team"}}},
		{name: "department_role", decision: base, grants: []grant{env, {"events", "application", "list-view-app"}}, department: true, allow: true},
		{name: "disabled_persisted_actor", decision: base, grants: []grant{env, {"events", "global", "*"}}, disabled: true},
		{name: "settings_demo_actor", decision: base, grants: []grant{env, {"events", "global", "*"}}, demo: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := p.Store.DB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='list-view-role'; DELETE FROM rbac_department_roles WHERE role_id='list-view-role'; DELETE FROM rbac_user_roles WHERE user_id='list-view-user'`); err != nil {
				t.Fatal(err)
			}
			source, password, externalID := "local", "unused", ""
			disabled := c.disabled
			if c.demo {
				source, password, externalID, disabled = "settings_demo", "", "list-view-demo-id", true
			}
			var dept any
			if c.department {
				dept = "list-view-team"
			}
			if _, err := p.Store.DB.Exec(ctx, `UPDATE users SET disabled=$1,source=$2,department_id=$3,password_hash=$4,external_id=$5 WHERE id='list-view-user'`, disabled, source, dept, password, externalID); err != nil {
				t.Fatal(err)
			}
			if c.department {
				if _, err := p.Store.DB.Exec(ctx, `INSERT INTO rbac_department_roles(department_id,role_id) VALUES('list-view-team','list-view-role')`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := p.Store.DB.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES('list-view-user','list-view-role')`); err != nil {
					t.Fatal(err)
				}
			}
			for _, g := range c.grants {
				if _, err := p.Store.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('list-view-role',$1,'view',$2,$3)`, g.module, g.kind, g.id); err != nil {
					t.Fatal(err)
				}
			}
			if c.name == "wrong_action_grant" {
				if _, err := p.Store.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('list-view-role','events','change','application','list-view-app')`); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := p.Store.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			// Stabilize the same assignment/user facts as the list caller while comparing
			// both implementations. Neither policy evaluator itself caches or adds locks.
			if _, err = tx.Exec(ctx, `SELECT id FROM users WHERE id='list-view-user' FOR SHARE; LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			before := trace.calls.Load()
			old := p.AuthorizeWithTx(ctx, tx, user, c.decision)
			oldCount := trace.calls.Load() - before
			before = trace.calls.Load()
			got := p.AuthorizeViewWithTx(ctx, tx, user, c.decision)
			newCount := trace.calls.Load() - before
			if c.allow {
				if old != nil || got != nil {
					t.Fatalf("allowed matrix changed: original=%v combined=%v", old, got)
				}
			} else if !errors.Is(old, store.ErrForbidden) || !errors.Is(got, store.ErrForbidden) {
				t.Fatalf("denial changed: original=%v combined=%v", old, got)
			}
			if newCount != 1 {
				t.Errorf("fresh view policy statements=%d want=1", newCount)
			}
			if oldCount < 1 || oldCount > 2 {
				t.Errorf("original policy no longer uses original one/two branch statements: %d", oldCount)
			}
		})
	}
}

func TestAuthorizeViewWithTxRejectsMutationsAndPropagatesFailures(t *testing.T) {
	p, trace := newViewPolicyFixture(t)
	user := core.User{ID: "list-view-user"}
	tx, err := p.Store.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for _, action := range []string{"approve", "change", "delete", "execute", "manage"} {
		before := trace.calls.Load()
		err = p.AuthorizeViewWithTx(t.Context(), tx, user, V3Decision{Module: "events", Action: action, EnvironmentID: "dev", ApplicationID: "list-view-app"})
		if !errors.Is(err, store.ErrForbidden) || trace.calls.Load() != before {
			t.Fatalf("read-only entry accepted or queried mutation %s: %v", action, err)
		}
	}
	decision := V3Decision{Module: "events", Action: "view", EnvironmentID: "dev", ApplicationID: "list-view-app"}
	if err = p.AuthorizeViewWithTx(t.Context(), nil, user, decision); !errors.Is(err, ErrInvalidV3Decision) {
		t.Fatalf("nil tx accepted: %v", err)
	}
	invalid := decision
	invalid.TeamID = "list-view-team"
	if err = p.AuthorizeViewWithTx(t.Context(), tx, user, invalid); !errors.Is(err, ErrInvalidV3Decision) {
		t.Fatalf("mixed application/team scope accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = p.AuthorizeViewWithTx(ctx, tx, user, decision); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled policy did not propagate error: %v", err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed, err := p.Store.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = closed.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = p.AuthorizeViewWithTx(t.Context(), closed, user, decision); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("ended tx accepted: %v", err)
	}
	broken, err := p.Store.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Rollback(context.Background())
	if _, err = broken.Exec(t.Context(), `SET LOCAL search_path=pg_catalog`); err != nil {
		t.Fatal(err)
	}
	if err = p.AuthorizeViewWithTx(t.Context(), broken, user, decision); err == nil || errors.Is(err, store.ErrForbidden) {
		t.Fatalf("missing dependency became authorization success/denial instead of DB failure: %v", err)
	}
}
