package v3events

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
)

func wireListViewPolicy(f *listClaimFixture) {
	// Match the explicit composition-root callback; fallback and write callbacks
	// remain the original AuthorizeWithTx policy.
	f.svc.AuthorizeListTx = func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
		return f.policy.AuthorizeViewWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}
}

func TestListViewPolicyOneFreshStatementPerScope(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups"} {
		t.Run(path, func(t *testing.T) {
			f, trace := newListAuthorityFixture(t)
			wireListViewPolicy(f)
			for _, sql := range []string{
				`INSERT INTO applications(id,name,key,environment) SELECT 'app-'||n,'App '||n,'app-'||n,'dev' FROM generate_series(4,100) n`,
				`INSERT INTO application_environments(id,application_id,key,name) SELECT 'app-'||n||'-dev','app-'||n,'dev','Dev' FROM generate_series(4,100) n`,
				`INSERT INTO v3_application_environments(application_id,environment_id) SELECT 'app-'||n,'dev' FROM generate_series(4,100) n`,
				`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,updated_at,labels) SELECT 'incident-'||n,'fixture','app-'||n,'app-'||n||'-dev','fingerprint-'||n,'Title '||n,'warning','firing',now(),now()-interval '3 seconds',jsonb_build_object('application','app-'||n,'environment','dev') FROM generate_series(4,100) n`,
			} {
				if _, err := f.db.Exec(t.Context(), sql); err != nil {
					t.Fatal(err)
				}
			}
			response := f.request(t.Context(), http.MethodGet, path, f.tokens[0], "")
			var body struct {
				Counts struct {
					All int `json:"all"`
				} `json:"counts"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Counts.All != 100 {
				t.Fatalf("authorized list: %d %s", response.Code, response.Body.String())
			}
			policy := trace.count("FROM v3_effective_role_grants")
			if policy != 100 {
				t.Errorf("fresh policy SQL statements=%d want=100 (one combined current decision per distinct scope)", policy)
			}
			for _, fragment := range []string{"SELECT id FROM users", "SELECT user_id FROM sessions", "LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants"} {
				if n := trace.count(fragment); n != 1 {
					t.Errorf("shared held lock %q=%d want=1", fragment, n)
				}
			}
			if n := trace.count("FOR SHARE OF a,e,ve"); n != 100 {
				t.Errorf("fresh scope bindings=%d want=100", n)
			}
			wantClock := 100
			if path == "/api/v1/inbox" {
				wantClock = 101
			}
			if n := trace.count("SELECT expires_at>clock_timestamp()"); n != wantClock {
				t.Errorf("current DB clock checks=%d want=%d", n, wantClock)
			}
			t.Logf("policy SQL statements=%d, authority-only SQL=%d (includes Inbox precheck; excludes final clock/candidate/notification/middleware)", policy, policy+100+wantClock-1+3)
		})
	}
}

func TestListViewPolicyDoesNotReplaceDefaultOrClaimAuthorization(t *testing.T) {
	f, trace := newListAuthorityFixture(t)
	var calls atomic.Int32
	f.svc.AuthorizeListTx = func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
		calls.Add(1)
		return f.policy.AuthorizeViewWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}
	detail := f.request(t.Context(), http.MethodGet, "/api/v1/alerts/incident", f.tokens[0], "")
	if detail.Code != 200 {
		t.Fatalf("ordinary detail authorization changed: %d %s", detail.Code, detail.Body.String())
	}
	revision := f.revision(t)
	claim := f.request(t.Context(), http.MethodPost, "/api/v1/alerts/incident/claim", f.tokens[0], `{"expected_revision":"`+revision+`"}`)
	if claim.Code != 200 {
		t.Fatalf("first claim changed: %d %s", claim.Code, claim.Body.String())
	}
	after := f.revision(t)
	retry := f.request(t.Context(), http.MethodPost, "/api/v1/alerts/incident/claim", f.tokens[0], `{"expected_revision":"`+after+`"}`)
	if retry.Code != 200 || f.revision(t) != after {
		t.Fatalf("idempotent claim changed: %d %s", retry.Code, retry.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("ordinary detail/first/idempotent claim invoked list-only callback %d times", calls.Load())
	}
	if n := trace.count("FROM v3_effective_role_grants"); n != 10 {
		t.Errorf("detail plus first/retry claim view+change decisions must retain two SQL each: %d want=10", n)
	}
	var auditCount int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("claim audit after idempotent retry: count=%d err=%v", auditCount, err)
	}
	f.svc.AuthorizeListTx = nil
	before := trace.count("FROM v3_effective_role_grants")
	response := f.request(t.Context(), http.MethodGet, "/api/v1/inbox", f.tokens[0], "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "\"incident-b\"") {
		t.Fatalf("nil list callback compatibility changed: %d %s", response.Code, response.Body.String())
	}
	if n := trace.count("FROM v3_effective_role_grants") - before; n != 6 {
		t.Errorf("unconfigured three-scope list must retain original two-query policy: %d want=6", n)
	}
}

func TestListViewPolicyTeamFallbackStaysCurrentAndUsesOriginalPolicy(t *testing.T) {
	f, trace := newListAuthorityFixture(t)
	wireListViewPolicy(f)
	for _, sql := range []string{
		`INSERT INTO teams(id,name) VALUES('list-team-a','List Team A'),('list-team-b','List Team B')`,
		`UPDATE applications SET owner_team_id=CASE WHEN id='app' THEN 'list-team-a' ELSE 'list-team-b' END WHERE id IN ('app','app-b','app-c')`,
		`DELETE FROM v3_role_grants WHERE role_id='list-event-reader' AND module='events' AND action='view'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('list-event-reader','events','view','team','list-team-a')`,
	} {
		if _, err := f.db.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	var owners []string
	f.svc.AuthorizeTeamTx = func(ctx context.Context, tx pgx.Tx, user core.User, team, env, action string) error {
		owners = append(owners, team)
		return f.policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, TeamID: team, EnvironmentID: env})
	}
	response := f.request(t.Context(), http.MethodGet, "/api/v1/alert-groups", f.tokens[0], "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "\"incident\"") || strings.Contains(response.Body.String(), "\"incident-b\"") || strings.Contains(response.Body.String(), "\"incident-c\"") {
		t.Fatalf("current team fallback leaked scope: %d %s", response.Code, response.Body.String())
	}
	if strings.Join(owners, ",") != "list-team-a,list-team-b,list-team-b" {
		t.Fatalf("fallback owner teams=%v", owners)
	}
	if n := trace.count("FROM v3_effective_role_grants"); n != 9 {
		t.Errorf("three list single-query app decisions + three original two-query team decisions=%d want=9", n)
	}
}
