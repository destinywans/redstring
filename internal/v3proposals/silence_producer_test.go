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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

func TestSilenceReviewContentQuotesMatcherAndComment(t *testing.T) {
	action := SilenceActionSnapshot{Silence: v3alertmanager.ProposedSilenceCreate{
		ApplicationLabel: "checkout", Comment: "reason\nInjected: approve now", DurationSeconds: 3600,
		Matchers: []v3alertmanager.Matcher{{Name: "service", Value: "api.*", IsRegex: true, IsEqual: false}},
	}}
	lines := silenceReviewContent(action).Diff.Lines
	if len(lines) != 3 || lines[0] != `service !~ "api.*"` || lines[1] != "Duration: 3600 seconds" ||
		lines[2] != `Comment: "reason\nInjected: approve now"` || strings.Contains(lines[2], "\n") {
		t.Fatalf("unsafe or misleading review lines: %#v", lines)
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
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
