package v3events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// PA-239: a group's severity is its highest member's. A known P1 member
// decides it; otherwise an unknown member could outrank the known ones, so
// the group's severity is unknown too.
func TestAlertGroupSeverityWithUnknownMembers(t *testing.T) {
	start := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	member := func(id, severity string) incidentRecord {
		return incidentRecord{ID: id, SourceID: "am", Fingerprint: "fp", ApplicationID: "app-1",
			EnvironmentID: "env-1", EnvironmentKey: "prod", Title: "Odd", Severity: severity,
			AlertStatus: "firing", StartsAt: start}
	}
	for _, tc := range []struct {
		name    string
		members []incidentRecord
		want    string
		reason  string
	}{
		{"unknown only", []incidentRecord{member("a", "major")}, "", "unclassified"},
		{"P1 outranks unknown", []incidentRecord{member("a", "major"), member("b", "critical")}, "P1", ""},
		{"unknown may outrank P2", []incidentRecord{member("a", "warning"), member("b", "major")}, "", "unclassified"},
		{"known only", []incidentRecord{member("a", "info"), member("b", "low")}, "P3", ""},
	} {
		groups, err := aggregateAlertGroups(tc.members, start.Add(time.Hour))
		if err != nil || len(groups) != 1 {
			t.Fatalf("%s: groups=%d err=%v", tc.name, len(groups), err)
		}
		for _, g := range groups {
			got, reason := "", ""
			if g.group.Severity != nil {
				got = *g.group.Severity
			}
			if g.group.SeverityReason != nil {
				reason = *g.group.SeverityReason
			}
			if got != tc.want || reason != tc.reason || (got == "") != slices.Contains(g.group.Unavailable, "severity") {
				t.Errorf("%s: severity=%q reason=%q unavailable=%v", tc.name, got, reason, g.group.Unavailable)
			}
		}
	}
}

// severityFixture serves the events routes over an isolated schema holding
// the given ops_incidents VALUES rows (id,source_id,application_id,
// environment_id,fingerprint,title,severity,alert_status,summary,starts_at,
// labels) for application "app" in environment "dev"; get decodes a 200.
func severityFixture(t *testing.T, alerts string) (get func(path string, into any)) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_severity_unknown_test_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-severity-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
		INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev');
		INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,summary,starts_at,labels)
		 VALUES `+alerts); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-severity-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	service := New(db,
		func(ctx context.Context, user core.User, app, env, action string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
		},
		func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
			return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
		},
	)
	api := httpapi.New(st, nil, true)
	service.RegisterNew(api)
	service.RegisterV3Incidents(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	return func(path string, into any) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
			t.Fatalf("GET %s: %v %s", path, err, w.Body.String())
		}
	}
}

type severityFields struct {
	ID             string   `json:"id"`
	Severity       *string  `json:"severity"`
	SeverityReason *string  `json:"severity_reason"`
	Unavailable    []string `json:"unavailable"`
}

// PA-239: an alert whose recorded severity is outside the table keeps every
// events page readable; each one says the severity is unknown instead of
// failing (inbox, alert groups, alert and incident detail) or calling it P4
// (incident list).
func TestUnknownSeverityAlertKeepsEventPagesReadable(t *testing.T) {
	get := severityFixture(t, `('known','am','app','app-dev','fp-known','Known alert','critical','firing','',now()-interval '5 minutes','{"severity":"critical"}'),
		       ('odd','am','app','app-dev','fp-odd','Odd alert','major','firing','',now()-interval '3 minutes','{"severity":"major"}')`)
	unknown := func(where string, v severityFields, listsUnavailable bool) {
		t.Helper()
		if v.Severity != nil || v.SeverityReason == nil || *v.SeverityReason != "unclassified" ||
			listsUnavailable && !slices.Contains(v.Unavailable, "severity") {
			t.Errorf("%s: severity=%v reason=%v unavailable=%v; want null, unclassified", where, v.Severity, v.SeverityReason, v.Unavailable)
		}
	}

	var inbox struct {
		Items []struct {
			AlertID string `json:"alert_id"`
			Tone    string `json:"tone"`
		} `json:"items"`
	}
	get("/api/v1/inbox", &inbox)
	if len(inbox.Items) != 2 {
		t.Fatalf("inbox items=%+v", inbox.Items)
	}
	for _, item := range inbox.Items {
		if item.AlertID == "odd" && item.Tone != "neutral" {
			t.Errorf("unknown severity inbox tone=%q; want neutral", item.Tone)
		}
	}

	var groups struct {
		Items []severityFields `json:"items"`
	}
	get("/api/v1/alert-groups", &groups)
	if len(groups.Items) != 2 {
		t.Fatalf("alert groups=%+v", groups.Items)
	}
	// Known P1 first, the unknown group after it.
	if groups.Items[0].Severity == nil || *groups.Items[0].Severity != "P1" {
		t.Errorf("first group=%+v", groups.Items[0])
	}
	unknown("alert group", groups.Items[1], true)

	var incidents struct {
		Items []severityFields `json:"items"`
	}
	get("/api/v1/incidents", &incidents)
	found := false
	for _, item := range incidents.Items {
		if item.ID == "odd" {
			found = true
			unknown("incident list", item, false)
		}
	}
	if !found {
		t.Fatalf("incident list=%+v", incidents.Items)
	}

	var alert severityFields
	get("/api/v1/alerts/odd", &alert)
	unknown("alert detail", alert, true)
	var incident severityFields
	get("/api/v1/incidents/odd", &incident)
	unknown("incident detail", incident, true)
}

// PA-240: an alert that carried no severity label has no severity. New rows
// store it as ”; rows stored before the fix hold "warning" with recorded
// labels that lack severity. A row without recorded labels (AI patrol,
// older rows) and a real warning label keep P2.
func TestMissingSeverityIsUnknownNotWarning(t *testing.T) {
	get := severityFixture(t, `('blank','am','app','app-dev','fp-blank','Blank alert','','firing','',now()-interval '5 minutes','{"alertname":"Blank"}'),
		       ('legacy','am','app','app-dev','fp-legacy','Legacy alert','warning','firing','',now()-interval '4 minutes','{"alertname":"Legacy"}'),
		       ('nolabels','am','app','app-dev','fp-nolabels','Unlabelled alert','warning','firing','',now()-interval '3 minutes',NULL),
		       ('warned','am','app','app-dev','fp-warned','Warned alert','warning','firing','',now()-interval '2 minutes','{"severity":"warning"}')`)
	for _, tc := range []struct{ id, want string }{{"blank", ""}, {"legacy", ""}, {"nolabels", "P2"}, {"warned", "P2"}} {
		for _, path := range []string{"/api/v1/alerts/" + tc.id, "/api/v1/incidents/" + tc.id} {
			var v severityFields
			get(path, &v)
			if tc.want == "" {
				if v.Severity != nil || v.SeverityReason == nil || *v.SeverityReason != "not_filled" || !slices.Contains(v.Unavailable, "severity") {
					t.Errorf("%s: severity=%v reason=%v unavailable=%v; want null, not_filled", path, v.Severity, v.SeverityReason, v.Unavailable)
				}
			} else if v.Severity == nil || *v.Severity != tc.want || v.SeverityReason != nil {
				t.Errorf("%s: severity=%v reason=%v; want %s", path, v.Severity, v.SeverityReason, tc.want)
			}
		}
	}
	var incidents struct {
		Items []severityFields `json:"items"`
	}
	get("/api/v1/incidents", &incidents)
	for _, item := range incidents.Items {
		if (item.ID == "blank" || item.ID == "legacy") && (item.Severity != nil || item.SeverityReason == nil || *item.SeverityReason != "not_filled") {
			t.Errorf("incident list %s: severity=%v reason=%v", item.ID, item.Severity, item.SeverityReason)
		}
	}
}
