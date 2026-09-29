package v3configsync

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// Only a digest-pinned catalog reference can be promoted. Mutable tags could
// point at different bytes after MR review. The catalog itself accepts a wider
// syntax because it is an internal metadata source.
func safeAgentArtifactRef(ref string) bool {
	if !boundedText(ref, 500) || !artifactRE.MatchString(ref) ||
		strings.Contains(ref, "#") || strings.Contains(ref, "://") {
		return false
	}
	at := strings.IndexByte(ref, '@')
	if at <= 0 || strings.LastIndexByte(ref, '@') != at {
		return false
	}
	digest := strings.TrimPrefix(ref[at+1:], "sha256:")
	return digest != ref[at+1:] && hexDigestRE.MatchString(digest)
}

// resolveAgentVersionSnapshot captures only the currently stable catalog
// version. A catalog version is an artifact reference, not an attestation of
// the remote artifact bytes. Prompts, tools, rollout state, and credentials
// are intentionally absent from the signed promotion snapshot.
func (s *Service) resolveAgentVersionSnapshot(ctx context.Context, tx pgx.Tx, actor core.User, env, id string) (readAgentSnapshot, string, error) {
	if s == nil || s.Access == nil || tx == nil || !objectKeyRE.MatchString(id) ||
		!boundedText(env, 100) || env == "prod" || env == "all" {
		return readAgentSnapshot{}, "", errInvalid
	}
	var name, appID, version, artifactRef string
	err := tx.QueryRow(ctx, `SELECT a.name,a.application_id,a.stable_version,v.artifact_ref
 FROM v3_agents a
 JOIN v3_agent_versions v ON v.agent_id=a.id AND v.version=a.stable_version
 JOIN applications app ON app.id=a.application_id AND NOT app.archived
 JOIN v3_application_environments ae ON ae.application_id=a.application_id AND ae.environment_id=a.environment_id
 WHERE a.id=$1 AND a.environment_id=$2 AND a.stable_version IS NOT NULL
 FOR SHARE OF a,v,app,ae`, id, env).Scan(&name, &appID, &version, &artifactRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return readAgentSnapshot{}, "", store.ErrNotFound
	}
	if err != nil {
		return readAgentSnapshot{}, "", err
	}
	if err = s.Access.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{
		Module: "agents", Action: "change", EnvironmentID: env, ApplicationID: appID,
	}); err != nil {
		return readAgentSnapshot{}, "", err
	}
	snapshot := readAgentSnapshot{Name: name, Version: version, ArtifactRef: artifactRef}
	if !objectKeyRE.MatchString(appID) || !boundedText(name, 160) ||
		!versionKeyRE.MatchString(version) || !safeAgentArtifactRef(artifactRef) {
		return readAgentSnapshot{}, "", errInvalid
	}
	return snapshot, appID, nil
}

// saveAgentVersionTarget adds the reviewed catalog version in production.
// A new Agent starts paused at L0; an existing Agent keeps its rollout state.
// The caller owns the transaction, import ledger, success audit, and global
// administrator recheck. This helper never activates the imported version.
func (s *Service) saveAgentVersionTarget(ctx context.Context, tx pgx.Tx, actor core.User, m readManifestV2) (string, error) {
	if s == nil || s.Access == nil || tx == nil || m.Kind != "agent_version" || m.AgentVersion == nil {
		return "", errInvalidManifest
	}
	if _, err := m.validate(m.PromotionID); err != nil {
		return "", err
	}
	if !safeAgentArtifactRef(m.AgentVersion.ArtifactRef) {
		return "", errInvalidManifest
	}
	if err := s.Access.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{
		Module: "agents", Action: "change", EnvironmentID: "prod", ApplicationID: m.ApplicationID,
	}); err != nil {
		return "", err
	}
	// The same application must already be registered for production. A
	// promotion cannot invent a production application or environment binding.
	var appID string
	err := tx.QueryRow(ctx, `SELECT app.id FROM applications app
 JOIN v3_application_environments ae ON ae.application_id=app.id AND ae.environment_id='prod'
 JOIN application_environments configured ON configured.application_id=app.id
   AND configured.key='prod' AND NOT configured.archived
 WHERE app.id=$1 AND NOT app.archived FOR SHARE OF app,ae,configured`, m.ApplicationID).Scan(&appID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrConflict
	}
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agents(id,application_id,environment_id,name)
 VALUES($1,$2,'prod',$3) ON CONFLICT (id) DO NOTHING`, m.SourceID, appID, m.AgentVersion.Name)
	if err != nil {
		if importUniqueConflict(err, "v3_agents") {
			return "", store.ErrConflict
		}
		return "", err
	}
	var targetApp, targetEnv, targetName string
	err = tx.QueryRow(ctx, `SELECT application_id,environment_id,name FROM v3_agents
 WHERE id=$1 FOR UPDATE`, m.SourceID).Scan(&targetApp, &targetEnv, &targetName)
	if err != nil {
		return "", err
	}
	if targetApp != appID || targetEnv != "prod" || targetName != m.AgentVersion.Name {
		return "", store.ErrConflict
	}
	inserted, err := tx.Exec(ctx, `INSERT INTO v3_agent_versions(agent_id,version,artifact_ref,created_by)
 VALUES($1,$2,$3,$4) ON CONFLICT (agent_id,version) DO NOTHING`,
		m.SourceID, m.SourceVersion, m.AgentVersion.ArtifactRef, actor.ID)
	if err != nil {
		if importUniqueConflict(err, "v3_agent_versions") {
			return "", store.ErrConflict
		}
		return "", err
	}
	if inserted.RowsAffected() == 0 {
		var recorded bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_config_imports
 WHERE kind='agent_version' AND promotion_id=$1 AND target_id=$2)`,
			m.PromotionID, m.SourceID+"@"+m.SourceVersion).Scan(&recorded)
		if err != nil {
			return "", err
		}
		if !recorded {
			return "", store.ErrConflict
		}
	}
	var persistedRef string
	err = tx.QueryRow(ctx, `SELECT artifact_ref FROM v3_agent_versions
 WHERE agent_id=$1 AND version=$2 FOR SHARE`, m.SourceID, m.SourceVersion).Scan(&persistedRef)
	if err != nil {
		return "", err
	}
	if persistedRef != m.AgentVersion.ArtifactRef {
		return "", store.ErrConflict
	}
	return m.SourceID + "@" + m.SourceVersion, nil
}

// verifyImportedAgentVersion checks the exact catalog version represented by
// an import ledger row. Rollout state is intentionally omitted: a later,
// separately approved activation must not erase evidence of the prior import.
// The caller separately verifies the merge identity, ledger, and success audit.
func (s *Service) verifyImportedAgentVersion(ctx context.Context, m readManifestV2, targetID string) error {
	if s == nil || s.DB == nil || m.Kind != "agent_version" || m.AgentVersion == nil ||
		targetID != m.SourceID+"@"+m.SourceVersion {
		return errUntrusted
	}
	if _, err := m.validate(m.PromotionID); err != nil {
		return errUntrusted
	}
	var matched bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_agents a
 JOIN v3_agent_versions v ON v.agent_id=a.id AND v.version=$2
 JOIN applications app ON app.id=a.application_id AND NOT app.archived
 JOIN v3_application_environments ae ON ae.application_id=a.application_id AND ae.environment_id='prod'
 JOIN application_environments configured ON configured.application_id=a.application_id
   AND configured.key='prod' AND NOT configured.archived
 WHERE a.id=$1 AND a.environment_id='prod' AND a.application_id=$3
 AND a.name=$4 AND v.artifact_ref=$5)`, m.SourceID, m.SourceVersion,
		m.ApplicationID, m.AgentVersion.Name, m.AgentVersion.ArtifactRef).Scan(&matched)
	if err != nil {
		return err
	}
	if !matched {
		return errUntrusted
	}
	return nil
}
