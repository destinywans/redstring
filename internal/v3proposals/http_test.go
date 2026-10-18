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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
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
	t.Run("alert-rule-view", func(t *testing.T) {
		// D11 §11.4: an alert rule proposal carries the domain's view; a
		// failed view leaves the review readable.
		alertID := create("inv-1", "app-1", "dev")
		digest := strings.Repeat("a", 64)
		if _, err := db.Exec(ctx, `INSERT INTO v3_proposal_actions(proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,
 request_hash,binding_fingerprint,creator_id,environment_id,application_id,investigation_id,investigation_updated_at,idempotency_key,source_kind)
 VALUES($1,'prometheus.alert_rule.disable',1,'{"kind":"prometheus.alert_rule.disable","schema_version":2,"observed_rule_id":"prom-x","observed_digest":"d"}',
 $2,$2,$2,$2,$3,'dev','app-1','inv-1',now(),$1,'human_request')`, alertID, digest, reviewer.ID); err != nil {
			t.Fatal(err)
		}
		views := New(db, policy)
		failing := true
		views.AlertRuleView = func(ctx context.Context, tx pgx.Tx, id string) (*ProposalAlertRule, error) {
			if failing {
				_, err := tx.Exec(ctx, `SELECT * FROM missing_table`)
				return nil, err
			}
			return &ProposalAlertRule{Operation: "disable", Repository: ProposalAlertRuleRepository{ProjectID: "6"}}, nil
		}
		viewAPI := httpapi.New(st, nil, true)
		views.Register(viewAPI)
		if err := viewAPI.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
			policy, st.Session, nil); err != nil {
			t.Fatal(err)
		}
		w := request(viewAPI, "GET", alertID, "", "dev", reviewerToken, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"alert_rule":null`) || !strings.Contains(w.Body.String(), `"action_kind":"prometheus.alert_rule.disable"`) {
			t.Fatalf("failed view: %d %s", w.Code, w.Body.String())
		}
		failing = false
		if w := request(viewAPI, "GET", alertID, "", "dev", reviewerToken, "", ""); w.Code != 200 ||
			!strings.Contains(w.Body.String(), `"alert_rule":{"operation":"disable"`) {
			t.Fatalf("view: %d %s", w.Code, w.Body.String())
		}
		if w := request(viewAPI, "GET", mainID, "", "dev", reviewerToken, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"alert_rule":null`) {
			t.Fatalf("other proposal: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("unauthorized-review-skips-discussion-read", func(t *testing.T) {
		blocker, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `LOCK TABLE v3_proposal_comments IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		for _, operation := range []struct {
			method, suffix, body string
		}{
			{"GET", "", ""},
			{"POST", "/reject", `{"comment":"Unauthorized"}`},
		} {
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				response <- request(api, operation.method, otherAppID, operation.suffix,
					"dev", reviewerToken, operation.body, "")
			}()
			select {
			case result := <-response:
				assert(result, 403, "forbidden")
			case <-time.After(2 * time.Second):
				t.Fatalf("unauthorized %s waited on discussion table", operation.method)
			}
		}
	})
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
	// PA-74: the proposing user's display name, read at projection time;
	// null for an agent proposal or a user without a name.
	t.Run("proposer-name", func(t *testing.T) {
		if proposal.ProposedByName == nil || *proposal.ProposedByName != "Reviewer" || proposal.ProposedBy != completeContent().ProposedBy {
			t.Fatalf("proposer name: %s", view.Body.String())
		}
		content := completeContent()
		agentID, err := svc.Create(ctx, NewProposal{ApplicationID: "app-1", EnvironmentID: "dev", CreatedBy: "investigation-agent", CreatorKind: "agent", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		agent := request(api, "GET", agentID, "", "dev", reviewerToken, "", "")
		assert(agent, 200, "")
		if !strings.Contains(agent.Body.String(), `"proposed_by_name":null`) {
			t.Fatalf("agent proposal: %s", agent.Body.String())
		}
		if _, err := db.Exec(ctx, `UPDATE users SET name=' ' WHERE id=$1`, reviewer.ID); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(ctx, `UPDATE users SET name='Reviewer' WHERE id=$1`, reviewer.ID)
		nameless := request(api, "GET", mainID, "", "dev", reviewerToken, "", "")
		assert(nameless, 200, "")
		if !strings.Contains(nameless.Body.String(), `"proposed_by_name":null`) {
			t.Fatalf("user without a name: %s", nameless.Body.String())
		}
	})
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
	if proposal.Execution != nil || !strings.Contains(view.Body.String(), `"execution":null`) ||
		proposal.Decision != nil || !strings.Contains(view.Body.String(), `"decision":null`) {
		t.Fatalf("pending proposal reported an execution or a decision: %s", view.Body.String())
	}
	// PA-36: once approved, the detail shows the decision's execution and the
	// executor's state. The vote chain is out of scope here, so the two rows
	// are written directly with foreign-key triggers suspended.
	t.Run("approved-execution-projection", func(t *testing.T) {
		executionID, digest := strings.Repeat("e", 32), strings.Repeat("d", 64)
		fixture, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer fixture.Rollback(ctx)
		for _, statement := range []string{
			`SET LOCAL session_replication_role=replica`,
			`INSERT INTO v3_proposal_approval_decisions(proposal_id,execution_id,action_kind,action_digest,review_content_digest,
 approval_digest,creator_id,environment_id,application_id,risk,required_count,first_vote_id,second_vote_id,approved_at,expires_at)
 VALUES($1,$2,'alertmanager.silence.create',$3,$3,$3,$4,'dev','app-1','low',2,
 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',now(),now()+interval '1 hour')`,
			// PA-102: the two passkey votes the decision counts.
			`INSERT INTO v3_proposal_approval_votes(id,proposal_id,approver_id,challenge_id,action_kind,action_digest,
 review_content_digest,instance_id,rp_id,credential_id,credential_fingerprint,request_hash,verified_at,expires_at)
 SELECT v.id,$1,v.approver,'challenge-'||v.id,'alertmanager.silence.create',$3,$3,'test','localhost','\x01',
 '0123456789abcdef',$3,now()-v.ago,now()-v.ago+interval '15 minutes'
 FROM (VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',$4::text,interval '2 minutes'),
  ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',(SELECT id FROM users WHERE email='admin@example.invalid'),interval '1 minute')) v(id,approver,ago)
 WHERE $2::text<>''`,
		} {
			if _, err := fixture.Exec(ctx, statement, map[bool][]any{true: {mainID, executionID, digest, reviewer.ID}, false: nil}[strings.Contains(statement, "$1")]...); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		read := func() *ProposalExecution {
			t.Helper()
			w := request(api, "GET", mainID, "", "dev", reviewerToken, "", "")
			assert(w, 200, "")
			var got Proposal
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Execution == nil {
				t.Fatalf("approved detail: %s (%v)", w.Body.String(), err)
			}
			return got.Execution
		}
		if got := read(); got.ExecutionID != executionID || got.ActionKind != "alertmanager.silence.create" || got.Status != nil ||
			got.UpstreamID != nil || got.Unavailable["status"] != "not_generated" || got.Unavailable["upstream_id"] != "not_generated" {
			t.Fatalf("decision without executor record: %+v", got)
		}
		upstream := "d262c6a3-5c8c-4560-9026-e31fd977b5a2"
		if _, err := db.Exec(ctx, `SET session_replication_role=replica; INSERT INTO v3_silence_execution_claims(proposal_id,action_kind,action_digest,
 review_content_digest,approval_digest,create_operation_id,upstream_id,state,dispatch_started_at)
 VALUES('`+mainID+`','alertmanager.silence.create','`+digest+`','`+digest+`','`+digest+`','op-1','`+upstream+`','confirmed',now()); SET session_replication_role=origin`); err != nil {
			t.Fatal(err)
		}
		if got := read(); got.Status == nil || *got.Status != "confirmed" || got.UpstreamID == nil || *got.UpstreamID != upstream || len(got.Unavailable) != 0 {
			t.Fatalf("confirmed execution: %+v", got)
		}
	})
	// PA-102: the decision from the approval records. Approved with both
	// passkey votes; taken up by the executor, so its lapse is no expiry;
	// a revocation is reported with it.
	t.Run("decision", func(t *testing.T) {
		decision := func() *ProposalDecision {
			t.Helper()
			w := request(api, "GET", mainID, "", "dev", reviewerToken, "", "")
			assert(w, 200, "")
			var got Proposal
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			return got.Decision
		}
		d := decision()
		if d == nil || d.State != "approved" || d.DecidedAt == "" || len(d.Approvers) != 2 || d.RejectedBy != nil || d.Revoked != nil || d.Reason != nil ||
			d.Approvers[0].Name != "Reviewer" || d.Approvers[0].ID != reviewer.ID || d.Approvers[0].Method != "passkey" ||
			d.Approvers[1].Name == "" || d.Approvers[0].At >= d.Approvers[1].At {
			t.Fatalf("approved decision: %+v", d)
		}
		if _, err := db.Exec(ctx, `SET session_replication_role=replica; UPDATE v3_proposal_approval_decisions
 SET approved_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE proposal_id='`+mainID+`'; SET session_replication_role=origin`); err != nil {
			t.Fatal(err)
		}
		if d = decision(); d == nil || d.State != "approved" {
			t.Fatalf("executed approval reported as expired: %+v", d)
		}
		if _, err := db.Exec(ctx, `SET session_replication_role=replica; DELETE FROM v3_silence_execution_claims WHERE proposal_id='`+mainID+`'; SET session_replication_role=origin`); err != nil {
			t.Fatal(err)
		}
		if d = decision(); d == nil || d.State != "expired" || d.DecidedAt >= time.Now().UTC().Format(time.RFC3339) || len(d.Approvers) != 2 {
			t.Fatalf("lapsed approval: %+v", d)
		}
		if _, err := db.Exec(ctx, `INSERT INTO v3_proposal_approval_revocations(proposal_id,revoked_by,reason)
 VALUES($1,$2,'Approver left the team')`, mainID, reviewer.ID); err != nil {
			t.Fatal(err)
		}
		if d = decision(); d == nil || d.State != "approved" || d.Revoked == nil || d.Revoked.Name != "Reviewer" || d.Revoked.At == "" {
			t.Fatalf("revoked approval: %+v", d)
		}
	})
	// PA-205: the detail says whether the caller's own vote still counts, so
	// a first approver isn't asked to review again; a lapsed vote no longer
	// counts. Votes are written directly with foreign-key triggers suspended.
	t.Run("my-vote", func(t *testing.T) {
		id := create("inv-1", "app-1", "dev")
		myVote := func(token string) any {
			t.Helper()
			w := request(api, "GET", id, "", "dev", token, "", "")
			assert(w, 200, "")
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			vote, ok := body["my_vote"]
			if !ok {
				t.Fatalf("detail without my_vote: %s", w.Body.String())
			}
			return vote
		}
		if vote := myVote(adminToken); vote != nil {
			t.Fatalf("vote before any vote: %v", vote)
		}
		vote := func(voteID, ago, lifetime string) {
			t.Helper()
			digest := strings.Repeat("c", 64)
			if _, err := db.Exec(ctx, `SET session_replication_role=replica; INSERT INTO v3_proposal_approval_votes(id,proposal_id,approver_id,challenge_id,action_kind,action_digest,
 review_content_digest,instance_id,rp_id,credential_id,credential_fingerprint,request_hash,verified_at,expires_at)
 SELECT '`+voteID+`','`+id+`',id,'challenge-`+voteID+`','alertmanager.silence.create','`+digest+`','`+digest+`','test','localhost','\x01',
 '0123456789abcdef','`+digest+`',now()-interval '`+ago+`',now()-interval '`+ago+`'+interval '`+lifetime+`'
 FROM users WHERE email='admin@example.invalid'; SET session_replication_role=origin`); err != nil {
				t.Fatal(err)
			}
		}
		vote(strings.Repeat("1", 32), "30 minutes", "15 minutes")
		if got := myVote(adminToken); got != nil {
			t.Fatalf("lapsed vote still counted: %v", got)
		}
		vote(strings.Repeat("2", 32), "1 minute", "15 minutes")
		got, ok := myVote(adminToken).(map[string]any)
		if !ok || got["verified_at"] == "" || got["expires_at"] == "" || got["verified_at"] == nil || got["expires_at"] == nil {
			t.Fatalf("current vote: %v", got)
		}
		if other := myVote(reviewerToken); other != nil {
			t.Fatalf("another person's vote reported as the caller's: %v", other)
		}
	})
	// PA-205: an alert rule execution's failure code reaches the review page,
	// which tells a load timeout (needs attention) from an ordinary wait.
	t.Run("alert-rule-failure-code", func(t *testing.T) {
		id := create("inv-1", "app-1", "dev")
		executionID, digest := strings.Repeat("f", 32), strings.Repeat("d", 64)
		if _, err := db.Exec(ctx, `SET session_replication_role=replica;
 INSERT INTO v3_proposal_approval_decisions(proposal_id,execution_id,action_kind,action_digest,review_content_digest,
 approval_digest,creator_id,environment_id,application_id,risk,required_count,first_vote_id,second_vote_id,approved_at,expires_at)
 VALUES('`+id+`','`+executionID+`','prometheus.alert_rule.enable','`+digest+`','`+digest+`','`+digest+`','`+reviewer.ID+`','dev','app-1','low',2,
 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',now(),now()+interval '1 hour');
 INSERT INTO v3_alert_rule_enable_executions(proposal_id,execution_id,action_digest,approval_id,approval_digest,approver_id,state,failure_code)
 VALUES('`+id+`','`+executionID+`','`+digest+`','`+executionID+`','`+digest+`','`+reviewer.ID+`','awaiting_provider','load_timeout');
 SET session_replication_role=origin`); err != nil {
			t.Fatal(err)
		}
		execution := func() map[string]any {
			t.Helper()
			w := request(api, "GET", id, "", "dev", reviewerToken, "", "")
			assert(w, 200, "")
			var body struct {
				Execution map[string]any `json:"execution"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Execution == nil {
				t.Fatalf("approved detail: %s (%v)", w.Body.String(), err)
			}
			return body.Execution
		}
		if got := execution(); got["status"] != "awaiting_provider" || got["failure_code"] != "load_timeout" {
			t.Fatalf("load timeout: %v", got)
		}
		if _, err := db.Exec(ctx, `SET session_replication_role=replica; UPDATE v3_alert_rule_enable_executions SET state='enabled',failure_code=''
 WHERE proposal_id='`+id+`'; SET session_replication_role=origin`); err != nil {
			t.Fatal(err)
		}
		if got := execution(); got["status"] != "enabled" {
			t.Fatalf("enabled: %v", got)
		} else if code, ok := got["failure_code"]; !ok || code != nil {
			t.Fatalf("enabled execution with a failure code: %v", got)
		}
	})
	t.Run("machine-reject-skips-discussion-read", func(t *testing.T) {
		blocker, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `LOCK TABLE v3_proposal_comments IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response <- request(api, "POST", mainID, "/reject", "dev", machineToken,
				`{"comment":"Machine may not reject"}`, "")
		}()
		select {
		case result := <-response:
			assert(result, 403, "forbidden")
		case <-time.After(2 * time.Second):
			t.Fatal("machine rejection waited on discussion table")
		}
	})
	assert(request(api, "POST", mainID, "/reject", "dev", machineToken, `{"comment":"Unsafe"}`, ""), 403, "forbidden")
	rejected := request(api, "POST", mainID, "/reject", "dev", reviewerToken, `{"comment":"Unsafe"}`, "")
	assert(rejected, 200, "")
	if err := json.Unmarshal(rejected.Body.Bytes(), &proposal); err != nil || proposal.Status != "rejected" || len(proposal.Discussion) != 1 {
		t.Fatalf("incorrect rejection DTO: %s (%v)", rejected.Body.String(), err)
	}
	// PA-102: a rejection is the decision, whatever was approved before.
	if d := proposal.Decision; d == nil || d.State != "rejected" || d.RejectedBy == nil || d.RejectedBy.ID != reviewer.ID ||
		d.RejectedBy.Name != "Reviewer" || d.RejectedBy.At != d.DecidedAt || len(d.Approvers) != 0 || d.Reason == nil || *d.Reason != "Unsafe" {
		t.Fatalf("rejection decision: %+v", proposal.Decision)
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

	// PA-57: a human holding the approve grant for the proposal's scope may
	// reject without proposals.manage, with the same audit row. An approve
	// grant for another application, or held by a machine, is not enough.
	t.Run("approver-rejects", func(t *testing.T) {
		if _, err := db.Exec(ctx, `INSERT INTO rbac_roles(id,name) VALUES('proposal-approver','Proposal Approver'),('other-approver','Other Approver');
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES
 ('proposal-approver','proposals','approve','application','app-1','passkey'),
 ('other-approver','proposals','approve','application','app-2','passkey')`); err != nil {
			t.Fatal(err)
		}
		tokens := map[string]string{}
		ids := map[string]string{}
		for _, who := range []struct{ name, role string }{{"approver", "proposal-approver"}, {"other-approver", "other-approver"}, {"machine-approver", "proposal-approver"}} {
			user, err := st.CreateUser(ctx, core.User{Email: who.name + "@example.invalid", Name: who.name, Role: "viewer"}, "isolated-"+who.name+"-password")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, user.ID, who.role); err != nil {
				t.Fatal(err)
			}
			if who.name == "machine-approver" {
				if _, err := db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, user.ID); err != nil {
					t.Fatal(err)
				}
			}
			ids[who.name], tokens[who.name] = user.ID, login(who.name+"@example.invalid", "isolated-"+who.name+"-password")
		}
		id := create("inv-1", "app-1", "dev")
		assert(request(api, "POST", id, "/reject", "dev", tokens["other-approver"], `{"comment":"Not mine"}`, ""), 403, "forbidden")
		assert(request(api, "POST", id, "/reject", "dev", tokens["machine-approver"], `{"comment":"Machine"}`, ""), 403, "forbidden")
		rejected := request(api, "POST", id, "/reject", "dev", tokens["approver"], `{"comment":"Unsafe window"}`, "")
		assert(rejected, 200, "")
		var out Proposal
		if err := json.Unmarshal(rejected.Body.Bytes(), &out); err != nil || out.Status != "rejected" {
			t.Fatalf("approver rejection: %s (%v)", rejected.Body.String(), err)
		}
		var actor, rejectedBy string
		if err := db.QueryRow(ctx, `SELECT a.actor_id,p.rejected_by FROM management_audit a JOIN v3_proposals p ON p.id=a.target
 WHERE a.action='v3.proposal.rejected' AND a.target=$1`, id).Scan(&actor, &rejectedBy); err != nil || actor != ids["approver"] || rejectedBy != ids["approver"] {
			t.Fatalf("approver rejection audit: actor=%s rejected_by=%s err=%v", actor, rejectedBy, err)
		}
	})

	// PA-173: the rejection reason reaches the proposer through the detail
	// decision, trimmed; a rejection without one reports null.
	t.Run("rejection-reason", func(t *testing.T) {
		reason := func(id string) (any, bool) {
			t.Helper()
			w := request(api, "GET", id, "", "dev", reviewerToken, "", "")
			assert(w, 200, "")
			var got struct {
				Decision map[string]any `json:"decision"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Decision == nil || got.Decision["state"] != "rejected" {
				t.Fatalf("rejected detail: %s (%v)", w.Body.String(), err)
			}
			v, ok := got.Decision["reason"]
			return v, ok
		}
		given := create("inv-1", "app-1", "dev")
		assert(request(api, "POST", given, "/reject", "dev", reviewerToken, `{"comment":"  Rollback window overlaps the release freeze \n"}`, ""), 200, "")
		if v, ok := reason(given); !ok || v != "Rollback window overlaps the release freeze" {
			t.Fatalf("decision.reason = %#v (present %v), want the given reason", v, ok)
		}
		blank := create("inv-1", "app-1", "dev")
		assert(request(api, "POST", blank, "/reject", "dev", reviewerToken, `{"comment":"   "}`, ""), 200, "")
		if v, ok := reason(blank); !ok || v != nil {
			t.Fatalf("decision.reason = %#v (present %v), want null", v, ok)
		}
		// A stored reason of only whitespace, as older rows may hold, is no
		// reason either.
		if _, err := db.Exec(ctx, `UPDATE v3_proposals SET rejection_reason=E' \n\t ' WHERE id=$1`, blank); err != nil {
			t.Fatal(err)
		}
		if v, ok := reason(blank); !ok || v != nil {
			t.Fatalf("whitespace decision.reason = %#v (present %v), want null", v, ok)
		}
	})

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
					blockedReviewSQL := "LOCK TABLE rbac_user_roles, rbac_department_roles, v3_role_grants IN SHARE MODE"
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
						blockAfter := 1
						if operation.action != "change" {
							blockAfter = 2 // GET and Reject also perform tentative admission before discussion reads.
						}
						barrier := &reviewAuthorizationBarrier{V3Policy: policy, Action: operation.action, BlockAfter: blockAfter,
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
		barrier := &reviewAuthorizationBarrier{V3Policy: policy, Action: "view", BlockAfter: 2,
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
	// Role deletion and association editing lock the role first, then delete
	// its assignment. Exercise those real writers against the review table
	// locks in both commit orders; a trigger provides a deterministic writer
	// barrier without changing production code.
	if _, err := db.Exec(ctx, `CREATE FUNCTION pause_proposal_role_assignment_delete() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
  IF OLD.role_id LIKE 'proposal-cycle-%' THEN
   PERFORM pg_advisory_xact_lock(hashtextextended(current_schema(), 11));
  END IF;
  RETURN OLD;
 END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TRIGGER pause_proposal_role_assignment_delete
 BEFORE DELETE ON rbac_user_roles FOR EACH ROW EXECUTE FUNCTION pause_proposal_role_assignment_delete()`); err != nil {
		t.Fatal(err)
	}
	adminUser, err := st.Session(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"delete-role", "remove-association"} {
		for _, winner := range []string{"mutation", "review"} {
			t.Run(change+"/"+winner, func(t *testing.T) {
				roleID := "proposal-cycle-" + store.ID()
				for _, statement := range []string{
					`INSERT INTO rbac_roles(id,name) VALUES($1,$1)`,
					`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES($1,'proposals','view','team','proposal-ops')`,
				} {
					if _, err := db.Exec(ctx, statement, roleID); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, teamReviewer.ID, roleID); err != nil {
					t.Fatal(err)
				}
				id := create("inv-2", "app-2", "dev")
				mutate := func() error {
					writer := &rbac.Service{Store: st}
					actorCtx := store.WithPrincipal(ctx, adminUser)
					if change == "delete-role" {
						return writer.Delete(actorCtx, "roles", roleID, 1)
					}
					_, err := writer.SaveAssociations(actorCtx, roleID, rbac.Associations{
						Revision: 1, Users: rbac.Delta{Remove: []string{teamReviewer.ID}},
					})
					return err
				}
				if winner == "mutation" {
					barrier, err := db.Acquire(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer barrier.Release()
					if _, err := barrier.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended(current_schema(), 11))`); err != nil {
						t.Fatal(err)
					}
					held := true
					defer func() {
						if held {
							_, _ = barrier.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended(current_schema(), 11))`)
						}
					}()
					mutationDone := make(chan error, 1)
					go func() { mutationDone <- mutate() }()
					waitReviewLock(t, db, "DELETE FROM rbac_user_roles")
					response := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						response <- request(api, "GET", id, "", "dev", teamReviewerToken, "", "")
					}()
					waitReviewLock(t, db, "LOCK TABLE rbac_user_roles, rbac_department_roles, v3_role_grants IN SHARE MODE")
					if _, err := barrier.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended(current_schema(), 11))`); err != nil {
						t.Fatal(err)
					}
					held = false
					if err := <-mutationDone; err != nil {
						t.Fatal(err)
					}
					assert(<-response, 403, "forbidden")
				} else {
					access := &reviewAuthorizationBarrier{V3Policy: policy, Action: "view", BlockAfter: 2,
						Ready: make(chan struct{}), Release: make(chan struct{})}
					reviewAPI := serve(access)
					response := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						response <- request(reviewAPI, "GET", id, "", "dev", teamReviewerToken, "", "")
					}()
					select {
					case <-access.Ready:
					case <-time.After(5 * time.Second):
						t.Fatal("role reviewer did not reach authorization barrier")
					}
					mutationDone := make(chan error, 1)
					go func() { mutationDone <- mutate() }()
					waitReviewLock(t, db, "DELETE FROM rbac_user_roles")
					close(access.Release)
					assert(<-response, 200, "")
					if err := <-mutationDone; err != nil {
						t.Fatal(err)
					}
					assert(request(api, "GET", id, "", "dev", teamReviewerToken, "", ""), 403, "forbidden")
				}
			})
		}
	}
	for _, winner := range []string{"mutation", "review"} {
		t.Run("grant-insert/"+winner, func(t *testing.T) {
			if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='proposal-team-reviewer'
 AND module='proposals' AND action='change' AND scope_kind='team' AND scope_id='proposal-ops'`); err != nil {
				t.Fatal(err)
			}
			id := create("inv-2", "app-2", "dev")
			insert := func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('proposal-team-reviewer','proposals','change','team','proposal-ops')`)
				return err
			}
			if winner == "mutation" {
				writer, err := db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Rollback(ctx)
				if err := insert(writer); err != nil {
					t.Fatal(err)
				}
				response := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					response <- request(api, "POST", id, "/comments", "dev", teamReviewerToken,
						`{"text":"Grant committed first"}`, "")
				}()
				waitReviewLock(t, db, "LOCK TABLE rbac_user_roles, rbac_department_roles, v3_role_grants IN SHARE MODE")
				if err := writer.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				assert(<-response, 201, "")
			} else {
				access := &reviewAuthorizationBarrier{V3Policy: policy, Action: "change", BlockDenied: true, BlockAfter: 1,
					Ready: make(chan struct{}), Release: make(chan struct{})}
				reviewAPI := serve(access)
				response := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					response <- request(reviewAPI, "POST", id, "/comments", "dev", teamReviewerToken,
						`{"text":"Grant committed later"}`, "")
				}()
				select {
				case <-access.Ready:
				case <-time.After(5 * time.Second):
					t.Fatal("denied review did not reach authorization barrier")
				}
				mutationDone := make(chan error, 1)
				go func() {
					writer, err := db.Begin(ctx)
					if err == nil {
						err = insert(writer)
						if err == nil {
							err = writer.Commit(ctx)
						} else {
							writer.Rollback(ctx)
						}
					}
					mutationDone <- err
				}()
				waitReviewLock(t, db, "INSERT INTO v3_role_grants")
				close(access.Release)
				assert(<-response, 403, "forbidden")
				if err := <-mutationDone; err != nil {
					t.Fatal(err)
				}
				assert(request(api, "POST", id, "/comments", "dev", teamReviewerToken,
					`{"text":"Now authorized"}`, ""), 201, "")
			}
		})
	}
}

type reviewAuthorizationBarrier struct {
	*rbac.V3Policy
	Action      string
	BlockDenied bool
	BlockAfter  int
	Calls       int
	Ready       chan struct{}
	Release     chan struct{}
	Once        sync.Once
}

func (a *reviewAuthorizationBarrier) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, d rbac.V3Decision) error {
	err := a.V3Policy.AuthorizeWithTx(ctx, tx, user, d)
	if (err == nil || a.BlockDenied) && d.Module == "proposals" && d.Action == a.Action && d.TeamID == "proposal-ops" {
		a.Calls++
		if a.Calls == a.BlockAfter {
			a.Once.Do(func() {
				close(a.Ready)
				<-a.Release
			})
		}
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
