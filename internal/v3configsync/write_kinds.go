package v3configsync

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

func normalizePromotionKind(value string) (string, bool) {
	switch value {
	case "template", "templates":
		return "template", true
	case "runbook", "runbooks":
		return "runbook", true
	case "alert_rule", "alert-rules":
		return "alert_rule", true
	case "agent_version", "agents":
		return "agent_version", true
	default:
		return "", false
	}
}

func promotionWriteAccess(kind string) (string, string, bool) {
	switch kind {
	case "runbook":
		return "knowledge", "manage", true
	case "alert_rule":
		return "alert_rules", "manage", true
	case "agent_version":
		return "agents", "change", true
	default:
		return "", "", false
	}
}

func (s *Service) exportKind(r *http.Request, kind, env, sourceID string) (MR, string, error) {
	ctx, actor := r.Context(), httpapi.CurrentUser(r)
	record, appID, err := s.reserveKindExport(ctx, actor, kind, env, sourceID)
	if err != nil {
		return MR{}, "", err
	}
	if record.State != "exported" {
		title := "Promote " + kind + " " + sourceID
		if len(title) > 180 {
			title = title[:180]
		}
		mr, exportErr := s.Repository.Export(ctx, record.Branch, record.Path, record.Payload, title)
		if exportErr != nil || mr.IID <= 0 || mr.URL == "" || mr.SourceBranch != record.Branch ||
			mr.SourceProjectID != record.ProjectID || mr.TargetProjectID != record.ProjectID {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if auditErr := s.markExportUnknown(cleanupCtx, actor, record.ID, record.Attempt); auditErr != nil {
				return MR{}, "", auditErr
			}
			if errors.Is(exportErr, ErrMRLinkMismatch) {
				return MR{}, "", ErrMRLinkMismatch
			}
			if exportErr != nil {
				return MR{}, "", errUnavailable
			}
			return MR{}, "", errUntrusted
		}
		finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err = s.finishExport(finalizeCtx, actor, record.ID, record.Attempt, mr); err != nil {
			return MR{}, "", err
		}
		record.MR, record.URL = mr.IID, mr.URL
	}
	if err = s.authorizeCurrentListSession(r); err != nil {
		return MR{}, "", err
	}
	if err = s.authorizeCurrentKindWrite(ctx, actor, kind, env, appID); err != nil {
		return MR{}, "", err
	}
	return MR{IID: record.MR, URL: record.URL}, record.Path, nil
}

// GitLab may take longer than a grant revocation or application move. Recheck
// the same application/team write scope that reserved the signed export before
// returning the MR to the caller.
func (s *Service) authorizeCurrentKindWrite(ctx context.Context, actor core.User, kind, env, appID string) error {
	module, action, ok := promotionWriteAccess(kind)
	if !ok {
		return errInvalid
	}
	var ownerTeam string
	err := s.DB.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived`, appID, env).Scan(&ownerTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrConflict
	}
	if err != nil {
		return err
	}
	decision := rbac.V3Decision{Module: module, Action: action, EnvironmentID: env, ApplicationID: appID}
	err = s.Access.Authorize(ctx, actor, decision)
	if errors.Is(err, store.ErrForbidden) && kind == "alert_rule" && ownerTeam != "" {
		decision.ApplicationID = ""
		decision.TeamID = ownerTeam
		err = s.Access.Authorize(ctx, actor, decision)
	}
	if err != nil {
		return err
	}
	if kind == "runbook" && ownerTeam != "" {
		decision.ApplicationID = ""
		decision.TeamID = ownerTeam
		return s.Access.Authorize(ctx, actor, decision)
	}
	return nil
}

func (s *Service) reserveKindExport(ctx context.Context, actor core.User, kind, env, sourceID string) (exportRecord, string, error) {
	if _, _, ok := promotionWriteAccess(kind); !ok {
		return exportRecord{}, "", errInvalid
	}
	var alertCandidate alertRuleCandidate
	if kind == "alert_rule" {
		if s.AlertRules == nil {
			return exportRecord{}, "", errKindSourceUnavailable
		}
		var err error
		alertCandidate, err = s.AlertRules.Resolve(ctx, env, sourceID)
		if err != nil {
			return exportRecord{}, "", err
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return exportRecord{}, "", err
	}
	defer tx.Rollback(ctx)
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return exportRecord{}, "", err
		}
	}
	var active string
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL
 AND source<>'settings_demo' FOR SHARE`, actor.ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return exportRecord{}, "", store.ErrForbidden
	}
	if err != nil {
		return exportRecord{}, "", err
	}
	m := readManifestV2{Schema: readManifestKeyed, Kind: kind, SourceID: sourceID,
		SourceInstance: s.SourceInstanceID, SourceEnvironment: env}
	switch kind {
	case "runbook":
		var snapshot readRunbookSnapshot
		snapshot, m.ApplicationID, err = s.resolveRunbookSnapshot(ctx, tx, actor, env, sourceID)
		m.Runbook = &snapshot
		m.SourceVersion = strconv.Itoa(snapshot.Version)
	case "agent_version":
		var snapshot readAgentSnapshot
		snapshot, m.ApplicationID, err = s.resolveAgentVersionSnapshot(ctx, tx, actor, env, sourceID)
		m.AgentVersion = &snapshot
		m.SourceVersion = snapshot.Version
	case "alert_rule":
		m.ApplicationID, m.AlertRule = alertCandidate.ApplicationID, &alertCandidate.Snapshot
		m.SourceVersion = alertCandidate.Snapshot.Version
	}
	if err != nil {
		return exportRecord{}, "", err
	}
	var ownerTeam string
	err = tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,''),COALESCE(a.key,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived`, m.ApplicationID, env).Scan(&ownerTeam, &m.ApplicationKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return exportRecord{}, "", store.ErrNotFound
	}
	if err != nil {
		return exportRecord{}, "", err
	}
	module, action, _ := promotionWriteAccess(kind)
	err = s.Access.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{Module: module, Action: action,
		EnvironmentID: env, ApplicationID: m.ApplicationID})
	if errors.Is(err, store.ErrForbidden) && kind == "alert_rule" && ownerTeam != "" {
		err = s.Access.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{Module: module, Action: action,
			EnvironmentID: env, TeamID: ownerTeam})
	}
	if err != nil {
		return exportRecord{}, "", err
	}
	if alertCandidate.Refused != nil {
		return exportRecord{}, "", alertCandidate.Refused
	}
	// Production finds the application by its key (PA-100).
	if !objectKeyRE.MatchString(m.ApplicationKey) {
		return exportRecord{}, "", errApplicationKeyMissing
	}
	snapshot, _, err := m.snapshot()
	if err != nil {
		return exportRecord{}, "", errInvalidManifest
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return exportRecord{}, "", err
	}
	sum := sha256.Sum256(encoded)
	m.SourceDigest = hex.EncodeToString(sum[:])
	m.PromotionID = store.ID()
	if err = m.sign(s.SigningKey); err != nil {
		return exportRecord{}, "", err
	}
	payload, err := json.Marshal(m)
	if err != nil || len(payload) > 128<<10 {
		return exportRecord{}, "", errInvalidManifest
	}
	path, err := promotionPathForKind(kind, m.PromotionID)
	if err != nil {
		return exportRecord{}, "", err
	}
	branch := s.Repository.BranchPrefix() + m.PromotionID
	// Export format 2 carries the application key (PA-100); an earlier
	// format-1 record of the same digest is history, not this export.
	_, err = tx.Exec(ctx, `INSERT INTO v3_config_exports
 (id,kind,object_id,environment_id,application_id,source_version,source_digest,payload,
 branch,path,project_id,state,actor_id,export_format)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,'reserved',$12,2)
 ON CONFLICT(kind,object_id,environment_id,source_digest,export_format) DO NOTHING`,
		m.PromotionID, kind, sourceID, env, m.ApplicationID, m.SourceVersion, m.SourceDigest,
		payload, branch, path, s.Repository.ProjectID(), actor.ID)
	if err != nil {
		return exportRecord{}, "", err
	}
	var record exportRecord
	var storedActor, storedApp, storedVersion, storedDigest string
	err = tx.QueryRow(ctx, `SELECT id,branch,path,project_id,payload,state,COALESCE(mr_iid,0),mr_url,
 attempt_generation,actor_id,COALESCE(application_id,''),COALESCE(source_version,''),source_digest
 FROM v3_config_exports WHERE kind=$1 AND object_id=$2 AND environment_id=$3 AND source_digest=$4
 AND export_format=2 FOR UPDATE`,
		kind, sourceID, env, m.SourceDigest).Scan(&record.ID, &record.Branch, &record.Path, &record.ProjectID,
		&record.Payload, &record.State, &record.MR, &record.URL, &record.Attempt,
		&storedActor, &storedApp, &storedVersion, &storedDigest)
	if err != nil {
		return exportRecord{}, "", err
	}
	trusted := map[string]ed25519.PublicKey{s.SourceInstanceID: s.SigningKey.Public().(ed25519.PublicKey)}
	summary, err := readPromotionSummary(record.Payload, record.ID, trusted)
	expectedPath, pathErr := promotionPathForKind(kind, record.ID)
	if err != nil || pathErr != nil || storedActor != actor.ID || storedApp != m.ApplicationID ||
		storedVersion != m.SourceVersion || storedDigest != m.SourceDigest ||
		summary.Kind != kind || summary.SourceID != sourceID || summary.SourceVersion != m.SourceVersion ||
		summary.SourceDigest != m.SourceDigest || summary.ApplicationID != m.ApplicationID ||
		summary.ApplicationKey != m.ApplicationKey ||
		summary.SourceInstance != s.SourceInstanceID || summary.SourceEnvironment != env ||
		record.ProjectID != s.Repository.ProjectID() || record.Branch != s.Repository.BranchPrefix()+record.ID ||
		record.Path != expectedPath {
		return exportRecord{}, "", store.ErrConflict
	}
	if record.State == "exported" {
		if record.MR <= 0 || record.URL == "" {
			return exportRecord{}, "", errUntrusted
		}
		if err = tx.Commit(ctx); err != nil {
			return exportRecord{}, "", err
		}
		return record, m.ApplicationID, nil
	}
	if record.State == "exporting" {
		var leaseActive bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false)
 FROM v3_config_exports WHERE id=$1`, record.ID).Scan(&leaseActive); err != nil {
			return exportRecord{}, "", err
		}
		if leaseActive {
			return exportRecord{}, "", errBusy
		}
	}
	err = tx.QueryRow(ctx, `UPDATE v3_config_exports SET state='exporting',
 attempt_generation=attempt_generation+1,lease_until=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp()
 WHERE id=$1 RETURNING attempt_generation`, record.ID).Scan(&record.Attempt)
	if err != nil {
		return exportRecord{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.config_export.requested',$3,'pending',jsonb_build_object(
 'environment_id',$4::text,'kind',$5::text,'source_id',$6::text,'source_version',$7::text,
 'source_digest',$8::text,'project_id',$9::bigint,'attempt_generation',$10::bigint))`,
		actor.ID, m.ApplicationID, record.ID, env, kind, sourceID, m.SourceVersion,
		m.SourceDigest, record.ProjectID, record.Attempt)
	if err != nil {
		return exportRecord{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return exportRecord{}, "", err
	}
	return record, m.ApplicationID, nil
}

func importedKindItem(r *http.Request, mr MR, summary PromotionSummary) SyncItem {
	sha := mr.MergeCommitSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}
	return SyncItem{ID: fmt.Sprintf("mr-%d", mr.IID), Kind: summary.Kind,
		KindLabel: configKindLabel(r, summary.Kind), Title: summary.Title,
		MR: fmt.Sprintf("!%d", mr.IID), Author: mr.Author.Name,
		Meta: msg(r, "已合并提交 ", "Merged commit ") + sha, Status: "imported"}
}
