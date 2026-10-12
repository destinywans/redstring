package v3configsync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// readAnyImport accepts only the exact reviewed single-file merge at its
// immutable commit. The manifest itself selects the importer, never the URL.
func (s *Service) readAnyImport(ctx context.Context, iid int64) (MR, []byte, PromotionSummary, error) {
	mr, path, raw, err := s.Repository.ReadMergedManifest(ctx, iid)
	if err != nil {
		return MR{}, nil, PromotionSummary{}, err
	}
	id := strings.TrimPrefix(mr.SourceBranch, s.Repository.BranchPrefix())
	if mr.IID != iid || mr.State != "merged" || !shaRE.MatchString(mr.MergeCommitSHA) ||
		mr.SourceBranch != s.Repository.BranchPrefix()+id || !promotionIDRE.MatchString(id) ||
		mr.SourceProjectID != s.Repository.ProjectID() || mr.TargetProjectID != s.Repository.ProjectID() {
		return MR{}, nil, PromotionSummary{}, errUntrusted
	}
	summary, err := readPromotionSummary(raw, id, s.TrustedSources)
	if err != nil {
		return MR{}, nil, PromotionSummary{}, errUntrusted
	}
	expectedPath, err := promotionPathForKind(summary.Kind, id)
	if err != nil || path != expectedPath {
		return MR{}, nil, PromotionSummary{}, errUntrusted
	}
	return mr, raw, summary, nil
}

// importTarget maps a verified manifest to this instance's application
// (PA-100). Instances register applications separately, so their IDs differ
// and only the key is common: production finds the active application with
// the manifest's application_key registered in prod. The returned copy names
// that application; the source's application ID stays in the manifest and
// the audit as evidence. A schema 2 manifest has no key and must be
// exported again.
func importTarget(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, m readManifestV2) (readManifestV2, error) {
	if m.Schema != readManifestKeyed || m.ApplicationKey == "" {
		return m, errReexportRequired
	}
	var app string
	err := q.QueryRow(ctx, `SELECT a.id FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='prod'
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key='prod' AND NOT ae.archived
 WHERE a.key=$1 AND NOT a.archived`, m.ApplicationKey).Scan(&app)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, errApplicationNotRegistered
	}
	if err != nil {
		return m, err
	}
	target := m
	target.ApplicationID = app
	return target, nil
}

func (s *Service) saveKindImport(ctx context.Context, actor core.User, mr MR, m readManifestV2) error {
	_, verifyErr := m.verify(m.PromotionID, s.TrustedSources)
	if m.Kind == "template" || verifyErr != nil ||
		mr.SourceBranch != s.Repository.BranchPrefix()+m.PromotionID {
		return errUntrusted
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, key := range []int64{752018992, 752018995, 752018998, 752018999} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return err
		}
	}
	var active string
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL
 AND source<>'settings_demo' FOR SHARE`, actor.ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	if err = s.authorizeGlobalManager(ctx, actor, "prod", tx); err != nil {
		return err
	}
	var existingSHA, existingID, existingKind, existingTarget, existingActor string
	err = tx.QueryRow(ctx, `SELECT merge_sha,promotion_id,kind,target_id,actor_id
 FROM v3_config_imports WHERE project_id=$1 AND mr_iid=$2 FOR UPDATE`, s.Repository.ProjectID(), mr.IID).
		Scan(&existingSHA, &existingID, &existingKind, &existingTarget, &existingActor)
	previous := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if previous && (existingSHA != mr.MergeCommitSHA || existingID != m.PromotionID || existingKind != m.Kind) {
		return store.ErrConflict
	}
	source := m
	if m, err = importTarget(ctx, tx, m); err != nil {
		return err
	}
	var targetID string
	switch m.Kind {
	case "runbook":
		targetID, err = s.saveRunbookTarget(ctx, tx, actor, m)
	case "alert_rule":
		targetID, err = s.saveAlertRuleTarget(ctx, tx, actor, m)
	case "agent_version":
		targetID, err = s.saveAgentVersionTarget(ctx, tx, actor, m)
	default:
		return errUntrusted
	}
	if err != nil {
		return err
	}
	if previous {
		if existingTarget != targetID {
			return store.ErrConflict
		}
		if err = s.verifyKindAuditWithTx(ctx, tx, mr, m, source.ApplicationID, targetID, existingActor); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_config_imports
 (id,project_id,mr_iid,merge_sha,promotion_id,kind,target_id,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, store.ID(), s.Repository.ProjectID(),
		mr.IID, mr.MergeCommitSHA, m.PromotionID, m.Kind, targetID, actor.ID)
	if err != nil {
		if importUniqueConflict(err, "v3_config_imports") {
			return store.ErrConflict
		}
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$4,'v3.config_import.completed',$2,'success',jsonb_build_object(
 'environment_id','prod','kind',$3::text,'application_id',$4::text,
 'project_id',$5::bigint,'mr_iid',$6::bigint,'merge_sha',$7::text,
 'promotion_id',$8::text,'source_instance',$9::text,'source_environment',$10::text,
 'source_id',$11::text,'source_version',$12::text,'source_digest',$13::text,
 'application_key',$14::text,'source_application_id',$15::text))`,
		actor.ID, targetID, m.Kind, m.ApplicationID, s.Repository.ProjectID(), mr.IID,
		mr.MergeCommitSHA, m.PromotionID, m.SourceInstance, m.SourceEnvironment,
		m.SourceID, m.SourceVersion, m.SourceDigest, m.ApplicationKey, source.ApplicationID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) verifyKindAuditWithTx(ctx context.Context, tx pgx.Tx, mr MR, m readManifestV2, sourceApp, targetID, actorID string) error {
	var exists bool
	err := tx.QueryRow(ctx, kindAuditSQL, actorID, targetID, m.Kind, m.ApplicationID,
		strconv.FormatInt(s.Repository.ProjectID(), 10), strconv.FormatInt(mr.IID, 10),
		mr.MergeCommitSHA, m.PromotionID, m.SourceInstance, m.SourceEnvironment,
		m.SourceID, m.SourceVersion, m.SourceDigest, m.ApplicationKey, sourceApp).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errUntrusted
	}
	return nil
}

const kindAuditSQL = `SELECT EXISTS(SELECT 1 FROM management_audit WHERE actor_id=$1
 AND action='v3.config_import.completed' AND target=$2 AND outcome='success'
 AND changes->>'environment_id'='prod' AND changes->>'kind'=$3
 AND changes->>'application_id'=$4 AND changes->>'project_id'=$5
 AND changes->>'mr_iid'=$6 AND changes->>'merge_sha'=$7
 AND changes->>'promotion_id'=$8 AND changes->>'source_instance'=$9
 AND changes->>'source_environment'=$10 AND changes->>'source_id'=$11
 AND changes->>'source_version'=$12 AND changes->>'source_digest'=$13
 AND changes->>'application_key'=$14 AND changes->>'source_application_id'=$15)`

func (s *Service) verifyImportedKind(ctx context.Context, raw []byte, mr MR, kind, targetID, actorID string) error {
	var m readManifestV2
	if !noDuplicateKeys(raw) || decodeStrict(raw, &m) != nil || m.Kind != kind {
		return errUntrusted
	}
	if _, err := m.verify(m.PromotionID, s.TrustedSources); err != nil {
		return errUntrusted
	}
	sourceApp := m.ApplicationID
	m, err := importTarget(ctx, s.DB, m)
	if err != nil {
		return errUntrusted
	}
	switch kind {
	case "runbook":
		err = s.verifyImportedRunbook(ctx, m, targetID)
	case "alert_rule":
		err = s.verifyImportedAlertRule(ctx, m, targetID)
	case "agent_version":
		err = s.verifyImportedAgentVersion(ctx, m, targetID)
	default:
		return errUntrusted
	}
	if err != nil {
		return err
	}
	var audited bool
	err = s.DB.QueryRow(ctx, kindAuditSQL, actorID, targetID, m.Kind, m.ApplicationID,
		strconv.FormatInt(s.Repository.ProjectID(), 10), strconv.FormatInt(mr.IID, 10),
		mr.MergeCommitSHA, m.PromotionID, m.SourceInstance, m.SourceEnvironment,
		m.SourceID, m.SourceVersion, m.SourceDigest, m.ApplicationKey, sourceApp).Scan(&audited)
	if err != nil {
		return err
	}
	if !audited {
		return fmt.Errorf("%w: import audit missing", errUntrusted)
	}
	return nil
}
