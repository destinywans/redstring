package v3knowledge

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) authorizeRunbookRead(r *http.Request, doc knowledge.Document) error {
	if doc.Lifecycle == "published" {
		return s.authorizeDocument(r, doc, "view")
	}
	if doc.Lifecycle != "draft" && doc.Lifecycle != "review" {
		return store.ErrForbidden
	}
	// An unapproved draft may contain source material from a private
	// conversation. Until publication it is visible only to its maintainer or
	// managers reviewing it, never to ordinary knowledge viewers.
	if doc.Lifecycle == "draft" && doc.Metadata.MaintainerID != httpapi.CurrentUser(r).ID {
		return store.ErrForbidden
	}
	return s.authorizeDocument(r, doc, "manage")
}

func (s *Service) authorizeRunbookReadWithTx(r *http.Request, tx pgx.Tx, doc knowledge.Document) error {
	if doc.Lifecycle == "published" {
		return s.authorizeDocumentWithTx(r, tx, doc, "view")
	}
	if doc.Lifecycle != "draft" && doc.Lifecycle != "review" {
		return store.ErrForbidden
	}
	if doc.Lifecycle == "draft" && doc.Metadata.MaintainerID != httpapi.CurrentUser(r).ID {
		return store.ErrForbidden
	}
	return s.authorizeDocumentWithTx(r, tx, doc, "manage")
}

type reviewInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

func (s *Service) reviewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		var in reviewInput
		if !httpapi.Decode(w, r, &in) {
			return
		}
		in.Note = strings.TrimSpace(in.Note)
		if (in.Decision != "approve" && in.Decision != "request_changes") || len([]rune(in.Note)) > 4000 ||
			(in.Decision == "request_changes" && in.Note == "") {
			httpapi.Error(w, 422, "invalid_review", "Select a review decision and explain requested changes")
			return
		}
		id := r.PathValue("id")
		if len(scopeIDs(r)) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment to review a Runbook")
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		var doc knowledge.Document
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(application_id,''),source,version,lifecycle,metadata
 FROM knowledge_documents WHERE id=$1 FOR UPDATE`, id).
			Scan(&doc.ApplicationID, &doc.Source, &doc.Version, &doc.Lifecycle, &doc.Metadata)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if !isRunbook(doc.Source) || !documentInScope(r, doc) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if err = s.authorizeDocumentWithTx(r, tx, doc, "manage"); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				httpapi.Error(w, 404, "not_found", "Runbook not found")
			} else {
				httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
			}
			return
		}
		actor := httpapi.CurrentUser(r).ID
		if doc.Metadata.MaintainerID == "" {
			httpapi.Error(w, 409, "maintainer_unknown", "Runbook maintainer must be established before review")
			return
		}
		if actor == doc.Metadata.MaintainerID {
			httpapi.Error(w, 403, "self_review_forbidden", "A different manager must review this Runbook")
			return
		}
		if doc.Lifecycle != "review" {
			httpapi.Error(w, 409, "invalid_state", "Only a submitted Runbook can be reviewed")
			return
		}
		next := "published"
		operation := "runbook.review.approved"
		if in.Decision == "request_changes" {
			next = "draft"
			operation = "runbook.review.changes_requested"
		}
		_, err = tx.Exec(r.Context(), `UPDATE knowledge_documents SET lifecycle=$2 WHERE id=$1`, id, next)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO v3_runbook_reviews(id,document_id,document_version,decision,reviewer_id,note)
 VALUES($1,$2,$3,$4,$5,$6)`, store.ID(), id, doc.Version, in.Decision, actor, in.Note)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO knowledge_history(document_id,version,actor_id,operation)
 VALUES($1,$2,$3,$4)`, id, doc.Version, actor, operation)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,'succeeded',jsonb_build_object('environment_id',$5::text,'version',$6::int,'decision',$7::text))`,
				actor, doc.ApplicationID, operation, id, scopeIDs(r)[0], doc.Version, in.Decision)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"id": id, "status": next, "version": doc.Version})
	}
}
