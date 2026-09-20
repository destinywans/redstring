package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

const selectDraft = `SELECT d.id,d.investigation_id,d.application_id,d.title,d.body,d.evidence_ids,d.created_by,d.created_at,COALESCE(d.confirmed_by,''),d.confirmed_at,COALESCE(d.document_id,''),d.origin,d.generation_run_id,d.generation_model FROM knowledge_drafts d `

func scanDraft(row pgx.Row) (Draft, error) {
	var d Draft
	e := row.Scan(&d.ID, &d.InvestigationID, &d.ApplicationID, &d.Title, &d.Body, &d.EvidenceIDs, &d.CreatedBy, &d.CreatedAt, &d.ConfirmedBy, &d.ConfirmedAt, &d.DocumentID, &d.Origin, &d.GenerationRunID, &d.GenerationModel)
	if errors.Is(e, pgx.ErrNoRows) {
		e = store.ErrNotFound
	}
	return d, e
}
func validateDraftEvidence(ctx context.Context, tx pgx.Tx, inv string, ids []string) error {
	var count int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM evidence e JOIN branches b ON b.id=e.branch_id WHERE b.investigation_id=$1 AND e.id=ANY($2::text[])`, inv, ids).Scan(&count); e != nil {
		return e
	}
	if count != len(ids) {
		return fmt.Errorf("%w: evidence must belong to this investigation", ErrInvalid)
	}
	return nil
}
func draftEvent(ctx context.Context, tx pgx.Tx, inv, kind string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO events(investigation_id,type,payload) VALUES($1,$2,$3)", inv, kind, b)
	return e
}
func (s *PGRepository) CreateDraft(ctx context.Context, inv string, in DraftInput, user string) (Draft, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Draft{}, e
	}
	defer tx.Rollback(ctx)
	var app string
	if e = tx.QueryRow(ctx, "SELECT application_id FROM investigations WHERE id=$1 FOR UPDATE", inv).Scan(&app); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = store.ErrNotFound
		}
		return Draft{}, e
	}
	if e = validateDraftEvidence(ctx, tx, inv, in.EvidenceIDs); e != nil {
		return Draft{}, e
	}
	id := store.ID()
	ids, e := json.Marshal(in.EvidenceIDs)
	if e != nil {
		return Draft{}, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO knowledge_drafts(id,investigation_id,application_id,title,body,evidence_ids,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)", id, inv, app, in.Title, in.Body, ids, user); e != nil {
		return Draft{}, e
	}
	if e = draftEvent(ctx, tx, inv, "knowledge.drafted", map[string]any{"draft_id": id, "evidence_ids": in.EvidenceIDs}); e != nil {
		return Draft{}, e
	}
	d, e := scanDraft(tx.QueryRow(ctx, selectDraft+" WHERE d.id=$1", id))
	if e != nil {
		return d, e
	}
	return d, tx.Commit(ctx)
}
func (s *PGRepository) ListDrafts(ctx context.Context, inv string) ([]Draft, error) {
	var exists bool
	if e := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM investigations WHERE id=$1)", inv).Scan(&exists); e != nil {
		return nil, e
	}
	if !exists {
		return nil, store.ErrNotFound
	}
	rows, e := s.DB.Query(ctx, selectDraft+" WHERE d.investigation_id=$1 ORDER BY d.created_at,d.id", inv)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Draft{}
	for rows.Next() {
		d, e := scanDraft(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *PGRepository) ConfirmDraft(ctx context.Context, id, user string) (string, bool, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return "", false, e
	}
	defer tx.Rollback(ctx)
	// Lock the investigation first, matching its event sequence serialization.
	var inv string
	if e = tx.QueryRow(ctx, "SELECT investigation_id FROM knowledge_drafts WHERE id=$1", id).Scan(&inv); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = store.ErrNotFound
		}
		return "", false, e
	}
	var status string
	if e = tx.QueryRow(ctx, "SELECT status FROM investigations WHERE id=$1 FOR UPDATE", inv).Scan(&status); e != nil {
		return "", false, e
	}
	d, e := scanDraft(tx.QueryRow(ctx, selectDraft+" WHERE d.id=$1 FOR UPDATE", id))
	if e != nil {
		return "", false, e
	}
	if d.DocumentID != "" {
		return d.DocumentID, false, tx.Commit(ctx)
	}
	if status != "closed" {
		return "", false, store.ErrConflict
	}
	if e = validateDraftEvidence(ctx, tx, inv, d.EvidenceIDs); e != nil {
		return "", false, e
	}
	document := store.ID()
	ids, e := json.Marshal(d.EvidenceIDs)
	if e != nil {
		return "", false, e
	}
	source := "investigation://" + inv + "/knowledge-drafts/" + id
	meta := Metadata{MaintainerID: user, Origin: d.Origin, ActorID: d.CreatedBy, GenerationRunID: d.GenerationRunID, GenerationModel: d.GenerationModel, EvidenceIDs: d.EvidenceIDs, Visibility: "application"}
	var environment string
	if err := tx.QueryRow(ctx, "SELECT coalesce(environment_id,'') FROM investigations WHERE id=$1", inv).Scan(&environment); err != nil {
		return "", false, err
	}
	if environment != "" {
		meta.EnvironmentIDs = []string{environment}
	}
	metaJSON, _ := json.Marshal(meta)
	if _, e = tx.Exec(ctx, "INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,investigation_id,evidence_ids,lifecycle,metadata) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9,'published',$10)", document, d.ApplicationID, d.Title, d.Body, source, store.Hash(d.Body), d.CreatedBy, inv, ids, metaJSON); e != nil {
		return "", false, e
	}
	doc, err := scanDoc(tx.QueryRow(ctx, selectDoc+" WHERE d.id=$1", document))
	if err != nil {
		return "", false, err
	}
	if err = saveRevision(ctx, tx, doc, user); err != nil {
		return "", false, err
	}
	if err = knowledgeAudit(ctx, tx, doc, user, "knowledge.experience_published"); err != nil {
		return "", false, err
	}
	if _, e = tx.Exec(ctx, "UPDATE knowledge_drafts SET document_id=$2,confirmed_by=$3,confirmed_at=now() WHERE id=$1", id, document, user); e != nil {
		return "", false, e
	}
	if e = draftEvent(ctx, tx, inv, "knowledge.confirmed", map[string]any{"draft_id": id, "document_id": document, "confirmed_by": user, "evidence_ids": d.EvidenceIDs}); e != nil {
		return "", false, e
	}
	return document, true, tx.Commit(ctx)
}

func (s *PGRepository) DraftApplication(ctx context.Context, kind, id string) (string, error) {
	query := "SELECT application_id FROM investigations WHERE id=$1"
	if kind == "draft" {
		query = "SELECT application_id FROM knowledge_drafts WHERE id=$1"
	}
	var app string
	err := s.DB.QueryRow(ctx, query, id).Scan(&app)
	if errors.Is(err, pgx.ErrNoRows) {
		err = store.ErrNotFound
	}
	return app, err
}
