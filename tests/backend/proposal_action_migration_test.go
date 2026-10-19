package backend_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/migrations"
	"github.com/jackc/pgx/v5/pgconn"
)

const oldProposalActionKinds = "CHECK (action_kind IN ('alertmanager.silence.create','prometheus.alert_rule.enable'));"
const repairedProposalActionKinds = "CHECK (action_kind IN ('alertmanager.silence.create','alertmanager.silence.cancel','prometheus.alert_rule.enable'));"

// Replay actual historical files and their filename ledger. For the old-143
// fixture only, retain the original faulty definition even after source repair.
func proposalActionHistory(t *testing.T, s *store.Store, last string, original143 bool) {
	t.Helper()
	tx, err := s.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), `CREATE TABLE schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, name := range migrationNames(t) {
		if name[:3] > last {
			continue
		}
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(raw)
		if original143 && name == "143_v3_alert_rule_enable_intents.sql" {
			sql = strings.Replace(sql, repairedProposalActionKinds, oldProposalActionKinds, 1)
			if !strings.Contains(sql, oldProposalActionKinds) {
				t.Fatal("original 143 fixture definition unavailable")
			}
		}
		if _, err := tx.Exec(t.Context(), sql); err != nil {
			t.Fatalf("historical migration %s: %v", name, err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func seedProposalActionMetadata(t *testing.T, s *store.Store) {
	t.Helper()
	_, err := s.DB.Exec(t.Context(), `INSERT INTO users(id,email,name,role,password_hash) VALUES('migration-owner','migration@example.invalid','Synthetic history','viewer','unused');
INSERT INTO applications(id,key,name,environment) VALUES('migration-app','migration-app','Synthetic migration app','test');
INSERT INTO application_environments(id,application_id,key,name) VALUES('migration-env','migration-app','test','Test');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('migration-app','test');
INSERT INTO investigations(id,application_id,environment_id,title) VALUES('migration-investigation','migration-app','migration-env','Synthetic historical review');`)
	if err != nil {
		t.Fatal(err)
	}
}

// These are legal synthetic review/action metadata, not approved decisions or
// executions. Keep all real FKs, shape/digest checks and immutable triggers on.
func insertProposalActionMetadata(t *testing.T, s *store.Store, kind string) (string, error) {
	t.Helper()
	id := store.ID()
	tx, err := s.DB.Begin(t.Context())
	if err != nil {
		return id, err
	}
	defer tx.Rollback(t.Context())
	_, err = tx.Exec(t.Context(), `INSERT INTO v3_proposals(id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
VALUES($1,'migration-investigation','migration-app','test','{"title":"Synthetic migration review"}','migration-owner','user')`, id)
	if err != nil {
		return id, err
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO v3_proposal_actions(proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,request_hash,binding_fingerprint,creator_id,environment_id,application_id,investigation_id,investigation_updated_at,idempotency_key,source_kind)
SELECT $1,$2,1,jsonb_build_object('kind',$2::text,'draft_operation_id',$1::text,'draft_id',$1::text),repeat('a',64),repeat('b',64),repeat('c',64),repeat('d',64),'migration-owner','test','migration-app','migration-investigation',updated_at,$1,'human_request' FROM investigations WHERE id='migration-investigation'`, id, kind)
	if err != nil {
		return id, err
	}
	return id, tx.Commit(t.Context())
}

func assertProposalActionKinds(t *testing.T, s *store.Store) {
	t.Helper()
	for _, kind := range []string{"alertmanager.silence.create", "alertmanager.silence.cancel", "prometheus.alert_rule.enable"} {
		if _, err := insertProposalActionMetadata(t, s, kind); err != nil {
			t.Fatalf("legal action %s rejected: %v", kind, err)
		}
	}
	id, err := insertProposalActionMetadata(t, s, "unreviewed.action")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "v3_proposal_actions_action_kind_check" {
		t.Fatalf("invalid kind must fail exact CHECK with SQLSTATE 23514, got %v", err)
	}
	var count int
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_proposals WHERE id=$1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("invalid action left a partial proposal")
	}
}

func migrateProposalActionsConcurrently(t *testing.T, s *store.Store) {
	t.Helper()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Migrate(t.Context()) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLedger(t, s)
}

func TestProposalActionMigrationFreshConcurrentAndRepeat(t *testing.T) {
	s, _ := isolatedStore(t, readFixture(t, false))
	migrateProposalActionsConcurrently(t, s)
	seedProposalActionMetadata(t, s)
	assertProposalActionKinds(t, s)
}

func TestProposalActionMigrationUpgrade142PreservesCancel(t *testing.T) {
	s, _ := isolatedStore(t, readFixture(t, false))
	proposalActionHistory(t, s, "142", false)
	seedProposalActionMetadata(t, s)
	id, err := insertProposalActionMetadata(t, s, "alertmanager.silence.cancel")
	if err != nil {
		t.Fatalf("legal pre-143 cancellation fixture: %v", err)
	}
	migrateProposalActionsConcurrently(t, s)
	var kind, digest string
	if err := s.DB.QueryRow(t.Context(), `SELECT action_kind,action_digest FROM v3_proposal_actions WHERE proposal_id=$1`, id).Scan(&kind, &digest); err != nil {
		t.Fatal(err)
	}
	if kind != "alertmanager.silence.cancel" || digest != strings.Repeat("a", 64) {
		t.Fatal("upgrade altered or lost immutable historical cancellation")
	}
	assertProposalActionKinds(t, s)
}

func TestProposalActionMigrationRepairAlreadyAppliedOld143(t *testing.T) {
	s, _ := isolatedStore(t, readFixture(t, false))
	proposalActionHistory(t, s, "152", true)
	seedProposalActionMetadata(t, s)
	create, err := insertProposalActionMetadata(t, s, "alertmanager.silence.create")
	if err != nil {
		t.Fatal(err)
	}
	_, err = insertProposalActionMetadata(t, s, "alertmanager.silence.cancel")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatal("old fixture must retain the original 143 rejection before repair")
	}
	var before int
	var beforeLedger string
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*),jsonb_object_agg(name,applied_at)::text FROM schema_migrations`).Scan(&before, &beforeLedger); err != nil {
		t.Fatal(err)
	}
	migrateProposalActionsConcurrently(t, s)
	var after int
	var oldLedger string
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(t.Context(), `SELECT jsonb_object_agg(name,applied_at)::text FROM schema_migrations WHERE name<>'153_v3_proposal_action_kind_repair.sql'`).Scan(&oldLedger); err != nil {
		t.Fatal(err)
	}
	if after != before+1 || oldLedger != beforeLedger {
		t.Fatal("repair must apply only new 153; old filename ledger may not be replayed or rewritten")
	}
	var preserved bool
	if err := s.DB.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM v3_proposal_actions WHERE proposal_id=$1 AND action_kind='alertmanager.silence.create')`, create).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if !preserved {
		t.Fatal("repair lost an existing action")
	}
	assertProposalActionKinds(t, s)
}
