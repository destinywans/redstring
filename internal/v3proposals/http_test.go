package v3proposals

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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestProposalHTTPDetailCommentRejectAndScope(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_proposals_http_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES('proposal-reviewer','Proposal Reviewer')`,
		`INSERT INTO rbac_roles(id,name) VALUES('proposal-team-reviewer','Proposal Team Reviewer')`,
		`INSERT INTO teams(id,name) VALUES('proposal-ops','Proposal Operations')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('proposal-reviewer','proposals','view','application','app-1'),
 ('proposal-reviewer','proposals','change','application','app-1'),
 ('proposal-reviewer','proposals','manage','application','app-1'),
 ('proposal-team-reviewer','proposals','view','team','proposal-ops'),
 ('proposal-team-reviewer','proposals','change','team','proposal-ops'),
 ('proposal-team-reviewer','proposals','manage','team','proposal-ops')`,
		`INSERT INTO applications(id,name,key,environment) VALUES
 ('app-1','App One','app-one','dev'),('app-2','App Two','app-two','dev')`,
		`UPDATE applications SET owner_team_id='proposal-ops' WHERE id='app-2'`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('app-1-dev','app-1','dev','Dev'),('app-1-test','app-1','test','Test'),
 ('app-2-dev','app-2','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-1','dev'),('app-1','test'),('app-2','dev')`,
		`INSERT INTO investigations(id,application_id,environment_id,title) VALUES
 ('inv-1','app-1','app-1-dev','First'),('inv-2','app-2','app-2-dev','Other app'),
 ('inv-3','app-1','app-1-test','Other environment')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	reviewer, err := st.CreateUser(ctx, core.User{Email: "reviewer@example.invalid", Name: "Reviewer", Role: "viewer"}, "isolated-reviewer-password")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := st.CreateUser(ctx, core.User{Email: "machine@example.invalid", Name: "Machine", Role: "viewer"}, "isolated-machine-password")
	if err != nil {
		t.Fatal(err)
	}
	teamReviewer, err := st.CreateUser(ctx, core.User{Email: "team-reviewer@example.invalid", Name: "Team Reviewer", Role: "viewer"}, "isolated-team-reviewer-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'proposal-reviewer'),($2,'proposal-reviewer')`, reviewer.ID, machine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'proposal-team-reviewer')`, teamReviewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, machine.ID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(db, policy)
	create := func(inv, app, env string) string {
		t.Helper()
		content := completeContent()
		content.InvestigationID = inv
		id, err := svc.Create(ctx, NewProposal{ApplicationID: app, EnvironmentID: env, CreatedBy: reviewer.ID, CreatorKind: "user", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mainID := create("inv-1", "app-1", "dev")
	otherAppID := create("inv-2", "app-2", "dev")
	otherEnvID := create("inv-3", "app-1", "test")
	login := func(email, password string) string {
		t.Helper()
		_, token, err := st.Login(ctx, email, password)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	reviewerToken := login("reviewer@example.invalid", "isolated-reviewer-password")
	machineToken := login("machine@example.invalid", "isolated-machine-password")
	teamReviewerToken := login("team-reviewer@example.invalid", "isolated-team-reviewer-password")
	adminToken := login("admin@example.invalid", "isolated-admin-password")
	serve := func(access Access) *httpapi.Server {
		t.Helper()
		api := httpapi.New(st, nil, true)
		New(db, access).Register(api)
		if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
			policy, st.Session, nil); err != nil {
			t.Fatal(err)
		}
		return api
	}
	api := serve(policy)
	request := func(api *httpapi.Server, method, id, suffix, env, token, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/proposals/"+id+suffix, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("status=%d body=%s; want status=%d code=%q", w.Code, w.Body.String(), status, code)
		}
	}
	assert(request(api, "GET", mainID, "", "dev", "", "", ""), 401, "")
	assert(request(api, "GET", otherAppID, "", "dev", reviewerToken, "", ""), 403, "forbidden")
	assert(request(api, "GET", otherEnvID, "", "all", reviewerToken, "", ""), 404, "not_found")
	assert(request(api, "GET", otherEnvID, "", "test", reviewerToken, "", ""), 403, "forbidden")
	assert(request(api, "POST", mainID, "/comments", "all", reviewerToken, `{"text":"Review evidence"}`, "comment-1"), 409, "env_view_only")
	assert(request(api, "POST", mainID, "/reject", "all", reviewerToken, `{"comment":"Unsafe"}`, ""), 409, "env_view_only")
	view := request(api, "GET", mainID, "", "dev", reviewerToken, "", "")
	assert(view, 200, "")
	var proposal Proposal
	if err := json.Unmarshal(view.Body.Bytes(), &proposal); err != nil || proposal.ID != mainID || proposal.Status != "pending" || proposal.Title != completeContent().Title || len(proposal.Discussion) != 0 {
		t.Fatalf("incorrect persisted detail: %s (%v)", view.Body.String(), err)
	}
	comment := request(api, "POST", mainID, "/comments", "dev", reviewerToken, `{"text":" Review evidence "}`, "comment-1")
	assert(comment, 201, "")
	var posted CommentItem
	if err := json.Unmarshal(comment.Body.Bytes(), &posted); err != nil || posted.Author != "Reviewer" || posted.Text != "Review evidence" || posted.Time == "" {
		t.Fatalf("incorrect comment: %s (%v)", comment.Body.String(), err)
	}
	assert(request(api, "POST", mainID, "/comments", "dev", reviewerToken, `{"text":"Review evidence"}`, "comment-1"), 200, "")
	assert(request(api, "POST", mainID, "/comments", "dev", reviewerToken, `{"text":"Different"}`, "comment-1"), 409, "idempotency_conflict")
	view = request(api, "GET", mainID, "", "all", adminToken, "", "")
	assert(view, 200, "")
	if err := json.Unmarshal(view.Body.Bytes(), &proposal); err != nil || len(proposal.Discussion) != 1 || proposal.Discussion[0].Text != "Review evidence" {
		t.Fatalf("comment not persisted in detail: %s (%v)", view.Body.String(), err)
	}
	assert(request(api, "POST", mainID, "/reject", "dev", machineToken, `{"comment":"Unsafe"}`, ""), 403, "forbidden")
	rejected := request(api, "POST", mainID, "/reject", "dev", reviewerToken, `{"comment":"Unsafe"}`, "")
	assert(rejected, 200, "")
	if err := json.Unmarshal(rejected.Body.Bytes(), &proposal); err != nil || proposal.Status != "rejected" || len(proposal.Discussion) != 1 {
		t.Fatalf("incorrect rejection DTO: %s (%v)", rejected.Body.String(), err)
	}
	assert(request(api, "POST", mainID, "/reject", "dev", reviewerToken, `{"comment":"Unsafe"}`, ""), 200, "")
	assert(request(api, "POST", mainID, "/reject", "dev", adminToken, `{"comment":"Unsafe"}`, ""), 409, "proposal_closed")
	assert(request(api, "POST", mainID, "/comments", "dev", reviewerToken, `{"text":"After close"}`, "comment-2"), 409, "proposal_closed")
	var comments, commentAudit, rejectAudit int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_proposal_comments WHERE proposal_id=$1`, mainID).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='v3.proposal.comment_added'),count(*) FILTER (WHERE action='v3.proposal.rejected')
 FROM management_audit WHERE application_id='app-1' AND target=$1`, mainID).Scan(&commentAudit, &rejectAudit); err != nil {
		t.Fatal(err)
	}
	if comments != 1 || commentAudit != 1 || rejectAudit != 1 {
		t.Fatalf("duplicate/invalid transitions changed durable records: comments=%d comment-audit=%d reject-audit=%d", comments, commentAudit, rejectAudit)
	}
	// This reviewer has only a team-scoped proposal grant. Resolve the team
	// from the persisted application for reads and both write decisions.
	assert(request(api, "GET", mainID, "", "dev", teamReviewerToken, "", ""), 403, "forbidden")
	assert(request(api, "GET", otherAppID, "", "dev", teamReviewerToken, "", ""), 200, "")
	assert(request(api, "POST", otherAppID, "/comments", "dev", teamReviewerToken, `{"text":"Team review"}`, "team-comment"), 201, "")
	assert(request(api, "POST", otherAppID, "/reject", "dev", teamReviewerToken, `{"comment":"Team declined"}`, ""), 200, "")

	// The request and a permission/owner mutation each win once. Confirm the
	// loser waits on a specific row lock before allowing the winner to commit.
	for _, operation := range []struct {
		name, method, suffix, body, action string
		accepted                           int
	}{
		{"detail", "GET", "", "", "view", 200},
		{"comment", "POST", "/comments", `{"text":"Concurrent team review"}`, "change", 201},
		{"reject", "POST", "/reject", `{"comment":"Concurrent decline"}`, "manage", 200},
	} {
		for _, mutation := range []string{"grant", "owner"} {
			for _, winner := range []string{"mutation", "review"} {
				t.Run(operation.name+"/"+mutation+"/"+winner, func(t *testing.T) {
					if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='proposal-ops' WHERE id='app-2'`); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('proposal-team-reviewer','proposals',$1,'team','proposal-ops') ON CONFLICT DO NOTHING`, operation.action); err != nil {
						t.Fatal(err)
					}
					id := create("inv-2", "app-2", "dev")
					mutationSQL := `DELETE FROM v3_role_grants WHERE role_id='proposal-team-reviewer'
 AND module='proposals' AND action=$1 AND scope_kind='team' AND scope_id='proposal-ops'`
					mutationArg := operation.action
					blockedReviewSQL := "SELECT role_id FROM v3_role_grants WHERE role_id=ANY"
					blockedMutationSQL := "DELETE FROM v3_role_grants WHERE role_id='proposal-team-reviewer'"
					if mutation == "owner" {
						mutationSQL = `UPDATE applications SET owner_team_id=NULL WHERE id='app-2'`
						mutationArg = ""
						blockedReviewSQL = "SELECT COALESCE(owner_team_id,'') FROM applications"
						blockedMutationSQL = "UPDATE applications SET owner_team_id=NULL WHERE id='app-2'"
					}
					mutate := func(tx pgx.Tx) error {
						if mutationArg == "" {
							_, err := tx.Exec(ctx, mutationSQL)
							return err
						}
						_, err := tx.Exec(ctx, mutationSQL, mutationArg)
						return err
					}
					if winner == "mutation" {
						writer, err := db.Begin(ctx)
						if err != nil {
							t.Fatal(err)
						}
						defer writer.Rollback(ctx)
						if err := mutate(writer); err != nil {
							t.Fatal(err)
						}
						response := make(chan *httptest.ResponseRecorder, 1)
						go func() {
							response <- request(api, operation.method, id, operation.suffix, "dev", teamReviewerToken, operation.body, "")
						}()
						waitReviewLock(t, db, blockedReviewSQL)
						if err := writer.Commit(ctx); err != nil {
							t.Fatal(err)
						}
						assert(<-response, 403, "forbidden")
						var status string
						var count int
						if err := db.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM v3_proposal_comments WHERE proposal_id=$1)
 FROM v3_proposals WHERE id=$1`, id).Scan(&status, &count); err != nil || status != "pending" || count != 0 {
							t.Fatalf("revoked review changed record: status=%s comments=%d err=%v", status, count, err)
						}
					} else {
						barrier := &reviewAuthorizationBarrier{V3Policy: policy, Action: operation.action,
							Ready: make(chan struct{}), Release: make(chan struct{})}
						reviewAPI := serve(barrier)
						response := make(chan *httptest.ResponseRecorder, 1)
						go func() {
							response <- request(reviewAPI, operation.method, id, operation.suffix, "dev", teamReviewerToken, operation.body, "")
						}()
						select {
						case <-barrier.Ready:
						case <-time.After(5 * time.Second):
							t.Fatal("review did not reach authorized transaction barrier")
						}
						mutationDone := make(chan error, 1)
						go func() {
							writer, err := db.Begin(ctx)
							if err == nil {
								err = mutate(writer)
								if err == nil {
									err = writer.Commit(ctx)
								} else {
									writer.Rollback(ctx)
								}
							}
							mutationDone <- err
						}()
						waitReviewLock(t, db, blockedMutationSQL)
						close(barrier.Release)
						assert(<-response, operation.accepted, "")
						if err := <-mutationDone; err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
	t.Run("review/update-member/matrix-lock-order", func(t *testing.T) {
		if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='proposal-ops' WHERE id='app-2'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('proposal-team-reviewer','proposals','view','team','proposal-ops') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatal(err)
		}
		id := create("inv-2", "app-2", "dev")
		barrier := &reviewAuthorizationBarrier{V3Policy: policy, Action: "view",
			Ready: make(chan struct{}), Release: make(chan struct{})}
		reviewAPI := serve(barrier)
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response <- request(reviewAPI, "GET", id, "", "dev", teamReviewerToken, "", "")
		}()
		select {
		case <-barrier.Ready:
		case <-time.After(5 * time.Second):
			t.Fatal("review did not reach authorized transaction barrier")
		}
		newName := "Team Reviewer Updated"
		memberDone := make(chan error, 1)
		go func() {
			_, err := st.UpdateMember(ctx, teamReviewer.ID, store.MemberUpdate{
				AccountUpdate: store.AccountUpdate{Name: &newName},
			})
			memberDone <- err
		}()
		waitReviewLock(t, db, "FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE")
		matrixDone := make(chan error, 1)
		go func() {
			writer, err := db.Begin(ctx)
			if err == nil {
				for _, key := range []int64{752018992, 752018995, 752018998} {
					_, err = writer.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key)
					if err != nil {
						break
					}
				}
				if err == nil {
					_, err = writer.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='proposal-team-reviewer'
 AND module='proposals' AND action='view' AND scope_kind='team' AND scope_id='proposal-ops'`)
				}
				if err == nil {
					err = writer.Commit(ctx)
				} else {
					writer.Rollback(ctx)
				}
			}
			matrixDone <- err
		}()
		waitReviewLock(t, db, "SELECT pg_advisory_xact_lock($1)")
		close(barrier.Release)
		assert(<-response, 200, "")
		if err := <-memberDone; err != nil {
			t.Fatal(err)
		}
		if err := <-matrixDone; err != nil {
			t.Fatal(err)
		}
		assert(request(api, "GET", id, "", "dev", teamReviewerToken, "", ""), 403, "forbidden")
	})
}

type reviewAuthorizationBarrier struct {
	*rbac.V3Policy
	Action  string
	Ready   chan struct{}
	Release chan struct{}
	Once    sync.Once
}

func (a *reviewAuthorizationBarrier) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, d rbac.V3Decision) error {
	err := a.V3Policy.AuthorizeWithTx(ctx, tx, user, d)
	if err == nil && d.Module == "proposals" && d.Action == a.Action && d.TeamID == "proposal-ops" {
		a.Once.Do(func() {
			close(a.Ready)
			<-a.Release
		})
	}
	return err
}

func waitReviewLock(t *testing.T, db *pgxpool.Pool, queryPart string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%'||$1||'%')`, queryPart).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("query did not wait on expected row lock: %s", queryPart)
}
