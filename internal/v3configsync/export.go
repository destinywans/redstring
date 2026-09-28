package v3configsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type exportRecord struct {
	ID, Branch, Path, State, URL string
	Payload                      []byte
	MR                           int64
	ProjectID                    int64
	Lease                        *time.Time
}

func (s *Service) exportTemplate(r *http.Request, env, templateID string) (MR, string, error) {
	ctx, actor := r.Context(), httpapi.CurrentUser(r)
	record, err := s.reserveExport(ctx, actor, env, templateID)
	if err != nil {
		return MR{}, "", err
	}
	if record.State == "exported" {
		return MR{IID: record.MR, URL: record.URL}, record.Path, nil
	}
	mr, err := s.Repository.Export(ctx, record.Branch, record.Path, record.Payload, "Promote canvas template "+templateID)
	if err != nil {
		// A timeout after a GitLab POST leaves an unknown remote result. Retrying
		// this same persisted branch/path lets the adapter reconcile before it
		// creates anything else. Do not claim a successful export here.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if auditErr := s.markExportUnknown(cleanupCtx, actor, record.ID); auditErr != nil {
			return MR{}, "", auditErr
		}
		return MR{}, "", errUnavailable
	}
	if mr.IID <= 0 || mr.URL == "" || mr.SourceBranch != record.Branch ||
		mr.SourceProjectID != record.ProjectID || mr.TargetProjectID != record.ProjectID {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if auditErr := s.markExportUnknown(cleanupCtx, actor, record.ID); auditErr != nil {
			return MR{}, "", auditErr
		}
		return MR{}, "", errUntrusted
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.finishExport(finalizeCtx, actor, record.ID, mr); err != nil {
		return MR{}, "", err
	}
	return mr, record.Path, nil
}

func (s *Service) reserveExport(ctx context.Context, actor core.User, env, templateID string) (exportRecord, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return exportRecord{}, err
	}
	defer tx.Rollback(ctx)
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return exportRecord{}, err
		}
	}
	var active string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, actor.ID).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exportRecord{}, store.ErrForbidden
		}
		return exportRecord{}, err
	}
	var owner, sourceEnv, kind, name, description string
	var nodes, edges []byte
	err = tx.QueryRow(ctx, `SELECT COALESCE(owner_id,''),COALESCE(environment_id,''),kind,name,
 description,nodes,edges FROM v3_canvas_templates WHERE id=$1 FOR SHARE`, templateID).
		Scan(&owner, &sourceEnv, &kind, &name, &description, &nodes, &edges)
	if err != nil {
		return exportRecord{}, err
	}
	if owner != actor.ID || sourceEnv != env {
		return exportRecord{}, store.ErrNotFound
	}
	if err = s.Access.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{
		Module: "canvas", Action: "change", EnvironmentID: env,
	}); err != nil {
		return exportRecord{}, err
	}
	// V3 private templates are immutable and contain only sanitized graph
	// structure. A nonempty description or richer node payload is outside the
	// export contract and must be rejected, not silently copied.
	if description != "" {
		return exportRecord{}, errInvalidManifest
	}
	template := Template{Kind: kind, Name: name}
	if err = decodeStrict(nodes, &template.Nodes); err != nil {
		return exportRecord{}, errInvalidManifest
	}
	if err = decodeStrict(edges, &template.Edges); err != nil {
		return exportRecord{}, errInvalidManifest
	}
	digest, err := templateDigest(template)
	if err != nil {
		return exportRecord{}, err
	}
	newID := store.ID()
	manifest := Manifest{Schema: manifestVersion, PromotionID: newID, Kind: "template",
		SourceID: templateID, SourceInstance: s.SourceInstanceID,
		SourceEnvironment: env, SourceDigest: digest, Template: template}
	if err = manifest.sign(s.SigningKey); err != nil {
		return exportRecord{}, err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return exportRecord{}, err
	}
	if len(payload) > 128<<10 {
		return exportRecord{}, errInvalidManifest
	}
	branch, path := s.Repository.BranchPrefix()+newID, promotionPath(newID)
	_, err = tx.Exec(ctx, `INSERT INTO v3_config_exports
 (id,kind,object_id,environment_id,source_digest,payload,branch,path,project_id,state,actor_id)
 VALUES($1,'template',$2,$3,$4,$5::jsonb,$6,$7,$8,'reserved',$9)
 ON CONFLICT(kind,object_id,environment_id,source_digest) DO NOTHING`,
		newID, templateID, env, digest, payload, branch, path, s.Repository.ProjectID(), actor.ID)
	if err != nil {
		return exportRecord{}, err
	}
	var record exportRecord
	err = tx.QueryRow(ctx, `SELECT id,branch,path,project_id,payload,state,COALESCE(mr_iid,0),mr_url,lease_until
 FROM v3_config_exports WHERE kind='template' AND object_id=$1 AND environment_id=$2 AND source_digest=$3
 FOR UPDATE`, templateID, env, digest).
		Scan(&record.ID, &record.Branch, &record.Path, &record.ProjectID, &record.Payload,
			&record.State, &record.MR, &record.URL, &record.Lease)
	if err != nil {
		return exportRecord{}, err
	}
	var stored Manifest
	if decodeStrict(record.Payload, &stored) != nil || stored.validate(record.ID) != nil {
		return exportRecord{}, store.ErrConflict
	}
	expected := manifest
	expected.PromotionID = record.ID
	storedUnsigned, _ := stored.signedBytes()
	expectedUnsigned, _ := expected.signedBytes()
	if record.ProjectID != s.Repository.ProjectID() || record.Branch != s.Repository.BranchPrefix()+record.ID ||
		record.Path != promotionPath(record.ID) || stored.Signature == "" ||
		!bytes.Equal(storedUnsigned, expectedUnsigned) {
		return exportRecord{}, store.ErrConflict
	}
	if record.State == "exported" {
		if record.MR <= 0 || record.URL == "" {
			return exportRecord{}, errUntrusted
		}
		if err = tx.Commit(ctx); err != nil {
			return exportRecord{}, err
		}
		return record, nil
	}
	if record.State == "exporting" && record.Lease != nil && record.Lease.After(now()) {
		return exportRecord{}, errBusy
	}
	_, err = tx.Exec(ctx, `UPDATE v3_config_exports SET state='exporting',lease_until=now()+interval '1 minute',updated_at=now()
 WHERE id=$1`, record.ID)
	if err != nil {
		return exportRecord{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.config_export.requested',$2,'pending',jsonb_build_object(
 'environment_id',$3::text,'kind','template','source_id',$4::text,
 'source_digest',$5::text,'project_id',$6::bigint))`, actor.ID, record.ID, env,
		templateID, digest, record.ProjectID)
	if err != nil {
		return exportRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return exportRecord{}, err
	}
	return record, nil
}

func (s *Service) markExportUnknown(ctx context.Context, actor core.User, id string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE v3_config_exports SET state='uncertain',lease_until=NULL,updated_at=now()
 WHERE id=$1 AND state='exporting'`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.config_export.result_unknown',$2,'unknown','{}'::jsonb)`, actor.ID, id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) finishExport(ctx context.Context, actor core.User, id string, mr MR) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM v3_config_exports WHERE id=$1 FOR UPDATE`, id).Scan(&state)
	if err != nil {
		return err
	}
	if state != "exporting" && state != "uncertain" {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE v3_config_exports SET state='exported',mr_iid=$2,mr_url=$3,
 lease_until=NULL,updated_at=now() WHERE id=$1`, id, mr.IID, mr.URL)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.config_export.completed',$2,'success',jsonb_build_object(
 'project_id',$3::bigint,'mr_iid',$4::bigint))`, actor.ID, id,
		s.Repository.ProjectID(), mr.IID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
