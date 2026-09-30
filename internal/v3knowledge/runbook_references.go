package v3knowledge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
)

// RunbookReference exposes only persisted identity and governance fields. It
// never carries body, steps, usage, execution results or inferred health.
type RunbookReference struct {
	ID               string
	Title            string
	EnvironmentID    string
	ApplicationID    string
	TeamID           string
	Version          int
	GovernanceStatus string
	DetailAvailable  bool
}

var ErrRunbookReferencesUnavailable = errors.New("runbook reference policy unavailable")

const referenceBatchSize = 256
const referenceReadTimeout = 30 * time.Second

const runbookReferenceColumns = `d.id,COALESCE(d.application_id,''),d.title,d.source,
 d.version,d.lifecycle,d.metadata,d.created_at`

// The SQL matcher and searchtext.Match use the same literal/pinyin rules.
// Preselecting the environment array limits the scan; the exact raw metadata,
// application, team, lifecycle and grant checks still run below.
const runbookReferenceListSQL = `SELECT ` + runbookReferenceColumns + `
 FROM knowledge_documents d
 WHERE d.source LIKE 'runbook://%'
 AND CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array'
   THEN d.metadata->'environment_ids' ?| $1::text[] ELSE false END
 AND ($2='' OR aiops_search_match(concat_ws(' ',d.id,d.title,
   COALESCE((SELECT string_agg(tag.value,' ' ORDER BY tag.position) FROM jsonb_array_elements_text(
     CASE WHEN jsonb_typeof(d.metadata->'tags')='array'
       THEN d.metadata->'tags' ELSE '[]'::jsonb END) WITH ORDINALITY AS tag(value,position)),'')),$2))
 AND ($3::timestamptz IS NULL OR (d.created_at,d.id)<($3::timestamptz,$4::text))
 ORDER BY d.created_at DESC,d.id DESC LIMIT 256`

const runbookReferenceByIDSQL = `SELECT ` + runbookReferenceColumns + `
 FROM knowledge_documents d
 WHERE d.id=$1 AND d.source LIKE 'runbook://%'`

// ListRunbookReferences returns at most visibleLimit authorized records.
func (s *Service) ListRunbookReferences(r *http.Request, q string, visibleLimit int) ([]RunbookReference, error) {
	if !s.referenceReady(r) || visibleLimit < 1 || visibleLimit > 101 {
		return nil, ErrRunbookReferencesUnavailable
	}
	ctx, cancel := context.WithTimeout(r.Context(), referenceReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	tx, err := s.beginRunbookReadTx(r, ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	result := make([]RunbookReference, 0, visibleLimit)
	var cursor *time.Time
	cursorID := ""
	for {
		rows, err := tx.Query(ctx, runbookReferenceListSQL, scopeIDs(r), q, cursor, cursorID)
		if err != nil {
			return nil, err
		}
		batch := make([]knowledge.Document, 0, referenceBatchSize)
		for rows.Next() {
			var doc knowledge.Document
			err = rows.Scan(&doc.ID, &doc.ApplicationID, &doc.Title, &doc.Source,
				&doc.Version, &doc.Lifecycle, &doc.Metadata, &doc.CreatedAt)
			if err != nil {
				break
			}
			batch = append(batch, doc)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, doc := range batch {
			if publicStatus(doc.Lifecycle) == "" || !documentInScope(r, doc) ||
				(q != "" && !referenceMatch(doc, q)) {
				continue
			}
			// Do not lock another user's hidden document while scanning.
			if _, err := s.authorizedRunbookEnvironmentsWithTx(r, tx, doc); errors.Is(err, store.ErrForbidden) {
				continue
			} else if err != nil {
				return nil, err
			}
			if err := lockRunbookReferenceDocument(ctx, tx, doc); err != nil {
				return nil, err
			}
			ref, err := s.authorizedRunbookReference(r, tx, doc)
			if errors.Is(err, store.ErrForbidden) {
				continue
			}
			if err != nil {
				return nil, err
			}
			result = append(result, ref)
			if len(result) == visibleLimit {
				if err := finishRunbookReferenceRead(ctx, r, tx); err != nil {
					return nil, err
				}
				return result, nil
			}
		}
		if len(batch) < referenceBatchSize {
			break
		}
		last := batch[len(batch)-1]
		cursor, cursorID = &last.CreatedAt, last.ID
	}
	if err := finishRunbookReferenceRead(ctx, r, tx); err != nil {
		return nil, err
	}
	return result, nil
}

// RunbookReferenceByID uses the same transaction and grants as the list. A
// missing or hidden ID has the same outward result.
func (s *Service) RunbookReferenceByID(r *http.Request, id string) (RunbookReference, error) {
	if !s.referenceReady(r) {
		return RunbookReference{}, ErrRunbookReferencesUnavailable
	}
	ctx, cancel := context.WithTimeout(r.Context(), referenceReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	tx, err := s.beginRunbookReadTx(r, ctx)
	if err != nil {
		return RunbookReference{}, err
	}
	defer tx.Rollback(ctx)
	var doc knowledge.Document
	err = tx.QueryRow(ctx, runbookReferenceByIDSQL, id).Scan(&doc.ID, &doc.ApplicationID,
		&doc.Title, &doc.Source, &doc.Version, &doc.Lifecycle, &doc.Metadata, &doc.CreatedAt)
	if err != nil {
		return RunbookReference{}, err
	}
	if publicStatus(doc.Lifecycle) == "" || !documentInScope(r, doc) {
		return RunbookReference{}, pgx.ErrNoRows
	}
	if _, err := s.authorizedRunbookEnvironmentsWithTx(r, tx, doc); errors.Is(err, store.ErrForbidden) {
		return RunbookReference{}, pgx.ErrNoRows
	} else if err != nil {
		return RunbookReference{}, err
	}
	if err := lockRunbookReferenceDocument(ctx, tx, doc); err != nil {
		return RunbookReference{}, err
	}
	ref, err := s.authorizedRunbookReference(r, tx, doc)
	if errors.Is(err, store.ErrForbidden) {
		return RunbookReference{}, pgx.ErrNoRows
	}
	if err != nil {
		return RunbookReference{}, err
	}
	if err := finishRunbookReferenceRead(ctx, r, tx); err != nil {
		return RunbookReference{}, err
	}
	return ref, nil
}

func (s *Service) referenceReady(r *http.Request) bool {
	if s == nil || s.DB == nil || s.Policy == nil || r == nil || httpapi.CurrentUser(r).ID == "" {
		return false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	return ok && len(scope.EnvironmentIDs) > 0
}

func referenceMatch(doc knowledge.Document, q string) bool {
	return searchtext.Match(strings.Join(append([]string{doc.ID, doc.Title}, doc.Metadata.Tags...), " "), q)
}

// Under RepeatableRead this lock fails serialization if the candidate's
// version, lifecycle or ownership was updated after the snapshot. Hidden
// candidates are never locked.
func lockRunbookReferenceDocument(ctx context.Context, tx pgx.Tx, doc knowledge.Document) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM knowledge_documents
 WHERE id=$1 AND version=$2 AND source LIKE 'runbook://%' FOR SHARE`, doc.ID, doc.Version).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	return err
}

func (s *Service) authorizedRunbookReference(r *http.Request, tx pgx.Tx, doc knowledge.Document) (RunbookReference, error) {
	if err := lockRunbookReferenceOwnership(r, tx, doc); err != nil {
		return RunbookReference{}, err
	}
	allowed, err := s.authorizedRunbookEnvironmentsWithTx(r, tx, doc)
	if err != nil {
		return RunbookReference{}, err
	}
	detailAvailable, err := runbookReferenceDetailAvailable(r, tx, doc.ID)
	if err != nil {
		return RunbookReference{}, err
	}
	return RunbookReference{ID: doc.ID, Title: doc.Title, EnvironmentID: allowed[0],
		ApplicationID: doc.ApplicationID, TeamID: doc.Metadata.TeamID,
		Version: doc.Version, GovernanceStatus: doc.Lifecycle,
		DetailAvailable: detailAvailable}, nil
}

func runbookReferenceDetailAvailable(r *http.Request, tx pgx.Tx, id string) (bool, error) {
	var documentID string
	err := tx.QueryRow(r.Context(), `SELECT document_id FROM v3_runbooks
 WHERE document_id=$1 FOR SHARE`, id).Scan(&documentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Lock the application owner and all selected mapping rows that the existing
// object-level policy will inspect. A concurrent ownership/version change
// becomes a transaction error rather than an authorized stale projection.
func lockRunbookReferenceOwnership(r *http.Request, tx pgx.Tx, doc knowledge.Document) error {
	if doc.ApplicationID == "" {
		return nil
	}
	var appID string
	err := tx.QueryRow(r.Context(), `SELECT id FROM applications WHERE id=$1 AND NOT archived FOR SHARE`, doc.ApplicationID).Scan(&appID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT e.id FROM v3_application_environments ve
 JOIN application_environments e ON e.application_id=ve.application_id
  AND e.key=ve.environment_id AND NOT e.archived
 WHERE ve.application_id=$1 AND ve.environment_id=ANY($2::text[])
 FOR SHARE OF ve,e`, doc.ApplicationID, scopeIDs(r))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return err
}

func finishRunbookReferenceRead(ctx context.Context, r *http.Request, tx pgx.Tx) error {
	if err := lockRunbookReadSession(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
