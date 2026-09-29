package v3configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type agentPromotionAccess struct {
	deny      bool
	decisions []rbac.V3Decision
}

func (a *agentPromotionAccess) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	a.decisions = append(a.decisions, d)
	if a.deny {
		return store.ErrForbidden
	}
	return nil
}

func (a *agentPromotionAccess) AuthorizeWithTx(_ context.Context, _ pgx.Tx, _ core.User, d rbac.V3Decision) error {
	a.decisions = append(a.decisions, d)
	if a.deny {
		return store.ErrForbidden
	}
	return nil
}

func TestSafeAgentArtifactRef(t *testing.T) {
	for _, ref := range []string{
		"registry.example/triage@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if !safeAgentArtifactRef(ref) {
			t.Errorf("expected safe: %q", ref)
		}
	}
	for _, ref := range []string{"registry.example/triage:v3", "oci://registry.example/triage:v3",
		"https://user:password@registry.example/image", "registry.example/agent#token",
		"https://user:password@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"registry.example/agent@sha256:bad", "registry.example/agent@@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if safeAgentArtifactRef(ref) {
			t.Errorf("expected rejected: %q", ref)
		}
	}
}

func installAgentPromotionTables(t *testing.T, s *store.Store, env string) {
	t.Helper()
	_, err := s.DB.Exec(t.Context(), `CREATE TABLE v3_agents (
 id text PRIMARY KEY,application_id text NOT NULL,environment_id text NOT NULL,
 name text NOT NULL,stable_version text,level text NOT NULL DEFAULT 'L0',
 paused boolean NOT NULL DEFAULT true,
 FOREIGN KEY (application_id,environment_id)
 REFERENCES v3_application_environments(application_id,environment_id));
CREATE TABLE v3_agent_versions (
 agent_id text NOT NULL REFERENCES v3_agents(id),version text NOT NULL,
	 artifact_ref text NOT NULL,created_by text NOT NULL,
	 PRIMARY KEY (agent_id,version))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(t.Context(), `INSERT INTO v3_application_environments(application_id,environment_id)
 VALUES('app-a',$1)`, env); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(t.Context(), `INSERT INTO application_environments(id,application_id,key,name)
 VALUES($1,'app-a',$2,$2) ON CONFLICT(id) DO NOTHING`, "app-a-"+env, env); err != nil {
		t.Fatal(err)
	}
}

func agentManifestForTest(t *testing.T, snapshot readAgentSnapshot) readManifestV2 {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return readManifestV2{Schema: 2, PromotionID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Kind: "agent_version", SourceID: "triage", SourceVersion: snapshot.Version,
		SourceInstance: "aiops-test", SourceEnvironment: "test", ApplicationID: "app-a",
		SourceDigest: hex.EncodeToString(sum[:]), AgentVersion: &snapshot}
}

func TestAgentVersionPromotionTarget(t *testing.T) {
	source := isolatedConfigStore(t)
	installAgentPromotionTables(t, source, "test")
	ctx := t.Context()
	_, err := source.DB.Exec(ctx, `INSERT INTO v3_agents(id,application_id,environment_id,name,stable_version)
 VALUES('triage','app-a','test','Triage','v3');
INSERT INTO v3_agent_versions(agent_id,version,artifact_ref,created_by)
 VALUES('triage','v3','registry.example/triage@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner')`)
	if err != nil {
		t.Fatal(err)
	}
	access := &agentPromotionAccess{deny: true}
	service := &Service{DB: source.DB, Access: access}
	tx, err := source.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err := service.resolveAgentVersionSnapshot(ctx, tx, core.User{ID: "owner"}, "test", "triage"); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("permission: got %v", err)
	}
	access.deny = false
	snapshot, appID, err := service.resolveAgentVersionSnapshot(ctx, tx, core.User{ID: "owner"}, "test", "triage")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Name != "Triage" || snapshot.Version != "v3" ||
		snapshot.ArtifactRef != "registry.example/triage@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || appID != "app-a" {
		t.Fatalf("unexpected source: %+v, %s", snapshot, appID)
	}
	if len(access.decisions) != 2 || access.decisions[1].Module != "agents" ||
		access.decisions[1].Action != "change" || access.decisions[1].ApplicationID != "app-a" ||
		access.decisions[1].EnvironmentID != "test" {
		t.Fatalf("unexpected source ACL: %+v", access.decisions)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	production := isolatedConfigStore(t)
	installAgentPromotionTables(t, production, "prod")
	prodAccess := &agentPromotionAccess{}
	importer := &Service{DB: production.DB, Access: prodAccess}
	manifest := agentManifestForTest(t, snapshot)
	importTx, err := production.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer importTx.Rollback(ctx)
	targetID, err := importer.saveAgentVersionTarget(ctx, importTx, core.User{ID: "admin"}, manifest)
	if err != nil || targetID != "triage@v3" {
		t.Fatalf("import: target=%q err=%v", targetID, err)
	}
	if err = importTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if len(prodAccess.decisions) != 1 || prodAccess.decisions[0].Action != "change" ||
		prodAccess.decisions[0].EnvironmentID != "prod" {
		t.Fatalf("unexpected production ACL: %+v", prodAccess.decisions)
	}
	var paused bool
	var level string
	var stable *string
	if err = production.DB.QueryRow(ctx, `SELECT paused,level,stable_version FROM v3_agents WHERE id='triage'`).
		Scan(&paused, &level, &stable); err != nil || !paused || level != "L0" || stable != nil {
		t.Fatalf("new production agent should be paused L0: paused=%t level=%q stable=%v err=%v", paused, level, stable, err)
	}
	if err = importer.verifyImportedAgentVersion(ctx, manifest, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err = production.DB.Exec(ctx, `INSERT INTO v3_config_imports
 (id,project_id,mr_iid,merge_sha,promotion_id,kind,target_id,actor_id)
 VALUES($1,1,42,$2,$3,'agent_version',$4,'admin')`, store.ID(), strings.Repeat("b", 40),
		manifest.PromotionID, targetID); err != nil {
		t.Fatal(err)
	}
	// A retry may see the same catalog version before the outer import ledger
	// is checked. It must converge without changing the target.
	retry, err := production.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Rollback(ctx)
	if repeated, retryErr := importer.saveAgentVersionTarget(ctx, retry, core.User{ID: "admin"}, manifest); retryErr != nil || repeated != targetID {
		t.Fatalf("retry: target=%q err=%v", repeated, retryErr)
	}
	if err = retry.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Later rollout is separate and must not make a real historical import vanish.
	if _, err = production.DB.Exec(ctx, `UPDATE v3_agents SET paused=false,stable_version='v3' WHERE id='triage'`); err != nil {
		t.Fatal(err)
	}
	if err = importer.verifyImportedAgentVersion(ctx, manifest, targetID); err != nil {
		t.Fatalf("verification after rollout: %v", err)
	}
	if _, err = production.DB.Exec(ctx, `UPDATE v3_agent_versions SET artifact_ref='registry.example/other:v3'
 WHERE agent_id='triage' AND version='v3'`); err != nil {
		t.Fatal(err)
	}
	if err = importer.verifyImportedAgentVersion(ctx, manifest, targetID); !errors.Is(err, errUntrusted) {
		t.Fatalf("tampered target: %v", err)
	}

	// A second promotion of the same version with a different reference cannot
	// overwrite or silently adopt the existing target version.
	changed := snapshot
	changed.ArtifactRef = "registry.example/other-new@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	changedManifest := agentManifestForTest(t, changed)
	again, err := production.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Rollback(ctx)
	if _, err = importer.saveAgentVersionTarget(ctx, again, core.User{ID: "admin"}, changedManifest); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflicting version: %v", err)
	}
}
