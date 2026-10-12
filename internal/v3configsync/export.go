package v3configsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Attempt                      int64
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
		if auditErr := s.markExportUnknown(cleanupCtx, actor, record.ID, record.Attempt); auditErr != nil {
			return MR{}, "", auditErr
		}
		return MR{}, "", errUnavailable
	}
	if mr.IID <= 0 || mr.URL == "" || mr.SourceBranch != record.Branch ||
		mr.SourceProjectID != record.ProjectID || mr.TargetProjectID != record.ProjectID {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if auditErr := s.markExportUnknown(cleanupCtx, actor, record.ID, record.Attempt); auditErr != nil {
			return MR{}, "", auditErr
		}
		return MR{}, "", errUntrusted
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.finishExport(finalizeCtx, actor, record.ID, record.Attempt, mr); err != nil {
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
		return exportRecord{}, errTemplateDescription
	}
	template, err := exportableTemplate(kind, name, nodes, edges)
	if err != nil {
		return exportRecord{}, err
	}
	digest, err := templateDigest(template)
	if err != nil {
		return exportRecord{}, errTemplateStructure
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
	err = tx.QueryRow(ctx, `SELECT id,branch,path,project_id,payload,state,COALESCE(mr_iid,0),mr_url,attempt_generation
 FROM v3_config_exports WHERE kind='template' AND object_id=$1 AND environment_id=$2 AND source_digest=$3
 FOR UPDATE`, templateID, env, digest).
		Scan(&record.ID, &record.Branch, &record.Path, &record.ProjectID, &record.Payload,
			&record.State, &record.MR, &record.URL, &record.Attempt)
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
	if record.State == "exporting" {
		// Read the DB clock after acquiring the row lock. PostgreSQL now() is
		// fixed at transaction start, which may precede a long lock wait.
		var leaseActive bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false)
 FROM v3_config_exports WHERE id=$1`, record.ID).Scan(&leaseActive); err != nil {
			return exportRecord{}, err
		}
		if leaseActive {
			return exportRecord{}, errBusy
		}
	}
	err = tx.QueryRow(ctx, `UPDATE v3_config_exports SET state='exporting',
 attempt_generation=attempt_generation+1,lease_until=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp()
 WHERE id=$1 RETURNING attempt_generation`, record.ID).Scan(&record.Attempt)
	if err != nil {
		return exportRecord{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.config_export.requested',$2,'pending',jsonb_build_object(
 'environment_id',$3::text,'kind','template','source_id',$4::text,
 'source_digest',$5::text,'project_id',$6::bigint,'attempt_generation',$7::bigint))`, actor.ID, record.ID, env,
		templateID, digest, record.ProjectID, record.Attempt)
	if err != nil {
		return exportRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return exportRecord{}, err
	}
	return record, nil
}

func (s *Service) markExportUnknown(ctx context.Context, actor core.User, id string, attempt int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE v3_config_exports SET state='uncertain',lease_until=NULL,updated_at=now()
 WHERE id=$1 AND state='exporting' AND attempt_generation=$2`, id, attempt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.config_export.result_unknown',$2,'unknown',jsonb_build_object(
 'attempt_generation',$3::bigint))`, actor.ID, id, attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) finishExport(ctx context.Context, actor core.User, id string, attempt int64, mr MR) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	var currentAttempt int64
	err = tx.QueryRow(ctx, `SELECT state,attempt_generation FROM v3_config_exports WHERE id=$1 FOR UPDATE`, id).Scan(&state, &currentAttempt)
	if err != nil {
		return err
	}
	if state != "exporting" || currentAttempt != attempt {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE v3_config_exports SET state='exported',mr_iid=$2,mr_url=$3,
 lease_until=NULL,updated_at=now() WHERE id=$1 AND attempt_generation=$4`, id, mr.IID, mr.URL, attempt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.config_export.completed',$2,'success',jsonb_build_object(
 'project_id',$3::bigint,'mr_iid',$4::bigint,'attempt_generation',$5::bigint))`, actor.ID, id,
		s.Repository.ProjectID(), mr.IID, attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// privateTemplateMetadata is what V3 template saving records on the first
// node as private_template_v1: which parts the owner kept, the Runbooks it
// links on this instance, and the source canvas's services.
type privateTemplateMetadata struct {
	Steps          bool              `json:"steps,omitempty"`
	Content        bool              `json:"content,omitempty"`
	Runbooks       []json.RawMessage `json:"runbooks,omitempty"`
	SourceServices []string          `json:"source_services"`
}

// exportableTemplate reads a saved V3 template as the promotion manifest's
// structure (PA-96). Its private_template_v1 record is not part of the
// manifest: source_services are this environment's services, checked again
// against the target's when a canvas is created, and the steps and content
// flags only say what the owner chose to keep. What was actually kept is in
// the nodes, so retained titles, steps or content are refused, as are linked
// Runbooks, which name this instance's Runbook versions. Any other field is
// refused rather than dropped.
func exportableTemplate(kind, name string, rawNodes, rawEdges []byte) (Template, error) {
	template := Template{Kind: kind, Name: name}
	var nodes []json.RawMessage
	if decodeStrict(rawNodes, &nodes) != nil {
		return Template{}, errTemplateStructure
	}
	template.Nodes = make([]TemplateNode, 0, len(nodes))
	for i, raw := range nodes {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return Template{}, errTemplateStructure
		}
		if private, present := fields["private_template_v1"]; present {
			var metadata privateTemplateMetadata
			if i != 0 || decodeStrict(private, &metadata) != nil || metadata.SourceServices == nil {
				return Template{}, errTemplateStructure
			}
			if len(metadata.Runbooks) != 0 {
				return Template{}, errTemplateRunbooks
			}
			delete(fields, "private_template_v1")
			var err error
			if raw, err = json.Marshal(fields); err != nil {
				return Template{}, errTemplateStructure
			}
		}
		var node TemplateNode
		if decodeStrict(raw, &node) != nil {
			return Template{}, errTemplateStructure
		}
		template.Nodes = append(template.Nodes, node)
	}
	if decodeStrict(rawEdges, &template.Edges) != nil {
		return Template{}, errTemplateStructure
	}
	if !validTemplate(template) {
		for i, n := range template.Nodes {
			if n.Title != fmt.Sprintf("节点 %d", i+1) || n.Sub != "" || len(n.Blocks) != 0 {
				return Template{}, errTemplateContent
			}
		}
		return Template{}, errTemplateStructure
	}
	return template, nil
}
