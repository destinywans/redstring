package v3knowledge

import (
	"errors"
	"net/http"
	"strings"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type runbookEdit struct {
	ExpectedVersion int    `json:"expected_version"`
	Title           string `json:"title"`
	When            string `json:"when"`
	Steps           []Step `json:"steps"`
}

func (s *Service) editHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		envs := scopeIDs(r)
		if len(envs) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment for a Runbook")
			return
		}
		var in runbookEdit
		if !httpapi.Decode(w, r, &in) {
			return
		}
		in.Title = strings.TrimSpace(in.Title)
		bodyInput := createInput{Title: in.Title, Source: "blank", Steps: in.Steps, Status: "draft"}
		if in.ExpectedVersion < 1 || in.Steps == nil || len(in.When) > 16000 || strings.ContainsRune(in.When, 0) || validateCreate(bodyInput) != nil {
			httpapi.Error(w, 422, "invalid_runbook", "Provide a current version, title, and valid steps")
			return
		}
		ctx := r.Context()
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(ctx)
		actor := httpapi.CurrentUser(r).ID
		if err = lockRunbookReadSession(ctx, tx, r); err == nil {
			err = lockRunbookGrantSources(ctx, tx, actor)
		}
		if err != nil {
			runbookReadAuthorityError(w, err)
			return
		}
		var doc knowledge.Document
		doc.ID = r.PathValue("id")
		err = tx.QueryRow(ctx, `SELECT COALESCE(application_id,''),source,version,lifecycle,metadata,created_at,
 COALESCE(investigation_id,''),evidence_ids FROM knowledge_documents WHERE id=$1 FOR UPDATE`, doc.ID).Scan(
			&doc.ApplicationID, &doc.Source, &doc.Version, &doc.Lifecycle, &doc.Metadata, &doc.CreatedAt, &doc.InvestigationID, &doc.EvidenceIDs)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (!isRunbook(doc.Source) || !documentInScope(r, doc)) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if doc.Metadata.MaintainerID != actor {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if err = s.authorizeDocumentWithTx(r, tx, doc, "manage"); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				httpapi.Error(w, 404, "not_found", "Runbook not found")
			} else {
				runbookReadAuthorityError(w, err)
			}
			return
		}
		if doc.Lifecycle != "draft" || doc.Version != in.ExpectedVersion {
			httpapi.Error(w, 409, "runbook_changed", "Only the current draft revision can be edited")
			return
		}
		bodyInput.Steps = normalizeSteps(in.Steps)
		doc.Title = in.Title
		doc.Body = renderBody(bodyInput)
		if when := strings.TrimSpace(in.When); when != "" {
			doc.Body += "\n" + when + "\n"
		}
		doc.ContentHash = store.Hash(doc.Body)
		doc.Version++
		tag, err := tx.Exec(ctx, `UPDATE v3_runbooks SET when_text=$2,steps=$3,last_verified_at=NULL,updated_at=now() WHERE document_id=$1`, doc.ID, strings.TrimSpace(in.When), bodyInput.Steps)
		if err == nil && tag.RowsAffected() != 1 {
			structuredRunbookError(w, pgx.ErrNoRows)
			return
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE knowledge_documents SET title=$2,body=$3,content_hash=$4,version=$5 WHERE id=$1`, doc.ID, doc.Title, doc.Body, doc.ContentHash, doc.Version)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by) VALUES($1,$2,$3,$4)`, doc.ID, doc.Version, doc, actor)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,$2,$3,'v3.runbook.edited')`, doc.ID, doc.Version, actor)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'runbook.edited',$3,'succeeded',$4)`, actor, doc.ApplicationID, doc.ID,
				map[string]any{"environment_id": envs[0], "previous_version": in.ExpectedVersion, "version": doc.Version})
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"id": doc.ID, "version": doc.Version, "status": "draft"})
	}
}
