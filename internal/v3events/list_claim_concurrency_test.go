package v3events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

type listPauseKey struct{}
type listClaimFixture struct {
	db     *pgxpool.Pool
	api    *httpapi.Server
	svc    *Service
	policy *rbac.V3Policy
	tokens []string
}

func newListClaimFixture(t *testing.T) *listClaimFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated random schema")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Port != 15461 || cfg.ConnConfig.Database != "aiops_testlab" {
		t.Skip("dedicated disposable testlab migration DB required")
	}
	ctx := t.Context()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_list_claim_" + store.ID()
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
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, e := admin.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 10
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('alice','alice-list@example.invalid','Alice','viewer',$1),('bob','bob-list@example.invalid','Bob','viewer',$1)`, store.PasswordHash("isolated-list-claim-password")); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DELETE FROM rbac_user_roles WHERE user_id IN ('alice','bob')`,
		`INSERT INTO rbac_roles(id,name) VALUES('list-event-reader','List Event Reader')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES('alice','list-event-reader'),('bob','list-event-reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('list-event-reader','environment','view','environment','dev'),('list-event-reader','events','view','environment','dev'),('list-event-reader','events','change','environment','dev')`,
		`INSERT INTO applications(id,name,key,environment) VALUES('app','Authorized App','app','dev'),('foreign','Foreign App','foreign','staging')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev'),('foreign-staging','foreign','staging','Staging')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('foreign','staging')`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,summary,severity,alert_status,starts_at,labels) VALUES('incident','fixture','app','app-dev','fingerprint','Authorized old title','Authorized old summary','warning','firing',now(),'{"application":"app","environment":"dev"}')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	f := &listClaimFixture{db: db, policy: &rbac.V3Policy{Store: st}}
	for _, email := range []string{"alice-list@example.invalid", "bob-list@example.invalid"} {
		_, token, e := st.Login(ctx, email, "isolated-list-claim-password")
		if e != nil {
			t.Fatal(e)
		}
		f.tokens = append(f.tokens, token)
	}
	f.svc = New(db, func(ctx context.Context, u core.User, app, env, action string) error {
		return f.policy.Authorize(ctx, u, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}, f.authorize)
	f.api = httpapi.New(st, nil, true)
	f.svc.RegisterNew(f.api)
	f.svc.RegisterV3Incidents(f.api)
	if err = f.api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, f.policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *listClaimFixture) authorize(ctx context.Context, tx pgx.Tx, u core.User, app, env, action string) error {
	return f.policy.AuthorizeWithTx(ctx, tx, u, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
}
func (f *listClaimFixture) request(ctx context.Context, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("X-AIOps-Environment", "dev")
	r.Header.Set("Idempotency-Key", "list-claim-create")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}
func (f *listClaimFixture) revision(t *testing.T) string {
	t.Helper()
	var revision string
	if err := f.db.QueryRow(t.Context(), `SELECT xmin::text FROM ops_incidents WHERE id='incident'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}
func (f *listClaimFixture) pausedList(t *testing.T, path string) (<-chan *httptest.ResponseRecorder, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.svc.AuthorizeTx = func(ctx context.Context, tx pgx.Tx, u core.User, app, env, action string) error {
		if ctx.Value(listPauseKey{}) == true {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return f.authorize(ctx, tx, u, app, env, action)
	}
	c, done := context.WithTimeout(context.WithValue(t.Context(), listPauseKey{}, true), 8*time.Second)
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() { defer done(); out <- f.request(c, http.MethodGet, path, f.tokens[0], "") }()
	resume := func() { once.Do(func() { close(release) }) }
	t.Cleanup(resume)
	select {
	case <-entered:
	case <-c.Done():
		resume()
		t.Fatal("list did not reach authority barrier")
	}
	return out, resume
}

func TestEventListDoesNotBlockFirstAndIdempotentClaims(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups", "/api/v1/incidents"} {
		t.Run(path, func(t *testing.T) {
			f := newListClaimFixture(t)
			out, resume := f.pausedList(t, path)
			defer resume()
			revision := f.revision(t)
			results := make(chan struct {
				index    int
				response *httptest.ResponseRecorder
			}, 2)
			for i := 0; i < 2; i++ {
				go func(i int) {
					c, done := context.WithTimeout(t.Context(), 2*time.Second)
					defer done()
					results <- struct {
						index    int
						response *httptest.ResponseRecorder
					}{i, f.request(c, http.MethodPost, "/api/v1/alerts/incident/claim", f.tokens[i], `{"expected_revision":"`+revision+`"}`)}
				}(i)
			}
			a, b := <-results, <-results
			if !((a.response.Code == 200 && b.response.Code == 412) || (b.response.Code == 200 && a.response.Code == 412)) {
				resume()
				<-out
				t.Fatalf("first claims blocked by paused read or lost mutual exclusion: %d/%d", a.response.Code, b.response.Code)
			}
			winner := a.index
			if b.response.Code == 200 {
				winner = b.index
			}
			newRevision := f.revision(t)
			c, done := context.WithTimeout(t.Context(), 2*time.Second)
			retry := f.request(c, http.MethodPost, "/api/v1/alerts/incident/claim", f.tokens[winner], `{"expected_revision":"`+newRevision+`"}`)
			done()
			if retry.Code != 200 {
				resume()
				<-out
				t.Fatalf("idempotent claim blocked by paused read: %d %s", retry.Code, retry.Body.String())
			}
			if f.revision(t) != newRevision {
				t.Fatal("idempotent retry changed xmin")
			}
			var audits int
			if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='alert.claim' AND target='incident'`).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("claim audit count=%d err=%v", audits, err)
			}
			resume()
			read := <-out
			if read.Code != 200 {
				t.Fatalf("authorized captured read failed: %d %s", read.Code, read.Body.String())
			}
			if path == "/api/v1/inbox" {
				var body struct {
					Counts struct {
						Mine int `json:"mine"`
					} `json:"counts"`
				}
				if json.Unmarshal(read.Body.Bytes(), &body) != nil || body.Counts.Mine != 0 {
					t.Fatal("read mixed newly assigned state into its captured incident snapshot")
				}
			}
		})
	}
}

func TestEventListCapturedScopeAndNotificationIsolation(t *testing.T) {
	for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups", "/api/v1/incidents"} {
		t.Run(path, func(t *testing.T) {
			f := newListClaimFixture(t)
			out, resume := f.pausedList(t, path)
			defer resume()
			c, done := context.WithTimeout(t.Context(), 2*time.Second)
			_, err := f.db.Exec(c, `UPDATE ops_incidents SET application_id='foreign',environment_id='foreign-staging',title='FORBIDDEN new title',summary='FORBIDDEN new summary',labels='{"application":"foreign","environment":"staging"}' WHERE id='incident'`)
			done()
			if err != nil {
				resume()
				<-out
				t.Fatal("read held incident scope update lock:", err)
			}
			if path == "/api/v1/alert-groups" {
				_, err = f.db.Exec(t.Context(), `INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,body) VALUES('foreign-note','incident','foreign','foreign-staging','fixture','foreign-note','FORBIDDEN new notification')`)
				if err != nil {
					t.Fatal(err)
				}
			}
			resume()
			read := <-out
			if strings.Contains(read.Body.String(), "FORBIDDEN") {
				t.Fatal("new unauthorized scope data leaked into captured projection")
			}
			if path != "/api/v1/alert-groups" {
				if read.Code != 200 || !strings.Contains(read.Body.String(), "Authorized old title") || path == "/api/v1/inbox" && !strings.Contains(read.Body.String(), "Authorized old summary") {
					t.Fatalf("captured authorized scope not preserved: %d %s", read.Code, read.Body.String())
				}
			} else if read.Code != 503 || !strings.Contains(read.Body.String(), "alert_group_unavailable") {
				t.Fatalf("cross-scope notification did not fail closed: %d %s", read.Code, read.Body.String())
			}
		})
	}
}

func TestEventListRechecksAuthorityAfterApplicationLockWait(t *testing.T) {
	for _, condition := range []string{"grant_revoke", "session_expire"} {
		for _, path := range []string{"/api/v1/inbox", "/api/v1/alert-groups", "/api/v1/incidents"} {
			t.Run(condition+path, func(t *testing.T) {
				f := newListClaimFixture(t)
				ctx := t.Context()
				if condition == "session_expire" {
					// Inbox locks the session before waiting on the application.
					// Let its real clock expiry pass; a concurrent forced UPDATE
					// would wait on our own reader's session SHARE lock.
					if _, err := f.db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1`, store.Hash(f.tokens[0])); err != nil {
						t.Fatal(err)
					}
				}
				blocker, err := f.db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if _, err = blocker.Exec(ctx, `SELECT id FROM applications WHERE id='app' FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
				out := make(chan *httptest.ResponseRecorder, 1)
				c, done := context.WithTimeout(ctx, 6*time.Second)
				defer done()
				go func() { out <- f.request(c, http.MethodGet, path, f.tokens[0], "") }()
				waitCtx, waitDone := context.WithTimeout(ctx, 2*time.Second)
				defer waitDone()
				for {
					var blocked bool
					err = f.db.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%FOR SHARE OF a,e,ve%')`, blocker.Conn().PgConn().PID()).Scan(&blocked)
					if err != nil {
						t.Fatal("read did not wait on application authorization:", err)
					}
					if blocked {
						break
					}
					select {
					case <-waitCtx.Done():
						t.Fatal("read did not reach application lock wait")
					case <-time.After(10 * time.Millisecond):
					}
				}
				expected := 200 // A list excludes objects whose current view grant was revoked.
				if condition == "grant_revoke" {
					_, err = f.db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='list-event-reader' AND module='events' AND action='view'`)
				} else {
					expected = 401
					for {
						var expired bool
						err = f.db.QueryRow(waitCtx, `SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1`, store.Hash(f.tokens[0])).Scan(&expired)
						if err != nil || expired {
							break
						}
						select {
						case <-waitCtx.Done():
							t.Fatal("natural fixture session expiry did not pass")
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				response := <-out
				if response.Code != expected {
					t.Fatalf("stale authority after lock wait: status=%d expected=%d body=%s", response.Code, expected, response.Body.String())
				}
				if condition == "grant_revoke" && (strings.Contains(response.Body.String(), "Authorized old") || strings.Contains(response.Body.String(), "fingerprint")) {
					t.Fatal("revoked object's captured data was returned")
				}
				if condition == "grant_revoke" {
					var body struct {
						Items  []json.RawMessage `json:"items"`
						Counts struct {
							All int `json:"all"`
						} `json:"counts"`
					}
					if json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Items) != 0 || body.Counts.All != 0 {
						t.Fatalf("revoked object remained in list/counts: %s", response.Body.String())
					}
				}
			})
		}
	}
}

type listPolicySourceFunc func(context.Context, string, map[string]string) (v3alertmanager.GroupPolicyObservation, error)

func (f listPolicySourceFunc) ObserveGroupPolicy(ctx context.Context, env string, labels map[string]string) (v3alertmanager.GroupPolicyObservation, error) {
	return f(ctx, env, labels)
}
func TestEventGroupPolicyRechecksDigestAndAuthorityAfterProviderRead(t *testing.T) {
	for _, condition := range []string{"member_change", "grant_revoke"} {
		t.Run(condition, func(t *testing.T) {
			f := newListClaimFixture(t)
			f.svc.GroupPolicyBindings = map[string]GroupPolicyBinding{"dev": {SourceIDs: map[string]bool{"fixture": true}, EnvironmentLabel: "environment", ApplicationBindings: map[string]string{"app": "app"}}}
			observations := 0
			f.svc.GroupPolicySource = listPolicySourceFunc(func(ctx context.Context, env string, labels map[string]string) (v3alertmanager.GroupPolicyObservation, error) {
				observations++
				if env != "dev" || labels["application"] != "app" {
					t.Error("provider read not bound to captured authorized scope")
				}
				var err error
				if condition == "member_change" {
					_, err = f.db.Exec(ctx, `UPDATE ops_incidents SET summary='changed during provider I/O' WHERE id='incident'`)
				} else {
					_, err = f.db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='list-event-reader' AND module='events' AND action='view'`)
				}
				return v3alertmanager.GroupPolicyObservation{}, err
			})
			id := localAlertGroupID(incidentRecord{EnvironmentID: "app-dev", ApplicationID: "app", SourceID: "fixture", Fingerprint: "fingerprint"})
			response := f.request(t.Context(), http.MethodGet, "/api/v1/alert-groups/"+id+"/policy", f.tokens[0], "")
			if observations != 1 {
				t.Fatalf("provider read count=%d", observations)
			}
			if condition == "member_change" {
				if response.Code != 409 || !strings.Contains(response.Body.String(), "alert_group_changed") {
					t.Fatalf("two-phase member drift accepted: %d %s", response.Code, response.Body.String())
				}
			} else if response.Code != 404 {
				t.Fatalf("two-phase revoked authority accepted: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func (f *listClaimFixture) createCase(ctx context.Context) *httptest.ResponseRecorder {
	return f.request(ctx, http.MethodPost, "/api/v1/incidents", f.tokens[0], `{"title":"Confirmed old case","severity":"P2","env":"dev","services":["app"],"alert_ids":["incident"]}`)
}

func caseCreated(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var result incidentCreateResult
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &result) != nil || !strings.HasPrefix(result.ID, "INC-") {
		t.Fatalf("case create failed: %d %s", response.Code, response.Body.String())
	}
	return result.ID
}

func assertIncidentSnapshot(t *testing.T, response *httptest.ResponseRecorder, id, status string) {
	t.Helper()
	var body struct {
		Items  []struct{ ID, Status string }       `json:"items"`
		Counts struct{ All, Active, Resolved int } `json:"counts"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Items) != 1 || body.Counts.All != 1 || body.Items[0].ID != id || body.Items[0].Status != status {
		t.Fatalf("incident snapshot duplicated, omitted or mixed state: %d %s", response.Code, response.Body.String())
	}
	if status == "resolved" && (body.Counts.Active != 0 || body.Counts.Resolved != 1) || status != "resolved" && (body.Counts.Active != 1 || body.Counts.Resolved != 0) {
		t.Fatalf("snapshot counts mixed states: %s", response.Body.String())
	}
}

func TestEventIncidentListDoesNotBlockCaseStatusCAS(t *testing.T) {
	f := newListClaimFixture(t)
	id := caseCreated(t, f.createCase(t.Context()))
	out, resume := f.pausedList(t, "/api/v1/incidents?status=all")
	defer resume()
	c, done := context.WithTimeout(t.Context(), 2*time.Second)
	response := f.request(c, http.MethodPost, "/api/v1/incidents/"+id+"/status", f.tokens[0], `{"status":"resolved","expected_revision":1,"request_id":"list-status-resolve"}`)
	done()
	if response.Code != 201 {
		resume()
		<-out
		t.Fatalf("paused case read blocked status CAS: %d %s", response.Code, response.Body.String())
	}
	c, done = context.WithTimeout(t.Context(), 2*time.Second)
	replay := f.request(c, http.MethodPost, "/api/v1/incidents/"+id+"/status", f.tokens[0], `{"status":"resolved","expected_revision":1,"request_id":"list-status-resolve"}`)
	done()
	if replay.Code != 200 || !strings.Contains(replay.Body.String(), `"replayed":true`) {
		resume()
		<-out
		t.Fatalf("case status replay failed during paused read: %d %s", replay.Code, replay.Body.String())
	}
	var revision, events int
	if err := f.db.QueryRow(t.Context(), `SELECT status_revision,(SELECT count(*) FROM v3_incident_case_status_events WHERE case_id=$1) FROM v3_incident_cases WHERE id=$1`, id).Scan(&revision, &events); err != nil || revision != 2 || events != 1 {
		t.Fatalf("status CAS not committed exactly once: revision=%d events=%d err=%v", revision, events, err)
	}
	resume()
	assertIncidentSnapshot(t, <-out, id, "active")
	assertIncidentSnapshot(t, f.request(t.Context(), http.MethodGet, "/api/v1/incidents?status=all", f.tokens[0], ""), id, "resolved")
}

func TestEventIncidentListAndCaseCreationDedupOrder(t *testing.T) {
	for _, order := range []string{"create_commits_first", "list_holds_link_lock_first"} {
		t.Run(order, func(t *testing.T) {
			f := newListClaimFixture(t)
			if order == "create_commits_first" {
				id := caseCreated(t, f.createCase(t.Context()))
				assertIncidentSnapshot(t, f.request(t.Context(), http.MethodGet, "/api/v1/incidents", f.tokens[0], ""), id, "active")
				return
			}
			out, resume := f.pausedList(t, "/api/v1/incidents")
			defer resume()
			c, done := context.WithTimeout(t.Context(), 6*time.Second)
			defer done()
			created := make(chan *httptest.ResponseRecorder, 1)
			go func() { created <- f.createCase(c) }()
			waitCtx, waitDone := context.WithTimeout(t.Context(), 2*time.Second)
			defer waitDone()
			for {
				var blocked bool
				err := f.db.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE l.relation='v3_incident_case_alerts'::regclass AND l.mode='RowExclusiveLock' AND NOT l.granted AND a.datname=current_database())`).Scan(&blocked)
				if err != nil {
					t.Fatal("create did not wait on list's link boundary:", err)
				}
				if blocked {
					break
				}
				select {
				case early := <-created:
					t.Fatalf("creation bypassed link-table SHARE boundary: %d %s", early.Code, early.Body.String())
				case <-waitCtx.Done():
					t.Fatal("create did not reach link lock wait")
				case <-time.After(10 * time.Millisecond):
				}
			}
			resume()
			assertIncidentSnapshot(t, <-out, "incident", "active")
			id := caseCreated(t, <-created)
			assertIncidentSnapshot(t, f.request(t.Context(), http.MethodGet, "/api/v1/incidents", f.tokens[0], ""), id, "active")
		})
	}
}
