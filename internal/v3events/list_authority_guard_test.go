package v3events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

type listAuthorityQueryKey struct{}

type listAuthorityTrace struct {
	mu              sync.Mutex
	queries         []string
	before          func(context.Context, string)
	zeroBindingRows int
}

func (q *listAuthorityTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	q.mu.Lock()
	q.queries = append(q.queries, data.SQL)
	q.mu.Unlock()
	if q.before != nil {
		q.before(ctx, data.SQL)
	}
	return context.WithValue(ctx, listAuthorityQueryKey{}, data.SQL)
}
func (q *listAuthorityTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(listAuthorityQueryKey{}).(string)
	if strings.Contains(sql, "FOR SHARE OF a,e,ve") && data.Err == nil && data.CommandTag.RowsAffected() == 0 {
		q.mu.Lock()
		q.zeroBindingRows++
		q.mu.Unlock()
	}
}
func (q *listAuthorityTrace) noRowsBindings() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.zeroBindingRows
}
func (q *listAuthorityTrace) count(fragment string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, query := range q.queries {
		if strings.Contains(query, fragment) {
			n++
		}
	}
	return n
}

func newListAuthorityFixture(t *testing.T) (*listClaimFixture, *listAuthorityTrace) {
	t.Helper()
	f := newListClaimFixture(t)
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES('app-b','App B','app-b','dev'),('app-c','App C','app-c','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-b-dev','app-b','dev','Dev'),('app-c-dev','app-c','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-b','dev'),('app-c','dev')`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,updated_at,labels)
		VALUES('incident-b','fixture','app-b','app-b-dev','fingerprint-b','App B title','warning','firing',now(),now()-interval '1 second','{"application":"app-b","environment":"dev"}'),
		('incident-c','fixture','app-c','app-c-dev','fingerprint-c','App C title','warning','firing',now(),now()-interval '2 seconds','{"application":"app-c","environment":"dev"}')`,
	} {
		if _, err := f.db.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	trace := &listAuthorityTrace{}
	cfg := f.db.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.db, f.svc.DB = pool, pool
	// Exercise the same explicit list-view entry point as cmd/aiops. Default/nil
	// compatibility is separately verified in list_policy_query_test.go.
	wireListViewPolicy(f)
	return f, trace
}

func TestListAuthorityGuardUsesOneLockedActorAndCurrentClockPerScope(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups"} {
		t.Run(path, func(t *testing.T) {
			f, trace := newListAuthorityFixture(t)
			response := f.request(t.Context(), http.MethodGet, path, f.tokens[0], "")
			if response.Code != 200 || !strings.Contains(response.Body.String(), "\"incident-b\"") || !strings.Contains(response.Body.String(), "\"incident-c\"") {
				t.Fatalf("authorized multi-scope list: status=%d body=%s", response.Code, response.Body.String())
			}
			for _, fragment := range []string{"SELECT id FROM users", "SELECT user_id FROM sessions", "LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants"} {
				if count := trace.count(fragment); count != 1 {
					t.Errorf("shared authority fact must be locked once per top-level tx: %q actual=%d expected=1", fragment, count)
				}
			}
			if count := trace.count("FOR SHARE OF a,e,ve"); count != 3 {
				t.Errorf("fresh application scopes=%d expected=3", count)
			}
			wantClock := 3 // Two cached scopes plus final wall-clock expiry.
			if path == "/api/v1/inbox" {
				wantClock = 4
			} // Inbox explicitly carries its successful precheck.
			if count := trace.count("SELECT expires_at>clock_timestamp()"); count != wantClock {
				t.Errorf("current DB wall-clock checks=%d expected=%d", count, wantClock)
			}
			t.Logf("authority SQL: actor=%d session=%d tables=%d scopes=%d current-clock=%d", trace.count("SELECT id FROM users"), trace.count("SELECT user_id FROM sessions"), trace.count("LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants"), trace.count("FOR SHARE OF a,e,ve"), trace.count("SELECT expires_at>clock_timestamp()"))
		})
	}
}

// waitAuthorityReader waits for an actual PostgreSQL lock dependency, rather
// than inferring lock ownership from a sleep or a Go-side guard flag.
func waitAuthorityReader(t *testing.T, f *listClaimFixture, blockerPID uint32, queryFragment string) uint32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var pid uint32
		err := f.db.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND position($2 in query)>0 LIMIT 1`, blockerPID, queryFragment).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected PostgreSQL lock dependency not observed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func authorityListAsync(t *testing.T, f *listClaimFixture, path string) <-chan *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() { defer cancel(); out <- f.request(ctx, http.MethodGet, path, f.tokens[0], "") }()
	t.Cleanup(cancel)
	return out
}

func TestListAuthorityGuardRechecksClockAfterCachedScopeWait(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups"} {
		t.Run(path, func(t *testing.T) {
			f, trace := newListAuthorityFixture(t)
			if _, err := f.db.Exec(t.Context(), `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1`, store.Hash(f.tokens[0])); err != nil {
				t.Fatal(err)
			}
			block, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer block.Rollback(context.Background())
			if _, err = block.Exec(t.Context(), `SELECT id FROM applications WHERE id='app-b' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			out := authorityListAsync(t, f, path)
			waitAuthorityReader(t, f, block.Conn().PgConn().PID(), "FOR SHARE OF a,e,ve")
			if trace.count("LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants") != 1 {
				t.Fatal("first scope must complete before cached scope waits")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			for {
				var expired bool
				if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1`, store.Hash(f.tokens[0])).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("fixture did not naturally expire")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err = block.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			response := <-out
			if response.Code != 401 || strings.Contains(response.Body.String(), "incident-b") {
				t.Fatalf("cached scope returned expired data: %d %s", response.Code, response.Body.String())
			}
			if trace.count("SELECT id FROM users") != 1 || trace.count("SELECT user_id FROM sessions") != 1 {
				t.Fatal("cached scope must use current clock without repeating held actor/session locks")
			}
		})
	}
}

func TestListAuthorityGuardHeldLocksSerializeRevocation(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups"} {
		for _, condition := range []string{"disable", "logout", "grant_revoke"} {
			t.Run(path+"/"+condition, func(t *testing.T) {
				f, _ := newListAuthorityFixture(t)
				block, err := f.db.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer block.Rollback(context.Background())
				if _, err = block.Exec(t.Context(), `SELECT id FROM applications WHERE id='app-b' FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
				out := authorityListAsync(t, f, path)
				readerPID := waitAuthorityReader(t, f, block.Conn().PgConn().PID(), "FOR SHARE OF a,e,ve")
				sql := `UPDATE users SET disabled=true WHERE id='alice'`
				args := []any{}
				if condition == "logout" {
					sql = `DELETE FROM sessions WHERE token_hash=$1`
					args = []any{store.Hash(f.tokens[0])}
				}
				if condition == "grant_revoke" {
					sql = `DELETE FROM v3_role_grants WHERE role_id='list-event-reader' AND module='events' AND action='view'`
				}
				writer := make(chan error, 1)
				ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
				defer cancel()
				go func() { _, e := f.db.Exec(ctx, sql, args...); writer <- e }()
				waitAuthorityReader(t, f, readerPID, sql)
				if err = block.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				response := <-out
				if response.Code != 200 || !strings.Contains(response.Body.String(), "\"incident-b\"") {
					t.Fatalf("held authority changed before read commit: %d %s", response.Code, response.Body.String())
				}
				if err = <-writer; err != nil {
					t.Fatal(err)
				}
				next := f.request(t.Context(), http.MethodGet, path, f.tokens[0], "")
				if condition == "grant_revoke" {
					if next.Code != 200 || strings.Contains(next.Body.String(), "\"incident-b\"") {
						t.Fatalf("next tx cached revoked grant: %d %s", next.Code, next.Body.String())
					}
				} else if next.Code != 401 {
					t.Fatalf("next tx cached inactive actor/session: %d %s", next.Code, next.Body.String())
				}
			})
		}
	}
}

func TestListAuthorityGuardInactiveBeforeFirstSuccess(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups"} {
		for _, condition := range []string{"disable", "logout"} {
			t.Run(path+"/"+condition, func(t *testing.T) {
				f, _ := newListAuthorityFixture(t)
				block, err := f.db.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer block.Rollback(context.Background())
				lock := `SELECT id FROM applications WHERE id='app' FOR UPDATE`
				wait := "FOR SHARE OF a,e,ve"
				if path == "/api/v1/inbox" {
					lock = `SELECT id FROM users WHERE id='alice' FOR UPDATE`
					wait = "SELECT id FROM users"
				}
				if _, err = block.Exec(t.Context(), lock); err != nil {
					t.Fatal(err)
				}
				out := authorityListAsync(t, f, path)
				waitAuthorityReader(t, f, block.Conn().PgConn().PID(), wait)
				if condition == "disable" {
					_, err = block.Exec(t.Context(), `UPDATE users SET disabled=true WHERE id='alice'`)
				} else {
					_, err = block.Exec(t.Context(), `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(f.tokens[0]))
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = block.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				response := <-out
				if response.Code != 401 || strings.Contains(response.Body.String(), "\"incident-b\"") {
					t.Fatalf("inactive authority before first held proof accepted: %d %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestListAuthorityGuardUsesEachCurrentOwnerTeam(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups"} {
		t.Run(path, func(t *testing.T) {
			f, trace := newListAuthorityFixture(t)
			for _, sql := range []string{
				`INSERT INTO teams(id,name) VALUES('team-a','Team A'),('team-b','Team B')`,
				`UPDATE applications SET owner_team_id=CASE WHEN id='app' THEN 'team-a' ELSE 'team-b' END WHERE id IN ('app','app-b','app-c')`,
				`DELETE FROM v3_role_grants WHERE role_id='list-event-reader' AND module='events' AND action='view'`,
				`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('list-event-reader','events','view','team','team-a')`,
			} {
				if _, err := f.db.Exec(t.Context(), sql); err != nil {
					t.Fatal(err)
				}
			}
			var teams []string
			f.svc.AuthorizeTeamTx = func(ctx context.Context, tx pgx.Tx, u core.User, team, env, action string) error {
				teams = append(teams, team)
				return f.policy.AuthorizeWithTx(ctx, tx, u, rbac.V3Decision{Module: "events", Action: action, TeamID: team, EnvironmentID: env})
			}
			response := f.request(t.Context(), http.MethodGet, path, f.tokens[0], "")
			if response.Code != 200 || strings.Contains(response.Body.String(), "\"incident-b\"") || strings.Contains(response.Body.String(), "\"incident-c\"") || !strings.Contains(response.Body.String(), "\"incident\"") {
				t.Fatalf("team fallback leaked another owner's scope: %d %s", response.Code, response.Body.String())
			}
			if strings.Join(teams, ",") != "team-a,team-b,team-b" {
				t.Fatalf("fallback owners=%v", teams)
			}
			if trace.count("LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants") != 1 {
				t.Fatal("fallback must retain one shared lock proof while re-evaluating each policy")
			}
		})
	}
}

func TestListAuthorityGuardMissingFirstScopeDoesNotHoldActorProof(t *testing.T) {
	f, trace := newListAuthorityFixture(t)
	// Pause after the candidate snapshot, before executing its first current
	// binding query. Archiving beforehand would simply filter out the candidate
	// and would not exercise the authorization query's NoRows branch.
	entered, release := make(chan struct{}), make(chan struct{})
	var pauseOnce, releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(resume)
	trace.before = func(ctx context.Context, query string) {
		if strings.Contains(query, "FOR SHARE OF a,e,ve") {
			pauseOnce.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
	}
	block, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	if _, err = block.Exec(t.Context(), `SELECT id FROM applications WHERE id='app-b' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	out := authorityListAsync(t, f, "/api/v1/alert-groups")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first binding query did not reach candidate-snapshot barrier")
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE applications SET archived=true WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	resume()
	waitAuthorityReader(t, f, block.Conn().PgConn().PID(), "FOR SHARE OF a,e,ve")
	if trace.count("SELECT id FROM users") != 0 || trace.count("LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants") != 0 {
		t.Fatal("missing first scope acquired a false held-authority proof")
	}
	if n := trace.noRowsBindings(); n != 1 {
		t.Fatalf("first current scope query must actually return SELECT 0, got %d", n)
	}
	t.Log("actual PG first binding query returned SELECT 0 after captured candidate archive; no user/session/grant proof held before B wait")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err = f.db.Exec(ctx, `UPDATE users SET disabled=true WHERE id='alice'`); err != nil {
		t.Fatal("missing scope must not block actor disable:", err)
	}
	if err = block.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	response := <-out
	if response.Code != 401 {
		t.Fatalf("missing scope allowed later disabled actor: %d %s", response.Code, response.Body.String())
	}
}

func TestListAuthorityGuardRejectsRequestOrTransactionReuse(t *testing.T) {
	f, trace := newListAuthorityFixture(t)
	const path = "/api/v1/test-authority-ownership"
	f.api.Register("GET "+path, "viewer", func(w http.ResponseWriter, r *http.Request) {
		tx, err := f.db.Begin(r.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		guard := newListAuthorityGuard(r, tx)
		v := incidentRecord{ApplicationID: "app", EnvironmentID: "app-dev", EnvironmentKey: "dev"}
		if err = f.svc.authorizeAlertWithGuard(r, tx, v, "view", guard); err != nil {
			t.Fatal(err)
		}
		other, err := f.db.Begin(r.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer other.Rollback(context.Background())
		before := trace.count("FOR SHARE OF a,e,ve")
		for _, attempt := range []struct {
			request *http.Request
			tx      pgx.Tx
			action  string
		}{
			{r.Clone(r.Context()), tx, "view"}, {r, other, "view"}, {r, tx, "change"},
		} {
			if err = f.svc.authorizeAlertWithGuard(attempt.request, attempt.tx, v, attempt.action, guard); !errors.Is(err, store.ErrForbidden) {
				t.Fatalf("foreign guard ownership accepted: %v", err)
			}
		}
		r.Header.Set("Cookie", "aiops_session="+f.tokens[1])
		if err = f.svc.authorizeAlertWithGuard(r, tx, v, "view", guard); !errors.Is(err, store.ErrForbidden) {
			t.Fatalf("changed token accepted: %v", err)
		}
		if trace.count("FOR SHARE OF a,e,ve") != before {
			t.Fatal("invalid guard ownership touched authorization SQL")
		}
		r.Header.Set("Cookie", "aiops_session="+f.tokens[0])
		if err = tx.Commit(r.Context()); err != nil {
			t.Fatal(err)
		}
		if err = f.svc.authorizeAlertWithGuard(r, tx, v, "view", guard); !errors.Is(err, pgx.ErrTxClosed) {
			t.Fatalf("ended transaction reused held proof: %v", err)
		}
		w.WriteHeader(204)
	})
	f.api.MarkV3ObjectRoute("GET "+path, "events", "view")
	if response := f.request(t.Context(), http.MethodGet, path, f.tokens[0], ""); response.Code != 204 {
		t.Fatalf("ownership regression handler not reached: %d %s", response.Code, response.Body.String())
	}
}

func TestListAuthorityGuardHundredScopeSQLBudget(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups"} {
		t.Run(path, func(t *testing.T) {
			f, trace := newListAuthorityFixture(t)
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
				t.Fatalf("100 distinct authorized scopes not returned: %d %s", response.Code, response.Body.String())
			}
			for _, fragment := range []string{"SELECT id FROM users", "SELECT user_id FROM sessions", "LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants"} {
				if count := trace.count(fragment); count != 1 {
					t.Errorf("held fact %q actual=%d want=1", fragment, count)
				}
			}
			if n := trace.count("FOR SHARE OF a,e,ve"); n != 100 {
				t.Errorf("binding re-reads=%d want=100", n)
			}
			if n := trace.count("g.module='environment'"); n != 100 {
				t.Errorf("environment policy=%d want=100", n)
			}
			if n := trace.count("g.module=$2"); n != 100 {
				t.Errorf("events policy=%d want=100", n)
			}
			clock, authority := 100, 302 // 5 + 99*(binding+clock+one fresh policy); final clock excluded.
			if path == "/api/v1/inbox" {
				clock, authority = 101, 303
			} // precheck2 + tables1 +100*3.
			if n := trace.count("SELECT expires_at>clock_timestamp()"); n != clock {
				t.Errorf("current DB clock queries=%d want=%d", n, clock)
			}
			actual := trace.count("SELECT id FROM users") + trace.count("SELECT user_id FROM sessions") + trace.count("LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants") + trace.count("FOR SHARE OF a,e,ve") + trace.count("FROM v3_effective_role_grants") + trace.count("SELECT expires_at>clock_timestamp()") - 1
			if actual != authority {
				t.Errorf("authority-only SQL excluding final clock=%d want=%d", actual, authority)
			}
			t.Logf("100 scopes authority SQL=%d (includes Inbox precheck, excludes final clock, candidate/notification SQL and HTTP middleware); bindings=100 policy=100 (two fresh EXISTS per statement) clock=%d actor/session/table=1/1/1", actual, clock)
		})
	}
}

// Only the external observation response is a fake; both authorization phases
// and the concurrent revocation/member change run on the real isolated PG.
func TestListAuthorityGuardPolicyStartsFreshAfterProviderRead(t *testing.T) {
	for _, condition := range []string{"grant_revoke", "member_change"} {
		t.Run(condition, func(t *testing.T) {
			f, trace := newListAuthorityFixture(t)
			f.svc.GroupPolicyBindings = map[string]GroupPolicyBinding{"dev": {SourceIDs: map[string]bool{"fixture": true}, EnvironmentLabel: "environment", ApplicationBindings: map[string]string{"app": "app"}}}
			observations := 0
			f.svc.GroupPolicySource = listPolicySourceFunc(func(ctx context.Context, _ string, _ map[string]string) (v3alertmanager.GroupPolicyObservation, error) {
				observations++
				sql := `DELETE FROM v3_role_grants WHERE role_id='list-event-reader' AND module='events' AND action='view'`
				if condition == "member_change" {
					sql = `UPDATE ops_incidents SET summary='changed after first transaction' WHERE id='incident'`
				}
				_, err := f.db.Exec(ctx, sql)
				return v3alertmanager.GroupPolicyObservation{}, err
			})
			id := localAlertGroupID(incidentRecord{EnvironmentID: "app-dev", ApplicationID: "app", SourceID: "fixture", Fingerprint: "fingerprint"})
			response := f.request(t.Context(), http.MethodGet, "/api/v1/alert-groups/"+id+"/policy", f.tokens[0], "")
			expected := 404
			if condition == "member_change" {
				expected = 409
			}
			if observations != 1 || response.Code != expected {
				t.Fatalf("two-phase guard boundary lost: observations=%d status=%d body=%s", observations, response.Code, response.Body.String())
			}
			for _, fragment := range []string{"SELECT id FROM users", "SELECT user_id FROM sessions", "LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants"} {
				if n := trace.count(fragment); n != 2 {
					t.Errorf("each top-level policy tx must acquire fresh proof: %q=%d want=2", fragment, n)
				}
			}
			if n := trace.count("FOR SHARE OF a,e,ve"); n != 6 {
				t.Errorf("each policy phase must re-read all scopes: %d want=6", n)
			}
		})
	}
}

func TestListAuthorityGuardCachedScopeHonorsRequestDeadline(t *testing.T) {
	f, _ := newListAuthorityFixture(t)
	block, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	if _, err = block.Exec(t.Context(), `SELECT id FROM applications WHERE id='app-b' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/test-authority-deadline"
	f.api.Register("GET "+path, "viewer", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		tx, err := f.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		guard := newListAuthorityGuard(r, tx)
		if err = f.svc.authorizeAlertWithGuard(r, tx, incidentRecord{ApplicationID: "app", EnvironmentID: "app-dev", EnvironmentKey: "dev"}, "view", guard); err != nil {
			t.Fatal(err)
		}
		if err = f.svc.authorizeAlertWithGuard(r, tx, incidentRecord{ApplicationID: "app-b", EnvironmentID: "app-b-dev", EnvironmentKey: "dev"}, "view", guard); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cached guard ignored request deadline while waiting for scope: %v", err)
		}
		w.WriteHeader(204)
	})
	f.api.MarkV3ObjectRoute("GET "+path, "events", "view")
	if response := f.request(t.Context(), http.MethodGet, path, f.tokens[0], ""); response.Code != 204 {
		t.Fatalf("deadline regression handler not reached: %d %s", response.Code, response.Body.String())
	}
}
