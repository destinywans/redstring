// Package memoryview exposes authoritative memory without creating a second
// memory store. Knowledge writes keep their existing version and ACL checks.
package memoryview

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/store"
)

type Service struct {
	Store     *store.Store
	Knowledge *knowledge.Service
}
type ContextSummary struct {
	ID                 string          `json:"id"`
	Title              string          `json:"title"`
	InvestigationID    string          `json:"investigation_id"`
	InvestigationTitle string          `json:"investigation_title"`
	ApplicationID      string          `json:"application_id"`
	EnvironmentID      string          `json:"environment_id"`
	ParentID           string          `json:"parent_id,omitempty"`
	ModelID            string          `json:"model_id"`
	Revision           int64           `json:"revision"`
	Messages           int             `json:"message_count"`
	CreatedAt          time.Time       `json:"created_at"`
	Provenance         json.RawMessage `json:"provenance"`
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/memory/documents", "viewer", func(w http.ResponseWriter, r *http.Request) {
		limit, e := pageLimit(r)
		if e != nil {
			fail(w, e)
			return
		}
		offset, e := pageOffset(r, limit)
		if e != nil {
			fail(w, e)
			return
		}
		scope, e := httpapi.ParseReadScope(r.URL.Query())
		if e != nil {
			fail(w, e)
			return
		}
		apps, e := api.VisibleApplicationIDs(r.Context(), httpapi.CurrentUser(r))
		if e != nil {
			fail(w, e)
			return
		}
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		kind := r.URL.Query().Get("kind")
		cursor := r.URL.Query().Get("cursor")
		if len(q) > 200 || len(cursor) > 200 {
			fail(w, store.ErrInvalidReference)
			return
		}
		// Apply access, applicability and search before the page boundary. The
		// knowledge overview's bounded List is not a pageable source of truth.
		args := []any{apps, scope.ApplicationIDs, scope.EnvironmentIDs, q, kind, r.URL.Query().Get("lifecycle")}
		// Keep totals and candidates on the same database snapshot. Document
		// content is still projected through Knowledge.Get with a fresh ACL check.
		tx, e := s.Store.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if e != nil {
			fail(w, e)
			return
		}
		defer tx.Rollback(r.Context())
		var total int64
		if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM ("+documentFilteredSQL+") matching", args...).Scan(&total); e != nil {
			fail(w, e)
			return
		}
		rows, e := tx.Query(r.Context(), documentIDsSQL, append(args, cursor, limit+1, offset)...)
		if e != nil {
			fail(w, e)
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				break
			}
			ids = append(ids, id)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			fail(w, e)
			return
		}
		_ = tx.Rollback(r.Context())
		next := ""
		if len(ids) > limit {
			ids = ids[:limit]
			next = ids[len(ids)-1]
		}
		items := []knowledge.Document{}
		for _, id := range ids {
			// Reuse the authoritative projection and recheck current access in
			// case a grant or document visibility changed after candidate selection.
			d, err := s.Knowledge.Get(r.Context(), id)
			if errors.Is(err, knowledge.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				fail(w, err)
				return
			}
			d.Body = ""
			items = append(items, d)
		}
		httpapi.WriteJSON(w, 200, map[string]any{"items": items, "next_cursor": next, "total": total})
	})
	api.Register("GET /api/v1/memory/contexts", "viewer", func(w http.ResponseWriter, r *http.Request) {
		limit, e := pageLimit(r)
		if e != nil {
			fail(w, e)
			return
		}
		offset, e := pageOffset(r, limit)
		if e != nil {
			fail(w, e)
			return
		}
		scope, e := httpapi.ParseReadScope(r.URL.Query())
		if e != nil {
			fail(w, e)
			return
		}
		apps, e := api.VisibleApplicationIDs(r.Context(), httpapi.CurrentUser(r))
		if e != nil {
			fail(w, e)
			return
		}
		q := r.URL.Query().Get("q")
		cursor := r.URL.Query().Get("cursor")
		if len(q) > 200 || len(cursor) > 200 {
			fail(w, store.ErrInvalidReference)
			return
		}
		args := []any{apps, scope.ApplicationIDs, scope.EnvironmentIDs, q}
		// Keep totals and candidates on the same database snapshot. Document
		// content is still projected through Knowledge.Get with a fresh ACL check.
		tx, e := s.Store.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if e != nil {
			fail(w, e)
			return
		}
		defer tx.Rollback(r.Context())
		var total int64
		if e = tx.QueryRow(r.Context(), "SELECT count(*)"+contextFilteredSQL, args...).Scan(&total); e != nil {
			fail(w, e)
			return
		}
		rows, e := tx.Query(r.Context(), `SELECT b.id,b.title,b.investigation_id,i.title,i.application_id,coalesce(i.environment_id,''),coalesce(b.parent_id,''),b.model_id,b.revision,jsonb_array_length(c.messages),c.created_at,c.provenance`+contextFilteredSQL+` AND ($5='' OR b.id<$5) ORDER BY b.id DESC LIMIT $6 OFFSET $7`, append(args, cursor, limit+1, offset)...)
		if e != nil {
			fail(w, e)
			return
		}
		defer rows.Close()
		items := []ContextSummary{}
		for rows.Next() {
			var v ContextSummary
			if e = rows.Scan(&v.ID, &v.Title, &v.InvestigationID, &v.InvestigationTitle, &v.ApplicationID, &v.EnvironmentID, &v.ParentID, &v.ModelID, &v.Revision, &v.Messages, &v.CreatedAt, &v.Provenance); e != nil {
				fail(w, e)
				return
			}
			items = append(items, v)
		}
		if e = rows.Err(); e != nil {
			fail(w, e)
			return
		}
		next := ""
		if len(items) > limit {
			items = items[:limit]
			next = items[len(items)-1].ID
		}
		httpapi.WriteJSON(w, 200, map[string]any{"items": items, "next_cursor": next, "total": total})
	})
	api.Register("GET /api/v1/memory/documents/{id}/references", "viewer", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Knowledge.Get(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		apps, e := api.VisibleApplicationIDs(r.Context(), httpapi.CurrentUser(r))
		if e != nil {
			fail(w, e)
			return
		}
		// Return recorded, exact hit references only. A retrieval hit is not proof
		// that the assistant used it in its conclusion; no inferred hit counters.
		rows, e := s.Store.DB.Query(r.Context(), `SELECT e.id,e.branch_id,b.investigation_id,i.title,e.created_at,e.content FROM evidence e JOIN branches b ON b.id=e.branch_id JOIN investigations i ON i.id=b.investigation_id WHERE i.application_id=ANY($1::text[]) AND e.source='knowledge://search' AND position($2 in e.content)>0 ORDER BY e.created_at DESC,e.id DESC LIMIT 100`, apps, d.ID)
		if e != nil {
			fail(w, e)
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, bid, inv, title, body string
			var at time.Time
			if e = rows.Scan(&id, &bid, &inv, &title, &at, &body); e != nil {
				fail(w, e)
				return
			}
			var result knowledge.SearchResult
			if json.Unmarshal([]byte(body), &result) != nil {
				continue
			}
			for _, hit := range result.Items {
				if hit.DocumentID == d.ID {
					items = append(items, map[string]any{"evidence_id": id, "branch_id": bid, "investigation_id": inv, "title": title, "created_at": at, "version": hit.Version, "chunk": hit.Chunk, "generation_id": hit.GenerationID})
					break
				}
			}
		}
		if e = rows.Err(); e != nil {
			fail(w, e)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"items": items, "coverage": "recent_recorded_retrieval_snapshots", "limit": 100})
	})
}

// Scope is applicability, not a substitute for the actor's application ACL.
// An unrestricted environment list on a document means all environments of its
// applicable applications, rather than all environments in the workspace.
const documentFilteredSQL = `
WITH selected AS (
 SELECT ARRAY(SELECT unnest($2::text[]) INTERSECT SELECT unnest($1::text[])) AS apps
)
SELECT d.id FROM knowledge_documents d CROSS JOIN selected
WHERE (d.application_id=ANY($1::text[]) OR d.metadata->>'visibility'='workspace'
 OR COALESCE(d.metadata->'shared_application_ids','[]'::jsonb) ?| $1::text[])
AND (COALESCE(cardinality($2::text[]),0)=0 OR
 (cardinality(selected.apps)>0 AND (d.application_id=ANY(selected.apps)
 OR d.metadata->>'visibility'='workspace'
 OR COALESCE(d.metadata->'shared_application_ids','[]'::jsonb) ?| selected.apps)))
AND (COALESCE(cardinality($3::text[]),0)=0 OR EXISTS (
 SELECT 1 FROM application_environments env
 WHERE env.id=ANY($3::text[]) AND env.application_id=ANY($1::text[])
 AND (COALESCE(cardinality($2::text[]),0)=0 OR env.application_id=ANY(selected.apps))
 AND (d.application_id=env.application_id OR d.metadata->>'visibility'='workspace'
 OR COALESCE(d.metadata->'shared_application_ids','[]'::jsonb) ? env.application_id)
 AND (COALESCE(jsonb_array_length(NULLIF(d.metadata->'environment_ids','null'::jsonb)),0)=0
 OR d.metadata->'environment_ids' ? env.id)
))
AND ($4='' OR position($4 in lower(d.title||' '||d.body||' '||d.source))>0)
AND ($5<>'experience' OR d.investigation_id IS NOT NULL)
AND ($5<>'runbook' OR d.investigation_id IS NULL)
AND ($6='' OR d.lifecycle=$6)`
const documentIDsSQL = documentFilteredSQL + `
AND ($7='' OR d.id<$7) ORDER BY d.id DESC LIMIT $8 OFFSET $9`
const contextFilteredSQL = ` FROM branches b JOIN investigations i ON i.id=b.investigation_id JOIN context_revisions c ON c.branch_id=b.id AND c.revision=b.revision WHERE i.application_id=ANY($1::text[]) AND (cardinality($2::text[])=0 OR i.application_id=ANY($2::text[])) AND (cardinality($3::text[])=0 OR i.environment_id=ANY($3::text[])) AND ($4='' OR position(lower($4) in lower(b.title||' '||i.title))>0)`

func pageLimit(r *http.Request) (int, error) {
	raw, exists := r.URL.Query()["limit"]
	if !exists {
		return 50, nil
	}
	if len(raw) != 1 {
		return 0, store.ErrInvalidReference
	}
	limit, e := strconv.Atoi(raw[0])
	if e != nil || limit < 1 || limit > 50 {
		return 0, store.ErrInvalidReference
	}
	return limit, nil
}

// Page mode reads the requested offset directly; omitted page retains cursor
// semantics. Never combine an offset with a cursor and silently skip records.
func pageOffset(r *http.Request, limit int) (int64, error) {
	raw, exists := r.URL.Query()["page"]
	if !exists {
		return 0, nil
	}
	if len(raw) != 1 || r.URL.Query().Get("cursor") != "" {
		return 0, store.ErrInvalidReference
	}
	page, e := strconv.ParseInt(raw[0], 10, 64)
	if e != nil || page < 1 || page > 1_000_000 {
		return 0, store.ErrInvalidReference
	}
	return (page - 1) * int64(limit), nil
}

func fail(w http.ResponseWriter, e error) {
	status := 503
	code := "memory_unavailable"
	switch {
	case errors.Is(e, store.ErrForbidden), errors.Is(e, knowledge.ErrForbidden):
		status = 403
		code = "forbidden"
	case errors.Is(e, store.ErrNotFound):
		status = 404
		code = "not_found"
	case errors.Is(e, store.ErrInvalidReference):
		status = 422
		code = "invalid_filter"
	}
	httpapi.Error(w, status, code, "Cannot read this memory scope")
}
