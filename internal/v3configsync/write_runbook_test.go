package v3configsync

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type runbookPromotionAccess struct{ decisions []rbac.V3Decision }

func (a *runbookPromotionAccess) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	a.decisions = append(a.decisions, d)
	return nil
}
func (a *runbookPromotionAccess) AuthorizeWithTx(_ context.Context, _ pgx.Tx, _ core.User, d rbac.V3Decision) error {
	a.decisions = append(a.decisions, d)
	return nil
}

func TestRunbookSnapshotSanitizesContentAndRejectsUnsafeLabels(t *testing.T) {
	raw := []byte(`[{"n":1,"kind":"check","title":"Inspect metrics","body":"password=source-only",
"code":"curl -H 'Bearer source-only'","connectors":["private-connector"],
"tool":{"brand":"private","text":"source-only"},"readonly":true}]`)
	snapshot, err := sanitizeRunbookSnapshot("Inspect traffic", 3, "Error rate rises", raw)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 3 || len(snapshot.Steps) != 1 || !snapshot.Steps[0].Readonly {
		t.Fatalf("wrong sanitized configuration: %+v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"source-only", "private-connector", "Bearer", "code", "tool", "body"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Runbook promotion leaked %s: %s", forbidden, encoded)
		}
	}
	for _, altered := range []struct {
		title, when string
		steps       []byte
	}{
		{"password=123", "safe", raw}, {"safe", "https://secret.invalid", raw},
		{"safe", "safe", []byte(`[{"n":1,"kind":"check","title":"api_key=123"}]`)},
		{"safe", "safe", []byte(`[{"n":1,"kind":"check","title":"safe","unknown":"x"}]`)},
		{"safe", "safe", []byte(`[{"n":1,"kind":"check","title":"safe"},{"n":1,"kind":"check","title":"bad order"}]`)},
	} {
		if _, err := sanitizeRunbookSnapshot(altered.title, 1, altered.when, altered.steps); !errors.Is(err, errInvalidManifest) {
			t.Fatalf("unsafe Runbook snapshot unexpectedly accepted: %+v, %v", altered, err)
		}
	}
}

func prepareRunbookPromotionFixture(t *testing.T) *Service {
	t.Helper()
	st := isolatedConfigStore(t)
	ctx := t.Context()
	for _, sql := range []string{
		`ALTER TABLE knowledge_documents ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();`,
		`ALTER TABLE v3_runbooks ADD COLUMN source_ref text NOT NULL DEFAULT '';`,
		`ALTER TABLE v3_runbooks ADD COLUMN when_text text NOT NULL DEFAULT '';`,
		`CREATE TABLE knowledge_history(document_id text NOT NULL,version integer NOT NULL,actor_id text NOT NULL,operation text NOT NULL);`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-a-prod','app-a','prod','Prod');`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-a','test');`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-a','prod');`,
	} {
		if _, err := st.DB.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	a := &runbookPromotionAccess{}
	s := New(st.DB, a, &fakeRepository{})
	s.SourceInstanceID = "aiops-test"
	s.SigningKey = testSigningKey()
	s.TrustedSources = map[string]ed25519.PublicKey{"aiops-test": testSigningKey().Public().(ed25519.PublicKey)}
	return s
}

func runbookSignedManifest(t *testing.T, snapshot readRunbookSnapshot) readManifestV2 {
	t.Helper()
	m := readManifestV2{Schema: readManifestVersion, PromotionID: store.ID(), Kind: "runbook",
		SourceID: "source-runbook", SourceVersion: "3", SourceInstance: "aiops-test",
		SourceEnvironment: "test", ApplicationID: "app-a", Runbook: &snapshot}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	m.SourceDigest = hex.EncodeToString(digest[:])
	if err := m.sign(testSigningKey()); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunbookSourceCurrentRevisionAndProductionDraft(t *testing.T) {
	s := prepareRunbookPromotionFixture(t)
	ctx := t.Context()
	metadata := knowledge.Metadata{MaintainerID: "owner", TeamID: "team-ops", Origin: "human",
		ActorID: "owner", Visibility: "application", EnvironmentIDs: []string{"test"}}
	body := "# Inspect traffic\nprivate source content"
	doc := knowledge.Document{ID: "source-runbook", ApplicationID: "app-a", Title: "Inspect traffic",
		Body: body, Source: "runbook://v3/source-runbook", Version: 3, ContentHash: store.Hash(body),
		Lifecycle: "published", Metadata: metadata, EvidenceIDs: []string{}}
	steps := []byte(`[{"n":1,"kind":"check","title":"Inspect metrics","body":"private source content",
"code":"private source command","connectors":["prometheus"],"readonly":true}]`)
	if _, err := s.DB.Exec(ctx, `INSERT INTO knowledge_documents
 (id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
 VALUES($1,$2,$3,$4,$5,$6,$7,'owner','published',$8::jsonb)`, doc.ID, doc.ApplicationID,
		doc.Title, doc.Body, doc.Source, doc.Version, doc.ContentHash, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by)
 VALUES($1,3,$2::jsonb,'owner')`, doc.ID, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_runbooks(document_id,source_kind,when_text,steps)
 VALUES($1,'blank','Error rate rises',$2::jsonb)`, doc.ID, steps); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	resolved, appID, err := s.resolveRunbookSnapshot(ctx, tx, core.User{ID: "owner"}, "test", doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if appID != "app-a" || resolved.Title != doc.Title || resolved.Version != 3 ||
		len(resolved.Steps) != 1 || resolved.Steps[0].Title != "Inspect metrics" {
		t.Fatalf("wrong source snapshot: %+v %q", resolved, appID)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	decisions := s.Access.(*runbookPromotionAccess).decisions
	if len(decisions) != 2 || decisions[0].Module != "knowledge" || decisions[0].Action != "manage" ||
		decisions[0].ApplicationID != "app-a" || decisions[1].TeamID != "team-ops" {
		t.Fatalf("Runbook ACL was not bound to application and team: %+v", decisions)
	}
	m := runbookSignedManifest(t, resolved)
	tx, err = s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	targetID, err := s.saveRunbookTarget(ctx, tx, core.User{ID: "admin"}, m)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if targetID != importedRunbookID(m) || s.verifyImportedRunbook(ctx, m, targetID) != nil {
		t.Fatal("new production draft lacks durable import evidence")
	}
	var importedBody, lifecycle string
	if err := s.DB.QueryRow(ctx, `SELECT body,lifecycle FROM knowledge_documents WHERE id=$1`, targetID).
		Scan(&importedBody, &lifecycle); err != nil || importedBody != "" || lifecycle != "draft" {
		t.Fatalf("imported Runbook must start as a nonexecutable draft: %q %q %v", importedBody, lifecycle, err)
	}
	var importedSteps []byte
	if err := s.DB.QueryRow(ctx, `SELECT steps FROM v3_runbooks WHERE document_id=$1`, targetID).Scan(&importedSteps); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(importedSteps), "private") || strings.Contains(string(importedSteps), "connectors") {
		t.Fatalf("imported Runbook carried source body or connector: %s", importedSteps)
	}
	// A later approved revision is still historically an imported Runbook.
	if _, err := s.DB.Exec(ctx, `UPDATE knowledge_documents SET version=2,lifecycle='published',body='Reviewed body'
 WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE v3_runbooks SET steps='[{"n":1,"kind":"check","title":"Reviewed"}]'::jsonb
 WHERE document_id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	if err := s.verifyImportedRunbook(ctx, m, targetID); err != nil {
		t.Fatalf("legitimate later revision erased import history: %v", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE knowledge_revisions SET snapshot=jsonb_set(snapshot,'{config_import,source_digest}',to_jsonb('bad'::text))
 WHERE document_id=$1 AND version=1`, targetID); err != nil {
		t.Fatal(err)
	}
	if err := s.verifyImportedRunbook(ctx, m, targetID); !errors.Is(err, errUntrusted) {
		t.Fatalf("tampered original import evidence accepted: %v", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE knowledge_documents SET version=4 WHERE id=$1`, doc.ID); err != nil {
		t.Fatal(err)
	}
	tx, err = s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.resolveRunbookSnapshot(ctx, tx, core.User{ID: "owner"}, "test", doc.ID)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("historical steps were guessed without a matching current revision: %v", err)
	}
}

func TestRunbookManifestIsStableAcrossMarshal(t *testing.T) {
	snapshot := readRunbookSnapshot{Title: "Inspect", Version: 3, When: "on alert",
		Steps: []readRunbookStep{{N: 1, Kind: "check", Title: "Inspect metrics", Readonly: true}}}
	m := runbookSignedManifest(t, snapshot)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := readPromotionSummary(raw, m.PromotionID,
		map[string]ed25519.PublicKey{"aiops-test": testSigningKey().Public().(ed25519.PublicKey)})
	if err != nil || !reflect.DeepEqual(summary, PromotionSummary{Kind: "runbook", Title: "Inspect",
		SourceID: "source-runbook", SourceVersion: "3", SourceInstance: "aiops-test",
		SourceEnvironment: "test", ApplicationID: "app-a", SourceDigest: m.SourceDigest}) {
		t.Fatalf("signed Runbook envelope did not survive round trip: %+v %v", summary, err)
	}
}
