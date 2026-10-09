package v3events

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

type upstreamStub struct {
	alerts []v3alertmanager.AlertState
	err    error
}

func (u *upstreamStub) ListAlertStates(context.Context, string) ([]v3alertmanager.AlertState, error) {
	return u.alerts, u.err
}

// PA-47: firing incidents whose alert is gone upstream (or replaced by a later
// occurrence) are resolved; silenced ones are marked; nothing changes when the
// upstream read fails, for recent incidents or for unreviewed sources.
func TestAlertReconcilerResolvesGoneAlertsAndMarksSilenced(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_alert_reconcile_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
		INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-time.Hour)
	for _, row := range []struct {
		id, source, fingerprint string
		updated                 time.Time
	}{
		{"gone", "am", "fp-gone", now.Add(-10 * time.Minute)},
		{"silenced", "am", "fp-silenced", now.Add(-10 * time.Minute)},
		{"refired", "am", "fp-refired", now.Add(-10 * time.Minute)},
		{"active", "am", "fp-active", now.Add(-10 * time.Minute)},
		{"recent", "am", "fp-recent", now},
		{"unreviewed", "other", "fp-unreviewed", now.Add(-10 * time.Minute)},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,updated_at)
			VALUES($1,$2,'app','app-dev',$3,'Checkout 5xx','critical','firing',$4,$5)`, row.id, row.source, row.fingerprint, started, row.updated); err != nil {
			t.Fatal(err)
		}
	}
	source := &upstreamStub{alerts: []v3alertmanager.AlertState{
		{Fingerprint: "fp-silenced", StartsAt: started, State: "suppressed", Silenced: true},
		{Fingerprint: "fp-refired", StartsAt: now.Add(-5 * time.Minute), State: "active"},
		{Fingerprint: "fp-active", StartsAt: started, State: "active"},
	}}
	reconciler := &AlertReconciler{DB: db, Source: source, Now: func() time.Time { return now },
		Bindings: map[string]GroupPolicyBinding{"dev": {SourceIDs: map[string]bool{"am": true}}}}
	alertStates := func() map[string]string {
		rows, err := db.Query(ctx, `SELECT id,alert_status FROM ops_incidents`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, state string
			if err := rows.Scan(&id, &state); err != nil {
				t.Fatal(err)
			}
			out[id] = state
		}
		return out
	}

	// A failed or invalid upstream read changes nothing.
	source.err = errors.New("Alertmanager HTTP 503")
	if _, err := reconciler.ReconcileEnvironment(ctx, "dev"); err == nil {
		t.Fatal("a failed upstream read was reconciled")
	}
	source.err = nil
	duplicate := source.alerts
	source.alerts = append(append([]v3alertmanager.AlertState{}, duplicate...), duplicate[0])
	if _, err := reconciler.ReconcileEnvironment(ctx, "dev"); err == nil {
		t.Fatal("duplicate fingerprints were reconciled")
	}
	source.alerts = duplicate
	for id, state := range alertStates() {
		if state != "firing" {
			t.Fatalf("%s changed after a failed read: %s", id, state)
		}
	}

	result, err := reconciler.ReconcileEnvironment(ctx, "dev")
	if err != nil || result != (ReconcileResult{Checked: 4, Resolved: 2, Active: 1, Silenced: 1}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := map[string]string{"gone": "resolved", "refired": "resolved", "silenced": "firing", "active": "firing",
		"recent": "firing", "unreviewed": "firing"}
	if got := alertStates(); len(got) != len(want) {
		t.Fatalf("states: %v", got)
	} else {
		for id, state := range want {
			if got[id] != state {
				t.Fatalf("%s: %s, want %s", id, got[id], state)
			}
		}
	}
	var resolvedAudits, stateAudits int
	var endsAt time.Time
	if err := db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM management_audit WHERE action='v3.alert.reconciled' AND actor_kind='system' AND application_id='app' AND changes->>'environment_id'='dev'),
		(SELECT count(*) FROM management_audit WHERE action='v3.alert.upstream_state' AND changes->>'to'='silenced'),
		(SELECT ends_at FROM ops_incidents WHERE id='gone')`).Scan(&resolvedAudits, &stateAudits, &endsAt); err != nil ||
		resolvedAudits != 2 || stateAudits != 1 || !endsAt.Equal(now) {
		t.Fatalf("audits resolved=%d state=%d ends=%v err=%v", resolvedAudits, stateAudits, endsAt, err)
	}

	// The silenced incident carries its upstream state into reads, as a
	// localized badge while fresh and as unverified once stale.
	silenced, err := scanIncident(db.QueryRow(ctx, incidentSelect+` WHERE i.id='silenced'`))
	if err != nil || upstreamBadge(silenced, now) != "silenced" || upstreamBadge(silenced, now.Add(10*time.Minute)) != "unverified" {
		t.Fatalf("upstream state: %+v %v", silenced, err)
	}
	if badges := textFor("zh-CN").upstreamBadges(map[string]bool{"silenced": true}); len(badges) != 1 || badges[0].Text != "已静默（Alertmanager）" {
		t.Fatalf("badges: %+v", badges)
	}
	// A repeat pass with the same upstream state writes no new audit row.
	if _, err := reconciler.ReconcileEnvironment(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.alert.%'`).Scan(&stateAudits); err != nil || stateAudits != 3 {
		t.Fatalf("repeat pass audited again: %d %v", stateAudits, err)
	}
	// An environment without reviewed sources is never touched.
	if result, err := reconciler.ReconcileEnvironment(ctx, "prod"); err != nil || result.Checked != 0 {
		t.Fatalf("unbound environment: %+v %v", result, err)
	}
}
