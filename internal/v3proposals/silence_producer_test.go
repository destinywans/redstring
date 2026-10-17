package v3proposals

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

func TestSilenceReviewContentQuotesMatcherAndComment(t *testing.T) {
	action := SilenceActionSnapshot{Silence: v3alertmanager.ProposedSilenceCreate{
		ApplicationLabel: "checkout", Comment: "reason\nInjected: approve now", DurationSeconds: 3600,
		Matchers: []v3alertmanager.Matcher{{Name: "service", Value: "api.*", IsRegex: true, IsEqual: false}},
	}}
	for _, tc := range []struct {
		content           Content
		duration, comment string
	}{
		{silenceReviewContent(action, false), "时长：1 小时", `备注："reason\nInjected: approve now"`},
		{silenceReviewContent(action, true), "Duration: 1 hour", `Comment: "reason\nInjected: approve now"`},
		{legacySilenceReviewContent(action), "Duration: 3600 seconds", `Comment: "reason\nInjected: approve now"`},
	} {
		lines := tc.content.Diff.Lines
		if len(lines) != 3 || lines[0] != `service !~ "api.*"` || lines[1] != tc.duration ||
			lines[2] != tc.comment || strings.Contains(lines[2], "\n") {
			t.Fatalf("unsafe or misleading review lines: %#v", lines)
		}
	}
}

type silenceProposalFixture struct {
	db       *pgxpool.Pool
	svc      *Service
	ledger   *v3alertmanager.DatabaseSilenceLedger
	client   *v3alertmanager.Client
	actor    core.User
	requests atomic.Int32
}

func newSilenceProposalFixture(t *testing.T) *silenceProposalFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated random schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_silence_proposal_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	f := &silenceProposalFixture{}
	t.Cleanup(func() {
		if f.db != nil {
			f.db.Close()
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
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	cfg.MaxConns = 5
	f.db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: f.db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO applications(id,key,name,environment) VALUES('app-1','checkout','Checkout','test'),('app-2','other','Other','test')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-1-test','app-1','test','Test'),('app-2-test','app-2','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-1','test'),('app-2','test')`,
		`INSERT INTO investigations(id,application_id,environment_id,title) VALUES('inv-1','app-1','app-1-test','First'),('inv-closed','app-1','app-1-test','Closed')`,
		`UPDATE investigations SET status='closed' WHERE id='inv-closed'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
		 ('viewer','environment','view','environment','test'),
		 ('viewer','silences','manage','application','app-1'),
		 ('viewer','proposals','change','application','app-1')`,
	} {
		if _, err := f.db.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	f.actor, err = st.CreateUser(ctx, core.User{Email: "writer@example.invalid", Name: "Writer", Role: "viewer"}, "isolated-writer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	f.svc = New(f.db, policy)
	f.ledger = v3alertmanager.NewDatabaseSilenceLedger(f.db, policy,
		map[string]string{"test": "environment"}, map[string]map[string]string{"test": {"checkout": "app-1"}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		http.Error(w, "proposal must not contact Alertmanager", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	f.client, err = v3alertmanager.New(map[string]v3alertmanager.Endpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func silenceProposalInput(key string) SilenceProposalRequest {
	return SilenceProposalRequest{InvestigationID: "inv-1", Silence: v3alertmanager.SilenceCreateInput{
		EnvironmentID: "test", IdempotencyKey: key, Comment: "Investigate downstream errors", Duration: time.Hour,
		Matchers: []v3alertmanager.Matcher{{Name: "application", Value: "checkout", IsEqual: true},
			{Name: "alertname", Value: "DownstreamTimeout", IsEqual: true}},
	}}
}

func (f *silenceProposalFixture) counts(t *testing.T) (int, int, int) {
	t.Helper()
	var proposals, actions, reservations int
	for _, item := range []struct {
		sql string
		out *int
	}{
		{`SELECT count(*) FROM v3_proposals`, &proposals},
		{`SELECT count(*) FROM v3_proposal_actions`, &actions},
		{`SELECT count(*) FROM v3_silence_create_operations`, &reservations},
	} {
		if err := f.db.QueryRow(t.Context(), item.sql).Scan(item.out); err != nil {
			t.Fatal(err)
		}
	}
	return proposals, actions, reservations
}

func TestCreateSilenceProposalReplayAndImmutableReview(t *testing.T) {
	f := newSilenceProposalFixture(t)
	ctx := t.Context()
	in := silenceProposalInput("proposal-key-001")
	first, err := f.svc.CreateSilenceProposal(ctx, f.actor, in, f.ledger, f.client)
	if err != nil || first.ProposalID == "" || first.ActionDigest == "" || first.Status != "pending" {
		t.Fatalf("create=%+v err=%v", first, err)
	}
	var wg sync.WaitGroup
	results := make([]SilenceProposalResult, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = f.svc.CreateSilenceProposal(ctx, f.actor, in, f.ledger, f.client)
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != first {
			t.Fatalf("replay %d=%+v err=%v", i, results[i], errs[i])
		}
	}
	changed := in
	changed.Silence.Comment = "Different request"
	if _, err := f.svc.CreateSilenceProposal(ctx, f.actor, changed, f.ledger, f.client); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed request err=%v", err)
	}
	driftedClient, err := v3alertmanager.New(map[string]v3alertmanager.Endpoint{
		"test": {URL: "http://127.0.0.1:9", AllowHTTP: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateSilenceProposal(ctx, f.actor, in, f.ledger, driftedClient); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed server binding err=%v", err)
	}
	if p, a, r := f.counts(t); p != 1 || a != 1 || r != 0 {
		t.Fatalf("rows proposals=%d actions=%d reservations=%d", p, a, r)
	}
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("Alertmanager requests=%d", n)
	}
	var audit int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.proposal.silence_create_requested' AND target=$1`, first.ProposalID).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("audit=%d err=%v", audit, err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	action, loaded, err := LoadSilenceActionWithTx(ctx, tx, first.ProposalID)
	if err != nil || loaded != first || action.Silence.ApplicationID != "app-1" || action.InvestigationID != "inv-1" || action.Silence.ActorID != f.actor.ID {
		t.Fatalf("load=%+v result=%+v err=%v", action, loaded, err)
	}
	if err := f.ledger.RevalidateCreateProposalWithTx(ctx, tx, f.actor, action.Silence, f.client); err != nil {
		t.Fatalf("unchanged action rejected: %v", err)
	}
	if err := f.ledger.RevalidateCreateProposalWithTx(ctx, tx, f.actor, action.Silence, driftedClient); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed binding revalidation err=%v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE v3_proposal_actions SET action=jsonb_set(action,'{silence,comment}','"tampered"') WHERE proposal_id=$1`,
		`DELETE FROM v3_proposal_actions WHERE proposal_id=$1`,
		`UPDATE v3_proposals SET content=jsonb_set(content,'{title}','"tampered"') WHERE id=$1`,
	} {
		if _, err := f.db.Exec(ctx, stmt, first.ProposalID); err == nil {
			t.Fatalf("immutable row changed: %s", stmt)
		}
	}
	for _, tc := range []struct{ name, column, value string }{
		{"action", "action", `jsonb_set(action,'{silence,comment}','"tampered"')`},
		{"review", "content", `jsonb_set(content,'{title}','"tampered"')`},
		{"parent scope", "application_id", `'app-2'`},
	} {
		t.Run(tc.name+" tamper rejected by loader", func(t *testing.T) {
			tx, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			table, trigger, idColumn := "v3_proposal_actions", "v3_proposal_action_immutable", "proposal_id"
			if tc.column == "content" || tc.name == "parent scope" {
				table, trigger, idColumn = "v3_proposals", "v3_proposal_review_identity_immutable", "id"
			}
			if _, err := tx.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER "+trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(t.Context(), "UPDATE "+table+" SET "+tc.column+"="+tc.value+" WHERE "+idColumn+"=$1", first.ProposalID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := LoadSilenceActionWithTx(t.Context(), tx, first.ProposalID); !errors.Is(err, ErrInvalidProposal) {
				t.Fatalf("tampered load err=%v", err)
			}
		})
	}
}

func TestCreateSilenceProposalRejectsStaleAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, update string
		request      func(SilenceProposalRequest) SilenceProposalRequest
		want         error
	}{
		{"machine", `UPDATE users SET v3_identity_kind='machine' WHERE email='writer@example.invalid'`, nil, store.ErrForbidden},
		{"disabled", `UPDATE users SET disabled=true WHERE email='writer@example.invalid'`, nil, store.ErrForbidden},
		{"revoked silence grant", `DELETE FROM v3_role_grants WHERE module='silences' AND action='manage'`, nil, store.ErrNotFound},
		{"revoked proposal grant", `DELETE FROM v3_role_grants WHERE module='proposals' AND action='change'`, nil, store.ErrForbidden},
		{"closed investigation", "", func(in SilenceProposalRequest) SilenceProposalRequest { in.InvestigationID = "inv-closed"; return in }, store.ErrNotFound},
		{"application archived", `UPDATE applications SET archived=true WHERE id='app-1'`, nil, store.ErrNotFound},
		{"application rebind", "", nil, store.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSilenceProposalFixture(t)
			if tc.update != "" {
				if _, err := f.db.Exec(t.Context(), tc.update); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "application rebind" {
				f.ledger = v3alertmanager.NewDatabaseSilenceLedger(f.db, f.ledger.Access,
					map[string]string{"test": "environment"}, map[string]map[string]string{"test": {"checkout": "app-2"}})
			}
			in := silenceProposalInput("authority-key-001")
			if tc.request != nil {
				in = tc.request(in)
			}
			if _, err := f.svc.CreateSilenceProposal(t.Context(), f.actor, in, f.ledger, f.client); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if p, a, r := f.counts(t); p != 0 || a != 0 || r != 0 {
				t.Fatalf("unauthorized rows proposals=%d actions=%d reservations=%d", p, a, r)
			}
			if n := f.requests.Load(); n != 0 {
				t.Fatalf("Alertmanager requests=%d", n)
			}
		})
	}
}

func TestCreateSilenceProposalRejectsInactiveActorBeforeApplicationLookup(t *testing.T) {
	for _, tc := range []struct {
		name, update string
	}{
		{"disabled", `UPDATE users SET disabled=true WHERE id=$1`},
		{"deleted", `UPDATE users SET disabled=true,deleted_at=now() WHERE id=$1`},
		{"machine", `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSilenceProposalFixture(t)
			if _, err := f.db.Exec(t.Context(), tc.update, f.actor.ID); err != nil {
				t.Fatal(err)
			}
			for _, applicationLabel := range []string{"checkout", "unknown-app"} {
				in := silenceProposalInput("inactive-actor-key-001")
				in.Silence.Matchers[0].Value = applicationLabel
				result, err := f.svc.CreateSilenceProposal(t.Context(), f.actor, in, f.ledger, f.client)
				if !errors.Is(err, store.ErrForbidden) || result.ProposalID != "" {
					t.Fatalf("%s actor, application %q: result=%+v err=%v; want forbidden", tc.name, applicationLabel, result, err)
				}
			}
			if p, a, r := f.counts(t); p != 0 || a != 0 || r != 0 || f.requests.Load() != 0 {
				t.Fatalf("%s actor produced rows or upstream calls: proposals=%d actions=%d reservations=%d requests=%d",
					tc.name, p, a, r, f.requests.Load())
			}
		})
	}
}

func TestCreateSilenceProposalDoesNotDiscloseInvestigationWithoutChangeGrant(t *testing.T) {
	for _, investigationID := range []string{"inv-1", "inv-closed", "inv-missing"} {
		t.Run(investigationID, func(t *testing.T) {
			f := newSilenceProposalFixture(t)
			if _, err := f.db.Exec(t.Context(), `DELETE FROM v3_role_grants
				WHERE role_id='viewer' AND module='proposals' AND action='change'`); err != nil {
				t.Fatal(err)
			}
			in := silenceProposalInput("hidden-investigation-key-001")
			in.InvestigationID = investigationID
			result, err := f.svc.CreateSilenceProposal(t.Context(), f.actor, in, f.ledger, f.client)
			if !errors.Is(err, store.ErrForbidden) || result.ProposalID != "" {
				t.Fatalf("investigation %q disclosed by result=%+v err=%v; want forbidden", investigationID, result, err)
			}
			if p, a, r := f.counts(t); p != 0 || a != 0 || r != 0 || f.requests.Load() != 0 {
				t.Fatalf("investigation %q produced rows or upstream calls: proposals=%d actions=%d reservations=%d requests=%d",
					investigationID, p, a, r, f.requests.Load())
			}
		})
	}
}

func TestCreateSilenceProposalRejectsChangeGrantRevokedWhileWaitingForInvestigation(t *testing.T) {
	f := newSilenceProposalFixture(t)
	ctx := t.Context()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT id FROM investigations WHERE id='inv-1' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	created := make(chan silenceCreateResult, 1)
	go func() {
		proposal, err := f.svc.CreateSilenceProposal(ctx, f.actor,
			silenceProposalInput("revoke-while-waiting-key-001"), f.ledger, f.client)
		created <- silenceCreateResult{proposal, err}
	}()
	waitForSilenceProposalLock(t, f.db, `FROM investigations i`, created)
	revoked := make(chan error, 1)
	go func() {
		tag, err := f.db.Exec(ctx, `DELETE FROM v3_role_grants
			WHERE role_id='viewer' AND module='proposals' AND action='change'`)
		if err == nil && tag.RowsAffected() != 1 {
			err = errors.New("change grant revocation did not delete exactly one row")
		}
		revoked <- err
	}()
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatalf("revoke change grant: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("change grant revocation did not complete before investigation lock release")
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-created:
		if !errors.Is(result.err, store.ErrForbidden) || result.proposal.ProposalID != "" {
			t.Fatalf("proposal after completed change grant revocation: %+v err=%v", result.proposal, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not finish after investigation lock release")
	}
	if p, a, r := f.counts(t); p != 0 || a != 0 || r != 0 || f.requests.Load() != 0 {
		t.Fatalf("revoked proposal produced rows or upstream calls: proposals=%d actions=%d reservations=%d requests=%d",
			p, a, r, f.requests.Load())
	}
}

// The investigation row lock stops the producer at its FOR SHARE query
// without adding a production test hook.
type silenceCreateResult struct {
	proposal SilenceProposalResult
	err      error
}

func TestCreateSilenceProposalRevocationCannotOvertakeCommit(t *testing.T) {
	for _, revocation := range []struct {
		name, revokeSQL, waitQuery string
		setup                      func(*testing.T, *silenceProposalFixture)
	}{
		{name: "silences manage", revokeSQL: `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='silences' AND action='manage' AND scope_kind='application' AND scope_id='app-1'`, waitQuery: `DELETE FROM v3_role_grants`},
		{name: "proposals change", revokeSQL: `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='proposals' AND action='change' AND scope_kind='application' AND scope_id='app-1'`, waitQuery: `DELETE FROM v3_role_grants`},
		{name: "direct role assignment", setup: func(t *testing.T, f *silenceProposalFixture) {
			if _, err := f.db.Exec(t.Context(), `UPDATE rbac_user_roles SET origin='direct' WHERE user_id=$1 AND role_id='viewer'`, f.actor.ID); err != nil {
				t.Fatal(err)
			}
		}, revokeSQL: `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='viewer' AND origin='direct'`, waitQuery: `DELETE FROM rbac_user_roles`},
		{name: "account disabled", revokeSQL: `UPDATE users SET disabled=true WHERE id=$1`, waitQuery: `UPDATE users SET disabled=true`},
		{name: "department migration", setup: func(t *testing.T, f *silenceProposalFixture) {
			for _, stmt := range []string{
				`INSERT INTO teams(id,name) VALUES('proposal-race-dept','Proposal Race Dept')`,
				`INSERT INTO rbac_department_roles(department_id,role_id) VALUES('proposal-race-dept','viewer')`,
				`DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='viewer'`,
				`UPDATE users SET department_id='proposal-race-dept' WHERE id=$1`,
			} {
				args := []any{}
				if strings.Contains(stmt, "$1") {
					args = append(args, f.actor.ID)
				}
				if _, err := f.db.Exec(t.Context(), stmt, args...); err != nil {
					t.Fatal(err)
				}
			}
		}, revokeSQL: `UPDATE users SET department_id=NULL WHERE id=$1`, waitQuery: `UPDATE users SET department_id=NULL`},
	} {
		t.Run(revocation.name, func(t *testing.T) {
			f := newSilenceProposalFixture(t)
			ctx := t.Context()
			if revocation.setup != nil {
				revocation.setup(t, f)
			}
			var effective int
			if err := f.db.QueryRow(ctx, `SELECT count(*) FROM v3_effective_role_grants WHERE user_id=$1
				AND ((module='silences' AND action='manage') OR (module='proposals' AND action='change'))`, f.actor.ID).Scan(&effective); err != nil || effective != 2 {
				t.Fatalf("fixture must have both effective grants: count=%d err=%v", effective, err)
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `SELECT id FROM investigations WHERE id='inv-1' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}

			created := make(chan silenceCreateResult, 1)
			go func() {
				proposal, err := f.svc.CreateSilenceProposal(ctx, f.actor,
					silenceProposalInput("revoke-race-key-001"), f.ledger, f.client)
				created <- silenceCreateResult{proposal, err}
			}()
			waitForSilenceProposalLock(t, f.db, `SELECT i.updated_at FROM investigations i`, created)

			revoked := make(chan error, 1)
			go func() {
				args := []any{}
				if strings.Contains(revocation.revokeSQL, "$1") {
					args = append(args, f.actor.ID)
				}
				_, err := f.db.Exec(ctx, revocation.revokeSQL, args...)
				revoked <- err
			}()
			// A completed revocation may only precede a rejected proposal. A
			// guarded producer can instead hold the grant until it commits.
			revokedBeforeRelease := false
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case err := <-revoked:
					if err != nil {
						t.Fatalf("revoke grant: %v", err)
					}
					revokedBeforeRelease = true
					goto release
				case <-ticker.C:
					if pgQueryWaitingForLock(t, f.db, revocation.waitQuery) {
						goto release
					}
				case <-deadline.C:
					t.Fatal("revocation neither completed nor waited on a PostgreSQL lock")
				}
			}
		release:
			if err := blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-created:
				if revokedBeforeRelease {
					if result.err == nil || result.proposal.ProposalID != "" {
						t.Fatalf("proposal committed after completed %s revocation: %+v err=%v", revocation.name, result.proposal, result.err)
					}
				} else if result.err != nil || result.proposal.ProposalID == "" {
					t.Fatalf("proposal did not commit before blocked revocation: %+v err=%v", result.proposal, result.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("producer did not finish after investigation lock release")
			}
			if !revokedBeforeRelease {
				select {
				case err := <-revoked:
					if err != nil {
						t.Fatalf("revoke grant: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("revocation did not finish after producer commit")
				}
			}
			proposals, actions, reservations := f.counts(t)
			want := 1
			if revokedBeforeRelease {
				want = 0
			}
			if proposals != want || actions != want || reservations != 0 || f.requests.Load() != 0 {
				t.Fatalf("after revocation: proposals=%d actions=%d reservations=%d Alertmanager requests=%d; want proposal rows=%d",
					proposals, actions, reservations, f.requests.Load(), want)
			}
		})
	}
}

func TestCreateSilenceProposalDoesNotBlockUnrelatedRoleRevocation(t *testing.T) {
	f := newSilenceProposalFixture(t)
	ctx := t.Context()
	other, err := (&store.Store{DB: f.db}).CreateUser(ctx,
		core.User{Email: "other@example.invalid", Name: "Other", Role: "viewer"}, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	tag, err := f.db.Exec(ctx, `UPDATE rbac_user_roles SET origin='direct'
		WHERE user_id=$1 AND role_id='viewer'`, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("unrelated account must have one direct viewer assignment: updated=%d", tag.RowsAffected())
	}

	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT id FROM investigations WHERE id='inv-1' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	created := make(chan silenceCreateResult, 1)
	go func() {
		proposal, err := f.svc.CreateSilenceProposal(ctx, f.actor,
			silenceProposalInput("unrelated-revoke-key-001"), f.ledger, f.client)
		created <- silenceCreateResult{proposal, err}
	}()
	waitForSilenceProposalLock(t, f.db, `FROM investigations i`, created)

	revoked := make(chan error, 1)
	go func() {
		tag, err := f.db.Exec(ctx, `DELETE FROM rbac_user_roles
			WHERE user_id=$1 AND role_id='viewer' AND origin='direct'`, other.ID)
		if err == nil && tag.RowsAffected() != 1 {
			err = errors.New("unrelated role revocation did not delete exactly one assignment")
		}
		revoked <- err
	}()
	// The unrelated account must remain writable while this proposal waits on
	// its investigation. A table-wide grant lock makes the revoke wait here.
	revokedBeforeRelease := false
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waitedOnGrantTable := false
	for !revokedBeforeRelease && !waitedOnGrantTable {
		select {
		case err := <-revoked:
			if err != nil {
				t.Fatalf("revoke unrelated role: %v", err)
			}
			revokedBeforeRelease = true
		case <-ticker.C:
			waitedOnGrantTable = pgQueryWaitingForLock(t, f.db, `DELETE FROM rbac_user_roles`)
		case <-deadline.C:
			t.Fatal("unrelated role revocation neither completed nor waited on a PostgreSQL lock")
		}
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-created:
		if result.err != nil || result.proposal.ProposalID == "" {
			t.Fatalf("proposal after investigation lock release: %+v err=%v", result.proposal, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not finish after investigation lock release")
	}
	if !revokedBeforeRelease {
		select {
		case err := <-revoked:
			if err != nil {
				t.Fatalf("revoke unrelated role: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("unrelated role revocation did not finish after producer commit")
		}
		t.Fatal("unrelated role revocation waited for the investigation lock release")
	}
	var remaining int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM rbac_user_roles WHERE user_id=$1 AND role_id='viewer' AND origin='direct'`, other.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("unrelated role assignments remaining=%d err=%v", remaining, err)
	}
}

// The old replay loaded its immutable parent after locking the grant table.
// Reject holds the parent before reading the reviewer row; UpdateMember holds
// that row before its role-sync trigger needs the grant table. Prelocking the
// parent before grant sources breaks this three-transaction cycle. The session
// advisory lock below is only a deterministic barrier in a disposable schema.
func TestSilenceReplayRejectMemberUpdateDoesNotDeadlock(t *testing.T) {
	f := newSilenceProposalFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	st := &store.Store{DB: f.db}
	first, err := f.svc.CreateSilenceProposal(ctx, f.actor,
		silenceProposalInput("replay-review-member-cycle"), f.ledger, f.client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
		VALUES('viewer','proposals','manage','application','app-1')`); err != nil {
		t.Fatal(err)
	}
	reviewer, err := st.CreateUser(ctx, core.User{Email: "review-cycle@example.invalid", Name: "Review Cycle", Role: "viewer"}, "isolated-review-password")
	if err != nil {
		t.Fatal(err)
	}
	_, reviewerToken, err := st.Login(ctx, reviewer.Email, "isolated-review-password")
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil, true)
	f.svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		&rbac.V3Policy{Store: st}, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	// Every UpdateMember statement sets role, even when this test only disables
	// the account. Its existing role-sync trigger will then write rbac_user_roles.
	if _, err := f.db.Exec(ctx, `CREATE FUNCTION pause_review_member_update() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
	 IF NEW.id = '`+reviewer.ID+`' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema(), 0)); END IF;
	 RETURN NEW;
	END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `CREATE TRIGGER pause_review_member_update BEFORE UPDATE OF role ON users
		FOR EACH ROW EXECUTE FUNCTION pause_review_member_update()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := f.db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Release()
	if _, err := barrier.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended(current_schema(), 0))`); err != nil {
		t.Fatal(err)
	}
	barrierHeld := true
	defer func() {
		if barrierHeld {
			_, _ = barrier.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended(current_schema(), 0))`)
		}
	}()
	memberDone := make(chan error, 1)
	go func() {
		disabled := true
		_, err := st.UpdateMember(store.WithPrincipal(ctx, admin), reviewer.ID,
			store.MemberUpdate{AccountUpdate: store.AccountUpdate{Disabled: &disabled}})
		memberDone <- err
	}()
	waitForSilenceCycleLock(t, f.db, `UPDATE users SET email=`, memberDone)

	rejectDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/v1/proposals/"+first.ProposalID+"/reject",
			strings.NewReader(`{"comment":"Unsafe"}`)).WithContext(ctx)
		r.Header.Set("X-AIOps-Environment", "test")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: reviewerToken})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		rejectDone <- w
	}()
	waitForSilenceCycleLock(t, f.db, `SELECT id FROM users
 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, rejectDone)

	replayDone := make(chan silenceCreateResult, 1)
	go func() {
		proposal, err := f.svc.CreateSilenceProposal(ctx, f.actor,
			silenceProposalInput("replay-review-member-cycle"), f.ledger, f.client)
		replayDone <- silenceCreateResult{proposal, err}
	}()
	// Both the old loader and the corrected prelock wait on the parent here.
	waitForSilenceCycleLock(t, f.db, `FROM v3_proposal_actions`, replayDone)
	if _, err := barrier.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended(current_schema(), 0))`); err != nil {
		t.Fatal(err)
	}
	barrierHeld = false
	select {
	case err := <-memberDone:
		if err != nil {
			t.Fatalf("member disable after barrier: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("member disable did not finish")
	}
	select {
	case response := <-rejectDone:
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"unauthorized"`) {
			t.Fatalf("disabled reviewer rejection status=%d body=%s", response.Code, response.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("reject did not finish")
	}
	select {
	case result := <-replayDone:
		if result.err != nil || result.proposal != first {
			t.Fatalf("immutable replay=%+v err=%v; want %+v", result.proposal, result.err, first)
		}
	case <-ctx.Done():
		t.Fatal("replay did not finish")
	}
	var status string
	var rejects int
	if err := f.db.QueryRow(ctx, `SELECT status FROM v3_proposals WHERE id=$1`, first.ProposalID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action='v3.proposal.rejected'`, first.ProposalID).Scan(&rejects); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || rejects != 0 || f.requests.Load() != 0 {
		t.Fatalf("disabled reviewer changed proposal or called upstream: status=%s reject audits=%d requests=%d",
			status, rejects, f.requests.Load())
	}
}

func waitForSilenceCycleLock[T any](t *testing.T, db *pgxpool.Pool, query string, done <-chan T) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			t.Fatalf("operation finished before waiting for lock: %s", query)
		case <-ticker.C:
			if pgQueryWaitingForLock(t, db, query) {
				return
			}
		case <-deadline.C:
			t.Fatalf("operation did not wait for lock: %s", query)
		}
	}
}

func waitForSilenceProposalLock(t *testing.T, db *pgxpool.Pool, query string, created <-chan silenceCreateResult) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-created:
			t.Fatalf("producer finished before investigation lock: %+v err=%v", result.proposal, result.err)
		case <-ticker.C:
			if pgQueryWaitingForLock(t, db, query) {
				return
			}
		case <-deadline.C:
			t.Fatal("producer did not wait on investigation row lock")
		}
	}
}

func pgQueryWaitingForLock(t *testing.T, db *pgxpool.Pool, query string) bool {
	t.Helper()
	var waiting bool
	if err := db.QueryRow(t.Context(), `SELECT EXISTS(
		SELECT 1 FROM pg_stat_activity
		WHERE pid <> pg_backend_pid() AND wait_event_type='Lock'
		  AND application_name=$2 AND position($1 in query) > 0)`,
		query, db.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	return waiting
}
