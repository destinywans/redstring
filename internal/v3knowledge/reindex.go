package v3knowledge

import (
	"errors"
	"net/http"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// reindexHandler retries a published Runbook's semantic generation under the
// V3 object ACL. A failed build leaves the document published and searchable by
// keyword; the response reports semantic readiness separately.
func (s *Service) reindexHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		if len(scopeIDs(r)) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment to reindex a Runbook")
			return
		}
		id := r.PathValue("id")
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		var doc knowledge.Document
		doc.ID = id
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(application_id,''),source,version,lifecycle,metadata
 FROM knowledge_documents WHERE id=$1 FOR SHARE`, id).
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
		if doc.Lifecycle != "published" {
			httpapi.Error(w, 409, "invalid_state", "Only a published Runbook can be reindexed")
			return
		}
		actor := httpapi.CurrentUser(r)
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'runbook.reindex.requested',$3,'requested',jsonb_build_object('environment_id',$4::text,'version',$5::int))`,
			actor.ID, doc.ApplicationID, id, scopeIDs(r)[0], doc.Version)
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		// The review transaction is complete. The version check prevents a
		// known stale request from indexing a later draft or changed scope.
		current, err := s.Knowledge.Repository.Get(r.Context(), id)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if current.Version != doc.Version || current.Lifecycle != "published" ||
			current.ApplicationID != doc.ApplicationID || !isRunbook(current.Source) ||
			!documentInScope(r, current) {
			httpapi.Error(w, 409, "document_changed", "Runbook changed before reindexing; retry with its current version")
			return
		}
		// The persisted object was authorized above by V3Policy. Reuse the
		// governed index adapters without the independent legacy operate ACL.
		indexer := knowledge.NewWithRepository(s.Knowledge.Repository, s.Knowledge.Embedding, s.Knowledge.Vectors)
		indexErr := indexer.Index(knowledge.WithPrincipal(r.Context(), actor.ID, actor.Role), id)
		status, ready := s.runbookIndexStatus(r, id, doc.Version, indexErr)
		httpapi.WriteJSON(w, 200, map[string]any{
			"id": id, "status": "published", "version": doc.Version,
			"index_status": status, "semantic_search_ready": ready, "index_retry_required": !ready,
		})
	}
}

func (s *Service) runbookIndexStatus(r *http.Request, id string, version int, indexErr error) (string, bool) {
	indexed, err := s.Knowledge.Repository.Get(r.Context(), id)
	if err != nil || indexed.Version != version || indexed.Lifecycle != "published" {
		return "unavailable", false
	}
	status := indexed.Status
	if indexErr != nil && status != "failed" {
		return "unavailable", false
	}
	if status != "ready" || indexErr != nil {
		return status, false
	}
	var active bool
	err = s.DB.QueryRow(r.Context(), `SELECT EXISTS (
 SELECT 1 FROM knowledge_documents d JOIN knowledge_generations g ON g.id=d.active_generation
 WHERE d.id=$1 AND d.version=$2 AND d.lifecycle='published'
 AND g.document_version=d.version AND g.status='ready')`, id, version).Scan(&active)
	if err != nil || !active {
		return "unavailable", false
	}
	return "ready", true
}
