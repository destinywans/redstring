package v3configsync

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// keyedManifest signs a schema 3 manifest of the source instance, whose
// application is app-src with key checkout-api.
func keyedManifest(t *testing.T, kind, sourceID, version string, set func(*readManifestV2)) readManifestV2 {
	t.Helper()
	m := readManifestV2{Schema: readManifestKeyed, PromotionID: store.ID(), Kind: kind, SourceID: sourceID,
		SourceVersion: version, SourceInstance: "aiops-test", SourceEnvironment: "test",
		ApplicationID: "app-src", ApplicationKey: "checkout-api"}
	set(&m)
	snapshot, _, err := m.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(raw)
	m.SourceDigest = hex.EncodeToString(sum[:])
	if err := m.sign(testSigningKey()); err != nil {
		t.Fatal(err)
	}
	return m
}

// PA-100: nonprod and prod register checkout-api separately, under different
// IDs. A Runbook, an alert rule and an Agent version exported from nonprod
// import into prod's checkout-api by key, with the source ID kept as
// evidence; the read side verifies them the same way.
func TestKindImportMapsApplicationByKey(t *testing.T) {
	st := isolatedConfigStore(t)
	ctx := t.Context()
	installAgentPromotionTables(t, st, "prod")
	for _, sql := range []string{
		`ALTER TABLE knowledge_documents ADD COLUMN created_at timestamptz NOT NULL DEFAULT now()`,
		`ALTER TABLE v3_runbooks ADD COLUMN source_ref text NOT NULL DEFAULT ''`,
		`ALTER TABLE v3_runbooks ADD COLUMN when_text text NOT NULL DEFAULT ''`,
		`CREATE TABLE knowledge_history(document_id text NOT NULL,version integer NOT NULL,actor_id text NOT NULL,operation text NOT NULL)`,
		// app-a keeps another key; prod's checkout-api is app-prod.
		`UPDATE applications SET key='legacy-app-a' WHERE id='app-a'`,
		`INSERT INTO applications(id,name,owner_team_id,key) VALUES('app-prod','Checkout','team-ops','checkout-api')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-prod-prod','app-prod','prod','Prod')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-prod','prod')`,
		`INSERT INTO applications(id,name,key) VALUES('app-test-only','Test only','test-only')`,
	} {
		if _, err := st.DB.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	var hasAlertRules bool
	if err := st.DB.QueryRow(ctx, `SELECT to_regclass('v3_config_alert_rules') IS NOT NULL`).Scan(&hasAlertRules); err != nil {
		t.Fatal(err)
	}
	if !hasAlertRules {
		migration, err := os.ReadFile("../../migrations/125_v3_config_sync_import_targets.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	access := &agentPromotionAccess{}
	s := New(st.DB, access, &fakeRepository{})
	s.TrustedSources = map[string]ed25519.PublicKey{"aiops-test": testSigningKey().Public().(ed25519.PublicKey)}
	admin := core.User{ID: "admin"}

	identity := strings.Repeat("ab", 32)
	alertLabels := map[string]string{"environment": "test", "application": "checkout-api", "severity": "P3"}
	alert := readAlertSnapshot{Name: "CheckoutErrors", Expression: `sum(rate(http_requests_total{application="checkout-api"}[5m])) > 1`,
		DurationSeconds: 60, Labels: alertLabels, Annotations: map[string]string{}, SourceIdentityDigest: identity}
	raw, _ := json.Marshal(alert)
	sum := sha256.Sum256(raw)
	alert.Version = "sha256:" + hex.EncodeToString(sum[:])
	manifests := []readManifestV2{
		keyedManifest(t, "runbook", "source-runbook", "3", func(m *readManifestV2) {
			m.Runbook = &readRunbookSnapshot{Title: "Inspect traffic", Version: 3, When: "Error rate rises",
				Steps: []readRunbookStep{{N: 1, Kind: "check", Title: "Inspect metrics", Readonly: true}}}
		}),
		keyedManifest(t, "alert_rule", "prom-"+identity[:24], alert.Version, func(m *readManifestV2) { m.AlertRule = &alert }),
		keyedManifest(t, "agent_version", "triage", "v3", func(m *readManifestV2) {
			m.AgentVersion = &readAgentSnapshot{Name: "Triage", Version: "v3",
				ArtifactRef: "registry.example/triage@sha256:" + strings.Repeat("a", 64)}
		}),
	}
	targets := map[string]string{}
	for i, m := range manifests {
		mr := MR{IID: int64(10 + i), SourceBranch: "aiops-promotion/" + m.PromotionID, MergeCommitSHA: strings.Repeat("c", 40)}
		for attempt := 0; attempt < 2; attempt++ {
			if err := s.saveKindImport(ctx, admin, mr, m); err != nil {
				t.Fatalf("%s import %d: %v", m.Kind, attempt, err)
			}
		}
		var targetID, auditApp, sourceApp, key string
		if err := st.DB.QueryRow(ctx, `SELECT i.target_id,a.application_id,a.changes->>'source_application_id',a.changes->>'application_key'
 FROM v3_config_imports i JOIN management_audit a ON a.target=i.target_id AND a.action='v3.config_import.completed'
 WHERE i.mr_iid=$1`, mr.IID).Scan(&targetID, &auditApp, &sourceApp, &key); err != nil {
			t.Fatalf("%s ledger: %v", m.Kind, err)
		}
		if auditApp != "app-prod" || sourceApp != "app-src" || key != "checkout-api" {
			t.Fatalf("%s audit: app=%s source=%s key=%s", m.Kind, auditApp, sourceApp, key)
		}
		targets[m.Kind] = targetID
		raw, _ := json.Marshal(m)
		if err := s.verifyImportedKind(ctx, raw, mr, m.Kind, targetID, "admin"); err != nil {
			t.Fatalf("%s read-side verification: %v", m.Kind, err)
		}
	}
	for _, check := range []struct{ name, query, id string }{
		{"runbook", `SELECT application_id FROM knowledge_documents WHERE id=$1`, targets["runbook"]},
		{"alert rule", `SELECT application_id FROM v3_config_alert_rules WHERE id=$1`, targets["alert_rule"]},
		{"agent", `SELECT application_id FROM v3_agents WHERE id=$1`, "triage"},
	} {
		var app string
		if err := st.DB.QueryRow(ctx, check.query, check.id).Scan(&app); err != nil || app != "app-prod" {
			t.Fatalf("%s target application: %q %v", check.name, app, err)
		}
	}
	for _, decision := range access.decisions {
		if decision.Module == "agents" && decision.ApplicationID != "app-prod" {
			t.Fatalf("Agent import authorized another application: %+v", decision)
		}
	}

	// A key prod has not registered, a key without prod, and an old manifest
	// without a key are refused with what to do.
	for name, c := range map[string]struct {
		set  func(*readManifestV2)
		want error
	}{
		"unknown key":       {func(m *readManifestV2) { m.ApplicationKey = "payments" }, errApplicationNotRegistered},
		"key without prod":  {func(m *readManifestV2) { m.ApplicationKey = "test-only" }, errApplicationNotRegistered},
		"schema 2 manifest": {func(m *readManifestV2) { m.Schema, m.ApplicationKey = readManifestVersion, "" }, errReexportRequired},
	} {
		m := keyedManifest(t, "agent_version", "triage-2", "v1", func(m *readManifestV2) {
			m.AgentVersion = &readAgentSnapshot{Name: "Triage 2", Version: "v1",
				ArtifactRef: "registry.example/triage@sha256:" + strings.Repeat("b", 64)}
			c.set(m)
		})
		mr := MR{IID: 90, SourceBranch: "aiops-promotion/" + m.PromotionID, MergeCommitSHA: strings.Repeat("d", 40)}
		if err := s.saveKindImport(ctx, admin, mr, m); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
