package v3knowledge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/store"
)

type structuredRunbook struct {
	SourceKind string
	SourceRef  string
	WhenText   string
	Steps      []Step
}

func readStructuredRunbook(ctx context.Context, tx pgx.Tx, id string) (structuredRunbook, error) {
	var record structuredRunbook
	err := tx.QueryRow(ctx, `SELECT source_kind,source_ref,when_text,steps
 FROM v3_runbooks WHERE document_id=$1`, id).
		Scan(&record.SourceKind, &record.SourceRef, &record.WhenText, &record.Steps)
	if record.Steps == nil {
		record.Steps = []Step{}
	}
	return record, err
}

func structuredRunbookError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 503, "structured_steps_unavailable", "Runbook has no structured V3 record")
		return
	}
	knowledgeError(w, err)
}

func lockRunbookReadSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	return store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID)
}

func runbookReadAuthorityError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	httpapi.Error(w, 503, "authorization_unavailable", "Runbook access could not be checked")
}

// Session and grant row locks serialize a response with logout and changes to
// the locked role grants while document and permission reads use one snapshot.
var errRunbookReadStart = errors.New("runbook read transaction could not start")

func (s *Service) beginRunbookRead(w http.ResponseWriter, r *http.Request, ctx context.Context) (pgx.Tx, bool) {
	tx, err := s.beginRunbookReadTx(r, ctx)
	if err != nil {
		if errors.Is(err, errRunbookReadStart) {
			knowledgeError(w, err)
		} else {
			runbookReadAuthorityError(w, err)
		}
		return nil, false
	}
	return tx, true
}

// Keep the reference projection and the full Runbook reads on the same
// session/grant locking policy. The caller owns rollback or commit.
func (s *Service) beginRunbookReadTx(r *http.Request, ctx context.Context) (pgx.Tx, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, errors.Join(errRunbookReadStart, err)
	}
	if err = lockRunbookReadSession(ctx, tx, r); err == nil {
		err = lockRunbookGrantSources(ctx, tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *Service) readableRunbook(w http.ResponseWriter, r *http.Request) (pgx.Tx, knowledge.Document, bool) {
	if !s.ready(w) {
		return nil, knowledge.Document{}, false
	}
	tx, ok := s.beginRunbookRead(w, r, r.Context())
	if !ok {
		return nil, knowledge.Document{}, false
	}
	var doc knowledge.Document
	doc.ID = r.PathValue("id")
	err := tx.QueryRow(r.Context(), `SELECT COALESCE(application_id,''),title,source,version,lifecycle,metadata
 FROM knowledge_documents WHERE id=$1`, doc.ID).
		Scan(&doc.ApplicationID, &doc.Title, &doc.Source, &doc.Version, &doc.Lifecycle, &doc.Metadata)
	if err != nil {
		_ = tx.Rollback(r.Context())
		knowledgeError(w, err)
		return nil, knowledge.Document{}, false
	}
	if !isRunbook(doc.Source) || !documentInScope(r, doc) {
		_ = tx.Rollback(r.Context())
		httpapi.Error(w, 404, "not_found", "Runbook not found")
		return nil, knowledge.Document{}, false
	}
	if err = s.authorizeRunbookReadWithTx(r, tx, doc); err != nil {
		_ = tx.Rollback(r.Context())
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
		}
		return nil, knowledge.Document{}, false
	}
	return tx, doc, true
}

// The record route exposes only fields backed by a persisted document. The
// full Runbook DTO still needs complete usage and resolution event producers.
func (s *Service) recordHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tx, doc, ok := s.readableRunbook(w, r)
		if !ok {
			return
		}
		defer tx.Rollback(r.Context())
		record, err := readStructuredRunbook(r.Context(), tx, doc.ID)
		if err != nil {
			structuredRunbookError(w, err)
			return
		}
		if err = lockRunbookReadSession(r.Context(), tx, r); err != nil {
			runbookReadAuthorityError(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			knowledgeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{
			"id": doc.ID, "title": doc.Title, "version": "v" + strconv.Itoa(doc.Version),
			"status": doc.Lifecycle, "owner": doc.Metadata.MaintainerID,
			"application_id": doc.ApplicationID, "environment_ids": doc.Metadata.EnvironmentIDs,
			"team_id": doc.Metadata.TeamID, "tags": nonnilStrings(doc.Metadata.Tags),
			"when": record.WhenText, "steps": record.Steps, "source_kind": record.SourceKind,
			"partial": true, "unavailable_fields": []string{"usage", "incident_resolution", "execution_verification"},
		})
	}
}

func nonnilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

type historyRow struct {
	ID        string `json:"id,omitempty"`
	Version   int    `json:"version"`
	ActorID   string `json:"actor_id"`
	Decision  string `json:"decision,omitempty"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

type validationRow struct {
	ID            string           `json:"id"`
	Version       int              `json:"version"`
	ActorID       string           `json:"actor_id"`
	StepCount     int              `json:"step_count"`
	ReadonlyValid bool             `json:"readonly_valid"`
	Checks        []map[string]any `json:"checks"`
	CreatedAt     string           `json:"created_at"`
}

func (s *Service) historyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tx, doc, ok := s.readableRunbook(w, r)
		if !ok {
			return
		}
		defer tx.Rollback(r.Context())
		versions := []historyRow{}
		reviews := []historyRow{}
		validations := []validationRow{}
		includeReviewNotes := doc.Metadata.MaintainerID == httpapi.CurrentUser(r).ID
		if !includeReviewNotes {
			manageErr := s.authorizeDocumentWithTx(r, tx, doc, "manage")
			if manageErr == nil {
				includeReviewNotes = true
			} else if !errors.Is(manageErr, store.ErrForbidden) {
				httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
				return
			}
		}
		rows, err := tx.Query(r.Context(), `SELECT version,created_by,created_at
 FROM knowledge_revisions WHERE document_id=$1 ORDER BY version DESC LIMIT 101`, doc.ID)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		for rows.Next() {
			var row historyRow
			var at time.Time
			if err = rows.Scan(&row.Version, &row.ActorID, &at); err != nil {
				break
			}
			row.CreatedAt = at.UTC().Format(time.RFC3339)
			versions = append(versions, row)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			knowledgeError(w, err)
			return
		}
		rows, err = tx.Query(r.Context(), `SELECT id,document_version,reviewer_id,decision,note,created_at
 FROM v3_runbook_reviews WHERE document_id=$1 ORDER BY created_at DESC,id DESC LIMIT 101`, doc.ID)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		for rows.Next() {
			var row historyRow
			var at time.Time
			if err = rows.Scan(&row.ID, &row.Version, &row.ActorID, &row.Decision, &row.Note, &at); err != nil {
				break
			}
			row.CreatedAt = at.UTC().Format(time.RFC3339)
			if !includeReviewNotes {
				row.Note = ""
			}
			reviews = append(reviews, row)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			knowledgeError(w, err)
			return
		}
		rows, err = tx.Query(r.Context(), `SELECT id,document_version,actor_id,step_count,readonly_valid,checks,created_at
 FROM v3_runbook_validation_previews WHERE document_id=$1 ORDER BY created_at DESC,id DESC LIMIT 101`, doc.ID)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		for rows.Next() {
			var row validationRow
			var at time.Time
			if err = rows.Scan(&row.ID, &row.Version, &row.ActorID, &row.StepCount, &row.ReadonlyValid, &row.Checks, &at); err != nil {
				break
			}
			row.CreatedAt = at.UTC().Format(time.RFC3339)
			validations = append(validations, row)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if len(versions) > 100 || len(reviews) > 100 || len(validations) > 100 {
			httpapi.Error(w, 413, "history_too_large", "Runbook history exceeds the unpaged response limit")
			return
		}
		if err = lockRunbookReadSession(r.Context(), tx, r); err != nil {
			runbookReadAuthorityError(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			knowledgeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{
			"id": doc.ID, "versions": versions, "reviews": reviews, "validation_previews": validations,
			"execution_verification_available": false,
		})
	}
}
